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
	"strconv"
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

// questioningClient is the engine that puts a prepared question to the wired
// service — exactly what the kernel's approver does — and reports the answer it
// was given. It records every Ask it ran, which is how "the answer did not
// start a new drive" is asserted.
type questioningClient struct {
	unauditedClient
	asked    chan *assistant.ApprovalRequest
	answered chan assistant.Answer
	asks     chan struct{}
}

func newQuestioningClient() *questioningClient {
	return &questioningClient{
		asked:    make(chan *assistant.ApprovalRequest, 4),
		answered: make(chan assistant.Answer, 4),
		asks:     make(chan struct{}, 4),
	}
}

func (c *questioningClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true}, nil
}

func (*questioningClient) Discard(string) {}

func (c *questioningClient) Ask(ctx context.Context, p assistant.AskParams, _ func(assistant.AskEvent) error) error {
	c.asks <- struct{}{}
	req := &assistant.ApprovalRequest{
		RunID: p.RunID, Attempt: p.Attempt, Tool: "session.read", CallID: "call_1",
		Arguments: "{}", ArgHash: "hash-1", Effect: content.EffectObserve,
		EntryID: p.TurnEntryID,
	}
	// What the kernel does before it asks: the proposal is recorded in the
	// store the answer is checked against.
	p.Approvals.Request(assistant.Approval{
		RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID,
		ArgHash: req.ArgHash, Effect: req.Effect, EntryID: req.EntryID,
	})
	if p.Questioner == nil {
		return errNoQuestioner
	}
	c.asked <- req
	answer := p.Questioner.Ask(ctx, req)
	c.answered <- answer
	return nil
}

var errNoQuestioner = errApprovalServiceNotWired

// TestAgentApprove_WakesTheCallThatIsWaiting: the run moves to
// awaiting_approval while the call is still on the stack, agent.approve settles
// the exact binding and the WAITING call receives the yes — the engine ran
// once, so no new drive was started — and the run completes.
func TestAgentApprove_WakesTheCallThatIsWaiting(t *testing.T) {
	client := newQuestioningClient()
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	select {
	case <-client.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never put its question to the service")
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	var n agentApprovalRequested
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if n.Tool != "session.read" || n.CallID != "call_1" || n.ArgHash != "hash-1" {
		t.Fatalf("question = %+v, want the binding the service was handed", n)
	}
	// The durable state is awaiting_approval WHILE the call is still blocked:
	// the run did not end at the question, which is the difference this stage
	// makes.
	st, err := h.db.Ledger().RunState(context.Background(), res.RunID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state while the question is open = %v (err %v), want awaiting_approval", st, err)
	}

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "session.read",
		"callId": "call_1", "argHash": "hash-1", "approved": true, "scope": "once",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.State != string(content.RunStreaming) {
		t.Fatalf("approve state = %q, want streaming", got.State)
	}

	select {
	case answer := <-client.answered:
		if _, ok := answer.(assistant.Approved); !ok {
			t.Fatalf("the waiting call received %T, want the person's yes", answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the person's yes never reached the call that was waiting")
	}

	raw = readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.State != string(content.RunCompleted) {
		t.Fatalf("runState = %s (err %v), want completed", raw, err)
	}
	select {
	case <-client.asks:
	default:
		t.Fatal("the engine was never asked")
	}
	select {
	case <-client.asks:
		t.Fatal("the answer started a second drive — the engine was asked twice")
	default:
	}
}

// TestAgentApprove_DeclineWakesTheCallWithThePersonsSentence: a no reaches the
// same call as a refusal in the person's own words, and nothing is re-driven.
func TestAgentApprove_DeclineWakesTheCallWithThePersonsSentence(t *testing.T) {
	client := newQuestioningClient()
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	select {
	case <-client.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never put its question to the service")
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "session.read",
		"callId": "call_1", "argHash": "hash-1", "approved": false, "scope": "once",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.State != string(content.RunStreaming) {
		t.Fatalf("approve state = %q, want streaming", got.State)
	}

	select {
	case answer := <-client.answered:
		declined, ok := answer.(assistant.Declined)
		if !ok {
			t.Fatalf("the waiting call received %T, want the person's no", answer)
		}
		if want := assistant.PersonDeclineResult("session.read", assistant.DeclineCallOnce); declined.Reason != want {
			t.Fatalf("refusal = %q, want %q", declined.Reason, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the person's no never reached the call that was waiting")
	}
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.State != string(content.RunCompleted) {
		t.Fatalf("runState = %s (err %v), want completed — a refusal is an answer", raw, err)
	}
}
