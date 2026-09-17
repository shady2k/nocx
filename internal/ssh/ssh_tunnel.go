package ssh

import (
	"errors"
	"net"
)

// TunnelConn is the lease surface a forward, a remote listener or an API
// request's connection holds (spec §7.3). It exists so feature packages can
// fake the lease, and so the loss contract is a declared part of the API
// rather than an implementation detail.
//
// # Where the implementation is, and why it is not here
//
// It used to be this package: RealClient.TunnelConn acquired a pooled
// connection (AD-4) and this type was a reference to it. The owner's invariant
// of 2026-09-13 — there is no ssh connection without a helper, and the
// coordinator holds no ssh client (epic nocx-50w7p, plan §3) — moved the dial
// into THIS MACHINE'S HELPER, and the implementation moved with it:
// internal/helper/tunnelchan opens ssh channels on the helper
// (proto/ssh_service.go) and satisfies this interface. Every tenant of the
// lease moved in the same landing group, which is why the interface stayed
// here while its only implementation did not.
//
// So the semantics below are still the CONTRACT the callers switch on, and two
// of them were re-stated by that move rather than kept:
//
//   - the lease is a GROUP of streams to one destination, not a reference to
//     one pooled connection. The connection is the helper's pool's (AD-4) and
//     outlives the lease; the destination is resolved and authorized once, at
//     acquisition, and the helper dials when the first channel is opened.
//   - Done reports what nothing this lease holds can survive — for the helper,
//     the loss of the connection to the DAEMON, and the end of a listener
//     nobody here asked to end (the far side's connection dying under a remote
//     forward). It is not raised by one stream failing, because that is a fact
//     about that stream and a forward whose target refused a connection must
//     not stop.
type TunnelConn interface {
	// Dial opens a direct-tcpip channel to addr on the FAR side's network.
	// Each call is an independent channel: one stream failing — a remote
	// target refusing the connection — never affects the lease or any other
	// stream. addr is "host:port"; a name is resolved by the far side, which
	// is the point of both a local forward (-L) and a SOCKS proxy (-D).
	Dial(addr string) (net.Conn, error)
	// Listen asks the FAR side for a listening socket (the -R request): the
	// request carries the address as given, so a bind host that is a hostname
	// is resolved by the server, never locally. The returned listener's Addr
	// reports the address the server allocated — a requested port 0 is
	// resolved by the server and never reported as 0 — and its concrete type
	// is *net.TCPAddr, which the remote lifecycle adapter asserts on. Each
	// accepted connection arrives as a forwarded channel over the same
	// connection, so the listener must be serviced or the connection may
	// hang; closing it cancels the remote listen. When the server refuses —
	// its AllowTcpForwarding is off, or the bind is outside PermitListen — the
	// error is a refusal, not a dial failure.
	Listen(addr string) (net.Listener, error)
	// Done closes when nothing this lease holds can work any more. It does NOT
	// close on Close: an intentional stop while other leases use the same
	// transport must not read as connection loss.
	Done() <-chan struct{}
	// LostErr reports why the lease was lost. Meaningful once Done has closed;
	// nil when what ended was not a failure.
	LostErr() error
	// Close releases this lease: its streams end and its listeners are
	// cancelled. The pooled connection stays open for every other lease's
	// streams — an install, a file listing and a forward to one host share one
	// transport, and closing one of them must not take the others with it.
	Close() error
}

// ErrTunnelConnLost is returned by Dial after the lease lost whatever carries
// it: the helper connection died (the daemon went away), or the remote
// listener a caller was using ended under it.
var ErrTunnelConnLost = errors.New("ssh: tunnel connection lost")

// ErrTunnelConnClosed is returned by Dial after the lease was released by
// Close.
var ErrTunnelConnClosed = errors.New("ssh: tunnel connection closed")
