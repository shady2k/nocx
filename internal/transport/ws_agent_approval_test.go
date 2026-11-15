package transport

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

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

func TestAgentApproval_TheBlockedCallShowsItsQuestion(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))
	n := h.asked
	if n.Tool != "session.read" || n.Reason != "policy" {
		t.Fatalf("question = %+v", n)
	}
	if n.ArgHash == "" || n.CallID == "" || n.Attempt != 1 || n.RunID == "" {
		t.Fatalf("incomplete binding = %+v", n)
	}
	assertAwaitingApproval(t, h)
	noNotification(t, h.conn, 200*time.Millisecond)
}

func assertAwaitingApproval(t *testing.T, h *scopeHarness) {
	t.Helper()
	st, err := h.db.Ledger().RunState(context.Background(), h.runID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("run state = %v (err %v), want awaiting_approval", st, err)
	}
}

func TestAgentApprove_TheYesReachesTheCallThatAsked(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))
	pid := h.pid()
	if got := h.approve(t, "once"); got.State != "streaming" {
		t.Fatalf("approve = %+v", got)
	}
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.State != "completed" {
		t.Fatalf("runState = %s (%v)", raw, err)
	}
	assertOneApprovalCall(t, h)
	if after := h.pid(); after != pid {
		t.Fatalf("approval replaced actor %d with %d", pid, after)
	}
	ap := scopeApprovalBinding(h)
	if !h.ws.agentApprovals.IsApproved(ap) {
		t.Fatal("the exact approved binding was not stored")
	}
	if result := h.toolResult(); strings.Contains(result, "declined") {
		t.Fatalf("approved tool result = %s", result)
	}
}

func assertOneApprovalCall(t *testing.T, h *scopeHarness) {
	t.Helper()
	entries, err := h.db.Ledger().Caused(t.Context(), h.entryID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, entry := range entries {
		if entry.Kind == content.EntryAction && entry.Intent == h.asked.Tool {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("accepted tool actions = %d, want exactly one", calls)
	}
}

func scopeApprovalBinding(h *scopeHarness) assistant.Approval {
	return assistant.Approval{RunID: h.asked.RunID, Attempt: h.asked.Attempt, Tool: h.asked.Tool, CallID: h.asked.CallID, ArgHash: h.asked.ArgHash}
}

func TestAgentApprove_TheNoReachesTheCallThatAsked(t *testing.T) {
	h := suspendedDeclinableRun(t, askPolicyStore(t))
	pid := h.pid()
	if got := h.deny(t, "once"); got.State != "streaming" {
		t.Fatalf("deny = %+v", got)
	}
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.State != "completed" {
		t.Fatalf("runState = %s (%v)", raw, err)
	}
	assertOneApprovalCall(t, h)
	if after := h.pid(); after != pid {
		t.Fatalf("decline replaced actor %d with %d", pid, after)
	}
	if result := h.toolResult(); !strings.Contains(result, "declin") && !strings.Contains(result, "refus") {
		t.Fatalf("call did not receive refusal: %s", result)
	}
	q, err := h.db.Ledger().Entry(context.Background(), h.entryID)
	if err != nil || q == nil || len(q.Executions) == 0 {
		t.Fatalf("question entry = %v (%v)", q, err)
	}
	if q.Executions[0].TerminationReason == nil || *q.Executions[0].TerminationReason != content.TermCompleted {
		t.Fatalf("termination = %v", q.Executions[0].TerminationReason)
	}
}

func TestAgentApprove_UnknownIdResumesNothing(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))
	params := h.answer(true, "once")
	params["argHash"] = "changed-binding"
	if _, errObj := approveOverWire(t, h.conn, params, 2); errObj == nil {
		t.Fatal("unknown binding succeeded")
	}
	assertAwaitingApproval(t, h)
	// The original binding still reaches the one accepted call.
	h.deny(t, "once")
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
}

func TestAgentApproval_EgressFindingOverTheWire(t *testing.T) {
	h := suspendedEgressRun(t, askPolicyStore(t))
	n := h.asked
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-proj-abcdefghijklmnopqrstuvwx") {
		t.Fatalf("secret leaked: %s", raw)
	}
	if n.Reason != "egress" || len(n.Findings) != 1 {
		t.Fatalf("egress question = %+v", n)
	}
	if n.Findings[0].Source != assistant.EgressFindingHeuristic || n.Findings[0].Kind != "openai" || n.Findings[0].SecretName != "" {
		t.Fatalf("finding = %+v", n.Findings[0])
	}
	if n.ArgHash == "" {
		t.Fatal("missing egress binding")
	}
	assertAwaitingApproval(t, h)
}

func TestAgentApprove_TheEgressNoReachesTheCallThatAsked(t *testing.T) {
	h := suspendedEgressRun(t, askPolicyStore(t))
	if got := h.deny(t, "once"); got.State != "streaming" {
		t.Fatalf("deny = %+v", got)
	}
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.State == "completed" {
		t.Fatalf("egress refusal did not stop run: %s (%v)", raw, err)
	}
	result, err := os.ReadFile(h.script.ResultFile)
	if err == nil && strings.Contains(string(result), "sk-proj-") {
		t.Fatalf("withheld material reached agent: %s", result)
	}
}
