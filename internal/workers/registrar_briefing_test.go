package workers

// The briefing a registration hands its worker (nocx-luqz9.5; design §6; one
// message since nocx-xn63t.4.16).
//
// ONE CALL, ONE MESSAGE, AND THE ORDER IS THE TEXT'S. A registration with a
// task hands the composition root's queue a Briefing — the rules and the task,
// as one text — in one call rather than two, because two calls would make the
// order an accident of which goroutine reached the queue first, and a worker
// that gets a task before its rules has no way to report on it.
//
// What this package can assert stops at the seam: the queue's own delivery and
// its refusal paths are internal/app.paneMessages' suite
// (internal/app/pane_messages_briefing_test.go), exactly as the task's delivery
// mechanics already were before this bead.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTaskQueue records every EnqueueBriefing call. The name is kept from the
// task-delivery era this seam grew out of, and what it records is one BRIEFING
// rather than one task: the two travel together, so a fake that could record
// half of one would let a test pass on a briefing nobody could deliver.
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
	briefing           Briefing
}

func (f *fakeTaskQueue) EnqueueBriefing(_ context.Context, coordinatorSession string, participant Participant, b Briefing) error {
	f.calls = append(f.calls, fakeTaskQueueCall{coordinatorSession: coordinatorSession, participant: participant, briefing: b})
	if f.liveCheck != nil {
		f.sawLive = append(f.sawLive, f.liveCheck(participant.ID))
	}
	return f.err
}

func TestRegisterEnqueuesTheBriefingExactlyOnceAfterTheParticipantIsLive(t *testing.T) {
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
		t.Fatalf("EnqueueBriefing calls = %d, want exactly 1: %+v", len(queue.calls), queue.calls)
	}
	call := queue.calls[0]
	if call.coordinatorSession != coordSession {
		t.Fatalf("coordinator session = %q, want %q", call.coordinatorSession, coordSession)
	}
	if call.participant.ID != reg.Participant.ID {
		t.Fatalf("participant = %q, want the registered %q", call.participant.ID, reg.Participant.ID)
	}
	if call.briefing.Task != task {
		t.Fatalf("briefing task = %q, want %q", call.briefing.Task, task)
	}
	// The rules ARE the text this package owns, naming THIS coordinator: a
	// registration that handed the queue a preamble of its own would be a
	// second answer to what a worker is told.
	if call.briefing.Preamble != Preamble(coordSession) {
		t.Fatalf("briefing preamble is not the preamble for this coordinator:\n%s", call.briefing.Preamble)
	}
	if !strings.Contains(call.briefing.Preamble, coordSession) {
		t.Fatalf("the briefing does not name the coordinator a worker must report to:\n%s", call.briefing.Preamble)
	}
	// AND THE ONE MESSAGE THE QUEUE IS HANDED CARRIES BOTH, rules first: the
	// registration hands over a Briefing and never a joined string of its own,
	// so there is exactly one place that decides how the two are spelled
	// together (Briefing.Text, and its own test beside this file).
	text := call.briefing.Text()
	rest, found := strings.CutPrefix(text, call.briefing.Preamble)
	if !found {
		t.Fatalf("the briefing's message does not open with the rules it carries:\n%s", text)
	}
	if !strings.Contains(rest, task) {
		t.Fatalf("the briefing's message does not carry the task after the rules:\n%s", text)
	}
	if len(queue.sawLive) != 1 || !queue.sawLive[0] {
		t.Fatalf("EnqueueBriefing ran before the participant was marked live (sawLive=%v)", queue.sawLive)
	}
	if !reg.Delivery.BriefingQueued {
		t.Fatalf("delivery = %+v, want the registration to say its briefing was queued", reg.Delivery)
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
		t.Fatalf("EnqueueBriefing calls = %d, want 0 for a registration with no task", len(queue.calls))
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

// Criterion (nocx-luqz9.5, acceptance 4): A WORKER THAT CANNOT BE TOLD ITS
// RULES IS REPORTED, AND NOTHING IS QUEUED.
//
// The rules and the task are ONE fact: a queue that could not take the briefing
// took neither half of it — they are one message and one call — so a
// registration that only logged this would leave a coordinator believing its
// worker was working while the worker had been told nothing at all. The
// registration therefore SAYS SO, in the delivery it hands back, which is the
// same place the task's own non-delivery is already answered.
//
// THE PARTICIPANT IS STILL LIVE, and that is deliberate rather than an
// oversight: the delivery fact is nocx's own failure to queue, not a screen
// reading (ADR-0064 §4's asymmetry is about the pane, and it stands), so what
// changes is what the coordinator is TOLD — the worker stands, unbriefed, and
// is closed by the coordinator that now knows.
func TestARegistrationWhoseBriefingCouldNotBeQueuedSaysSoAndQueuesNoTask(t *testing.T) {
	h := newHarness(t)
	queue := &fakeTaskQueue{err: errors.New("queue: no runtime holds this pane")}
	h.reg.SetTaskQueue(queue)

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
	if reg.Delivery.BriefingQueued {
		t.Fatalf("delivery = %+v, want the registration to say its briefing never reached the queue", reg.Delivery)
	}
	if reg.Delivery.Typed {
		t.Fatalf("delivery = %+v, want no task typed when nothing was queued", reg.Delivery)
	}
	// ONE ATTEMPT, AND IT CARRIED THE WHOLE BRIEFING. A registration that
	// retried the task alone after the briefing refused — or that handed the
	// queue the task without the rules — would be the defect this criterion
	// names outright: the worker gets its work and never learns how to report.
	if len(queue.calls) != 1 {
		t.Fatalf("EnqueueBriefing calls = %d, want exactly 1: %+v", len(queue.calls), queue.calls)
	}
	if queue.calls[0].briefing.Task == "" || strings.TrimSpace(queue.calls[0].briefing.Text()) == "" {
		t.Fatalf("the refused call carried nothing to type: %+v", queue.calls[0])
	}
}

// The briefing is not half a briefing (the queue refuses to take one without
// rules — internal/app's own test), so the shape cannot be built here either:
// a registration whose task is empty has nothing to brief and says so.
func TestABriefingWithoutRulesOrWithoutATaskIsNotABriefing(t *testing.T) {
	if err := (Briefing{Preamble: Preamble(coordSession), Task: "t"}).Validate(); err != nil {
		t.Fatalf("a complete briefing was refused: %v", err)
	}
	for _, b := range []Briefing{
		{Preamble: "", Task: "t"},
		{Preamble: Preamble(coordSession), Task: ""},
		{Preamble: "", Task: ""},
	} {
		if err := b.Validate(); err == nil {
			t.Fatalf("Briefing%+v validated, want a refusal", b)
		}
	}
}
