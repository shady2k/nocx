package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// FSConn is the lease surface the file manager holds on a pooled SSH
// connection (spec §3, D3): a sibling of DiscoveryConn that exposes an SFTP
// subsystem instead of exec. It owns its OWN pooled reference — never the
// tab's — so closing the terminal that created it cannot drop the transport
// underneath an in-flight read, and it shares the tab's connection when the
// pool key matches (AD-4). The concrete implementation is *fsConn, returned
// by RealClient.FSConn; the interface exists so feature packages can fake
// the lease without a live connection.
//
// Release the lease with Close when the file manager stops; on connection
// loss the lease releases itself and Done closes.
//
// Cancellation is split, because pkg/sftp is split: exactly one public
// *Client method takes a context — ReadDirContext — so listing is natively
// cancellable and must use it. Every other call here (Stat, Lstat, RealPath,
// and the Open/Read/Close inside ReadFile) takes no context, and for those
// cancellation is closing: the lease closes the subsystem, which unblocks
// the call, and waits for the call to observe the close, so no goroutine
// from this lease outlives the operation.
type FSConn interface {
	// ReadDir lists the directory at path. Natively cancellable:
	// ReadDirContext issues repeated SSH_FXP_READDIR packets and checks ctx
	// on each one, so a cancelled ctx returns ctx.Err() without touching
	// the client. Like every call, it runs in the lease's bounded lane.
	ReadDir(ctx context.Context, path string) ([]os.FileInfo, error)
	// Stat returns the file info for path, following symlinks. Non-context:
	// a call wedged against a silent server is unblocked by closing the
	// lease, or killed by the lane's hard timeout (which poisons the lease).
	Stat(path string) (os.FileInfo, error)
	// Lstat returns the file info for path without following symlinks.
	Lstat(path string) (os.FileInfo, error)
	// ReadLink returns the target of the symbolic link at path — the link
	// text as the server stores it, not the resolved path: a broken link
	// still returns its target, which is what lets the file manager
	// distinguish "target missing" from "cannot read the link".
	// Non-context, like Stat/Lstat/RealPath: a call wedged against a
	// silent server is unblocked by closing the lease, or killed by the
	// lane's hard timeout (which poisons the lease).
	ReadLink(path string) (string, error)
	// RealPath resolves path to the server's canonical absolute form.
	RealPath(path string) (string, error)
	// ReadFile opens path and reads at most maxBytes bytes, reading one
	// byte past the bound internally so truncated reports whether more
	// data remains — a file that ends exactly at the bound is not
	// truncated. The read is buffered in memory; the caller chooses the
	// bound, and maxBytes <= 0 means fsReadCap. The whole
	// open-read-close sequence runs as ONE lane call, so a wedged read
	// cannot hold a slot forever: the lane's hard timeout closes and
	// poisons the client, which is what unblocks File.Read.
	ReadFile(ctx context.Context, path string, maxBytes int64) (data []byte, truncated bool, err error)
	// Done closes when the underlying connection shuts down: connection
	// loss, server close, keepalive failure. It does NOT close on Close: an
	// intentional stop while the connection is still shared must not read
	// as connection loss.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close releases this lease's pooled reference and closes the SFTP
	// subsystem — the only thing that unblocks a non-context call in
	// flight — then waits for the subsystem's reader to observe the close,
	// so no reader goroutine from this lease outlives Close. The
	// connection stays open for every other reference.
	Close() error
}

// FSConn errors. These are the SFTP half of the lease contract (spec §3, D3):
// a refused session, a refused subsystem and a lost connection are different
// facts and must map to different file-manager states.
var (
	// ErrFSSessionRefused is returned by FSConn when the server refused the
	// additional session channel — OpenSSH's MaxSessions 1, or policy. The
	// interactive shell holds the only channel; SFTP cannot run here.
	ErrFSSessionRefused = errors.New("ssh: sftp session refused")
	// ErrFSSubsystemRefused is returned by FSConn when the server refused
	// the sftp subsystem request itself: the host runs SSH but no SFTP
	// server (no sftp-server, a restricted shell, ForceCommand policy).
	ErrFSSubsystemRefused = errors.New("ssh: sftp subsystem refused")
	// ErrFSTimedOut is returned by FSConn when the remote accepted the
	// subsystem but never completed the version handshake within the
	// lease's hard timeout.
	ErrFSTimedOut = errors.New("ssh: sftp handshake timed out")
	// ErrFSLost is returned when the underlying connection shut down
	// before or during a call.
	ErrFSLost = errors.New("ssh: sftp connection lost")
	// ErrFSClosed is returned after the lease was released by Close.
	ErrFSClosed = errors.New("ssh: sftp lease closed")
	// ErrFSDead is returned once the lane's hard timeout has poisoned the
	// lease: the client was closed and its pooled reference released, so
	// nothing on this lease can ever succeed again. A poisoned lease is a
	// terminal state the caller can observe — not a silent retry loop.
	ErrFSDead = errors.New("ssh: sftp lease dead: hard timeout exceeded")
)

// fsLaneCap bounds concurrent in-flight SFTP calls per lease. One SFTP
// client multiplexes all its requests, so cancelling one request must not
// close the client out from under the others; the lane is what separates
// "this call is cancelled" (client stays healthy) from "the server is
// wedged" (the hard timeout poisons the whole lease).
const fsLaneCap = 4

// fsHardTimeout is the lane's backstop: a call that has not returned within
// it is presumed stuck against a non-replying server, and the lease is
// closed and poisoned — the only mechanism that actually unblocks a
// non-context call. The file manager's user-facing elapsed-time cap is
// enforced inside ReadDirContext by the caller's context; this is the
// operational limit that guarantees no slot is held forever.
const fsHardTimeout = 30 * time.Second

// fsReadCap bounds one ReadFile when the caller passes maxBytes <= 0. It is
// the transport ceiling, not product policy: the caller's bound can only
// lower it.
const fsReadCap = 2 << 20

// fsConn is the concrete FSConn. It holds its own pooled reference,
// released exactly once (by Close, the loss watcher, or poison), and the
// underlying connection closes when the LAST reference — tabs and leases
// alike — releases.
type fsConn struct {
	sess *gossh.Session // the SFTP session channel: closing it is the close-to-cancel mechanism
	sftp *sftp.Client   // the SFTP subsystem

	// done closes on transport shutdown (the loss signal); closed closes on
	// Close; dead closes when the lane's hard timeout poisons the lease.
	// Calls check dead, then closed, then done — sequentially, not in one
	// select: when several have fired, the lease's own state is the
	// deterministic answer.
	done   chan struct{}
	closed chan struct{}
	dead   chan struct{}

	release func()
	// releaseOnce drops the pool reference exactly once whichever path
	// fires first: Close, the loss watcher, or poison.
	releaseOnce sync.Once
	closeOnce   sync.Once
	poisonOnce  sync.Once

	// lostErr is written by the watcher before done closes, so reading it
	// after <-done is ordered by the channel close.
	lostErr error

	// lane is the bounded semaphore capping concurrent in-flight calls.
	lane chan struct{}
	// hardTimeout is the lane's backstop (fsHardTimeout in production;
	// shortened in tests).
	hardTimeout time.Duration
}

// newFSConn acquires an SFTP subsystem on the pooled connection: a fresh
// session channel, an accepted sftp subsystem request, and a completed
// version handshake. Every step is cancellable: the handshake runs in a
// goroutine over the session's pipes, and closing the session — the only
// handle the lease holds from the outside — is what unblocks it, so FSConn
// always returns within ctx or the hard timeout and never leaks a goroutine.
// On any failure the pooled reference is released before returning.
func newFSConn(client *gossh.Client, release func(), ctx context.Context) (*fsConn, error) {
	return newFSConnLane(client, release, ctx, fsHardTimeout)
}

func newFSConnLane(client *gossh.Client, release func(), ctx context.Context, hardTimeout time.Duration) (*fsConn, error) {
	openCtx, cancel := context.WithTimeout(ctx, hardTimeout)
	defer cancel()
	sess, sftpClient, err := openSFTPSubsystem(client, openCtx)
	if err != nil {
		release()
		return nil, err
	}

	c := &fsConn{
		sess:        sess,
		sftp:        sftpClient,
		done:        make(chan struct{}),
		closed:      make(chan struct{}),
		dead:        make(chan struct{}),
		release:     release,
		lane:        make(chan struct{}, fsLaneCap),
		hardTimeout: hardTimeout,
	}
	// One watcher per lease: gossh.Client.Wait returns when the transport
	// shuts down. Report loss and drop our reference so a dead entry cannot
	// linger behind an unreleased lease.
	go func() {
		c.lostErr = client.Wait()
		close(c.done)
		c.releaseOnce.Do(func() {
			if c.release != nil {
				c.release()
			}
		})
	}()
	return c, nil
}

// openSFTPSubsystem acquires an SFTP subsystem on client: a fresh session
// channel, an accepted sftp subsystem request, and a completed version
// handshake. Every step is cancellable: the handshake runs in a goroutine
// over the session's pipes, and closing the session — the only handle the
// caller holds from the outside — is what unblocks it, so the function
// always returns within ctx and never leaks a goroutine. On failure the
// session is closed and nil returned; the caller still owns the pooled
// reference and must release it. Both the FSConn lease and the
// helper-install lease acquire through here, so the negotiation and its
// refusal classification have one implementation.
func openSFTPSubsystem(client *gossh.Client, ctx context.Context) (*gossh.Session, *sftp.Client, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, nil, classifyFSConnectError(err)
	}
	// The pipes are the sftp client's wire endpoints; the session stays
	// with the lease so shutdown can close the channel itself. Closing only
	// the client would send EOF (CloseWrite) and then WAIT for the server
	// to close the channel — a non-replying server never does, so the
	// session is what close-to-cancel closes.
	pw, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, nil, err
	}
	pr, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, nil, err
	}
	if err := sess.RequestSubsystem("sftp"); err != nil {
		_ = sess.Close()
		// x/crypto/ssh returns exactly "ssh: subsystem request failed" when
		// the server replies false; anything else at this step is a
		// transport failure — the connection died before the request could
		// be answered. The shape is pinned by go.mod at v0.54.0 (mirrors
		// classifyExecError), and the partition is complete: on a healthy
		// connection a subsystem request can only be answered true or false.
		if err.Error() == "ssh: subsystem request failed" {
			return nil, nil, fmt.Errorf("%w: %v", ErrFSSubsystemRefused, err)
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrFSLost, err)
	}

	type openResult struct {
		client *sftp.Client
		err    error
	}
	resCh := make(chan openResult, 1)
	go func() {
		cl, err := sftp.NewClientPipe(pr, pw)
		resCh <- openResult{cl, err}
	}()

	// The version handshake can hang against a server that accepts the
	// subsystem and never answers INIT. Closing the session unblocks the
	// handshake goroutine, exactly as Close unblocks a wedged call; the
	// watcher fires on ctx.Done (including the caller's hard-timeout
	// deadline, so a Background ctx still cannot hang the acquisition
	// forever).
	watchDone := make(chan struct{})
	watchExit := make(chan struct{})
	go func() {
		defer close(watchExit)
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-watchDone:
		}
	}()

	var res openResult
	select {
	case res = <-resCh:
	case <-ctx.Done():
		_ = sess.Close()
		res = <-resCh // the close unblocked it; no goroutine outlives the call
	}
	close(watchDone)
	<-watchExit
	// The deadline is the deterministic answer whenever it fired, even if
	// the handshake result and the deadline became ready in the same
	// select: a client that completed at the same moment the deadline
	// fired is closed again, never handed out.
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = sess.Close()
		if res.client != nil {
			_ = res.client.Close()
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("%w: remote did not complete the sftp version handshake", ErrFSTimedOut)
		}
		return nil, nil, ctxErr
	}
	if res.err != nil {
		_ = sess.Close()
		if errors.Is(res.err, io.EOF) || errors.Is(res.err, io.ErrUnexpectedEOF) {
			// The server accepted the subsystem and then the transport
			// died before the version handshake completed.
			return nil, nil, fmt.Errorf("%w: %v", ErrFSLost, res.err)
		}
		return nil, nil, fmt.Errorf("ssh: sftp handshake: %w", res.err)
	}
	return sess, res.client, nil
}

func (c *fsConn) Done() <-chan struct{} { return c.done }

func (c *fsConn) LostErr() error {
	select {
	case <-c.done:
		return c.lostErr
	default:
		return nil
	}
}

// Close releases this lease's pooled reference and stops any call still in
// flight: the SFTP session channel is closed — which is what unblocks a
// non-context call wedged against a silent server — before the reference
// drops. The sftp client's own Close then waits for its reader goroutine to
// observe the channel close, so no reader from this lease outlives Close.
// Done is deliberately NOT closed: an intentional stop must not read as
// connection loss.
func (c *fsConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.sess.Close()
		_ = c.sftp.Close()
		c.releaseOnce.Do(func() {
			if c.release != nil {
				c.release()
			}
		})
	})
	return nil
}

// poison is the lane's hard-timeout response: the client and session are
// closed — the only thing that unblocks a non-context call — the pooled
// reference is released, and dead closes so every call, in flight and
// future, reports ErrFSDead. A poisoned lease is terminal; it never
// recovers and never retries.
func (c *fsConn) poison() {
	c.poisonOnce.Do(func() {
		close(c.dead)
		_ = c.sess.Close()
		_ = c.sftp.Close()
		c.releaseOnce.Do(func() {
			if c.release != nil {
				c.release()
			}
		})
	})
}

// run executes fn under the lease's bounded lane. It acquires a slot first
// (interruptible by ctx, dead, closed or done), then arms the hard-timeout
// watchdog: if fn has not returned within c.hardTimeout, the watchdog
// poisons the lease, which closes the subsystem and unblocks fn, and run
// reports ErrFSDead. The watchdog is joined before run returns, so no
// goroutine from this lease outlives the call.
func (c *fsConn) run(ctx context.Context, fn func() error) error {
	// State is checked before and inside the slot wait — sequentially, not
	// in one select: when several have fired, the lease's own state is the
	// deterministic answer.
	select {
	case <-c.dead:
		return ErrFSDead
	default:
	}
	select {
	case <-c.closed:
		return ErrFSClosed
	default:
	}
	select {
	case <-c.done:
		return ErrFSLost
	default:
	}
	select {
	case c.lane <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.dead:
		return ErrFSDead
	case <-c.closed:
		return ErrFSClosed
	case <-c.done:
		return ErrFSLost
	}
	defer func() { <-c.lane }()

	watchDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTimer(c.hardTimeout)
		defer t.Stop()
		select {
		case <-t.C:
			c.poison()
		case <-watchDone:
		}
	}()
	err := fn()
	close(watchDone)
	wg.Wait()
	select {
	case <-c.dead:
		return ErrFSDead
	default:
	}
	return err
}

// classify maps a call error to the lease's typed errors. The lease's own
// state is checked first because a select race may deliver the call error
// after dead/closed/done fired — the deterministic answer wins.
func (c *fsConn) classify(err error) error {
	select {
	case <-c.dead:
		return ErrFSDead
	default:
	}
	select {
	case <-c.closed:
		return ErrFSClosed
	default:
	}
	select {
	case <-c.done:
		return ErrFSLost
	default:
	}
	// pkg/sftp delivers ErrSSHFxConnectionLost to every in-flight request
	// when its reader observes the channel close — the client side of a
	// lost connection.
	if errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return fmt.Errorf("%w: %v", ErrFSLost, err)
	}
	return err
}

func (c *fsConn) ReadDir(ctx context.Context, path string) ([]os.FileInfo, error) {
	var out []os.FileInfo
	err := c.run(ctx, func() error {
		var err error
		out, err = c.sftp.ReadDirContext(ctx, path)
		return err
	})
	if err != nil {
		return nil, c.classify(err)
	}
	return out, nil
}

func (c *fsConn) Stat(path string) (os.FileInfo, error) {
	var out os.FileInfo
	err := c.run(context.Background(), func() error {
		var err error
		out, err = c.sftp.Stat(path)
		return err
	})
	if err != nil {
		return nil, c.classify(err)
	}
	return out, nil
}

func (c *fsConn) Lstat(path string) (os.FileInfo, error) {
	var out os.FileInfo
	err := c.run(context.Background(), func() error {
		var err error
		out, err = c.sftp.Lstat(path)
		return err
	})
	if err != nil {
		return nil, c.classify(err)
	}
	return out, nil
}

func (c *fsConn) ReadLink(path string) (string, error) {
	var out string
	err := c.run(context.Background(), func() error {
		var err error
		out, err = c.sftp.ReadLink(path)
		return err
	})
	if err != nil {
		return "", c.classify(err)
	}
	return out, nil
}

func (c *fsConn) RealPath(path string) (string, error) {
	var out string
	err := c.run(context.Background(), func() error {
		var err error
		out, err = c.sftp.RealPath(path)
		return err
	})
	if err != nil {
		return "", c.classify(err)
	}
	return out, nil
}

func (c *fsConn) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = fsReadCap
	}
	var data []byte
	truncated := false
	err := c.run(ctx, func() error {
		f, err := c.sftp.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		// ReadFull loops until the buffer is full or EOF, so the bound is
		// real even though a single File.Read may return short; the +1 byte
		// is how truncation is learned without a second round trip.
		// ReadFull returns io.EOF exactly when zero bytes were read — the
		// empty file — which is a successful zero-byte read, not an error.
		buf := make([]byte, maxBytes+1)
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		truncated = int64(n) > maxBytes
		if truncated {
			n = int(maxBytes)
		}
		data = buf[:n]
		return nil
	})
	if err != nil {
		return nil, false, c.classify(err)
	}
	return data, truncated, nil
}

// classifyFSConnectError maps a refused session channel open to the typed
// session error. OpenSSH reports "resource shortage" for MaxSessions and
// "administratively prohibited" for policy refusals; both mean SFTP cannot
// run here. Anything that is not a channel refusal is a transport failure:
// on a healthy connection an open is answered accept or reject, and
// OpenChannelError is the only reject shape x/crypto/ssh produces.
func classifyFSConnectError(err error) error {
	var ocErr *gossh.OpenChannelError
	if errors.As(err, &ocErr) {
		return fmt.Errorf("%w: %v", ErrFSSessionRefused, err)
	}
	return fmt.Errorf("%w: %v", ErrFSLost, err)
}

// FSConn acquires an owned lease on the pooled SSH connection for host,
// with an SFTP subsystem (spec §3, D3). It takes its OWN pooled reference —
// never the tab's — so closing the creating tab can never kill an in-flight
// read's connection underneath it, and the interactive session stays fully
// usable while the file manager reads on its own channel. Release the lease
// with Close when the file manager stops; on connection loss the lease
// releases itself and Done closes.
//
// The same connection configuration (credentials, keys, jump route) as a
// Connect to host is resolved and authorized: the lease is bound by the same
func (rc *RealClient) FSConn(ctx context.Context, host string, opts ...ConnectOption) (FSConn, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}
	// newFSConn returns a *fsConn; returning it directly would box a typed
	// nil into the FSConn interface on the error paths, and fc != nil would
	// lie. Split the multi-value return so an error yields a nil interface.
	fc, err := newFSConn(acq.client, func() { rc.pool.Release(acq.handle) }, ctx)
	if err != nil {
		return nil, err
	}
	return fc, nil
}
