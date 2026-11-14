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
	"context"
	"errors"
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

// Reconnect is the one action a person may take on this pane (design §5.6,
// §5.8). Its PRESENCE is the offer; EndsShells is how many shells would end if
// it is pressed, hops included (design §5.4).
type Reconnect struct {
	EndsShells int
}

// The refusals a press can meet, each a different sentence for the surface that
// sent it (the transport maps them to wire refusals).
var (
	// ErrUnknownPane is a reconnect for a pane this driver was never told about.
	ErrUnknownPane = errors.New("panedriver: no such pane")
	// ErrStaleEpoch is a press about a connection state that has moved on.
	ErrStaleEpoch = errors.New("panedriver: the connection epoch is stale")
	// ErrNoOffer means nothing on this pane can be pressed: a healthy
	// connection has no button, and a withdrawn one is not offered again by an
	// old frame.
	ErrNoOffer = errors.New("panedriver: no reconnect is offered for this pane")
	// ErrReconnecting is a second press while one reconnect is running.
	ErrReconnecting = errors.New("panedriver: a reconnect is already running for this pane")
)

// Attempt is the open order's state. Until the open order lands every member is
// `none`, and the values are carried so a later stage needs no new shape.
type Attempt struct {
	Shown     string
	Candidate string
	Reason    string
}

// Snapshot is one pane's observation, ready to be carried to the renderer.
type Snapshot struct {
	PaneID             string
	ConnectionID       string
	ConnectionEpoch    uint64
	ConnectionSequence uint64
	InstanceID         string
	SessionID          string
	Transport          Transport
	Quality            *Quality
	Attempt            Attempt
	// Reconnect is present exactly when the backend offers the person the
	// action on this observation.
	Reconnect  *Reconnect
	ObservedAt time.Time
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

	// source is the helper instance the cached observations came from. A
	// connection id is a destination digest and outlives a helper process,
	// while a sequence belongs to one supervisor's lifetime; without this
	// fence a replacement helper's observations would look stale until they
	// exceeded the old helper's last sequence.
	source string
	// action replaces shells after the connection supervisor restores transport.
	action         func(context.Context, string, uint64) error
	nextGeneration uint64
}

// binding is one pane's side: which connection it rides, how many observations
// it has been given, and the session a shell ended up in when one was adopted.
type binding struct {
	connectionID string
	epoch        uint64
	sessionID    string
	generation   uint64
	lost         bool
	ctx          context.Context
	cancel       context.CancelFunc
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
	d := &Driver{
		log:      logger,
		instance: instanceID,
		panes:    make(map[string]*binding),
		conns:    make(map[string]*connection),
	}
	return d
}

// SetAction installs the shell replacement callback. It runs once per held
// shell after a loss returns to connected. Its context ends when the pane
// closes; the open path must check it
// before registering the replacement shell. The supervisor owns retry policy.
func (d *Driver) SetAction(fn func(context.Context, string, uint64) error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.action = fn
	d.mu.Unlock()
}

// Unbind forgets one pane and cancels its shell replacement lifetime.
func (d *Driver) Unbind(paneID string) {
	if d == nil || paneID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if b := d.panes[paneID]; b != nil {
		b.cancel()
		if c := d.conns[b.connectionID]; c != nil {
			delete(c.panes, paneID)
		}
		delete(d.panes, paneID)
	}
}

// PaneClosed is the transport's seam name for Unbind: a pane left the layout,
// so its binding and replacement lifetime go with it.
func (d *Driver) PaneClosed(paneID string) { d.Unbind(paneID) }

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
// Every bind clears the old session, including a reconnect to the same
// connection. The returned generation belongs to this open and must accompany
// adoption. It is unique across pane closes, so an older open cannot adopt into
// a newly created pane with the same id.
func (d *Driver) Bind(paneID, connectionID string) uint64 {
	if d == nil || paneID == "" || connectionID == "" {
		return 0
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
		b.ctx, b.cancel = context.WithCancel(context.Background())
		d.panes[paneID] = b
	}
	b.connectionID = connectionID
	d.nextGeneration++
	b.generation = d.nextGeneration
	b.sessionID = ""
	b.lost = false
	generation := b.generation
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
		return generation
	}
	snap := d.snapshotLocked(paneID, b, c)
	d.mu.Unlock()
	d.emit(snap)
	return generation
}

// AdoptSession records a shell only if its open still owns the binding.
// Connection identity alone is insufficient: overlapping opens and reconnects
// can use the same connection id and complete in reverse order.
func (d *Driver) AdoptSession(paneID, connectionID, sessionID, instanceID string, generation uint64) {
	if d == nil || paneID == "" || connectionID == "" || sessionID == "" {
		return
	}
	d.mu.Lock()
	b := d.panes[paneID]
	if b == nil || b.connectionID != connectionID || b.generation != generation {
		// A pane that was never bound has no connection to name, and a pane
		// that has moved on is not the pane this shell belongs to.
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

// SetSource adopts the helper instance the driver is reading from. A CHANGED
// source is a replaced helper: its supervisors start their sequences at 1 under
// the same connection ids, so every cached observation is dropped and every
// automatic retry is cancelled. A pane binding while the new helper has said
// nothing yet publishes nothing — the driver no longer has a state to replay,
// which is the honest answer rather than the previous helper's.
func (d *Driver) SetSource(instance string) {
	if d == nil || instance == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if instance == d.source {
		return
	}
	first := d.source == ""
	d.source = instance
	if first {
		return
	}
	for _, c := range d.conns {
		c.seq = 0
		c.transport = Transport{}
		c.quality = nil
		c.observed = time.Time{}
	}
}

// ApplyFrom folds one connection observation into every pane bound to it.
//
// An observation that is not newer than the last applied for its connection is
// REFUSED: it describes an older moment, whatever order it arrived in, and
// applying it would move a pane's state backwards. That is the late-event rule
// design §5.6 states as "a late one is refused", at the connection's boundary
// rather than the pane's, because the sequence belongs to the connection.
// The observation belongs to the helper instance that reported it.
//
// An observation from a helper the driver has already replaced is REFUSED: its
// sequence was taken against a supervisor that no longer exists, and applying
// it would move a pane's state backwards under the new helper's name.
func (d *Driver) ApplyFrom(source string, ev proto.ConnectionStateEvent) {
	if d == nil || ev.ConnectionID == "" {
		return
	}
	transport, quality, err := fromEvent(ev)
	if err != nil {
		d.log.Warn("pane connection event refused", "connection", ev.ConnectionID, "error", err)
		return
	}
	d.mu.Lock()
	if source != "" {
		if d.source == "" {
			d.source = source
		} else if d.source != source {
			d.mu.Unlock()
			return
		}
	}
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
	type replacement struct {
		pane       string
		epoch      uint64
		binding    *binding
		ctx        context.Context
		generation uint64
	}
	var replacements []replacement
	for paneID := range c.panes {
		if b := d.panes[paneID]; b != nil {
			snaps = append(snaps, d.snapshotLocked(paneID, b, c))
			if ev.State == proto.ConnectionReconnecting || ev.State == proto.ConnectionEnded || ev.State == proto.ConnectionFailed {
				b.lost = b.lost || b.sessionID != ""
			}
			if ev.State == proto.ConnectionConnected && b.lost {
				b.lost = false
				replacements = append(replacements, replacement{paneID, b.epoch, b, b.ctx, b.generation})
			}
		}
	}
	action := d.action
	d.mu.Unlock()
	for _, snap := range snaps {
		d.emit(snap)
	}
	if action != nil {
		for _, r := range replacements {
			d.mu.Lock()
			live := d.panes[r.pane] == r.binding && r.binding.generation == r.generation && r.ctx.Err() == nil
			d.mu.Unlock()
			if live {
				go func() { _ = action(r.ctx, r.pane, r.epoch) }()
			}
		}
	}
}

// snapshotLocked mints the pane's next epoch and renders what it is owed. The
// epoch is minted at OBSERVATION and monotonic per pane, which is what lets the
// transport and the renderer refuse an older one.
func (d *Driver) snapshotLocked(paneID string, b *binding, c *connection) Snapshot {
	b.epoch++
	return Snapshot{
		PaneID:             paneID,
		ConnectionID:       b.connectionID,
		ConnectionEpoch:    b.epoch,
		ConnectionSequence: c.seq,
		InstanceID:         d.instance,
		SessionID:          b.sessionID,
		Transport:          c.transport,
		Quality:            c.quality,
		Attempt:            Attempt{Shown: "none", Candidate: "none"},
		Reconnect:          offerFor(c.transport),
		ObservedAt:         time.Now(),
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

// offerFor answers whether this observation offers the person a Reconnect, and
// what pressing it would end.
//
// THE OFFER DOES NOT DEPEND ON THE POLICY (design §5.4, §5.8). `ask` and
// `never` differ in what the backend does BY ITSELF, not in whether a person
// may choose; `auto` reconnects without asking and still shows the button while
// an attempt is in flight. So the rule is about the connection's state only:
//
//   - stalled with a plain shell: the flow has stopped answering and closing it
//     would end work, so the person is the one who decides (§5.3), and the
//     number they are told is the shells the connection carries, hops included.
//   - reconnecting, failed, ended: the connection is not usable; pressing is
//     the way out (§5.3, "Reconnect is the way out").
//   - opening, waiting_person, connected: nothing to reconnect.
func offerFor(t Transport) *Reconnect {
	switch t.State {
	case proto.ConnectionStalled:
		if t.HasPlainShells && t.PlainShells > 0 {
			return &Reconnect{EndsShells: t.PlainShells}
		}
		return nil
	case proto.ConnectionReconnecting, proto.ConnectionFailed, proto.ConnectionEnded:
		ends := 0
		if t.HasPlainShells {
			ends = t.PlainShells
		}
		return &Reconnect{EndsShells: ends}
	}
	return nil
}

// fromEvent converts the helper protocol's event into this package's neutral
// state, refusing a state or an accompanying field the contract does not admit.
func fromEvent(ev proto.ConnectionStateEvent) (Transport, *Quality, error) {
	// THE FIELDS ARE TAKEN PER STATE, and a field the state does not own is
	// REFUSED rather than ignored. A flat struct makes that a real possibility —
	// a connected event carrying a stage decodes perfectly — and copying it
	// would publish a renderer payload the stricter pane.connection schema
	// forbids, from a helper payload its own schema accepted.
	t := Transport{State: ev.State, StateMs: ev.StateMs}
	forbidden := func(fields map[string]string) error {
		for value, name := range fields {
			if value != "" {
				return fmt.Errorf("%s carries %s", ev.State, name)
			}
		}
		return nil
	}
	ownStage, ownPerson, ownCause := "", "", ""
	switch ev.State {
	case proto.ConnectionOpening:
		if ev.Stage == "" {
			return Transport{}, nil, fmt.Errorf("opening with no stage")
		}
		if err := forbidden(map[string]string{ev.Person: "person", ev.Cause: "cause"}); err != nil {
			return Transport{}, nil, err
		}
		if ev.Attempt != 0 {
			return Transport{}, nil, fmt.Errorf("opening carries attempt")
		}
		ownStage = ev.Stage
	case proto.ConnectionWaitingPerson:
		if ev.Person == "" {
			return Transport{}, nil, fmt.Errorf("waiting_person with no person")
		}
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Cause: "cause"}); err != nil {
			return Transport{}, nil, err
		}
		if ev.Attempt != 0 {
			return Transport{}, nil, fmt.Errorf("waiting_person carries attempt")
		}
		ownPerson = ev.Person
	case proto.ConnectionConnected:
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Person: "person", ev.Cause: "cause"}); err != nil {
			return Transport{}, nil, err
		}
		if ev.Attempt != 0 {
			return Transport{}, nil, fmt.Errorf("connected carries attempt")
		}
		t.PlainShells, t.HasPlainShells = ev.PlainShells, true
	case proto.ConnectionStalled:
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Person: "person", ev.Cause: "cause"}); err != nil {
			return Transport{}, nil, err
		}
		if ev.Attempt != 0 {
			return Transport{}, nil, fmt.Errorf("stalled carries attempt")
		}
		t.PlainShells, t.HasPlainShells = ev.PlainShells, true
	case proto.ConnectionReconnecting:
		if ev.Attempt < 1 {
			return Transport{}, nil, fmt.Errorf("reconnecting with attempt %d", ev.Attempt)
		}
		if ev.Cause != "lost-link" && ev.Cause != "silence" {
			return Transport{}, nil, fmt.Errorf("reconnecting with invalid cause %q", ev.Cause)
		}
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Person: "person"}); err != nil {
			return Transport{}, nil, err
		}
		ownCause = ev.Cause
	case proto.ConnectionFailed, proto.ConnectionEnded:
		valid := false
		if ev.State == proto.ConnectionFailed {
			valid = ev.Cause == "host-key-changed" || ev.Cause == "auth-rejected" || ev.Cause == "no-auth-method"
		} else {
			valid = ev.Cause == "lost-link" || ev.Cause == "released" || ev.Cause == "silence" || ev.Cause == "tainted"
		}
		if !valid {
			return Transport{}, nil, fmt.Errorf("%s with invalid cause %q", ev.State, ev.Cause)
		}
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Person: "person"}); err != nil {
			return Transport{}, nil, err
		}
		if ev.Attempt != 0 {
			return Transport{}, nil, fmt.Errorf("%s carries attempt", ev.State)
		}
		ownCause = ev.Cause
	default:
		return Transport{}, nil, fmt.Errorf("state %q is not a connection state", ev.State)
	}
	t.Stage, t.Person, t.Cause, t.Attempt = ownStage, ownPerson, ownCause, ev.Attempt
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
