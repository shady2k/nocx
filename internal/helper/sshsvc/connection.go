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
// # Why the watch is per pane and not per connection
//
// A connection can carry several panes, and each pane's own spawn says which
// coordinator connection should hear about it. The subscription therefore
// belongs to the spawn, is taken before the first acquire (the pane listeners
// dial before the shell), and ends when the session ends. Duplicate
// observations of one connection are harmless: the coordinator drops a
// sequence it has already applied, and that is cheaper than a
// subscription-with-refcount table this generation does not need.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/connkey"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// connection answers which connection this destination's shell will ride.
//
// It dials nothing, resolves no secret and asks no person: the key is a
// function of the resolved destination the coordinator already sent, and the
// derivation is the pool's own. An address that cannot name a connection is
// refused with the same vocabulary a probe refuses an incomplete destination
// with, because "this cannot be dialled" and "this cannot be named" are one
// question at this layer.
func (s *Service) connection(p proto.ConnectionParams) (proto.ConnectionResult, error) {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return proto.ConnectionResult{}, err
	}
	if err := validateIdentity(p.Destination.Identity); err != nil {
		return proto.ConnectionResult{}, err
	}
	return proto.ConnectionResult{ConnectionID: connectionKey(p.Destination).ID()}, nil
}

// Watch begins reporting one pane's connection to the coordinator that asked,
// and answers the function that ends the report.
//
// It must be called BEFORE the first acquire for the pane: the pane listeners
// dial before the shell does, and the stages that matter most are the first
// ones. The listener receives the state the connection is already in at once,
// so a pane joining a connection somebody else dialled is not blank.
func (s *Service) Watch(ctx context.Context, d proto.SSHDestination) (func(), error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		// A pane's spawn is coordinator-initiated, so this is the re-dial case:
		// there is nowhere to send an observation, and the honest answer is the
		// same refusal the other ops give rather than a silent stream.
		return nil, errNoAuthChannel
	}
	if err := validateDestinationAddress(d); err != nil {
		return nil, err
	}
	if err := validateIdentity(d.Identity); err != nil {
		return nil, err
	}
	key := connectionKey(d)
	return s.client.WatchConnection(key, func(f hostconn.Facts) {
		if err := conn.SendNotification(proto.Notification{
			Service: proto.ServiceSSH,
			Event:   proto.EventConnectionState,
			Params:  connectionEvent(f),
		}); err != nil {
			s.log.Debug("ssh: a connection observation could not be sent",
				"connection", string(key.ID()), "error", err)
		}
	}), nil
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
		ev.Attempt = f.State.Attempt
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
