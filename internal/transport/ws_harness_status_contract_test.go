package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
)

type unavailableStatusHarness struct {
	statusHarness
	catalogueErr error
}

func (h *unavailableStatusHarness) Models(context.Context) ([]agentharness.Model, error) {
	return nil, h.catalogueErr
}

func TestHarnessStatusReadinessCatalogueFailure_OverTheWireConformsToContract(t *testing.T) {
	for _, state := range []agentharness.ReadinessState{agentharness.NotInstalled, agentharness.NotConfigured, agentharness.Ready} {
		t.Run(string(state), func(t *testing.T) {
			_, logger := logtest.New(t)
			backend := &unavailableStatusHarness{statusHarness: statusHarness{readiness: agentharness.Readiness{State: state, Message: "Fix the agent installation or credentials"}}, catalogueErr: errors.New("catalogue unavailable")}
			ws := NewWSServer(logger, newRegWithStub(logger), WithAgentHarness("fixture", backend), WithProfileRepository(profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))))
			if err := ws.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws.Stop(context.Background()) }()
			conn := connectWS(t, ws)
			defer func() { _ = conn.Close() }()
			raw := jsonrpcCall(t, conn, "agent.status", nil)
			var envelope struct {
				Result json.RawMessage `json:"result"`
				Error  *RPCError       `json:"error"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error != nil {
				t.Fatalf("status %s: %v", raw, err)
			}
			validateJSON(t, loadSchema(t, "agent.status.schema.json"), envelope.Result, "catalogue failure status")
			var got harnessStatusResult
			if err := json.Unmarshal(envelope.Result, &got); err != nil {
				t.Fatal(err)
			}
			reason := "unavailable"
			if state == agentharness.NotInstalled {
				reason = "not-installed"
			}
			if state == agentharness.NotConfigured {
				reason = "not-configured"
			}
			if got.Answering.Ready || got.Answering.Reason == nil || *got.Answering.Reason != reason {
				t.Fatalf("expected %s: %s", reason, raw)
			}
			if state != agentharness.Ready && (got.Message == nil || *got.Message != backend.readiness.Message) {
				t.Fatalf("lost connector diagnostic: %s", raw)
			}
		})
	}
}

// A chosen model can authenticate even when the agent's native default cannot.
type chosenStatusHarness struct{ statusHarness }

func (h *chosenStatusHarness) ReadinessForModel(_ context.Context, model string) agentharness.Readiness {
	if model == "chosen" {
		return agentharness.Readiness{State: agentharness.Ready}
	}
	return h.readiness
}

func TestHarnessStatusChosenModelReadiness_OverTheWireConformsToContract(t *testing.T) {
	store := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.AssignRole(profile.RoleAssignment{Role: profile.RoleAnswering, AgentID: "fixture", Model: "chosen"}); err != nil {
		t.Fatal(err)
	}
	backend := &chosenStatusHarness{statusHarness{readiness: agentharness.Readiness{State: agentharness.NotConfigured, Message: "Default credentials are invalid"}, models: []agentharness.Model{{ID: "chosen"}}}}
	_, logger := logtest.New(t)
	ws := NewWSServer(logger, newRegWithStub(logger), WithAgentHarness("fixture", backend), WithProfileRepository(store))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	raw := jsonrpcCall(t, conn, "agent.status", nil)
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error != nil {
		t.Fatalf("status %s: %v", raw, err)
	}
	validateJSON(t, loadSchema(t, "agent.status.schema.json"), envelope.Result, "chosen model readiness")
	var got harnessStatusResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Answering.Ready || got.Answering.Model == nil || *got.Answering.Model != "chosen" || got.Answering.Reason != nil || got.Message != nil {
		t.Fatalf("chosen model must not use default credentials: %s", raw)
	}
}

func TestHarnessStatusReadiness_DTOConformsToContract(t *testing.T) {
	cases := map[string]harnessStatusResult{
		"no harness":          {Answering: harnessAnswering{Reason: strPtr("unavailable")}, Message: strPtr("Choose an available agent in Settings")},
		"invalid credentials": {Answering: harnessAnswering{AgentID: strPtr("fixture"), Reason: strPtr("not-configured")}, Message: strPtr("The agent's credentials are invalid")},
		"agent default":       {Answering: harnessAnswering{Ready: true, AgentID: strPtr("fixture")}},
		"chosen model":        {Answering: harnessAnswering{Ready: true, AgentID: strPtr("fixture"), Model: strPtr("chosen")}},
		"removed model":       {Answering: harnessAnswering{AgentID: strPtr("fixture"), Model: strPtr("chosen"), Reason: strPtr("model-gone")}, Message: strPtr("Choose an available model in Settings")},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, loadSchema(t, "agent.status.schema.json"), mustMarshal(dto), "readiness DTO")
		})
	}
}
