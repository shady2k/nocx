package transport

// The approval wire (nocx-z9hj4): a run that escalates reaches the person as
// agent.approvalRequested over the REAL socket — naming the tool, the
// arguments, the binding and why — the run rests in awaiting_approval in the
// ledger, and agent.approve (yes) resumes it or (no) terminalizes it with
// agent-declined. A stale id is answered honestly and resumes nothing; a
// changed argument does not resume under the old approval. The engine under
// test is a SCRIPTED stub returning the real exported suspension errors — the
// middleware's own production of them is proven in internal/assistant.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// approvalScriptStep is one Ask outcome of the scripted engine.
type approvalScriptStep struct {
	deltas  []string
	suspend func(runID string) error // non-nil → Ask returns this suspension
}

// scriptedApprovalClient plays a script of Ask outcomes, recording every
// AskParams it received so the tests can assert what the resume carried.
type scriptedApprovalClient struct {
	mu       sync.Mutex
	script   []approvalScriptStep
	received []assistant.AskParams
	count    int
}

func (s *scriptedApprovalClient) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true, Model: p.Model}, nil
}

func (s *scriptedApprovalClient) Ask(ctx context.Context, p assistant.AskParams, onDelta func(string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, p)
	step := s.script[s.count]
	s.count++
	if step.suspend != nil {
		return step.suspend(p.RunID)
	}
	for _, d := range step.deltas {
		if err := onDelta(d); err != nil {
			return err
		}
	}
	return nil
}

func (s *scriptedApprovalClient) params() []assistant.AskParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]assistant.AskParams(nil), s.received...)
}

func (s *scriptedApprovalClient) askCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// policySuspension builds the exported policy-gate suspension for a run. It
// carries an effect because the real gate always does: agent.approvalRequested
// REQUIRES the field, so a stand-in that omitted it would model a middleware
// that cannot exist — and the notification it produced would not validate.
func policySuspension(tool, callID, args, argHash string) func(runID string) error {
	return func(runID string) error {
		return &assistant.ApprovalRequestedError{Request: &assistant.ApprovalRequest{
			RunID: runID, Attempt: 1, Tool: tool, CallID: callID,
			Arguments: args, ArgHash: argHash,
			Effect:   content.EffectObserve,
			Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
		}}
	}
}

// approvalWireResult is the decoded agent.approve result: the state the run
// moved to, plus the sentence the standing part of the answer failed with —
// empty when there was nothing to record or it was recorded.
type approvalWireResult struct {
	State   string `json:"state"`
	Warning string `json:"warning"`
}

func approveOverWire(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (approvalWireResult, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCall(t, conn, "agent.approve", params)
	var env struct {
		Error  *jsonrpcErrorObj `json:"error"`
		Result approvalWireResult
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("agent.approve unmarshal: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

// noNotification asserts that NOTHING arrives on the wire within the bound —
// the negative side of "a suspension is not a failure": no terminal
// notification follows it.
func noNotification(t *testing.T, conn *websocket.Conn, d time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, msg, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("expected no notification, got: %s", msg)
	}
	if !websocket.IsUnexpectedCloseError(err) && !isTimeout(err) {
		t.Fatalf("expected a read timeout, got: %v", err)
	}
}

func isTimeout(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline"))
}

// approveDeclineOverWire drives agent.approve(approved:false) and returns
// BOTH the runState notification and the response: the decline terminalizes
// BEFORE it answers, so the runState notification precedes the response on
// the wire — a plain jsonrpcCall would consume and discard it while looking
// for the matching id.
func approveDeclineOverWire(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (agentRunState, approvalWireResult, *jsonrpcErrorObj) {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "agent.approve", "params": params})
	if err != nil {
		t.Fatalf("marshal approve: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write approve: %v", werr)
	}
	var st agentRunState
	var res approvalWireResult
	var errObj *jsonrpcErrorObj
	gotState, gotResponse := false, false
	deadline := time.Now().Add(5 * time.Second)
	for !gotState || !gotResponse {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for the decline outcome (state=%v response=%v)", gotState, gotResponse)
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read decline outcome: %v", rerr)
		}
		var env struct {
			ID     *json.RawMessage   `json:"id"`
			Method string             `json:"method"`
			Params json.RawMessage    `json:"params"`
			Error  *jsonrpcErrorObj   `json:"error"`
			Result approvalWireResult `json:"result"`
		}
		if uerr := json.Unmarshal(msg, &env); uerr != nil {
			t.Fatalf("unmarshal decline frame: %v\nraw: %s", uerr, msg)
		}
		if env.ID == nil {
			if env.Method == "agent.runState" && !gotState {
				if perr := json.Unmarshal(env.Params, &st); perr != nil {
					t.Fatalf("unmarshal runState params: %v\nraw: %s", perr, env.Params)
				}
				gotState = true
			}
			continue // some other notification; keep looking
		}
		if !gotResponse {
			res, errObj = env.Result, env.Error
			gotResponse = true
		}
	}
	return st, res, errObj
}

// ── criterion 1 + 4: an escalation reaches the person, over the real socket ─

// TestAgentApproval_SuspensionOverTheWire: a run whose Ask suspends at the
// policy gate moves to awaiting_approval in the ledger and sends the
// question — tool, arguments, binding, reason — as a notification. It is
// NEVER reported as a model failure: no terminal notification arrives, the
// run is not terminal.
func TestAgentApproval_SuspensionOverTheWire(t *testing.T) {
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	var n agentApprovalRequested
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if n.Tool != "files.read" || !strings.Contains(n.Arguments, "a.txt") {
		t.Fatalf("question = %+v, want files.read on a.txt — the tool and the arguments", n)
	}
	if n.Reason != "policy" {
		t.Fatalf("reason = %q, want policy", n.Reason)
	}
	if n.ArgHash != "hash-a" || n.CallID != "call_1" || n.Attempt != 1 {
		t.Fatalf("binding = %+v, want the full binding the answer will name", n)
	}
	if n.RunID != strconv.FormatInt(res.RunID, 10) {
		t.Fatalf("notification runId = %q, want %d", n.RunID, res.RunID)
	}

	// The durable state says a question is outstanding (criterion 4): a
	// reconnecting renderer reads awaiting_approval, distinguishable from a
	// run mid-answer.
	led := h.db.Ledger()
	st, err := led.RunState(context.Background(), res.RunID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state = %v (err %v), want awaiting_approval", st, err)
	}

	// And NOT a model failure: no terminal notification follows the
	// suspension. The run is still open — a question is being decided.
	noNotification(t, h.conn, 200*time.Millisecond)
}

// ── criterion 2: yes resumes, no terminalizes with agent-declined ─────────

// TestAgentApprove_YesResumesTheRun: the person's yes is recorded against
// the exact binding and the run streams again — the resume Ask carries the
// approval store, the same run id and the same attempt, and the run
// completes like any other.
func TestAgentApprove_YesResumesTheRun(t *testing.T) {
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
		{deltas: []string{"done"}},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	// The renderer's literal payload: the full binding plus the decision.
	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-a", "approved": true, "scope": "once",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.State != "streaming" {
		t.Fatalf("approve state = %q, want streaming (the resume is in flight)", got.State)
	}

	// The resume streams — an observable state change (never a duration) —
	// and THEN its Ask is inspectable: the task runs off the read loop on
	// its own admission, so the engine may not have been called the instant
	// the approve response arrived.
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)

	// The resume reached the engine with the approval store and the run's
	// identity — the yes crossed the suspension.
	params := client.params()
	if len(params) != 2 {
		t.Fatalf("engine received %d asks, want 2 (escalate + resume)", len(params))
	}
	p := params[1]
	if p.Approvals == nil {
		t.Fatal("the resume Ask carried no approval store — the yes cannot cross the suspension")
	}
	if p.RunID != strconv.FormatInt(res.RunID, 10) || p.Attempt != 1 {
		t.Fatalf("resume identity = run %q attempt %d, want the SAME binding the person approved", p.RunID, p.Attempt)
	}

	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.State != "completed" {
		t.Fatalf("runState = %s (err %v), want completed", raw, err)
	}

	// The yes is a store fact keyed by the exact binding.
	ap := assistant.Approval{RunID: strconv.FormatInt(res.RunID, 10), Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: "hash-a"}
	if !h.ws.agentApprovals.IsApproved(ap) {
		t.Fatal("the approved binding is not recorded in the store")
	}
}

// TestAgentApprove_NoTerminalizesDeclined: answering no terminalizes the run
// with the reason that says a person declined — agent-declined — and the
// run is never resumed.
func TestAgentApprove_NoTerminalizesDeclined(t *testing.T) {
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	// The decline terminalizes BEFORE it answers: the runState notification
	// precedes the response on the wire, and the reader captures both.
	st, got, errObj := approveDeclineOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-a", "approved": false, "scope": "once",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.approve(no): %+v", errObj)
	}
	if got.State != "failed" {
		t.Fatalf("approve(no) state = %q, want failed", got.State)
	}
	if n := client.askCount(); n != 1 {
		t.Fatalf("engine received %d asks, want 1 — a declined run is never resumed", n)
	}
	if st.State != "failed" {
		t.Fatalf("runState = %q, want failed", st.State)
	}

	// The ledger says a person declined: the run row's termination reason.
	led := h.db.Ledger()
	q, err := led.Entry(context.Background(), res.QuestionID)
	if err != nil || q == nil || len(q.Executions) == 0 {
		t.Fatalf("question entry: %v (err %v)", q, err)
	}
	if q.Executions[0].TerminationReason == nil || *q.Executions[0].TerminationReason != content.TermAgentDeclined {
		t.Fatalf("run termination = %v, want agent-declined", q.Executions[0].TerminationReason)
	}
}

// ── criterion 7: a stale or unknown approval id is answered honestly ───────

// TestAgentApprove_UnknownIdResumesNothing: an approve naming a binding that
// was never asked — or was already answered — is refused, and nothing
// resumes: no second model request, the run stays awaiting_approval.
func TestAgentApprove_UnknownIdResumesNothing(t *testing.T) {
	const args = `{"path":"/repo/a.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", args, "hash-a")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	// A changed argument hash is a different proposal: nothing was asked
	// about it, so the yes is answered honestly and resumes nothing.
	_, errObj = approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-CHANGED", "approved": true, "scope": "once",
	}, 2)
	if errObj == nil {
		t.Fatal("agent.approve with an unknown binding succeeded — criterion 7: it must be answered honestly")
	}
	if n := client.askCount(); n != 1 {
		t.Fatalf("engine received %d asks, want 1 — the unknown approval resumed nothing", n)
	}
	led := h.db.Ledger()
	st, err := led.RunState(context.Background(), res.RunID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state = %v (err %v), want still awaiting_approval", st, err)
	}
}

// ── criterion 3: a changed argument does not resume under the old approval ─

// TestAgentApprove_ChangedArgumentsResuspend: the resume re-runs the
// pipeline; a call whose arguments CHANGED hashes differently and is not
// covered by the old approval — the run suspends AGAIN with the new
// proposal, and the first yes did not run the changed call.
func TestAgentApprove_ChangedArgumentsResuspend(t *testing.T) {
	const argsA = `{"path":"/repo/a.txt"}`
	const argsB = `{"path":"/repo/b.txt"}`
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", argsA, "hash-a")},
		{suspend: policySuspension("files.read", "call_1", argsB, "hash-b")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	// The person approves the ORIGINAL proposal.
	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(res.RunID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-a", "approved": true, "scope": "once",
	}, 2)
	if errObj != nil || got.State != "streaming" {
		t.Fatalf("agent.approve: %+v (err %v)", got, errObj)
	}

	// The resume's call carries DIFFERENT arguments: the old approval does
	// not cover it — the run suspends again, with the new proposal.
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	var n agentApprovalRequested
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("second approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if n.ArgHash != "hash-b" || !strings.Contains(n.Arguments, "b.txt") {
		t.Fatalf("second question = %+v, want the CHANGED proposal — the old approval must not cover it", n)
	}
	if n.RunID != strconv.FormatInt(res.RunID, 10) {
		t.Fatalf("second question runId = %q, want the same run", n.RunID)
	}

	// Still not terminal: the second question is being decided.
	led := h.db.Ledger()
	st, err := led.RunState(context.Background(), res.RunID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state = %v (err %v), want awaiting_approval after the re-suspension", st, err)
	}
}

// ── criterion 6: an egress finding suspends through the SAME surface ───────

// TestAgentApproval_EgressFindingOverTheWire: the egress gate's suspension
// ends at the same question — reason "egress", the findings rendered — and
// no finding carries the secret material at any point on the wire.
func TestAgentApproval_EgressFindingOverTheWire(t *testing.T) {
	const secret = "sk-proj-abcdefghijklmnopqrstuvwx"
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: func(runID string) error {
			return &assistant.EgressRequestedError{Request: &assistant.EgressRequest{
				RunID: runID, Attempt: 1, Tool: "files.read", CallID: "call_1",
				Arguments: `{"path":"/repo/a.txt"}`, ArgHash: "hash-a",
				Effect:   content.EffectObserve,
				Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
				Findings: []assistant.EgressFinding{{
					Source: assistant.EgressFindingHeuristic, Kind: "openai-api-key",
					Start: 11, End: 11 + len(secret),
				}},
			}}
		}},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please read it", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the wire carried the secret material itself: %s", raw)
	}
	var n agentApprovalRequested
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if n.Reason != "egress" {
		t.Fatalf("reason = %q, want egress", n.Reason)
	}
	if len(n.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", n.Findings)
	}
	f0 := n.Findings[0]
	if f0.Source != assistant.EgressFindingHeuristic || f0.Kind != "openai-api-key" {
		t.Fatalf("finding = %+v, want the heuristic kind rendered", f0)
	}
	if f0.SecretName != "" {
		t.Fatalf("finding carries material: %+v", f0)
	}
	if n.ArgHash != "hash-a" {
		t.Fatalf("egress question binding = %+v, want the full binding", n)
	}
	if st, err := h.db.Ledger().RunState(context.Background(), res.RunID); err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state = %v (err %v), want awaiting_approval", st, err)
	}
}
