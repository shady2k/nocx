package transport

// The roles wire (bead nocx-e6kn2): roles.list and roles.assign over the
// real socket. The wire shape is declared once in contracts/roles.list.schema.json
// (referenced cross-file by roles.assign's result) and proven over the
// socket at the bottom of this file.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
)

// listRoles returns the roles table as roles.list sent it.
func (h *endpointHarness) listRoles(t *testing.T) []profile.RoleDTO {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "roles.list", nil)
	var env struct {
		Error  *struct{ Code int } `json:"error"`
		Result struct {
			Roles []profile.RoleDTO `json:"roles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("roles.list unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("roles.list: code %d\nraw: %s", env.Error.Code, raw)
	}
	return env.Result.Roles
}

func (h *endpointHarness) assignRole(t *testing.T, role string, endpointID, model *string) {
	t.Helper()
	params := map[string]any{"role": role}
	if endpointID != nil {
		params["endpointId"] = *endpointID
		params["model"] = *model
	}
	raw := jsonrpcCall(t, h.conn, "roles.assign", params)
	if isErrorResponse(t, raw) {
		t.Fatalf("roles.assign: %s", raw)
	}
}

// The closed set is visible even with NOTHING configured: an unassigned
// role is a null row, never an absent row — the state the role's failure
// must be visible as.
func TestRolesList_UnassignedRolesAreVisibleNullRows(t *testing.T) {
	h := newEndpointHarness(t)
	roles := h.listRoles(t)
	if len(roles) != 2 {
		t.Fatalf("roles.list = %+v, want exactly the two product roles", roles)
	}
	want := []profile.ModelRole{profile.RoleAnswering, profile.RoleClassifier}
	for i, r := range roles {
		if r.Role != want[i] {
			t.Errorf("roles[%d].role = %q, want %q (product order)", i, r.Role, want[i])
		}
		if r.EndpointID != nil || r.Model != nil {
			t.Errorf("roles[%d] = %q assigned, want null for an unassigned role", i, r.Role)
		}
	}
}

// The assignment a person makes in the product is what roles.list reports
// and what resolution will use.
func TestRolesAssign_ListsWhatWasAssigned(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))
	h.assignRole(t, "answering", &created.ID, strPtr("qwen3"))

	roles := h.listRoles(t)
	byRole := map[profile.ModelRole]profile.RoleDTO{}
	for _, r := range roles {
		byRole[r.Role] = r
	}
	ans := byRole[profile.RoleAnswering]
	if ans.EndpointID == nil || *ans.EndpointID != created.ID || ans.Model == nil || *ans.Model != "qwen3" {
		t.Fatalf("answering role = %+v, want the assigned endpoint+model", ans)
	}
	if cl := byRole[profile.RoleClassifier]; cl.EndpointID != nil {
		t.Errorf("classifier role = %+v, want it untouched (null)", cl)
	}
}

// A second assignment for the same role REPLACES the first: the role has
// exactly one (endpoint, model) pair.
func TestRolesAssign_SecondAssignmentReplacesTheFirst(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	first := h.createEndpoint(t, endpointParams("First", "http://127.0.0.1:11434/v1", "sk-test-123"))
	second := h.createEndpoint(t, endpointParams("Second", "http://127.0.0.1:11434/v1", "sk-test-456"))
	h.assignRole(t, "answering", &first.ID, strPtr("qwen3"))
	h.assignRole(t, "answering", &second.ID, strPtr("local2"))

	var ans profile.RoleDTO
	for _, r := range h.listRoles(t) {
		if r.Role == profile.RoleAnswering {
			ans = r
		}
	}
	if ans.EndpointID == nil || *ans.EndpointID != second.ID || ans.Model == nil || *ans.Model != "local2" {
		t.Fatalf("answering role after reassignment = %+v, want the SECOND pair", ans)
	}
}

// The clear write (both fields empty) returns the role to the visible
// unassigned state — the failure the ask refuses on.
func TestRolesAssign_ClearReturnsTheRoleToUnassigned(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))
	h.assignRole(t, "answering", &created.ID, strPtr("qwen3"))
	h.assignRole(t, "answering", nil, nil)
	for _, r := range h.listRoles(t) {
		if r.Role == profile.RoleAnswering && r.EndpointID != nil {
			t.Fatalf("answering role after clear = %+v, want null", r)
		}
	}
}

func TestRolesAssign_RefusesAnUnknownRoleAndAHalfPair(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	for name, params := range map[string]map[string]any{
		"unknown role":           {"role": "invented", "endpointId": created.ID, "model": "qwen3"},
		"endpoint without model": {"role": "answering", "endpointId": created.ID},
		"model without endpoint": {"role": "answering", "model": "qwen3"},
		"missing role":           {"endpointId": created.ID, "model": "qwen3"},
	} {
		raw := jsonrpcCall(t, h.conn, "roles.assign", params)
		if !strings.Contains(string(raw), "-32602") {
			t.Errorf("%s: roles.assign = %s, want -32602", name, raw)
		}
	}
	// Nothing was stored by any refusal.
	if roles := h.listRoles(t); roles[0].EndpointID != nil {
		t.Fatalf("refused assigns stored state: %+v", roles)
	}
}

// ── wire shape (contracts/README.md row 2 and 3) ────────────────────────

func TestRolesList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "roles.list.schema.json")
	ep := "endpoint:custom:local:00000000000000000000000000000001"
	cases := map[string]rolesListResponse{
		"unassigned": {Roles: wireRoles(nil)},
		"assigned": {Roles: wireRoles([]profile.RoleAssignment{
			{Role: profile.RoleAnswering, EndpointID: ep, Model: "qwen3"},
		})},
		"all assigned": {Roles: wireRoles([]profile.RoleAssignment{
			{Role: profile.RoleAnswering, EndpointID: ep, Model: "qwen3"},
			{Role: profile.RoleClassifier, EndpointID: ep, Model: "gpt-4o-mini"},
		})},
		// The default is a field with two shapes, and BOTH are the DTO's
		// (bead nocx-rikz5): the chosen pair, and the null a fresh profile
		// reports. A case for only one of them is a case for the shape
		// that happened to be written first.
		"a default and nothing else": {
			Roles:   wireRoles(nil),
			Default: wireDefaultModel(profile.DefaultModel{EndpointID: ep, Model: "qwen3"}),
		},
		"a default beside an assignment": {
			Roles: wireRoles([]profile.RoleAssignment{
				{Role: profile.RoleAnswering, EndpointID: ep, Model: "qwen3"},
			}),
			Default: wireDefaultModel(profile.DefaultModel{EndpointID: ep, Model: "gpt-4o-mini"}),
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(dto), "roles.list DTO ("+name+")")
		})
	}
}

// The contract's teeth: a payload the DTO could never marshal, but a
// hand-rolled handler could, is REFUSED. Half a default names nothing, and
// a default is a required field — without both assertions the schema would
// accept a table that has quietly lost the pair.
func TestRolesList_ContractRefusesAHalfDefaultAndAMissingOne(t *testing.T) {
	schema := loadSchema(t, "roles.list.schema.json")
	table := `"roles":[{"role":"answering","endpointId":null,"model":null},` +
		`{"role":"classifier","endpointId":null,"model":null}]`
	for name, raw := range map[string]string{
		"endpoint without model": `{` + table + `,"default":{"endpointId":"e1"}}`,
		"model without endpoint": `{` + table + `,"default":{"model":"m-a"}}`,
		"a field nobody named":   `{` + table + `,"default":{"endpointId":"e1","model":"m-a","label":"x"}}`,
		"no default at all":      `{` + table + `}`,
	} {
		if err := validateJSONErr(schema, []byte(raw)); err == nil {
			t.Errorf("%s: the contract accepted %s, want a refusal", name, raw)
		}
	}
}

// The real results off the real socket — the assertion that would have
// caught a handler sending something the DTO could not.
func TestRoles_OverTheWireConformsToContract(t *testing.T) {
	listSchema := loadSchema(t, "roles.list.schema.json")
	assignSchema := loadSchema(t, "roles.assign.schema.json")

	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	listRaw := jsonrpcCall(t, h.conn, "roles.list", nil)
	var listEnv struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(listRaw, &listEnv); err != nil || listEnv.Error != nil {
		t.Fatalf("roles.list: %v\n%s", err, listRaw)
	}
	validateJSON(t, listSchema, listEnv.Result, "roles.list result (real socket)")

	assignRaw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": created.ID, "model": "qwen3",
	})
	var assignEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(assignRaw, &assignEnv); err != nil || assignEnv.Result == nil {
		t.Fatalf("roles.assign: %v\nraw: %s", err, assignRaw)
	}
	validateJSON(t, assignSchema, assignEnv.Result, "roles.assign result (real socket)")

	clearRaw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"role": "answering"})
	var clearEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(clearRaw, &clearEnv); err != nil {
		t.Fatalf("roles.assign (clear): %v", err)
	}
	validateJSON(t, assignSchema, clearEnv.Result, "roles.assign clear result (real socket)")

	// roles.setDefault returns the SAME shape — the table plus the default
	// it just wrote — so it is validated against the same contract (bead
	// nocx-rikz5). Validated in BOTH states: with a default chosen, and
	// after the clear that returns it to null.
	setRaw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "gpt-4o-mini",
	})
	var setEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(setRaw, &setEnv); err != nil || setEnv.Result == nil {
		t.Fatalf("roles.setDefault: %v\nraw: %s", err, setRaw)
	}
	validateJSON(t, listSchema, setEnv.Result, "roles.setDefault result (real socket)")
	if d := wireDefault(t, setEnv.Result); d == nil {
		t.Fatalf("roles.setDefault result carried no default: %s", setEnv.Result)
	}
	// And roles.list, whose default is now the populated shape rather than
	// the null the first assertion above saw.
	validateJSON(t, listSchema, h.rolesListRaw(t), "roles.list result with a default (real socket)")

	clearDefaultRaw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{"endpointId": "", "model": ""})
	var clearDefaultEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(clearDefaultRaw, &clearDefaultEnv); err != nil || clearDefaultEnv.Result == nil {
		t.Fatalf("roles.setDefault (clear): %v\nraw: %s", err, clearDefaultRaw)
	}
	validateJSON(t, listSchema, clearDefaultEnv.Result, "roles.setDefault clear result (real socket)")
}

// ── the default (bead nocx-rikz5) ───────────────────────────────────────

// rolesListRaw returns roles.list's whole result. The roles-only helper
// above cannot see the default at all — which is the shape of the defect
// this task exists to prevent: a reader that names only the field it
// already knows about reports nothing when a new one is missing.
func (h *endpointHarness) rolesListRaw(t *testing.T) json.RawMessage {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "roles.list", nil)
	var env struct {
		Error  *struct{ Code int } `json:"error"`
		Result json.RawMessage     `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("roles.list unmarshal: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("roles.list: code %d\nraw: %s", env.Error.Code, raw)
	}
	if env.Result == nil {
		t.Fatalf("roles.list returned no result: %s", raw)
	}
	return env.Result
}

// wireDefault decodes the default off a roles.list/roles.assign/
// roles.setDefault result. nil is the "nobody has chosen one" state.
func wireDefault(t *testing.T, result json.RawMessage) *profile.DefaultModel {
	t.Helper()
	var got struct {
		Default *profile.DefaultModel `json:"default"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode default: %v\nresult: %s", err, result)
	}
	return got.Default
}

// The default a person chooses on this surface is what roles.list reports
// back — the field the whole task exists for. A fresh profile has none.
func TestRolesSetDefault_IsReadBackByRolesList(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	fresh := h.rolesListRaw(t)
	if d := wireDefault(t, fresh); d != nil {
		t.Fatalf("a fresh profile reported default %+v, want null\nresult: %s", d, fresh)
	}

	setRaw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "gpt-4o-mini",
	})
	if isErrorResponse(t, setRaw) {
		t.Fatalf("roles.setDefault: %s", setRaw)
	}

	after := wireDefault(t, h.rolesListRaw(t))
	if after == nil || after.EndpointID != created.ID || after.Model != "gpt-4o-mini" {
		t.Fatalf("default read back as %+v, want %s/gpt-4o-mini", after, created.ID)
	}
}

// The write's own result carries the same table and the same default, so
// the surface adopts both from one answer and cannot render a default and
// a role table from two different moments.
func TestRolesSetDefault_ReturnsTheTableAndTheDefaultItJustWrote(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	raw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "gpt-4o-mini",
	})
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Result == nil {
		t.Fatalf("roles.setDefault: %v\nraw: %s", err, raw)
	}
	var got struct {
		Roles []profile.RoleDTO `json:"roles"`
	}
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("decode roles: %v", err)
	}
	if len(got.Roles) != 2 {
		t.Fatalf("roles.setDefault returned %d rows, want the closed role set", len(got.Roles))
	}
	if d := wireDefault(t, env.Result); d == nil || d.Model != "gpt-4o-mini" {
		t.Fatalf("roles.setDefault result default = %+v, want the pair it just wrote", d)
	}
}

// An endpoint id naming no endpoint is refused at the wire, and nothing is
// stored: a dangling default would break EVERY unassigned role at once,
// with nothing on screen naming which choice did it.
func TestRolesSetDefault_RefusesAnEndpointThatDoesNotExist(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	raw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": "endpoint:custom:ghost:00000000000000000000000000000009",
		"model":      "gpt-4o-mini",
	})
	if !strings.Contains(string(raw), "-32602") {
		t.Fatalf("roles.setDefault with a ghost endpoint = %s, want -32602", raw)
	}
	if d := wireDefault(t, h.rolesListRaw(t)); d != nil {
		t.Fatalf("a refused write stored %+v, want default null", d)
	}
}

// The empty pair is the CLEAR write: it returns every unassigned role to
// the visible "choose a model" failure rather than to another model.
func TestRolesSetDefault_TheEmptyPairClearsIt(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	if raw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "gpt-4o-mini",
	}); isErrorResponse(t, raw) {
		t.Fatalf("roles.setDefault: %s", raw)
	}
	clearRaw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{"endpointId": "", "model": ""})
	if isErrorResponse(t, clearRaw) {
		t.Fatalf("roles.setDefault (clear): %s", clearRaw)
	}
	if d := wireDefault(t, h.rolesListRaw(t)); d != nil {
		t.Fatalf("default after the clear = %+v, want null", d)
	}
}

// A half pair names nothing, so it is refused rather than stored as half a
// choice — the same shape rule roles.assign applies to an assignment.
func TestRolesSetDefault_RefusesAHalfPair(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	for name, params := range map[string]map[string]any{
		"endpoint without model": {"endpointId": created.ID, "model": ""},
		"model without endpoint": {"endpointId": "", "model": "gpt-4o-mini"},
	} {
		raw := jsonrpcCall(t, h.conn, "roles.setDefault", params)
		if !strings.Contains(string(raw), "-32602") {
			t.Errorf("%s: roles.setDefault = %s, want -32602", name, raw)
		}
	}
	if d := wireDefault(t, h.rolesListRaw(t)); d != nil {
		t.Fatalf("a refused half pair stored %+v, want default null", d)
	}
}

// A default names an endpoint AND a model, so a model the endpoint does not
// offer is refused at the wire with the SAME code the missing endpoint gets
// (-32602, invalid params), and nothing is stored. Starting from a default
// that is ALREADY SET is the point: the claim is "nothing was stored", and
// a store that was empty before cannot tell an untouched default from a
// cleared one.
func TestRolesSetDefault_RefusesAModelTheEndpointDoesNotOffer(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	created := h.createEndpoint(t, endpointParams("Local", "http://127.0.0.1:11434/v1", "sk-test-123"))

	if raw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "gpt-4o-mini",
	}); isErrorResponse(t, raw) {
		t.Fatalf("roles.setDefault (the valid one): %s", raw)
	}

	raw := jsonrpcCall(t, h.conn, "roles.setDefault", map[string]any{
		"endpointId": created.ID, "model": "a-model-this-endpoint-never-offered",
	})
	if !strings.Contains(string(raw), "-32602") {
		t.Fatalf("roles.setDefault with an invented model = %s, want -32602", raw)
	}
	d := wireDefault(t, h.rolesListRaw(t))
	if d == nil || d.EndpointID != created.ID || d.Model != "gpt-4o-mini" {
		t.Fatalf("after the refusal the default is %+v, want the original %s/gpt-4o-mini — a refused write stores nothing", d, created.ID)
	}
}
