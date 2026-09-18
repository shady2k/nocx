package app

// The briefing's own delivery: the rules and the task, as ONE message
// (nocx-xn63t.4.16; the text is nocx-luqz9.5 and design §6).
//
// ONE MESSAGE, ONE PASTE, ONE ENTER. The briefing is enqueued in one call and
// delivered by the SAME "when=free" path every other queued message takes
// (design §8) — it is not a second write mechanism, and it is not typed by the
// caller either: the caller hands the queue one text and the queue's own
// delivery is what puts it in the pane.
//
// WHAT WAS A GATE IS NOW THE TEXT'S OWN ORDER. The rules and the task were two
// messages once, submitted as two turns, and the queue REFUSED a task whose
// rules had not landed — a worker that read the rules alone reported finding no
// task, which is the defect this bead was filed from (the owner's live run,
// 2026-09-18). With one message there is no second outcome left to wait on: the
// rules are written FIRST inside the very text the worker is given. What is
// asserted here is that shape — exactly one submission, rules before task — and
// the phase the queue records for it.
//
// Every test below runs against the REAL paneMessages over the fakes at the
// paneKeysReader/PaneKeys seam (pane_messages_test.go's own stand), because a
// text that reaches a pane is not something a fake of this package's own queue
// could prove.

import (
	"context"
	"strings"
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

// nocxPending is every namespace "nocx" message the queue holds for this
// participant, in queue order — the readback a coordinator reads it by. A
// briefing is ONE of them, and the COUNT is asserted rather than assumed: a
// regression that enqueued two messages would otherwise be read as whichever
// one a lookup happened to find first.
func (s *briefingStand) nocxPending() []assistant.MessageView {
	var out []assistant.MessageView
	for _, m := range s.pm.Pending(s.sessionID) {
		if m.Namespace == "nocx" {
			out = append(out, m)
		}
	}
	return out
}

// phase answers the briefing's own record — the one namespace "nocx" message,
// which this helper insists on there being exactly one of, so a phase can never
// be read off a half of something that should not be in halves.
func (s *briefingStand) phase() (assistant.MessagePhase, bool) {
	pending := s.nocxPending()
	if len(pending) != 1 {
		return "", false
	}
	return pending[0].Phase, true
}

// exactlyOneBriefing asserts the queue holds this participant's briefing and
// nothing else in namespace "nocx". It is called straight after an enqueue,
// BEFORE anything is delivered, so a regression that queued two messages fails
// here with the shape named — rather than as a wait on phase(), which refuses
// to read a half of something that should not be in halves and would time out
// instead.
func (s *briefingStand) exactlyOneBriefing(t *testing.T) assistant.MessageView {
	t.Helper()
	pending := s.nocxPending()
	if len(pending) != 1 || pending[0].ID != briefingID {
		t.Fatalf("the queue holds %+v, want exactly one namespace \"nocx\" message with id %q", pending, briefingID)
	}
	return pending[0]
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

// Criterion (acceptance 1): a spawned mock worker's pane receives exactly ONE
// submission, carrying the rules and then the task, driven through the real
// queue. The order asserted is the order inside that one message — the rules
// open it, the task follows them — which is the only ordering a worker can
// observe, and the phase is read off the queue's own record rather than
// inferred from the calls.
func TestABriefingReachesTheWorkerAsOneSubmissionWithTheRulesFirst(t *testing.T) {
	s := newBriefingStand(t, "one", nil)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	// THE SHAPE, BEFORE ANYTHING IS DELIVERED: one enqueue puts ONE message in
	// namespace "nocx" on the queue. A regression to the two-message briefing
	// this bead removed fails here, naming what is queued.
	s.exactlyOneBriefing(t)
	waitForCondition(t, "the briefing to be submitted", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseSubmitted
	})

	pending := s.nocxPending()
	if len(pending) != 1 || pending[0].ID != briefingID {
		t.Fatalf("the queue holds %+v, want exactly one namespace \"nocx\" message with id %q", pending, briefingID)
	}
	pasted := s.pastedTexts()
	if len(pasted) != 1 {
		t.Fatalf("pastes = %d (%q), want exactly one message carrying the rules and the task", len(pasted), pasted)
	}
	one := pasted[0]
	if one != s.briefing.Text() {
		t.Fatalf("the pasted message is not the briefing's own text:\n%q\nwant:\n%q", one, s.briefing.Text())
	}
	if !strings.HasPrefix(one, s.briefing.Preamble) {
		t.Fatalf("the pasted message does not open with the rules:\n%q", one)
	}
	// The task is looked for in what FOLLOWS the rules, not anywhere in the
	// message: a task that appeared before them would be a briefing whose two
	// halves came out in the wrong order, which is the whole of what this
	// criterion is about.
	if rest := one[len(s.briefing.Preamble):]; !strings.Contains(rest, s.briefing.Task) {
		t.Fatalf("the task does not follow the rules in the pasted message:\n%q", one)
	}
	// ONE ENTER: the submission is the whole briefing's — a second Enter would
	// mean a second message, which is the shape this bead removed.
	if enters := s.keys.enterCalls(); enters != 1 {
		t.Fatalf("Enter calls = %d, want exactly one for one message", enters)
	}
}

// Criterion (acceptance 2, the queue's half): a briefing that cannot be
// delivered types nothing. The paste is refused here — the helper's own answer
// for a write it will not make — the delivery ends there, and nothing is
// submitted into the worker.
//
// The other half of that criterion — that the SPAWN says so — is the
// registration's own: internal/workers/registrar_briefing_test.go's
// TestARegistrationWhoseBriefingCouldNotBeQueuedSaysSoAndQueuesNoTask asserts
// the delivery it hands back carries BriefingQueued false.
func TestABriefingThatCannotBeDeliveredTypesNothing(t *testing.T) {
	keys := &fakeMsgKeys{pasteResult: assistant.KeysResult{State: "refused"}}
	s := newBriefingStand(t, "refused", keys)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	s.exactlyOneBriefing(t)
	waitForCondition(t, "the briefing to end refused", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseRefused
	})

	if enters := s.keys.enterCalls(); enters != 0 {
		t.Fatalf("Enter calls = %d, want none: a briefing that was not pasted must not be submitted", enters)
	}
	if calls := s.keys.callCount(); calls != 1 {
		t.Fatalf("keys calls = %d, want the one refused paste attempt and nothing behind it", calls)
	}
}

// Criterion (acceptance 3): with a menu up, NOTHING is typed and the briefing
// stays queued rather than being refused or written into the menu; the existing
// "when=free" retry is the whole mechanism, and it is the same one every other
// queued message uses.
//
// The menu is the corpus's own frame and the shipped rule's own classification
// (pane_messages_test.go's menuUp), so pasteReady refuses for the reason a real
// pane refuses: a Claude menu displaces the input box.
func TestABriefingBehindAMenuTypesNothingAndThenDeliversOnce(t *testing.T) {
	s := newBriefingStand(t, "menu", nil)
	menuUp(t, s.reader)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	s.exactlyOneBriefing(t)
	waitForCondition(t, "the briefing to be recorded as queued", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseQueued
	})
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0 while the menu is still up", calls)
	}

	// The menu clears — a person answering it, or the coordinator's own
	// session.keys — and the queue delivers the briefing: one text, one Enter.
	s.reader.setFrame(boxFrame(""), agentdriver.StateFreeText, sessionruntime.TargetInput)
	waitForCondition(t, "the briefing to reach submitted once the menu cleared", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseSubmitted
	})
	pasted := s.pastedTexts()
	if len(pasted) != 1 || pasted[0] != s.briefing.Text() {
		t.Fatalf("pastes = %q, want the briefing's one message", pasted)
	}
	if enters := s.keys.enterCalls(); enters != 1 {
		t.Fatalf("Enter calls = %d, want exactly one", enters)
	}
}

// A briefing is rules AND a task. Half of one is refused at the QUEUE — the
// caller never reaches the delivery — so "a worker was handed work without ever
// being told how to report on it" is not a state a caller can put this queue
// into. With one message the refusal is the value's own
// (workers.Briefing.Validate), because there is no longer a later moment at
// which a missing rules half could be noticed.
func TestABriefingWithoutItsRulesIsRefusedRatherThanQueued(t *testing.T) {
	s := newBriefingStand(t, "half", nil)

	err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant,
		workers.Briefing{Task: "do the thing"})
	if err == nil {
		t.Fatal("a briefing with no rules was queued")
	}
	if pending := s.nocxPending(); len(pending) != 0 {
		t.Fatalf("a refused briefing left %+v queued", pending)
	}
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0", calls)
	}
}

// The idempotency the task's own queue already had (design §8.4) is kept for
// the briefing: a second briefing for the same participant incarnation queues
// nothing new, and a second briefing whose TEXT differs is refused rather than
// silently replacing what the worker was told.
func TestASecondBriefingForTheSameWorkerIsNotQueuedTwice(t *testing.T) {
	s := newBriefingStand(t, "idempotent", nil)
	ctx := context.Background()

	if err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("first EnqueueBriefing: %v", err)
	}
	s.exactlyOneBriefing(t)
	if err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("second EnqueueBriefing: %v", err)
	}
	waitForCondition(t, "the briefing to reach submitted", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseSubmitted
	})
	if pasted := s.pastedTexts(); len(pasted) != 1 {
		t.Fatalf("pastes = %q, want the briefing delivered exactly once", pasted)
	}
	if pending := s.nocxPending(); len(pending) != 1 {
		t.Fatalf("the queue holds %+v, want one briefing", pending)
	}

	changed := s.briefing
	changed.Task = s.briefing.Task + ", and report how many you found"
	err := s.pm.EnqueueBriefing(ctx, s.coordinatorSession, s.participant, changed)
	if err == nil {
		t.Fatal("a briefing whose text differs from the one already delivered was accepted")
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
	// briefing: nothing can be written yet, so it waits.
	menuUp(t, s.reader)

	if err := s.pm.EnqueueBriefing(context.Background(), s.coordinatorSession, s.participant, s.briefing); err != nil {
		t.Fatalf("EnqueueBriefing: %v", err)
	}
	s.exactlyOneBriefing(t)
	waitForCondition(t, "the briefing to be recorded as queued", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseQueued
	})

	if err := s.hub.registrar.Close(context.Background(), s.coordinatorSession, workers.ParticipantID(s.sessionID)); err != nil {
		t.Fatalf("close (simulate Kill/exit): %v", err)
	}

	// The menu now clears — a person could still press Enter on a pane nocx
	// no longer holds authority over — and the queue must refuse rather than
	// deliver, and must not spin forever either.
	s.reader.setFrame(boxFrame(""), agentdriver.StateFreeText, sessionruntime.TargetInput)
	waitForCondition(t, "the briefing to end refused rather than deliver to a gone participant", func() bool {
		phase, ok := s.phase()
		return ok && phase == assistant.PhaseRefused
	})
	if calls := s.keys.callCount(); calls != 0 {
		t.Fatalf("keys calls = %d, want 0: nothing may be written to a participant that is gone", calls)
	}
}
