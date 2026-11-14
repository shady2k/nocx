package transport

// The approval service (nocx-a62cl.3.1, ADR-anntnxdj §3): an ask decision
// blocks the very tool call on a person's answer, the run rests in
// awaiting_approval while it does, and agent.approve wakes that call instead of
// starting a new drive.
//
// Three things are held here. The WAIT itself, with the clock under the test's
// control, because the declared bound is ten minutes and a test may not sleep
// it (AGENTS.md: wait on an observable state, never on a duration). The
// DECLARATION, because ADR-8eej3e66 makes the catalogue the one place a
// lifetime is written and the wait must read it rather than restate it. And the
// WAKE over the real socket, because "the answer reaches the same call" is the
// one claim a unit test cannot make: only the wire shows that the client that
// asked is the client that got the answer, once.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// ── the wait, with the clock under control ────────────────────────────────

// TestAnswerWait_AnAnswerAfterTenMinutesResumesTheCall is ADR-anntnxdj's
// quality scenario 2: a person takes ten minutes to answer, and the call does
// not break. The clock is the test's, so nothing sleeps.
func TestAnswerWait_AnAnswerAfterTenMinutesResumesTheCall(t *testing.T) {
	clock := notify.NewManualClock()
	w := newAnswerWait(clock, "session.run", answerLimit)
	w.begin()
	defer w.stop()

	clock.Advance(10 * time.Minute)
	if !w.settle(assistant.Approved{}) {
		t.Fatal("the answer was refused after ten minutes — the declared limit is at least ten, so ten is inside it")
	}
	if _, ok := w.result(t.Context()).(assistant.Approved); !ok {
		t.Fatalf("result = %T, want the person's yes", w.result(t.Context()))
	}
}

// TestAnswerWait_NoAnswerByTheDeclaredLimitIsARefusal: the limit is a refusal,
// never a hang and never a yes, and an answer that arrives after it decides
// nothing.
func TestAnswerWait_NoAnswerByTheDeclaredLimitIsARefusal(t *testing.T) {
	clock := notify.NewManualClock()
	w := newAnswerWait(clock, "session.run", answerLimit)
	w.begin()
	defer w.stop()

	clock.Advance(answerLimit)
	got := w.result(t.Context())
	declined, ok := got.(assistant.Declined)
	if !ok {
		t.Fatalf("result = %T, want a refusal when nobody answered by the limit", got)
	}
	if declined.Reason == "" {
		t.Fatal("the refusal carries no sentence — the model would read an empty answer")
	}
	if w.settle(assistant.Approved{}) {
		t.Fatal("an answer that arrived after the limit was accepted")
	}
}

// TestAnswerWait_ACancelledCallIsARefusal: the call's own context is the third
// way out — a cancelled call and a closed connection both end it here.
func TestAnswerWait_ACancelledCallIsARefusal(t *testing.T) {
	clock := notify.NewManualClock()
	w := newAnswerWait(clock, "session.run", answerLimit)
	w.begin()
	defer w.stop()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got := w.result(ctx)
	unanswered, ok := got.(assistant.Unanswered)
	if !ok {
		t.Fatalf("result = %T, want an unanswered refusal for a cancelled call", got)
	}
	if unanswered.Err == nil {
		t.Fatal("the refusal carries no cause — a cancellation must stay classifiable")
	}
}

// TestTheAnswerLimitIsTheDeclaredAnswerPhase is ADR-8eej3e66's own check for
// this wait: internal.agent.run declares an `answer` phase holding no execution
// capacity, a finite limit of at least ten minutes, and the service reads that
// declaration instead of restating it.
func TestTheAnswerLimitIsTheDeclaredAnswerPhase(t *testing.T) {
	decl := catalog.OpInternalAgentRun.Declaration()
	bound, ok := decl.Answer.Deadline()
	if !ok {
		t.Fatal("internal.agent.run declares no answer bound — the wait would be unbounded")
	}
	if bound.Duration() < 10*time.Minute {
		t.Fatalf("answer limit = %v, want at least ten minutes (ADR-anntnxdj §3)", bound.Duration())
	}
	if answerLimit != bound.Duration() {
		t.Fatalf("the service waits %v, but the catalogue declares %v — a wait that restates its bound can disagree with it", answerLimit, bound.Duration())
	}
	found := false
	for _, phase := range decl.Phases {
		if phase.Name != "answer" {
			continue
		}
		found = true
		if len(phase.Resources) != 0 {
			t.Fatalf("the answer phase holds %v — the wait holds no execution capacity", phase.Resources)
		}
	}
	if !found {
		t.Fatal("internal.agent.run has no answer phase")
	}
}

// ── the wake, over the real socket ────────────────────────────────────────

// The real connector calls one tool and receives the answer without restarting.
func TestAgentApprove_WakesTheCallThatIsWaiting(t *testing.T) {
	h := suspendedRun(t, askObserveStore(t))
	state, err := h.db.Ledger().RunState(t.Context(), h.runID)
	if err != nil || state == nil || *state != content.RunAwaitingApproval {
		t.Fatalf("waiting state = %v, %v", state, err)
	}
	if got := h.approve(t, "once"); got.State != string(content.RunStreaming) {
		t.Fatalf("state = %s", got.State)
	}
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(raw), "completed") {
		t.Fatalf("run did not complete: %s", raw)
	}
	if got := h.toolResult(); got == "" || strings.Contains(got, "declined") {
		t.Fatalf("tool did not receive approval: %s", got)
	}
	if h.pid() <= 0 {
		t.Fatal("no real connector process")
	}
}

func TestAgentApprove_DeclineWakesTheCallWithThePersonsSentence(t *testing.T) {
	h := suspendedDeclinableRun(t, askObserveStore(t))
	h.deny(t, "once")
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(raw), "completed") {
		t.Fatalf("a refusal is an answer: %s", raw)
	}
	if want := assistant.PersonDeclineResult("session.read", assistant.DeclineCallOnce); !strings.Contains(h.toolResult(), want) {
		t.Fatalf("result = %q, want %q", h.toolResult(), want)
	}
}

func TestAnswerWait_ExpiryClosesWindowBeforeResultIsRead(t *testing.T) {
	clock := notify.NewManualClock()
	w := newAnswerWait(clock, "session.run", answerLimit)
	w.begin()
	defer w.stop()
	clock.Advance(answerLimit)
	if w.settle(assistant.Approved{}) {
		t.Fatal("approval accepted after expiry, before result consumed it")
	}
	if _, ok := w.result(t.Context()).(assistant.Declined); !ok {
		t.Fatal("expired wait did not refuse")
	}
}

func TestApprovalService_AnswerNearMaxPausesRealScope(t *testing.T) {
	clock := notify.NewManualClock()
	scope := catalog.BeginWithClock(t.Context(), catalog.OpInternalAgentRun, catalog.Invocation{}, clock)
	clock.Advance(scope.Declaration().Max.Duration() - time.Minute)
	if !scope.BeginAnswer() {
		t.Fatal("answer phase refused")
	}
	w := newAnswerWait(clock, "session.read", answerLimit)
	w.begin()
	defer w.stop()
	clock.Advance(10 * time.Minute)
	if outcome, done := scope.Outcome(); done {
		t.Fatalf("scope settled %v during answer", outcome)
	}
	if !w.settle(assistant.Approved{}) {
		t.Fatal("answer refused")
	}
	scope.EndAnswer()
	if !scope.Complete() {
		t.Fatal("scope expired while answering")
	}
}

func TestAgentApprove_ExpiredWindowCannotWriteStandingPolicy(t *testing.T) {
	policy := askObserveStore(t)
	h := suspendedRun(t, policy)
	clock := notify.NewManualClock()
	h.ws.pendingRunsMu.Lock()
	rc := h.ws.pendingRuns[h.runID]
	original := rc.approvalWait
	original.stop()
	w := newAnswerWait(clock, h.asked.Tool, answerLimit)
	w.binding = original.binding
	rc.approvalWait = w
	h.ws.pendingRuns[h.runID] = rc
	h.ws.pendingRunsMu.Unlock()
	w.begin()
	defer w.stop()
	clock.Advance(answerLimit)
	defer original.settle(assistant.Declined{Reason: "test cleanup"})
	_, errObj := approveOverWire(t, h.conn, h.answer(true, "always"), 2)
	if got := policy.Policy().DecisionFor(content.EffectObserve); got != content.DecisionAsk {
		t.Fatalf("late answer changed standing policy to %v", got)
	}
	if errObj == nil {
		t.Fatal("late standing approval was accepted")
	}
}

func TestAgentApprovalRetired_OverTheWireConformsToContract(t *testing.T) {
	h := suspendedRun(t, askObserveStore(t))
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "agent.approve", "params": h.answer(true, "once")})
	if err := h.conn.WriteMessage(1, raw); err != nil {
		t.Fatal(err)
	}
	retired := readNotification(t, h.conn, "agent.approvalRetired", 5*time.Second)
	validateJSON(t, loadSchema(t, "agent.approvalRetired.schema.json"), retired, "real retirement notification")
	var got struct {
		RunID  string `json:"runId"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(retired, &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != h.asked.RunID || got.Reason != "policy" {
		t.Fatalf("retired wrong phase: %s", retired)
	}
}

func TestAgentApprovalRetired_DTOConformsToContract(t *testing.T) {
	for _, phase := range []string{"policy", "egress"} {
		raw, err := json.Marshal(agentApprovalRetired{"7", 1, "files.read", "call_1", "hash", phase})
		if err != nil {
			t.Fatal(err)
		}
		validateJSON(t, loadSchema(t, "agent.approvalRetired.schema.json"), raw, "retirement DTO")
	}
}
