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
	}
	for _, o := range opts {
		o(b)
	}
	return b
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
	b.mu.Unlock()

	out := b.attempt(ctx, f)

	b.mu.Lock()
	if cur, still := b.open[key]; still {
		cur.Wake = out
	}
	b.mu.Unlock()
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
func (b *Backstop) due(key factKey) {
	b.mu.Lock()
	f, still := b.open[key]
	if !still || f.Escalated {
		b.mu.Unlock()
		return
	}
	f.Escalated = true
	snapshot := *f
	b.mu.Unlock()

	if b.escalate == nil {
		b.log.Error("wave: a fact went undispatched past its deadline and this backend has nothing to tell anyone with",
			"participant", string(snapshot.Participant), "wave", string(snapshot.Wave),
			"kind", string(snapshot.Kind))
		return
	}
	b.log.Warn("wave: a fact went undispatched past its deadline and is reaching the human",
		"participant", string(snapshot.Participant), "wave", string(snapshot.Wave),
		"kind", string(snapshot.Kind), "wake_delivered", snapshot.Wake.Delivered,
		"wake_refused_because", snapshot.Wake.Reason)
	// context.WithoutCancel of nothing: an alarm has no caller's context to
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
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, cancel := range b.cancel {
		if !told[key.participant] {
			continue
		}
		cancel()
		delete(b.cancel, key)
		delete(b.open, key)
	}
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
