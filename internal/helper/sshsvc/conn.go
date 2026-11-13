//go:build nocx_local_ssh

package sshsvc

// The pooled reference a holder carries, as the service hands it out.
//
// Before the connection supervisor existed, every holder held a
// *ssh.PooledConn: its own reference to the pool entry, released when its
// channel ended. Now the CONNECTION's reference belongs to the supervisor
// (design §5.1: one supervisor per connection key, and the count that decides
// when it closes belongs to the connection), and what a holder gets is a lease
// on it — plus the pool handle the channel kinds still open through, because
// Files, Git, ports and tunnels have not moved behind the Carrier yet
// (design §5.1.2).
//
// One object rather than two fields at every call site: the lease and the pool
// are one reference, and a holder that released one without the other would
// either leak a connection or close one somebody else is using.

import (
	"errors"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// pooledConn is one held reference to a pooled connection.
type pooledConn struct {
	lease *hostconn.Lease
	pool  *ssh.PooledConn
}

// errNoPooledConnection is a holder that asks a reference it does not have —
// nil in the tests that drive an op's own refusal path without a connection.
var errNoPooledConnection = errors.New("sshsvc: this holder carries no pooled connection")

// Client answers the connection the channel kinds in this package open on.
func (c *pooledConn) Client() *gossh.Client {
	if c == nil || c.pool == nil {
		return nil
	}
	return c.pool.Client()
}

// NewSession opens a session channel on this connection, with the far side's
// release of the previous session awaited first (ssh.PooledConn.NewSession's
// own rule, and the reason that method exists).
func (c *pooledConn) NewSession() (*gossh.Session, error) {
	if c == nil || c.pool == nil {
		return nil, errNoPooledConnection
	}
	return c.pool.NewSession()
}

// Fingerprint is the target host's key fingerprint, as the handshake saw it.
func (c *pooledConn) Fingerprint() string {
	if c == nil || c.pool == nil {
		return ""
	}
	return c.pool.Fingerprint()
}

// Taint stops the pool handing this connection to any new caller (spec §5.7's
// first detach).
func (c *pooledConn) Taint() {
	if c == nil || c.pool == nil {
		return
	}
	c.pool.Taint()
}

// TaintReason is why the pool closed this connection under its cap or its
// prober, or "" for a connection that was not closed that way.
func (c *pooledConn) TaintReason() string {
	if c == nil || c.pool == nil {
		return ""
	}
	return c.pool.TaintReason()
}

// Askable reports whether the connection is answering now: connected and not
// stalled. A caller about to send a request that waits for a reply uses it to
// avoid parking behind a probe the supervisor already has in flight (design
// §5.3: no channel is asked of a stalled connection).
func (c *pooledConn) Askable() bool {
	if c == nil || c.lease == nil {
		return false
	}
	return c.lease.Current().Kind == hostconn.Connected && !c.Lost()
}

// Lost reports whether the transport this reference rides has ended. It is the
// supervisor's fact, read from the lease, and it is what keeps a dead
// connection from being reported as the far side refusing something.
func (c *pooledConn) Lost() bool {
	if c == nil || c.lease == nil {
		return false
	}
	return c.lease.Lost()
}

// observe asks the connection's supervisor to report every probe to fn. It is
// what a shell's OnLiveness is now: the connection's ONE prober already runs,
// and a holder listens to it rather than arming a second.
func (c *pooledConn) observe(fn func(hostconn.Observation)) {
	if c == nil || c.lease == nil {
		return
	}
	c.lease.Observe(fn)
}

// Close gives this holder's reference back. The connection itself closes when
// the LAST holder's does, through the supervisor.
func (c *pooledConn) Close() error {
	if c == nil || c.lease == nil {
		return nil
	}
	c.lease.Release()
	return nil
}
