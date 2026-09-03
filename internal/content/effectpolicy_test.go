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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
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

func TestAsGrantDropsDisjointSelectorKind(t *testing.T) {
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes: []content.GrantScope{{
				Kind: content.ResourceSession,
				ID:   "operator-authored-session",
			}},
		},
	}
	g := p.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "live-session"},
		{Kind: content.ResourcePath, ID: "/"},
		{Kind: content.ResourceContent, ID: "content"},
	})
	scopes := g.Policy.RowScopes(content.EffectObserve)
	if len(scopes) != 2 ||
		scopes[0] != (content.GrantScope{Kind: content.ResourcePath, ID: "/"}) ||
		scopes[1] != (content.GrantScope{Kind: content.ResourceContent, ID: "content"}) {
		t.Fatalf("observe scopes = %+v, want only fence kinds absent from the selector", scopes)
	}
	for _, effect := range g.Effects {
		if effect == content.EffectObserve {
			t.Fatalf("partly overlapping selector kept observe effect in grant: %v", g.Effects)
		}
	}
}

func TestAsGrantDropsDisjointPathSelector(t *testing.T) {
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes: []content.GrantScope{{
				Kind: content.ResourcePath,
				ID:   "/repo",
			}},
		},
	}
	g := p.AsGrant([]content.GrantScope{{
		Kind: content.ResourcePath,
		ID:   "/home/dev",
	}})
	if got := g.Policy.RowScopes(content.EffectObserve); len(got) != 0 {
		t.Fatalf("observe scopes = %+v, want no path coverage for disjoint fence", got)
	}
	for _, effect := range g.Effects {
		if effect == content.EffectObserve {
			t.Fatalf("disjoint selector kept observe effect in grant: %v", g.Effects)
		}
	}
}

func TestAsGrantKeepsKindWhenAnyFenceScopeOverlaps(t *testing.T) {
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes: []content.GrantScope{{
				Kind: content.ResourcePath,
				ID:   "/repo",
			}},
		},
	}
	g := p.AsGrant([]content.GrantScope{
		{Kind: content.ResourcePath, ID: "/repo/project"},
		{Kind: content.ResourcePath, ID: "/home/dev"},
	})
	scopes := g.Policy.RowScopes(content.EffectObserve)
	if len(scopes) != 1 || scopes[0].ID != "/repo/project" {
		t.Fatalf("observe scopes = %+v, want only the overlapping fence scope", scopes)
	}
	for _, effect := range g.Effects {
		if effect == content.EffectObserve {
			return
		}
	}
	t.Fatalf("any overlapping fence scope refused observe: %v", g.Effects)
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

func TestPolicyAcceptsContentAndWorkspaceScopes(t *testing.T) {
	p, err := content.ParseEffectPolicy([]byte(`{"observe":{"decision":"permit","scopes":[{"kind":"content","id":"note/note-a"},{"kind":"workspace","id":"workspace/ws-a/tab/tab-a"}]}}`))
	if err != nil {
		t.Fatalf("new resource scopes refused: %v", err)
	}
	scopes := p.RowScopes(content.EffectObserve)
	if len(scopes) != 2 || scopes[0].Kind != content.ResourceContent || scopes[1].Kind != content.ResourceWorkspace {
		t.Fatalf("observe scopes = %+v, want content and workspace", scopes)
	}
}

func TestPolicyRefusesMalformedHierarchicalScopes(t *testing.T) {
	for _, bad := range []string{
		`{"observe":{"decision":"permit","scopes":[{"kind":"content","id":"note/"}]}}`,
		`{"observe":{"decision":"permit","scopes":[{"kind":"content","id":"note/../secret"}]}}`,
		`{"observe":{"decision":"permit","scopes":[{"kind":"workspace","id":"workspace/ws-a/pane/pane-a"}]}}`,
		`{"observe":{"decision":"permit","scopes":[{"kind":"workspace","id":"workspace/ws-a/tab/tab-a/pane/"}]}}`,
	} {
		if _, err := content.ParseEffectPolicy([]byte(bad)); err == nil {
			t.Fatalf("malformed hierarchical scope parsed: %s", bad)
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

	if got := content.ResolvePolicy(global, nil, content.SessionOverrides{}); got.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatal("nil workspace must resolve the global default")
	}
	if got := content.ResolvePolicy(global, &workspace, content.SessionOverrides{}); got.DecisionFor(content.EffectObserve) != content.DecisionAsk {
		t.Fatal("a workspace policy must override the global default")
	}
}

func TestResolvePolicy_SessionOverrideChangesOneRowOnly(t *testing.T) {
	global := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk, Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: "/w"}}},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
	}
	got := content.ResolvePolicy(global, nil, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.DecisionPermit},
	})

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

	if d := content.ResolvePolicy(global, nil, content.SessionOverrides{}).DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("nil overrides, no workspace: got %q, want permit", d)
	}
	if d := content.ResolvePolicy(global, ws, content.SessionOverrides{}).DecisionFor(content.EffectObserve); d != content.DecisionRefuse {
		t.Fatalf("empty overrides, workspace wins: got %q, want refuse", d)
	}
}

func TestResolvePolicy_SessionOverlaysOnTopOfTheWorkspace(t *testing.T) {
	global := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionRefuse}}
	ws := &content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}

	got := content.ResolvePolicy(global, ws, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.DecisionPermit},
	})
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
	got := content.ResolvePolicy(global, nil, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.Decision("yes-please")},
	})
	if d := got.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("invalid override: got %q, want the untouched ask", d)
	}
}

// ── a command's resources produce scopes of every kind they can name (nocx-c88xr) ──
//
// The defect these tests exist for: namedResourceScope yielded a scope only
// for an absolute path, always of kind ResourcePath, so every resource a
// command names over the network — a curl URL, an ssh destination, a kubectl
// cluster, all recorded with verb ResourceNetwork — produced no scope at all
// and no row could bound it. A destination scope governed fetch.url, which
// resolves a real ResourceDestination, and did not govern curl.

// curlInvocation is what internal/assistant's parser produces for
// `curl <url>`: one resolved resource, the URL, under ResourceNetwork
// (cmdeffect.go, the curl branch of appendResourceReport). It is built here
// rather than parsed because parseCanonicalInvocation is unexported and
// internal/content may not reach into the assistant; the cross-path test
// below is what keeps this shape honest, since it drives the same address
// through fetch.url's real resolver.
func curlInvocation(url string) content.Invocation {
	return content.Invocation{
		Commands: [][]string{{"curl", url}},
		Parsed:   true,
		Resources: content.ResourceReport{
			Resources: []content.Resource{{Path: url, Verb: content.ResourceNetwork}},
		},
	}
}

func crossBoundaryPermitting(scopes ...content.GrantScope) content.EffectPolicy {
	var p content.EffectPolicy
	p.CrossBoundary = content.EffectRow{Decision: content.DecisionPermit, Scopes: scopes}
	return p
}

func TestARowDestinationScopeBoundsACommandToo(t *testing.T) {
	// A person who narrowed "reach another host" to github.com has narrowed
	// the command path too, and the endpoint form of Task 2 is what the
	// narrowing is written in.
	cases := []struct {
		name    string
		scope   content.GrantScope
		command string
		want    content.Decision
	}{
		{
			name:    "another host is outside the only scope",
			scope:   content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com"},
			command: "https://example.com",
			want:    content.DecisionAsk,
		},
		{
			name:    "a document at the granted place is inside it",
			scope:   content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com"},
			command: "https://github.com/owner/repo",
			want:    content.DecisionPermit,
		},
		{
			name:    "a subdomain is outside a scope that does not claim subdomains",
			scope:   content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com"},
			command: "https://api.github.com/x",
			want:    content.DecisionAsk,
		},
		{
			name:    "a subdomain is inside a scope that claims them",
			scope:   content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com", IncludeSubdomains: true},
			command: "https://api.github.com/x",
			want:    content.DecisionPermit,
		},
		{
			name:    "a host that merely ends in the granted name is outside it",
			scope:   content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com", IncludeSubdomains: true},
			command: "https://notgithub.com/x",
			want:    content.DecisionAsk,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := crossBoundaryPermitting(tc.scope)
			got := policy.DecisionForInvocation(content.EffectCrossBoundary, curlInvocation(tc.command))
			if got != tc.want {
				t.Fatalf("curl %s under a row scoped to %q (subdomains=%t) decides %s, want %s",
					tc.command, tc.scope.ID, tc.scope.IncludeSubdomains, got, tc.want)
			}
		})
	}
}

// scopeFormPerVerb states, independently of the implementation, the scope
// kind each ResourceVerb is written in — one resource a command can name, a
// row scope that holds it and a row scope of the same kind that does not.
var scopeFormPerVerb = map[content.ResourceVerb]struct {
	resource content.Resource
	holds    content.GrantScope
	excludes content.GrantScope
}{
	content.ResourceRead: {
		resource: content.Resource{Path: "/home/dev/notes.txt", Verb: content.ResourceRead},
		holds:    content.GrantScope{Kind: content.ResourcePath, ID: "/home/dev"},
		excludes: content.GrantScope{Kind: content.ResourcePath, ID: "/etc"},
	},
	content.ResourceWrite: {
		resource: content.Resource{Path: "/home/dev/notes.txt", Verb: content.ResourceWrite},
		holds:    content.GrantScope{Kind: content.ResourcePath, ID: "/home/dev"},
		excludes: content.GrantScope{Kind: content.ResourcePath, ID: "/etc"},
	},
	content.ResourceDelete: {
		resource: content.Resource{Path: "/home/dev/notes.txt", Verb: content.ResourceDelete},
		holds:    content.GrantScope{Kind: content.ResourcePath, ID: "/home/dev"},
		excludes: content.GrantScope{Kind: content.ResourcePath, ID: "/etc"},
	},
	content.ResourceExecute: {
		resource: content.Resource{Path: "/usr/local/bin/deploy", Verb: content.ResourceExecute},
		holds:    content.GrantScope{Kind: content.ResourcePath, ID: "/usr/local/bin"},
		excludes: content.GrantScope{Kind: content.ResourcePath, ID: "/etc"},
	},
	content.ResourceSource: {
		resource: content.Resource{Path: "/home/dev/.env", Verb: content.ResourceSource},
		holds:    content.GrantScope{Kind: content.ResourcePath, ID: "/home/dev"},
		excludes: content.GrantScope{Kind: content.ResourcePath, ID: "/etc"},
	},
	content.ResourceNetwork: {
		resource: content.Resource{Path: "https://github.com/owner/repo", Verb: content.ResourceNetwork},
		holds:    content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com"},
		excludes: content.GrantScope{Kind: content.ResourceDestination, ID: "https://example.com"},
	},
}

// verbsWithNoScopeForm is the explicit other half of the mapping: a verb a
// row can never bound, and the reason it is not a hole.
var verbsWithNoScopeForm = map[content.ResourceVerb]string{
	content.ResourceUnknown: "the verb of an UnresolvedResource only — never of a resolved Resource, " +
		"and an unresolved report already takes the worst declared effect",
}

func TestEveryResourceVerbHasAScopeKind(t *testing.T) {
	// The exhaustiveness tripwire. A verb added to resources.go and to
	// neither map above is a resource a row can never bound, which is this
	// task's whole defect; it fails here rather than returning false in
	// silence.
	for _, verb := range declaredResourceVerbs(t) {
		_, mapped := scopeFormPerVerb[verb]
		reason, excused := verbsWithNoScopeForm[verb]
		switch {
		case mapped && excused:
			t.Errorf("verb %q both has a scope form and is excused from one (%s)", verb, reason)
		case !mapped && !excused:
			t.Errorf("verb %q maps to no scope kind: a row can never bound it. "+
				"Give it a form in scopeFormPerVerb, or say in verbsWithNoScopeForm why it needs none.", verb)
		}
	}
}

func TestEachVerbIsBoundedByItsOwnScopeForm(t *testing.T) {
	// The consequence of the mapping, per verb: a row scoped away from the
	// resource does not permit, and a row scoped over it does.
	for verb, form := range scopeFormPerVerb {
		t.Run(string(verb), func(t *testing.T) {
			inv := content.Invocation{
				Commands:  [][]string{{"anything"}},
				Parsed:    true,
				Resources: content.ResourceReport{Resources: []content.Resource{form.resource}},
			}
			var out content.EffectPolicy
			out.Observe = content.EffectRow{Decision: content.DecisionPermit, Scopes: []content.GrantScope{form.excludes}}
			if got := out.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionAsk {
				t.Errorf("%s resource %q under a row scoped to %q decides %s, want ask",
					verb, form.resource.Path, form.excludes.ID, got)
			}
			var in content.EffectPolicy
			in.Observe = content.EffectRow{Decision: content.DecisionPermit, Scopes: []content.GrantScope{form.holds}}
			if got := in.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionPermit {
				t.Errorf("%s resource %q under a row scoped to %q decides %s, want permit",
					verb, form.resource.Path, form.holds.ID, got)
			}
		})
	}
}

// declaredResourceVerbs reads the ResourceVerb constants out of resources.go
// rather than from a hand-kept list, because a hand-kept list is exactly the
// thing a newly added verb would not appear in.
func declaredResourceVerbs(t *testing.T) []content.ResourceVerb {
	t.Helper()
	const source = "resources.go"
	file, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", source, err)
	}
	var verbs []content.ResourceVerb
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "ResourceVerb" {
				continue
			}
			for _, expr := range value.Values {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: a ResourceVerb constant is not a string literal", source)
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquote %s: %v", source, lit.Value, err)
				}
				verbs = append(verbs, content.ResourceVerb(text))
			}
		}
	}
	if len(verbs) == 0 {
		t.Fatalf("%s declares no ResourceVerb constants — the tripwire is reading the wrong file", source)
	}
	return verbs
}

func TestTheSameAddressIsBoundedTheSameWayThroughACommandAndATool(t *testing.T) {
	// One policy, one address, two paths to it: the command path through
	// DecisionForInvocation, and the declared path through fetch.url's real
	// resolver and the row scopes the kernel checks it against
	// (internal/assistant/kernel.go, inScope). They must agree on whether
	// the call is permitted. They do NOT yet agree on the CAUSE — the
	// command path answers ask and the declared path refuses out of scope —
	// which is nocx-okdsm, the next task.
	reg, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble the real tool registry: %v", err)
	}
	tool, ok := reg.Lookup("fetch.url")
	if !ok {
		t.Fatal("fetch.url is not in the registry — the declared path this test compares against does not exist")
	}

	scope := content.GrantScope{Kind: content.ResourceDestination, ID: "https://github.com", IncludeSubdomains: true}
	policy := crossBoundaryPermitting(scope)

	for _, address := range []string{
		"https://github.com/owner/repo",
		"https://api.github.com/x",
		"https://example.com",
		"https://notgithub.com/x",
	} {
		t.Run(address, func(t *testing.T) {
			refs, err := tool.ResolveResources(map[string]any{"url": address}, agenttools.RunContext{})
			if err != nil {
				t.Fatalf("fetch.url resolves %q: %v", address, err)
			}
			if len(refs) != 1 || refs[0].Kind != content.ResourceDestination {
				t.Fatalf("fetch.url resolved %+v, want one destination", refs)
			}
			declaredPermits := policy.DecisionFor(content.EffectCrossBoundary) == content.DecisionPermit
			for _, ref := range refs {
				inside := false
				for _, s := range policy.RowScopes(content.EffectCrossBoundary) {
					if s.Contains(content.GrantScope{Kind: ref.Kind, ID: ref.ID}) {
						inside = true
						break
					}
				}
				if !inside {
					declaredPermits = false
				}
			}
			commandPermits := policy.DecisionForInvocation(
				content.EffectCrossBoundary, curlInvocation(address),
			) == content.DecisionPermit
			if declaredPermits != commandPermits {
				t.Fatalf("%s: fetch.url permits=%t but curl permits=%t — one path is bounded and the other is not",
					address, declaredPermits, commandPermits)
			}
		})
	}
}

// ── one evaluator, one typed cause (nocx-t6h2u) ──
//
// The defect these tests exist for: two containment paths answered "the call
// named a resource outside the row scopes" differently — the command path
// asked (DecisionForInvocation), the declared path refused
// (internal/assistant/kernel.go, RefusedOutOfScope) — and EffectRow's own doc
// comment stated refusal for both. The split is not between the two paths; it
// is between two CAUSES, and one evaluator now reports which.

// catInvocation is what internal/assistant's parser produces for
// `cat <path>`: one resolved resource, the path, under ResourceRead. Built
// here for the same reason curlInvocation is — parseCanonicalInvocation is
// unexported and internal/content may not reach into the assistant.
func catInvocation(path string) content.Invocation {
	return content.Invocation{
		Commands: [][]string{{"cat", path}},
		Parsed:   true,
		Resources: content.ResourceReport{
			Resources: []content.Resource{{Path: path, Verb: content.ResourceRead}},
		},
	}
}

func observePermitting(scopes ...content.GrantScope) content.EffectPolicy {
	var p content.EffectPolicy
	p.Observe = content.EffectRow{Decision: content.DecisionPermit, Scopes: scopes}
	return p
}

func TestOutOfScopeCauseSeparatesAQuestionFromARefusal(t *testing.T) {
	policy := observePermitting(content.GrantScope{Kind: content.ResourcePath, ID: "/workspace"})

	editable := policy.EvaluateInvocation(content.EffectObserve, catInvocation("/etc/hosts"), nil)
	if editable.Decision != content.DecisionAsk || editable.Cause != content.OutOfScopeRowScope {
		t.Errorf("a path outside an editable row scope gave %+v; it must be a question", editable)
	}
	if editable.Resource.ID != "/etc/hosts" || editable.Resource.Kind != content.ResourcePath {
		t.Errorf("the verdict does not name what fell outside: %+v", editable.Resource)
	}

	fenced := policy.EvaluateInvocation(content.EffectObserve, catInvocation("/etc/hosts"),
		[]content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}})
	if fenced.Decision != content.DecisionRefuse || fenced.Cause != content.OutOfScopeFence {
		t.Errorf("a path outside the fence gave %+v; approval cannot make it executable", fenced)
	}
	if fenced.Resource.ID != "/etc/hosts" {
		t.Errorf("the fence refusal does not name what fell outside: %+v", fenced.Resource)
	}
}
