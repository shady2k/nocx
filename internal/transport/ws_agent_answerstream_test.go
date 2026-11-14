package transport

// The answer stream on the wire (nocx-shxv0, nocx-bshm2, nocx-s92so): a tool
// call and the model's reasoning are their own notifications, they arrive in
// the order they happened, and a run with neither is byte-for-byte the run it
// always was.
//
// contracts/README row 3 is the one that matters here: every conformance
// assertion below reads the REAL notification off the REAL socket. A test
// that validated a payload it built itself would prove the struct is
// well-formed, not that the server sends it.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

// askAndCollect drives one ask over the real socket and reads notifications
// until the run's terminal agent.runState, returning them in ARRIVAL order.
// The order is the property under test, so nothing is filtered on the way in.
func askAndCollect(t *testing.T, h *askHarness, question string) []wireNotification {
	t.Helper()
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": question, "cwd": "/repo",
		"attachedContent": []any{},
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	var got []wireNotification
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = h.conn.SetReadDeadline(deadline)
		_, raw, err := h.conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var n wireNotification
		if err := json.Unmarshal(raw, &n); err != nil {
			continue
		}
		if !strings.HasPrefix(n.Method, "agent.run") {
			continue
		}
		got = append(got, n)
		if n.Method == "agent.runState" {
			return got
		}
	}
	t.Fatalf("no terminal agent.runState within the deadline; got %v", methodsOf(got))
	return nil
}

type wireNotification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func methodsOf(ns []wireNotification) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Method)
	}
	return out
}

// A permitted read announces the durable attempt through the real kernel.
func oneToolCall(t *testing.T) protocol.Step { return readFileStep(t) }

// ── criterion 1: the order, on the wire ───────────────────────────────────

// TestAskStream_TheToolCallPrecedesTheAnswerWrittenFromIt: the notification
// announcing the call reaches the renderer BEFORE the delta of the answer
// written from its result. Asserted on arrival order off the real socket.
func TestAskStream_TheToolCallPrecedesTheAnswerWrittenFromIt(t *testing.T) {
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		oneToolCall(t),
		{Kind: protocol.StepAnswer, Text: "the file says hello"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))

	got := askAndCollect(t, h, "what is in that file?")
	call, delta := -1, -1
	for i, n := range got {
		if n.Method == "agent.runToolCall" && call < 0 {
			call = i
		}
		if n.Method == "agent.runDelta" && delta < 0 {
			delta = i
		}
	}
	if call < 0 {
		t.Fatalf("no agent.runToolCall on the wire: %v", methodsOf(got))
	}
	if delta < 0 {
		t.Fatalf("no agent.runDelta on the wire: %v", methodsOf(got))
	}
	if call > delta {
		t.Fatalf("the call was announced AFTER the answer written from it: %v", methodsOf(got))
	}
}

// ── criterion 5: the real payloads, against their contracts ───────────────

func TestAgentRunToolCall_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		oneToolCall(t),
		{Kind: protocol.StepAnswer, Text: "done"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "what is in that file?", "cwd": "/repo",
		"attachedContent": []any{},
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runToolCall", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runToolCall params (real socket)")

	// And the facts a person is shown are the ones the middleware derived —
	// not a raw arguments blob and not the tool's result.
	var got struct {
		Tool          string `json:"tool"`
		CallID        string `json:"callId"`
		ActionEntryID string `json:"actionEntryId"`
		Effect        string `json:"effect"`
		OpensBlock    bool   `json:"opensBlock"`
		Resource      *struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tool != "files.read" || got.CallID == "" || got.ActionEntryID == "" {
		t.Fatalf("payload = %s, want the middleware's facts", raw)
	}
	if got.Effect != "observe" {
		t.Fatalf("effect = %q, want observe", got.Effect)
	}
	if got.Resource == nil || got.Resource.Kind != "path" || !strings.HasSuffix(got.Resource.ID, "/a.txt") {
		t.Fatalf("resource = %+v, want the path the call named", got.Resource)
	}
	// files.read opens no block, so its LINE is the only thing that says the
	// call happened (nocx-9sqii) — and the fact is on the wire either way,
	// so the renderer never has to know which tools open blocks.
	if got.OpensBlock {
		t.Fatalf("files.read announced opensBlock=true: %s", raw)
	}
}

// The other value of the same fact, off the same socket: `session.run` submits a
// command, so the block that command opens is the account of the call and
// the flow draws no line beside it (nocx-9sqii). Asserted here because a
// boolean asserted in one state is a field nobody has checked.
func TestAgentRunToolCall_ACallThatOpensABlockSaysSoOverTheWire(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "session.run", Arguments: map[string]any{"command": "df -h"}},
		{Kind: protocol.StepAnswer, Text: "41G free"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "how much disk is left?", "cwd": "/repo",
		"attachedContent": []any{},
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runToolCall", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runToolCall params (a call that opens a block)")
	var got struct {
		Tool       string `json:"tool"`
		OpensBlock bool   `json:"opensBlock"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tool != "session.run" || !got.OpensBlock {
		t.Fatalf("payload = %s, want session.run announcing opensBlock=true", raw)
	}
}

// Optional resource omission is a DTO/socket contract, not a fabricated
// engine event. The real bridge case above proves the populated variant.
func TestAgentRunToolCall_NoResourceOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")
	h := newAskHarness(t, answerScript("unused"))
	// A completed control-plane round trip proves the connection is registered.
	_ = jsonrpcCall(t, h.conn, "settings.list", map[string]any{})
	h.ws.connsMu.Lock()
	var conn *wsConn
	for live := range h.ws.conns {
		conn = live
	}
	h.ws.connsMu.Unlock()
	if conn == nil {
		t.Fatal("no registered WebSocket")
	}
	if err := conn.TryNotify("agent.runToolCall", mustMarshal(agentRunToolCall{
		RunID: 1, EntryID: "turn", CallID: "call", ActionEntryID: "action",
		Tool: "git.status", Args: map[string]any{}, Effect: string(content.EffectObserve),
	})); err != nil {
		t.Fatal(err)
	}
	raw := readNotification(t, h.conn, "agent.runToolCall", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runToolCall params, no resource (real socket)")
	if strings.Contains(string(raw), `"resource"`) {
		t.Fatalf("resource unexpectedly present: %s", raw)
	}
}

func TestAgentRunReasoning_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runReasoning.schema.json")
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepReasoning, Text: "the question is about the file"},
		{Kind: protocol.StepAnswer, Text: "it says hello"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "what is in that file?", "cwd": "/repo",
		"attachedContent": []any{},
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runReasoning", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runReasoning params (real socket)")
	if !strings.Contains(string(raw), "the question is about the file") {
		t.Fatalf("reasoning payload = %s, want the model's thinking", raw)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
}

// ── criterion 2 and 4: what the ANSWER carries, and the unchanged run ─────

// TestAskStream_ReasoningNeverReachesTheAnswerDeltas: the deltas of a run
// that reasoned carry the answer and nothing else. The wire, not the struct:
// a renderer that read the reasoning out of agent.runDelta would be right
// back at the defect.
func TestAskStream_ReasoningNeverReachesTheAnswerDeltas(t *testing.T) {
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepReasoning, Text: "SENTINEL-thinking"},
		{Kind: protocol.StepAnswer, Text: "the answer"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	for _, n := range askAndCollect(t, h, "why?") {
		if n.Method == "agent.runDelta" && strings.Contains(string(n.Params), "SENTINEL-thinking") {
			t.Fatalf("the model's thinking travelled the delta path: %s", n.Params)
		}
	}
}

// TestAskStream_APlainRunIsExactlyWhatItWas is criterion 4's other end
// (AGENTS.md testing rule 3): a model with no reasoning and an ask with no
// tool calls put deltas and one terminal state on the wire, and NOTHING
// else. No empty reasoning frame, no placeholder call.
func TestAskStream_APlainRunIsExactlyWhatItWas(t *testing.T) {
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "he"},
		{Kind: protocol.StepAnswer, Text: "llo"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	got := methodsOf(askAndCollect(t, h, "hi?"))
	want := []string{"agent.runDelta", "agent.runDelta", "agent.runState"}
	if len(got) != len(want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notifications = %v, want %v", got, want)
		}
	}
}
