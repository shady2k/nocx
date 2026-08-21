package apisend

// The SSH half of design §7.1's seam, and the one place in this feature
// where a limit has to be written down rather than designed away.
//
// `tunnelConn.Dial` (internal/ssh/ssh_tunnel.go:116) is
//
//	Dial(addr string) (net.Conn, error)
//
// It returns a net.Conn, which is what matters — every byte of the exchange
// then rides an ordinary connection and TLS is ours end to end. But it is
// NOT http.Transport.DialContext's signature and it takes NO CONTEXT. So
// this adapter does two things the signature makes unavoidable:
//
//  1. It SUPPLIES THE NETWORK PARAMETER. The tunnel opens a direct-tcpip
//     channel, which is tcp and only tcp, so a network the channel cannot
//     honour is refused rather than silently widened.
//
//  2. It DROPS THE CONTEXT. The consequence is honest and is not hidden
//     anywhere: CANCELLING A REQUEST DOES NOT INTERRUPT A BLOCKED REMOTE
//     CHANNEL OPEN. There is no seam through which it could — a
//     context-aware tunnel dial would remove the limit, and adding one is
//     not in this design's scope.
//
// What is guaranteed instead, stated as an interval with both ends because
// a moment is not an invariant (AGENTS.md testing rule 3):
//
//	A dial is outstanding from the call until EITHER the far side answers,
//	OR the context is done, OR dialTimeout elapses — whichever is first.
//	From that moment the caller holds no connection and never will: a
//	connection that arrives afterwards is CLOSED by this adapter and handed
//	to nobody, so a cancelled run can never acquire one.
//
// And the refusal that is the point of the whole route: a spent lease
// REFUSES. It never falls back to a local dialer, because a silent fallback
// would send a production request around its bastion — which is the exact
// send §6.5 exists to make impossible.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// defaultSSHDialTimeout bounds a dial when the caller names no bound. It is
// the only thing standing between a bastion that has stopped answering and
// a run that never ends, because the context cannot reach into the tunnel.
const defaultSSHDialTimeout = 30 * time.Second

// ErrSSHDialTimeout is the bound elapsing. It is its own sentinel because
// it is its own sentence for the user: "the connection did not answer" is
// not "you cancelled" and not "the target refused".
var ErrSSHDialTimeout = errors.New(component + ": the connection did not open a channel within the dial timeout")

// ErrNoSSHLease is a route wired without a lease. A refusal rather than a
// panic in the dial path — and, like every other failure here, never a
// local dial.
var ErrNoSSHLease = errors.New(component + ": this route has no lease on an SSH connection")

// NewSSHDialer adapts a pooled SSH lease to the Dialer the executor takes.
//
// It does NOT acquire the lease and it does NOT release it: the lease is a
// reference to a POOLED connection (AD-7 — a session references a pooled
// connection, never owns it), taken and released by whoever owns the route.
// A dialer that closed the lease it was handed would kill a connection tabs
// and other tunnels are sharing.
//
// dialTimeout at or below zero means defaultSSHDialTimeout.
func NewSSHDialer(lease ssh.TunnelConn, dialTimeout time.Duration) Dialer {
	if dialTimeout <= 0 {
		dialTimeout = defaultSSHDialTimeout
	}
	return &sshDialer{lease: lease, timeout: dialTimeout}
}

type sshDialer struct {
	lease   ssh.TunnelConn
	timeout time.Duration
}

// dialResult is one completed remote dial, whoever is still listening.
type dialResult struct {
	conn net.Conn
	err  error
}

func (d *sshDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.lease == nil {
		return nil, ErrNoSSHLease
	}
	// The network parameter is SUPPLIED by this adapter, so it is also
	// checked by it. tcp4 and tcp6 pin an address family that the REMOTE
	// side decides — the far end resolves the name and chooses the address
	// (§7.1) — so honouring them would be a promise this end cannot keep.
	if network != "tcp" {
		return nil, fmt.Errorf("%s: cannot dial %q through an SSH connection: a direct-tcpip channel is tcp, "+
			"and the address family is the remote side's choice", component, network)
	}
	// Checked before dialling, so a cancelled queue of runs asks nothing of
	// the far side at all.
	if err := ctx.Err(); err != nil {
		return nil, d.wrap(addr, err)
	}

	// Buffered, so the dialing goroutine never blocks on a send nobody is
	// waiting for. It ends when Dial returns, which is bounded by the SSH
	// connection's own lifetime: the lease's watcher closes the connection
	// on loss and Dial comes back with ErrTunnelConnLost.
	done := make(chan dialResult, 1)
	go func() {
		c, err := d.lease.Dial(addr)
		done <- dialResult{conn: c, err: err}
	}()

	timer := time.NewTimer(d.timeout)
	defer timer.Stop()

	select {
	case r := <-done:
		if r.err != nil {
			return nil, d.wrap(addr, r.err)
		}
		return r.conn, nil
	case <-ctx.Done():
		go closeLate(done)
		return nil, d.wrap(addr, ctx.Err())
	case <-timer.C:
		go closeLate(done)
		return nil, d.wrap(addr, ErrSSHDialTimeout)
	}
}

// wrap names the sender rather than the ssh package the user has never
// heard of, keeps the address so a surface can say what could not be
// reached, and leaves errors.Is reaching the real reason — the cancelled
// context, the timeout, ssh.ErrTunnelConnClosed or ssh.ErrTunnelConnLost,
// each of which is a different sentence for the user.
func (d *sshDialer) wrap(addr string, err error) error {
	return fmt.Errorf("%s: dial %s through the SSH connection: %w", component, addr, err)
}

// closeLate takes the connection nobody is waiting for and closes it. This
// is the mitigation that replaces the guarantee the missing context makes
// impossible: the caller has already been told the dial failed, so a
// connection arriving now belongs to nobody, and leaving it open would be a
// live channel to the far side that no run will ever read.
//
// It blocks until the remote dial returns. That wait is unbounded in this
// adapter and bounded by the SSH connection: when the connection dies the
// lease's watcher fires and Dial returns ErrTunnelConnLost.
func closeLate(done <-chan dialResult) {
	if r := <-done; r.conn != nil {
		_ = r.conn.Close()
	}
}
