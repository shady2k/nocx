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
	typist, ok := stand.spawner.typist.(*agenttyping.Typist)
	if !ok {
		t.Fatalf("the stand's typist is %T, want the real gate", stand.spawner.typist)
	}
	return stand, sid, &workerAnswerer{grid: stand.grid, typist: typist}
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
}
