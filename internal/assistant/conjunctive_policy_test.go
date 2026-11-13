package assistant

// nocx-ykjai, the decision half. ForGrant stops OFFERING a conjunctive tool
// when one of its classes is refused; this is what happens when the model
// names it anyway — the policy decision must be the strictest across every row
// the call reaches, not the decision of the worst row alone.

import (
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func skillsInstallTool(t *testing.T) agenttools.Tool {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("skills.install")
	if !ok {
		t.Fatal("skills.install is not in the registry")
	}
	return tool
}

func TestAConjunctiveToolIsRefusedWhenEitherRowRefuses(t *testing.T) {
	tool := skillsInstallTool(t)
	for _, tc := range []struct {
		name   string
		policy content.EffectPolicy
		want   policyOutcome
	}{
		{
			name:   "the write is refused and the fetch permitted",
			policy: autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionRefuse),
			want:   policyRefuse,
		},
		{
			name:   "the fetch is refused and the write permitted",
			policy: autonomousMatrix().SetRowDecision(content.EffectCrossBoundary, content.DecisionRefuse),
			want:   policyRefuse,
		},
		{
			// Both permitted still ASKS, because a skill outlives the run
			// whose grant authorised it (isSkillMutationTool). The point here
			// is that it is an ask and not a refusal.
			name:   "both are permitted",
			policy: autonomousMatrix(),
			want:   policyAsk,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := tc.policy.AsGrant([]content.GrantScope{
				{Kind: content.ResourceContent, ID: "skill"},
				{Kind: content.ResourceDestination, ID: "*"},
			})
			mw := middlewareFor(t, grant, &fakeLedger{}, nil)
			resources, err := tool.ResolveResources(
				map[string]any{"url": "https://example.test/SKILL.md"}, mw.kernel.runCtx)
			if err != nil {
				t.Fatalf("ResolveResources: %v", err)
			}
			outcome, _, _ := mw.kernel.decideInvocationWithReason(tool, resources, true, content.Invocation{Parsed: true})
			if outcome != tc.want {
				t.Fatalf("outcome = %v, want %v", outcome, tc.want)
			}
		})
	}
}

func TestAConjunctiveToolAsksWhenEitherRowAsks(t *testing.T) {
	// The strictest of {permit, ask} is ask. This is the same rule as the
	// refusal above and it is asserted separately because a "worst row wins"
	// implementation gets the refusal right by luck — cross-boundary is the
	// higher row — and this one wrong.
	tool := skillsInstallTool(t)
	policy := autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionAsk)
	grant := policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceContent, ID: "skill"},
		{Kind: content.ResourceDestination, ID: "*"},
	})
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)
	resources, err := tool.ResolveResources(
		map[string]any{"url": "https://example.test/SKILL.md"}, mw.kernel.runCtx)
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	outcome, _, _ := mw.kernel.decideInvocationWithReason(tool, resources, true, content.Invocation{Parsed: true})
	if outcome != policyAsk {
		t.Fatalf("outcome = %v, want policyAsk", outcome)
	}
}

func TestAnAlternativeToolKeepsDecidingOnItsSelectedRow(t *testing.T) {
	// session.run is the door ADR-0053 was written for: refusing one of its
	// classes must not refuse the tool, or an operator who declines
	// irreversible change loses lsblk with rm.
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("session.run")
	if !ok {
		t.Fatal("session.run is not in the registry")
	}
	policy := autonomousMatrix().SetRowDecision(content.EffectMutateDestructive, content.DecisionRefuse)
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "*"}})
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)

	// Selected at execution from the arguments: an observation.
	selected, invocation := classifyCall(tool, map[string]any{"command": "lsblk"})
	resources, err := selected.ResolveResources(map[string]any{"command": "lsblk"}, mw.kernel.runCtx)
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	if outcome, _, _ := mw.kernel.decideInvocationWithReason(selected, resources, true, invocation); outcome == policyRefuse {
		t.Fatal("refusing mutate-destructive refused an observation: the alternation was lost")
	}
}
