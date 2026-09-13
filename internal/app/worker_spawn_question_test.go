package app

// A pane that asks a question is not a pane that failed (nocx-f545a.3).
//
// Measured 2026-09-10: a worker's agent opened its folder-trust question,
// nocx waited out the whole enrolment budget for an input box that was never
// coming, deleted the tab and told the coordinator to try again. ADR-0064
// makes a positively identified question an ANSWER to the wait: the worker
// goes live with its tab and session standing, nothing is typed into it, and
// the registration is told what became of the task. These tests assert that
// through the real observation and typing seams, off the real capture of that
// question, and assert the other half too — a pane nocx cannot read still
// fails, and now says in a value that it could not read it.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// mustNotTypeTypist fails the test if anything is submitted. A question is a
// screen the real gate would refuse anyway; this makes "nocx never tried" the
// assertion rather than "nocx tried and was stopped".
type mustNotTypeTypist struct{ t *testing.T }

func (m mustNotTypeTypist) Submit(ctx context.Context, pane, text string) agenttyping.Result {
	m.t.Errorf("Submit(%q, %q) was called on a pane that is asking a question", pane, text)
	return agenttyping.Result{PaneID: pane, Outcome: agenttyping.OutcomeRefused, Reason: "test: must not type"}
}

// Criterion, off the real corpus: the folder-trust question ends the spawn's
// wait as an answer. The participant is live, keeps its tab and its session,
// nothing but the command line reaches its pane, and the registration learns
// the task is waiting on a permission choice.
func TestASpawnWhosePaneAsksAQuestionGoesLiveWithItsTaskUntyped(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	const task = "read AGENTS.md and report what it says about workers"

	type outcome struct {
		sp  workers.Spawned
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		sp, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
			Participant: "p-asks", Group: "worker-1", Task: task, Command: "claude",
		})
		done <- outcome{sp, err}
	}()

	sid := waitForNewSession(t, stand.reg)
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	waittest.WaitFor(t, "the worker's pane to be classified as asking a question", func() bool {
		stand.watch.Sweep()
		o, ok := stand.watch.Snapshot(string(sid))
		return ok && o.State == agentdriver.StatePermissionChoice
	})

	got := <-done
	if got.err != nil {
		t.Fatalf("Spawn refused a pane that was asking a question: %v", got.err)
	}
	deliverer, ok := got.sp.(workers.TaskDeliverer)
	if !ok {
		t.Fatalf("the spawned participant (%T) does not say what became of its task", got.sp)
	}
	if d := deliverer.TaskDelivery(); d.Typed || d.WaitingOn != string(agentdriver.StatePermissionChoice) {
		t.Fatalf("delivery = %+v, want the task untyped and waiting on %q", d, agentdriver.StatePermissionChoice)
	}

	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 0 {
		t.Fatalf("tabs created=%v deleted=%v, want the participant's tab standing", created, deleted)
	}
	if _, err := stand.reg.Get(sid); err != nil {
		t.Fatalf("the participant's session is gone after a question: %v", err)
	}
	if full := stand.ptys.last().read(); strings.Contains(full, task) {
		t.Fatalf("the task reached a pane that was asking a question: %q", full)
	}
}

// Criterion: both kinds of question end the wait, and neither is typed into.
// The typist here fails the test if it is called at all.
func TestAQuestionOfEitherKindEndsTheWaitWithoutTypingAnything(t *testing.T) {
	for _, state := range []agentdriver.State{agentdriver.StatePermissionChoice, agentdriver.StateModalChoice} {
		t.Run(string(state), func(t *testing.T) {
			stand := newTaskDeliveryStand(t)
			stand.spawner.readiness = fixedStateReadiness{state: state}
			stand.spawner.typist = mustNotTypeTypist{t: t}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sp, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
				Participant: workers.ParticipantID("p-" + string(state)), Group: "worker-1",
				Task: "do the thing", Command: "claude",
			})
			if err != nil {
				t.Fatalf("Spawn refused a pane asking %q: %v", state, err)
			}
			if ctx.Err() != nil {
				t.Fatal("the wait ran to its deadline instead of ending at the question")
			}
			deliverer, ok := sp.(workers.TaskDeliverer)
			if !ok {
				t.Fatalf("the spawned participant (%T) does not say what became of its task", sp)
			}
			d := deliverer.TaskDelivery()
			if d.Typed || d.WaitingOn != string(state) {
				t.Fatalf("delivery = %+v, want untyped and waiting on %q", d, state)
			}
			if _, deleted := stand.tabs.snapshot(); len(deleted) != 0 {
				t.Fatalf("a question deleted the participant's tab: %v", deleted)
			}
		})
	}
}

// Criterion: the other side is unchanged in behaviour and sharper in what it
// says. A pane nocx cannot read still fails the spawn and compensates, and
// the refusal now CARRIES the state, so a caller can tell "nocx could not
// read it" from "the agent was busy" without parsing a sentence.
func TestARefusedWaitCarriesTheStateThePaneHeld(t *testing.T) {
	cases := []struct {
		state      agentdriver.State
		unreadable bool
	}{
		{agentdriver.StateUnknown, true},
		{agentdriver.StateWorking, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			stand := newTaskDeliveryStand(t)
			stand.spawner.readiness = fixedStateReadiness{state: tc.state}

			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
				Participant: workers.ParticipantID("p-" + string(tc.state)), Group: "worker-1",
				Task: "do the thing", Command: "claude",
			})
			if err == nil {
				t.Fatalf("Spawn succeeded on a pane that held %q for the whole budget", tc.state)
			}
			var never *workers.PaneNeverTypable
			if !errors.As(err, &never) {
				t.Fatalf("error %v does not carry *workers.PaneNeverTypable", err)
			}
			if !errors.Is(err, workers.ErrPaneNeverTypable) {
				t.Fatalf("error %v stopped answering the sentinel every caller already asks", err)
			}
			if never.State != string(tc.state) || never.Unreadable() != tc.unreadable {
				t.Fatalf("refusal = %+v (unreadable=%v), want state %q unreadable=%v",
					never, never.Unreadable(), tc.state, tc.unreadable)
			}
			if created, deleted := stand.tabs.snapshot(); len(created) != 1 || len(deleted) != 1 {
				t.Fatalf("tabs created=%v deleted=%v, want the refused spawn compensated", created, deleted)
			}
		})
	}
}

// And a pane nocx never read at all is unreadable too — the one case with no
// state to carry.
func TestAPaneNeverObservedIsAnUnreadableRefusal(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	stand.spawner.readiness = neverObservedReadiness{}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-never-observed", Group: "worker-1", Task: "do the thing", Command: "claude",
	})
	var never *workers.PaneNeverTypable
	if !errors.As(err, &never) || never.State != "" || !never.Unreadable() {
		t.Fatalf("err = %v, want an unreadable refusal with no state", err)
	}
}

// ── the debt itself: marked at spawn, dropped at either closing event
// other than an answer paying it (nocx-f545a.7) ────────────────────────────

// Criterion: a spawn whose pane asks a question marks the participant's
// task owed, and Kill — the compensation for every failure after the fork,
// reached both by compensateSpawn here and by
// internal/workers.Registrar.compensate for a later failure — drops that
// debt, so a registration that never becomes a supervised participant does
// not leave one nothing will ever clear.
func TestKillDropsAnOwedTask(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sid, _, sp := spawnStuckOnQuestion(t, stand, "p-owed-kill-2", "do the thing")

	if !stand.owed.take(sid) {
		t.Fatal("the spawn did not mark its task owed")
	}
	stand.owed.mark(sid) // restore for Kill to find and drop

	if err := sp.Kill(context.Background()); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if stand.owed.take(sid) {
		t.Fatal("Kill did not drop the participant's owed task")
	}
}

// Criterion: the other closing event — the participant's session ending —
// drops the same debt, from workerSupervisor.report, whether or not
// anything downstream is wired to hear about the exit.
func TestSupervisorReportDropsAnOwedTask(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sid := session.ID("sid-owed-report")
	stand.owed.mark(sid)

	stand.sup.report(context.Background(), workers.Participant{
		ID: "p-owed-report", Liveness: workers.Liveness{SessionID: string(sid)},
	}, workers.Exit{Cause: "exited"})

	if stand.owed.take(sid) {
		t.Fatal("workerSupervisor.report did not drop the participant's owed task")
	}
}
