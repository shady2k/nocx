package assistant

// The amendment's acceptance criteria asserted through the real pipeline —
// the middleware's permit/ask/refuse path, not just the matrix's vocabulary:
//
// - criterion 2: per resource scope, a policy finer than any preset and a
//   run permitted or refused accordingly (the effect dimension's
//   per-class decisions live in the vocabulary tests, internal/content,
//   which drive the same DecisionFor the pipeline calls);
// - criterion 4: the presets, expressed as matrices, decide exactly as the
//   old preset switch did on identical calls;
// - criterion 5: an unstated or empty policy fails toward asking — asserted
//   through the pipeline, not only at DecisionFor.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func decideOutcome(t *testing.T, grant content.Grant, tool agenttools.Tool, args map[string]any) policyOutcome {
	t.Helper()
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)
	return mw.decide(tool, args)
}

func filesReadTool(t *testing.T) agenttools.Tool {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not in the registry")
	}
	return tool
}

// TestMatrixCriterion2_RunPermittedAskedRefused drills the pipeline's decide
// with a policy finer than any preset, on a real tool: observe permitted
// within one path and asked on another; the same row refusing; and an
// out-of-row-scope call refused even though the minted grant's SCOPE UNION
// covers that path through a different row — no scope leak between effects.
func TestMatrixCriterion2PerScopePermitAskRefuse(t *testing.T) {
	home := t.TempDir()
	ectc := t.TempDir() // another row's scope — must not leak into observe
	var policy content.EffectPolicy
	policy.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: home}},
	}
	policy.MutateReversible = content.EffectRow{
		Decision: content.DecisionAsk,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: ectc}},
	}
	grant := policy.AsGrant(nil)
	tool := filesReadTool(t)

	if got := decideOutcome(t, grant, tool, map[string]any{"path": filepath.Join(home, "a.txt")}); got != policyPermit {
		t.Fatalf("observe inside its row scope = %v, want permit", got)
	}
	if got := decideOutcome(t, grant, tool, map[string]any{"path": filepath.Join(ectc, "a.txt")}); got != policyRefuse {
		t.Fatalf("observe on a path another effect's row covers = %v, want refuse — per-row scopes, no union leak", got)
	}

	// The ask end of the same effect row: observe asks.
	policy.Observe.Decision = content.DecisionAsk
	if got := decideOutcome(t, policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: home}}), tool, map[string]any{"path": filepath.Join(home, "a.txt")}); got != policyAsk {
		t.Fatalf("observe ask row = %v, want ask", got)
	}
	// The refuse end: observe refuses.
	policy.Observe.Decision = content.DecisionRefuse
	if got := decideOutcome(t, policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: home}}), tool, map[string]any{"path": filepath.Join(home, "a.txt")}); got != policyRefuse {
		t.Fatalf("observe refuse row = %v, want refuse", got)
	}
}

// TestMatrixCriterion4PresetsDecideAsTheOldSwitchDid maps each preset to its
// matrix and asserts the pipeline's outcome on identical in-scope observe
// calls matches what the old grant.Policy switch returned: autonomous and
// ask-on-mutate permitted in-scope observe, ask-every-time asked it.
func TestMatrixCriterion4PresetsDecideAsTheOldSwitchDid(t *testing.T) {
	dir := t.TempDir()
	tool := filesReadTool(t)
	inScope := map[string]any{"path": filepath.Join(dir, "a.txt")}

	cases := []struct {
		name         string
		policy       content.EffectPolicy
		oldPermitted bool // what the old switch decided for an in-scope observe
	}{
		{"ask-every-time", askEveryTimeMatrix(), false},
		{"ask-on-mutate", askOnMutateMatrix(), true},
		{"autonomous", autonomousMatrix(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant := tc.policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
			got := decideOutcome(t, grant, tool, inScope)
			if tc.oldPermitted && got != policyPermit {
				t.Fatalf("old preset permitted an in-scope observe; matrix decides %v", got)
			}
			if !tc.oldPermitted && got != policyAsk {
				t.Fatalf("old preset asked an in-scope observe; matrix decides %v", got)
			}
		})
	}
}

// TestMatrixCriterion5EmptyAndUnstatedFailTowardAsking drives the pipeline
// with grants minted from zero and empty policies: the run asks an in-scope
// read, never permits it — the fail-toward-asking ends a store can actually
// produce. A grant whose matrix field was never set is authority nobody
// minted: its rows carry no scopes, so nothing is in scope and the call is
// refused — still never permitted, and never silently executed.
func TestMatrixCriterion5EmptyStatefulFailTowardAsking(t *testing.T) {
	dir := t.TempDir()
	tool := filesReadTool(t)
	inScope := map[string]any{"path": filepath.Join(dir, "a.txt")}

	zero := content.EffectPolicy{}
	if got := decideOutcome(t, zero.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}}), tool, inScope); got != policyAsk {
		t.Fatalf("zero matrix = %v, want ask", got)
	}
	empty, err := content.ParseEffectPolicy([]byte(`{}`))
	if err != nil {
		t.Fatalf("parse empty policy: %v", err)
	}
	if got := decideOutcome(t, empty.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}}), tool, inScope); got != policyAsk {
		t.Fatalf("empty matrix = %v, want ask", got)
	}
	silent := content.Grant{Version: 1, Effects: []content.Effect{content.EffectObserve}, Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: dir}}}
	if got := decideOutcome(t, silent, tool, inScope); got != policyRefuse {
		t.Fatalf("grant without a matrix = %v, want refuse (nothing is in scope without a mint)", got)
	}
}
