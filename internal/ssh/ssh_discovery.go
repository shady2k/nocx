package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

// DiscoveryConn is the lease surface port discovery holds on a pooled SSH
// connection (spec §3): it owns its OWN pooled reference — never the tab's —
// and runs auxiliary execs on fresh sessions, so the user's interactive
// shell, its tty and its history are untouched, and discovery keeps working
// while a command is running. The concrete implementation is *discoveryConn,
// returned by RealClient.DiscoveryConn; the interface exists so feature
// packages can fake the lease without a live connection.
//
// Release the lease with Close when discovery stops; on connection loss the
// lease releases itself and Done closes.
type DiscoveryConn interface {
	// Exec runs cmd on a fresh auxiliary session over the pooled
	// connection, capturing stdout and stderr separately and returning the
	// remote exit status.
	//
	// Closing the session is what stops a remote exec: context cancellation
	// alone does not make Session.Run context-aware, so Exec closes the
	// session when ctx is done, when the transport dies, when the lease is
	// closed, or when the captured output hits its bound — and then waits
	// for Run to return, so no goroutine outlives the call.
	Exec(ctx context.Context, cmd string) (*ExecResult, error)
	// Done closes when the underlying connection shuts down: connection
	// loss, server close, keepalive failure. It does NOT close on Close: an
	// intentional stop while the connection is still shared must not read
	// as connection loss.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close releases this lease's pooled reference and stops any exec still
	// in flight. The connection stays open for every other reference — tabs
	// and other leases alike.
	Close() error
}

// ExecResult is the outcome of one auxiliary exec: the captured stdout and
// stderr, the remote exit status, and whether a capture bound was hit
// (Truncated — the output is not complete).
type ExecResult struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	Truncated  bool
}

// Exec errors. These are the exec half of the discovery contract (spec §3.1):
// a refused session, a refused exec and a lost connection are different
// facts and must map to different discovery states.
var (
	// ErrExecSessionRefused is returned by Exec when the server refused the
	// additional session channel — OpenSSH's MaxSessions 1, or policy. The
	// interactive shell holds the only channel; discovery cannot run here.
	ErrExecSessionRefused = errors.New("ssh: additional exec session refused")
	// ErrExecProhibited is returned when the server refused the exec
	// request itself (restricted shell, ForceCommand-style policy).
	ErrExecProhibited = errors.New("ssh: exec request refused")
	// ErrExecLost is returned when the underlying connection shut down
	// before or during the exec.
	ErrExecLost = errors.New("ssh: exec connection lost")
	// ErrExecClosed is returned after the lease was released by Close.
	ErrExecClosed = errors.New("ssh: discovery connection closed")
)

// errExecOutputCapped is returned by cappedBuffer.Write once a capture bound
// is hit. io.Copy inside x/crypto/ssh stops on the first writer error, which
// is what makes the bound real at the writer boundary instead of after the
// buffer has grown.
var errExecOutputCapped = errors.New("ssh: exec output exceeded capture bound")

// execOutputCap bounds one exec's captured stdout and stderr. A wedged or
// hostile remote cannot grow memory past this: the bound is enforced at the
// writer boundary, and Exec closes the session when it fires, so the remote
// cannot block forever on a full channel buffer either.
const execOutputCap = 64 << 10

// cappedBuffer is a bytes.Buffer that refuses writes past cap. On the first
// over-cap write it marks over and fires onCap once (non-blocking), then
// returns errExecOutputCapped so io.Copy stops reading.
type cappedBuffer struct {
	buf   bytes.Buffer
	cap   int
	over  bool
	onCap func()
}

func newCappedBuffer(cap int, onCap func()) *cappedBuffer {
	return &cappedBuffer{cap: cap, onCap: onCap}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.cap - b.buf.Len()
	if room <= 0 {
		b.markOver()
		return 0, errExecOutputCapped
	}
	if len(p) > room {
		n, _ := b.buf.Write(p[:room])
		b.markOver()
		return n, errExecOutputCapped
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) markOver() {
	if !b.over {
		b.over = true
		if b.onCap != nil {
			b.onCap()
		}
	}
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// discoveryConn is the concrete DiscoveryConn. A detector must NOT borrow
// the tab's pool reference — closing the tab that created it would kill an
// in-flight sample's connection underneath it. This lease holds its own
// reference, released exactly once (by Close or the loss watcher), and the
// underlying connection closes when the LAST reference — tabs and leases
// alike — releases.
type discoveryConn struct {
	client *gossh.Client

	// done closes on transport shutdown (the loss signal); closed closes on
	// Close. Exec fails after either, closed checked first: when the lease
	// was explicitly closed AND the connection died (closing the last ref
	// does both), the lease's own state is the deterministic answer.
	done   chan struct{}
	closed chan struct{}

	release func()
	// releaseOnce drops the pool reference exactly once whichever path
	// fires first: Close or the loss watcher.
	releaseOnce sync.Once
	closeOnce   sync.Once

	// lostErr is written by the watcher before done closes, so reading it
	// after <-done is ordered by the channel close.
	lostErr error

	// mu guards sessions: the set of auxiliary sessions currently
	// executing. Close closes every one of them before releasing the lease
	// — closing the auxiliary session is what stops the remote exec, so a
	// tab death mid-sample tears the remote probe down instead of leaving
	// it running on the far host.
	mu       sync.Mutex
	sessions map[*gossh.Session]struct{}
}

// newDiscoveryConn wires a lease. release drops this lease's pool reference
// (pool.Release is already idempotent per handle; the once guard keeps the
// watcher and Close from double-firing the callback).
func newDiscoveryConn(client *gossh.Client, release func()) *discoveryConn {
	dc := &discoveryConn{
		client:   client,
		done:     make(chan struct{}),
		closed:   make(chan struct{}),
		release:  release,
		sessions: make(map[*gossh.Session]struct{}),
	}
	// One watcher per lease: gossh.Client.Wait returns when the transport
	// shuts down. Report loss and drop our reference so a dead entry cannot
	// linger behind an unreleased lease.
	go func() {
		dc.lostErr = client.Wait()
		close(dc.done)
		dc.releaseOnce.Do(func() {
			if dc.release != nil {
				dc.release()
			}
		})
	}()
	return dc
}

func (c *discoveryConn) Done() <-chan struct{} { return c.done }

func (c *discoveryConn) LostErr() error {
	select {
	case <-c.done:
		return c.lostErr
	default:
		return nil
	}
}

// Close releases this lease's pooled reference and stops any exec still in
// flight: every auxiliary session is closed — which is what stops the remote
// exec — before the reference drops, so a tab death mid-sample tears the
// remote probe down instead of leaving it running on the far host.
func (c *discoveryConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.mu.Lock()
		for sess := range c.sessions {
			_ = sess.Close()
		}
		c.mu.Unlock()
		c.releaseOnce.Do(func() {
			if c.release != nil {
				c.release()
			}
		})
	})
	return nil
}

// Exec implements DiscoveryConn.Exec. See the interface doc for the
// cancellation contract.
func (c *discoveryConn) Exec(ctx context.Context, cmd string) (*ExecResult, error) {
	// The bound, before anything is opened (nocx-e4ir3). Discovery's probes
	// are short and ours, which is exactly what was true of the integration
	// command until it was 92 KiB. Refusing here keeps that true of the
	// probe added next, and gives the caller OUR named error rather than
	// the far side's: an over-long command dies in the remote execve at
	// MAX_ARG_STRLEN, reporting nothing a person can act on.
	if len(cmd) >= MaxRemoteCommandLen {
		return nil, fmt.Errorf("%w: %d bytes, bound %d", ErrCommandTooLong, len(cmd), MaxRemoteCommandLen)
	}
	// closed is checked before done — sequentially, not in one select: when
	// the lease was explicitly closed AND the connection died, a select
	// would pick between two ready cases at random.
	select {
	case <-c.closed:
		return nil, ErrExecClosed
	default:
	}
	select {
	case <-c.done:
		return nil, ErrExecLost
	default:
	}

	sess, err := c.client.NewSession()
	if err != nil {
		return nil, classifySessionOpenError(err)
	}
	// Register under the same mutex Close scans: a lease closed between the
	// initial check and here must close THIS session too.
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		_ = sess.Close()
		return nil, ErrExecClosed
	default:
	}
	c.sessions[sess] = struct{}{}
	c.mu.Unlock()
	defer func() {
		_ = sess.Close()
		c.mu.Lock()
		delete(c.sessions, sess)
		c.mu.Unlock()
	}()

	capped := make(chan struct{}, 1)
	onCap := func() {
		select {
		case capped <- struct{}{}:
		default:
		}
	}
	stdout := newCappedBuffer(execOutputCap, onCap)
	stderr := newCappedBuffer(execOutputCap, onCap)
	sess.Stdout = stdout
	sess.Stderr = stderr

	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(cmd) }()

	var runErr error
	var capFired bool
	select {
	case runErr = <-errCh:
	case <-capped:
		// The capture bound fired: stop the remote rather than let it block
		// forever on a full channel buffer, then wait for Run to observe it.
		capFired = true
		_ = sess.Close()
		runErr = <-errCh
	case <-ctx.Done():
		_ = sess.Close()
		<-errCh // Run has observed the close; no goroutine outlives Exec
		return nil, ctx.Err()
	case <-c.done:
		_ = sess.Close()
		<-errCh
		return nil, ErrExecLost
	case <-c.closed:
		_ = sess.Close()
		<-errCh
		return nil, ErrExecClosed
	}

	result := &ExecResult{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.over || stderr.over,
	}
	var exitErr *gossh.ExitError
	switch {
	case runErr == nil:
		return result, nil
	case errors.As(runErr, &exitErr):
		result.ExitStatus = exitErr.ExitStatus()
		return result, nil
	case errors.Is(runErr, errExecOutputCapped):
		return result, nil
	case capFired:
		// We closed the session ourselves, so whatever Run reports afterwards
		// is a consequence of that close and not a fact about the host. Which
		// error arrives is a race: the capture error if the copy goroutine
		// observed the bound first, ExitMissingError if our Close beat the
		// remote's exit-status message. Both mean the same thing — the output
		// was truncated on purpose — and the caller learns it from Truncated.
		return result, nil
	}
	return nil, classifyExecError(c, runErr, cmd)
}

// classifySessionOpenError maps a refused session channel open to the typed
// exec error. OpenSSH reports "resource shortage" for MaxSessions and
// "administratively prohibited" for policy refusals; both mean discovery
// cannot run here, but they are different facts about the host.
func classifySessionOpenError(err error) error {
	var ocErr *gossh.OpenChannelError
	if errors.As(err, &ocErr) {
		switch ocErr.Reason {
		case gossh.ResourceShortage:
			return fmt.Errorf("%w: %v", ErrExecSessionRefused, err)
		case gossh.Prohibited:
			return fmt.Errorf("%w: %v", ErrExecProhibited, err)
		}
	}
	return err
}

// classifyExecError maps a Run error that is not an exit status to a typed
// exec error, keeping the raw error for everything else. The lease's own
// state is checked first because a select race may deliver the run error
// after done or closed fired — the deterministic answer wins.
func classifyExecError(c *discoveryConn, err error, cmd string) error {
	select {
	case <-c.done:
		return ErrExecLost
	default:
	}
	select {
	case <-c.closed:
		return ErrExecClosed
	default:
	}
	// x/crypto/ssh returns exactly "ssh: command <cmd> failed" when the
	// server replies false to the exec request (session.go Start). There is
	// no typed sentinel; the shape is pinned by go.mod at v0.54.0.
	if err.Error() == "ssh: command "+cmd+" failed" {
		return fmt.Errorf("%w: %v", ErrExecProhibited, err)
	}
	return err
}

// DiscoveryConn acquires an owned lease on the pooled SSH connection for
// host, for port discovery (spec §3). It takes its OWN pooled reference —
// never the tab's — so closing the creating tab can never kill an in-flight
// sample's connection underneath it, and the interactive session stays fully
// usable while discovery runs on its own channel. Release the lease with
// Close when the detector stops; on connection loss the lease releases
// itself and Done closes.
//
// The same connection configuration (credentials, keys, jump route) as a
// Connect to host is resolved and authorized: discovery is bound by the same
// credential authorization as a tab, and shares the tab's connection when
// the pool key matches (AD-4).
func (rc *RealClient) DiscoveryConn(ctx context.Context, host string, opts ...ConnectOption) (DiscoveryConn, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}
	return newDiscoveryConn(acq.client, func() { rc.pool.Release(acq.handle) }), nil
}
