package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
)

// statusHarness supplies only the capabilities exercised by agent.status.
type statusHarness struct {
	agentharness.Harness
	readiness agentharness.Readiness
	models    []agentharness.Model
}

func (h *statusHarness) Readiness(context.Context) agentharness.Readiness     { return h.readiness }
func (h *statusHarness) Models(context.Context) ([]agentharness.Model, error) { return h.models, nil }

func TestHarnessStatusUsesConversationPinBeforeUnavailableRole(t *testing.T) {
	backend := newModelPinHarness(t, "pinned", "removed-choice")
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("test", backend))
	sid := openSessionInAskPane(t, h.conn, 1)
	ask := func(id string) {
		t.Helper()
		_, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": id, "sessionId": sid, "question": "continue?", "cwd": "/repo", "attachedContent": []any{}}, 2)
		if rpcErr != nil {
			t.Fatalf("ask: %+v", rpcErr)
		}
		readNotification(t, h.conn, "agent.runState", 5*time.Second)
	}
	ask("pin-before-role-change")
	raw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"agentId": "test", "role": "answering", "model": "removed-choice"})
	if isErrorResponse(t, raw) {
		t.Fatalf("assign: %s", raw)
	}
	backend.mu.Lock()
	backend.models = []agentharness.Model{{ID: "pinned"}}
	backend.mu.Unlock()
	for _, continuing := range []bool{true, false} {
		params := map[string]any{}
		if continuing {
			params["sessionId"] = sid
		}
		raw = jsonrpcCall(t, h.conn, "agent.status", params)
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.Error != nil {
			t.Fatalf("status: %s (%v)", raw, err)
		}
		validateJSON(t, loadSchema(t, "agent.status.schema.json"), env.Result, "conversation readiness")
		var got harnessStatusResult
		if err := json.Unmarshal(env.Result, &got); err != nil {
			t.Fatal(err)
		}
		if continuing {
			if !got.Answering.Ready || got.Answering.Model == nil || *got.Answering.Model != "pinned" {
				t.Fatalf("pinned conversation must stay ready: %s", raw)
			}
			ask("continue-after-role-change")
			if backend.modelAt(1) != "pinned" {
				t.Fatalf("continued on %q", backend.modelAt(1))
			}
		} else if got.Answering.Ready || got.Answering.Reason == nil || *got.Answering.Reason != "model-gone" {
			t.Fatalf("new conversation must use role: %s", raw)
		}
	}
}

func TestHarnessStatusParams(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"sessionId":"session-1"}`} {
		if msg := validateHarnessStatusRaw(json.RawMessage(raw)); msg != "" {
			t.Fatalf("%s: %s", raw, msg)
		}
	}
	for _, raw := range []string{`[]`, `{"sessionId":" "}`, `{"sessionId":null}`, `{"sessionId":2}`, `{"paneId":"untrusted"}`} {
		if msg := validateHarnessStatusRaw(json.RawMessage(raw)); msg == "" {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestHarnessStatus_DTOConformsToContract(t *testing.T) {
	for _, dto := range []harnessStatusResult{
		{Answering: harnessAnswering{Reason: strPtr("unavailable")}, Message: strPtr("Choose an agent in Settings")},
		{Answering: harnessAnswering{Ready: true, AgentID: strPtr("fixture"), Model: strPtr("chosen")}},
	} {
		validateJSON(t, loadSchema(t, "agent.status.schema.json"), mustMarshal(dto), "harness status DTO")
	}
}

func TestHarnessStatus_OverTheWireConformsToContract(t *testing.T) {
	for _, state := range []agentharness.ReadinessState{agentharness.Ready, agentharness.NotInstalled, agentharness.NotConfigured} {
		t.Run(string(state), func(t *testing.T) {
			_, logger := logtest.New(t)
			backend := &statusHarness{readiness: agentharness.Readiness{State: state, Message: "Check agent settings"}}
			ws := NewWSServer(logger, newRegWithStub(logger), WithAgentHarness("fixture", backend), WithProfileRepository(profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))))
			if err := ws.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws.Stop(context.Background()) }()
			conn := connectWS(t, ws)
			defer func() { _ = conn.Close() }()
			raw := jsonrpcCall(t, conn, "agent.status", nil)
			var env struct {
				Error  *RPCError       `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Error != nil {
				t.Fatalf("status %s (%v)", raw, err)
			}
			validateJSON(t, loadSchema(t, "agent.status.schema.json"), env.Result, "harness status socket")
			var got harnessStatusResult
			if err := json.Unmarshal(env.Result, &got); err != nil {
				t.Fatal(err)
			}
			if got.Answering.Ready != (state == agentharness.Ready) || got.Answering.AgentID == nil || *got.Answering.AgentID != "fixture" {
				t.Fatalf("status %s", raw)
			}
		})
	}
}

func TestHarnessStatusChosenAndRemovedModelOverTheWire(t *testing.T) {
	store := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.AssignRole(profile.RoleAssignment{Role: profile.RoleAnswering, AgentID: "fixture", Model: "chosen"}); err != nil {
		t.Fatal(err)
	}
	backend := &statusHarness{readiness: agentharness.Readiness{State: agentharness.Ready}, models: []agentharness.Model{{ID: "chosen"}}}
	_, logger := logtest.New(t)
	ws := NewWSServer(logger, newRegWithStub(logger), WithAgentHarness("fixture", backend), WithProfileRepository(store))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	for _, removed := range []bool{false, true} {
		if removed {
			backend.models = nil
		}
		raw := jsonrpcCall(t, conn, "agent.status", nil)
		var env struct {
			Result harnessStatusResult `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		got := env.Result
		if !removed && (!got.Answering.Ready || got.Answering.Model == nil || *got.Answering.Model != "chosen") {
			t.Fatalf("chosen: %s", raw)
		}
		if removed && (got.Answering.Model == nil || *got.Answering.Model != "chosen" || got.Answering.Ready || got.Answering.Reason == nil || *got.Answering.Reason != "model-gone" || got.Message == nil) {
			t.Fatalf("removed: %s", raw)
		}
	}
}
