package workers

// The wake (nocx-luqz9.3; ADR-0070 decision 4, design §5).
//
// Every test here drives TIME through the injected alarm and the record's own
// clock, and none of them depends on a duration: the retry pause is two minutes
// in the product, so a test that waited for one would be measuring nothing.
// What is asserted is what the mechanism DOES at each of the moments it can be
// at — a batch that has never been typed, a line that has been typed and not
// read, a budget that has been spent — and every one of those moments is
// reached by moving a clock rather than by sleeping.
//
// The readings of the coordinator's own pane come through the RECORD
// (ObserveCoordinator), because that is where the settle window lives and
// because a test that called the wake directly would be asserting the wake's
// rules against a caller the product does not have.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// ── the doubles ───────────────────────────────────────────────────────────

// fakeAlarms records what is armed and lets a test fire it. It counts ARMED
// alarms rather than total ones, because the claim being made is about what is
// running now.
type fakeAlarms struct {
	mu     sync.Mutex
	armed  map[int]func()
	nextID int
	ds     []time.Duration
}

func newFakeAlarms() *fakeAlarms { return &fakeAlarms{armed: map[int]func(){}} }

func (a *fakeAlarms) After(d time.Duration, f func()) func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	id := a.nextID
	a.armed[id] = f
	a.ds = append(a.ds, d)
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.armed, id)
	}
}

func (a *fakeAlarms) running() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.armed)
}

// fireAll runs every alarm armed at this instant, which is what the passage of
// time looks like from here. It TAKES them before running any, so an alarm a
// callback arms does not fire in the same round: the pause is a pause, and a
// round that consumed the whole chain at once could not tell one interval from
// four.
func (a *fakeAlarms) fireAll() {
	a.mu.Lock()
	fs := make([]func(), 0, len(a.armed))
	for id, f := range a.armed {
		fs = append(fs, f)
		delete(a.armed, id)
	}
	a.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

type wakeCall struct {
	session   string
	text      string
	delivered bool
}

type fakeWaker struct {
	mu    sync.Mutex
	calls []wakeCall
	out   WakeOutcome
}

func (w *fakeWaker) Wake(_ context.Context, session, text string) WakeOutcome {
	w.mu.Lock()
	defer w.mu.Unlock()
	if session == "" {
		// The real waker refuses this rather than typing into whichever pane
		// happens to be keyed by the empty string, and a double that accepted
		// an address the product refuses would let a defect through here and
		// only there.
		w.calls = append(w.calls, wakeCall{session: session, text: text})
		return WakeOutcome{Reason: "this worker records no coordinator session to type into"}
	}
	w.calls = append(w.calls, wakeCall{session: session, text: text, delivered: w.out.Delivered})
	return w.out
}

func (w *fakeWaker) seen() []wakeCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wakeCall(nil), w.calls...)
}

// typed is the lines the pane actually took. It is the count the retry budget
// is spent from, so a test about the budget reads this rather than the call
// list: a refused attempt appears in one and not the other.
func (w *fakeWaker) typed() []wakeCall {
	var out []wakeCall
	for _, c := range w.seen() {
		if c.delivered {
			out = append(out, c)
		}
	}
	return out
}

type fakeEscalation struct {
	mu      sync.Mutex
	notices []Notice
	// refuse makes every notice fail to deliver, which is criterion 8's case:
	// a backend with no renderer attached, so no channel is reached.
	refuse error
}

func (e *fakeEscalation) Escalate(_ context.Context, n Notice) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notices = append(e.notices, n)
	return e.refuse
}

func (e *fakeEscalation) seen() []Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Notice(nil), e.notices...)
}

func delivered() WakeOutcome { return WakeOutcome{Delivered: true} }

// ── the stand ─────────────────────────────────────────────────────────────

// wakeStand is one wired record whose clock the test moves, over the harness's
// own doubles, plus the two facts a wake test needs that no other test does:
// the coordinator's settle window and how many lines a batch may get.
type wakeStand struct {
	*harness
	clock  *movingClock
	window time.Duration
	ctx    context.Context
	// worker is the one participant every case starts from. It is held here
	// rather than registered per test because the small cases are about ONE
	// worker's mail and a coordinator that holds more than one changes what the
	// record owes judgement on — which is a different test's subject, and the
	// mistake it causes (a fact read as routine, a coordinator read as holding
	// nothing) is silent.
	worker Participant
}

// newWakeStand wires a record whose settle window and attempt limit are the
// case's own arguments, because a test asserting "not before the window" or
// "exactly the limit of lines" has to say what each one is.
func newWakeStand(t *testing.T, window time.Duration, attempts int) *wakeStand {
	t.Helper()
	return newWakeStandWith(t, window, attempts)
}

// newWakeStandWith is newWakeStand with the options named, so a case that is
// about what the wake LOGS can hand it the context its own logger lives in.
// The zero context is the ordinary one: a reading driven by a sweep has no
// request to inherit, and log.From answers it with the process root — which is
// exactly what production's observer passes.
func newWakeStandWith(t *testing.T, window time.Duration, attempts int, opts ...Option) *wakeStand {
	t.Helper()
	return newWakeStandCtx(t, context.Background(), window, attempts, opts...)
}

func newWakeStandCtx(t *testing.T, ctx context.Context, window time.Duration, attempts int, opts ...Option) *wakeStand {
	t.Helper()
	clock := &movingClock{at: time.Unix(1_700_000_000, 0).UTC()}
	h := newHarnessBound(t, 8, append([]Option{WithSettleWindow(window)}, opts...)...)
	h.reg.now = clock.now
	h.wakeState.attempts = attempts
	s := &wakeStand{harness: h, clock: clock, window: window, ctx: ctx}
	// A coordinator that has never been read holds nothing, and the record drops
	// a reading about it — so the one worker every case needs is registered here
	// rather than in each test.
	s.worker = mustRegister(t, h)
	return s
}

// settle moves the clock by one window, which is what makes the next reading
// of an unchanged state a state that has HELD.
func (s *wakeStand) settle() { s.clock.advance(s.window) }

// says admits one settled reading of the coordinator's own pane, delivered the
// way the sweep delivers it.
//
// The window is crossed between readings rather than assumed: a state is a fact
// only once it has held, and handing the record the same reading twice inside
// one instant is not a hold — it is one reading taken twice.
func (s *wakeStand) says(t *testing.T, state ObservedState) {
	t.Helper()
	s.settle()
	s.harness.reg.ObserveCoordinator(s.ctx, coordSession, state)
	s.settle()
	s.harness.reg.ObserveCoordinator(s.ctx, coordSession, state)
}

// reading delivers EXACTLY ONE settled reading of the coordinator's own pane,
// which says() cannot: says crosses the window before each of its two calls, so
// one says is one or two attempts depending on whether the state was already
// held. A test counting what a FAILING notice cost needs one reading to be one
// attempt, so it drives the record directly.
func (s *wakeStand) reading(t *testing.T, state ObservedState) {
	t.Helper()
	s.settle()
	s.harness.reg.ObserveCoordinator(s.ctx, coordSession, state)
}

// settles puts one worker's pane into a state that holds, which is what places
// a message in the coordinator's mailbox and what the wake is told about.
func (s *wakeStand) settles(t *testing.T, p Participant, state ObservedState) {
	t.Helper()
	s.settle()
	if err := s.harness.reg.Observe(s.ctx, p.ID, p.Liveness, state); err != nil {
		t.Fatalf("observe %s: %v", state, err)
	}
	s.settle()
	if err := s.harness.reg.Observe(s.ctx, p.ID, p.Liveness, state); err != nil {
		t.Fatalf("observe %s: %v", state, err)
	}
}

// read is the coordinator's own fetch of its mailbox, which is the only thing
// that clears a batch.
func (s *wakeStand) read(t *testing.T) Fetch {
	t.Helper()
	got, err := s.harness.reg.Inbox(s.ctx, coordBox, coordBox, 0)
	if err != nil {
		t.Fatalf("the coordinator's own read: %v", err)
	}
	return got
}

// mailed is what the mailbox holds, read without handing anything over — a
// fetch would advance a cursor as part of the arrangement.
func (s *wakeStand) mailed(t *testing.T) []Message {
	t.Helper()
	return s.harness.store.mailbox(t, coordBox)
}

func (s *wakeStand) lines() []wakeCall { return s.harness.wake.typed() }
func (s *wakeStand) attempts() []wakeCall {
	return s.harness.wake.seen()
}
func (s *wakeStand) notices() []Notice { return s.harness.human.seen() }

// ── 1. the criterion: idle, unread, exactly one line ──────────────────────

// THE CRITERION. A coordinator sitting idle with unread mail is told once, in a
// line that says what to call and nothing of what its workers said, and the
// coordinator's own read is what ends the batch.
func TestAnIdleCoordinatorWithUnreadMailIsToldOnceAndItsReadClearsTheBatch(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker

	// The coordinator is idle before any mail exists: the wake has nothing to
	// say and says nothing, and that is the state everything below starts from.
	s.says(t, ObservedIdle)
	if got := s.lines(); len(got) != 0 {
		t.Fatalf("nocx typed into a coordinator with nothing to read: %+v", got)
	}

	// Now the worker settles idle, which is one message in the mailbox.
	s.settles(t, p, ObservedIdle)
	if got := len(s.mailed(t)); got != 1 {
		t.Fatalf("the mailbox holds %d messages, want the worker's one observation", got)
	}

	got := s.lines()
	if len(got) != 1 {
		t.Fatalf("the coordinator was typed at %d times, want exactly 1: %+v", len(got), got)
	}
	if got[0].session != coordSession {
		t.Fatalf("the line was typed at %q, want the coordinator's own session %q", got[0].session, coordSession)
	}
	// THE WHOLE LINE, asserted as an equality rather than by looking for
	// absent words. That is the strongest form of "it carries no worker
	// output": there is no room in it for anybody's text, and a line that grew
	// one would fail here rather than in a reviewer's head. It is also design
	// §5.2's sentence, verbatim.
	const want = "nocx: you have 1 new messages from your workers. Call workers.inbox."
	if got[0].text != want {
		t.Fatalf("the wake line is\n  %q\nwant\n  %q", got[0].text, want)
	}

	// The coordinator reads, and that ends the batch: no timer is left armed,
	// so a batch that has been read is not retyped at the pause.
	if read := s.read(t); len(read.Messages) != 1 {
		t.Fatalf("the coordinator was handed %d messages, want 1", len(read.Messages))
	}
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("a read left %d alarms armed, want none", got)
	}
	s.harness.alarms.fireAll()
	if got := len(s.lines()); got != 1 {
		t.Fatalf("a batch that was read produced %d lines, want the one it already had", got)
	}
	if got := len(s.notices()); got != 0 {
		t.Fatalf("the human was told about mail that was read: %+v", got)
	}
}

// ── 2. working: nothing typed, no timer, and the line comes when it settles ──

// A COORDINATOR THAT IS WORKING IS NOT TYPED AT, AND NO RETRY IS ARMED FOR IT.
//
// This is the hazard the typing gate exists for: a line typed into an agent
// mid-turn lands inside its turn, and into a pane showing a menu it ANSWERS the
// menu. The line waits for the pane to be waiting — and it is not lost while it
// waits, which is the half that would make this a silent drop.
func TestACoordinatorThatIsWorkingIsNotTypedAndGetsItsLineWhenItSettlesIdle(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker

	s.says(t, ObservedWorking)
	s.settles(t, p, ObservedIdle)

	if got := s.lines(); len(got) != 0 {
		t.Fatalf("nocx typed into a coordinator that was working: %+v", got)
	}
	// NOTHING IS RUNNING EITHER. A timer armed here would fire into whatever
	// the pane had become by then, which is the same hazard one pause later.
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("a working coordinator left %d alarms armed, want none", got)
	}

	// The mail is still unread, so the idle that follows is when it gets its
	// line.
	s.says(t, ObservedIdle)
	got := s.lines()
	if len(got) != 1 {
		t.Fatalf("the coordinator settled idle with unread mail and got %d lines, want 1: %+v", len(got), got)
	}
	if !strings.Contains(got[0].text, "workers.inbox") {
		t.Fatalf("the line does not say what to call: %q", got[0].text)
	}
}

// ── 3. mail arriving after a line, before a read ──────────────────────────

// MAIL THAT ARRIVES AFTER A LINE WAS TYPED DOES NOT PRODUCE A SECOND LINE AT
// ONCE. The coordinator has a line it has not acted on, and a second one would
// be the same instruction twice. It arrives at the retry pause, like any other
// retry — and it arrives, which is what "one line per batch" must not cost.
func TestMailArrivingAfterALineWaitsForTheRetryPause(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	first := s.worker
	second := mustRegister(t, s.harness)

	s.says(t, ObservedIdle)
	s.settles(t, first, ObservedIdle)
	if got := len(s.lines()); got != 1 {
		t.Fatalf("lines after the first observation = %d, want 1", got)
	}

	s.settles(t, second, ObservedIdle)
	if got := len(s.lines()); got != 1 {
		t.Fatalf("mail arriving after a line produced %d lines, want the pause to come first: %+v",
			len(s.lines()), s.lines())
	}
	// And the pause is what produces it.
	s.harness.alarms.fireAll()
	got := s.lines()
	if len(got) != 2 {
		t.Fatalf("lines after the retry pause = %d, want 2: %+v", len(got), got)
	}
	// The second line counts BOTH, because the count is taken when the line is
	// written rather than when the first one was: a coordinator told "1" about
	// a mailbox holding two would act on the wrong number.
	if !strings.Contains(got[1].text, "2 new messages") {
		t.Fatalf("the retry line does not count what is now waiting: %q", got[1].text)
	}
}

// ── 4. the retries, and the human at the end of them ──────────────────────

// THE BUDGET IS SPENT IN LINES AT PAUSES, AND THEN THE HUMAN IS TOLD.
//
// Exactly the limit, exactly at the pause intervals, and exactly one notice:
// the person is told once about one situation, because a card per fact is how
// an attention surface becomes noise — and the notice follows the pause rather
// than the last line, because the coordinator has had its last chance only once
// the interval after it has gone unanswered.
func TestTheBatchIsRetypedAtThePauseUntilTheLimitAndThenTheHumanIsTold(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker

	s.says(t, ObservedIdle)
	s.settles(t, p, ObservedIdle)
	if got := len(s.lines()); got != 1 {
		t.Fatalf("the first line = %d, want 1", got)
	}

	for want := 2; want <= 3; want++ {
		s.harness.alarms.fireAll()
		if got := len(s.lines()); got != want {
			t.Fatalf("after pause %d the coordinator had %d lines, want %d", want-1, got, want)
		}
		if got := len(s.notices()); got != 0 {
			t.Fatalf("the human was told before the budget was spent: %+v", got)
		}
	}
	s.harness.alarms.fireAll()

	told := s.notices()
	if len(told) != 1 {
		t.Fatalf("notices = %d, want exactly one after the limit: %+v", len(told), told)
	}
	if told[0].Mailbox != coordBox || told[0].Kind != NoticeUnread {
		t.Fatalf("the notice is %+v, want an unread notice about %q", told[0], coordBox)
	}
	if told[0].Lines != 3 {
		t.Fatalf("the notice says %d lines were typed, want 3", told[0].Lines)
	}
	if told[0].Unread == 0 {
		t.Fatalf("the notice does not say how much is waiting: %+v", told[0])
	}
	// And no more lines: the budget is spent, and a coordinator cannot be typed
	// at for ever about one batch.
	s.harness.alarms.fireAll()
	if got := len(s.lines()); got != 3 {
		t.Fatalf("lines after the notice = %d, want the 3 the budget allows", got)
	}
	if got := len(s.notices()); got != 1 {
		t.Fatalf("the human was told %d times for one situation, want 1", got)
	}
}

// A READ BETWEEN TWO RETRIES STOPS THEM AND RAISES NOTHING. This is the whole
// point of a bounded retry: the coordinator answered, so there is nothing to
// escalate — and nobody is called about mail somebody has read.
func TestAReadBetweenRetriesStopsThemAndRaisesNoNotice(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker

	s.says(t, ObservedIdle)
	s.settles(t, p, ObservedIdle)
	s.harness.alarms.fireAll()
	if got := len(s.lines()); got != 2 {
		t.Fatalf("lines before the read = %d, want 2", got)
	}

	s.read(t)
	s.harness.alarms.fireAll()
	s.harness.alarms.fireAll()

	if got := len(s.lines()); got != 2 {
		t.Fatalf("a read did not stop the retries: %d lines", got)
	}
	if got := len(s.notices()); got != 0 {
		t.Fatalf("the human was told about a batch that was read: %+v", got)
	}
}

// THE COUNT IS EVERYTHING UNREAD, NOT ONE PAGE OF IT.
//
// A mailbox page is bounded (MaxFetch), and the naive way to count what is
// waiting is one call to it — which would tell a coordinator "50" about a box
// holding eighty and be wrong in the one direction that matters, because the
// number IS the message: it is how much the coordinator knows is waiting. The
// loop pages until one comes back short, and this is what says so.
func TestTheLineCountsUnreadMailPastOnePage(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	s.says(t, ObservedIdle)

	// More than one page, so a single call cannot see them all.
	total := MaxFetch + 7
	for i := 0; i < total; i++ {
		sayTo(t, s.harness, coordBox, coordBox, fmt.Sprintf("message %d", i+1))
	}

	s.harness.wakeState.Arrived(s.ctx, coordBox)
	got := s.lines()
	if len(got) != 1 {
		t.Fatalf("lines = %d, want 1: %+v", len(got), got)
	}
	want := fmt.Sprintf("nocx: you have %d new messages from your workers. Call workers.inbox.", total)
	if got[0].text != want {
		t.Fatalf("the line says\n  %q\nwant\n  %q", got[0].text, want)
	}
}

// ── 5. a refused type is not an attempt ───────────────────────────────────

// A REFUSED TYPE SPENDS NO ATTEMPT, AND THE NEXT IDLE IS WHAT RETRIES IT.
//
// A refusal means the screen changed under us — a menu came up, the pane
// stopped being watched, the session went away — so the thing that says it is
// safe to try again is the SCREEN HAVING COME BACK, not a clock that would type
// into whatever it became.
func TestARefusedTypeSpendsNoAttemptAndRetriesAtTheNextIdle(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker
	s.harness.wake.out = WakeOutcome{Reason: "that pane is asking you something"}

	s.says(t, ObservedIdle)
	s.settles(t, p, ObservedIdle)

	if got := s.attempts(); len(got) != 1 {
		t.Fatalf("attempts = %d, want the one the wake made", len(got))
	}
	if got := s.lines(); len(got) != 0 {
		t.Fatalf("a refused type was counted as a line: %+v", got)
	}
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("a refused type armed %d retries; a refusal is not something to come back to on a clock", got)
	}
	// The budget is untouched, so a refusal alone never reaches the human.
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 0 {
		t.Fatalf("a refusal reached the human: %+v", got)
	}

	// The pane comes back — the menu is answered, or the screen that refused is
	// gone — and the batch is retried, the same batch, because nothing was
	// read. The state has to CHANGE for this to be a new episode: a pane that
	// stayed idle would still be the reading the refusal was made under, and a
	// refusal is precisely a claim that the pane was not idle.
	s.says(t, ObservedWorking)
	s.says(t, ObservedIdle)
	if got := len(s.attempts()); got != 2 {
		t.Fatalf("the coordinator's pane came back and the batch was not retried: %d attempts", got)
	}
}

// ── 6. the blocked coordinator ────────────────────────────────────────────

// A COORDINATOR BLOCKED ON ITS OWN SCREEN WITH LIVE WORKERS IS THE HUMAN'S, AT
// ONCE. It is not a retry's business: a line typed at a menu answers it, so no
// number of lines changes anything, and nothing is being coordinated.
//
// "At once" is after the settle window and no later — the reading is a settled
// one, which is the same discipline every other fact here takes. And ONE: the
// person is told when the episode begins, not once per sweep that finds it
// still blocked.
func TestABlockedCoordinatorWithLiveWorkersIsTheHumansAndOneWithNoneIsNot(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	// A blocked coordinator whose workers have all finished is not stuck: there
	// is nothing to coordinate, and that is the whole of its notice's
	// condition.
	if _, err := s.harness.reg.Exited(s.ctx, s.worker.ID, s.worker.Liveness, Exit{Cause: "exited"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 0 {
		t.Fatalf("the human was told about a coordinator with no live workers: %+v", got)
	}

	// Now one that holds a live worker.
	live := mustRegister(t, s.harness)
	if live.State != StateLive {
		t.Fatalf("the second worker is %q, want live", live.State)
	}
	s.says(t, ObservedIdle)
	s.says(t, ObservedBlocked)

	told := s.notices()
	if len(told) != 1 {
		t.Fatalf("notices = %d, want exactly one: %+v", len(told), told)
	}
	if told[0].Kind != NoticeBlocked || told[0].Mailbox != coordBox {
		t.Fatalf("the notice is %+v, want a blocked notice about %q", told[0], coordBox)
	}
	if told[0].LiveWorkers != 1 {
		t.Fatalf("the notice says %d live workers, want 1", told[0].LiveWorkers)
	}
	// Not one per settled reading: the state does not change, so neither does
	// the news.
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 1 {
		t.Fatalf("the person was told %d times for one blocked episode, want 1", got)
	}
}

// A BLOCKED NOTICE THAT FAILS IS RETRIED AT THE PAUSE, AND ONE THAT SUCCEEDS IS
// NOT REPEATED.
//
// This is reach's rule for a blocked coordinator: a notice the pipeline refused
// has reached nobody, so the episode stays claimed and a retry is armed — and
// the retry waits for the PAUSE rather than arriving on the next reading,
// because a blocked coordinator is read once a second and a retry hung off the
// reading would hit the pipeline that just refused sixty times a minute for as
// long as the block lasted.
//
// The middle third of this test is what says so: readings arrive, and the count
// does NOT move. Without the pause bound they would each raise a notice, which
// is the shape of the defect the first version of this had.
func TestABlockedNoticeThatFailsIsRetriedAtThePause(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")

	// A live worker, so a blocked coordinator is the situation the notice is
	// about: nothing is being coordinated.
	if s.worker.State != StateLive {
		t.Fatalf("the stand's worker is %q, want live", s.worker.State)
	}
	s.says(t, ObservedIdle)
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 1 {
		t.Fatalf("notices = %d, want the one attempt", got)
	}

	// The failure is said out loud with the reason, which is where a person
	// diagnosing "nocx never told me" looks. The LEVEL is asserted and not just
	// the text: a line this important that had sunk to Debug would be invisible
	// in a shipped build where the level is not Debug.
	if !logtest.WaitFor(ctx, 5*time.Second, func(r logtest.Record) bool {
		return r.Level == slog.LevelError &&
			strings.Contains(r.Message, "the human was not told that coordination has stopped")
	}) {
		t.Fatalf("a refused blocked notice was not reported at Error")
	}
	// And the retry is a TIMER, not the next reading.
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("a refused blocked notice left %d retries armed, want 1", got)
	}

	// READINGS DO NOT RE-RAISE: the coordinator stays blocked and holding its
	// worker, and every further settled reading finds the episode already
	// claimed and its retry on the clock.
	for i := 0; i < 5; i++ {
		s.reading(t, ObservedBlocked)
	}
	if got := len(s.notices()); got != 1 {
		t.Fatalf("a blocked episode was raised once per reading: %d notices after 5 readings", got)
	}

	// The pause is what retries it.
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 2 {
		t.Fatalf("notices after the pause = %d, want the retry", got)
	}
	// Still refusing, so the next pause is armed in its turn — bounded, one
	// attempt per pause, for as long as the coordinator stays blocked.
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("the second refusal armed %d retries, want 1", got)
	}

	// Now the pipeline takes it. The episode is over when it succeeds: a
	// blocked coordinator that stays blocked is ONE notice, not one per pause.
	s.harness.human.mu.Lock()
	s.harness.human.refuse = nil
	s.harness.human.mu.Unlock()
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 3 {
		t.Fatalf("notices after the pipeline recovered = %d, want 3", got)
	}
	s.harness.alarms.fireAll()
	s.reading(t, ObservedBlocked)
	if got := len(s.notices()); got != 3 {
		t.Fatalf("an episode that was reported was raised again: %d notices", got)
	}
}

// AND A COORDINATOR THAT STOPS BEING BLOCKED ENDS THE EPISODE, retry included:
// the situation the pending notice was about is over, so the armed timer fires
// into nothing and tells nobody.
//
// The test stops at that assertion deliberately. A coordinator that is cleared
// and then blocks AGAIN is a NEW episode and is legitimately raised again —
// that is one notice per episode, which is the rule, not a leak — so re-blocking
// here would be measuring the second rule instead of this one.
func TestACoordinatorThatStopsBeingBlockedEndsTheEpisodeAndItsRetry(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")

	s.says(t, ObservedIdle)
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 1 {
		t.Fatalf("notices = %d, want the one refused attempt", got)
	}
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("retries armed = %d, want 1", got)
	}

	// The person clears the menu the coordinator was stuck on, and the pipeline
	// recovers — so if the stale timer still raised anything, it would be TAKEN
	// and the count would move.
	s.harness.human.mu.Lock()
	s.harness.human.refuse = nil
	s.harness.human.mu.Unlock()
	// says and not reading: a state CHANGE is not a settled reading, so one
	// reading only records that this pane is no longer blocked — the episode
	// ends on the reading that finds that state HELD, which is the same two-step
	// every other fact here takes.
	s.says(t, ObservedIdle)

	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 1 {
		t.Fatalf("a retry outlived the episode it was about: %d notices", got)
	}
}

// THE TWO NOTICES DO NOT SHARE A GUARD. A batch whose unread notice was
// DELIVERED has its mark set, and a blocked coordinator can then need a notice
// of its own — including a pause retry if the pipeline refuses it. The timer
// belongs to the batch, so an arming rule that consulted the UNREAD mark would
// drop the blocked retry and tell nobody, which is the same silent loss
// reach exists to prevent.
func TestABlockedRetryIsArmedEvenWhenTheUnreadNoticeWasDelivered(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))

	// An unread batch spends its whole budget and its notice is DELIVERED, so
	// the unread mark is set when the blocked episode begins.
	s.says(t, ObservedIdle)
	s.settles(t, s.worker, ObservedIdle)
	for i := 0; i < 3; i++ {
		s.harness.alarms.fireAll()
	}
	if got := len(s.notices()); got != 1 {
		t.Fatalf("notices after the unread budget = %d, want 1", got)
	}

	// The coordinator is now blocked and holding a live worker, and the
	// pipeline refuses this notice.
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")
	s.says(t, ObservedWorking)
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 2 {
		t.Fatalf("notices after the blocked attempt = %d, want 2", got)
	}
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("the blocked retry is not on the clock: %d timers armed, want 1", got)
	}

	// And the pause is what retries it.
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 3 {
		t.Fatalf("notices after the pause = %d, want the blocked retry", got)
	}
}

// MAIL ARRIVING WHILE A BLOCKED NOTICE IS WAITING DOES NOT KILL THE NOTICE.
//
// A blocked coordinator can still receive mail — a worker reporting, an
// observation landing — and that arrival runs the batch's own path. The blocked
// notice's retry is on the batch's one timer, so a path that stops the timer
// without clearing the mark leaves the episode claimed and the retry gone: the
// person is never told, and nothing ever tries again. That is the same silent
// loss reach exists to prevent, reached from the other side.
func TestMailArrivingWhileABlockedRetryIsPendingDoesNotKillTheNotice(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))

	// A blocked coordinator holding a live worker, whose notice is refused —
	// so a retry is armed and the episode is claimed.
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")
	s.says(t, ObservedIdle)
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 1 {
		t.Fatalf("notices = %d, want the one refused attempt", got)
	}
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("timers = %d, want the blocked notice's retry", got)
	}

	// Mail arrives while the coordinator is still blocked: a second worker
	// settles idle, which is one more row in the batch's mailbox.
	other := mustRegister(t, s.harness)
	s.settles(t, other, ObservedIdle)

	// The retry must still be on the clock, and the pause must still reach the
	// person.
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("mail arriving while blocked left %d timers, want the blocked retry still armed", got)
	}
	s.harness.human.mu.Lock()
	s.harness.human.refuse = nil
	s.harness.human.mu.Unlock()
	s.harness.alarms.fireAll()
	var blocked int
	for _, n := range s.notices() {
		if n.Kind == NoticeBlocked {
			blocked++
		}
	}
	if blocked != 2 {
		t.Fatalf("blocked notices = %d, want the attempt and its retry", blocked)
	}
}

// A COORDINATOR THAT STARTS WORKING IS NOT BLOCKED, and that is why its
// reading ends the episode rather than preserving the notice.
//
// This is the asymmetry with the mail path, asserted rather than argued from
// the comment. CONTEXT.md's Blocked is "cannot continue until somebody steps
// in" — a menu, a question, an agent error — so a reading that finds the
// coordinator WORKING says that stopped being true. The episode is over and
// nothing is lost, because a coordinator that blocks AGAIN is a new episode
// and gets its own notice.
func TestACoordinatorThatStartsWorkingEndsTheEpisodeAndReBlocksFresh(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")

	s.says(t, ObservedIdle)
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 1 {
		t.Fatalf("notices = %d, want the one refused attempt", got)
	}

	// It gets on with its work, so it is not stuck and the retry is moot.
	s.says(t, ObservedWorking)
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("a working coordinator left %d timers, want none: the block ended", got)
	}
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 1 {
		t.Fatalf("a retry outlived the block it was about: %d notices", got)
	}

	// And a NEW block is a new episode: the person is told, which is what says
	// the first notice was not lost.
	s.harness.human.mu.Lock()
	s.harness.human.refuse = nil
	s.harness.human.mu.Unlock()
	s.says(t, ObservedBlocked)
	if got := len(s.notices()); got != 2 {
		t.Fatalf("a coordinator that blocked again was not reported: %d notices", got)
	}
}

// A LINE OWED AT IDLE IS TYPED AT ONCE, even when the batch's one timer was
// armed for a BLOCKED NOTICE that is now moot.
//
// This is the shape the earlier version of this test got wrong, and the mistake
// is worth naming: the refusal has to be in place BEFORE the mail lands, or the
// type is delivered and the batch stops owing a line — and then nothing about
// the timer is being tested at all. With the refusal first, a line is owed and
// NO attempt has been spent (a refusal costs none), which is precisely the
// state in which the batch's one timer can be held by something else.
//
// What the cancellation buys, concretely: without it the idle reading finds
// `armed` true, arms nothing and types nothing; the notice's timer then fires,
// consumes `blockedRetry`, sees an idle coordinator and RETURNS — so the line
// the coordinator owes is never typed, and no timer remains to type it. The
// episode did not lose a notice; the batch lost its line.
func TestALineOwedAtIdleIsTypedAtOnceWhileABlockedRetryIsPending(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 3, WithLogger(log.From(ctx)))

	// A line is owed and its type is REFUSED, so no attempt is spent. The
	// refusal is armed BEFORE the mail so the refusal is what meets the type.
	s.says(t, ObservedIdle)
	s.harness.wake.out = WakeOutcome{Reason: "that pane is asking you something"}
	s.settles(t, s.worker, ObservedIdle)
	if got := len(s.lines()); got != 0 {
		t.Fatalf("lines = %d, want none: the type was refused and costs no attempt", got)
	}
	// A refused type arms NOTHING — the retry it wants is the pane coming back,
	// not a clock — so the batch has a line owed and no timer at all, which is
	// the state the block is about to occupy.
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("timers = %d, want none after a refusal", got)
	}

	// The coordinator blocks and its notice is refused, so the batch's one
	// timer — the only one it will have — belongs to the notice.
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")
	s.says(t, ObservedWorking)
	s.says(t, ObservedBlocked)
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("timers = %d, want the blocked notice's retry", got)
	}

	// The person clears the menu and the pane will take a line again. The owed
	// line must be typed NOW, not lost to a timer armed for a notice.
	s.harness.wake.out = WakeOutcome{Delivered: true}
	s.says(t, ObservedIdle)
	if got := len(s.lines()); got != 1 {
		t.Fatalf("the coordinator owed a line and has %d: a timer armed for a "+
			"notice held the batch's clock and nothing typed the line", got)
	}
}

// ── 7. nothing is raised from a per-fact deadline ─────────────────────────

// A FACT ALONE ARMS NO TIMER AND CALLS NOBODY (nocx-luqz9.3's deletion, design
// §5).
//
// The mechanism this replaced put every fact under its own deadline, so a
// worker's declaration could reach the human on its own. Two mechanisms that
// can both call the human will, and the fact that reaches a person is not "this
// worker declared" but "this coordinator has not read anything in a while".
func TestAFactAloneArmsNoTimerAndCallsNobody(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	p := s.worker

	if _, err := s.harness.reg.Declared(s.ctx, p.ID, p.Liveness,
		Declaration{OK: true, Summary: "done"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := len(s.harness.reg.Undispatched()); got != 1 {
		t.Fatalf("undispatched = %d, want the fact recorded", got)
	}
	if got := s.harness.alarms.running(); got != 0 {
		t.Fatalf("a fact armed %d timers; nothing here times a fact", got)
	}
	// The wake did not run either, and could not: a worker's declaration puts
	// nothing in the coordinator's MAILBOX — an observation does — so there is
	// no batch and nothing to type.
	if got := s.attempts(); len(got) != 0 {
		t.Fatalf("a fact alone produced a line: %+v", got)
	}
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 0 {
		t.Fatalf("a fact alone reached the human: %+v", got)
	}
}

// ── 8. the failure path: nobody to tell ───────────────────────────────────

// IF THE NOTICE CANNOT BE DELIVERED, NOTHING IS PRETENDED AND NOTHING IS LOST.
//
// The attempt is logged, the batch is exactly as it was, and a later read still
// clears it — which is what makes a missing renderer a diagnosable state rather
// than a coordinator silently typed at for ever.
func TestANoticeThatCannotBeDeliveredLeavesTheBatchUntouched(t *testing.T) {
	ctx, _ := logtest.New(t)
	s := newWakeStandCtx(t, ctx, 3*time.Second, 1, WithLogger(log.From(ctx)))
	p := s.worker
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")

	s.says(t, ObservedIdle)
	s.settles(t, p, ObservedIdle)
	s.harness.alarms.fireAll()

	if got := s.notices(); len(got) != 1 {
		t.Fatalf("the notice was not even attempted: %+v", got)
	}
	// The failure is said out loud AT ERROR, which is where a person diagnosing
	// "nocx never told me" looks and where the record's own convention puts a
	// loss nobody else will report. The LEVEL is asserted and not just the
	// text: a line this important that had sunk to Debug would be invisible in
	// a shipped build, and a test that accepted any level would pass while the
	// one report of the last resort went quiet.
	if !logtest.WaitFor(ctx, 5*time.Second, func(r logtest.Record) bool {
		return r.Level == slog.LevelError &&
			strings.Contains(r.Message, "the human was not told that coordination has stopped")
	}) {
		t.Fatalf("a refused notice was not reported at Error")
	}

	// The batch is unchanged: the mail is still there and still unread, so a
	// read still works and the count a later line would carry is still right.
	if got := len(s.mailed(t)); got == 0 {
		t.Fatalf("the mail was lost with the notice")
	}
	read := s.read(t)
	if len(read.Messages) == 0 {
		t.Fatalf("a read after an undelivered notice found nothing")
	}
	// AND IT NEVER RETRIED, because the read ended the batch: `notified` is not
	// the only thing that stops a notice, and a retry armed under a batch the
	// coordinator has since read would be exactly the false alarm this whole
	// mechanism exists to avoid.
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 1 {
		t.Fatalf("a batch that was read still raised a notice: %+v", got)
	}
}

// AND A REFUSED NOTICE IS RETRIED RATHER THAN GIVING UP SILENTLY.
//
// The seam's refusal is the pipeline declining to take the event at all —
// admission refused, or the caller cancelled. That notice reached nobody, and
// the one path whose whole purpose is to be the last resort must not treat its
// first refusal as done: the mark is cleared and the next pause tries again.
func TestARefusedNoticeIsRetriedAtTheNextPause(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 1)
	p := s.worker
	s.harness.human.refuse = fmt.Errorf("the pipeline is shutting down")

	s.says(t, ObservedIdle)
	s.settles(t, p, ObservedIdle)
	s.harness.alarms.fireAll()

	if got := s.notices(); len(got) != 1 {
		t.Fatalf("notices = %d, want the one attempt", len(got))
	}
	// The refusal did not end the batch, so the pause that follows it has a
	// retry armed under it.
	if got := s.harness.alarms.running(); got != 1 {
		t.Fatalf("a refused notice left %d retries armed, want 1", got)
	}
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 2 {
		t.Fatalf("notices after the retry = %d, want 2", got)
	}

	// And once one is taken, there is no third: the situation has been reported.
	s.harness.human.mu.Lock()
	s.harness.human.refuse = nil
	s.harness.human.mu.Unlock()
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 3 {
		t.Fatalf("notices after the pipeline recovered = %d, want 3", got)
	}
	s.harness.alarms.fireAll()
	if got := len(s.notices()); got != 3 {
		t.Fatalf("a notice the pipeline took was raised again: %d", got)
	}
}
