package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/log/logtest"
)

type catalogueStub struct {
	refresh bool
	err     error
	rows    []agentharness.AgentCatalogue
}

func (s *catalogueStub) Catalogue(_ context.Context, refresh bool) ([]agentharness.AgentCatalogue, error) {
	s.refresh = refresh
	return s.rows, s.err
}

func TestAgentRecordsCatalogue_OverTheWireConformsToContract(t *testing.T) {
	stub := &catalogueStub{rows: []agentharness.AgentCatalogue{{ID: "fixture", Readiness: agentharness.Readiness{State: agentharness.Ready, Message: "Ready"}, Models: []agentharness.Model{{ID: "fake/model", Name: "Fixture model"}}}}}
	_, logger := logtest.New(t)
	ws := NewWSServer(logger, newRegWithStub(logger), WithAgentCatalogue(stub))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	raw := jsonrpcCall(t, conn, "agentRecords.catalogue", map[string]any{"refresh": true})
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil {
		t.Fatalf("catalogue: %+v", envelope.Error)
	}
	validateJSON(t, loadSchema(t, "agentRecords.catalogue.schema.json"), envelope.Result, "catalogue wire")
	var result agentRecordsCatalogueResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !stub.refresh || len(result.Agents) != 1 || result.Agents[0].Readiness.State != agentharness.Ready || len(result.Agents[0].Models) != 1 || result.Agents[0].Models[0].ID != "fake/model" {
		t.Fatalf("refresh=%v result=%+v", stub.refresh, result)
	}
	for _, rows := range [][]agentharness.AgentCatalogue{
		{{ID: "fixture", Readiness: agentharness.Readiness{State: agentharness.NotConfigured, Message: "Configure agent"}}},
		nil,
	} {
		stub.rows = rows
		raw = jsonrpcCall(t, conn, "agentRecords.catalogue", map[string]any{})
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error != nil {
			t.Fatalf("catalogue: %+v", envelope.Error)
		}
		validateJSON(t, loadSchema(t, "agentRecords.catalogue.schema.json"), envelope.Result, "unready/empty wire")
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatal(err)
		}
		if stub.refresh || len(result.Agents) != len(rows) {
			t.Fatalf("default refresh or empty agents: %s", raw)
		}
		if len(rows) > 0 && (result.Agents[0].Readiness.State != agentharness.NotConfigured || len(result.Agents[0].Models) != 0) {
			t.Fatalf("unready result: %s", raw)
		}
	}
	stub.err = errors.New("catalogue unavailable")
	raw = jsonrpcCall(t, conn, "agentRecords.catalogue", map[string]any{})
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32000 {
		t.Fatalf("failure hidden: %s", raw)
	}
}

func TestAgentRecordsCatalogue_DTOConformsToContract(t *testing.T) {
	for _, state := range []agentharness.ReadinessState{agentharness.Ready, agentharness.NotConfigured, agentharness.NotInstalled} {
		raw, err := json.Marshal(agentRecordsCatalogueResult{Agents: []agentCatalogueRow{{ID: "fixture", Readiness: agentReadinessDTO{State: state, Message: "Check agent settings"}, Models: []agentModelDTO{}}}})
		if err != nil {
			t.Fatal(err)
		}
		validateJSON(t, loadSchema(t, "agentRecords.catalogue.schema.json"), raw, string(state))
	}
	raw, err := json.Marshal(agentRecordsCatalogueResult{Agents: []agentCatalogueRow{}})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "agentRecords.catalogue.schema.json"), raw, "empty")
}

func TestAgentRecordsCatalogueUnavailableAndInvalidParams(t *testing.T) {
	_, logger := logtest.New(t)
	ws := NewWSServer(logger, newRegWithStub(logger))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	for _, test := range []struct {
		params map[string]any
		code   int
	}{
		{map[string]any{}, -32601},
		{map[string]any{"refresh": nil}, -32602},
		{map[string]any{"extra": true}, -32602},
	} {
		raw := jsonrpcCall(t, conn, "agentRecords.catalogue", test.params)
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != test.code {
			t.Fatalf("params=%v response=%s", test.params, raw)
		}
	}
}
