package client

// The coordinator's end of a REMOTE LISTENER: a socket the helper asked the
// far host to open, and the connections that arrive on it.
//
// # Why this is not a ChannelStream
//
// A ChannelStream is a stream the coordinator ASKED for and holds. A Forward
// is a stream FACTORY: the coordinator asks for a listener, and the
// connections that arrive on it are somebody else's act — a shell connecting
// to a loopback port on its own host, a client reaching a forwarded port.
// Nobody here opened them, so there is no response to name them, and the id
// arrives as an announcement instead (proto.EventForwardedTCPIP).
//
// What the two DO share is everything after that point: an accepted connection
// is an ordinary proxied channel — same identity kind, same data frames, same
// close, same end-of-stream notification — so it is registered in the same
// table and read through the same ChannelStream. This type owns the LISTENER's
// lifetime and nothing else; the bytes on it are a channel like any other.
//
// # The announcement is the whole ordering argument, again
//
// A channel's id is learned from the open's response, and this file's is
// learned from a notification. Both arrive on the same wire as the data frames
// they describe, and the helper writes the announcement under the same mutex
// it writes the first byte with — so on this side the notification is always
// processed before the bytes. What is NOT guaranteed is that the CALLER has
// accepted by then, which is why the stream is registered by the announcement
// and not by Accept: bytes for a stream nobody has accepted yet are queued on
// the stream itself (the same queue an sfpt handshake's first bytes land in),
// and the accept queue only carries streams, never bytes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ErrForwardClosed is returned by a Forward whose listener is over: this side
// closed it, the helper reported it gone, or the connection carrying it died.
// It is not ErrLost — the connection may be perfectly healthy — and the
// distinction is what lets a caller tell "this listener ended" from "the
// helper is gone".
var ErrForwardClosed = errors.New("helper: the forward is closed")

// forwardAcceptBacklog bounds the connections waiting to be accepted by a
// caller.
//
// It is a BOUND and not a queue with a policy: past it the connection is
// closed and logged, because the reader loop must never wait on a consumer (a
// read loop that blocks on one caller wedges every session on the helper) and
// because a listener whose owner has stopped accepting is a listener nobody
// wants the connections of. Sixty-four is far above what the callers in this
// repository hold open at once (the lifecycle adapter accepts up to its
// candidate bound) and far below anything that would matter as memory.
const forwardAcceptBacklog = 64

// parkedForwardEvents bounds the announcements parked for a listener whose
// `ssh.forward` call has not returned yet.
//
// It is the data plane's `parkedChannels` one op over, and its bound is the same
// shape and for the same reason: the window is one round trip (the read loop
// reaches the announcement before the goroutine waiting on the response has
// woken), so a bound that could hold a stream would be a bound that hides a
// listener nobody ever claimed. Sixty-four announced connections inside a
// single round trip is not a caller — it is a helper announcing for a listener
// the coordinator is never going to register, and past the bound those are
// closed, which is what happens today to all of them.
const parkedForwardEvents = 64

// parkedForwardTotal bounds the park ACROSS listeners, which is the half that
// makes it a bound rather than a policy: an id nobody claims is a key in a map,
// and a helper that announced for a thousand of them would grow it. The channel
// park states the same thing in bytes (parkedChannelBytes) for the same reason.
const parkedForwardTotal = 256

// Forward is one remote listener, as seen by the coordinator that asked for
// it.
type Forward struct {
	client *Client
	id     proto.ForwardID
	bind   proto.ChannelTarget

	accepted chan *ChannelStream
	done     chan struct{}

	mu     sync.Mutex
	closed bool
	cause  error
	// peers carries each announced connection's reported remote address to
	// the Accept that will hand it over, and is emptied by Peer.
	peers map[*ChannelStream]string
}

// OpenForward asks the helper for one listener and returns it.
//
// The listen itself happens on the helper's side of the wire and may be
// refused by the far host — a server with AllowTcpForwarding off, or a bind
// outside PermitListen — so a failure here is the SERVER's refusal, carried
// back as the helper classified it, and never a local error.
func (c *Client) OpenForward(ctx context.Context, params proto.ForwardParams) (*Forward, error) {
	if params.Bind.Host == "" {
		return nil, errors.New("helper: open forward: no bind host")
	}
	var result proto.ForwardResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpForward, params, &result); err != nil {
		return nil, err
	}
	if result.Forward.IsZero() {
		return nil, errors.New("helper: open forward: the helper answered no forward id")
	}
	f := &Forward{
		client:   c,
		id:       result.Forward,
		bind:     result.Bind,
		accepted: make(chan *ChannelStream, forwardAcceptBacklog),
		done:     make(chan struct{}),
		peers:    make(map[*ChannelStream]string),
	}
	c.mu.Lock()
	if _, exists := c.forwards[result.Forward]; exists {
		c.mu.Unlock()
		// The helper minted an id it is already serving. Telling it so is the
		// only honest answer: leaving it would be a listener the helper holds
		// for a caller that has forgotten it. The close is BOUNDED for the
		// reason every close here is — this branch runs while the caller waits
		// for an answer, and a helper gone quiet must not turn a protocol
		// mistake into a hang.
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		_ = c.Call(ctx, proto.ServiceSSH, proto.OpUnforward, proto.UnforwardParams{Forward: result.Forward}, nil)
		return nil, fmt.Errorf("helper: the helper reused forward id %s", result.Forward)
	}
	c.forwards[result.Forward] = f
	// Whatever was announced while this call was in flight goes in FIRST, in
	// the order it arrived, for the reason the channel park exists: the
	// announcement and this registration race, and the loser's cost is the
	// FIRST connection somebody made to a listener that is working perfectly.
	parked := c.parkedForwards[result.Forward]
	delete(c.parkedForwards, result.Forward)
	c.mu.Unlock()
	for _, ev := range parked {
		c.acceptAnnounced(f, ev)
	}
	return f, nil
}

// ID is the wire identity of this listener, for a log line or a test.
func (f *Forward) ID() proto.ForwardID { return f.id }

// Done closes when this listener is over, whoever ended it. It is the signal a
// caller that is not sitting in Accept needs: the remote lifecycle adapter's
// own loss watcher is exactly that shape.
func (f *Forward) Done() <-chan struct{} { return f.done }

// Peer is the far side's report of who connected on this stream, empty when
// the helper announced none. It is read ONCE per accepted stream, and the
// announcement that carried it is the only source: nothing here authenticates
// it (the lifecycle capability does that) and it is a label for a log line and
// for RemoteAddr.
func (f *Forward) Peer(s *ChannelStream) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	peer := f.peers[s]
	delete(f.peers, s)
	return peer
}

// CloseCause is why the listener is over, or nil: nil while it is still
// serving, nil when the coordinator's own unforward ended it, and the helper's
// sentence when it ended for a reason nobody here asked for.
func (f *Forward) CloseCause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cause
}

// Bind is the address the SERVER bound, as the helper reported it. A requested
// port 0 comes back allocated; a hostname bind comes back as the address the
// server chose, which is why every caller that shows it to a person discloses
// that it is not a verified bind (internal/tunnel's remote strategy has said
// so since nocx-wzc4.1).
func (f *Forward) Bind() proto.ChannelTarget { return f.bind }

// Accept hands over the next connection that arrived on the listener, as a
// stream of its own. It returns ErrForwardClosed (or the helper's own cause)
// once the listener is over, and ErrLost when the connection carrying it died.
func (f *Forward) Accept() (*ChannelStream, error) {
	for {
		select {
		case s := <-f.accepted:
			if s != nil {
				return s, nil
			}
		case <-f.done:
			// Drained first, closed second: a connection that arrived before
			// the listener ended is still owed to the caller, and reporting
			// the end first would lose it. The same rule ChannelStream.Read
			// applies to bytes, applied to connections.
			select {
			case s := <-f.accepted:
				if s != nil {
					return s, nil
				}
			default:
			}
			f.mu.Lock()
			cause := f.cause
			f.mu.Unlock()
			if cause != nil {
				return nil, cause
			}
			return nil, ErrForwardClosed
		case <-f.client.done:
			f.mu.Lock()
			cause := f.cause
			f.mu.Unlock()
			if cause != nil {
				return nil, cause
			}
			return nil, fmt.Errorf("%w: %v", ErrLost, f.client.lostErr)
		}
	}
}

// Close ends the listener: the helper is told to cancel the remote listen, the
// registration is dropped, and every waiter is unblocked. It is idempotent,
// and the request is best-effort — a Close whose helper never answers must
// still release the caller, whose next act is usually to exit.
func (f *Forward) Close() error {
	if f.close() {
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		return f.client.unforward(ctx, f.id)
	}
	return nil
}

// close marks the listener over, once, and reports whether THIS call is the
// one that did it.
func (f *Forward) close() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.closed = true
	close(f.done)
	return true
}

// finish ends the listener from the client's side: the registration is
// dropped, waiters are unblocked, and the streams already accepted are left
// alone — their own end arrives as a channel-closed notification, which is
// what lets a caller keep reading a connection that was accepted before the
// listener went away.
func (c *Client) forgetForward(id proto.ForwardID, cause error) {
	c.mu.Lock()
	f := c.forwards[id]
	delete(c.forwards, id)
	c.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.cause == nil {
		f.cause = cause
	}
	f.mu.Unlock()
	f.close()
}

// unforward asks the helper to end the listener. It is the wire half of
// Close, named so that the bounded call site is written once.
func (c *Client) unforward(ctx context.Context, id proto.ForwardID) error {
	if id.IsZero() {
		return errors.New("helper: unforward: no forward id")
	}
	return c.Call(ctx, proto.ServiceSSH, proto.OpUnforward, proto.UnforwardParams{Forward: id}, nil)
}

// forwardAccept queues one announced connection for its listener's caller.
//
// A listener nobody is accepting for is BOUNDED rather than blocked: the
// connection is closed, which answers the far side with an end instead of
// leaving it connected to a socket nobody reads. The stream is registered
// before this runs, so the close reaches the far side through the ordinary
// path.
func (f *Forward) forwardAccept(s *ChannelStream) {
	select {
	case f.accepted <- s:
	default:
		f.client.log.Warn("forwarded connection dropped: the caller is not accepting",
			"forward", f.id.String(), "channel", s.ID().String())
		_ = s.Close()
	}
}

// forwardedTCPIP routes one ssh.forwarded-tcpip announcement: a connection
// arrived on a listener, and this is the channel its bytes are keyed by.
//
// An id for a listener this client has never heard of is PARKED rather than
// refused, and the reason is not the one the channel park has — it is the same
// ordering seen from the other side. The announcement and the response that
// names the listener are two frames on one wire, and the reader loop can reach
// the first before the goroutine waiting on the second has registered anything;
// dropping it would cost the first connection to a forward that is otherwise
// working, which for the remote lifecycle channel is the shell's only attempt.
// Past the park's bound the connection is closed, which is what a listener
// nobody will ever claim deserves.
func (c *Client) forwardedTCPIP(raw []byte) {
	var ev proto.ForwardedTCPIPEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.log.Warn("malformed forwarded-tcpip notification", "err", err)
		return
	}
	if ev.Channel.IsZero() || ev.Forward.IsZero() {
		c.log.Warn("forwarded-tcpip dropped: the notification names no listener or channel",
			"forward", ev.Forward.String(), "channel", ev.Channel.String())
		return
	}
	c.mu.Lock()
	if f := c.forwards[ev.Forward]; f != nil {
		c.mu.Unlock()
		c.acceptAnnounced(f, ev)
		return
	}
	parked := c.parkedForwards[ev.Forward]
	if len(parked) >= parkedForwardEvents || parkedForwardCount(c.parkedForwards) >= parkedForwardTotal {
		c.mu.Unlock()
		c.log.Warn("forwarded-tcpip dropped: nothing has claimed this listener and the park is full",
			"forward", ev.Forward.String(), "channel", ev.Channel.String())
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		_ = c.tellHelperChannelClosed(ctx, ev.Channel)
		return
	}
	c.parkedForwards[ev.Forward] = append(parked, ev)
	c.mu.Unlock()
	c.log.Debug("forwarded-tcpip parked until its forward returns",
		"forward", ev.Forward.String(), "channel", ev.Channel.String())
}

// parkedForwardCount is the park's size across every listener.
func parkedForwardCount(parked map[proto.ForwardID][]proto.ForwardedTCPIPEvent) int {
	total := 0
	for _, events := range parked {
		total += len(events)
	}
	return total
}

// acceptAnnounced hands one announced connection to its listener: the channel
// is claimed, the peer the far side reported is recorded, and the stream joins
// the accept queue.
//
// It is ONE function for the two paths that reach it — the live announcement
// and the drain of what a pending open parked — because a second copy would be
// a second place for the peer to be recorded after the stream is queued, or not
// at all.
func (c *Client) acceptAnnounced(f *Forward, ev proto.ForwardedTCPIPEvent) {
	s, err := c.claimChannel(ev.Channel)
	if err != nil {
		c.log.Warn("forwarded-tcpip dropped: the channel id is already in use",
			"forward", ev.Forward.String(), "channel", ev.Channel.String())
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		_ = c.tellHelperChannelClosed(ctx, ev.Channel)
		return
	}
	// The peer is recorded BEFORE the stream reaches the accept queue: the
	// caller may Accept and ask for it immediately, and the map is the only
	// place the announcement's peer survives.
	f.mu.Lock()
	if f.peers == nil {
		f.peers = make(map[*ChannelStream]string)
	}
	f.peers[s] = ev.Peer
	f.mu.Unlock()
	c.log.Debug("forwarded connection arrived",
		"forward", ev.Forward.String(), "channel", ev.Channel.String(), "peer", ev.Peer)
	f.forwardAccept(s)
}

// forwardClosed routes one ssh.forward-closed notification: the listener is
// gone. The cause is empty when the coordinator itself asked (its own
// unforward), and the helper's sentence when the far side's connection died
// under it — which is the case a -R forward and the remote lifecycle channel
// both have to hear, because both hold an Accept that would otherwise wait for
// ever.
func (c *Client) forwardClosed(raw []byte) {
	var ev proto.ForwardClosedEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.log.Warn("malformed forward-closed notification", "err", err)
		return
	}
	var cause error = ErrForwardClosed
	if ev.Error != "" {
		cause = fmt.Errorf("helper: forward closed: %s", ev.Error)
	}
	c.forgetForward(ev.Forward, cause)
}
