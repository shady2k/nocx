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
	"errors"
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

// A client at the real Questioner seam drives the catalogue and answer clocks.
type clockedQuestioningClient struct {
	*questioningClient
	clock   *notify.ManualClock
	scope   *catalog.Scope
	service approvalService
	ready   chan struct{}
}

func (c *clockedQuestioningClient) Ask(ctx context.Context, p assistant.AskParams, emit func(assistant.AskEvent) error) error {
	if _, ok := catalog.ScopeFrom(ctx); !ok {
		return errors.New("run scope missing from question context")
	}
	c.scope = catalog.BeginWithClock(ctx, catalog.OpInternalAgentRun, catalog.Invocation{}, c.clock)
	c.clock.Advance(c.scope.Declaration().Max.Duration() - time.Minute)
	s, ok := p.Questioner.(approvalService)
	if !ok {
		return errors.New("questioner is not the transport approval service")
	}
	s.clock = c.clock
	c.service = s
	p.Questioner = s
	close(c.ready)
	err := c.questioningClient.Ask(catalog.WithScope(ctx, c.scope), p, emit)
	if !c.scope.Complete() {
		return errors.New("scope expired while answering")
	}
	return err
}

func TestApprovalService_AnswerNearMaxPausesRealScope(t *testing.T) {
	c := &clockedQuestioningClient{questioningClient: newQuestioningClient(), clock: notify.NewManualClock(), ready: make(chan struct{})}
	h := newAskHarness(t, c)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{"askId": "near-max", "sessionId": sid, "question": "read", "cwd": "/repo"}, 1)
	if errObj != nil {
		t.Fatal(errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	<-c.ready
	c.clock.Advance(10 * time.Minute)
	if outcome, done := c.scope.Outcome(); done {
		t.Fatalf("scope settled %v during answer", outcome)
	}
	_, errObj = approveOverWire(t, h.conn, map[string]any{"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "session.read", "callId": "call_1", "argHash": "hash-1", "approved": true, "scope": "once"}, 2)
	if errObj != nil {
		t.Fatal(errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if outcome, _ := c.scope.Outcome(); outcome != catalog.OutcomeCompleted {
		t.Fatalf("scope outcome %v", outcome)
	}
}

func TestAgentApprove_ExpiredWindowCannotWriteStandingPolicy(t *testing.T) {
	c := &clockedQuestioningClient{questioningClient: newQuestioningClient(), clock: notify.NewManualClock(), ready: make(chan struct{})}
	policy := askObserveStore(t)
	h := newAskHarnessWithOpts(t, c, WithAgentPolicy(policy))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{"askId": "expired", "sessionId": sid, "question": "read", "cwd": "/repo"}, 1)
	if errObj != nil {
		t.Fatal(errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	<-c.ready
	// Keep the actual service waiting while a separately registered window
	// expires without a consumer. This exercises handleApprove before result's
	// cleanup can hide the pending proposal from the late handler.
	handlers := c.service.handlers
	handlers.pendingRunsMu.Lock()
	rc := handlers.pendingRuns[res.RunID]
	original := rc.approvalWait
	original.stop()
	w := newAnswerWait(c.clock, "session.read", answerLimit)
	w.binding = original.binding
	rc.approvalWait = w
	handlers.pendingRuns[res.RunID] = rc
	handlers.pendingRunsMu.Unlock()
	w.begin()
	c.clock.Advance(answerLimit)
	defer original.settle(assistant.Declined{Reason: "test cleanup"})
	_, errObj = approveOverWire(t, h.conn, map[string]any{"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "session.read", "callId": "call_1", "argHash": "hash-1", "approved": true, "scope": "always"}, 2)
	if got := policy.Policy().DecisionFor(content.EffectObserve); got != content.DecisionAsk {
		t.Fatalf("late answer changed standing policy to %v", got)
	}
	if errObj == nil {
		t.Fatal("late standing approval was accepted")
	}
	original.settle(assistant.Declined{Reason: "test cleanup"})
	if _, ok := (<-c.answered).(assistant.Declined); !ok {
		t.Fatal("expired call did not receive refusal")
	}
}

func TestAgentApprovalRetired_OverTheWireConformsToContract(t *testing.T) {
	c := newQuestioningClient()
	h := newAskHarness(t, c)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, e := askOverWire(t, h.conn, map[string]any{"askId": "retire", "sessionId": sid, "question": "read", "cwd": "/repo"}, 1)
	if e != nil {
		t.Fatal(e)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	params := map[string]any{"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "session.read", "callId": "call_1", "argHash": "hash-1", "approved": true, "scope": "once"}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "agent.approve", "params": params})
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
	if got.RunID != strconv.FormatInt(res.RunID, 10) || got.Reason != "policy" {
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
