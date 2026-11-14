// Package panedriver is the coordinator's binding between a pane and the SSH
// connection it rides (design §5.1, §5.6; nocx-oalo1.5.3).
//
// # What it owns, and what it does not
//
// The HELPER owns a connection's state: it dials, it probes, it decides. The
// TRANSPORT owns the wire: it knows which renderer opened a pane and it refuses
// an epoch it has already moved past. This package owns the one fact neither of
// them can: WHICH connection a pane rides, and therefore what that pane is told
// when the connection says something.
//
// A pane is bound at open, before the shell exists (design §5.5), using the
// identity the helper answers for the resolved destination. Several panes may
// share one connection and one connection may serve several panes; an
// observation of the connection becomes one observation of each pane, each with
// its own epoch. A pane that is not bound to a connection has nothing to be
// told, and an observation of a connection no pane rides is recorded for the
// next pane that binds to it.
//
// # Why it imports nothing from the transport or the connection
//
// The design's §5.1.1 sets the direction: this package consumes the helper
// protocol's neutral event and produces a neutral snapshot, and the composition
// root adapts the snapshot to the transport's wire shape. Importing the wire
// would put the renderer's vocabulary inside the party that decides what is
// true, and importing internal/ssh would build the SSH client into tests that
// need to assert a mapping.
package panedriver

import (
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
)

// Transport is one state of a connection, in the neutral terms the wire's
// transport object uses. Only the fields the state makes true are set.
type Transport struct {
	State          string
	Stage          string
	Person         string
	Cause          string
	Attempt        int
	StateMs        int64
	LookAnsweredAt time.Time
	HasLook        bool
	PlainShells    int
	HasPlainShells bool
}

// Quality is the round-trip measurement and its grade.
type Quality struct {
	RoundTripMs int64
	Slow        bool
}

// Attempt is the open order's state. Until the open order lands every member is
// `none`, and the values are carried so a later stage needs no new shape.
type Attempt struct {
	Shown     string
	Candidate string
	Reason    string
}

// Snapshot is one pane's observation, ready to be carried to the renderer.
type Snapshot struct {
	PaneID          string
	ConnectionID    string
	ConnectionEpoch uint64
	InstanceID      string
	SessionID       string
	Transport       Transport
	Quality         *Quality
	Attempt         Attempt
	ObservedAt      time.Time
}

// Driver maps connection observations to the panes bound to them.
type Driver struct {
	log log.Logger

	mu       sync.Mutex
	instance string
	publish  func(Snapshot)
	// panes is every pane this coordinator has bound, keyed by the renderer's
	// own id. Its size is bounded by what a person opened.
	panes map[string]*binding
	// conns is the newest observation of each connection any pane has been
	// bound to. Keeping it is what lets a pane binding AFTER the dial learn
	// the state the connection is already in — the ordinary case for a second
	// pane on a host that is already connected.
	conns map[string]*connection
}

// binding is one pane's side: which connection it rides, how many observations
// it has been given, and the session a shell ended up in when one was adopted.
type binding struct {
	connectionID string
	epoch        uint64
	sessionID    string
}

// connection is the newest observation of one connection, with the panes
// currently bound to it.
type connection struct {
	seq       uint64
	transport Transport
	quality   *Quality
	observed  time.Time
	panes     map[string]struct{}
}

// New builds the driver over one backend instance.
func New(instanceID string, logger log.Logger) *Driver {
	if logger == nil {
		logger = log.NewSlogAdapter(nil)
	}
	return &Driver{
		log:      logger,
		instance: instanceID,
		panes:    make(map[string]*binding),
		conns:    make(map[string]*connection),
	}
}

// SetPublisher installs the one function snapshots leave through. It is a
// setter because the transport is built after the opener that opens panes, the
// same late-binding the other open-path seams use; until it is set, snapshots
// are produced and dropped, which is the truth for a backend nobody is attached
// to.
func (d *Driver) SetPublisher(fn func(Snapshot)) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.publish = fn
	d.mu.Unlock()
}

// Bind records that a pane rides a connection, and answers the observation the
// pane is owed right now: the connection's current state if it has one, or the
// opening the pane was just bound for.
//
// binding again is ordinary — a pane that reopens gets a new connection — and
// the pane's epoch keeps increasing, so the renderer refuses the older one even
// if it is still in flight.
func (d *Driver) Bind(paneID, connectionID string) {
	if d == nil || paneID == "" || connectionID == "" {
		return
	}
	d.mu.Lock()
	b := d.panes[paneID]
	if b != nil && b.connectionID != connectionID {
		if old := d.conns[b.connectionID]; old != nil {
			delete(old.panes, paneID)
		}
	}
	if b == nil {
		b = &binding{}
		d.panes[paneID] = b
	}
	b.connectionID = connectionID
	c := d.conns[connectionID]
	if c == nil {
		c = &connection{panes: make(map[string]struct{})}
		d.conns[connectionID] = c
	}
	c.panes[paneID] = struct{}{}
	// NOTHING IS PUBLISHED FOR A CONNECTION WITH NO OBSERVATION YET. The
	// helper's first observation of a fresh connection is `opening`, and it
	// arrives on the subscription the spawner takes a moment later; inventing
	// one here would be the coordinator saying what it does not know, and a
	// zero-state notification is a shape the wire refuses anyway.
	if c.transport.State == "" {
		d.mu.Unlock()
		return
	}
	snap := d.snapshotLocked(paneID, b, c)
	d.mu.Unlock()
	d.emit(snap)
}

// AdoptSession records the session a pane's shell became, and publishes it
// beside the connection's current state.
//
// It is a separate event rather than a field of every observation: the session
// id exists once, when a shell is adopted, and repeating it on every later
// observation would make a state that did not change look like one that had.
func (d *Driver) AdoptSession(paneID, sessionID, instanceID string) {
	if d == nil || paneID == "" || sessionID == "" {
		return
	}
	d.mu.Lock()
	b := d.panes[paneID]
	if b == nil {
		// A pane that was never bound has no connection to name, and an
		// observation with an empty connectionId is a shape the wire does not
		// admit.
		d.mu.Unlock()
		return
	}
	b.sessionID = sessionID
	c := d.conns[b.connectionID]
	if c == nil {
		c = &connection{panes: map[string]struct{}{paneID: {}}}
		d.conns[b.connectionID] = c
	}
	if instanceID != "" && d.instance == "" {
		d.instance = instanceID
	}
	if c.transport.State == "" {
		d.mu.Unlock()
		return
	}
	snap := d.snapshotLocked(paneID, b, c)
	d.mu.Unlock()
	d.emit(snap)
}

// Apply folds one connection observation into every pane bound to it.
//
// An observation that is not newer than the last applied for its connection is
// REFUSED: it describes an older moment, whatever order it arrived in, and
// applying it would move a pane's state backwards. That is the late-event rule
// design §5.6 states as "a late one is refused", at the connection's boundary
// rather than the pane's, because the sequence belongs to the connection.
func (d *Driver) Apply(ev proto.ConnectionStateEvent) {
	if d == nil || ev.ConnectionID == "" {
		return
	}
	transport, quality, err := fromEvent(ev)
	if err != nil {
		d.log.Warn("pane connection event refused", "connection", ev.ConnectionID, "error", err)
		return
	}
	d.mu.Lock()
	c := d.conns[ev.ConnectionID]
	if c == nil {
		c = &connection{panes: make(map[string]struct{})}
		d.conns[ev.ConnectionID] = c
	}
	if ev.Seq <= c.seq {
		d.mu.Unlock()
		return
	}
	c.seq = ev.Seq
	c.transport, c.quality, c.observed = transport, quality, time.Now()
	snaps := make([]Snapshot, 0, len(c.panes))
	for paneID := range c.panes {
		if b := d.panes[paneID]; b != nil {
			snaps = append(snaps, d.snapshotLocked(paneID, b, c))
		}
	}
	d.mu.Unlock()
	for _, snap := range snaps {
		d.emit(snap)
	}
}

// snapshotLocked mints the pane's next epoch and renders what it is owed. The
// epoch is minted at OBSERVATION and monotonic per pane, which is what lets the
// transport and the renderer refuse an older one.
func (d *Driver) snapshotLocked(paneID string, b *binding, c *connection) Snapshot {
	b.epoch++
	return Snapshot{
		PaneID:          paneID,
		ConnectionID:    b.connectionID,
		ConnectionEpoch: b.epoch,
		InstanceID:      d.instance,
		SessionID:       b.sessionID,
		Transport:       c.transport,
		Quality:         c.quality,
		Attempt:         Attempt{Shown: "none", Candidate: "none"},
		ObservedAt:      time.Now(),
	}
}

// emit hands one snapshot to the transport, outside the lock: a publisher that
// took this mutex would let a slow socket block every pane's state.
func (d *Driver) emit(s Snapshot) {
	d.mu.Lock()
	publish := d.publish
	d.mu.Unlock()
	if publish != nil {
		publish(s)
	}
}

// fromEvent converts the helper protocol's event into this package's neutral
// state, refusing a state or an accompanying field the contract does not admit.
func fromEvent(ev proto.ConnectionStateEvent) (Transport, *Quality, error) {
	t := Transport{
		State:   ev.State,
		Stage:   ev.Stage,
		Person:  ev.Person,
		Cause:   ev.Cause,
		Attempt: ev.Attempt,
		StateMs: ev.StateMs,
	}
	switch ev.State {
	case proto.ConnectionOpening:
		if ev.Stage == "" {
			return Transport{}, nil, fmt.Errorf("opening with no stage")
		}
	case proto.ConnectionWaitingPerson:
		if ev.Person == "" {
			return Transport{}, nil, fmt.Errorf("waiting_person with no person")
		}
	case proto.ConnectionConnected, proto.ConnectionStalled:
		t.PlainShells, t.HasPlainShells = ev.PlainShells, true
	case proto.ConnectionReconnecting:
		if ev.Attempt < 1 {
			return Transport{}, nil, fmt.Errorf("reconnecting with attempt %d", ev.Attempt)
		}
	case proto.ConnectionFailed, proto.ConnectionEnded:
		if ev.Cause == "" {
			return Transport{}, nil, fmt.Errorf("%s with no cause", ev.State)
		}
	default:
		return Transport{}, nil, fmt.Errorf("state %q is not a connection state", ev.State)
	}
	if ev.LookAnsweredAt != "" {
		at, err := time.Parse(time.RFC3339, ev.LookAnsweredAt)
		if err != nil {
			return Transport{}, nil, fmt.Errorf("lookAnsweredAt %q: %w", ev.LookAnsweredAt, err)
		}
		t.LookAnsweredAt, t.HasLook = at, true
	}
	var quality *Quality
	if ev.RoundTripMs > 0 || ev.Slow {
		quality = &Quality{RoundTripMs: ev.RoundTripMs, Slow: ev.Slow}
	}
	return t, quality, nil
}
