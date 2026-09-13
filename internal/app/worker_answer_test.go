package app

// The composition root's half of workers.answer (nocx-f545a.4), through the
// REAL typing gate, the real grid and the real rule, off the real capture of
// the folder-trust question. The pane's input is a stub pty that records what
// reached it, and the TUI's repaint after the movement key is supplied by the
// test — which is the one thing a stub pty cannot do — so what is asserted is
// the order the answerer must keep: move, wait for the screen to show it,
// confirm.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// selectYesRepaint is the TUI moving the trust question's selection one row
// down: the marker off "No, exit" and onto the Yes row, cursor parked on it.
const selectYesRepaint = "\x1b[14;2H \x1b[15;2H❯\x1b[15;2H"

func answerStand(t *testing.T) (*taskDeliveryStand, session.ID, *workerAnswerer) {
	t.Helper()
	stand := newTaskDeliveryStand(t)
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the worker's session: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	stand.watch.Sweep()
	return stand, sid, stand.answerer(t)
}

func TestAnAnswerMovesWaitsForTheScreenAndThenConfirms(t *testing.T) {
	stand, sid, answerer := answerStand(t)
	participant := workers.Participant{ID: "p-answer", Liveness: workers.Liveness{SessionID: string(sid)}}

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		a, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
		done <- outcome{a, err}
	}()

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the movement key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\x1b[B")
	})
	if strings.Contains(pty.read(), "\r") {
		t.Fatalf("the confirm key was sent before the screen showed the selection: %q", pty.read())
	}
	stand.grid.Feed(string(sid), []byte(selectYesRepaint))

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("answer = %+v, want submitted", got.a)
	}
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return pty.read() == "\x1b[B\r"
	})
}

// A screen that never shows the move is not confirmed on a guess: the answer
// comes back typed, with the reason, and the confirm key never reaches the pane.
func TestAnAnswerWhoseMoveNeverShowsConfirmsNothing(t *testing.T) {
	stand, sid, answerer := answerStand(t)
	participant := workers.Participant{ID: "p-answer", Liveness: workers.Liveness{SessionID: string(sid)}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != string(agenttyping.OutcomeTyped) || got.Reason == "" {
		t.Fatalf("answer = %+v, want typed with its reason", got)
	}
	if full := stand.ptys.last().read(); strings.Contains(full, "\r") {
		t.Fatalf("the confirm key reached a pane whose screen never showed the selection: %q", full)
	}
}

// The option already selected is confirmed at once — no movement, no wait.
//
// Its participant carries no Task and was never spawned, so it is the
// ordinary NOT-OWED case (nocx-f545a.7): a confirmed answer types nothing
// beyond the confirm key, and the result carries no task field at all —
// never a Task that quietly says "nothing was owed".
func TestAnAnswerNamingTheSelectedOptionConfirmsAtOnce(t *testing.T) {
	stand, sid, answerer := answerStand(t)
	got, err := answerer.Answer(context.Background(),
		workers.Participant{ID: "p-answer", Liveness: workers.Liveness{SessionID: string(sid)}}, "No, exit")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("answer = %+v, want submitted", got)
	}
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return stand.ptys.last().read() == "\r"
	})
	if got.Task != nil {
		t.Fatalf("task = %+v, want no task field for a participant that was never owed one", got.Task)
	}
}

// ── the debt a spawn's question leaves, and what pays it (nocx-f545a.7) ───
//
// Every test below starts from spawnStuckOnQuestion (worker_spawn_task_test.go):
// a real Spawn whose pane shows the real folder-trust question, so its task
// is left owed exactly as nocx-f545a.3 describes. What differs is what
// happens to the pane AFTER the coordinator's answer confirms.

// Criterion: an owed task is typed, and typed EXACTLY ONCE, the moment the
// answer that confirmed the question is also followed by the pane reaching
// free_text — and the debt is gone afterwards.
func TestAnOwedTaskIsTypedOnceTheAnswerConfirmsAndThePaneReachesFreeText(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "read AGENTS.md and report what it says about workers"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-typed", task)
	answerer := stand.answerer(t)

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		a, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
		done <- outcome{a, err}
	}()

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the movement key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\x1b[B")
	})
	stand.grid.Feed(string(sid), []byte(selectYesRepaint))
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return strings.HasSuffix(pty.read(), "\x1b[B\r")
	})
	if strings.Contains(pty.read(), task) {
		t.Fatalf("the task reached the pane before it was ever shown ready for it: %q", pty.read())
	}

	// The pane repaints as Claude's own idle prompt — the same corpus every
	// other free_text test in this package drives to.
	stand.repaintAsIdle(t, sid)

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("answer = %+v, want submitted", got.a)
	}
	if got.a.Task == nil || got.a.Task.Delivery != "typed" {
		t.Fatalf("task = %+v, want delivery typed", got.a.Task)
	}
	// Waited for the SUBMIT key too, not only the paste text: the paste and
	// the submit are two separate writes through the session's own queue,
	// and a wait that stopped at Contains(task) could observe the paste
	// alone, mid-write, exactly as often as it observed both.
	var typed string
	waittest.WaitFor(t, "the task text and its submit key to reach the pane", func() bool {
		typed = pty.read()
		return strings.Contains(typed, task) && strings.HasSuffix(typed, "\r")
	})
	if n := strings.Count(typed, task); n != 1 {
		t.Fatalf("the task reached the pane %d times, want exactly 1: %q", n, typed)
	}
	if stand.owed.take(sid) {
		t.Fatal("the task is still marked owed after it was typed")
	}
}

// Criterion: when the confirmed answer is followed by ANOTHER question
// rather than free_text, nothing is typed, the debt is still owed, and the
// result says so — and answering that second question is what pays it.
func TestAnOwedTaskStaysOwedWhenTheAnsweredPaneAsksAnotherQuestion(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "please write to NOCX_AGENT_REPORT when you are done"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-waiting", task)
	answerer := stand.answerer(t)

	// "No, exit" is already selected by the capture at this timestamp
	// (TestAnAnswerNamingTheSelectedOptionConfirmsAtOnce), so this confirms
	// at once — no movement to wait out. Nothing is fed to the pane
	// afterwards, so its next reading is the SAME question: a pane nocx
	// never told anything new is exactly what "asks another question"
	// covers, and the point of this test is what nocx does with that
	// reading, not which question it names.
	got, err := answerer.Answer(context.Background(), participant, "No, exit")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("answer = %+v, want submitted", got)
	}
	if got.Task == nil || got.Task.Delivery != "waiting" || got.Task.State == "" {
		t.Fatalf("task = %+v, want delivery waiting with a state", got.Task)
	}
	if strings.Contains(stand.ptys.last().read(), task) {
		t.Fatalf("the task reached a pane that was asking another question: %q", stand.ptys.last().read())
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after an answer that could not pay it")
	}
	stand.owed.mark(sid) // restore what the assertion above just consumed

	// Answering the second question, now with the pane reaching free_text
	// AFTER it confirms, is what pays the very same debt. The confirm and
	// the repaint are ordered exactly as the first test in this file orders
	// them: nothing repaints the pane until the confirm that needs to see
	// the menu still on screen has already reached it.
	pty := stand.ptys.last()
	before := len(pty.read())
	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		a, err := answerer.Answer(context.Background(), participant, "No, exit")
		done <- outcome{a, err}
	}()
	waittest.WaitFor(t, "the second confirm key to reach the pane", func() bool {
		return strings.Contains(pty.read()[before:], "\r")
	})
	stand.repaintAsIdle(t, sid)

	got2ch := <-done
	if got2ch.err != nil {
		t.Fatalf("second Answer: %v", got2ch.err)
	}
	got2 := got2ch.a
	if got2.Task == nil || got2.Task.Delivery != "typed" {
		t.Fatalf("second task = %+v, want delivery typed", got2.Task)
	}
	waittest.WaitFor(t, "the task text to reach the pane", func() bool {
		return strings.Contains(pty.read()[before:], task)
	})
}

// Criterion: an answer the gate refuses outright — the option is not on the
// screen — pays no debt. Nothing is typed and the result carries no task
// field, because the answer itself was never confirmed.
func TestAnOwedTaskStaysOwedWhenTheAnswerIsRefused(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "an owed task that must not be typed on a refused answer"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-refused", task)
	answerer := stand.answerer(t)

	before := len(stand.ptys.last().read())
	got, err := answerer.Answer(context.Background(), participant, "an option that is not on the screen")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != string(agenttyping.OutcomeRefused) {
		t.Fatalf("answer = %+v, want refused", got)
	}
	if got.Task != nil {
		t.Fatalf("task = %+v, want no task field for an answer that was never confirmed", got.Task)
	}
	if after := stand.ptys.last().read(); after[before:] != "" {
		t.Fatalf("a refused answer wrote to the pane: %q", after[before:])
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after an answer the gate refused")
	}
}

// Criterion: an answer whose move never shows on screen confirms nothing —
// the SAME refusal TestAnAnswerWhoseMoveNeverShowsConfirmsNothing asserts —
// and an owed participant's task is no exception: nothing is typed and the
// debt stays owed, because the menu was never confirmed.
func TestAnOwedTaskStaysOwedWhenTheAnswersMoveNeverShows(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "an owed task that must not be typed when the move never shows"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-move-stuck", task)
	answerer := stand.answerer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != string(agenttyping.OutcomeTyped) || got.Reason == "" {
		t.Fatalf("answer = %+v, want typed with its reason", got)
	}
	if got.Task != nil {
		t.Fatalf("task = %+v, want no task field for an answer that was never confirmed", got.Task)
	}
	if full := stand.ptys.last().read(); strings.Contains(full, task) {
		t.Fatalf("the task reached a pane whose selection was never confirmed: %q", full)
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after an answer that never confirmed")
	}
}

// Criterion: two Answer calls racing one owed participant whose pane reaches
// free_text type its task AT MOST ONCE — owed.take's check-and-clear is what
// makes this true, and -race is what makes the assertion mean something.
func TestTwoConcurrentAnswersOnAnOwedParticipantTypeItsTaskOnce(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "read AGENTS.md and report what it says about workers"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-race", task)
	answerer := stand.answerer(t)

	// "No, exit" is already selected, so both calls confirm at once with no
	// movement to race on — what is being raced is which of the two takes
	// the debt, not which of the two moves the selection.
	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			a, err := answerer.Answer(context.Background(), participant, "No, exit")
			results <- outcome{a, err}
		}()
	}
	close(start)

	// Both confirms are sent before the pane repaints, so the race is over
	// which of the two TAKES the debt, never over which one still saw a
	// menu to confirm.
	pty := stand.ptys.last()
	waittest.WaitFor(t, "both confirm keys to reach the pane", func() bool {
		return strings.Count(pty.read(), "\r") == 2
	})

	// The pane must reach free_text for whichever call takes the debt to
	// have anything to pay it with; fed once, from outside the race.
	stand.repaintAsIdle(t, sid)

	var withTask, withoutTask int
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("Answer: %v", got.err)
		}
		if got.a.Task != nil {
			withTask++
			if got.a.Task.Delivery != "typed" {
				t.Fatalf("task = %+v, want delivery typed", got.a.Task)
			}
		} else {
			withoutTask++
		}
	}
	if withTask != 1 || withoutTask != 1 {
		t.Fatalf("task field present on %d of 2 concurrent answers, want exactly 1", withTask)
	}
	var typed string
	waittest.WaitFor(t, "the task text to reach the pane", func() bool {
		typed = stand.ptys.last().read()
		return strings.Contains(typed, task)
	})
	if n := strings.Count(typed, task); n != 1 {
		t.Fatalf("the task reached the pane %d times, want exactly 1: %q", n, typed)
	}
}

// refusingSubmitTypist confirms a menu through the real gate (Choose is
// forwarded) but refuses every Submit on its own — a fixed double for the
// one branch a real screen cannot easily be made to exercise: the gate
// reads a pane fine and still turns the submission away.
type refusingSubmitTypist struct {
	choose paneChooser
}

func (r refusingSubmitTypist) Choose(ctx context.Context, paneID, option string) agenttyping.Result {
	return r.choose.Choose(context.Background(), paneID, option)
}

func (refusingSubmitTypist) Submit(_ context.Context, _, _ string) agenttyping.Result {
	return agenttyping.Result{
		Outcome: agenttyping.OutcomeRefused, State: agentdriver.StateFreeText,
		Reason: "test: submit refused at the gate",
	}
}

// Criterion: a pane that reaches free_text but whose Submit the gate itself
// refuses pays no debt — "refused" with the gate's own reason, and the task
// stays owed for a later answer to try again.
func TestAnOwedTasksSubmitIsRefusedAtTheGate(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "an owed task the gate will refuse to submit"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-gate-refused", task)
	real := stand.realTypist(t)
	answerer := &workerAnswerer{
		screens: stand.grid.Store, typist: refusingSubmitTypist{choose: real},
		owed: stand.owed, classify: stand.watch, typing: refusingSubmitTypist{choose: real}, log: stand.log,
		now: settledClock(),
	}

	// The confirm needs the menu still on screen, so the pane repaints to
	// free_text only AFTER it — the same ordering test 1 in this file uses.
	pty := stand.ptys.last()
	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		a, err := answerer.Answer(context.Background(), participant, "No, exit")
		done <- outcome{a, err}
	}()
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\r")
	})
	stand.repaintAsIdle(t, sid)

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Task == nil || got.a.Task.Delivery != "refused" || got.a.Task.Reason == "" {
		t.Fatalf("task = %+v, want delivery refused with its reason", got.a.Task)
	}
	if strings.Contains(pty.read(), task) {
		t.Fatalf("the task reached the pane despite the gate refusing its submit: %q", pty.read())
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after the gate refused to submit it")
	}
}

// Criterion: a pane whose confirmed menu genuinely leaves the screen but
// then never reaches free_text — it settles on "working" and stays there —
// answers "waiting" rather than failing the call, inside the (shortened)
// sub-budget, with the LIVE state Classify reads. The task stays owed, and
// Answer itself returns no error, exactly as workers.answer's own Deadline
// requires (see answerTaskBudget's doc in workers.go). Both waits inside
// typeOwedTask are exercised here off the real grid: the menu really does
// leave (a fresh idle-then-working repaint), so it is the SECOND wait — the
// pane never reaching free_text — that times out.
func TestAnOwedTaskWaitsWhenThePaneNeverBecomesTypableWithinTheSubBudget(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "an owed task the pane never becomes ready for"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-never-typable", task)

	original := answerTaskBudget
	answerTaskBudget = 200 * time.Millisecond
	t.Cleanup(func() { answerTaskBudget = original })

	answerer := stand.answerer(t)

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		a, err := answerer.Answer(context.Background(), participant, "No, exit")
		done <- outcome{a, err}
	}()

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\r")
	})
	// The confirmed menu leaves the screen — a real repaint, off the real
	// corpus — but the pane settles on "working" and goes no further: a
	// stub pty, unlike a real agent, never reaches free_text on its own.
	stand.grid.Withdraw(string(sid))
	if err := stand.grid.Watch(string(sid), participantCols, participantRows); err != nil {
		t.Fatalf("re-enrol the worker's pane: %v", err)
	}
	stand.feedCapture(t, sid, "claude-working", 17000)

	got := <-done
	elapsed := time.Since(start)
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Answer took %s; the shortened sub-budget should have ended the wait quickly", elapsed)
	}
	if got.a.Task == nil || got.a.Task.Delivery != "waiting" || got.a.Task.State != string(agentdriver.StateWorking) {
		t.Fatalf("task = %+v, want delivery waiting with state %q", got.a.Task, agentdriver.StateWorking)
	}
	if strings.Contains(pty.read(), task) {
		t.Fatalf("the task reached a pane that never became typable: %q", pty.read())
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after the sub-budget expired")
	}
}

// ── the regression: the watcher's cache is stale and the answer path must
// never read it (nocx-f545a.7, a review of 1ffd3a56) ──────────────────────
//
// Red on 1ffd3a56: that commit's workerAnswerer read paneReadiness.Snapshot —
// the watcher's CACHE — with only a one-tick "don't trust the very first
// reading" workaround. Neither test below ever calls Touch or Sweep after
// the confirm, so on 1ffd3a56 the cache the answerer reads stays
// permission_choice for the whole test: TestOwedTaskIsTypedWithoutASweepAfterTheConfirm
// failed with
//
//	task = &{Delivery:waiting State:permission_choice Reason:}, want delivery typed
//
// (quoted from a run against 1ffd3a56 before this fix, kept here as the
// record the review asked for). Green here because workerAnswerer no longer
// reads the cache at all for this decision: it waits for the confirmed menu
// to leave the GRID (awaitMenuLeftScreen) and then classifies the CURRENT
// frame (paneobserve.Watcher.Classify) — both live, neither touched by a
// Sweep this test never runs.

// Criterion: an owed participant's answer confirms, nothing ever Touches or
// Sweeps the watcher afterward — its cached Snapshot for this pane is
// asserted to stay permission_choice for the whole test — and the pane is
// then fed straight to free_text. The task is typed exactly once and the
// result says "typed".
func TestOwedTaskIsTypedWithoutASweepAfterTheConfirm(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "read AGENTS.md and report what it says about workers"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-no-sweep-typed", task)
	answerer := stand.answerer(t)

	assertCacheStillStale := func() {
		t.Helper()
		o, ok := stand.watch.Snapshot(string(sid))
		if !ok || o.State != agentdriver.StatePermissionChoice {
			t.Fatalf("watcher cache = %+v (ok=%v), want it still stale at permission_choice — "+
				"something Touched or Swept it, which this regression must not need", o, ok)
		}
	}
	assertCacheStillStale()

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		// "No, exit" is already selected at this timestamp, so this
		// confirms at once — no movement, and so no chance for a movement's
		// own frame read to have coincidentally kept the cache "fresh".
		a, err := answerer.Answer(context.Background(), participant, "No, exit")
		done <- outcome{a, err}
	}()

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\r")
	})
	assertCacheStillStale()

	// The pane is reset and fed straight to free_text — Withdraw, Enrol,
	// Feed only. No Touch, no Sweep: exactly what a review of 1ffd3a56
	// asked this test to prove is enough.
	stand.grid.Withdraw(string(sid))
	if err := stand.grid.Watch(string(sid), participantCols, participantRows); err != nil {
		t.Fatalf("re-enrol the worker's pane: %v", err)
	}
	stand.feedCapture(t, sid, "claude-idle", 11000)
	assertCacheStillStale()

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Task == nil || got.a.Task.Delivery != "typed" {
		t.Fatalf("task = %+v, want delivery typed", got.a.Task)
	}
	var typed string
	waittest.WaitFor(t, "the task text and its submit key to reach the pane", func() bool {
		typed = pty.read()
		return strings.Contains(typed, task) && strings.HasSuffix(typed, "\r")
	})
	if n := strings.Count(typed, task); n != 1 {
		t.Fatalf("the task reached the pane %d times, want exactly 1: %q", n, typed)
	}
	if !strings.Contains(typed, "\x1b[200~"+task+"\x1b[201~") {
		t.Fatalf("the task did not arrive as one bracketed paste: %q", typed)
	}

	// The cache is STILL stale, even now: nothing this test did ever swept
	// it, and the answer above did not need it to.
	assertCacheStillStale()
	if stand.owed.take(sid) {
		t.Fatal("the task is still marked owed after it was typed")
	}
}

// Criterion, paired with the one above: an owed participant's answer
// confirms, nothing Touches or Sweeps the watcher afterward, and the pane is
// then fed a DIFFERENT question. The result is "waiting" with that state,
// the task stays owed, and no task bytes reach the pane — the watcher's
// cache is asserted stale throughout, the identical regression from the
// other side.
func TestOwedTaskWaitsOnADifferentQuestionWithoutASweepAfterTheConfirm(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "read AGENTS.md and report what it says about workers"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-no-sweep-waiting", task)
	answerer := stand.answerer(t)

	if o, ok := stand.watch.Snapshot(string(sid)); !ok || o.State != agentdriver.StatePermissionChoice {
		t.Fatalf("watcher cache = %+v (ok=%v), want permission_choice before the answer", o, ok)
	}

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		a, err := answerer.Answer(context.Background(), participant, "No, exit")
		done <- outcome{a, err}
	}()

	pty := stand.ptys.last()
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\r")
	})

	// A DIFFERENT question — a menu the agent did not raise itself, off the
	// real corpus — reset onto the pane with no Touch and no Sweep.
	stand.grid.Withdraw(string(sid))
	if err := stand.grid.Watch(string(sid), participantCols, participantRows); err != nil {
		t.Fatalf("re-enrol the worker's pane: %v", err)
	}
	stand.feedCapture(t, sid, "claude-modal", 20000)

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Task == nil || got.a.Task.Delivery != "waiting" || got.a.Task.State != string(agentdriver.StateModalChoice) {
		t.Fatalf("task = %+v, want delivery waiting with state %q", got.a.Task, agentdriver.StateModalChoice)
	}
	if strings.Contains(pty.read(), task) {
		t.Fatalf("the task reached a pane that was asking a different question: %q", pty.read())
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after an answer that could not pay it")
	}

	// The watcher's own cache never moved off what spawnStuckOnQuestion left
	// it at — nothing in this test Touched or Swept it — which is the whole
	// point: the modal_choice this answer reports came from a LIVE read.
	if o, ok := stand.watch.Snapshot(string(sid)); !ok || o.State != agentdriver.StatePermissionChoice {
		t.Fatalf("watcher cache = %+v (ok=%v), want it still stale at permission_choice", o, ok)
	}
}

// ── the settle wait before an answer's FIRST key (nocx-f545a.8) ───────────
//
// menuSettle's own doc (workers.go) has the measurement: a real Claude Code's
// menu can desync from what it repaints for up to ~100ms after it first
// appears, invisibly, so Answer now waits for the menu to stand still before
// it ever writes a key. Every test below drives that wait through an
// INJECTED CLOCK — never a real sleep — per menuSettle's own doc: shrinking
// the interval globally would hide the gate from every other test in this
// file, which is why every other test in this file instead gets
// settledClock() (below), through stand.answerer's own doc.

// fakeClock is a manually-driven time source for exercising
// workerAnswerer.awaitMenuSettled's own timing without ever sleeping: it
// reports whatever value it was last set to, and only test code moves it
// forward.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{t: start}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// settledClock is the clock every OTHER answer test in this package gets
// (through stand.answerer, worker_spawn_task_test.go): each call advances by
// menuSettle plus a little, so the SECOND poll of any one awaitMenuSettled
// call already reports its menu settled, regardless of how many Answer calls
// have already shared this same clock — every call restarts its own local
// `since` baseline (awaitMenuSettled's own doc) and only needs the clock to
// have moved forward by menuSettle between that call's first poll and its
// second, which an ever-advancing clock guarantees no matter where it
// started. Safe for concurrent Answer calls sharing one answerer: the mutex
// serializes advances, and nobody reads two values expecting them to agree.
func settledClock() func() time.Time {
	var mu sync.Mutex
	t := time.Now()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(menuSettle + time.Millisecond)
		return t
	}
}

// pollCounter counts awaitMenuSettled's own polls, for tests that need to
// know at least one MORE poll has happened since a point they name — never
// for tests to count exactly how many, which would make them depend on the
// real deliveryPoll cadence rather than on the fake clock they actually
// drive.
type pollCounter struct {
	mu sync.Mutex
	n  int
}

func (p *pollCounter) hook() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
}

func (p *pollCounter) load() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// waitForNextPoll blocks until pc has counted a poll after before — always a
// value pc.load() itself returned earlier in the same test, taken AFTER
// whatever mutation (a clock advance, a changed frame) the caller wants the
// next poll to observe.
func waitForNextPoll(t *testing.T, pc *pollCounter, before int) {
	t.Helper()
	waittest.WaitFor(t, "an awaitMenuSettled poll", func() bool {
		return pc.load() > before
	})
}

// fakeMenuFrame builds a paneview.Frame agenttyping.ReadMenu reads back as a
// menu offering exactly options, selected on options[selected] — synthetic,
// for testing awaitMenuSettled's own timing directly rather than through a
// real terminal capture, which has no scripted way to change a menu's option
// list mid-render on demand.
func fakeMenuFrame(options []string, selected int) paneview.Frame {
	lines := make([][]paneview.Cell, len(options))
	for i, opt := range options {
		marker := "  "
		if i == selected {
			marker = "❯ " // "❯ ", the same marker selectYesRepaint moves in this file's other tests
		}
		text := marker + opt
		cells := make([]paneview.Cell, 0, len(text))
		for _, r := range text {
			cells = append(cells, paneview.Cell{Text: string(r), Width: 1})
		}
		lines[i] = cells
	}
	return paneview.Frame{Cols: 80, Rows: len(options), CursorX: 0, CursorY: selected, Lines: lines}
}

// fakeSettleGrid is a minimal panegrid.Observer whose Frame is swappable
// live, so a test can change what a pane shows mid-wait without a real
// terminal behind it. Only Frame is exercised by awaitMenuSettled; the rest
// exist to satisfy the interface.
type fakeSettleGrid struct {
	mu    sync.Mutex
	frame paneview.Frame
}

func (g *fakeSettleGrid) set(f paneview.Frame) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.frame = f
}

func (g *fakeSettleGrid) Frame(string) (paneview.Frame, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.frame, nil
}
func (g *fakeSettleGrid) Enrol(string, int, int) error  { return nil }
func (g *fakeSettleGrid) Withdraw(string)               {}
func (g *fakeSettleGrid) Feed(string, []byte)           {}
func (g *fakeSettleGrid) Resize(string, int, int) error { return nil }
func (g *fakeSettleGrid) Enrolled(string) bool          { return true }

// fakeSettleClassifier is a minimal paneClassifier whose state is swappable
// live, the classify-side twin of fakeSettleGrid above.
type fakeSettleClassifier struct {
	mu    sync.Mutex
	state agentdriver.State
}

func (c *fakeSettleClassifier) set(s agentdriver.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = s
}

func (c *fakeSettleClassifier) Classify(string) (paneobserve.Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return paneobserve.Observation{State: c.state}, true
}

// Criterion: a menu first seen by an answer writes NOTHING while its own
// clock has not yet advanced past menuSettle — polls happen (proven via
// pollCounter) and the pane stays untouched — and once the clock is advanced
// past it, the ordinary move-then-confirm sequence runs exactly as every
// other test in this file already proves it does.
func TestAnAnswerWaitsForTheMenuToSettleBeforeItsFirstMove(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the worker's session: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	stand.watch.Sweep()

	typist := stand.realTypist(t)
	clock := newFakeClock(time.Now())
	var pc pollCounter
	answerer := &workerAnswerer{
		screens: stand.grid.Store, typist: typist,
		owed: stand.owed, classify: stand.watch, typing: typist, log: stand.log,
		now: clock.now, pollHook: pc.hook,
	}
	participant := workers.Participant{ID: "p-settle", Liveness: workers.Liveness{SessionID: string(sid)}}

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		a, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
		done <- outcome{a, err}
	}()

	before := pc.load()
	waitForNextPoll(t, &pc, before) // the first observation, baseline set
	before = pc.load()
	waitForNextPoll(t, &pc, before) // a second poll, the clock unmoved

	pty := stand.ptys.last()
	if b := pty.read(); b != "" {
		t.Fatalf("bytes reached the pane before the menu settled: %q", b)
	}
	select {
	case <-done:
		t.Fatal("Answer returned before the menu ever settled")
	default:
	}

	before = pc.load()
	clock.advance(menuSettle)
	waitForNextPoll(t, &pc, before)

	waittest.WaitFor(t, "the movement key to reach the pane", func() bool {
		return strings.Contains(pty.read(), "\x1b[B")
	})
	if strings.Contains(pty.read(), "\r") {
		t.Fatalf("the confirm key was sent before the screen showed the selection: %q", pty.read())
	}
	stand.grid.Feed(string(sid), []byte(selectYesRepaint))

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("answer = %+v, want submitted", got.a)
	}
	waittest.WaitFor(t, "the confirm key to reach the pane", func() bool {
		return pty.read() == "\x1b[B\r"
	})
}

// Criterion: a menu whose OPTIONS change mid-settle — a row appears that was
// not there before, even though the classification and the named option are
// both still present — restarts the clock. What would have completed the
// ORIGINAL interval is not enough for the restarted one; a full interval
// counted from the change is what settles it. Exercises awaitMenuSettled
// directly, off fakes: no real terminal can be scripted to change a menu's
// option list on a signal from the test rather than on a timer of its own.
func TestAwaitMenuSettledRestartsTheClockWhenTheMenusOptionsChange(t *testing.T) {
	const option = "Yes, I trust this folder"
	grid := &fakeSettleGrid{}
	grid.set(fakeMenuFrame([]string{"No, exit", option}, 0))
	classify := &fakeSettleClassifier{}
	classify.set(agentdriver.StatePermissionChoice)

	clock := newFakeClock(time.Now())
	var pc pollCounter
	answerer := &workerAnswerer{
		screens: grid, classify: classify,
		now: clock.now, pollHook: pc.hook,
	}

	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		done <- answerer.awaitMenuSettled(ctx, "pane-1", option)
	}()

	before := pc.load()
	waitForNextPoll(t, &pc, before) // the first observation sets the baseline

	half := menuSettle / 2
	before = pc.load()
	clock.advance(half)
	waitForNextPoll(t, &pc, before)
	select {
	case <-done:
		t.Fatal("awaitMenuSettled returned before its own interval elapsed")
	default:
	}

	// The OPTIONS change: a third row appears. The classification (still
	// permission_choice) and the named option (still offered) are both
	// unchanged, and the clock must restart anyway.
	grid.set(fakeMenuFrame([]string{"No, exit", option, "Yes, and don't ask again"}, 1))
	before = pc.load()
	waitForNextPoll(t, &pc, before) // the poll that notices the changed options

	before = pc.load()
	clock.advance(menuSettle - half)
	waitForNextPoll(t, &pc, before)
	select {
	case <-done:
		t.Fatal("the changed menu's clock was not restarted: it settled on time accrued against the old menu")
	default:
	}

	before = pc.load()
	clock.advance(menuSettle)
	waitForNextPoll(t, &pc, before)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitMenuSettled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitMenuSettled never returned after the restarted interval elapsed")
	}
}

// Criterion: a menu that disappears mid-settle — the pane repaints to
// free_text before the interval could ever elapse — ends the wait at once
// with nothing written, and Choose is left to refuse the confirm on its own
// screen read and its own reason, never a second one invented by the wait.
func TestAnAnswerWritesNothingWhenTheMenuDisappearsDuringTheSettle(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the worker's session: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	stand.watch.Sweep()

	typist := stand.realTypist(t)
	var pc pollCounter
	answerer := &workerAnswerer{
		screens: stand.grid.Store, typist: typist,
		owed: stand.owed, classify: stand.watch, typing: typist, log: stand.log,
		// now is left nil (real time.Now) deliberately: this wait must end
		// because the pane stopped offering the menu, never because the
		// clock reached menuSettle, so the real interval is never actually
		// waited out by this test.
		pollHook: pc.hook,
	}
	participant := workers.Participant{ID: "p-settle-vanish", Liveness: workers.Liveness{SessionID: string(sid)}}

	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		a, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
		done <- outcome{a, err}
	}()

	before := pc.load()
	waitForNextPoll(t, &pc, before)

	// The trust question is gone before any settle could complete.
	stand.repaintAsIdle(t, sid)

	got := <-done
	if got.err != nil {
		t.Fatalf("Answer: %v", got.err)
	}
	if got.a.Outcome != string(agenttyping.OutcomeRefused) {
		t.Fatalf("answer = %+v, want Choose's own refusal for a pane that stopped offering the menu", got.a)
	}
	if b := stand.ptys.last().read(); b != "" {
		t.Fatalf("bytes reached the pane whose menu disappeared during the settle: %q", b)
	}
}

// Criterion: a ctx cancelled mid-settle ends the wait promptly, with an
// error and with nothing written — the settle wait obeys the same
// cancellation contract awaitFreeText, awaitSelectionOn and
// awaitMenuLeftScreen already do.
func TestAnAnswerReturnsPromptlyWhenCtxIsCancelledDuringTheSettle(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the worker's session: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	stand.watch.Sweep()

	typist := stand.realTypist(t)
	// The clock never advances: nothing but ctx cancellation can end this
	// wait, because settling never happens on its own.
	clock := newFakeClock(time.Now())
	var pc pollCounter
	answerer := &workerAnswerer{
		screens: stand.grid.Store, typist: typist,
		owed: stand.owed, classify: stand.watch, typing: typist, log: stand.log,
		now: clock.now, pollHook: pc.hook,
	}
	participant := workers.Participant{ID: "p-settle-cancel", Liveness: workers.Liveness{SessionID: string(sid)}}

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		a   workers.PaneAnswer
		err error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		a, err := answerer.Answer(ctx, participant, "Yes, I trust this folder")
		done <- outcome{a, err}
	}()

	before := pc.load()
	waitForNextPoll(t, &pc, before)
	cancel()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("Answer = %+v, <nil>, want an error for a settle wait ended by ctx cancellation", got.a)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Answer took %s to return after ctx was cancelled during the settle", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Answer did not return promptly after ctx was cancelled during the settle")
	}
	if b := stand.ptys.last().read(); b != "" {
		t.Fatalf("bytes reached the pane despite ctx being cancelled during the settle: %q", b)
	}
}
