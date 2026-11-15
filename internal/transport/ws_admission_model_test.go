package transport

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func TestConversationModelPinsFirstCancelledDefault(t *testing.T) {
	backend := newModelPinHarness(t, "fake-default", "changed-role")
	agent := newScriptedAgent(t, protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "partial"}, {Kind: protocol.StepHang}}})
	backend.Harness = agent.harness
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "cancel-pin-first", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 1)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	agent.waitHung()
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw, _ := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
	if isErrorResponse(t, raw) {
		t.Fatalf("cancel: %s", raw)
	}
	backend.Harness = newScriptedAgent(t, protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "pinned answer"}, {Kind: protocol.StepEnd}}}).harness
	raw = jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"agentId": "test", "role": "answering", "model": "changed-role"})
	if isErrorResponse(t, raw) {
		t.Fatalf("assign: %s", raw)
	}
	_, rpcErr = askOverWire(t, h.conn, map[string]any{"askId": "cancel-pin-next", "sessionId": sid, "question": "again?", "cwd": "/repo", "attachedContent": []any{}}, 3)
	if rpcErr != nil {
		t.Fatalf("second ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := backend.modelAt(1); got != "fake-default" {
		t.Fatalf("after first cancellation model = %q, want fake-default", got)
	}
}
