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
	key := connectionKey(p.Destination)
	if conn, _ := host.ConnectionFrom(ctx).(*host.Host); conn != nil {
		s.ensureConnectionWatch(conn, key)
	}
	return proto.ConnectionResult{ConnectionID: key.ID()}, nil
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
	if s.connectionWatches == nil {
		s.connectionWatches = make(map[connectionWatchKey]*connectionWatch)
	}
	if _, ok := s.connectionWatches[k]; ok {
		// SOMEONE ALREADY LISTENS FOR THIS COORDINATOR. The earlier
		// subscription is the one; a second would duplicate every observation.
		s.mu.Unlock()
		return
	}
	holder := &connectionWatch{}
	s.connectionWatches[k] = holder
	s.mu.Unlock()

	stop := s.client.WatchConnection(key, func(f hostconn.Facts) {
		err := conn.SendNotification(proto.Notification{
			Service: proto.ServiceSSH,
			Event:   proto.EventConnectionState,
			Params:  connectionEvent(f),
		})
		if err == nil {
			return
		}
		s.log.Debug("ssh: a connection observation could not be sent",
			"connection", key.ID(), "error", err)
		s.forgetConnectionWatch(k, holder)
	})
	holder.set(stop)
}

// forgetConnectionWatch ends and removes one subscription, if it is still the
// one installed (a failed write races nothing, but a replacement must not be
// stopped by its predecessor's error).
func (s *Service) forgetConnectionWatch(k connectionWatchKey, holder *connectionWatch) {
	s.mu.Lock()
	if s.connectionWatches[k] != holder {
		s.mu.Unlock()
		return
	}
	delete(s.connectionWatches, k)
	s.mu.Unlock()
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
	key := connectionKey(p.Destination)
	return proto.DisconnectResult{
		ConnectionID: key.ID(),
		Closed:       s.client.Disconnect(key),
	}, nil
}

// Disconnect is the spawner's half of the op: the same close, asked with the
// destination the request carried rather than over the wire. One implementation
// behind both, so a helper-internal reopen cannot close a different connection
// from the one the wire op names.
func (s *Service) Disconnect(_ context.Context, d proto.SSHDestination) (bool, error) {
	res, err := s.disconnect(proto.DisconnectParams{Destination: d})
	return res.Closed, err
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
