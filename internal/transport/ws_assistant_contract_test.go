package transport

// The assistant wire shapes (nocx-edio): agent.status and endpoints.probe,
// pinned by their schemas. The probe result shape is declared ONCE in
// endpoints.probe.schema.json and referenced cross-file by agent.status's
// lastProbe — one concept, one owner (contracts/README.md, AD-8). The
// schema-level contract is proven the same way every other domain proves
// it: the DTO marshals to something the schema accepts, and the REAL result
// off the real socket satisfies it.

import (
	"bytes"
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
	name, model := "Local", "qwen3"
	cases := map[string]agentStatusResult{
		"populated": {
			EndpointConfigured: true,
			Credential:         cred("resolvable"),
			LastProbe:          probe,
			Answering:          answeringWire{Ready: true, Endpoint: &name, Model: &model},
		},
		"nothing configured": {
			EndpointConfigured: false,
			Credential:         nil,
			LastProbe:          nil,
			Answering:          answeringWire{Reason: reasonPtr(reasonNoEndpoints)},
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
			Answering: answeringWire{Ready: true, Endpoint: &name, Model: &model},
		},
	}
	// Every refusal rung (nocx-rikz5), each with credential and lastProbe
	// null: with no resolved endpoint there is no key the question is about.
	for _, reason := range []string{
		reasonNoEndpoints, reasonNoModels, reasonUnassigned,
		reasonEndpointGone, reasonModelGone, reasonUnavailable,
	} {
		cases["refusal: "+reason] = agentStatusResult{
			EndpointConfigured: reason != reasonNoEndpoints,
			Answering:          answeringWire{Reason: reasonPtr(reason)},
		}
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(dto), "agent.status DTO")
		})
	}
}

// What the contract must REFUSE. Without these the schema is theatre: an
// absent answering object, a key missing from inside it, a rung nobody named
// and an extra field are each a way the renderer reads "unknown" and renders
// nothing where a sentence belongs — which is the shape of defect that let
// vault.status declare defaultProvider it never sent.
func TestAgentStatus_ContractRefusesWhatItMustRefuse(t *testing.T) {
	schema := loadSchema(t, "agent.status.schema.json")
	const head = `{"endpointConfigured":true,"credential":null,"lastProbe":null`
	bad := map[string]string{
		"answering missing":       head + `}`,
		"answering null":          head + `,"answering":null}`,
		"ready missing":           head + `,"answering":{"reason":"unassigned","endpoint":null,"model":null}}`,
		"reason key absent":       head + `,"answering":{"ready":true,"endpoint":"e","model":"m"}}`,
		"a rung nobody named":     head + `,"answering":{"ready":false,"reason":"not-configured","endpoint":null,"model":null}}`,
		"an undeclared field":     head + `,"answering":{"ready":false,"reason":"unassigned","endpoint":null,"model":null,"why":"x"}}`,
		"an undeclared top field": head + `,"answering":{"ready":false,"reason":"unassigned","endpoint":null,"model":null},"ready":true}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if err := validateJSONErr(schema, []byte(raw)); err == nil {
				t.Fatalf("the contract accepted %s: %s", name, raw)
			}
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

	// The no-endpoints rung FIRST: the handler used to return early there,
	// which is exactly where the answering fact would have been skipped, so
	// it is asserted off the socket rather than only as a DTO.
	validateJSON(t, statusSchema, agentStatusOffTheWire(t, h),
		"agent.status no-endpoints result (real socket)")

	e := h.createEndpoint(t, testEndpointParams())

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

	// And the RESOLVED rung, where endpoint, model and credential are all
	// non-null — the shape the DTO case can only claim.
	h.mustSetDefault(e.ID, "qwen3")
	ready := agentStatusOffTheWire(t, h)
	validateJSON(t, statusSchema, ready, "agent.status ready result (real socket)")
	if !bytes.Contains(ready, []byte(`"ready":true`)) {
		t.Fatalf("status = %s, want a ready resolution off the socket", ready)
	}
}

// agentStatusOffTheWire calls agent.status over the socket and returns the
// raw result, failing on an RPC error: every state this method reports is a
// RESULT, including the ones a store failure produces.
func agentStatusOffTheWire(t *testing.T, h *assistantHarness) json.RawMessage {
	t.Helper()
	var env struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("status unmarshal: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("agent.status: %+v", env.Error)
	}
	return env.Result
}
