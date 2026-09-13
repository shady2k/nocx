package tunnelchan

// The two net shapes the tenants take: a connection (net.Conn) and a listener
// (net.Listener), both built on streams the helper holds.

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// channelConn is one proxied channel as a net.Conn: the direct-tcpip channel a
// Dial opened, or a connection that arrived on a listener.
//
// # The addresses are labels, and one of them is load-bearing
//
// LocalAddr and RemoteAddr report *net.TCPAddr values derived from the
// addresses the wire named. For a DIAL that is the ssh destination and the
// target; for an accepted connection it is the bind the server reported and
// the peer the far side saw. A hostname target yields a nil IP — an address
// that names no interface — which is what *net.TCPAddr can honestly hold.
//
// The load-bearing one is the LISTENER's Addr (remoteListener.Addr), because
// the remote lifecycle adapter asserts on the concrete type:
// lifecycleremote.portOf panics on anything that is not a *net.TCPAddr. It is
// the reason this file has no custom net.Addr type: the port on that address is
// the shell's connection target, and a bespoke addr would have to be
// type-asserted back into the shape the caller already requires.
type channelConn struct {
	stream        *helperclient.ChannelStream
	local, remote net.Addr
	onClose       func()

	once sync.Once
}

func (c *channelConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *channelConn) Write(p []byte) (int, error) { return c.stream.Write(p) }

// Close ends the connection: the helper is told, the far side's connection is
// closed there, and the lease stops tracking it. Idempotent, and the notice to
// the helper is best-effort — a Close whose helper never answers must still
// release the caller, which is why the stream's own Close is the bounded one.
func (c *channelConn) Close() error {
	var err error
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		err = c.stream.Close()
	})
	return err
}

func (c *channelConn) LocalAddr() net.Addr  { return c.local }
func (c *channelConn) RemoteAddr() net.Addr { return c.remote }

func (c *channelConn) SetDeadline(time.Time) error      { return errNoDeadline }
func (c *channelConn) SetReadDeadline(time.Time) error  { return errNoDeadline }
func (c *channelConn) SetWriteDeadline(time.Time) error { return errNoDeadline }

// remoteListener is a listener on the far side, as a net.Listener.
type remoteListener struct {
	lease *lease
	f     *helperclient.Forward
	local *net.TCPAddr

	once sync.Once
}

// Accept hands over the next connection that arrived on the listener. It
// returns the helper's cause once the listener is over — and ErrForwardClosed
// when the caller's own Close ended it — which is what lets the callers that
// hold an accept loop tell "I stopped it" from "it broke".
func (l *remoteListener) Accept() (net.Conn, error) {
	s, err := l.f.Accept()
	if err != nil {
		return nil, err
	}
	l.lease.trackStream(s)
	remote := l.f.Peer(s)
	return &channelConn{
		stream: s,
		local:  l.local,
		remote: tcpAddrOf(remote, portOfAddr(remote)),
		onClose: func() {
			l.lease.untrackStream(s)
		},
	}, nil
}

// Addr is the address the SERVER bound, as a *net.TCPAddr (see channelConn's
// own note for why the type is not negotiable). The port is the allocated one
// when 0 was requested; the host is the server's answer and never a verified
// bind.
func (l *remoteListener) Addr() net.Addr { return l.local }

// Close cancels the remote listen and ends this lease's interest in it. The
// accepted connections are NOT closed here: each has its own end, reported on
// its own reads, and closing them from the listener would take a caller's live
// stream away when all it asked for was to stop accepting new ones.
func (l *remoteListener) Close() error {
	var err error
	l.once.Do(func() {
		l.lease.untrackForward(l)
		err = l.f.Close()
	})
	return err
}

// watchLoss reports a listener end this lease did not ask for.
//
// The distinction is carried by the cause: the helper sets it only when
// nobody here asked for the end, so an end with no cause is this lease's own
// Close (or a coordinator asking) and is not a loss. One notification, two
// meanings, and the field that already existed for the difference decides.
func (l *remoteListener) watchLoss() {
	<-l.f.Done()
	l.lease.untrackForward(l)
	if l.f.CloseCause() != nil {
		l.lease.reportListenerGone(l.f.CloseCause())
	}
}

// parseTarget reads an "host:port" address into the typed pair the wire takes.
//
// Port 0 is ACCEPTED here and refused by the op that cannot honour it: it is
// the ordinary request for a remote listener (the server allocates and the
// answer carries the port), and it is never a valid direct-tcpip target. The
// helper's own validation is what refuses the second case, so the rule lives in
// one place rather than being half-applied on this side of the wire.
func parseTarget(addr string) (proto.ChannelTarget, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return proto.ChannelTarget{}, fmt.Errorf("tunnelchan: address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return proto.ChannelTarget{}, fmt.Errorf("tunnelchan: address %q: port %q: %w", addr, portStr, err)
	}
	if port < 0 || port > 65535 {
		return proto.ChannelTarget{}, fmt.Errorf("tunnelchan: address %q: port %d is out of range", addr, port)
	}
	if host == "" {
		return proto.ChannelTarget{}, fmt.Errorf("tunnelchan: address %q names no host", addr)
	}
	return proto.ChannelTarget{Host: host, Port: port}, nil
}

// tcpAddrOf turns a host and a port from the wire into the address type the
// callers assert on. A name yields a nil IP, which is the honest encoding of
// "this address names no interface on this machine" — the far side is the
// party that resolves it, and that is the whole point of a forward.
func tcpAddrOf(host string, port int) *net.TCPAddr {
	return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
}

// portOfAddr reads the port out of a reported peer address, 0 when there is
// none. It never fails: a peer address is a label for a log line, and a
// malformed one must not be able to refuse a connection that is otherwise
// fine.
func portOfAddr(addr string) int {
	if addr == "" {
		return 0
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}
