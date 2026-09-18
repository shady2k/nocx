package app

// A pane that asks a question is not a pane that failed (nocx-f545a.3).
//
// Measured 2026-09-10: a worker's agent opened its folder-trust question,
// nocx waited out the whole enrolment budget for an input box that was never
// coming, deleted the tab and told the coordinator to try again. ADR-0064
// makes a positively identified question an ANSWER to the wait: the worker
// goes live with its tab and session standing, nothing is typed into it, and
// the registration is told what became of the task. These tests assert that
// through the real observation seam, off the real capture of that question,
// and assert the other half too — a pane nocx cannot read still fails, and
// now says in a value that it could not read it.
//
// workerSpawner itself no longer types anything (design §9, Task 11, and
// design §6 for the rules that now travel with the task; nocx-luqz9.5): the
// owed-task debt these tests used to exercise directly (mark/take/restore/
// drop) is gone, replaced by the "when=free" session.messages the message
// queue delivers once the pane is free — internal/workers/
// registrar_briefing_test.go covers "Register enqueues the briefing", and
// internal/app/pane_messages_briefing_test.go covers the queue's own
// delivery order and gate. What stays true here, and is what these tests
// assert, is that a spawn meeting a question is not a failure: the
// participant goes live with its tab and session standing, and nothing
// reaches the pane while the question is up.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

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

// Criterion: both kinds of question end the wait, and neither is typed into
// — workerSpawner itself no longer types anything at all (design §9, Task
// 11), so this asserts the pty stays untouched rather than that a typist was
// never called.
func TestAQuestionOfEitherKindEndsTheWaitWithoutTypingAnything(t *testing.T) {
	for _, state := range []agentdriver.State{agentdriver.StatePermissionChoice, agentdriver.StateModalChoice} {
		t.Run(string(state), func(t *testing.T) {
			stand := newTaskDeliveryStand(t)
			stand.spawner.readiness = fixedStateReadiness{state: state}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const task = "do the thing"
			sp, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
				Participant: workers.ParticipantID("p-" + string(state)), Group: "worker-1",
				Task: task, Command: "claude",
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
			if full := stand.ptys.last().read(); strings.Contains(full, task) {
				t.Fatalf("the task reached a pane that was asking a question: %q", full)
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

// ── the debt is gone (design §9, Task 11) ──────────────────────────────────
//
// TestKillDropsAnOwedTask and TestSupervisorReportDropsAnOwedTask used to
// assert that Kill and workerSupervisor.report each cleared the in-memory
// owed-task debt for a participant that is gone — the closing events that
// debt map's own doc named. There is no such debt to clear any more: the
// task is a "when=free" session.message in internal/app.paneMessages' own
// queue, and that queue already re-checks StillHolds(chain) on every
// delivery attempt (pane_messages.go's deliverOne), so a participant whose
// delegation the store no longer resolves — because Kill closed its session,
// or its process exited — ends the delivery as PhaseRefused on its own, the
// same way any other caller's "when=free" message does when its target goes
// away. That termination path is internal/app/pane_messages_test.go's own
// coverage (TestRevocationAfterPasteLeavesPartialAndTakesNoFurtherStep and
// the StillHolds checks beside it), and
// pane_messages_briefing_test.go's own
// TestABriefingForAParticipantThatIsGoneIsRefusedRatherThanDelivered states
// it for the briefing, not this package's.
