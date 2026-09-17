package workers

// Task 11: the task a registration was given is enqueued for delivery once
// registration succeeds — never from inside Spawner.Spawn, because
// TaskQueue.EnqueueTask (internal/app.paneMessages) resolves the participant
// from its own session id, and that mapping does not exist until MarkLive
// writes the participant's Liveness (see TaskQueue's own doc, store.go).
// These assert the ONE thing this package can assert about that seam: it is
// called exactly once, with the right coordinator session, participant and
// task, after the participant is live — and never before, and never at all
// when there is no task or no queue wired. The queue's own delivery mechanics
// (paste, echo, Enter, the "when=free" retry until the pane is free) are
// internal/app.paneMessages' own suite, not this package's.

import (
	"context"
	"errors"
	"testing"
)

// fakeTaskQueue records every EnqueueTask call. seenLive, when set, refuses
// unless the store already reports the participant live — the assertion
// that this is called AFTER MarkLive, not from within Spawn.
type fakeTaskQueue struct {
	calls []fakeTaskQueueCall
	err   error
	// liveCheck, when set, is asked at call time and its answer is recorded
	// alongside the call rather than failing the test from inside a
	// production call path.
	liveCheck func(id ParticipantID) bool
	sawLive   []bool
}

type fakeTaskQueueCall struct {
	coordinatorSession string
	participant        Participant
	task               string
}

func (f *fakeTaskQueue) EnqueueTask(_ context.Context, coordinatorSession string, participant Participant, task string) error {
	f.calls = append(f.calls, fakeTaskQueueCall{coordinatorSession: coordinatorSession, participant: participant, task: task})
	if f.liveCheck != nil {
		f.sawLive = append(f.sawLive, f.liveCheck(participant.ID))
	}
	return f.err
}

func TestRegisterEnqueuesTheTaskExactlyOnceAfterTheParticipantIsLive(t *testing.T) {
	h := newHarness(t)
	queue := &fakeTaskQueue{
		liveCheck: func(id ParticipantID) bool {
			p, ok := h.store.read(t, id)
			return ok && p.State == StateLive
		},
	}
	h.reg.SetTaskQueue(queue)

	const task = "read AGENTS.md and report what it says about workers"
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: task, Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("EnqueueTask calls = %d, want exactly 1: %+v", len(queue.calls), queue.calls)
	}
	call := queue.calls[0]
	if call.coordinatorSession != coordSession {
		t.Fatalf("coordinatorSession = %q, want %q", call.coordinatorSession, coordSession)
	}
	if call.participant.ID != reg.ID {
		t.Fatalf("participant.ID = %q, want the registered participant %q", call.participant.ID, reg.ID)
	}
	if call.participant.Liveness != reg.Liveness {
		t.Fatalf("participant.Liveness = %+v, want %+v", call.participant.Liveness, reg.Liveness)
	}
	if call.task != task {
		t.Fatalf("task = %q, want %q", call.task, task)
	}
	if len(queue.sawLive) != 1 || !queue.sawLive[0] {
		t.Fatalf("EnqueueTask ran before the participant was marked live (sawLive=%v)", queue.sawLive)
	}
}

func TestRegisterDoesNotEnqueueWhenThereIsNoTask(t *testing.T) {
	h := newHarness(t)
	queue := &fakeTaskQueue{}
	h.reg.SetTaskQueue(queue)

	if _, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Command: "claude",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(queue.calls) != 0 {
		t.Fatalf("EnqueueTask calls = %d, want 0 for a registration with no task", len(queue.calls))
	}
}

func TestRegisterWithNoTaskQueueWiredStillSucceeds(t *testing.T) {
	h := newHarness(t)
	// h.reg.queue is left nil — the same absence case every other optional
	// seam in this package already treats.
	if _, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "do the thing", Command: "claude",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
}

// A queue that refuses the task never un-registers an already-live
// participant: the same asymmetry TaskDelivery's own doc draws for a screen
// reading (ADR-0064 §4) applies here too — a fact about delivery may not
// retroactively decide whether the participant exists.
func TestRegisterSucceedsEvenWhenTheQueueRefusesTheTask(t *testing.T) {
	h := newHarness(t)
	h.reg.SetTaskQueue(&fakeTaskQueue{err: errors.New("queue: refused")})

	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "do the thing", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.State != StateLive {
		t.Fatalf("state = %q, want live: a queue refusal must not undo a successful registration", reg.State)
	}
}
