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
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
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
	var typed string
	waittest.WaitFor(t, "the task text to reach the pane", func() bool {
		typed = pty.read()
		return strings.Contains(typed, task)
	})
	if n := strings.Count(typed, task); n != 1 {
		t.Fatalf("the task reached the pane %d times, want exactly 1: %q", n, typed)
	}
	if !strings.HasSuffix(typed, "\r") {
		t.Fatalf("the task was typed but never submitted, so no turn started: %q", typed)
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

func (r refusingSubmitTypist) Choose(paneID, option string) agenttyping.Result {
	return r.choose.Choose(paneID, option)
}

func (refusingSubmitTypist) Submit(string, string) agenttyping.Result {
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
		grid: stand.grid, typist: refusingSubmitTypist{choose: real},
		owed: stand.owed, readiness: stand.watch, typing: refusingSubmitTypist{choose: real}, log: stand.log,
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

// Criterion: a pane that never becomes typable inside the (shortened)
// sub-budget answers "waiting" rather than failing the call — the task stays
// owed, and Answer itself returns no error, exactly as workers.answer's own
// Deadline requires (see answerTaskBudget's doc in workers.go).
func TestAnOwedTaskWaitsWhenThePaneNeverBecomesTypableWithinTheSubBudget(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "an owed task the pane never becomes ready for"
	sid, participant, _ := spawnStuckOnQuestion(t, stand, "p-owed-never-typable", task)

	original := answerTaskBudget
	answerTaskBudget = 150 * time.Millisecond
	t.Cleanup(func() { answerTaskBudget = original })

	real := stand.realTypist(t)
	answerer := &workerAnswerer{
		grid: stand.grid, typist: real,
		owed: stand.owed, readiness: fixedStateReadiness{state: agentdriver.StateWorking},
		typing: real, log: stand.log,
	}

	start := time.Now()
	got, err := answerer.Answer(context.Background(), participant, "No, exit")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Answer took %s; the shortened sub-budget should have ended the wait quickly", elapsed)
	}
	if got.Task == nil || got.Task.Delivery != "waiting" || got.Task.State != string(agentdriver.StateWorking) {
		t.Fatalf("task = %+v, want delivery waiting with state %q", got.Task, agentdriver.StateWorking)
	}
	if strings.Contains(stand.ptys.last().read(), task) {
		t.Fatalf("the task reached a pane that never became typable: %q", stand.ptys.last().read())
	}
	if !stand.owed.take(sid) {
		t.Fatal("the task is no longer owed after the sub-budget expired")
	}
}
