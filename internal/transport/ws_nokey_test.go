package transport

import (
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func TestAgentAsk_AnswersWithoutEndpointCredential(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "local agent answer"}, {Kind: protocol.StepEnd},
	}})
	sid := openLocalSession(t, h.conn)
	if _, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "without-credential", "sessionId": sid, "question": "answer locally", "cwd": "/repo"}, 2); rpcErr != nil {
		t.Fatalf("agent.ask: %+v", rpcErr)
	}
	delta := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	if !strings.Contains(string(delta), "local agent answer") {
		t.Fatalf("answer: %s", delta)
	}
	state := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(state), `"state":"completed"`) {
		t.Fatalf("state: %s", state)
	}
}
