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
	"errors"
	"fmt"
	"math/rand/v2"
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

// Reconnect policies, spelling settings.SSHReconnect's values. They are restated
// here rather than imported because this package must not depend on the settings
// store: the policy is a value the composition root reads, not a store this
// package queries.
const (
	PolicyAuto  = "auto"
	PolicyAsk   = "ask"
	PolicyNever = "never"
)

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
	PaneID          string
	ConnectionID    string
	ConnectionEpoch uint64
	InstanceID      string
	SessionID       string
	Transport       Transport
	Quality         *Quality
	Attempt         Attempt
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
	// policy answers ssh.reconnect's value. Nil is never, the conservative
	// answer: nothing reconnects by itself until the composition root says so.
	policy func() string
	// action carries a reconnect out. It is the transport's one path, and this
	// package decides WHEN it is called — that is the whole split.
	action func(paneID string, epoch uint64) error
	// schedule runs fn after d, answering a cancel. It is injected so the
	// fault harness's clock is the test's, never a sleep's.
	schedule func(d time.Duration, fn func()) (cancel func())
	// jitter perturbs a backoff, so a hundred panes dropped by one router
	// restart do not all redial in the same millisecond.
	jitter func(time.Duration) time.Duration
	// inFlight is the panes a reconnect is being carried out for, whoever
	// asked. The automatic retry must not start beside a person's press.
	inFlight map[string]bool
	// autos is the pending automatic retry of each pane, so a newer
	// observation can cancel it, and autoSeq identifies the one that is live:
	// a timer cancelled as it fired may still run, and only the newest may
	// act.
	autos   map[string]func()
	autoSeq map[string]uint64
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

// Option configures a Driver.
type Option func(*Driver)

// WithPolicy is the value of ssh.reconnect, read per decision rather than
// captured at construction: a person who changes it must change what happens
// to the NEXT dropped pane.
func WithPolicy(policy func() string) Option {
	return func(d *Driver) {
		if policy != nil {
			d.policy = policy
		}
	}
}

// New builds the driver over one backend instance.
func New(instanceID string, logger log.Logger, opts ...Option) *Driver {
	if logger == nil {
		logger = log.NewSlogAdapter(nil)
	}
	d := &Driver{
		log:      logger,
		instance: instanceID,
		panes:    make(map[string]*binding),
		conns:    make(map[string]*connection),
		schedule: func(delay time.Duration, fn func()) func() {
			return afterFunc(delay, fn)
		},
		jitter:   jitterBy20Percent,
		inFlight: make(map[string]bool),
		autos:    make(map[string]func()),
		autoSeq:  make(map[string]uint64),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// SetAction installs the one function that carries a reconnect out. It is a
// setter because the transport is built after the opener that opens panes, the
// same late-binding the publisher uses; until it is set, a reconnect is not
// attempted by itself.
func (d *Driver) SetAction(fn func(paneID string, epoch uint64) error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.action = fn
	d.mu.Unlock()
}

// ReconnectStarted and ReconnectFinished bracket an action somebody else is
// carrying out — the transport's answer to a person's press — so the automatic
// retry does not start beside it.
func (d *Driver) ReconnectStarted(paneID string) {
	if d == nil || paneID == "" {
		return
	}
	d.mu.Lock()
	d.inFlight[paneID] = true
	if cancel := d.autos[paneID]; cancel != nil {
		cancel()
		delete(d.autos, paneID)
	}
	d.mu.Unlock()
}

// Unbind forgets one pane. It is the close's half of Bind, and it drops the
// automatic retry with the binding: an observation of a connection nobody's
// pane rides any more has nobody to be about.
func (d *Driver) Unbind(paneID string) {
	if d == nil || paneID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if b := d.panes[paneID]; b != nil {
		if c := d.conns[b.connectionID]; c != nil {
			delete(c.panes, paneID)
		}
		delete(d.panes, paneID)
	}
	if cancel := d.autos[paneID]; cancel != nil {
		cancel()
		delete(d.autos, paneID)
	}
	delete(d.autoSeq, paneID)
	delete(d.inFlight, paneID)
}

// PaneClosed is the transport's seam name for Unbind: a pane left the layout,
// so its binding and any automatic retry go with it.
func (d *Driver) PaneClosed(paneID string) { d.Unbind(paneID) }

// ReconnectFinished ends the interval ReconnectStarted opened.
func (d *Driver) ReconnectFinished(paneID string) {
	if d == nil || paneID == "" {
		return
	}
	d.mu.Lock()
	delete(d.inFlight, paneID)
	d.mu.Unlock()
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
		// THE PREVIOUS CONNECTION'S SHELL IS NOT THIS ONE'S. A pane that moves
		// to another connection has no session there until an adoption names
		// one, and carrying the old id would publish a session on the wrong
		// connection for as long as the new one takes to adopt.
		b.sessionID = ""
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
	d.autoLocked(paneID)
	d.mu.Unlock()
	d.emit(snap)
}

// AdoptSession records the session a pane's shell became, and publishes it
// beside the connection's current state.
//
// It is a separate event rather than a field of every observation: the session
// id exists once, when a shell is adopted, and repeating it on every later
// observation would make a state that did not change look like one that had.
// AdoptSession records the session a pane's shell became, FOR THE CONNECTION
// the caller opened it on, and publishes it beside the connection's current
// state.
//
// IT IS REFUSED WHEN THE PANE NO LONGER RIDES THAT CONNECTION, and that refusal
// is the point: an adoption is the second half of an open that can be overtaken
// — a pane rebound to another connection while the first open was still
// adopting — and accepting the late half would overwrite the new binding with
// the old shell under a freshly minted epoch, which nothing downstream could
// tell from the truth.
func (d *Driver) AdoptSession(paneID, connectionID, sessionID, instanceID string) {
	if d == nil || paneID == "" || connectionID == "" || sessionID == "" {
		return
	}
	d.mu.Lock()
	b := d.panes[paneID]
	if b == nil || b.connectionID != connectionID {
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
	d.autoLocked(paneID)
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
	for paneID, cancel := range d.autos {
		cancel()
		delete(d.autos, paneID)
	}
}

// Apply folds one connection observation into every pane bound to it.
//
// An observation that is not newer than the last applied for its connection is
// REFUSED: it describes an older moment, whatever order it arrived in, and
// applying it would move a pane's state backwards. That is the late-event rule
// design §5.6 states as "a late one is refused", at the connection's boundary
// rather than the pane's, because the sequence belongs to the connection.
// ApplyFrom folds one connection observation into every pane bound to it, as
// reported by one helper instance.
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
	for paneID := range c.panes {
		if b := d.panes[paneID]; b != nil {
			snaps = append(snaps, d.snapshotLocked(paneID, b, c))
			d.autoLocked(paneID)
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
		Reconnect:       offerFor(c.transport),
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

// The automatic retry's backoff (design §5.3): capped at fifteen seconds of
// waiting between attempts, with no attempt limit — the cap replaced the old
// three-attempt one.
const (
	autoBackoffBase = 500 * time.Millisecond
	autoBackoffCap  = 15 * time.Second
)

// autoLocked cancels this pane's pending automatic retry and, when the state
// still warrants one, schedules it. Called with the mutex held, after the
// pane's observation was produced.
func (d *Driver) autoLocked(paneID string) {
	if cancel := d.autos[paneID]; cancel != nil {
		cancel()
		delete(d.autos, paneID)
	}
	if d.action == nil || d.policy == nil || d.policy() != PolicyAuto {
		return
	}
	b := d.panes[paneID]
	if b == nil || d.inFlight[paneID] {
		return
	}
	c := d.conns[b.connectionID]
	if c == nil || !retryable(c.transport) {
		return
	}
	attempt := c.transport.Attempt
	if attempt < 1 {
		attempt = 1
	}
	d.autoSeq[paneID]++
	seq := d.autoSeq[paneID]
	d.autos[paneID] = d.schedule(d.backoff(attempt), func() {
		d.mu.Lock()
		live := d.autoSeq[paneID] == seq
		if live {
			delete(d.autos, paneID)
		}
		d.mu.Unlock()
		if live {
			d.runAuto(paneID)
		}
	})
}

// afterFunc is the production scheduler: one timer, cancellable.
func afterFunc(delay time.Duration, fn func()) func() {
	timer := time.AfterFunc(delay, fn)
	return func() { timer.Stop() }
}

// jitterBy20Percent spreads a backoff by up to a fifth either way. A hundred
// panes dropped by one router restart must not redial in the same millisecond,
// and a retry storm is the one thing a reconnect policy can make worse.
func jitterBy20Percent(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	spread := int64(delay) / 5
	if spread <= 0 {
		return delay
	}
	//nolint:gosec // a retry delay is not a secret: predictability here only spreads one host's retries
	return delay + time.Duration(rand.Int64N(2*spread+1)-spread)
}

// retryable says whether the backend may put this connection back by itself:
// the transport ended under a held connection (network-class), and not a
// refusal a retry cannot change, nor the policy's own silence close.
func retryable(t Transport) bool {
	return t.State == proto.ConnectionReconnecting && t.Cause == causeLostLink
}

// causeLostLink is hostconn.CauseLostLink's spelling on the wire; restated here
// rather than imported for the same reason the state names are.
const causeLostLink = "lost-link"

// backoff is the wait before the next automatic attempt: exponential from half
// a second, capped at fifteen seconds, then jittered.
func (d *Driver) backoff(attempt int) time.Duration {
	delay := autoBackoffBase
	for i := 1; i < attempt && delay < autoBackoffCap; i++ {
		delay *= 2
	}
	if delay > autoBackoffCap {
		delay = autoBackoffCap
	}
	return d.jitter(delay)
}

// runAuto carries one automatic attempt out, if it is still warranted when the
// timer fires. An observation that replaced the timer has advanced the pane's
// sequence and that older closure never reaches here.
func (d *Driver) runAuto(paneID string) {
	d.mu.Lock()
	if d.inFlight[paneID] || d.action == nil || d.policy == nil || d.policy() != PolicyAuto {
		d.mu.Unlock()
		return
	}
	b := d.panes[paneID]
	if b == nil {
		d.mu.Unlock()
		return
	}
	c := d.conns[b.connectionID]
	if c == nil || !retryable(c.transport) {
		d.mu.Unlock()
		return
	}
	// The epoch at the moment of the attempt is what a press would have
	// carried; the transport does not re-check it on the automatic path.
	epoch := b.epoch
	d.inFlight[paneID] = true
	action := d.action
	d.mu.Unlock()

	err := action(paneID, epoch)
	if err != nil {
		d.log.Debug("panedriver: the automatic reconnect did not open a shell", "pane", paneID, "error", err)
	}
	d.mu.Lock()
	delete(d.inFlight, paneID)
	d.mu.Unlock()
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
		if ev.Cause == "" {
			return Transport{}, nil, fmt.Errorf("reconnecting with no cause")
		}
		if err := forbidden(map[string]string{ev.Stage: "stage", ev.Person: "person"}); err != nil {
			return Transport{}, nil, err
		}
		ownCause = ev.Cause
	case proto.ConnectionFailed, proto.ConnectionEnded:
		if ev.Cause == "" {
			return Transport{}, nil, fmt.Errorf("%s with no cause", ev.State)
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
