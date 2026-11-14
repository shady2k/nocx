package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
)

func roleCall(t *testing.T, h *endpointHarness, method string, params map[string]any) json.RawMessage {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, method, params)
	var env struct {
		Error  *RPCError       `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error != nil || env.Result == nil {
		t.Fatalf("%s: %s (%v)", method, raw, err)
	}
	return env.Result
}

func TestRoles_PerAgentChoicesAndClearOverTheWire(t *testing.T) {
	h := newEndpointHarness(t)
	for agent, model := range map[string]string{"pi": "one", "omp": "two"} {
		roleCall(t, h, "roles.assign", map[string]any{"agentId": agent, "role": "answering", "model": model})
	}
	for agent, want := range map[string]string{"pi": "one", "omp": "two"} {
		raw := roleCall(t, h, "roles.list", map[string]any{"agentId": agent})
		var got rolesListResponse
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.AgentID != agent || len(got.Roles) != len(profile.AllRoles()) || got.Roles[0].Model == nil || *got.Roles[0].Model != want {
			t.Fatalf("%s: %s", agent, raw)
		}
	}
	raw := roleCall(t, h, "roles.assign", map[string]any{"agentId": "pi", "role": "answering", "model": nil})
	var got rolesListResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Roles[0].Model != nil {
		t.Fatalf("clear: %s", raw)
	}
}

func TestRolesList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "roles.list.schema.json")
	for _, as := range [][]profile.RoleAssignment{nil, {{Role: profile.RoleAnswering, AgentID: "pi", Model: "chosen"}}} {
		validateJSON(t, schema, mustMarshal(rolesListResponse{AgentID: "pi", Roles: wireRoles("pi", as)}), "role DTO")
	}
}

func TestRoles_OverTheWireConformsToContract(t *testing.T) {
	h := newEndpointHarness(t)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"roles.list", map[string]any{"agentId": "pi"}},
		{"roles.assign", map[string]any{"agentId": "pi", "role": "answering", "model": "chosen"}},
		{"roles.assign", map[string]any{"agentId": "pi", "role": "answering", "model": nil}},
	} {
		raw := roleCall(t, h, tc.method, tc.params)
		validateJSON(t, loadSchema(t, tc.method+".schema.json"), raw, tc.method)
	}
}

func TestRolesContractRejectsOldEndpointAndDefaultShape(t *testing.T) {
	schema := loadSchema(t, "roles.list.schema.json")
	for _, raw := range []string{`{"agentId":"pi","roles":[],"default":null}`, `{"roles":[]}`, `{"agentId":"pi","roles":[{"role":"answering","model":null,"endpointId":null}]}`} {
		if err := validateJSONErr(schema, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
