package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestAgentUsageRealPiAfterToolCycle(t *testing.T) {
	for _, terminal := range []string{"failed", "cancelled", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			gate := make(chan struct{})
			defer close(gate)
			second := fakemodel.Response{StatusCode: 400, Error: "provider refused the next call"}
			want := content.RunUsage{Input: 13, Output: 5, CacheRead: 7}
			if terminal == "cancelled" {
				second = fakemodel.Response{Block: gate}
			}
			if terminal == "completed" {
				second = fakemodel.Response{Text: []string{"finished"}, Usage: &fakemodel.Usage{InputTokens: 17, OutputTokens: 3, CacheReadTokens: 11}}
				want = content.RunUsage{Input: 30, Output: 8, CacheRead: 18}
			}
			backend, fake := realPrefixPi(t,
				fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "usage-tool", Name: "mcp__nocx__session_list", Arguments: `{}`}}, Usage: &fakemodel.Usage{InputTokens: 13, OutputTokens: 5, CacheReadTokens: 7}}, second)
			h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("pi", backend), WithAgentPolicy(autonomousPolicyStore(t)))
			seedPaneChain(t, h.db, locatePaneOther, locatePaneThis)
			sid, openErr := openSessionInPane(t, h.conn, locatePaneOther, 10)
			if openErr != nil {
				t.Fatal(openErr)
			}
			res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "usage-cycle", "sessionId": sid, "question": "list sessions then answer", "cwd": t.TempDir(), "attachedContent": []any{}}, 1)
			if rpcErr != nil {
				t.Fatalf("ask: %+v", rpcErr)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			requests, waitErr := fake.WaitRequests(ctx, 2)
			if waitErr != nil {
				t.Fatal(waitErr)
			}
			var toolResult struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
			}
			if err := json.Unmarshal(requests[1].Messages[len(requests[1].Messages)-1], &toolResult); err != nil {
				t.Fatal(err)
			}
			if toolResult.Role != "tool" || toolResult.ToolCallID != "usage-tool" {
				t.Fatalf("second call did not follow completed tool cycle: %s", requests[1].Body)
			}
			var notifications []json.RawMessage
			if terminal == "cancelled" {
				raw, notices := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
				var reply struct {
					Result agentCancelResponse `json:"result"`
					Error  *jsonrpcErrorObj    `json:"error"`
				}
				if err := json.Unmarshal(raw, &reply); err != nil {
					t.Fatal(err)
				}
				if reply.Error != nil || !reply.Result.Cancelled {
					t.Fatalf("Stop: %s", raw)
				}
				notifications = notices
			}
			var state agentRunState
			for state.State != terminal {
				var raw json.RawMessage
				if len(notifications) > 0 {
					raw, notifications = notifications[0], notifications[1:]
				} else {
					raw = readNotification(t, h.conn, "agent.runState", 20*time.Second)
				}
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				if state.State == "completed" || state.State == "failed" || state.State == "cancelled" {
					break
				}
			}
			if state.State != terminal {
				t.Fatalf("state=%s, want %s", state.State, terminal)
			}
			if state.Usage == nil || *state.Usage != want {
				t.Errorf("terminal usage=%+v, want %+v", state.Usage, want)
			}
			entry, err := h.db.Ledger().Entry(t.Context(), res.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			var facts content.RunFacts
			if err := json.Unmarshal([]byte(entry.Executions[0].Payload), &facts); err != nil {
				t.Fatal(err)
			}
			if facts.Usage == nil || *facts.Usage != want {
				t.Errorf("durable usage=%+v, want %+v", facts.Usage, want)
			}
			detail := vaultCall(t, h.conn, "ledger.get", map[string]any{"id": res.EntryID}, 3)
			if detail.Error != nil {
				t.Fatalf("ledger.get: %+v", detail.Error)
			}
			var restored struct {
				RunFacts *content.RunFacts `json:"runFacts"`
			}
			if err := json.Unmarshal(detail.Result, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.RunFacts == nil || restored.RunFacts.Usage == nil || *restored.RunFacts.Usage != want {
				t.Fatalf("restored usage: %s, want %+v", detail.Result, want)
			}
		})
	}
}
