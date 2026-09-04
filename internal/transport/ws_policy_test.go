package transport

// policy.get / policy.set over the real socket: the ONE global agent policy
// the run grants are minted from (ADR-0020 §7 as amended 2026-08-16,
// accepted). The tool-name rule is asserted by trying here, at the wire: a
// policy that names a tool is an invalid-params error, and there is no other
// vocabulary in which to express one.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
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
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
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

// TestPolicySet_PreservesStoredRulesWhenRowsOnlyPayloadOmitsThem proves the
// document owner protects standing answers from a forgetful matrix caller:
// policy.set carries only rows, then a fresh store reads the persisted rule
// back unchanged. It is the store's own invariant, and it stays asserted even
// though the wire no longer tests it — a matrix write may not name rules at
// all now, so this is the belt behind that braces.
//
// The rule is seeded through the store's one-rule seam rather than by handing
// SetPolicy a literal. A literal has no id and no source, and the document is
// normalised on the way back IN (ParseEffectPolicy mints the id an operator
// did not write), so comparing the reload against the literal compared a rule
// with the same rule after the gate had finished with it — and that assertion
// had been red since the provenance fields landed.
func TestPolicySet_PreservesStoredRulesWhenRowsOnlyPayloadOmitsThem(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	const name = "agent-policy.json"
	store := assistant.NewGlobalPolicyStore(doc, name)
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: content.EffectRow{Decision: content.DecisionAsk},
	}); err != nil {
		t.Fatalf("seed matrix: %v", err)
	}
	rule, err := store.SetRule(content.InvocationRule{
		Selector: content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision: content.DecisionPermit,
	})
	if err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	h := newAskHarnessWithOpts(t, mustClient(t), WithAgentPolicy(store))

	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": map[string]any{
			"observe": map[string]any{
				"decision": "permit",
				"scopes":   []any{},
			},
		},
	})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if envelope.Error != nil {
		t.Fatalf("policy.set error: %+v", envelope.Error)
	}

	reloaded := assistant.NewGlobalPolicyStore(doc, name)
	if got := reloaded.Policy().Rules; !reflect.DeepEqual(got, []content.InvocationRule{rule}) {
		t.Fatalf("reloaded rules = %+v, want the seeded rule preserved", got)
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
// seven rows and the live set includes every class carried by a declaration.
// session.run carries delegate in addition to the other reachable classes.
// It cannot know which, so policy.get says — and it says what the DECLARATION
// TABLE carries, read off the real socket rather than off a payload this test
// built. The harness names the seam exactly as the composition root does, so
// the value under test is production's, not one invented for the test.
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
	want := []content.Effect{content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive, content.EffectCrossBoundary, content.EffectDelegate}
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
// The expected wire value includes every effect carried by the declarations.
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
	if string(live) != `["observe","mutate-reversible","mutate-destructive","cross-boundary","delegate"]` {
		t.Fatalf("live bytes = %s, want [\"observe\",\"mutate-reversible\",\"mutate-destructive\",\"cross-boundary\",\"delegate\"]", live)
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

// TestPolicySet_DoesNotCarryRulesSoAConcurrentAnswerSurvives is the
// regression of nocx-39bly, written as the sequence it actually is rather
// than as the shape of the patch that used to hide it.
//
// The page reads the document. A rule is written through the store by
// another caller — that caller is the approval prompt, and it is why no
// merge on the READ side can help: the page's copy is stale by construction.
// The page then saves the matrix it read, and a renderer serialising an empty
// rule list sends `rules: []`, which is not absent and which the old
// nil-guard in SetPolicy could not see.
//
// The fix is that policy.set is a MATRIX write: a document naming rules is
// refused at the wire, and the sentence says where one rule goes instead.
func TestPolicySet_DoesNotCarryRulesSoAConcurrentAnswerSurvives(t *testing.T) {
	h, store := newPolicyHarness(t)

	// The page reads.
	read := jsonrpcCall(t, h.conn, "policy.get", nil)
	var got struct {
		Result policyResult     `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(read, &got); err != nil {
		t.Fatalf("policy.get %s: %v", read, err)
	}
	if got.Error != nil {
		t.Fatalf("policy.get error: %+v", got.Error)
	}
	if len(got.Result.Policy.Rules) != 0 {
		t.Fatalf("seeded rules = %+v, want none", got.Result.Policy.Rules)
	}

	// The prompt writes a standing answer, one rule at a time.
	saved, err := store.SetRule(content.InvocationRule{
		Selector: content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision: content.DecisionPermit,
		Source:   content.SourceAnswered,
	})
	if err != nil {
		t.Fatalf("the prompt could not save its standing answer: %v", err)
	}

	// The page saves the matrix it read a moment ago, rules and all — which
	// for a renderer holding an empty list is `rules: []`.
	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": map[string]any{
			"observe": map[string]any{"decision": "permit", "scopes": []any{}},
			"rules":   []any{},
		},
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("policy.set naming rules = %s, want -32602: the matrix write may not carry rules", raw)
	}
	if !strings.Contains(env.Error.Message, "policy.setRule") {
		t.Fatalf("policy.set refusal = %q, want it to name policy.setRule", env.Error.Message)
	}

	// And the answer the person gave is still there.
	rules := store.Policy().Rules
	if len(rules) != 1 || rules[0].ID != saved.ID {
		t.Fatalf("rules after the page's save = %+v, want the prompt's %q intact", rules, saved.ID)
	}
}

// ── policy.setRule / policy.forgetRule ────────────────────────────────────

// setRule drives policy.setRule over the real socket and answers with the
// result and the error, so each test asserts the one it is about.
func setRule(t *testing.T, h *askHarness, rule map[string]any) (policySetRuleResult, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "policy.setRule", map[string]any{"rule": rule})
	var env struct {
		Result policySetRuleResult `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.setRule %s: %v", raw, err)
	}
	return env.Result, env.Error
}

func forgetRule(t *testing.T, h *askHarness, id string) (policyForgetRuleResult, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "policy.forgetRule", map[string]any{"id": id})
	var env struct {
		Result policyForgetRuleResult `json:"result"`
		Error  *jsonrpcErrorObj       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.forgetRule %s: %v", raw, err)
	}
	return env.Result, env.Error
}

// exactRuleParams is the wire form of a rule over one literal command line —
// what the renderer sends: what the rule says, and nothing about where it
// came from.
func exactRuleParams(command ...string) map[string]any {
	tokens := make([]any, 0, len(command))
	for _, token := range command {
		tokens = append(tokens, token)
	}
	return map[string]any{
		"selector": map[string]any{"exact": []any{tokens}},
		"decision": "permit",
	}
}

// seedTwoRules puts two rules and a non-default matrix in the store, through
// the store's own one-rule seam.
func seedTwoRules(t *testing.T, store *assistant.GlobalPolicyStore) []content.InvocationRule {
	t.Helper()
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
		},
		MutateDestructive: content.EffectRow{Decision: content.DecisionRefuse},
	}); err != nil {
		t.Fatalf("seed matrix: %v", err)
	}
	var seeded []content.InvocationRule
	for _, command := range [][]string{{"df", "-h"}, {"uname", "-a"}} {
		saved, err := store.SetRule(content.InvocationRule{
			Selector: content.InvocationSelector{Exact: [][]string{command}},
			Decision: content.DecisionPermit,
		})
		if err != nil {
			t.Fatalf("seed rule %v: %v", command, err)
		}
		seeded = append(seeded, saved)
	}
	return seeded
}

// TestPolicySetRule_AddsOneAndTouchesNothingElse — criterion 1 at the wire.
// A person writing a third rule keeps the two they had and the matrix they
// set; the whole document is not rewritten to add one object to it.
func TestPolicySetRule_AddsOneAndTouchesNothingElse(t *testing.T) {
	h, store := newPolicyHarness(t)
	seeded := seedTwoRules(t, store)
	before := store.Policy()

	got, rpcErr := setRule(t, h, exactRuleParams("free", "-m"))
	if rpcErr != nil {
		t.Fatalf("policy.setRule error: %+v", rpcErr)
	}
	if !got.Added {
		t.Fatalf("result = %+v, want added=true for a rule that was not there", got)
	}

	after := store.Policy()
	if len(after.Rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(after.Rules))
	}
	if after.Rules[0].ID != seeded[0].ID || after.Rules[1].ID != seeded[1].ID || after.Rules[2].ID != got.ID {
		t.Fatalf("rules = %+v, want the two seeded then %q, in document order", after.Rules, got.ID)
	}
	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	} {
		if after.DecisionFor(e) != before.DecisionFor(e) ||
			!reflect.DeepEqual(after.RowScopes(e), before.RowScopes(e)) {
			t.Fatalf("row %s changed when one rule was written", e)
		}
	}
}

// TestPolicySetRule_ReplacesByIDInPlace — criterion 2 at the wire.
func TestPolicySetRule_ReplacesByIDInPlace(t *testing.T) {
	h, store := newPolicyHarness(t)
	seeded := seedTwoRules(t, store)

	replacement := exactRuleParams("df", "-k")
	replacement["id"] = seeded[0].ID
	replacement["decision"] = "refuse"
	got, rpcErr := setRule(t, h, replacement)
	if rpcErr != nil {
		t.Fatalf("policy.setRule error: %+v", rpcErr)
	}
	if got.Added {
		t.Fatalf("result = %+v, want added=false: replacing is not adding", got)
	}
	if got.ID != seeded[0].ID {
		t.Fatalf("result id = %q, want the id it named %q", got.ID, seeded[0].ID)
	}

	rules := store.Policy().Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2 — the count does not grow on a replace", len(rules))
	}
	if rules[0].ID != seeded[0].ID || rules[0].Decision != content.DecisionRefuse {
		t.Fatalf("rules[0] = %+v, want the replacement in position 0", rules[0])
	}
	if rules[1].ID != seeded[1].ID {
		t.Fatalf("rules[1] = %q, want the untouched %q", rules[1].ID, seeded[1].ID)
	}
}

// TestPolicySetRule_TheIDIsServerAuthoritative — criterion 3, both halves,
// at the seam a renderer reaches. A rule with no id is accepted and the
// answer carries the minted one; a rule carrying an id that names nothing is
// -32602. A renderer may replace a rule it can SEE, and may not choose the
// identity of a new one (AD-7).
func TestPolicySetRule_TheIDIsServerAuthoritative(t *testing.T) {
	h, store := newPolicyHarness(t)

	got, rpcErr := setRule(t, h, exactRuleParams("df", "-h"))
	if rpcErr != nil {
		t.Fatalf("policy.setRule with no id: %+v", rpcErr)
	}
	if got.ID == "" {
		t.Fatalf("result = %+v, want the minted id — there is nowhere else to learn it", got)
	}
	if store.Policy().Rules[0].ID != got.ID {
		t.Fatalf("stored id = %q, want the answered %q", store.Policy().Rules[0].ID, got.ID)
	}

	invented := exactRuleParams("uname", "-a")
	invented["id"] = "a-name-the-renderer-chose"
	_, rpcErr = setRule(t, h, invented)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("policy.setRule with an invented id = %+v, want -32602", rpcErr)
	}
	if n := len(store.Policy().Rules); n != 1 {
		t.Fatalf("rules after the refusal = %d, want the 1 that was there", n)
	}
}

// TestPolicyForgetRule_RemovesOneAndUnknownIsNotAnError — criterion 4 at the
// wire. The rest of the document survives, and forgetting what is already
// gone succeeds saying so.
func TestPolicyForgetRule_RemovesOneAndUnknownIsNotAnError(t *testing.T) {
	h, store := newPolicyHarness(t)
	seeded := seedTwoRules(t, store)
	before := store.Policy()

	got, rpcErr := forgetRule(t, h, seeded[0].ID)
	if rpcErr != nil {
		t.Fatalf("policy.forgetRule error: %+v", rpcErr)
	}
	if !got.Removed {
		t.Fatalf("result = %+v, want removed=true", got)
	}
	rules := store.Policy().Rules
	if len(rules) != 1 || rules[0].ID != seeded[1].ID {
		t.Fatalf("rules after the forget = %+v, want only %q", rules, seeded[1].ID)
	}
	for _, e := range []content.Effect{content.EffectObserve, content.EffectMutateDestructive} {
		if store.Policy().DecisionFor(e) != before.DecisionFor(e) {
			t.Fatalf("row %s changed when one rule was forgotten", e)
		}
	}

	again, rpcErr := forgetRule(t, h, seeded[0].ID)
	if rpcErr != nil {
		t.Fatalf("forgetting an unknown id raised %+v; the rule is already not there", rpcErr)
	}
	if again.Removed {
		t.Fatalf("forgetting an unknown id = %+v, want removed=false", again)
	}
}

// TestPolicySetRule_RefusesWhatTheContentGateRefuses: there is no second
// validator on the wire either. A hasFeature selector that permits is the
// asymmetry content owns, and it is refused as invalid params rather than
// accepted-and-ignored.
func TestPolicySetRule_RefusesWhatTheContentGateRefuses(t *testing.T) {
	h, store := newPolicyHarness(t)

	_, rpcErr := setRule(t, h, map[string]any{
		"selector": map[string]any{
			"hasFeature": map[string]any{"program": "tar", "feature": "writes-option-named-path"},
		},
		"decision": "permit",
	})
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("a permitting hasFeature rule = %+v, want -32602", rpcErr)
	}
	if n := len(store.Policy().Rules); n != 0 {
		t.Fatalf("rules after the refusal = %d, want none", n)
	}
}

// TestPolicySetRule_NoConfigurationPathNamesATool: ADR-0028 decision 4 holds
// at the new door too. A rule is over a command word in a parsed invocation
// and there is no key here in which a TOOL could be named — an unknown field
// is invalid params, so the vocabulary cannot be extended by sending one.
func TestPolicySetRule_NoConfigurationPathNamesATool(t *testing.T) {
	h, _ := newPolicyHarness(t)
	rule := exactRuleParams("df", "-h")
	rule["tool"] = "readScreen"
	_, rpcErr := setRule(t, h, rule)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("policy.setRule naming a tool = %+v, want -32602", rpcErr)
	}
}

// TestPolicyRuleMethods_UnwiredAreUnavailable: without the composition root's
// seam both answer method-not-found, the same way policy.get does.
func TestPolicyRuleMethods_UnwiredAreUnavailable(t *testing.T) {
	h := newAskHarness(t, mustClient(t)) // no WithAgentPolicy
	for method, params := range map[string]map[string]any{
		"policy.setRule":    {"rule": exactRuleParams("df", "-h")},
		"policy.forgetRule": {"id": "0123456789abcdef0123456789abcdef"},
	} {
		raw := jsonrpcCall(t, h.conn, method, params)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s %s: %v", method, raw, err)
		}
		if env.Error == nil || env.Error.Code != -32601 {
			t.Fatalf("%s without wiring = %s, want -32601", method, raw)
		}
	}
}

// TestPolicySetParams_SchemaAndValidatorBothRefuseRules keeps the two ends of
// the contract saying the same thing about the one key that matters here.
// The params contract declares the document may not name rules and the
// registered validator refuses it; a schema that allowed what the validator
// refuses would be a contract the renderer could satisfy and the server would
// not, which is the drift the whole directory exists to prevent.
func TestPolicySetParams_SchemaAndValidatorBothRefuseRules(t *testing.T) {
	schema := loadSchema(t, "policy.set.params.schema.json")
	spec, ok := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil))).methods["policy.set"]
	if !ok {
		t.Fatalf("policy.set is not registered")
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"policy":{"rules":[]}}`),
		json.RawMessage(`{"policy":{"observe":{"decision":"permit","scopes":[]},"rules":[]}}`),
	} {
		if err := validateJSONErr(schema, raw); err == nil {
			t.Fatalf("the params schema accepted a matrix write naming rules: %s", raw)
		}
		msg := spec.validate(raw)
		if msg == "" {
			t.Fatalf("the registered validator accepted a matrix write naming rules: %s", raw)
		}
		if !strings.Contains(msg, "policy.setRule") {
			t.Fatalf("refusal %q does not name policy.setRule, so it does not say where one rule goes", msg)
		}
	}
}
