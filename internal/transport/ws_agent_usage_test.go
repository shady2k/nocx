package transport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func TestAgentUsage_OverTheWireConformsToContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *protocol.Usage
	}{
		{name: "absent"},
		{name: "zero", usage: &protocol.Usage{}},
		{name: "reported", usage: &protocol.Usage{InputTokens: 123, OutputTokens: 45, CacheReadTokens: 67, CacheWriteTokens: 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "answer"}, {Kind: protocol.StepEnd, Model: "actual", Usage: tc.usage}}}
			h := newAskHarnessWithOpts(t, script)
			sid := openLocalSession(t, h.conn)
			res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "usage-ask", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
			if rpcErr != nil {
				t.Fatalf("ask: %+v", rpcErr)
			}
			raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
			validateJSON(t, loadSchema(t, "agent.runState.schema.json"), raw, "usage terminal over socket")
			var state struct {
				Usage *content.RunUsage `json:"usage"`
			}
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			entry, err := h.db.Ledger().Entry(t.Context(), res.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			var facts content.RunFacts
			if err := json.Unmarshal([]byte(entry.Executions[0].Payload), &facts); err != nil {
				t.Fatal(err)
			}
			detail := vaultCall(t, h.conn, "ledger.get", map[string]any{"id": res.EntryID}, 3)
			if detail.Error != nil {
				t.Fatalf("ledger.get: %+v", detail.Error)
			}
			validateJSON(t, loadSchema(t, "ledger.get.schema.json"), detail.Result, "usage restore over socket")
			var restored struct {
				RunFacts *struct {
					Model string            `json:"model"`
					Usage *content.RunUsage `json:"usage"`
				} `json:"runFacts"`
			}
			if err := json.Unmarshal(detail.Result, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.RunFacts == nil || restored.RunFacts.Model != "actual" {
				t.Fatalf("restored run facts: %s", detail.Result)
			}
			if tc.usage != nil && (restored.RunFacts.Usage == nil || state.Usage == nil || *restored.RunFacts.Usage != *state.Usage) {
				t.Fatalf("restored usage: %s", detail.Result)
			}
			if tc.usage == nil && restored.RunFacts.Usage != nil {
				t.Fatal("restored invented usage")
			}
			if tc.usage == nil {
				if state.Usage != nil || facts.Usage != nil {
					t.Fatal("invented usage")
				}
				return
			}
			want := content.RunUsage{Input: tc.usage.InputTokens, Output: tc.usage.OutputTokens, CacheRead: tc.usage.CacheReadTokens, CacheWrite: tc.usage.CacheWriteTokens}
			if state.Usage == nil || *state.Usage != want {
				t.Fatalf("wire usage %+v want %+v", state.Usage, want)
			}
			if facts.Usage == nil || *facts.Usage != want {
				t.Fatalf("ledger usage %+v want %+v", facts.Usage, want)
			}
		})
	}
}

func TestAgentUsage_DTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(agentRunState{RunID: 1, State: "completed", Usage: &content.RunUsage{Input: 123, Output: 45, CacheRead: 67, CacheWrite: 8}})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "agent.runState.schema.json"), raw, "usage DTO")
}
