package transport

// policy.get / policy.set over the real socket: the ONE global agent policy
// the run grants are minted from (ADR-0020 §7 as amended 2026-08-16,
// accepted). The tool-name rule is asserted by trying here, at the wire: a
// policy that names a tool is an invalid-params error, and there is no other
// vocabulary in which to express one.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// newPolicyHarness builds a server with the composition root's policy seams
// wired the way app.go wires them: a real GlobalPolicyStore over a real
// DocumentStore, and the real live list off the tool declaration table. The
// store is returned so tests seed and read the value the mint uses — no type
// assertion on the server's seam.
func newPolicyHarness(t *testing.T) (*askHarness, *assistant.GlobalPolicyStore) {
	t.Helper()
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	return newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(store),
		WithLiveEffects(agenttools.LiveEffects()),
	), store
}

func mustClient(t *testing.T) assistant.Client {
	t.Helper()
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	return client
}

// TestPolicyGet_ReturnsTheEffectivePolicy checks the matrix travels whole:
// every row present with its effective decision (unstated rows ask), and the
// scope a person expressed comes back.
func TestPolicyGet_ReturnsTheEffectivePolicy(t *testing.T) {
	h, store := newPolicyHarness(t)
	var p content.EffectPolicy
	p.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/home"}},
	}
	p.MutateDestructive = content.EffectRow{Decision: content.DecisionRefuse}
	if err := store.SetPolicy(p); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	raw := jsonrpcCall(t, h.conn, "policy.get", nil)
	var env struct {
		Result policyResult `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.get response %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.get error: %+v (%s)", env.Error, raw)
	}
	if got := env.Result.Policy.DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("observe decision = %s, want permit", got)
	}
	if got := env.Result.Policy.DecisionFor(content.EffectMutateDestructive); got != content.DecisionRefuse {
		t.Fatalf("mutate-destructive decision = %s, want refuse", got)
	}
	if got := env.Result.Policy.DecisionFor(content.EffectDelegate); got != content.DecisionAsk {
		t.Fatalf("unstated delegate decision = %s, want ask", got)
	}
}

// TestPolicySet_PersistsAndTheRunMintSeesIt sets a finer-than-presets
// policy over the socket and asserts the persisted store — the value the
// next ask run's grant is minted from — carries it.
func TestPolicySet_PersistsAndTheRunMintSeesIt(t *testing.T) {
	h, store := newPolicyHarness(t)

	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": map[string]any{
			"observe": map[string]any{
				"decision": "permit",
				"scopes":   []any{map[string]any{"kind": "path", "id": "/home/me"}},
			},
		},
	})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
		OK    bool             `json:"ok"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if envelope.Error != nil {
		t.Fatalf("policy.set error: %+v", envelope.Error)
	}
	if got := store.Policy().DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("store policy decides observe = %s, want permit — the mint reads this value", got)
	}
	if got := store.Policy().DecisionFor(content.EffectCrossBoundary); got != content.DecisionAsk {
		t.Fatalf("unstated cross-boundary = %s, want ask", got)
	}
}

// TestPolicySet_NoConfigurationPathNamesATool drives the wire's own refusal:
// a policy that keys a row by a tool name, and one whose row carries a
// tool-kind scope, are invalid params — there is no set that sticks.
func TestPolicySet_NoConfigurationPathNamesATool(t *testing.T) {
	h, _ := newPolicyHarness(t)
	for _, params := range []map[string]any{
		{"policy": map[string]any{"readScreen": map[string]any{"decision": "permit"}}},
		{"policy": map[string]any{"observe": map[string]any{
			"decision": "permit",
			"scopes":   []any{map[string]any{"kind": "tool", "id": "readScreen"}},
		}}},
	} {
		raw := jsonrpcCall(t, h.conn, "policy.set", params)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("policy.set %s: %v", raw, err)
		}
		if env.Error == nil || env.Error.Code != -32602 {
			t.Fatalf("policy.set with a tool name (%v) = %s, want -32602 invalid params", params, raw)
		}
	}
}

// TestPolicyGet_UnwiredIsUnavailable: without the composition root's seam,
// the methods answer method-not-found — the state before a policy is named.
func TestPolicyGet_UnwiredIsUnavailable(t *testing.T) {
	h := newAskHarness(t, mustClient(t)) // no WithAgentPolicy
	raw := jsonrpcCall(t, h.conn, "policy.get", nil)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.get %s: %v", raw, err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Fatalf("policy.get without wiring = %s, want -32601", raw)
	}
}

// TestPolicyGet_ReportsWhichEffectClassesAreLive: the settings page draws
// seven rows and only two of them govern anything today. It cannot know
// which, so policy.get says — and it says what the DECLARATION TABLE carries,
// read off the real socket rather than off a payload this test built. The
// harness names the seam exactly as the composition root does, so the value
// under test is production's, not one invented for the test.
func TestPolicyGet_ReportsWhichEffectClassesAreLive(t *testing.T) {
	h, _ := newPolicyHarness(t)

	raw := jsonrpcCall(t, h.conn, "policy.get", nil)
	var env struct {
		Result policyResult     `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.get %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.get error: %+v (%s)", env.Error, raw)
	}
	want := []content.Effect{content.EffectObserve, content.EffectMutateDestructive}
	if !reflect.DeepEqual(env.Result.Live, want) {
		t.Fatalf("live = %v, want %v", env.Result.Live, want)
	}
	// And it is the registry's answer, not a copy of it that happens to
	// agree today: the handler must have no list of its own to drift.
	if !reflect.DeepEqual(env.Result.Live, agenttools.LiveEffects()) {
		t.Fatalf("live = %v, want the registry's %v", env.Result.Live, agenttools.LiveEffects())
	}
}

// TestPolicyGetLive_OverTheWireConformsToContract: the REAL result off the
// REAL socket satisfies the contract now that "live" is required and the
// result object is closed — and the bytes really do carry the key, which a
// decode into a struct with a nil-able slice would not have shown.
func TestPolicyGetLive_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "policy.get.schema.json")
	h, _ := newPolicyHarness(t)

	raw := jsonrpcCall(t, h.conn, "policy.get", nil)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.get %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.get error: %+v (%s)", env.Error, raw)
	}
	validateJSON(t, schema, env.Result, "policy.get result (real socket)")

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &keys); err != nil {
		t.Fatalf("result %s: %v", env.Result, err)
	}
	live, ok := keys["live"]
	if !ok {
		t.Fatalf("result carries no live key: %s", env.Result)
	}
	if string(live) != `["observe","mutate-destructive"]` {
		t.Fatalf("live bytes = %s, want [\"observe\",\"mutate-destructive\"]", live)
	}
}

// TestPolicyResult_LiveIsNeverANull: the contract says live is an array, and
// "never a null" is a property of the SHAPE, not a thing every construction
// site has to remember. A DTO built without a live list still marshals [] —
// the same defect the schemas first caught when providers marshalled as null
// (AGENTS.md rule 5).
func TestPolicyResult_LiveIsNeverANull(t *testing.T) {
	raw, err := json.Marshal(policyResult{Policy: content.EffectPolicy{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if got := string(keys["live"]); got != "[]" {
		t.Fatalf("live = %s, want []", got)
	}
	validateJSON(t, loadSchema(t, "policy.get.schema.json"), raw, "policy.get DTO with no live list")
}

// TestPolicyGet_UnnamedLiveEffectsIsAnEmptyListNotANull: a server whose root
// wired the policy but never named the live list. The contract has no null
// branch, so the answer is [] — every row reads as governing nothing, which
// is a degrade a person can SEE on the page rather than a silent claim that
// all seven govern something. The interval both ends: nil on the seam in,
// [] on the wire out.
func TestPolicyGet_UnnamedLiveEffectsIsAnEmptyListNotANull(t *testing.T) {
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	h := newAskHarnessWithOpts(t, mustClient(t), WithAgentPolicy(store)) // no WithLiveEffects

	raw := jsonrpcCall(t, h.conn, "policy.get", nil)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.get %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.get error: %+v (%s)", env.Error, raw)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &keys); err != nil {
		t.Fatalf("result %s: %v", env.Result, err)
	}
	if got := string(keys["live"]); got != "[]" {
		t.Fatalf("live with no seam = %s, want []", got)
	}
	validateJSON(t, loadSchema(t, "policy.get.schema.json"), env.Result, "policy.get result (live seam unnamed)")
}
