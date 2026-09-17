package app

// EnqueueTask's own acceptance tests (design §9, Task 11): the owed task is
// re-homed as namespace "nocx"'s one message, at the same fake
// PaneKeys/PaneReader seam pane_messages_test.go (Task 10) already uses —
// never a real helper runtime or PTY, for the identical reason that file's
// own header states.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// TestASpawnThatMeetsAQuestionDeliversTheTaskOnceTheQuestionIsAnswered is
// Task 11's own acceptance criterion: a mock agent shows a trust menu at
// spawn (modelled here as pasteReady's own precondition failing — the box is
// not currently identifiable, exactly what nocx-6q1uh.10 measured a Claude
// menu does to its input box), the coordinator answers with session.keys
// option, and the task is submitted exactly once. A second answer path — the
// person pressing Enter instead — is modelled as a second, redundant
// EnqueueTask call for the identical spawn (the only way this mechanism is
// ever invoked twice for one participant): idempotency on the SAME
// MessageKey (namespace "nocx", id "task") is what makes that answer also
// result in exactly one submission rather than a second delivery attempt.
func TestASpawnThatMeetsAQuestionDeliversTheTaskOnceTheQuestionIsAnswered(t *testing.T) {
	hub, access, _, sessionID := newTestPaneAccess(t, "owed-task", &fixedResultHelper{})
	coordinatorSession := access.Controller()
	participant := workers.Participant{
		ID:       workers.ParticipantID(sessionID),
		Liveness: workers.Liveness{SessionID: sessionID},
	}
	const task = "read AGENTS.md and report what it says about workers"

	reader := newFakeMsgReader("")
	// A menu is up at spawn: no target kind currently mints (nocx-6q1uh.10 —
	// a Claude menu always displaces the input box, so "not identifiable" IS
	// "a menu is up").
	reader.setAvailable("")
	keys := happyKeys(reader, task)
	pm := newTestPaneMessages(t, hub, reader, keys)

	if err := pm.EnqueueTask(context.Background(), coordinatorSession, participant, task); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	// Nothing is written while the question is up, and the message stays
	// queued — the queue's own retry (deliverOne's pasteReady loop), never a
	// caller waiting on nocx's report.
	waitForCondition(t, "the owed task to be recorded as queued", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.Namespace == "nocx" && m.ID == "task" {
				return m.Phase == assistant.PhaseQueued
			}
		}
		return false
	})
	if keys.callCount() != 0 {
		t.Fatalf("keys calls = %d, want 0 while the menu is still up", keys.callCount())
	}

	// "The coordinator answers with session.keys option": the menu clears.
	// Modelled at the level pasteReady/deliverOne actually observe it —
	// deliverOne cannot tell a session.keys answer from a person pressing
	// Enter themselves, which is exactly the point of routing both through
	// one queue.
	reader.setAvailable(sessionruntime.TargetInput)

	waitForCondition(t, "the owed task to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.Namespace == "nocx" && m.ID == "task" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})
	if keys.callCount() != 2 {
		t.Fatalf("keys calls = %d, want exactly 2 (one paste, one Enter) for one submission", keys.callCount())
	}
	if calls := keys.calls; len(calls) != 2 || calls[0].Text == nil || *calls[0].Text != task || calls[1].Key == nil {
		t.Fatalf("calls = %+v, want a paste of the task followed by Enter", calls)
	}

	// The second answer path: a person pressing Enter, after the message has
	// already been submitted, cannot re-trigger delivery through this same
	// mechanism — the only surface that could ask again is another
	// EnqueueTask for the identical spawn, and design §8.4's idempotency
	// ("same key, same hash -> the recorded state") is what refuses to
	// deliver it twice.
	if err := pm.EnqueueTask(context.Background(), coordinatorSession, participant, task); err != nil {
		t.Fatalf("second EnqueueTask: %v", err)
	}
	if keys.callCount() != 2 {
		t.Fatalf("keys calls after a second EnqueueTask = %d, want still 2: the task must be submitted exactly once", keys.callCount())
	}
}

// TestEnqueueTaskRefusesRatherThanDeliveringToAParticipantThatIsGone
// re-expresses the two owed-task debt tests Task 11 deleted
// (TestKillDropsAnOwedTask, TestSupervisorReportDropsAnOwedTask,
// worker_spawn_question_test.go): there is no debt map to clear any more,
// because the queue itself re-checks StillHolds(chain) before every
// delivery attempt (pane_messages.go's deliverOne) — the same mechanism
// design §8.6 already gives every other caller's message. A participant
// closed (Kill, or the ordinary exit path workerSupervisor.report reduces)
// while its owed task is still waiting on a menu must not have that task
// delivered once the menu clears, and must not be retried forever either.
func TestEnqueueTaskRefusesRatherThanDeliveringToAParticipantThatIsGone(t *testing.T) {
	hub, access, _, sessionID := newTestPaneAccess(t, "owed-task-gone", &fixedResultHelper{})
	coordinatorSession := access.Controller()
	participant := workers.Participant{
		ID:       workers.ParticipantID(sessionID),
		Liveness: workers.Liveness{SessionID: sessionID},
	}
	const task = "read AGENTS.md and report"

	reader := newFakeMsgReader("")
	reader.setAvailable("") // a menu is up, exactly as at the moment a spawn owed this task
	keys := happyKeys(reader, task)
	pm := newTestPaneMessages(t, hub, reader, keys)

	if err := pm.EnqueueTask(context.Background(), coordinatorSession, participant, task); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	waitForCondition(t, "the owed task to be recorded as queued", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.Namespace == "nocx" && m.ID == "task" {
				return m.Phase == assistant.PhaseQueued
			}
		}
		return false
	})

	// The participant is gone — Kill's own path (spawnedParticipant.Kill)
	// and workerSupervisor.report both end here, at Registrar.Close, which
	// is what bumps this participant's delegation generation and makes
	// StillHolds false for the chain EnqueueTask resolved.
	if err := hub.registrar.Close(context.Background(), coordinatorSession, workers.ParticipantID(sessionID)); err != nil {
		t.Fatalf("close (simulate Kill/exit): %v", err)
	}

	// The menu now clears — a person could still press Enter on a pane
	// nocx no longer holds authority over — and the queue must refuse
	// rather than deliver, and must not spin forever either.
	reader.setAvailable(sessionruntime.TargetInput)

	waitForCondition(t, "the owed task to end refused rather than deliver to a gone participant", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.Namespace == "nocx" && m.ID == "task" {
				return m.Phase == assistant.PhaseRefused
			}
		}
		return false
	})
	if keys.callCount() != 0 {
		t.Fatalf("keys calls = %d, want 0: nothing may be written to a participant that is gone", keys.callCount())
	}
}
