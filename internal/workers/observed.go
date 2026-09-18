package workers

// The observed facts (nocx-luqz9.2; ADR-0070 decision 2, design §4.2–4.4).
//
// # Two ways a worker becomes known, never merged
//
// A worker SAYS — a report, in its own words, a claim. nocx SEES — the
// classification of the worker's pane, which is not a claim about anything: it
// is what is visible on a screen. This file is the second one.
//
// NOTHING HERE DECIDES A WORKER'S STATE. The reduction that does (reduce, in
// registrar.go) is driven by the process exit and the declaration and by nothing
// else, and an observation moves no record state (ADR-0070 decision 3). That is
// asserted in observed_test.go rather than left as this paragraph's promise.
//
// # A settled state, never a frame
//
// A pane repaints continuously — an agent's token counter moves on every
// response chunk — so a fact per reading would be a message per chunk's worth of
// output. What becomes a fact is a state that HELD: the pane was read as idle,
// and it was still idle at least the settle window later. A flicker shorter than
// the window is therefore not a fact at all, which is the whole reason the
// window exists rather than a rate limit.
//
// The window is measured against this package's own clock (Registrar.now —
// already injected, for the mailbox's timestamps and the fact deadline's),
// because the interval is in-process and a test has to be able to state it
// rather than wait for it. internal/monoclock answers a DIFFERENT question: a
// monotonic reading that a coordinator and a helper on one machine compare
// ACROSS a wire (the commitBy deadline). Nothing here crosses a process, so
// using it would buy a second clock seam and no property this one lacks.
//
// # Only a change is a fact, and one change is one message
//
// The machine below remembers what each pane was last read as, since when, and
// whether that hold has already been placed in a mailbox. A pane that stays idle
// is one fact and not one per window; a pane that was idle, worked, and settled
// idle again is two — because the worker BECAME idle twice, and that is what a
// coordinator acts on.
//
// # Nothing read off a screen ever travels
//
// An observed message carries a worker, a state and a time, and the type is the
// enforcement: Observed has three fields and no room for the text that produced
// the verdict. See ADR-0070, and this package's own wakeText for the reason —
// typing a worker's screen content into a coordinator's input box would be
// prompt injection performed with our own hands.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ObservedState is what a worker's pane was seen to be, in the words the
// coordinator reads: CONTEXT.md's Idle, Blocked and Exited, plus Working.
type ObservedState string

const (
	// ObservedIdle is a worker doing nothing and waiting for nothing: no turn,
	// no subagent, no command running, its input box free. It is a fact — a
	// coordinator that does not know its worker's arms are down holds a worker
	// it could be using.
	ObservedIdle ObservedState = "idle"
	// ObservedBlocked is a worker that cannot continue until somebody steps in:
	// a menu or a question on its screen, or its agent's own error. A fact for
	// the same reason, with more urgency behind it.
	ObservedBlocked ObservedState = "blocked"
	// ObservedExited is a worker whose agent process is gone. It is the one
	// member of this set that is not a screen reading — the shell underneath
	// keeps drawing — and it is placed by ObserveExit, which is called from the
	// one place that already learns a worker's process ended.
	ObservedExited ObservedState = "exited"
	// ObservedWorking is a READING and never a fact: a turn in flight, or a
	// screen the driver could not identify. Placing it would tell a coordinator
	// that nothing had happened, once per window, forever.
	//
	// It is a member of the accepted set rather than an absent reading because
	// the machine has to SEE it: a pane that was idle, worked, and settled idle
	// again is idle news TWICE, and a record never told about the intervening
	// turn could not tell those two apart.
	ObservedWorking ObservedState = "working"
)

// observedStates is the closed set Observe admits, and ObservedExited is
// deliberately NOT in it. A reading names something a screen showed; exited is
// what ObserveExit places after the record has admitted a process fact, and a
// caller that reached Observe with it is looking for a second door into the same
// fact.
//
// Anything else is refused rather than held: a state this package does not know
// would sit in the machine forever as "a state that never settles", which is
// indistinguishable from a busy worker — and a silent absence of facts is the
// one thing a coordinator cannot tell from a quiet worker.
var observedStates = map[ObservedState]bool{
	ObservedIdle:    true,
	ObservedBlocked: true,
	ObservedWorking: true,
}

// ErrNotAnObservedState means a reading names a state Observe does not admit.
// It is a programming error at the crossing rather than a condition anybody acts
// on, and it is named so a caller can tell it from a mailbox failure.
var ErrNotAnObservedState = errors.New("worker: not a state this record observes")

// DefaultSettleWindow is how long a state must hold before it is a fact, when
// the composition root names no other number (design §4.4).
//
// Three seconds, and the DIRECTION of the error is what chose it. Too short and
// a coordinator is told about states that were passing — a worker that paused to
// think is reported idle and woken about for nothing. Too long and a blocked
// worker waits longer for help, and the human notification of §5.4 sits behind
// this same window. An unnecessary wake is recoverable; nothing else here is as
// cheap.
const DefaultSettleWindow = 3 * time.Second

// Observed is one settled state of one worker: the whole of what the coordinator
// is told, and the whole of what ever crosses (ADR-0070 decision 3).
//
// Three fields, and no fourth with room for text. At is the record's own clock
// reading at the moment the fact was committed, so a coordinator comparing an
// observation against its own actions compares two readings of one clock.
type Observed struct {
	// Worker is the participant the state is about — the same id the
	// coordinator names in every other call, so an observation and a report
	// about one worker are about one row.
	Worker ParticipantID
	// State is what was seen. Never ObservedWorking: that is a reading.
	State ObservedState
	// At is when the fact was committed.
	At time.Time
}

// observedSender is the Sender stamped on every observed message.
//
// It is NOT a coordinator session, and deliberately not a participant id
// either. A coordinator's undelivered count answers "what did I say that nobody
// took", and a row nocx wrote itself is not something the coordinator said;
// naming the record here is also what lets a reader tell an observation from a
// colleague's mail without parsing anything, which is the distinction this whole
// vocabulary rests on.
const observedSender ReaderID = "nocx"

// settling is what one worker's pane was last READ as, and since when.
//
// placed is per HOLD rather than per state: it is cleared when the state
// changes, so a pane that leaves idle and settles idle again is news twice while
// a pane that stays idle is news once. "The state is idle" and "the worker
// became idle" are different facts, and only the second is one.
//
// told is the exit's own record, and it is separate from placed because the exit
// has no window: it is a fact the first time it is seen, and the RECORD
// legitimately admits the same exit more than once (admit's terminal guard
// exempts the state an exit alone leaves behind, because a late declaration
// still refines it — reduce). Two admissions of one exit, one fact, one message.
type settling struct {
	state ObservedState
	since time.Time
	// placed is "a fact for THIS HOLD has been written", and inFlight is "a
	// writer holds the right to write it and has not finished".
	//
	// BOTH, and the pair is the whole of the rule: the decision to place a fact
	// happens under this machine's lock and the write happens after it is
	// released, so `placed` alone leaves a window a second reader can walk into
	// and place the same fact twice. Reserving under the same lock closes it,
	// and releasing on every failure is what keeps the reservation from
	// suppressing a fact that was never written.
	placed   bool
	inFlight bool
	// told is the exit's own, and it is separate from placed because the exit
	// has no window: it is a fact the first time it is seen, and the RECORD
	// legitimately admits the same exit more than once (admit's terminal guard
	// exempts the state an exit alone leaves behind, because a late declaration
	// still refines it — reduce). Two admissions of one exit, one fact, one
	// message.
	told bool
}

// observedFacts is the settle machine: what each worker was last read as, and
// what has already been placed. One mutex, because the sweep that reads panes
// and the supervisor that reports an exit are two goroutines.
type observedFacts struct {
	mu      sync.Mutex
	window  time.Duration
	workers map[ParticipantID]*settling
}

// newObservedFacts builds the machine with the window it was given, and the
// ZERO IS A CONFIGURATION rather than a mistake: it means "a state is a fact on
// its second reading", which is what a caller that cannot move this package's
// clock (a test at the composition root) needs. The product's number is
// DefaultSettleWindow and it arrives through WithSettleWindow.
func newObservedFacts(window time.Duration) *observedFacts {
	return &observedFacts{window: window, workers: make(map[ParticipantID]*settling)}
}

// Observe admits one reading of a worker's pane.
//
// The caller is the crossing that read the screen (internal/app's observation
// bridge, over the transport's sweep), and what it passes is already this
// package's vocabulary: which driver state means idle and which means blocked
// belongs to whoever owns the driver's rules, and this package owns only what a
// SETTLED state does.
//
// A reading that has not held for the window places nothing and answers nil —
// the ordinary case, once per sweep per watched pane. A reading the record
// refuses (a replaced incarnation, a finished participant, a state this package
// does not know) answers that error and places nothing. A mailbox write that
// fails answers its error too, and the fact is NOT lost: the state is still the
// one being held, so the next reading of it — the next sweep — places what the
// failed one could not, once.
func (r *Registrar) Observe(ctx context.Context, id ParticipantID, l Liveness, state ObservedState) error {
	if state == ObservedExited {
		// Named refusal, because a caller that reached here with it is looking
		// for the door ObserveExit is.
		return fmt.Errorf("worker: %q is a fact about a process and not a reading: "+
			"admit the exit and call ObserveExit: %w", state, ErrNotAnObservedState)
	}
	if !observedStates[state] {
		return fmt.Errorf("worker: %q: %w", state, ErrNotAnObservedState)
	}
	// The identity guard first, and it is the same one every other fact passes
	// (admit): a reading from a replaced attempt is evidence about a process that
	// is not this record's.
	cur, err := r.store.Participant(ctx, id)
	if err != nil {
		return err
	}
	if !cur.Liveness.SameIncarnation(l) {
		return fmt.Errorf("worker: participant %q: %w", id, ErrStaleEvidence)
	}
	if cur.State.Terminal() {
		return fmt.Errorf("worker: participant %q is %s: %w", id, cur.State, ErrTerminal)
	}

	now := r.now()
	if state == ObservedWorking {
		// The machine has seen it and nothing crosses: working is the absence
		// of news. It is fed to the machine FIRST so the hold is reset — a pane
		// that worked between two idles is idle news twice.
		r.observations.held(id, state, now)
		return nil
	}
	if !r.observations.claim(id, state, now) {
		return nil
	}
	// From here a claim is HELD, and every way out must release it or settle
	// it: a claim abandoned by a bare return would suppress this fact for as
	// long as the hold lasts, which is a silent loss of the only evidence the
	// coordinator would get.
	coordinator, err := r.coordinatorOf(ctx, cur.Group)
	if err != nil {
		r.observations.release(id)
		return err
	}
	if err := r.placeObservation(ctx, cur, coordinator, Observed{Worker: id, State: state, At: now}); err != nil {
		r.observations.release(id)
		return err
	}
	r.observations.settle(id)
	return nil
}

// observeExit places the one observed fact that is about a PROCESS.
//
// It is called from Exited, AFTER the record has admitted the exit — the record
// is what established the process was this participant's, so an unexported call
// from there is the only door that exists and a caller cannot fabricate one. The
// two facts are separate everywhere else in this package for the same reason:
// the classification of a pane whose agent has exited is a classification of the
// SHELL, so "this worker's agent is gone" never travels as a screen reading.
//
// It has no window and it is told once. The record admits the same exit more than
// once by design (see settling.told), and a coordinator told twice about one
// process has been told a worker died twice.
//
// A mailbox that refuses the row is REPORTED and not returned: the record's own
// fact has already landed, and turning a recorded exit into a failed one would
// be the wrong half to lose. The loss is logged at Error.
//
// The claim is RELEASED on a failure rather than held, which is the one place
// the exit differs from a reading and it is not a detail: the record admits the
// same exit again (that is why told exists), so a second call really can retry,
// and a claim kept across a failed write would make the first transient routing
// failure the permanent loss of the only notice that a worker died.
func (r *Registrar) observeExit(ctx context.Context, p Participant) {
	if !r.observations.claimExit(p.ID) {
		return
	}
	coordinator, err := r.coordinatorOf(ctx, p.Group)
	if err != nil {
		r.observations.release(p.ID)
		r.logExitLoss(ctx, p.ID, err)
		return
	}
	if err := r.placeObservation(ctx, p, coordinator, Observed{
		Worker: p.ID, State: ObservedExited, At: r.now(),
	}); err != nil {
		r.observations.release(p.ID)
		r.logExitLoss(ctx, p.ID, err)
		return
	}
	r.observations.settleExit(p.ID)
}

// logExitLoss says out loud that a fact about a process reached nobody.
//
// The logger comes from log.From(ctx) rather than from r.log, so the line carries
// the module, request id, trace and span of whatever context the caller holds —
// the ratchet that keeps those four on every new call site is about which logger
// a site reaches for, and this one has a context to give it. From answers for a
// nil context too, which is the case the supervisor's own detached call presents.
func (r *Registrar) logExitLoss(ctx context.Context, id ParticipantID, cause error) {
	log.From(ctx).Error("worker: a worker's exit reached no mailbox, and no further reading of it will come",
		"participant", string(id), "error", cause)
}

// coordinatorOf names the mailbox an observation about this worker belongs in.
//
// The coordinator is read at the MOMENT of the fact rather than remembered from
// registration: a worker whose group gained its coordinator later is addressed
// to the one it has, and a group with none is refused by name rather than
// written into "": a mailbox belonging to nobody, which nothing would ever read.
func (r *Registrar) coordinatorOf(ctx context.Context, group ID) (ReaderID, error) {
	coordinator, err := r.store.CoordinatorSession(ctx, group)
	if err != nil {
		return "", fmt.Errorf("worker: worker %q: %w", group, err)
	}
	if coordinator == "" {
		// A NAMED FACT (nocx-luqz9.4). It used to answer ErrNoSuchParticipant,
		// which is a statement about a row; what is actually true is that
		// this worker has NO COORDINATOR, so there is no mailbox its mail
		// belongs in and nothing that could be written or read. A report
		// reaches the endpoint with this error, and the difference between
		// "the row is gone" and "nobody is above this worker" is the
		// difference between a fault and an inconsistency to tell a person
		// about.
		return "", fmt.Errorf("worker: nobody coordinates worker %q: %w", group, ErrNoCoordinator)
	}
	return ReaderID(coordinator), nil
}

// placeObservation commits one fact into the coordinator's mailbox.
//
// It goes straight to the store rather than through Say, and both halves of that
// are deliberate. Say refuses an empty body because an empty row costs a reader
// a fetch and tells it nothing; an observation row carries Observed INSTEAD of a
// body, so what a reader is handed is the state itself — more than a sentence
// would carry, and exactly as unauthoritative as any other mail. And Say checks
// membership of both ends, which here would be checking the record against
// itself: the sender is the record, and the recipient is the coordinator it just
// looked up.
func (r *Registrar) placeObservation(ctx context.Context, p Participant, coordinator ReaderID, o Observed) error {
	if _, err := r.store.Commit(ctx, Message{
		Group:       p.Group,
		Recipient:   coordinator,
		Sender:      observedSender,
		Observed:    &o,
		CommittedAt: r.now(),
	}); err != nil {
		return fmt.Errorf("worker: observe %q: %w", p.ID, err)
	}
	// The mail is in the box, so the coordinator may now be woken about it —
	// and only now. Telling the wake before the write would be this package
	// announcing a row a reader cannot find.
	r.wake.Arrived(ctx, coordinator)
	return nil
}

// ObserveCoordinator admits one reading of a COORDINATOR's own pane, with the
// number of live workers it holds read here rather than passed in.
//
// # Why the coordinator's state is judged here and not by whoever reads the pane
//
// The bridge in internal/app maps a driver state onto this package's
// vocabulary; what a SETTLED state of a coordinator MEANS is this record's,
// exactly as it is for a worker, and it is the same witness and the same window
// (design §5.2, §4.4). So the reading takes the same two-step discipline: a
// state becomes news when it has HELD, and a re-settled idle after a turn of
// work is idle news again.
//
// The session is a coordinator's BY HAVING WORKERS, which is the same thing
// that makes it addressable at all: a session the record holds no participant
// for is somebody's own agent, and there is nothing to coordinate. A
// coordinator that holds nothing is therefore dropped here, which is what its
// own notice's condition asks anyway — a blocked coordinator with no live
// workers is not a failure of coordination, because there is nothing to
// coordinate.
//
// `live` counts non-terminal participants, because that is what "holds live
// workers" means: a coordinator whose workers have all settled is not stuck,
// it is finished.
func (r *Registrar) ObserveCoordinator(ctx context.Context, sessionID string, state ObservedState) {
	if sessionID == "" || !observedStates[state] {
		// Exited included, and by the same rule Observe enforces: an exit is a
		// fact about a process and has its own door, and a caller reaching
		// here with it is looking for a second one.
		return
	}
	live, err := r.coordinatorLive(ctx, sessionID)
	if err != nil {
		// A read that failed is not evidence there are no workers, and a
		// coordinator the record cannot answer about is one nocx must not
		// type into. Nothing is admitted and the line says why.
		log.From(ctx).Debug("worker: a coordinator's own workers could not be counted",
			"session_id", sessionID, "error", err)
		return
	}
	if live == 0 {
		return
	}
	id := ParticipantID(sessionID)
	now := r.now()
	// Every settled reading reaches the wake, and the wake is what decides
	// whether there is anything to say. It is NOT the one-fact-per-hold rule
	// the worker half follows, and the difference is deliberate: a worker's
	// observation is news once per hold, while a coordinator's readings are the
	// only thing that can retry a line the pane refused — a refusal leaves no
	// timer behind, so the pane coming back is the observable, and it has to be
	// let through. See observedFacts.settledFor.
	if !r.coordReadings.settledFor(id, state, now) {
		return
	}
	r.wake.Coordinator(ctx, ReaderID(sessionID), state, live)
}

// coordinatorLive is how many workers one session holds that are not finished.
//
// It reads the store DIRECTLY rather than through HeldBy, and that is not a
// shortcut: HeldBy is the coordinator's own fetch, and a fetch DISPATCHES —
// a sweep that called it would clear the very facts the wake is meant to
// announce.
func (r *Registrar) coordinatorLive(ctx context.Context, sessionID string) (int, error) {
	held, err := r.store.HeldBy(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	live := 0
	for _, p := range held {
		if !p.State.Terminal() {
			live++
		}
	}
	return live, nil
}

// held advances the machine for one reading and reports whether the state has
// now held for the window.
//
// It is the reading that DOES NOT place a fact — ObservedWorking — and it exists
// separately from claim so that a working pane still resets the hold. A pane that
// was idle, worked, and settles idle again is idle news TWICE (design §4.3), and
// only the intervening turn reaching the machine lets it tell those two apart.
//
// The window is measured from the time the state was FIRST read, and a state is
// never a fact from a single reading — even with a zero window it takes a second
// reading of the same state, which is what "held" means and what keeps a
// zero-window caller a user of this rule rather than an exception to it.
func (f *observedFacts) held(id ParticipantID, state ObservedState, now time.Time) bool {
	return f.claim(id, state, now)
}

// settledFor answers whether this state has been continuously read for the
// window, WITHOUT placing anything and without consuming the hold — so the same
// reading may be asked about again on the next sweep and answered the same way.
//
// It exists for the coordinator's own pane (nocx-luqz9.3), and the difference
// from claim is the whole reason it does. `claim` is "this hold has not been
// reported yet", which is what a worker's observation needs: one fact per hold,
// ever. A coordinator needs the opposite — EVERY settled idle reading must
// reach the wake, because the wake is what decides whether there is anything to
// say, and a reading that never arrives can never retry a line the pane
// refused. A refusal leaves no timer behind (design §5.4), so the observable
// that retries it is the pane coming back, and that reading has to be let
// through. Nothing is placed here, so the two machines' rules stay distinct.
func (f *observedFacts) settledFor(id ParticipantID, state ObservedState, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.workers[id]
	if !ok || held.state != state {
		f.workers[id] = &settling{state: state, since: now}
		return false
	}
	return now.Sub(held.since) >= f.window
}

// claim reserves the right to place one fact for this worker, and reports
// whether the caller got it.
//
// THE RESERVATION IS THE POINT. The decision is taken under this lock and the
// write happens after it is released, so a reader answered "yes" and then
// overtaken by a second reader would place the same fact twice — and the two
// would agree in every test that drove one writer. Setting inFlight here, and
// clearing it in settle or release, makes the window one only the holder can be
// in.
func (f *observedFacts) claim(id ParticipantID, state ObservedState, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.workers[id]
	if !ok || held.state != state {
		f.workers[id] = &settling{state: state, since: now}
		return false
	}
	if held.placed || held.inFlight || now.Sub(held.since) < f.window {
		return false
	}
	held.inFlight = true
	return true
}

// release gives back a claim whose write did not happen, so the next reading may
// place the fact. Every failure path out of a claimed write calls this: a claim
// kept across a failure suppresses the fact for the rest of the hold, and for a
// lookup that failed once that is a silent loss.
func (f *observedFacts) release(id ParticipantID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if held, ok := f.workers[id]; ok {
		held.inFlight = false
	}
}

// settle records that the claimed fact was written, so the same hold is not
// placed twice and the next reading of this state is not news.
func (f *observedFacts) settle(id ParticipantID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if held, ok := f.workers[id]; ok {
		held.inFlight = false
		held.placed = true
	}
}

// claimExit reserves the exit's one message, which has no window: it is a fact
// the first time it is seen.
//
// The claim and the release are what keep the two admissions of one exit (see
// settling.told) from becoming two messages while leaving a failed write
// retryable by the next one.
func (f *observedFacts) claimExit(id ParticipantID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.workers[id]
	if !ok {
		f.workers[id] = &settling{state: ObservedExited, inFlight: true}
		return true
	}
	if held.told || held.inFlight {
		return false
	}
	held.state = ObservedExited
	held.inFlight = true
	return true
}

// settleExit records that the exit's one message was written. It is the caller's
// success path; claimExit's holder calls release instead on any failure.
func (f *observedFacts) settleExit(id ParticipantID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if held, ok := f.workers[id]; ok {
		held.inFlight = false
		held.told = true
	}
}
