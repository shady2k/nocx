package transport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

// steerScript keeps the turn open after it has said something: the moment a
// person types while the agent answers.
func steerScript() protocol.Script {
	return protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "reading the logs"},
		{Kind: protocol.StepHang},
	}}
}

func TestAgentSteer_DTOConformsToContract(t *testing.T) {
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), mustMarshal(agentSteerResponse{
		RunID: 7, Steered: true,
	}), "agent.steer DTO")
}

// A message typed while the conversation's turn runs reaches the agent that is
// answering it, over the real socket, and is answered as delivered.
func TestAgentSteer_OverTheWireReachesTheRunningAgent(t *testing.T) {
	h := newAskHarness(t, steerScript())
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-1", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	h.waitHung()

	const message = "also check the second log"
	resp := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": message}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), env.Result, "agent.steer result (real socket)")
	var result agentSteerResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.steer result: %v", err)
	}
	if result.RunID != res.RunID || !result.Steered {
		t.Fatalf("agent.steer result = %+v, want run %d steered", result, res.RunID)
	}
	// The agent itself received it, as the person wrote it.
	if got := strings.TrimSpace(string(h.agent.read(h.agent.script.SteerFile))); got != message {
		t.Fatalf("the agent received %q, want the person's message", got)
	}
	_ = jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": res.RunID}, 3)
}

// After the turn has ended there is nowhere for a message to land, and the
// answer says so instead of pretending it was delivered.
func TestAgentSteer_RefusedWhenNoTurnRuns(t *testing.T) {
	h := newAskHarness(t, answerScript("done"))
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-2", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	state := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var settled agentRunState
	if err := json.Unmarshal(state, &settled); err != nil {
		t.Fatalf("decode agent.runState: %v", err)
	}
	if settled.RunID != res.RunID || settled.State != string(content.RunCompleted) {
		t.Fatalf("runState = %+v, want run %d completed", settled, res.RunID)
	}
	resp := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": "too late"}, 2)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode refused agent.steer: %v\nraw: %s", err, resp)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "not active") {
		t.Fatalf("agent.steer after the turn ended = %s, want an honest not-active error", resp)
	}
}
