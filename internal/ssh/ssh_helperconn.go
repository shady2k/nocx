package ssh

// The pty-less exec lane (design D19): RealClient.HelperConn opens ONE
// long-lived exec session WITHOUT a pty-req — a pty applies line discipline
// (\n → \r\n, echoed input) and would corrupt the helper's binary frames —
// with pipes, on a pooled reference the lane owns itself. It is
// DiscoveryConn's sibling: it never touches the tab's reference, and where
// DiscoveryConn opens a fresh capped session per call, this lane holds one
// session for the helper process's lifetime.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

// HelperConn is the pty-less exec lane the helper client rides. The
// interface exists so feature packages can fake the lane with io.Pipe (the
// client's tests do exactly that — no SSH needed); the concrete
// implementation is *helperConn, returned by RealClient.HelperConn.
//
// Release the lease with Close when the helper is done; on connection loss
// the lease releases itself and Done closes.
type HelperConn interface {
	// Stdin returns the lane's stdin — the client's frame output.
	Stdin() io.WriteCloser
	// Stdout returns the lane's stdout — the wire.
	Stdout() io.Reader
	// Stderr returns the lane's stderr — diagnostics only (D22).
	Stderr() io.Reader
	// Start launches command over the already-open session. The channel was
	// opened WITHOUT a pty-req (D19); a server refusal of the exec surfaces
	// here.
	Start(command string) error
	// Wait returns the remote exit status once the command has exited.
	// Call it once.
	Wait() (int, error)
	// Done closes when the underlying connection shuts down: connection
	// loss, server close, keepalive failure. It does NOT close on Close: an
	// intentional stop while the connection is still shared must not read
	// as connection loss.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close ends the lane: the session is closed — which ends the remote
	// process via stdin EOF — and the lease's pooled reference is
	// released. The connection stays open for every other reference.
	Close() error
}

// helperConn is the concrete HelperConn.
type helperConn struct {
	client  *gossh.Client
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  io.Reader

	// done closes on transport shutdown (the loss signal); closed closes on
	// Close. The lease's own state is checked first when both have fired —
	// the deterministic answer wins (classifyExecError's rule).
	done   chan struct{}
	closed chan struct{}

	release     func()
	releaseOnce sync.Once
	closeOnce   sync.Once

	// lostErr is written by the watcher before done closes, so reading it
	// after <-done is ordered by the channel close.
	lostErr error
}

// newHelperConn opens the session and its pipes and wires the loss watcher.
// The session is deliberately NOT given a pty-req: the helper's frames must
// cross the wire byte-identical (D19).
func newHelperConn(client *gossh.Client, release func()) (*helperConn, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("helper session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("helper stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("helper stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("helper stderr pipe: %w", err)
	}
	hc := &helperConn{
		client:  client,
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
		release: release,
	}
	// One watcher per lane: gossh.Client.Wait returns when the transport
	// shuts down. Report loss and drop our reference so a dead entry cannot
	// linger behind an unreleased lease.
	go func() {
		hc.lostErr = client.Wait()
		close(hc.done)
		hc.releaseOnce.Do(func() {
			if hc.release != nil {
				hc.release()
			}
		})
	}()
	return hc, nil
}

func (c *helperConn) Stdin() io.WriteCloser { return c.stdin }
func (c *helperConn) Stdout() io.Reader     { return c.stdout }
func (c *helperConn) Stderr() io.Reader     { return c.stderr }

func (c *helperConn) Done() <-chan struct{} { return c.done }

func (c *helperConn) LostErr() error {
	select {
	case <-c.done:
		return c.lostErr
	default:
		return nil
	}
}

// Start launches the helper over the already-open session. No pty-req was
// sent when the session was opened, and none is sent here (D19).
func (c *helperConn) Start(command string) error {
	return c.session.Start(command)
}

// Wait reports the remote exit status once the command has exited. A
// nonzero exit is the version-mismatch signal the client classifies (D5);
// anything that is not an exit status — the transport dying — surfaces as
// the error.
func (c *helperConn) Wait() (int, error) {
	err := c.session.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *gossh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	return 0, err
}

// Close ends the lane: the session is closed — which ends the remote
// process — before the pooled reference drops, so a tab death mid-request
// tears the remote helper down instead of leaving it running on the far
// host.
func (c *helperConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.session.Close()
		c.releaseOnce.Do(func() {
			if c.release != nil {
				c.release()
			}
		})
	})
	return nil
}

// HelperConn acquires an owned lease on the pooled SSH connection for host,
// running the remote helper (design §4). It takes its OWN pooled reference
// — never the tab's — so closing the creating tab can never kill an
// in-flight helper's connection underneath it, and the interactive session
// stays fully usable while the helper runs on its own channel. The channel
// is opened without a pty-req (D19) and holds ONE session for the helper
// process's lifetime. Release the lease with Close when the helper is done;
// on connection loss the lease releases itself and Done closes.
//
// The same connection configuration (credentials, keys, jump route) as a
// Connect to host is resolved and authorized: the helper is bound by the
// same credential authorization as a tab, and shares the tab's connection
// when the pool key matches (AD-4).
func (rc *RealClient) HelperConn(ctx context.Context, host string, opts ...ConnectOption) (HelperConn, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}
	hc, err := newHelperConn(acq.client, func() { rc.pool.Release(acq.handle) })
	if err != nil {
		rc.pool.Release(acq.handle)
		return nil, err
	}
	return hc, nil
}
