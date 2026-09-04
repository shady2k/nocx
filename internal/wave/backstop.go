package wave

// The sixth thing the record holds: UNDISPATCHED FACTS AND THEIR DEADLINES
// (§6 of the 2026-08-24 orchestration mechanism design), and the two routes
// out of that set — the coordinator by a wake (D14), the human by a deadline
// (D2).
//
// # Why a per-fact deadline and not a lease
//
// A lease times the SUPERVISOR, so it runs during a wave that is entirely
// healthy and silent, and it cannot tell an idle coordinator with nothing to
// do from an absent one. This times the WORK: a deadline exists because a
// fact needs judgement, so an empty set means no timer is running at all.
// That is the whole of D2's advantage, and it is checkable — Open() is empty
// and no alarm is armed are the same statement here.
//
// # The wake is unacknowledged, so it closes nothing
//
// Seeing our text echo in a pane's input region is evidence it was typed,
// never that it was acted on. A fact therefore leaves this set only when the
// coordinator FETCHES it — D8 keeps four acknowledgements distinct and
// advances the cursor on the second — and the fetch is the coordinator's own
// call. A wake that could not be delivered is recorded with its reason and is
// never reported as sent; the deadline is still running under it, which is
// what makes a refused wake an escalation rather than a loss.
//
// # The set is in memory, deliberately
//
// The record's other five things are rows in the encrypted store. This one is
// not, and the argument is the startup sweep's own: a restart terminalizes
// every open participant as interrupted, because the worker died with the
// backend that held it. A fact that survived the restart would need judgement
// about a participant the same restart has already judged, and the
// coordinator it would be dispatched to is gone with its run. What the human
// is told about the restart ITSELF is open question §10.7 and is not this.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// FactKind names which of the two facts entered the set. There are exactly
// two because there are exactly two facts (D9); nothing read off a screen
// enters here, and the grid cannot reach this file at all.
type FactKind string

const (
	// FactDeclared is the participant's own account of its work.
	FactDeclared FactKind = "declared"
	// FactExited is the process fact.
	FactExited FactKind = "exited"
)

// Fact is one thing that needs judgement and has not been dispatched.
type Fact struct {
	Participant ParticipantID
	Wave        ID
	// CoordinatorSession is who must judge it. It is carried ON the fact
	// rather than looked up when the alarm fires: by then the wave may hold
	// nothing non-terminal, and the answer would be gone exactly when it is
	// needed.
	CoordinatorSession string
	Kind               FactKind
	// State is what the participant's record became. It is what the wake
	// names, and it is the record's own word rather than a second
	// vocabulary.
	State State
	// Task is what the participant was given, so an escalation can say what
	// the human is being asked about without reading anything back.
	Task      string
	EnteredAt time.Time
	// DueAt is when this fact reaches the human if nobody dispatched it. Both
	// ends of the interval are named; the NUMBER is injected, because a
	// deadline wrong in either direction breaks the backstop and §10.8 puts
	// its measurement in nocx-dkawo.4.
	DueAt time.Time
	// Wake is what happened when nocx tried to reach the coordinator. A
	// refusal keeps its reason here, which is what makes "recorded with its
	// reason rather than reported as sent" a property of the record and not
	// of a log line.
	Wake WakeOutcome
	// AlsoOwed is how many OTHER facts of the same wave were undispatched at
	// the moment this one reached the human. It exists because escalation
	// coalesces per wave: five workers finishing while the coordinator is
	// away must produce one card that says five, not five cards.
	AlsoOwed int
	// Escalated is set when the deadline elapsed and the human was reached.
	// The fact stays open afterwards and the alarm is not re-armed: it has
	// reached somebody, and re-arming would turn a backstop into a repeat.
	Escalated bool
}

// WakeOutcome is what one attempt to start a turn came to. Delivered is
// deliberately not "sent": the only outcome that sets it is a submission the
// pane accepted, and every other shape — a modal on screen, a pane nocx is
// not watching, a queue that refused — is a refusal carrying its reason.
type WakeOutcome struct {
	Attempted bool
	Delivered bool
	Reason    string
}

// Waker starts a turn the coordinator did not ask for.
//
// It is a seam because reaching a coordinator means TYPING INTO A PANE, and
// that decision belongs to internal/agenttyping, on frames it reads itself.
// This package reads no screen and imports no grid; it says who must be woken
// and about what, and never whether the screen permits it.
type Waker interface {
	// Wake types text into the coordinator's pane. It returns what happened
	// and must never report a refusal as a delivery.
	Wake(ctx context.Context, coordinatorSession, text string) WakeOutcome
}

// Escalation reaches the human about a fact nobody dispatched. It is the
// backstop's far end, and it is a seam for the same reason the Waker is:
// which surfaces a notification may reach, and under what trust, belongs to
// internal/notify and is not restated here (§6.1).
type Escalation interface {
	Escalate(ctx context.Context, f Fact)
}

// Alarms is the one-shot timer seam. It is injected so a test can assert the
// thing D2 actually claims — that NOTHING is armed while nothing is
// undispatched — and so no test here waits on a duration to see a deadline
// fire.
type Alarms interface {
	// After runs f once, d from now. The returned cancel is safe to call
	// after f has already run.
	After(d time.Duration, f func()) (cancel func())
}

// systemAlarms is the product's clock.
type systemAlarms struct{}

func (systemAlarms) After(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// defaultFactDeadline is how long a fact may sit undispatched before the
// human is told. It is a placeholder with both ends named, not a measured
// value: §10.8 says a number wrong in either direction breaks the backstop —
// too short and a thinking coordinator is escalated past, too long and the
// human learns late — and that it probably differs by fact class. The
// composition root overrides it; nocx-dkawo.4 measures it.
const defaultFactDeadline = 5 * time.Minute

// Stats is what the mechanism costs, counted rather than assumed.
//
// §12 of the design names the number it is judged by: what fraction of facts
// reaches the HUMAN rather than the coordinator. If most escalate, the
// mechanism moved the work to a person and should say so out loud instead of
// being described as orchestration. That number cannot be argued from a
// design, so the record counts it.
type Stats struct {
	// Routine is facts recorded that woke nobody, because the coordinator
	// had nothing to decide about them yet.
	Routine int
	// Judgement is facts that entered the undispatched set.
	Judgement int
	// Woken is wakes DELIVERED — never attempted, because an attempt that a
	// pane refused reached nobody.
	Woken int
	// Escalated is facts that reached the human. This over Facts() is the
	// fraction §12 asks for: coalescing means five facts can reach a person
	// in one card, and all five DID reach them, because the card says five.
	Escalated int
	// Cards is how many times a person was interrupted. It is a different
	// number from Escalated on purpose — one measures whether the mechanism
	// is doing the work, the other measures what it costs someone's
	// attention, and a design can be wrong in either direction alone.
	Cards int
	// Dispatched is facts the coordinator fetched for itself.
	Dispatched int
}

// Facts is every fact the record has routed, which is the denominator of the
// fraction.
func (s Stats) Facts() int { return s.Routine + s.Judgement }

// waveState is the per-wave half of the routing decision.
//
// The deadline stays PER FACT (D2) — nothing about coalescing changes what is
// being timed. What coalesces is the two things that cost somebody something:
// a wake costs the coordinator a turn, and an escalation costs a person their
// attention, and neither should be spent twice for one situation.
type waveState struct {
	// owed is how many facts of this wave are undispatched.
	owed int
	// changed is closed when ANY fact about this wave is admitted — routine
	// or judgement — and replaced with a fresh channel. A waiter that holds
	// one cannot miss an entry that happened between two waits, because what
	// it holds is the channel of the moment it started waiting.
	//
	// ROUTINE FACTS SIGNAL TOO, and that is the whole difference between
	// this and the wake. The routing table decides whether to spend a
	// coordinator's TURN, and a routine completion is not worth one; a wait
	// is a turn ALREADY SPENT, and "the first of three settles" is exactly
	// the routine case. A wait that woke only on judgement facts would sit
	// through the event it was opened for.
	changed chan struct{}
	// woken records that a wake was DELIVERED for this wave and the
	// coordinator has not fetched since. A REFUSED wake deliberately does not
	// set it: a refusal told the coordinator nothing, and the next fact is a
	// fresh chance to catch a pane that is waiting for input.
	woken bool
	// escalated records that the human has been told about this wave and has
	// not seen it cleared. A backstop that repeats is an alarm clock.
	escalated bool
}

// factKey is (participant, kind): one participant produces at most one of
// each fact, and a repeat of one it already owes judgement on is the same
// fact rather than a second one.
type factKey struct {
	participant ParticipantID
	kind        FactKind
}

// Backstop holds the undispatched facts and is what makes invariant 3 of §5
// true: a fact that needs judgement reaches somebody within a named deadline.
type Backstop struct {
	log      log.Logger
	wake     Waker
	escalate Escalation
	alarms   Alarms
	deadline time.Duration
	now      func() time.Time

	mu     sync.Mutex
	open   map[factKey]*Fact
	cancel map[factKey]func()
	waves  map[ID]*waveState
	stats  Stats
}

// BackstopOption configures a Backstop. The DEADLINE is the only thing the
// product varies, so it is the only option: the alarm and the clock are
// replaced by this package's own tests, in-package and by assignment, which
// keeps the exported surface the size of what the product actually uses.
type BackstopOption func(*Backstop)

// WithFactDeadline sets how long a fact may sit undispatched before the human
// is told. The composition root names it, because a deadline wrong in either
// direction breaks the backstop and the product's real values live there.
func WithFactDeadline(d time.Duration) BackstopOption {
	return func(b *Backstop) { b.deadline = d }
}

// NewBackstop wires the set to its two routes.
//
// A nil waker or a nil escalation is PERMITTED and is not a silent degrade: a
// fact still enters, the missing route is recorded on the fact as a refusal
// naming itself, and the log says so at Error. That is the same stance the
// supervisor takes with an unwired destination, and for the same reason —
// dropping a fact because a route was not built is how a feature that does
// not exist survives a release.
func NewBackstop(lg log.Logger, w Waker, e Escalation, opts ...BackstopOption) *Backstop {
	b := &Backstop{
		log:      lg,
		wake:     w,
		escalate: e,
		alarms:   systemAlarms{},
		deadline: defaultFactDeadline,
		now:      time.Now,
		open:     make(map[factKey]*Fact),
		cancel:   make(map[factKey]func()),
		waves:    make(map[ID]*waveState),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Routine records a fact the coordinator has nothing to decide about yet.
//
// It is one half of the routing table and it is deliberately a METHOD rather
// than an early return inside Entered: a fact that woke nobody is still a
// fact, and it is the denominator of the number §12 judges this design by. A
// routing table whose routine branch left no trace could not report what
// fraction of facts reached anybody.
func (b *Backstop) Routine(f Fact) {
	b.mu.Lock()
	b.stats.Routine++
	// A routine fact wakes nobody and still SETTLES something, so anything
	// waiting on this wave is told.
	b.announce(f.Wave)
	b.mu.Unlock()
	b.log.Debug("wave: a fact was recorded and nobody was woken",
		"participant", string(f.Participant), "wave", string(f.Wave),
		"kind", string(f.Kind), "state", string(f.State))
}

// Entered admits a fact that needs judgement.
//
// The order inside is the point. The alarm is armed BEFORE the wake is
// attempted, because a wake types into a pane and can take as long as a pane
// takes: a deadline armed afterwards would not be running during the one
// operation most likely to be slow. And the wake happens outside the lock,
// because nothing about typing into one coordinator's pane should hold the
// record of another wave's facts.
func (b *Backstop) Entered(ctx context.Context, f Fact) {
	key := factKey{participant: f.Participant, kind: f.Kind}
	f.EnteredAt = b.now()
	f.DueAt = f.EnteredAt.Add(b.deadline)

	b.mu.Lock()
	if _, already := b.open[key]; already {
		// The same fact about the same participant. Coalesced rather than
		// re-armed: a second alarm for one fact would escalate it twice, and
		// a second wake inside the window is the repeat every delivery route
		// drops anyway.
		b.mu.Unlock()
		return
	}
	b.open[key] = &f
	b.cancel[key] = b.alarms.After(b.deadline, func() { b.due(key) })
	b.stats.Judgement++
	b.announce(f.Wave)
	ws := b.waveOf(f.Wave)
	ws.owed++
	// One wake per wave per undispatched run. The coordinator's answer to a
	// wake is wave.holdings, which returns EVERYTHING its session holds — so
	// a second wake before it has fetched would spend a turn to say what the
	// first turn was already going to show. A refused wake does not count,
	// because it said nothing.
	alreadyWoken := ws.woken
	b.mu.Unlock()

	if alreadyWoken {
		// Recorded rather than left blank. Every fact carries why it was or
		// was not woken about; a zero outcome here would read as "nothing
		// happened", which is the one thing that did not.
		b.mu.Lock()
		if cur, still := b.open[key]; still {
			cur.Wake = WakeOutcome{
				Reason: "the coordinator was already woken for this wave and has not fetched since",
			}
		}
		b.mu.Unlock()
		b.log.Debug("wave: the coordinator is already awake for this wave",
			"participant", string(f.Participant), "wave", string(f.Wave))
		return
	}

	out := b.attempt(ctx, f)

	b.mu.Lock()
	if cur, still := b.open[key]; still {
		cur.Wake = out
	}
	if out.Delivered {
		b.stats.Woken++
		b.waveOf(f.Wave).woken = true
	}
	b.mu.Unlock()
}

// waveOf returns the wave's coalescing state, creating it if this is its first
// undispatched fact. The caller holds the lock.
func (b *Backstop) waveOf(id ID) *waveState {
	ws, ok := b.waves[id]
	if !ok {
		ws = &waveState{changed: make(chan struct{})}
		b.waves[id] = ws
	}
	return ws
}

// Changed returns the channel that closes when the next fact about this wave
// is admitted. A caller takes it BEFORE reading the record and selects on it
// afterwards, which is what makes the gap between the two harmless: a fact
// admitted in that gap has already closed the channel the caller holds.
func (b *Backstop) Changed(id ID) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waveOf(id).changed
}

// Owed is how many facts of this wave are undispatched. It is read BEFORE a
// fetch and never after: the fetch is what clears them, so asking afterwards
// always answers nothing.
func (b *Backstop) Owed(id ID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	ws, ok := b.waves[id]
	if !ok {
		return 0
	}
	return ws.owed
}

// announce wakes everything waiting on this wave and arms the next wait. The
// caller holds the lock.
func (b *Backstop) announce(id ID) {
	ws := b.waveOf(id)
	close(ws.changed)
	ws.changed = make(chan struct{})
}

// attempt tries to start the coordinator's turn and reports honestly.
func (b *Backstop) attempt(ctx context.Context, f Fact) WakeOutcome {
	if b.wake == nil {
		b.log.Error("wave: a fact needs judgement and this backend has nothing to wake a coordinator with",
			"participant", string(f.Participant), "wave", string(f.Wave), "kind", string(f.Kind))
		return WakeOutcome{Reason: "this backend has no way to type into a pane"}
	}
	out := b.wake.Wake(ctx, f.CoordinatorSession, wakeText(f))
	out.Attempted = true
	if out.Delivered {
		b.log.Info("wave: the coordinator was woken",
			"participant", string(f.Participant), "session_id", f.CoordinatorSession, "kind", string(f.Kind))
	} else {
		// Not an error: refusing to type into a pane showing a modal is the
		// mechanism working. What would be a defect is calling it a
		// delivery, and the reason is recorded on the fact either way.
		b.log.Info("wave: the coordinator was not woken, and the fact stays undispatched",
			"participant", string(f.Participant), "session_id", f.CoordinatorSession,
			"kind", string(f.Kind), "reason", out.Reason)
	}
	return out
}

// wakeText is what nocx types into the coordinator's pane.
//
// It carries a POINTER and never the participant's own content. A
// declaration's summary is free text from the participant — the record says
// so where Declaration is defined — and typing it into another agent's input
// region would be prompt injection performed with our own hands. Naming the
// participant and its state is also what makes every wake distinct, which the
// vendor routes §7.4 costs all require and which costs nothing here.
func wakeText(f Fact) string {
	// The KIND and the state, because they answer different questions and a
	// state alone answers neither well: a declaration leaves a running worker
	// live, so "your worker is live" would be the one sentence that reads as
	// nothing having happened at the exact moment something did.
	happened := "has reported"
	if f.Kind == FactExited {
		happened = "has ended"
	}
	return fmt.Sprintf("nocx: your worker %s %s and is %s. Call wave.holdings to see what your session holds.",
		f.Participant, happened, f.State)
}

// due is the deadline elapsing on one fact.
//
// The fact is marked escalated whether or not the human is told, because it
// HAS reached the end of its deadline and re-arming is not a thing this does.
// Whether a card appears is the wave's decision, not the fact's.
func (b *Backstop) due(key factKey) {
	b.mu.Lock()
	f, still := b.open[key]
	if !still || f.Escalated {
		b.mu.Unlock()
		return
	}
	f.Escalated = true
	b.stats.Escalated++
	ws := b.waveOf(f.Wave)
	snapshot := *f
	snapshot.AlsoOwed = ws.owed - 1
	repeat := ws.escalated
	ws.escalated = true
	if !repeat {
		b.stats.Cards++
	}
	b.mu.Unlock()

	if repeat {
		// The human has a card for this wave already and has not seen it
		// cleared. Five workers finishing while the coordinator is away is
		// one situation, and five cards for it is how an attention surface
		// becomes noise — which is the failure the attention queue's own
		// bead warns about in its first paragraph.
		b.log.Info("wave: another fact went undispatched, and the human already has this wave",
			"participant", string(snapshot.Participant), "wave", string(snapshot.Wave),
			"also_owed", snapshot.AlsoOwed)
		return
	}
	if b.escalate == nil {
		b.log.Error("wave: a fact went undispatched past its deadline and this backend has nothing to tell anyone with",
			"participant", string(snapshot.Participant), "wave", string(snapshot.Wave),
			"kind", string(snapshot.Kind))
		return
	}
	stats := b.Stats()
	b.log.Warn("wave: a fact went undispatched past its deadline and is reaching the human",
		"participant", string(snapshot.Participant), "wave", string(snapshot.Wave),
		"kind", string(snapshot.Kind), "also_owed", snapshot.AlsoOwed,
		"wake_delivered", snapshot.Wake.Delivered,
		"wake_refused_because", snapshot.Wake.Reason,
		// The number §12 judges the design by, on the one line that is
		// written exactly when it moves. A fraction nobody reports is a
		// fraction nobody measures.
		"escalated", stats.Escalated, "of_facts", stats.Facts(), "cards", stats.Cards)
	// context.Background of nothing: an alarm has no caller's context to
	// inherit, and the escalation is the last thing that will happen about
	// this fact.
	b.escalate.Escalate(context.Background(), snapshot)
}

// Dispatched removes every open fact about these participants and stops their
// alarms. This is the FETCH — the coordinator has been told — and it is the
// only thing that closes a fact. A wake does not, because delivery is
// unacknowledged.
func (b *Backstop) Dispatched(ids ...ParticipantID) {
	if len(ids) == 0 {
		return
	}
	told := make(map[ParticipantID]bool, len(ids))
	for _, id := range ids {
		told[id] = true
	}
	var settled []ID
	b.mu.Lock()
	for key, cancel := range b.cancel {
		if !told[key.participant] {
			continue
		}
		open, ok := b.open[key]
		if !ok {
			// open and cancel are written and deleted together, so this is
			// unreachable; it is guarded rather than asserted because a nil
			// dereference under this lock would take the whole record down.
			continue
		}
		wave := open.Wave
		cancel()
		delete(b.cancel, key)
		delete(b.open, key)
		b.stats.Dispatched++
		if ws, ok := b.waves[wave]; ok {
			ws.owed--
			if ws.owed <= 0 {
				// The wave owes nothing, so the next fact starts fresh: it
				// may wake the coordinator again, and it may raise a new
				// card. Coalescing suppresses a REPEAT of a situation the
				// person still has in front of them, never the next one.
				// The state is RESET rather than deleted: deleting it
				// would drop the channel every current waiter is holding,
				// and waveOf would mint a fresh one that the next announce
				// closes — so a waiter that started before the fetch would
				// never be woken again.
				ws.woken = false
				ws.escalated = false
				ws.owed = 0
				// And the number §12 judges the design by is written down at
				// the moment a wave settles, not only when something goes
				// wrong: an escalated fraction read only off escalation lines
				// has no denominator.
				settled = append(settled, wave)
			}
		}
	}
	stats := b.stats
	b.mu.Unlock()

	for _, id := range settled {
		b.log.Info("wave: the coordinator has judged everything this wave owed",
			"wave", string(id),
			"facts", stats.Facts(), "routine", stats.Routine,
			"woken", stats.Woken, "escalated", stats.Escalated, "cards", stats.Cards)
	}
}

// Stats is what the mechanism has cost so far. It is read by the escalation
// log line, which is where the fraction §12 asks for is written down.
func (b *Backstop) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Open is what the record still owes judgement on, newest information and
// all: whether the wake reached the coordinator, why it did not, and whether
// the human has been told. It is a snapshot and holds no pointers into the
// set.
func (b *Backstop) Open() []Fact {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Fact, 0, len(b.open))
	for _, f := range b.open {
		out = append(out, *f)
	}
	return out
}
