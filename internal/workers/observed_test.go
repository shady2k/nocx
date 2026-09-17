package workers

// The observed facts (nocx-luqz9.2; ADR-0070 decision 2, design §4.2–4.4).
//
// What is asserted here is the whole rule and its four edges: a state becomes a
// fact only after it has HELD for the settle window, a flicker shorter than the
// window is not a fact at all, a process exit is a fact with no window, one
// settled change is one message in the coordinator's mailbox, and no message
// ever carries anything read off a screen.
//
// Every test drives the same admission path the sweep does — Registrar.Observe
// with the participant's own Liveness — and reads the MAILBOX rather than a
// return value: "exactly one message" is a claim about what was committed, and
// a test reading a returned fact would be asserting a value the caller passed
// in.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// movingClock is the clock the settle window is measured against, moved by the
// test rather than by time. The window has two ends, and a test that slept to
// reach either would be measuring the machine rather than the rule.
type movingClock struct{ at time.Time }

func (c *movingClock) now() time.Time          { return c.at }
func (c *movingClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// observedStand is one wired record whose clock the test moves, over the
// harness's own doubles. The window is the case's own argument, because a test
// asserting "not before the window" has to say how long the window is.
type observedStand struct {
	*harness
	clock *movingClock
	ctx   context.Context
}

func newObservedStand(t *testing.T, window time.Duration) *observedStand {
	t.Helper()
	clock := &movingClock{at: time.Unix(1_700_000_000, 0).UTC()}
	h := newHarnessBound(t, 8, WithSettleWindow(window))
	h.reg.now = clock.now
	return &observedStand{harness: h, clock: clock, ctx: context.Background()}
}

// reading is one sweep's classification of a worker's pane, delivered the way
// production delivers it.
func (s *observedStand) reading(t *testing.T, p Participant, state ObservedState) error {
	t.Helper()
	return s.reg.Observe(s.ctx, p.ID, p.Liveness, state)
}

// mustReading fails the test rather than returning, for the readings that are
// arrangement rather than the assertion.
func (s *observedStand) mustReading(t *testing.T, p Participant, state ObservedState) {
	t.Helper()
	if err := s.reading(t, p, state); err != nil {
		t.Fatalf("observe %s for %s: %v", state, p.ID, err)
	}
}

// mailbox is what the coordinator's box HOLDS, without being handed it: these
// tests are about what was committed, and a fetch that advanced a cursor as
// part of the arrangement would be a second thing moving.
func (s *observedStand) mailbox(t *testing.T) []Message {
	t.Helper()
	return s.store.mailbox(t, coordBox)
}

// exit admits the process fact the supervisor reports, which is the one
// observed state that is not a screen reading.
func (s *observedStand) exit(t *testing.T, p Participant) error {
	t.Helper()
	_, err := s.reg.Exited(s.ctx, p.ID, p.Liveness, Exit{Cause: "exited", Code: 0, At: s.clock.now()})
	return err
}

// ── the criterion: one fact per settled change, and none before the window ──

func TestAWorkerThatSettlesIdleIsToldToItsCoordinatorOnceAndNotBeforeTheWindow(t *testing.T) {
	const window = 3 * time.Second
	s := newObservedStand(t, window)
	p := mustRegister(t, s.harness)

	// The pane was working. It is a reading and never a fact — the record is
	// told nothing about a worker that is getting on with its work.
	s.mustReading(t, p, ObservedWorking)
	s.mustReading(t, p, ObservedIdle)
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("the coordinator was told about a state before the pane had held it for the window: %+v", got)
	}

	// A hair short of the window is still short of it.
	s.mustReading(t, p, ObservedIdle)
	s.clock.advance(window - time.Millisecond)
	s.mustReading(t, p, ObservedIdle)
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a state held for less than the window was recorded: %+v", got)
	}

	s.clock.advance(time.Millisecond)
	s.mustReading(t, p, ObservedIdle)
	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("a settled idle pane produced %d messages, want exactly 1: %+v", len(got), got)
	}
	if got[0].Recipient != coordBox {
		t.Fatalf("the fact was committed to %q, want the coordinator's mailbox %q", got[0].Recipient, coordBox)
	}
	if got[0].Observed == nil {
		t.Fatalf("the message carries no observation at all: %+v", got[0])
	}
	want := Observed{Worker: p.ID, State: ObservedIdle, At: s.clock.now()}
	if *got[0].Observed != want {
		t.Fatalf("observation = %+v, want %+v", *got[0].Observed, want)
	}

	// And it stays one: a pane that goes on being idle is not news.
	s.clock.advance(10 * window)
	s.mustReading(t, p, ObservedIdle)
	if again := s.mailbox(t); len(again) != 1 {
		t.Fatalf("a settled idle pane was reported %d times, want once: %+v", len(again), again)
	}
}

// The settle window's whole job: a pane that flickers between states is not a
// worker that changed state. Without the window every repaint of an input box
// would be a fact for a coordinator to act on.
func TestAFlickerShorterThanTheWindowLeavesNoFact(t *testing.T) {
	const window = 3 * time.Second
	s := newObservedStand(t, window)
	p := mustRegister(t, s.harness)

	s.mustReading(t, p, ObservedWorking)
	s.mustReading(t, p, ObservedIdle)
	s.clock.advance(time.Second)
	s.mustReading(t, p, ObservedWorking)
	s.clock.advance(time.Second)
	s.mustReading(t, p, ObservedIdle)
	s.clock.advance(time.Second)
	s.mustReading(t, p, ObservedWorking)
	s.clock.advance(10 * window)
	s.mustReading(t, p, ObservedWorking)

	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a flicker shorter than the window was recorded as a fact: %+v", got)
	}
}

// ── blocked, and exited, which is not a screen reading at all ───────────────

func TestAWorkerBlockedOnAMenuIsToldOnceWithTheStateTheMenuMeans(t *testing.T) {
	const window = 3 * time.Second
	s := newObservedStand(t, window)
	p := mustRegister(t, s.harness)

	s.mustReading(t, p, ObservedWorking)
	s.mustReading(t, p, ObservedBlocked)
	s.clock.advance(window)
	s.mustReading(t, p, ObservedBlocked)

	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("a blocked worker produced %d messages, want exactly 1: %+v", len(got), got)
	}
	want := Observed{Worker: p.ID, State: ObservedBlocked, At: s.clock.now()}
	if got[0].Observed == nil || *got[0].Observed != want {
		t.Fatalf("observation = %+v, want %+v", got[0].Observed, want)
	}
}

// The exit needs no window — nothing about a process being gone becomes truer
// by being true for another three seconds — and it is TOLD ONCE even though the
// record admits a second exit fact over the same participant (admit's terminal
// guard exempts the state an exit alone leaves behind, because a late
// declaration still refines it).
func TestAWorkerWhoseProcessExitsIsToldOnExitedMessageAndNoSecondOne(t *testing.T) {
	const window = time.Hour
	s := newObservedStand(t, window)
	p := mustRegister(t, s.harness)

	// A pane that was idle a moment before it died has not held that state for
	// an hour, so nothing else is in the box to confuse the count.
	s.mustReading(t, p, ObservedIdle)
	if err := s.exit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}
	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("an exited worker produced %d messages, want exactly 1: %+v", len(got), got)
	}
	want := Observed{Worker: p.ID, State: ObservedExited, At: s.clock.now()}
	if got[0].Observed == nil || *got[0].Observed != want {
		t.Fatalf("observation = %+v, want %+v", got[0].Observed, want)
	}

	// The same exit arriving again — the supervisor reporting it twice, the
	// enrolment withdrawing as well as the process ending — is one fact.
	if err := s.exit(t, p); err != nil {
		t.Logf("a second exit fact was refused (%v); the count below is what matters", err)
	}
	if again := s.mailbox(t); len(again) != 1 {
		t.Fatalf("the exit was reported %d times, want once: %+v", len(again), again)
	}
}

// ── nothing on a screen ever travels ───────────────────────────────────────

// The message is EXACTLY a worker, a state and a time. The DeepEqual is the
// assertion that matters: a field added to Observed carrying anything read off a
// screen would have to break this test to land.
func TestAnObservedMessageCarriesNoScreenText(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)
	s.mustReading(t, p, ObservedBlocked)
	s.mustReading(t, p, ObservedBlocked)

	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("messages = %+v, want one", got)
	}
	if got[0].Body != "" {
		t.Fatalf("an observed message carries a body %q; a screen reading must never travel (ADR-0070)", got[0].Body)
	}
	if got[0].Sender != observedSender {
		t.Fatalf("observed message sender = %q, want the record's own %q", got[0].Sender, observedSender)
	}
	if *got[0].Observed != (Observed{Worker: p.ID, State: ObservedBlocked, At: s.clock.now()}) {
		t.Fatalf("observation = %+v, want exactly the worker, the state and the time", *got[0].Observed)
	}
	// Text mail carries no observation, which is how a reader tells the two
	// apart without parsing anything.
	said := sayTo(t, s.harness, coordBox, workerBox, "read AGENTS.md")
	if said.Observed != nil {
		t.Fatalf("text mail carries an observation: %+v", said.Observed)
	}
}

// ── whose mailbox, and what it cost the record ──────────────────────────────

// A coordinator is told about ITS workers and nobody else's. This is the whole
// of A9's rule read from the mailbox end: the recipient is derived from the
// record, never named by the caller.
func TestASecondCoordinatorsMailboxLearnsNothing(t *testing.T) {
	s := newObservedStand(t, 0)
	mine := mustRegister(t, s.harness)
	s.mustReading(t, mine, ObservedIdle)
	s.mustReading(t, mine, ObservedIdle)

	const (
		otherCoord = "sess-other-coordinator"
		otherGroup = ID("worker-2")
	)
	if err := s.store.EnsureGroup(s.ctx, otherGroup, otherCoord); err != nil {
		t.Fatalf("ensure the second worker: %v", err)
	}
	if _, err := s.reg.Register(s.ctx, RegisterRequest{
		Group: otherGroup, CoordinatorSession: otherCoord,
		Role: RoleWorker, Task: "somebody else's worker", Command: "claude",
	}); err != nil {
		t.Fatalf("register the second worker: %v", err)
	}

	if got := s.store.mailbox(t, ReaderID(otherCoord)); len(got) != 0 {
		t.Fatalf("a coordinator was told about a worker it does not hold: %+v", got)
	}
	if got := s.mailbox(t); len(got) != 1 {
		t.Fatalf("the holding coordinator has %d messages, want the one about its own worker", len(got))
	}
}

// A screen reading moves NO record state (ADR-0070 decision 3). The exit is the
// one fact that does, and it does so through the ordinary reduction — which is
// what these two assertions separate: an identical record across an idle and a
// blocked fact, and a record the exit alone moved.
func TestAnObservationMovesNoRecordStateAndTheExitAloneDoes(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)
	before, ok := s.store.read(t, p.ID)
	if !ok {
		t.Fatalf("the registered participant vanished")
	}

	s.mustReading(t, p, ObservedWorking)
	s.mustReading(t, p, ObservedIdle)
	s.mustReading(t, p, ObservedIdle)
	s.mustReading(t, p, ObservedBlocked)
	s.mustReading(t, p, ObservedBlocked)
	if got := s.mailbox(t); len(got) != 2 {
		t.Fatalf("the readings produced %d facts, want two, so this test is asserting the right thing: %+v", len(got), got)
	}
	after, ok := s.store.read(t, p.ID)
	if !ok {
		t.Fatalf("the participant vanished under observation")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a screen reading moved the record:\nbefore %+v\nafter  %+v", before, after)
	}

	if err := s.exit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}
	ended, ok := s.store.read(t, p.ID)
	if !ok {
		t.Fatalf("the participant vanished on exit")
	}
	if ended.Exited == nil {
		t.Fatalf("the exit did not reach the record: %+v", ended)
	}
	if !ended.State.Terminal() {
		t.Fatalf("the record state after an exit = %q, want terminal", ended.State)
	}
}

// ── the failure path, and the guards ───────────────────────────────────────

// A mailbox write that fails loses no fact and stops nothing. The reading IS the
// retry: the window has already elapsed, so the next reading of the same held
// state places what the failed one could not, and the coordinator is told once.
func TestAMailboxWriteThatFailsIsNotSilentAndTheNextReadingStillLands(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)
	// The first reading only opens the hold; the second is the one that writes,
	// and the fault is armed for exactly that write.
	s.mustReading(t, p, ObservedIdle)
	s.store.setFault("commit", 1)

	err := s.reading(t, p, ObservedIdle)
	if err == nil {
		t.Fatalf("a mailbox write that failed was reported as a success")
	}
	if !errors.Is(err, errInjected) {
		t.Fatalf("the failure is not named in the returned error: %v", err)
	}
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a failed write left a message behind: %+v", got)
	}

	// The next reading of the same held state arrives, exactly once.
	s.mustReading(t, p, ObservedIdle)
	if got := s.mailbox(t); len(got) != 1 {
		t.Fatalf("after the failed write the coordinator has %d messages, want 1: %+v", len(got), got)
	}
	s.clock.advance(time.Hour)
	s.mustReading(t, p, ObservedIdle)
	if got := s.mailbox(t); len(got) != 1 {
		t.Fatalf("the retry committed twice: %+v", got)
	}
}

// The incarnation guard, and the terminal one: a reading from a replaced attempt
// is evidence about a process that is not this record's, and a record that has
// taken its exit takes no more readings at all. Neither writes a message.
func TestAReadingFromAReplacedIncarnationOrAFinishedWorkerIsRefused(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)

	stale := p.Liveness
	stale.Epoch++
	if err := s.reg.Observe(s.ctx, p.ID, stale, ObservedIdle); !errors.Is(err, ErrStaleEvidence) {
		t.Fatalf("a reading from a replaced incarnation answered %v, want ErrStaleEvidence", err)
	}
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a stale reading was recorded: %+v", got)
	}

	if err := s.exit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if err := s.reg.Observe(s.ctx, p.ID, p.Liveness, ObservedIdle); !errors.Is(err, ErrTerminal) {
		t.Fatalf("a reading against a finished worker answered %v, want ErrTerminal", err)
	}
}

// A state this package does not know is refused rather than held forever: the
// machine would otherwise record it as "a state that never settles", which is
// indistinguishable from a worker that is genuinely working.
func TestAReadingInAStateThisPackageDoesNotKnowIsRefused(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)
	if err := s.reg.Observe(s.ctx, p.ID, p.Liveness, ObservedState("compleated")); !errors.Is(err, ErrNotAnObservedState) {
		t.Fatalf("an unknown observed state answered %v, want ErrNotAnObservedState", err)
	}
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("an unknown observed state produced %+v", got)
	}
}

// Exited is not a reading. A caller that reached Observe with it is looking for
// a second door onto a fact about a process, and the door that exists is the
// record's own admission of the exit.
func TestExitedIsNotAReadingAndObserveRefusesIt(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)
	if err := s.reg.Observe(s.ctx, p.ID, p.Liveness, ObservedExited); !errors.Is(err, ErrNotAnObservedState) {
		t.Fatalf("Observe with exited answered %v, want ErrNotAnObservedState", err)
	}
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a screen reading of exited was recorded: %+v", got)
	}
}

// ── the window is the one the record was built with ────────────────────────

// The zero value of the option is PRODUCTION — three seconds — so the number has
// one owner and a test that wants another says so. Both ends are asserted
// against a clock the test moves, never against a sleep.
func TestTheSettleWindowIsTheOneTheRecordWasBuiltWith(t *testing.T) {
	production := newHarnessBound(t, 4)
	clock := &movingClock{at: time.Unix(1_700_000_000, 0).UTC()}
	production.reg.now = clock.now
	p := mustRegister(t, production)
	if err := production.reg.Observe(context.Background(), p.ID, p.Liveness, ObservedIdle); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := production.store.mailbox(t, coordBox); len(got) != 0 {
		t.Fatalf("the default window recorded a state at once: %+v", got)
	}
	clock.advance(DefaultSettleWindow - time.Millisecond)
	if err := production.reg.Observe(context.Background(), p.ID, p.Liveness, ObservedIdle); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := production.store.mailbox(t, coordBox); len(got) != 0 {
		t.Fatalf("a state held for less than the default window was recorded: %+v", got)
	}
	clock.advance(time.Millisecond)
	if err := production.reg.Observe(context.Background(), p.ID, p.Liveness, ObservedIdle); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := production.store.mailbox(t, coordBox); len(got) != 1 {
		t.Fatalf("a state held for the default window produced %d messages, want 1", len(got))
	}

	// And a caller that names a different window gets that one.
	hour := newObservedStand(t, time.Hour)
	held := mustRegister(t, hour.harness)
	hour.mustReading(t, held, ObservedIdle)
	hour.clock.advance(time.Hour - time.Millisecond)
	hour.mustReading(t, held, ObservedIdle)
	if got := hour.mailbox(t); len(got) != 0 {
		t.Fatalf("the window the record was built with was not the one applied: %+v", got)
	}
	hour.clock.advance(time.Millisecond)
	hour.mustReading(t, held, ObservedIdle)
	if got := hour.mailbox(t); len(got) != 1 {
		t.Fatalf("the named window produced %d messages, want 1", len(got))
	}
}

// A report and a state are different facts, and neither suppresses the other
// (design §4.3): a worker that says `done` and then settles idle has done two
// things, and a coordinator is told both, in the order they happened.
//
// The case this guards is not hypothetical — an interactive worker's whole
// shape is "finish the turn, speak, sit at the prompt" — and a design that had
// the report mark the worker as already-reported would make the idle invisible
// at exactly the moment the coordinator is waiting for it.
func TestAReportDoesNotSuppressTheIdleThatFollowsIt(t *testing.T) {
	const window = 3 * time.Second
	s := newObservedStand(t, window)
	p := mustRegister(t, s.harness)

	// The worker was working, then reported, then settled idle.
	s.mustReading(t, p, ObservedWorking)
	if _, err := s.reg.Declared(s.ctx, p.ID, p.Liveness,
		Declaration{OK: true, Summary: "read AGENTS.md", At: s.clock.now()}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	// A report is not a fact about the record's observed state, and the pane is
	// still WORKING as far as anything here has been told.
	s.mustReading(t, p, ObservedWorking)
	s.mustReading(t, p, ObservedIdle)
	s.clock.advance(window)
	s.mustReading(t, p, ObservedIdle)

	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("the coordinator has %d messages, want exactly the one idle observation: %+v", len(got), got)
	}
	if got[0].Observed == nil || got[0].Observed.State != ObservedIdle {
		t.Fatalf("the message is not the idle observation: %+v", got[0])
	}
	if got[0].Observed.Worker != p.ID {
		t.Fatalf("the observation names %q, want the worker that settled", got[0].Observed.Worker)
	}
	// And the record still holds the declaration: the two facts travel
	// separately and neither is the other's precondition.
	after, ok := s.store.read(t, p.ID)
	if !ok {
		t.Fatalf("the participant vanished")
	}
	if after.Declared == nil || !after.Declared.OK {
		t.Fatalf("the declaration did not reach the record: %+v", after)
	}
	// It is NOT terminal: the agent said it finished and its process is still
	// there, so it may be given more work (reduce's own reading).
	if after.State.Terminal() {
		t.Fatalf("a declaration with no exit terminalized the record: %q", after.State)
	}
}

// One settled state is one fact, even when two readings of it are in flight at
// once.
//
// The window is the one between the decision to place a fact and the write that
// places it, and it is real rather than theoretical: `held` answers under the
// machine's lock and the write happens after the lock is released, so a second
// reader arriving in between would be told the same thing and place a second
// row. Production drives both readers from one sweep goroutine today, which is
// exactly the kind of "true because of who calls it" that stops being true
// silently — a second sweep, a second caller, and a coordinator is told twice
// about one worker going idle.
//
// The second reading is started from INSIDE the first one's write (memStore's
// duringCommit), so the interleaving is produced rather than hoped for: racing
// two goroutines at this window is what the resolve/revoke trials failed to do
// 50 runs in a row (nocx-xn63t.4.7).
func TestTwoReadingsInFlightAtOncePlaceOneFact(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)

	// The hold is opened, so the next reading of the same state is a fact.
	s.mustReading(t, p, ObservedIdle)

	nested := 0
	s.store.duringCommit = func() {
		nested++
		// The second reader arrives between the first one's claim and its
		// write — the exact window.
		if err := s.reg.Observe(s.ctx, p.ID, p.Liveness, ObservedIdle); err != nil {
			t.Errorf("the second reading was refused: %v", err)
		}
	}
	s.mustReading(t, p, ObservedIdle)

	if nested != 1 {
		t.Fatalf("the second reading never ran inside the window, so this test asserted nothing")
	}
	if got := s.mailbox(t); len(got) != 1 {
		t.Fatalf("one settled state produced %d facts, want exactly 1: %+v", len(got), got)
	}
}

// And a claim that fails is RELEASED rather than lost: the caller reads a
// routing failure and the next reading of the same held state still lands.
// A claim held across a failure would suppress the fact forever, which for a
// lookup that failed once is a silent loss of the only evidence the coordinator
// would get.
func TestAFailedRoutingLeavesTheFactClaimableAndNotLost(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)

	// The first reading opens the hold, then the group's coordinator becomes
	// unreadable for exactly the write that would have placed the fact.
	s.mustReading(t, p, ObservedIdle)
	s.store.setFault("coordinatorsession", 1)

	err := s.reading(t, p, ObservedIdle)
	if err == nil {
		t.Fatalf("a routing failure was reported as a placed fact")
	}
	if !errors.Is(err, errInjected) {
		t.Fatalf("the failure is not named: %v", err)
	}
	if got := s.mailbox(t); len(got) != 0 {
		t.Fatalf("a failed routing placed a fact anyway: %+v", got)
	}

	// The retry is the next reading, and the fact is not lost.
	s.mustReading(t, p, ObservedIdle)
	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("after a failed routing the coordinator has %d facts, want the one: %+v", len(got), got)
	}
	if got[0].Observed == nil || got[0].Observed.State != ObservedIdle {
		t.Fatalf("the retry placed the wrong fact: %+v", got[0])
	}
}

// And the exit's reservation is tested the same way, because the sequential case
// above does NOT exercise it: two `Exited` calls one after the other are caught
// by `told`, and the in-flight check is what catches two in OVERLAP.
//
// Both really can overlap, which is why the check exists rather than being
// defensive: the supervisor reports the exit it watched, and the agent's own
// withdrawal reports the one it caused, and nothing orders the two goroutines.
func TestTwoConcurrentExitReportsPlaceOneFact(t *testing.T) {
	s := newObservedStand(t, 0)
	p := mustRegister(t, s.harness)

	nested := 0
	s.store.duringCommit = func() {
		nested++
		// The second report arrives between the first one's claim and its
		// write. The record admits it — an exit with no declaration is the
		// state admit's terminal guard exempts, precisely so a late
		// declaration can still refine it — so what stops a second message
		// here is only the observation's own reservation.
		if _, err := s.reg.Exited(s.ctx, p.ID, p.Liveness, Exit{Cause: "exited", Code: 0, At: s.clock.now()}); err != nil {
			t.Logf("the second exit report was refused by the record (%v); the count below is what matters", err)
		}
	}
	if err := s.exit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}

	if nested != 1 {
		t.Fatalf("the second report never ran inside the window, so this test asserted nothing")
	}
	got := s.mailbox(t)
	if len(got) != 1 {
		t.Fatalf("one exit produced %d facts, want exactly 1: %+v", len(got), got)
	}
	if got[0].Observed == nil || got[0].Observed.State != ObservedExited {
		t.Fatalf("the fact is not the exit: %+v", got[0])
	}
}
