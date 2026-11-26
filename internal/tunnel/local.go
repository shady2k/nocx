package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/shady2k/nocx/internal/ssh"
)

// local implements the local (-L) strategy: a net.Listener locally, Accept,
// then one direct-tcpip channel per accepted connection to the destination —
// which resolves on the server's network — with a bidirectional copy
// (spec §7.1).
type local struct {
	conn Connector
	bind Bind
	dest string
	fl   *forwardLifecycle
}

func newLocal(bind Bind, dest string, conn Connector) *local {
	return &local{
		conn: conn,
		bind: bind,
		dest: dest,
		fl:   newForwardLifecycle(),
	}
}

// start implements strategy.start for -L.
//
// Order is the spec's trap order:
//  1. Bind before reporting — bind errors are synchronous.
//  2. No pre-check of port availability (TOCTOU); the listen is the check.
//  3. Port 0 allocates; the actual bind comes from the listener's address.
//  5. The connection lease is taken HERE, before the bind, so a bind failure
//     releases it instead of leaking a pooled reference.
func (l *local) start(ctx context.Context, host string, opts []ssh.ConnectOption) (Bind, error) {
	lease, err := l.conn.TunnelConn(ctx, host, opts...)
	if err != nil {
		return Bind{}, fmt.Errorf("tunnel: acquire connection: %w", err)
	}

	addr := net.JoinHostPort(l.bind.Host, strconv.Itoa(l.bind.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = lease.Close()
		// Marked, not merely wrapped: only this failure means the LOCAL
		// port is held, and the wire's typed cause is drawn from the mark
		// (IsLocalBind) — the acquire above can carry the same syscall.
		return Bind{}, &localBindError{err: err}
	}
	if !l.fl.publish(lease, ln) {
		_ = lease.Close()
		_ = ln.Close()
		return Bind{}, errors.New("tunnel: connection lost before bind completed")
	}

	// The loss watcher starts AFTER the lease and listener are published, so
	// shutdown can always find and close them (see publish).
	go l.fl.watchLoss(lease)

	hostStr, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		// Cannot happen for a TCP listener we just created, but be honest:
		// tear the forward down rather than leaking the bind.
		l.fl.shutdown(StopReasonError, err)
		return Bind{}, fmt.Errorf("tunnel: parse actual bind %q: %w", ln.Addr().String(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		l.fl.shutdown(StopReasonError, err)
		return Bind{}, fmt.Errorf("tunnel: parse actual port %q: %w", portStr, err)
	}

	// One direct-tcpip channel per accepted connection, over the pooled
	// connection the lease holds.
	go l.fl.serve(ln, func(c net.Conn) {
		l.fl.proxy(c, l.dest, lease.Dial)
	})
	return Bind{Host: hostStr, Port: port}, nil
}

// stop implements strategy.stop: the user stopped the forward.
func (l *local) stop() {
	l.fl.shutdown(StopReasonUser, nil)
}

func (l *local) done() <-chan struct{} { return l.fl.done() }

func (l *local) outcome() Outcome { return l.fl.outcome() }

// caveat implements strategy.caveat: -L has no bind caveat — the local
// listener's address is the OS's own answer, verified by the bind itself.
func (l *local) caveat() string { return "" }
