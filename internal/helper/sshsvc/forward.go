//go:build nocx_local_ssh

package sshsvc

// The FORWARD half of the `ssh` service: a listener on the far side, and the
// connections that arrive on it.
//
// # What a forward is, and why it is an op of its own
//
// A local forward (-L) and a SOCKS proxy (-D) are `direct-tcpip` channels:
// the caller asks for one stream and holds it. A remote forward (-R) and the
// remote lifecycle channel (ADR-0024) are the other direction — the caller
// asks the FAR SIDE to listen, and the connections that arrive there are
// nobody's request; they are somebody connecting to a port that exists because
// the caller asked for it.
//
// So this is a second op with its own identity and its own end (ForwardID,
// `unforward`), and the accepted connections become ORDINARY channels: they
// are registered in the same table an open's channel is, they are routed by
// the same ChannelData, ended by the same `close`, and announced by the same
// `channel-closed`. The one thing that differs is who ends them when the
// listener goes: the forward does, because the listener is what they arrived
// on.
//
// # The ordering that makes an announcement sufficient
//
// The coordinator learns a forwarded channel's id from a notification and
// from nowhere else — there is no response to answer, because nobody asked for
// this connection. So the notification must be on the wire BEFORE the first
// byte of the stream it names, and that is arranged rather than hoped for:
// SendNotification takes the host's writer mutex, the same mutex
// SendChannelData takes, so the announcement is written first and the pump
// that follows cannot have its bytes overtake it.
//
// # Which reference keeps the transport alive
//
// The FORWARD holds it. An accepted connection does not take one of its own
// (openChannel.pool is nil for these): a listener and the streams on it are
// one resource, and giving each accepted connection a reference would make
// "the listener was cancelled" and "a stream ended" two different states where
// the caller has one. The consequence is deliberate and is the same one the
// coordinator's own lease had before the dial moved here: cancelling the
// forward ends the streams it produced.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/ssh"
)

// openForward is one live listener on the far side.
type openForward struct {
	id   proto.ForwardID
	conn *host.Host
	pool *ssh.PooledConn
	ln   net.Listener
	// bind is what the SERVER answered, which is what the coordinator is
	// told: a requested port 0 comes back allocated, and a non-loopback host
	// comes back as whatever the server bound (GatewayPorts makes that
	// unverifiable from this side, and the coordinator discloses it).
	bind proto.ChannelTarget

	mu       sync.Mutex
	channels map[proto.ChannelID]struct{}

	closeOnce sync.Once
}

// forwardOps adds the two listener ops to the service's own.
func (s *Service) forwardOps() []string { return []string{proto.OpForward, proto.OpUnforward} }

// forward performs one `ssh.forward`: acquire the pooled connection, ask the
// far side for a listener, register it, and answer the id and the address the
// server bound.
//
// The ACCEPT LOOP is not started here — it is started from ResponseWritten,
// for exactly the reason the channel reader pump is (channel.go's own note):
// an accepted connection is announced by a notification naming the forward id,
// and the coordinator only learns that id from this op's response. A loop
// started here could announce a connection for an id the caller has never
// heard of, and the announcement would be dropped as belonging to nobody.
func (s *Service) forward(ctx context.Context, p proto.ForwardParams) (proto.ForwardResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.ForwardResult{}, errNoAuthChannel
	}
	if err := validateForward(p); err != nil {
		return proto.ForwardResult{}, err
	}

	pool, err := s.acquirePooled(ctx, conn, p.Destination, p.AcceptOnTrust)
	if err != nil {
		return proto.ForwardResult{}, err
	}
	addr := net.JoinHostPort(p.Bind.Host, strconv.Itoa(p.Bind.Port))
	ln, err := sshdial.ListenRemoteTCP(pool.Client(), addr)
	if err != nil {
		// The refusal is the server's, classified the way every other channel
		// failure is: a policy that forbids forwarding and a bind outside
		// PermitListen are indistinguishable on the wire, and the sentence
		// carries that rather than inventing a diagnosis.
		_ = pool.Close()
		return proto.ForwardResult{}, classifyChannelError(err)
	}

	id, err := mintForwardID()
	if err != nil {
		_ = ln.Close()
		_ = pool.Close()
		return proto.ForwardResult{}, internalRefusal("mint a forward id: %v", err)
	}
	bind, err := listenerTarget(ln, p.Bind)
	if err != nil {
		// The listener is real but its address cannot be read, so the
		// coordinator would have a port nobody can be told about: close it
		// rather than answer a forward that cannot be used.
		_ = ln.Close()
		_ = pool.Close()
		return proto.ForwardResult{}, internalRefusal("read the listener's address: %v", err)
	}
	f := &openForward{id: id, conn: conn, pool: pool, ln: ln, bind: bind, channels: map[proto.ChannelID]struct{}{}}

	s.mu.Lock()
	if s.forwards == nil {
		s.forwards = make(map[proto.ForwardID]*openForward)
	}
	if _, exists := s.forwards[id]; exists {
		s.mu.Unlock()
		_ = ln.Close()
		_ = pool.Close()
		return proto.ForwardResult{}, internalRefusal("forward id collision")
	}
	s.forwards[id] = f
	s.mu.Unlock()

	// A watcher, and it is what makes a -R forward's loss REPORTABLE. The far
	// side's connection dying closes the listening channel under this process
	// without anybody asking — the remote lifecycle adapter and the remote
	// strategy both hold an Accept that would otherwise wait for ever — so the
	// end is announced on both paths (the watcher here, and the accept loop's
	// own error) and finish is once-guarded.
	go func() {
		lost := f.pool.Client().Wait()
		f.finish(s, lossCause(lost))
	}()

	s.log.Info("ssh: forward opened",
		"forward", id.String(),
		"host", p.Destination.Host, "port", p.Destination.Port, "user", p.Destination.User,
		"bind", net.JoinHostPort(bind.Host, strconv.Itoa(bind.Port)))
	return proto.ForwardResult{Forward: id, Bind: bind}, nil
}

// unforward performs one `ssh.unforward`: end the listener, end the channels
// it produced, and release the pooled reference. An id this helper does not
// hold is answered as done, for the reason `close` is idempotent.
func (s *Service) unforward(id proto.ForwardID) (proto.UnforwardResult, error) {
	if id.IsZero() {
		return proto.UnforwardResult{}, fmt.Errorf("%w: no forward id", errBadChannelParams)
	}
	s.mu.Lock()
	f := s.forwards[id]
	delete(s.forwards, id)
	s.mu.Unlock()
	if f == nil {
		s.log.Debug("ssh: unforward for an unknown listener", "forward", id.String())
		return proto.UnforwardResult{}, nil
	}
	// cause is EMPTY because the coordinator is the one that asked: the
	// notification that follows still tells it the listener is over, and the
	// absent cause is what distinguishes "you asked" from "it broke".
	f.finish(s, "")
	return proto.UnforwardResult{}, nil
}

// ResponseWritten starts the accept loop, once the forward's answer is on the
// wire (host.ResponseObserver). See forward's own note for why it cannot start
// any earlier.
func (s *Service) forwardResponseWritten(result any) {
	res, ok := result.(proto.ForwardResult)
	if !ok {
		return
	}
	s.mu.Lock()
	f := s.forwards[res.Forward]
	s.mu.Unlock()
	if f != nil {
		go s.acceptForwarded(f)
	}
}

// acceptForwarded serves one listener: every connection the far side accepts
// becomes a channel, announced before its first byte.
func (s *Service) acceptForwarded(f *openForward) {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			// Two events reach here and the cause tells them apart: the
			// coordinator asked (unforward, whose finish has already run) or
			// the far side's connection died under the listener. Reporting
			// the second is what releases an Accept nobody would ever
			// answer; the first is idempotent with finish's own call.
			s.log.Info("ssh: forward stopped accepting", "forward", f.id.String(), "error", err)
			f.finish(s, err.Error())
			return
		}
		if !f.serveAccepted(s, c) {
			return
		}
	}
}

// serveAccepted turns one accepted connection into a channel. It answers
// false when the forward is over and the connection has been closed, which is
// how the accept loop learns to stop.
func (f *openForward) serveAccepted(s *Service, c net.Conn) bool {
	id, err := mintChannelID()
	if err != nil {
		_ = c.Close()
		s.log.Warn("ssh: forwarded connection dropped: no channel id", "forward", f.id.String(), "error", err)
		return true
	}
	ch := &openChannel{id: id, conn: f.conn, end: c, owner: f}
	if !f.track(id) {
		// The listener was cancelled between Accept and here. The connection
		// belongs to a forward that is over, and handing it to a caller that
		// has already been told the listener ended would be a stream nobody
		// can address.
		_ = c.Close()
		return false
	}
	if err := s.registerChannel(ch); err != nil {
		f.forget(id)
		_ = c.Close()
		s.log.Warn("ssh: forwarded connection dropped", "forward", f.id.String(), "error", err)
		return true
	}
	// The announcement goes out BEFORE the pump starts, under the host's one
	// writer mutex (see the file header): the coordinator cannot have a byte
	// of a channel it has not been told the id of.
	if err := f.conn.SendNotification(proto.Notification{
		Service: proto.ServiceSSH,
		Event:   proto.EventForwardedTCPIP,
		Params: proto.ForwardedTCPIPEvent{
			Forward: f.id,
			Channel: id,
			Peer:    peerOf(c),
		},
	}); err != nil {
		s.log.Warn("ssh: forwarded connection dropped: the coordinator could not be told",
			"forward", f.id.String(), "channel", id.String(), "error", err)
		f.finish(s, "the helper could not write to the coordinator: "+err.Error())
		return false
	}
	go ch.pumpToCoordinator(s.log)
	return true
}

// finish ends a listener once: the announcement, the remote listen, the
// channels it produced and the pooled reference, in that order.
//
// cause is the helper's sentence for an end nobody here asked for, empty when
// the coordinator did.
func (f *openForward) finish(s *Service, cause string) {
	f.closeOnce.Do(func() {
		// Announced on BOTH ways of ending, for the reason a channel's close
		// is: an id the coordinator has already forgotten is dropped by its
		// own routing, and one dropped frame is cheaper than a reader waiting
		// for an end that never comes.
		_ = f.conn.SendNotification(proto.Notification{
			Service: proto.ServiceSSH,
			Event:   proto.EventForwardClosed,
			Params:  proto.ForwardClosedEvent{Forward: f.id, Error: cause},
		})
		// Closing the listener is what cancels the remote listen
		// (x/crypto/ssh sends cancel-tcpip-forward) — the order matters, so
		// the far side stops producing connections before this end stops
		// serving them.
		_ = f.ln.Close()
		f.mu.Lock()
		live := make([]proto.ChannelID, 0, len(f.channels))
		for id := range f.channels {
			live = append(live, id)
		}
		f.channels = nil
		f.mu.Unlock()
		for _, id := range live {
			if ch := s.takeChannel(id); ch != nil {
				ch.finish(cause, nil)
			}
		}
		_ = f.pool.Close()
		s.mu.Lock()
		if s.forwards[f.id] == f {
			delete(s.forwards, f.id)
		}
		s.mu.Unlock()
	})
}

// track adds one channel to the listener's set. It answers false when the
// listener is already over — the set is nil'd by finish, and a nil map is the
// "closed" state rather than an error.
func (f *openForward) track(id proto.ChannelID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channels == nil {
		return false
	}
	f.channels[id] = struct{}{}
	return true
}

// forget drops one channel from the listener's set. A channel that ended on
// its own does this (openChannel.finish), so the set holds live streams and
// not a history of them; a nil map is the closed state and dropping from it is
// a no-op.
func (f *openForward) forget(id proto.ChannelID) {
	f.mu.Lock()
	delete(f.channels, id)
	f.mu.Unlock()
}

// validateForward refuses a listen this helper will not request, before
// anything is dialed.
func validateForward(p proto.ForwardParams) error {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return err
	}
	if p.Bind.Host == "" {
		return fmt.Errorf("%w: no bind host", errBadChannelParams)
	}
	// Port 0 is ALLOWED here, and it is the only place it is: asking the
	// server to allocate is the ordinary remote-forward request, and the
	// answer carries the port it allocated.
	if p.Bind.Port < 0 || p.Bind.Port > 65535 {
		return fmt.Errorf("%w: bind port %d", errBadChannelParams, p.Bind.Port)
	}
	return nil
}

// mintForwardID mints one listener's identity, in the same shape and for the
// same reasons mintChannelID mints a channel's.
func mintForwardID() (proto.ForwardID, error) {
	id, err := mintChannelID()
	if err != nil {
		return proto.ForwardID{}, err
	}
	return proto.ForwardID(id), nil
}

// listenerTarget reads the address the server bound.
//
// It is the ONE value the coordinator is told about the bind, and it is the
// transport's answer rather than a promise: a requested port 0 comes back as
// the allocated port, and a hostname bind comes back as whatever address the
// server chose. Both are stated at the wire's ForwardResult and disclosed by
// the coordinator's remote strategy; nothing here improves on the protocol.
func listenerTarget(ln net.Listener, requested proto.ChannelTarget) (proto.ChannelTarget, error) {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return proto.ChannelTarget{}, fmt.Errorf("listener address is %T, not a TCP address", ln.Addr())
	}
	host := requested.Host
	if addr.IP != nil && !addr.IP.IsUnspecified() {
		host = addr.IP.String()
	} else if addr.IP != nil {
		// The server bound every interface. Reporting the requested host here
		// would be a claim about a bind this end cannot see, so the
		// unspecified address is reported as itself and the caller's caveat
		// (which exists for exactly this) does the disclosing.
		host = addr.IP.String()
	}
	return proto.ChannelTarget{Host: host, Port: addr.Port}, nil
}

// peerOf is the far side's report of who connected, passed through for the log
// and for the coordinator's RemoteAddr. Empty when there is none — it is a
// fact about somebody else's machine and never an authentication.
func peerOf(c net.Conn) string {
	if c == nil || c.RemoteAddr() == nil {
		return ""
	}
	return c.RemoteAddr().String()
}

// lossCause turns a pooled connection's shutdown into the sentence a listener
// end carries. A clean close (the last reference released, which after finish
// is this process's own act) says nothing.
func lossCause(err error) string {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return ""
	}
	return err.Error()
}
