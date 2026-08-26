package content_test

// The policy matrix — acceptance criteria of the ADR-0020 §7 amendment
// (2026-08-16, accepted), asserted at the vocabulary level:
//
//   - criterion 2: per effect class and per resource scope, a policy is
//     expressed and the decision follows it;
//   - criterion 3: no configuration path can express a rule over a tool name
//     (asserted by trying — top-level row keys and scope kinds alike);
//   - criterion 4: the three presets of the original §7 remain expressible in
//     the new form and decide exactly as they did;
//   - criterion 5: an unstated, empty or unparseable policy fails toward
//     asking, never toward permitting;
//   - criterion 6: the resolution order (workspace override, global default)
//     is stated once, in ResolvePolicy, and behaves.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestPresetsExpressAsMatricesAndDecideAsTheyAlwaysDid(t *testing.T) {
	// The preset matrices decide each effect exactly as the old preset
	// switch did: ask-every-time asks everything; ask-on-mutate permits
	// observe and asks the rest; autonomous permits everything.
	effects := []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	}
	cases := []struct {
		name    string
		policy  content.EffectPolicy
		observe content.Decision
		other   content.Decision // every effect except observe
	}{
		{"ask every time", presetAskEveryTime(), content.DecisionAsk, content.DecisionAsk},
		{"ask on mutate", presetAskOnMutate(), content.DecisionPermit, content.DecisionAsk},
		{"autonomous", presetAutonomous(), content.DecisionPermit, content.DecisionPermit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, e := range effects {
				want := tc.other
				if e == content.EffectObserve {
					want = tc.observe
				}
				if got := tc.policy.DecisionFor(e); got != want {
					t.Fatalf("%s decides %s = %s, want %s", tc.name, e, got, want)
				}
			}
		})
	}
}

func TestMatrixDecidesPerEffectClass(t *testing.T) {
	// The finer than presets expression: one effect permitted, one asked,
	// one refused — the three outcomes in one policy.
	var p content.EffectPolicy
	p.Observe = content.EffectRow{Decision: content.DecisionPermit}
	p.MutateReversible = content.EffectRow{Decision: content.DecisionAsk}
	p.MutateDestructive = content.EffectRow{Decision: content.DecisionRefuse}

	if got := p.DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("observe = %s, want permit", got)
	}
	if got := p.DecisionFor(content.EffectMutateReversible); got != content.DecisionAsk {
		t.Fatalf("mutate-reversible = %s, want ask", got)
	}
	if got := p.DecisionFor(content.EffectMutateDestructive); got != content.DecisionRefuse {
		t.Fatalf("mutate-destructive = %s, want refuse", got)
	}
	// The unstated rows ask — partial policies fail toward asking.
	if got := p.DecisionFor(content.EffectDelegate); got != content.DecisionAsk {
		t.Fatalf("unspecified delegate = %s, want ask", got)
	}
	// Effects derived from rows: refused effects leave the grant, asked
	// effects stay declared (asking is a decision, not an absence).
	permitted := p.PermittedEffects()
	want := []content.Effect{
		content.EffectObserve, content.EffectMutateReversible,
		content.EffectPrivilegeChange, content.EffectDisclose,
		content.EffectCrossBoundary, content.EffectDelegate,
	}
	if len(permitted) != len(want) {
		t.Fatalf("PermittedEffects = %v, want %v", permitted, want)
	}
	for i := range want {
		if permitted[i] != want[i] {
			t.Fatalf("PermittedEffects = %v, want %v", permitted, want)
		}
	}
}

func TestMatrixScopeIsPerRowAndNeverLeaksIntoAnotherEffect(t *testing.T) {
	// observe is scoped to /home, mutate-reversible to /etc. Even though
	// the minted grant's SCOPE UNION covers both paths (declaration kind
	// coverage), the enforcement reads the SELECTED effect's row: observe
	// on /etc is refused, and mutate on /home is refused — per-effect
	// scope authority, no second scope truth.
	var p content.EffectPolicy
	p.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/home"}},
	}
	p.MutateReversible = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/etc"}},
	}
	g := p.AsGrant(nil)

	covered := map[content.ResourceKind]bool{}
	for _, s := range g.Scopes {
		covered[s.Kind] = true
	}
	if !covered[content.ResourcePath] {
		t.Fatal("grant scope union lacks path kind — files.read would not be declared")
	}

	observeScopes := g.Policy.RowScopes(content.EffectObserve)
	if len(observeScopes) != 1 || observeScopes[0].ID != "/home" {
		t.Fatalf("observe row scopes = %+v, want only /home — another effect's scopes must not leak into observe", observeScopes)
	}
	mutateScopes := g.Policy.RowScopes(content.EffectMutateReversible)
	if len(mutateScopes) != 1 || mutateScopes[0].ID != "/etc" {
		t.Fatalf("mutate-reversible row scopes = %+v, want only /etc", mutateScopes)
	}
}

func TestAsGrantFoldsRunScopesIntoEveryRowAndDerivesEffects(t *testing.T) {
	p := presetAskOnMutate()
	g := p.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "run-session"}})

	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectDelegate,
	} {
		scopes := g.Policy.RowScopes(e)
		if len(scopes) != 1 || scopes[0].Kind != content.ResourceSession || scopes[0].ID != "run-session" {
			t.Fatalf("row %s scopes = %+v, want the run's session scope folded in", e, scopes)
		}
	}
	// ask-on-mutate refuses nothing, so every effect is declared (and asks
	// at call time, except observe).
	if len(g.Effects) != 7 {
		t.Fatalf("Effects = %v, want all seven (ask rows stay declared)", g.Effects)
	}
}

func TestParsePolicyFailsTowardAsking(t *testing.T) {
	// Criterion 5, asserted for each of the three: unstated (the zero
	// matrix), empty (a document with no rows), unparseable (not JSON).
	var zero content.EffectPolicy
	for _, e := range []content.Effect{content.EffectObserve, content.EffectDelegate} {
		if got := zero.DecisionFor(e); got != content.DecisionAsk {
			t.Fatalf("zero matrix decides %s = %s, want ask", e, got)
		}
	}

	empty, err := content.ParseEffectPolicy([]byte(`{}`))
	if err != nil {
		t.Fatalf("empty policy: %v", err)
	}
	for _, e := range []content.Effect{content.EffectObserve, content.EffectMutateDestructive} {
		if got := empty.DecisionFor(e); got != content.DecisionAsk {
			t.Fatalf("empty policy decides %s = %s, want ask", e, got)
		}
	}

	if _, err := content.ParseEffectPolicy([]byte(`{not json`)); err == nil {
		t.Fatal("unparseable policy parsed; it must refuse parsing so the store can fail toward asking")
	} else if !errors.Is(err, content.ErrPolicySyntax) {
		t.Fatalf("unparseable policy error = %v, want ErrPolicySyntax", err)
	}
}

func TestNoConfigurationPathExpressesAToolName(t *testing.T) {
	// Criterion 3, asserted by trying — a tool name as a row key, and a
	// tool-kind scope, are both unparseable. There is no level at which a
	// person can say "permit readScreen".
	toolKey := []byte(`{"readScreen": {"decision": "permit"}}`)
	if _, err := content.ParseEffectPolicy(toolKey); err == nil {
		t.Fatal("a tool-name row key parsed — the tool-name rule must be impossible by construction")
	}

	effected := []byte(`{"observe": {"decision": "permit", "scopes": [{"kind": "tool", "id": "readScreen"}]}}`)
	if _, err := content.ParseEffectPolicy(effected); err == nil {
		t.Fatal("a tool-kind scope parsed — a policy may not bind an effect to a named tool")
	}

	// Unknown row keys and unknown row fields are unparseable too.
	for _, bad := range [][]byte{
		[]byte(`{"observe": {"decision": "maybe", "scopes": []}}`),
		[]byte(`{"observe": {"decision": "permit", "scope": "x"}}`),
		[]byte(`{"observe": {"decision": "permit", "scopes": [{"kind": "path", "id": "relative"}]}}`),
		[]byte(`{"observe": {"decision": "permit", "scopes": [{"kind": "session", "id": ""}]}}`),
		[]byte(`{"observe": {"decision": "permit", "scopes": [{"kind": "host"}]}}`),
	} {
		if _, err := content.ParseEffectPolicy(bad); err == nil {
			t.Fatalf("invalid policy parsed: %s", bad)
		}
	}
}

func TestPolicyWireIsCanonicalSevenRows(t *testing.T) {
	// The wire always carries all seven rows with effective decisions and a
	// non-null scopes array — a renderer can draw the whole matrix from a
	// policy expressed as two rows.
	p, err := content.ParseEffectPolicy([]byte(`{"observe": {"decision": "permit", "scopes": [{"kind": "path", "id": "/home"}]}, "delegate": {"decision": "refuse"}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{"observe", "mutate-reversible", "mutate-destructive", "privilege-change", "disclose", "cross-boundary", "delegate"} {
		row, ok := wire[key].(map[string]any)
		if !ok {
			t.Fatalf("wire lacks row %q: %s", key, b)
		}
		if _, ok := row["scopes"].([]any); !ok {
			t.Fatalf("row %q scopes is not an array: %s", key, b)
		}
	}
	observe, ok := wire["observe"].(map[string]any)
	if !ok {
		t.Fatalf("observe row missing from wire: %s", b)
	}
	if observe["decision"] != "permit" {
		t.Fatalf("observe decision = %v, want permit", observe["decision"])
	}
	delegate, ok := wire["delegate"].(map[string]any)
	if !ok {
		t.Fatalf("delegate row missing from wire: %s", b)
	}
	if delegate["decision"] != "refuse" {
		t.Fatalf("delegate decision = %v, want refuse", delegate["decision"])
	}
	unstated, ok := wire["disclose"].(map[string]any)
	if !ok {
		t.Fatalf("disclose row missing from wire: %s", b)
	}
	if unstated["decision"] != "ask" {
		t.Fatalf("unstated disclose decision = %v, want ask on the wire (effective decisions)", unstated["decision"])
	}
}

func TestResolvePolicyStatedOrderOnce(t *testing.T) {
	// Criterion 6: workspace overrides global when one exists; nil
	// workspace resolves the global default.
	global := presetAutonomous()
	workspace := presetAskEveryTime()

	if got := content.ResolvePolicy(global, nil, nil); got.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatal("nil workspace must resolve the global default")
	}
	if got := content.ResolvePolicy(global, &workspace, nil); got.DecisionFor(content.EffectObserve) != content.DecisionAsk {
		t.Fatal("a workspace policy must override the global default")
	}
}

func TestResolvePolicy_SessionOverrideChangesOneRowOnly(t *testing.T) {
	global := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk, Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: "/w"}}},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
	}
	got := content.ResolvePolicy(global, nil, content.SessionOverrides{content.EffectObserve: content.DecisionPermit})

	if d := got.DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe: got %q, want permit", d)
	}
	if d := got.DecisionFor(content.EffectMutateDestructive); d != content.DecisionAsk {
		t.Fatalf("mutate-destructive: got %q, want ask (untouched)", d)
	}
	// The overlay decides; it never re-scopes.
	scopes := got.RowScopes(content.EffectObserve)
	if len(scopes) != 1 || scopes[0].ID != "/w" {
		t.Fatalf("observe scopes: got %+v, want the global's [/w]", scopes)
	}
}

func TestResolvePolicy_NoSessionOverridesIsTheOldBehaviour(t *testing.T) {
	global := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}
	ws := &content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionRefuse}}

	if d := content.ResolvePolicy(global, nil, nil).DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("nil overrides, no workspace: got %q, want permit", d)
	}
	if d := content.ResolvePolicy(global, ws, content.SessionOverrides{}).DecisionFor(content.EffectObserve); d != content.DecisionRefuse {
		t.Fatalf("empty overrides, workspace wins: got %q, want refuse", d)
	}
}

func TestResolvePolicy_SessionOverlaysOnTopOfTheWorkspace(t *testing.T) {
	global := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionRefuse}}
	ws := &content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}

	got := content.ResolvePolicy(global, ws, content.SessionOverrides{content.EffectObserve: content.DecisionPermit})
	if d := got.DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("session over workspace: got %q, want permit", d)
	}
}

func TestSetRowDecision_ReplacesOneDecisionAndKeepsItsScopes(t *testing.T) {
	p := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk, Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: "/w"}}},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
	}
	got := p.SetRowDecision(content.EffectObserve, content.DecisionPermit)

	if d := got.DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe: got %q, want permit", d)
	}
	if sc := got.RowScopes(content.EffectObserve); len(sc) != 1 || sc[0].ID != "/w" {
		t.Fatalf("observe scopes: got %+v, want [/w] kept", sc)
	}
	if d := got.DecisionFor(content.EffectMutateDestructive); d != content.DecisionAsk {
		t.Fatalf("mutate-destructive: got %q, want the untouched ask", d)
	}
	if d := p.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatal("SetRowDecision mutated its receiver; it must return a copy")
	}
}

func TestSetRowDecision_IgnoresADecisionOutsideTheEnum(t *testing.T) {
	p := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}
	if d := p.SetRowDecision(content.EffectObserve, content.Decision("maybe")).DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("got %q, want the untouched ask", d)
	}
}

func TestResolvePolicy_InvalidSessionDecisionIsIgnored(t *testing.T) {
	// Fail toward asking: a value outside the enum must never widen a row.
	global := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}
	got := content.ResolvePolicy(global, nil, content.SessionOverrides{content.EffectObserve: content.Decision("yes-please")})
	if d := got.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("invalid override: got %q, want the untouched ask", d)
	}
}
