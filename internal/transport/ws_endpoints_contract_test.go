package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
)

// ── endpoints.* ─────────────────────────────────────────────────────────
//
// Four wire shapes (design §4.5.4, ADR-0030). The endpoint object is
// declared ONCE as $defs in contracts/endpoints.list.schema.json and
// referenced cross-file by create/update, the way git.status is referenced
// by the six git results that carry it — the loader registers every
// contract under its $id so those refs resolve.

func endpointDTO(credential *string) profile.EndpointDTO {
	return profile.EndpointDTO{
		ID:         "endpoint:custom:openai:1",
		Name:       "OpenAI",
		BaseURL:    "https://api.openai.com/v1",
		Schema:     profile.EndpointSchemaOpenAICompatible,
		Credential: credential,
		Models: []profile.EndpointModelDTO{
			{Name: "gpt-4o-mini"},
			{Name: "gpt-4o", Alias: ptrStr("gpt-4o (fast)")},
		},
		// Headers is never null on the wire (the contract declares an
		// array); a direct-built DTO must say so explicitly.
		Headers: []profile.EndpointHeaderDTO{},
	}
}

func ptrStr(s string) *string { return &s }

// The DTO's own conformance: field tags, null-vs-absent for the nullable
// credential and alias. Both interesting shapes — a set credential and a
// keyless one — must marshal to something the schema accepts.
func TestEndpointsList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "endpoints.list.schema.json")
	row := "secrow:0123456789abcdef"
	withHeaders := endpointDTO(&row)
	withHeaders.Headers = []profile.EndpointHeaderDTO{
		{Name: "HTTP-Referer", Value: ptrStr("nocx")},
		{Name: "api-key", Secret: &row},
	}
	cases := map[string]endpointsListResponse{
		"with credential": {Endpoints: []profile.EndpointDTO{endpointDTO(&row)}},
		"keyless":         {Endpoints: []profile.EndpointDTO{endpointDTO(nil)}},
		"with headers":    {Endpoints: []profile.EndpointDTO{withHeaders}},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(dto), "endpoints.list DTO")
		})
	}
}

// The create and update results carry the same endpoint object; the delete
// result is the empty object — the schema pins that shape.
func TestEndpointsCreateUpdate_DTOConformsToContract(t *testing.T) {
	createSchema := loadSchema(t, "endpoints.create.schema.json")
	updateSchema := loadSchema(t, "endpoints.update.schema.json")
	row := "secrow:0123456789abcdef"
	validateJSON(t, createSchema, mustMarshal(endpointResultResponse{Endpoint: endpointDTO(&row)}), "endpoints.create DTO")
	validateJSON(t, updateSchema, mustMarshal(endpointResultResponse{Endpoint: endpointDTO(nil)}), "endpoints.update DTO")
}

func TestEndpointsDelete_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "endpoints.delete.schema.json")
	validateJSON(t, schema, mustMarshal(struct{}{}), "endpoints.delete DTO")
}

// The real results off the real socket — the assertion that would have
// caught a handler not sending what the DTO could have. Nothing here names
// a field: the schema's additionalProperties:false plus required makes the
// key set exact in both directions.
func TestEndpoints_OverTheWireConformToContract(t *testing.T) {
	listSchema := loadSchema(t, "endpoints.list.schema.json")
	createSchema := loadSchema(t, "endpoints.create.schema.json")
	updateSchema := loadSchema(t, "endpoints.update.schema.json")
	deleteSchema := loadSchema(t, "endpoints.delete.schema.json")

	h := newEndpointHarness(t)
	h.setupAndUnseal()

	createParams := endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123")
	createParams["headers"] = []map[string]any{
		{"name": "HTTP-Referer", "value": "nocx", "secret": nil},
	}
	createRaw := jsonrpcCall(t, h.conn, "endpoints.create", createParams)
	var createEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(createRaw, &createEnv); err != nil {
		t.Fatalf("create unmarshal: %v", err)
	}
	if createEnv.Error != nil {
		t.Fatalf("endpoints.create: %+v", createEnv.Error)
	}
	validateJSON(t, createSchema, createEnv.Result, "endpoints.create result (real socket)")

	// The update and delete name the MINTED id, never a hardcoded one.
	var created struct {
		Endpoint struct {
			ID string `json:"id"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal(createEnv.Result, &created); err != nil {
		t.Fatalf("create result unmarshal: %v", err)
	}
	if created.Endpoint.ID == "" {
		t.Fatal("create result must carry the minted endpoint id")
	}
	epID := created.Endpoint.ID

	listRaw := jsonrpcCall(t, h.conn, "endpoints.list", nil)
	var listEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(listRaw, &listEnv); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	validateJSON(t, listSchema, listEnv.Result, "endpoints.list result (real socket)")

	// Update with no key: the credential must survive as a row handle, and
	// the custom headers survive the full-replace update (the literal row
	// is re-sent).
	updateParams := endpointParams("OpenAI EU", "https://api.eu.openai.com/v1", "")
	updateParams["id"] = epID
	updateParams["headers"] = []map[string]any{
		{"name": "HTTP-Referer", "value": "nocx", "secret": nil},
	}
	updateRaw := jsonrpcCall(t, h.conn, "endpoints.update", updateParams)
	var updateEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(updateRaw, &updateEnv); err != nil {
		t.Fatalf("update unmarshal: %v", err)
	}
	if updateEnv.Error != nil {
		t.Fatalf("endpoints.update: %+v", updateEnv.Error)
	}
	validateJSON(t, updateSchema, updateEnv.Result, "endpoints.update result (real socket)")

	delRaw := jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": epID})
	var delEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(delRaw, &delEnv); err != nil {
		t.Fatalf("delete unmarshal: %v", err)
	}
	if delEnv.Error != nil {
		t.Fatalf("endpoints.delete: %+v", delEnv.Error)
	}
	validateJSON(t, deleteSchema, delEnv.Result, "endpoints.delete result (real socket)")
}
