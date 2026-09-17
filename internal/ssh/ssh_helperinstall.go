package ssh

// The helper-install lease (remote-helper design D7, D20): a write-capable,
// purpose-specific lease the deploy package installs the helper through. It is
// the boundary ssh_uninstall.go already set — the raw *gossh.Client stays
// inside internal/ssh, and callers get purpose-specific capabilities — and
// it is why the install cannot reuse the shell-integration publisher: that
// publisher's manifest, generation, locking and foreign-root semantics
// belong to the shell bundle under ~/.nocx, while the helper has its own
// content-addressed layout and pruning rules (D7). One publisher serving
// both would couple two unrelated deployment protocols.
//
// # Where the SFTP stream comes from, since nocx-50w7p.3
//
// The lease no longer opens its own subsystem on a connection it dialed. The
// coordinator dials nothing (plan §1-§3): the LOCAL helper holds the pooled
// connection and opens the `sftp` subsystem on it, and the coordinator wraps
// the bytes it is handed. So the constructor takes a stream, and the pooled
// client this file used to take is gone from the path entirely.
//
// What does NOT change is everything below the acquisition, and that is the
// point of moving the transport rather than the consumer: the write surface,
// the mkdir+chmod and create+chmod rules, the close-to-cancel, the loss
// signal — all of it is the same code, tested by the same tests, over a stream
// that happens to arrive from another process.

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"sync"

	"github.com/pkg/sftp"
)

// HelperInstallConn is the write-capable lease the helper installer holds
// for the duration of one install: the SFTP write surface deploy.RemoteFS
// needs, plus SFTP-native home discovery and the lease lifecycle. Release
// the lease with Close; on connection loss the lease releases itself and
// Done closes.
type HelperInstallConn interface {
	// The write surface deploy.RemoteFS needs (D7): modes set at creation,
	// never left to the server's umask; lstat semantics throughout; no
	// path followed through a symlink.
	Lstat(path string) (iofs.FileInfo, error)
	Mkdir(path string, mode os.FileMode) error
	Create(path string, mode os.FileMode) (File, error)
	SyncDir(path string) error
	Rename(src, dst string) error
	Remove(path string) error
	ReadDir(path string) ([]iofs.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	// Home resolves the remote account's home directory, SFTP-native: the
	// server canonicalises "." — the SFTP session's starting directory is
	// the remote account's home — so no `echo $HOME` over exec is needed
	// and no remote command has to be allowed.
	Home() (string, error)
	// Done closes on transport shutdown: connection loss, server close,
	// keepalive failure. It does NOT close on Close: an intentional stop
	// must not read as connection loss.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close releases this lease. The stream is closed FIRST — the
	// close-to-cancel mechanism that unblocks a call wedged against a silent
	// server — so no call from this lease is in flight when it returns. Done
	// is deliberately NOT closed.
	Close() error
}

// helperInstallConn is the concrete HelperInstallConn. It holds the stream it
// was handed and the SFTP write primitives through the shared SFTPFS adapter —
// the same adapter the shell-integration publisher uses, so mkdir+chmod,
// create+chmod, rename, removal, fsync tolerance and status translation have
// one implementation (AD-8).
type helperInstallConn struct {
	*SFTPFS
	stream io.ReadWriteCloser
	sftp   *sftp.Client // the lifecycle owner; SFTPFS runs the operations

	done   chan struct{}
	closed chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	lostErr   error
}

// NewHelperInstallConn builds the write-capable lease over an SFTP stream that
// somebody else opened — in production the local helper, through `ssh.open`,
// which is what makes this machine's helper the only dialer.
//
// The version handshake runs inside ctx's bound, and the bound is enforced by
// CLOSING THE STREAM: pkg/sftp's constructor has no context-aware form, and a
// server that accepts the subsystem and then says nothing would otherwise hang
// the install for as long as the caller's patience lasted. Closing the stream
// unblocks the handshake goroutine, so this always returns within ctx or the
// hard timeout and never leaks one.
func NewHelperInstallConn(ctx context.Context, stream io.ReadWriteCloser) (HelperInstallConn, error) {
	if stream == nil {
		return nil, errors.New("ssh: helper install lease: no stream")
	}
	watch := &endWatch{inner: stream, onEnd: func(error) {}}
	c := &helperInstallConn{
		stream: watch,
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	watch.onEnd = func(err error) {
		c.mu.Lock()
		if c.lostErr == nil {
			c.lostErr = err
		}
		c.mu.Unlock()
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}

	openCtx, cancel := context.WithTimeout(ctx, fsHardTimeout)
	defer cancel()

	type handshake struct {
		client *sftp.Client
		err    error
	}
	ch := make(chan handshake, 1)
	go func() {
		client, err := sftp.NewClientPipe(watch, watch)
		ch <- handshake{client, err}
	}()
	select {
	case <-openCtx.Done():
		// Closing the stream is what unblocks the handshake; the goroutine's
		// send is buffered, so it cannot leak on a receiver that has left.
		_ = stream.Close()
		<-ch
		return nil, fmt.Errorf("ssh: helper install lease: %w", openCtx.Err())
	case hs := <-ch:
		if hs.err != nil {
			_ = stream.Close()
			return nil, hs.err
		}
		c.sftp = hs.client
		c.SFTPFS = NewSFTPFS(hs.client)
	}
	return c, nil
}

// endWatch reports the FIRST failure or end its stream produced, which is what
// gives the lease a loss signal without a *gossh.Client to wait on.
//
// Close is deliberately not an end: the interface says an intentional stop must
// not read as connection loss, and a lease that reported its own release as a
// fault would make every clean uninstall look like a dropped connection.
type endWatch struct {
	inner io.ReadWriteCloser
	once  sync.Once
	onEnd func(error)
}

func (w *endWatch) Read(p []byte) (int, error) {
	n, err := w.inner.Read(p)
	if err != nil {
		w.end(err)
	}
	return n, err
}

func (w *endWatch) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if err != nil {
		w.end(err)
	}
	return n, err
}

func (w *endWatch) Close() error { return w.inner.Close() }

func (w *endWatch) end(err error) {
	w.once.Do(func() { w.onEnd(err) })
}

func (c *helperInstallConn) Home() (string, error) {
	return c.SFTPFS.RealPath(".")
}

func (c *helperInstallConn) Done() <-chan struct{} { return c.done }

func (c *helperInstallConn) LostErr() error {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.lostErr
	default:
		return nil
	}
}

// Close ends the lease and stops any call still in flight: the stream is
// closed — which is what unblocks a call wedged against a silent server —
// before the client is. The sftp client's own Close then waits for its reader
// goroutine to observe the stream's end, so no reader from this lease outlives
// Close. Done is deliberately NOT closed: an intentional stop must not read as
// connection loss.
func (c *helperInstallConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.stream != nil {
			_ = c.stream.Close()
		}
		if c.sftp != nil {
			_ = c.sftp.Close()
		}
	})
	return nil
}
