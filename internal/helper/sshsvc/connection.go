//go:build nocx_local_ssh

package sshsvc

// The connection half of the ssh service: the identity a pane binds to, and the
// stream of observations that keeps it current (design §5.3, §5.5, §5.6;
// stage 3).
//
// # Why the identity is derived and not minted
//
// A pane is bound to its connection BEFORE its shell exists, because the whole
// point is that the pane can say what is happening during the dial. So the
// coordinator asks for the connection's identity at the start of an open, and
// the helper answers the same key its own acquire will use — endpoint, account,
// credential principal and route, in ONE derivation (ssh.PooledSpec.Key). A
// minted id would need a table and would disagree with the pool's key under the
// first bug; a digest of the key cannot.
//
// # Why the watch starts with the connection and not with the spawn
//
// The dial can precede the spawn: the coordinator publishes the integration
// bundle over the same destination first, and that publish acquires the
// connection. A subscription installed by `spawn-ssh` would miss the handshake —
// possibly all of it, at a password prompt — and the pane's first news would be
// a shell that already exists. The `connection` op is the earliest request that
// names the destination, so the listener starts there, before anything dials.
//
// It is ONE subscription per (coordinator connection, connection). Panes share
// a connection, and a per-pane subscription would put every observation on the
// wire once per pane; the coordinator's sequence fence would drop the copies,
// but reading them is not free. The subscription ends when a write to that
// coordinator fails, which is the one event that says nobody is listening.

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/connkey"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// connection answers which connection this destination's shell will ride, and
// STARTS ITS OBSERVATIONS if this coordinator is not already listening.
//
// The subscription belongs here rather than to the spawn because the dial can
// start before the spawn: the coordinator publishes the shell-integration
// bundle over the same destination first, and that publish acquires the
// connection. A watch installed with the spawn would miss the handshake —
// possibly the whole of it, waiting at a password — and the pane would be told
// nothing until the shell already existed. The op is the earliest request that
// names the destination, so it is where the listener starts.
//
// ONE SUBSCRIPTION PER (COORDINATOR, CONNECTION). Panes share a connection;
// two subscriptions would put every observation on the wire twice, and the
// coordinator's sequence fence would drop the duplicate at the cost of reading
// it. The subscription ends when writing to that coordinator fails, which is
// the one event that says nobody is listening any more.
func (s *Service) connection(ctx context.Context, p proto.ConnectionParams) (proto.ConnectionResult, error) {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return proto.ConnectionResult{}, err
	}
	if err := validateIdentity(p.Destination.Identity); err != nil {
		return proto.ConnectionResult{}, err
	}
	key := s.rememberDestination(p.Destination)
	if conn, _ := host.ConnectionFrom(ctx).(*host.Host); conn != nil {
		s.ensureConnectionWatch(conn, key)
		if p.PaneID != "" {
			s.retainPane(conn, p.PaneID, key, p.ReconnectPolicy)
		}
	}
	return proto.ConnectionResult{ConnectionID: key.ID()}, nil
}

// connections answers every connection this helper holds, and subscribes this
// coordinator to the ones that appear later (nocx-oalo1.6.2; design §5.10,
// scenario 7).
//
// THE ANSWER AND THE SUBSCRIPTION ARE ONE ACT, and that is what makes a restart
// race-free: the list is read from the same table the subscription was installed
// on, so a connection that appears between the two is not lost — it is announced
// by the subscription — and one that appears before it is in the list. A caller
// that forgot to subscribe, or subscribed a moment later, would have a window in
// which a tunnel appeared and was never reported.
func (s *Service) connections(ctx context.Context) (proto.ConnectionsResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.ConnectionsResult{}, errNoAuthChannel
	}
	s.ensureConnectionsWatch(conn)
	facts := s.client.ConnectionFacts()
	out := proto.ConnectionsResult{Connections: make([]proto.ConnectionSummary, 0, len(facts))}
	for _, f := range facts {
		summary := proto.ConnectionSummary{
			Connection:         connectionEvent(f),
			HostKeyFingerprint: f.HostKeyFingerprint,
		}
		if destination, known := s.destinationOf(f.Key.ID()); known {
			summary.Destination = &destination
		}
		out.Connections = append(out.Connections, summary)
	}
	return out, nil
}

// destinationOf is the resolved destination this helper was asked to acquire a
// connection for, if it was ever asked with one.
func (s *Service) destinationOf(connectionID string) (proto.SSHDestination, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	destination, ok := s.destinations[connectionID]
	return destination, ok
}

// connectionWatchKey identifies one observation subscription: the coordinator
// connection it reports on, and the connection it reports about.
type connectionWatchKey struct {
	host       *host.Host
	connection string
}

// connectionWatch is one subscription's lifetime. The Watch call delivers its
// first observation SYNCHRONOUSLY, so a write that fails can happen before the
// cancel function exists; the holder records that and closes the subscription
// as soon as it can.
type connectionWatch struct {
	mu      sync.Mutex
	stop    func()
	stopped bool
	ready   chan struct{}
}

func (w *connectionWatch) set(stop func()) {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		stop()
		return
	}
	w.stop = stop
	w.mu.Unlock()
}

func (w *connectionWatch) close() func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	stop := w.stop
	w.stop = nil
	return stop
}

// ensureConnectionWatch starts this coordinator's subscription for one
// connection, if it has none.
func (s *Service) ensureConnectionWatch(conn *host.Host, key connkey.Key) {
	k := connectionWatchKey{host: conn, connection: key.ID()}
	s.mu.Lock()
	if s.connectionsWatches[conn] != nil {
		// THIS COORDINATOR ALREADY READS THE WHOLE TABLE, which covers every
		// connection including the ones created later. A second subscription for
		// this one would put each observation on the wire twice.
		s.mu.Unlock()
		return
	}
	if s.connectionWatches == nil {
		s.connectionWatches = make(map[connectionWatchKey]*connectionWatch)
	}
	if existing, ok := s.connectionWatches[k]; ok {
		// SOMEONE ALREADY LISTENS FOR THIS COORDINATOR. The earlier
		// subscription is the one; a second would duplicate every observation.
		s.mu.Unlock()
		<-existing.ready
		return
	}
	holder := &connectionWatch{ready: make(chan struct{})}
	s.connectionWatches[k] = holder
	s.mu.Unlock()

	stop := s.client.WatchConnection(key, func(f hostconn.Facts) {
		if err := s.sendConnectionEvent(conn, f); err != nil {
			s.log.Debug("ssh: a connection observation could not be sent",
				"connection", key.ID(), "error", err)
			s.forgetConnectionWatch(k, holder)
		}
	})
	holder.set(stop)
	close(holder.ready)
}

// ensureConnectionsWatch starts this coordinator's subscription to EVERY
// connection this helper holds, if it has none — the `connections` op's other
// half (nocx-oalo1.6.2).
//
// ONE SUBSCRIPTION PER COORDINATOR CONNECTION, not one per connection: the
// manager's table announces connections it creates later, which is the whole
// point — a tunnel that appears a second after the answer must not need a second
// request to be visible.
func (s *Service) ensureConnectionsWatch(conn *host.Host) {
	s.mu.Lock()
	if s.connectionsWatches == nil {
		s.connectionsWatches = make(map[*host.Host]*connectionWatch)
	}
	if existing, ok := s.connectionsWatches[conn]; ok {
		s.mu.Unlock()
		<-existing.ready
		return
	}
	holder := &connectionWatch{ready: make(chan struct{})}
	s.connectionsWatches[conn] = holder
	s.mu.Unlock()

	stop := s.client.WatchConnections(func(f hostconn.Facts) {
		if err := s.sendConnectionEvent(conn, f); err != nil {
			s.log.Debug("ssh: a connection observation could not be sent",
				"connection", f.Key.ID(), "error", err)
			s.forgetConnectionsWatch(conn, holder)
		}
	})
	holder.set(stop)
	close(holder.ready)
}

// sendConnectionEvent puts one observation on this coordinator's wire, and
// answers the write's error. One function for both subscriptions, because a
// wire that refuses one refuses the other: the frame is the same frame.
func (s *Service) sendConnectionEvent(conn *host.Host, f hostconn.Facts) error {
	return conn.SendNotification(proto.Notification{
		Service: proto.ServiceSSH,
		Event:   proto.EventConnectionState,
		Params:  connectionEvent(f),
	})
}

// forgetConnectionWatch ends and removes one subscription, if it is still the
// one installed (a failed write races nothing, but a replacement must not be
// stopped by its predecessor's error).
//
// IT RELEASES ONLY THE HOLDS RIDING THIS CONNECTION, and that is the whole
// difference a failed write makes. A pane's hold is its interest in ONE
// connection; releasing a hold on another connection because this one's event
// could not be written would end a connection a person's pane is still riding —
// and the driver then reads `ended`, drops the pane's binding and reports a
// connection the person never lost. What a failed write does say is that nobody
// is listening for THIS connection, which is exactly the set of holds this
// subscription covered.
func (s *Service) forgetConnectionWatch(k connectionWatchKey, holder *connectionWatch) {
	s.mu.Lock()
	if s.connectionWatches[k] != holder {
		s.mu.Unlock()
		return
	}
	delete(s.connectionWatches, k)
	var releases []func()
	for pane, held := range s.paneHolds {
		if pane.host == k.host && held.key.ID() == k.connection {
			releases = append(releases, held.release)
			delete(s.paneHolds, pane)
		}
	}
	s.mu.Unlock()
	for _, release := range releases {
		release()
	}
	if stop := holder.close(); stop != nil {
		stop()
	}
}

// forgetConnectionsWatch ends this coordinator's subscription to the whole
// table, when it is still the one installed.
//
// IT RELEASES EVERYTHING THAT COORDINATOR HELD, and unlike a per-connection
// failure that is the honest reading rather than a broad one: the subscription
// covered every connection, so a write that fails is a coordinator that is gone,
// and nothing it asked for has a reader. The per-connection watches it also has
// (installed before this op, when a helper was older than the table) end with
// it: they write to the same connection that just refused.
func (s *Service) forgetConnectionsWatch(conn *host.Host, holder *connectionWatch) {
	s.mu.Lock()
	if s.connectionsWatches[conn] != holder {
		s.mu.Unlock()
		return
	}
	delete(s.connectionsWatches, conn)
	var releases []func()
	for pane, held := range s.paneHolds {
		if pane.host == conn {
			releases = append(releases, held.release)
			delete(s.paneHolds, pane)
		}
	}
	var stops []func()
	for k, watch := range s.connectionWatches {
		if k.host != conn {
			continue
		}
		delete(s.connectionWatches, k)
		if stop := watch.close(); stop != nil {
			stops = append(stops, stop)
		}
	}
	s.mu.Unlock()
	for _, release := range releases {
		release()
	}
	for _, stop := range stops {
		stop()
	}
	if stop := holder.close(); stop != nil {
		stop()
	}
}

// disconnect closes the connection a destination rides, if this helper holds
// one, and answers what it found.
//
// IT DOES NOT WAIT FOR THE FAR SIDE. Closing a stalled transport can block on a
// write the peer never acknowledges, which is exactly the state a person presses
// Reconnect out of; the supervisor closes the socket and lets the guard and the
// watcher unwind, and this answers as soon as the connection has left the pool.
func (s *Service) disconnect(p proto.DisconnectParams) (proto.DisconnectResult, error) {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return proto.DisconnectResult{}, err
	}
	if err := validateIdentity(p.Destination.Identity); err != nil {
		return proto.DisconnectResult{}, err
	}
	key := s.rememberDestination(p.Destination)
	return proto.DisconnectResult{
		ConnectionID: key.ID(),
		Closed:       s.client.Disconnect(key),
	}, nil
}

// rememberDestination derives the connection key a destination's dial will use
// and keeps the destination under it, so the `connections` op can answer a
// coordinator that restarted with something it can redial.
//
// IT ANSWERS THE KEY, so every caller that needs one gets it from here and the
// derivation has exactly one spelling. Recording happens where the key is
// derived rather than in a store of its own: a destination that reached a
// connection and one that reached the table are the same destination, and a
// second call site for the record would be a second chance to forget.
func (s *Service) rememberDestination(d proto.SSHDestination) connkey.Key {
	key := connectionKey(d)
	s.mu.Lock()
	if s.destinations == nil {
		s.destinations = make(map[string]proto.SSHDestination)
	}
	s.destinations[key.ID()] = d
	s.mu.Unlock()
	return key
}

// connectionKey derives the pool/supervisor identity a destination's dial will
// use. It reads the destination and nothing else: no client configuration, no
// host-key callback, no credential — those are the dial's, and asking for them
// here would make naming a connection as expensive and as interruptible as
// making one.
func connectionKey(d proto.SSHDestination) connkey.Key {
	spec := ssh.PooledSpec{
		Host:     d.Host,
		Port:     d.Port,
		User:     d.User,
		Identity: identityKey(d.Identity),
	}
	for _, hop := range d.Jumps {
		spec.Route = append(spec.Route, ssh.PooledHop{
			Host: hop.Host, Port: hop.Port, User: hop.User,
			Identity: identityKey(hop.Identity),
		})
	}
	return spec.Key()
}

// connectionEvent renders one observation as the wire's payload. Every field
// the current state does not name is absent, which is what lets the
// coordinator's own schema be the exact shape it publishes.
func connectionEvent(f hostconn.Facts) proto.ConnectionStateEvent {
	ev := proto.ConnectionStateEvent{
		ConnectionID: f.Key.ID(),
		Seq:          f.Seq,
		Generation:   f.Generation,
		State:        f.State.Kind.String(),
		StateMs:      f.StateFor.Milliseconds(),
		PlainShells:  f.PlainShells,
	}
	switch f.State.Kind {
	case hostconn.Opening:
		ev.Stage = string(f.State.Stage)
	case hostconn.WaitingPerson:
		ev.Person = string(f.State.Wait)
	case hostconn.Failed, hostconn.Ended:
		ev.Cause = string(f.State.Cause)
	case hostconn.Reconnecting:
		// THE CAUSE RIDES A RECONNECTING STATE TOO, and it is load-bearing:
		// only a network-class loss is retried automatically (design §5.3), so a
		// coordinator that could not tell a lost link from a silence close would
		// either retry what a person decided or refuse to retry what nobody
		// did.
		ev.Attempt = f.State.Attempt
		ev.Cause = string(f.State.Cause)
	}
	if f.LookAnswered {
		// The wire carries an instant and hostconn carries an age: the clock
		// the supervisor runs on is monotonic and deliberately not wall time,
		// and this is the one reader that has to name a moment a person can
		// compare with their own watch.
		ev.LookAnsweredAt = time.Now().Add(-f.LookAnsweredFor).UTC().Format(time.RFC3339)
	}
	if f.Measured {
		ev.RoundTripMs = f.RoundTrip.Milliseconds()
		ev.Slow = f.Slow
	}
	return ev
}

// A pane's hold is scoped to its coordinator, not to a physical shell channel.
type paneHoldKey struct {
	host *host.Host
	pane string
}

func (s *Service) retainPane(conn *host.Host, pane string, key connkey.Key, policy string) {
	k := paneHoldKey{conn, pane}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paneHolds == nil {
		s.paneHolds = make(map[paneHoldKey]paneHold)
	}
	if old, ok := s.paneHolds[k]; ok {
		if old.key == key {
			return
		}
		old.release()
	}
	s.paneHolds[k] = paneHold{key: key, release: s.client.RetainConnection(key, policy)}
}

func (s *Service) releaseConnection(ctx context.Context, p proto.ReleaseConnectionParams) proto.ReleaseConnectionResult {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	k := paneHoldKey{conn, p.PaneID}
	s.mu.Lock()
	held := s.paneHolds[k]
	delete(s.paneHolds, k)
	s.mu.Unlock()
	if held.release != nil {
		held.release()
	}
	return proto.ReleaseConnectionResult{Released: held.release != nil}
}

type paneHold struct {
	key     connkey.Key
	release func()
}

// ConnectionClosed ends logical pane holds even if a healthy transport emits
// no further observations after its coordinator disconnects.
func (s *Service) ConnectionClosed(conn *host.Host) {
	s.mu.Lock()
	var stops []func()
	var releases []func()
	for key, watch := range s.connectionWatches {
		if key.host == conn {
			delete(s.connectionWatches, key)
			if watch.stop != nil {
				stops = append(stops, watch.stop)
			}
		}
	}
	if watch, ok := s.connectionsWatches[conn]; ok {
		delete(s.connectionsWatches, conn)
		if stop := watch.close(); stop != nil {
			stops = append(stops, stop)
		}
	}
	for key, hold := range s.paneHolds {
		if key.host == conn {
			delete(s.paneHolds, key)
			releases = append(releases, hold.release)
		}
	}
	s.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	for _, release := range releases {
		release()
	}
}
