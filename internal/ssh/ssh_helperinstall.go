package ssh

// The helper-install lease (remote-helper design D7, D20): a write-capable,
// purpose-specific lease the deploy package installs the helper through. It
// is the boundary ssh_uninstall.go already set — the raw *gossh.Client stays
// inside internal/ssh, and callers get purpose-specific capabilities — and
// it is why the install cannot reuse the shell-integration publisher: that
// publisher's manifest, generation, locking and foreign-root semantics
// belong to the shell bundle under ~/.nocx, while the helper has its own
// content-addressed layout and pruning rules (D7). One publisher serving
// both would couple two unrelated deployment protocols.

import (
	"context"
	iofs "io/fs"
	"os"
	"sync"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
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
	// Close releases this lease's pooled reference. The SFTP session
	// channel is closed first — the close-to-cancel mechanism that
	// unblocks a call wedged against a silent server — so no call from
	// this lease is in flight when the reference drops. Done is
	// deliberately NOT closed.
	Close() error
}

// helperInstallConn is the concrete HelperInstallConn. It holds its own
// pooled reference, released exactly once (by Close or the loss watcher),
// and the SFTP write primitives through the shared SFTPFS adapter — the
// same adapter the shell-integration publisher uses, so mkdir+chmod,
// create+chmod, rename, removal, fsync tolerance and status translation
// have one implementation (AD-8).
type helperInstallConn struct {
	*SFTPFS
	sess *gossh.Session
	sftp *sftp.Client // the lifecycle owner; SFTPFS runs the operations

	done   chan struct{}
	closed chan struct{}

	release     func()
	releaseOnce sync.Once
	closeOnce   sync.Once

	lostErr error
}

// newHelperInstallConn acquires an SFTP subsystem on the pooled connection
// — the same acquisition FSConn uses, bounded by the same hard timeout so a
// Background ctx cannot hang it — and wraps it with the write surface. On
// any failure the pooled reference is released before returning.
func newHelperInstallConn(client *gossh.Client, release func(), ctx context.Context) (*helperInstallConn, error) {
	openCtx, cancel := context.WithTimeout(ctx, fsHardTimeout)
	defer cancel()
	sess, sftpClient, err := openSFTPSubsystem(client, openCtx)
	if err != nil {
		release()
		return nil, err
	}
	c := &helperInstallConn{
		SFTPFS:  NewSFTPFS(sftpClient),
		sess:    sess,
		sftp:    sftpClient,
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
		release: release,
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

func (c *helperInstallConn) Home() (string, error) {
	return c.SFTPFS.RealPath(".")
}

func (c *helperInstallConn) Done() <-chan struct{} { return c.done }

func (c *helperInstallConn) LostErr() error {
	select {
	case <-c.done:
		return c.lostErr
	default:
		return nil
	}
}

// Close releases this lease's pooled reference and stops any call still in
// flight: the SFTP session channel is closed — which is what unblocks a
// call wedged against a silent server — before the reference drops. The
// sftp client's own Close then waits for its reader goroutine to observe
// the channel close, so no reader from this lease outlives Close. Done is
// deliberately NOT closed: an intentional stop must not read as connection
// loss.
func (c *helperInstallConn) Close() error {
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

// HelperInstallConn acquires an owned lease on the pooled SSH connection
// for host, running the write-capable SFTP subsystem the helper installer
// needs (design D7). The same connection configuration (credentials, keys,
// jump route) as a Connect to host is resolved and authorized: the lease is
// bound by the same pool key (AD-4), and it holds its OWN pooled reference
// — never the tab's — so closing the tab can never kill an in-flight
// install underneath it. Release the lease with Close; on connection loss
// the lease releases itself and Done closes.
func (rc *RealClient) HelperInstallConn(ctx context.Context, host string, opts ...ConnectOption) (HelperInstallConn, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}
	// newHelperInstallConn returns a *helperInstallConn; returning it
	// directly would box a typed nil into the HelperInstallConn interface
	// on the error paths. Split the multi-value return so an error yields a
	// nil interface.
	c, err := newHelperInstallConn(acq.client, func() { rc.pool.Release(acq.handle) }, ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}
