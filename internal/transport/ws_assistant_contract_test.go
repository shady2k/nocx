package transport

// The assistant wire shapes (nocx-edio): agent.status and endpoints.probe,
// pinned by their schemas. The probe result shape is declared ONCE in
// endpoints.probe.schema.json and referenced cross-file by agent.status's
// lastProbe — one concept, one owner (contracts/README.md, AD-8). The
// schema-level contract is proven the same way every other domain proves
// it: the DTO marshals to something the schema accepts, and the REAL result
// off the real socket satisfies it.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
)

func TestAgentStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.status.schema.json")
	probe := &assistant.ProbeResult{
		EndpointName: "Local",
		Model:        "qwen3",
		Kind:         assistant.ProbeModel,
		OK:           true,
		ElapsedMS:    1234,
		At:           time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
	cred := func(s string) *string { return &s }
	cases := map[string]agentStatusResult{
		"populated": {
			EndpointConfigured: true,
			Credential:         cred("resolvable"),
			LastProbe:          probe,
		},
		"nothing configured": {
			EndpointConfigured: false,
			Credential:         nil,
			LastProbe:          nil,
		},
		"failed probe": {
			EndpointConfigured: true,
			Credential:         cred("sealed"),
			LastProbe: &assistant.ProbeResult{
				EndpointName: "Local",
				Model:        "qwen3",
				Kind:         assistant.ProbeModel,
				OK:           false,
				Error:        "dial tcp: connection refused",
				ElapsedMS:    5,
				At:           time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
			},
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(dto), "agent.status DTO")
		})
	}
}

func TestEndpointsProbe_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "endpoints.probe.schema.json")
	cases := map[string]assistant.ProbeResult{
		"ok": {
			EndpointName: "Local",
			Model:        "qwen3",
			Kind:         assistant.ProbeModel,
			OK:           true,
			ElapsedMS:    1234,
			At:           time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		},
		"failed": {
			EndpointName: "Local",
			Model:        "qwen3",
			Kind:         assistant.ProbeModel,
			OK:           false,
			Error:        "dial tcp: connection refused",
			ElapsedMS:    5,
		},
		"connection": {
			EndpointName: "Local",
			Kind:         assistant.ProbeConnection,
			OK:           true,
			Models:       []string{"qwen3", "llama3"},
			ElapsedMS:    12,
			At:           time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(dto), "endpoints.probe DTO")
		})
	}
}

// TestAssistant_OverTheWireConformToContract validates the REAL results off
// the real socket: agent.status with every field populated (including
// lastProbe, via a preceding endpoints.probe), and endpoints.probe's own
// result. A test that validates a payload it built itself proves the DTO is
// well-formed, not that the server sends it.
func TestAssistant_OverTheWireConformToContract(t *testing.T) {
	statusSchema := loadSchema(t, "agent.status.schema.json")
	testSchema := loadSchema(t, "endpoints.probe.schema.json")

	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			return assistant.ProbeResult{
				EndpointName: p.Name,
				Model:        p.Model,
				Kind:         probeKindFor(p),
				OK:           true,
				ElapsedMS:    42,
				At:           time.Now(),
			}, nil
		},
	})
	h.setupAndUnseal()
	h.createEndpoint(t, testEndpointParams())

	testRaw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk", "model": "qwen3",
	})
	var testEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(testRaw, &testEnv); err != nil {
		t.Fatalf("test unmarshal: %v", err)
	}
	if testEnv.Error != nil {
		t.Fatalf("endpoints.probe: %+v", testEnv.Error)
	}
	validateJSON(t, testSchema, testEnv.Result, "endpoints.probe result (real socket)")

	statusRaw := jsonrpcCall(t, h.conn, "agent.status", nil)
	var statusEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(statusRaw, &statusEnv); err != nil {
		t.Fatalf("status unmarshal: %v", err)
	}
	if statusEnv.Error != nil {
		t.Fatalf("agent.status: %+v", statusEnv.Error)
	}
	validateJSON(t, statusSchema, statusEnv.Result, "agent.status result (real socket)")
}
