package assistant

// WidenRowScope is the whole of what the widening answer changes (design
// §5.3). It is asserted here rather than through the wire because the
// transport's own tests are about the ANSWER; these are about the document.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func rowScopeIDs(p content.EffectPolicy, e content.Effect) []string {
	out := make([]string, 0, len(p.RowScopes(e)))
	for _, s := range p.RowScopes(e) {
		out = append(out, string(s.Kind)+":"+s.ID)
	}
	return out
}

// The row grows, and nothing else does — not the other rows, not the rules,
// not the row's decision. A widening that also moved a decision would be a
// standing answer nobody gave.
func TestWidenRowScope_GrowsOneRowAndTouchesNothingElse(t *testing.T) {
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo/src"}},
		},
		MutateReversible: content.EffectRow{
			Decision: content.DecisionAsk,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo/out"}},
		},
		Rules: []content.InvocationRule{{Pattern: [][]string{{"df", "-h"}}, Decision: content.DecisionPermit}},
	}

	next, err := WidenRowScope(p, content.EffectObserve, content.GrantScope{
		Kind: content.ResourcePath, ID: "/repo/lib",
	})
	if err != nil {
		t.Fatalf("WidenRowScope: %v", err)
	}
	if got := strings.Join(rowScopeIDs(next, content.EffectObserve), ","); got != "path:/repo/src,path:/repo/lib" {
		t.Fatalf("observe scopes = %q, want the row grown by the resource that fell outside", got)
	}
	if next.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatalf("observe decision = %q, want it unchanged", next.DecisionFor(content.EffectObserve))
	}
	if got := strings.Join(rowScopeIDs(next, content.EffectMutateReversible), ","); got != "path:/repo/out" {
		t.Fatalf("mutate-reversible scopes = %q, want another row untouched", got)
	}
	if len(next.Rules) != 1 || len(next.Rules[0].Pattern) != 1 || next.Rules[0].Pattern[0][0] != "df" {
		t.Fatalf("rules = %+v, want the standing rules preserved", next.Rules)
	}
	// The source is not mutated: the caller holds the store's live value.
	if got := strings.Join(rowScopeIDs(p, content.EffectObserve), ","); got != "path:/repo/src" {
		t.Fatalf("the policy passed in became %q — WidenRowScope must return a new value", got)
	}
}

// Answering the same question twice must not grow the document twice.
func TestWidenRowScope_IsIdempotent(t *testing.T) {
	scope := content.GrantScope{Kind: content.ResourcePath, ID: "/repo/lib"}
	p := content.EffectPolicy{Observe: content.EffectRow{
		Decision: content.DecisionPermit, Scopes: []content.GrantScope{scope},
	}}
	next, err := WidenRowScope(p, content.EffectObserve, scope)
	if err != nil {
		t.Fatalf("WidenRowScope: %v", err)
	}
	if got := len(next.RowScopes(content.EffectObserve)); got != 1 {
		t.Fatalf("scopes = %d, want the row unchanged when it already covers the resource", got)
	}
}

// The strict gate is the same one an operator's document crosses: a widening
// that would produce an unparseable policy is refused, not written.
func TestWidenRowScope_RefusesAScopeThePolicyGateRejects(t *testing.T) {
	p := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}
	for name, scope := range map[string]content.GrantScope{
		"a tool-kind scope names a tool": {Kind: content.ResourceTool, ID: "files.read"},
		"a relative path":                {Kind: content.ResourcePath, ID: "repo/lib"},
	} {
		if _, err := WidenRowScope(p, content.EffectObserve, scope); err == nil {
			t.Fatalf("%s: WidenRowScope accepted %+v", name, scope)
		}
	}
}

// An effect outside the lattice is an error, never a silent no-op: a widening
// that widened nothing would resume a call whose next identical proposal asks
// again — the exact defect §5.3 exists to remove.
func TestWidenRowScope_RefusesAnEffectOutsideTheLattice(t *testing.T) {
	p := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}
	if _, err := WidenRowScope(p, content.Effect("not-an-effect"), content.GrantScope{
		Kind: content.ResourcePath, ID: "/repo/lib",
	}); err == nil {
		t.Fatal("WidenRowScope accepted an effect that has no row")
	}
}
