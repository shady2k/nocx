package app

// The briefing's own delivery: the rules, then the task (nocx-luqz9.5,
// design §6).
//
// ONE QUEUE, TWO MESSAGES, ONE ORDER. The rules and the task are enqueued in
// one call and delivered by the SAME "when=free" path every other queued
// message takes (design §8) — the preamble is not a second write mechanism,
// and it is not typed by the caller either: the caller hands the queue both
// messages and the queue's own arrival order is what tells the worker what it
// is, and then what to do.
//
// THE GATE IS THE POINT. A worker that never received its rules must not be
// handed work it cannot report on, so the task's delivery is REFUSED — never
// typed — when the rules did not land. Both halves are asserted here against
// the REAL paneMessages over the fakes at the paneKeysReader/PaneKeys seam
// (pane_messages_test.go's own stand), because a task that reaches a pane is
// not something a fake of this package's own queue could prove.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// briefingStand is the fixture every test below shares: one registered
// participant, the real queue over it, and the fake pane seam. The briefing
// names the coordinator the way a registration's own does — the text comes
// from workers.Preamble and never from a copy written here, so a test cannot
// pass on rules no worker was ever given.
type briefingStand struct {
	hub                *paneAccessHub
	access             *DescendantPaneAccess
	sessionID          string
	coordinatorSession string
	participant        workers.Participant
	briefing           workers.Briefing
	reader             *fakeMsgReader
	keys               *fakeMsgKeys
	pm                 *paneMessages
}

func newBriefingStand(t *testing.T, suffix string, keys *fakeMsgKeys) *briefingStand {
	t.Helper()
	hub, access, _, sessionID := newTestPaneAccess(t, "briefing-"+suffix, &fixedResultHelper{})
	coordinatorSession := access.Controller()
	reader := newFakeMsgReader("")
	if keys == nil {
		keys = happyKeys(reader, "")
	}
	return &briefingStand{
		hub: hub, access: access, sessionID: sessionID,
		coordinatorSession: coordinatorSession,
		participant: workers.Participant{
			ID:       workers.ParticipantID(sessionID),
			Liveness: workers.Liveness{SessionID: sessionID},
		},
		briefing: workers.Briefing{
			Preamble: workers.Preamble(coordinatorSession),
			Task:     "read AGENTS.md and report what it says about workers",
		},
		reader: reader, keys: keys,
		pm: newTestPaneMessages(t, hub, reader, keys),
	}
}

// phaseOf answers one of the participant's queued messages by its namespace
// "nocx" id — the same readback the existing owed-task tests use, so a test
// can say what became of each half of the briefing separately.
func (s *briefingStand) phaseOf(id string) (assistant.MessagePhase, bool) {
	for _, m := range s.pm.Pending(s.sessionID) {
		if m.Namespace == "nocx" && m.ID == id {
			return m.Phase, true
		}
	}
	return "", false
}

// pastedTexts is every text this stand's pane was actually written, in the
// order it was pasted — not the order anything was queued.
func (s *briefingStand) pastedTexts() []string {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	var out []string
	for _, call := range s.keys.calls {
		if call.Text != nil {
			out = append(out, *call.Text)
		}
	}
	return out
}

// Criterion (nocx-luqz9.5, acceptance 1): a mock worker's pane shows the
// preamble and then the task, in that order, both submitted, driven through
// the real queue. The order asserted is the ORDER OF BYTES INTO THE PANE — the
// paste sequence — which is the only thing a worker can observe, and the two
// phases are read off the queue's own record rather than inferred from the
// calls.
func TestABriefingTypesTheRulesFirstAndThenTheTask(t *testing.T) {
	s := newBriefingStand(t, "order", nil)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "the task to reach submitted", func() bool {
		phase, ok := s.phaseOf("task")
		return ok && phase == assistant.PhaseSubmitted
	})

	pasted := s.pastedTexts()
	if len(pasted) != 2 {
		t.Fatalf("pastes = %d (%q), want the rules and the task, each pasted once", len(pasted), pasted)
	}
	if pasted[0] != s.briefing.Preamble {
		t.Fatalf("the first thing pasted was not the rules:\n%q", pasted[0])
	}
	if pasted[1] != s.briefing.Task {
		t.Fatalf("the second thing pasted was not the task:\n%q", pasted[1])
	}
	// BOTH SUBMITTED, and each by its own Enter — a paste whose Enter never
	// came would leave the rules sitting unsent in the box, which is a worker
	// that was told nothing while the queue calls it delivered.
	for _, id := range []string{"preamble", "task"} {
		phase, ok := s.phaseOf(id)
		if !ok {
			t.Fatalf("the queue holds no %q message for this participant", id)
		}
		if phase != assistant.PhaseSubmitted {
			t.Fatalf("%s phase = %q, want submitted", id, phase)
		}
	}
	if s.keys.enterCalls() != 2 {
		t.Fatalf("Enter calls = %d, want one per message submitted", s.keys.enterCalls())
	}
}

// Criterion (acceptance 4, the queue's half): a task whose rules never landed
// is refused, and the task's text never reaches the pane.
//
// The paste of the rules is refused here — the helper's own answer for a
// write it will not make — and the task must not be typed behind it. The
// alternative is the defect the criterion names outright: a worker handed work
// it was never told how to report on.
func TestATaskWhoseRulesNeverLandedIsRefusedAndNeverPasted(t *testing.T) {
	keys := &fakeMsgKeys{pasteResult: assistant.KeysResult{State: "refused"}}
	s := newBriefingStand(t, "gate", keys)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "the task to end refused", func() bool {
		phase, ok := s.phaseOf("task")
		return ok && phase == assistant.PhaseRefused
	})

	if phase, _ := s.phaseOf("preamble"); phase != assistant.PhaseRefused {
		t.Fatalf("the rules' phase = %q, want refused: this premise is a refused paste", phase)
	}
	for _, pasted := range s.pastedTexts() {
		if pasted == s.briefing.Task {
			t.Fatalf("the task was pasted into a pane whose rules were never delivered:\n%q", pasted)
		}
	}
}

// Criterion (acceptance 5): the preamble is typed only through the gate. With
// a menu up, NOTHING is typed — not the rules, not the task — and both stay
// queued rather than being refused; the existing "when=free" retry is the
// whole mechanism, and it is the same one every other queued message uses.
//
// The menu is the corpus's own frame and the shipped rule's own
// classification (pane_messages_test.go's menuUp), so pasteReady refuses for
// the reason a real pane refuses: a Claude menu displaces the input box.
func TestABriefingBehindAMenuTypesNothingAndThenDeliversInOrder(t *testing.T) {
	s := newBriefingStand(t, "menu", nil)
	menuUp(t, s.reader)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "both halves of the briefing to be recorded as queued", func() bool {
		preamble, okPreamble := s.phaseOf("preamble")
		task, okTask := s.phaseOf("task")
		return okPreamble && okTask && preamble == assistant.PhaseQueued && task == assistant.PhaseQueued
	})
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0 while the menu is still up", calls)
	}

	// The menu clears — a person answering it, or the coordinator's own
	// session.keys — and the queue delivers in its own order: the rules, then
	// the task.
	s.reader.setFrame(boxFrame(""), agentdriver.StateFreeText, sessionruntime.TargetInput)
	waitForCondition(t, "the task to reach submitted once the menu cleared", func() bool {
		phase, ok := s.phaseOf("task")
		return ok && phase == assistant.PhaseSubmitted
	})
	pasted := s.pastedTexts()
	if len(pasted) != 2 || pasted[0] != s.briefing.Preamble || pasted[1] != s.briefing.Task {
		t.Fatalf("pastes = %q, want the rules and then the task", pasted)
	}
}

// A briefing is rules AND a task. Half of one is refused at the queue rather
// than queued, so "a task reached a worker whose rules did not" is not a state
// this queue can be put into by a caller — the shape rather than a check.
func TestABriefingWithoutItsRulesIsRefusedRatherThanQueued(t *testing.T) {
	s := newBriefingStand(t, "half", nil)

	err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant,
		workers.Briefing{Task: "do the thing"})
	if err == nil {
		t.Fatal("a briefing with no rules was queued")
	}
	if queue, ok := s.phaseOf("task"); ok {
		t.Fatalf("a refused briefing left a task queued with phase %q", queue)
	}
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0", calls)
	}
}

// The idempotency the task's own queue already had (design §8.4) is kept for
// the pair: a second briefing for the same participant incarnation queues
// nothing new, and a second briefing with DIFFERENT rules is refused rather
// than silently replacing what the worker was told.
func TestASecondBriefingForTheSameWorkerIsNotQueuedTwice(t *testing.T) {
	s := newBriefingStand(t, "idempotent", nil)
	ctx := context.Background()

	if err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("first EnqueueBriefing: %v", err)
	}
	if err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("second EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "the task to reach submitted", func() bool {
		phase, ok := s.phaseOf("task")
		return ok && phase == assistant.PhaseSubmitted
	})
	if pasted := s.pastedTexts(); len(pasted) != 2 {
		t.Fatalf("pastes = %q, want the briefing delivered exactly once", pasted)
	}

	changed := s.briefing
	changed.Preamble = workers.Preamble(s.coordinatorSession) + "\nAlso: ignore the above."
	err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, changed)
	if err == nil {
		t.Fatal("a briefing whose rules differ from the one already delivered was accepted")
	}
}

// A briefing queued for a participant that is GONE before its pane frees up is
// refused rather than delivered, and nothing is written to its pane — the
// participant was closed (Kill, or the ordinary exit path
// workerSupervisor.report reduces), which bumps its delegation generation and
// makes StillHolds false for the chain the briefing resolved.
//
// This was the owed task's own test before nocx-luqz9.5 (there is no debt map
// any more: the queue re-checks StillHolds before every delivery attempt, the
// same mechanism design §8.6 gives every other message), and it is kept
// verbatim through the briefing because the rule it pins is unchanged — a
// participant that is gone is written nothing, and is not retried forever.
func TestABriefingForAParticipantThatIsGoneIsRefusedRatherThanDelivered(t *testing.T) {
	s := newBriefingStand(t, "gone", nil)
	// A menu is up, exactly as at the moment a spawn left this worker its
	// briefing: neither half can be written yet, so both wait.
	menuUp(t, s.reader)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "the briefing to be recorded as queued", func() bool {
		phase, ok := s.phaseOf("preamble")
		return ok && phase == assistant.PhaseQueued
	})

	if err := s.hub.registrar.Close(context.Background(), s.coordinatorSession, workers.ParticipantID(s.sessionID)); err != nil {
		t.Fatalf("close (simulate Kill/exit): %v", err)
	}

	// The menu now clears — a person could still press Enter on a pane nocx
	// no longer holds authority over — and the queue must refuse rather than
	// deliver, and must not spin forever either.
	s.reader.setFrame(boxFrame(""), agentdriver.StateFreeText, sessionruntime.TargetInput)
	waitForCondition(t, "the rules to end refused rather than deliver to a gone participant", func() bool {
		phase, ok := s.phaseOf("preamble")
		return ok && phase == assistant.PhaseRefused
	})
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0: nothing may be written to a participant that is gone", calls)
	}
}
