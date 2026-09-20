package agenttools

// nocx-ykjai. ADR-0053 reads a declaration's effect set as ALTERNATIVES a call
// selects between — session.run is lsblk OR rm -rf — and ForGrant offers a row
// when ANY of its classes is not refused. skills.install is the first row where
// the set is a CONJUNCTION: the fetch and the write both happen on every
// successful call, and nothing selects between them. Under alternation a policy
// refusing reversible mutation still offered the tool and still asked about it —
// a question about a write the person had already declined.

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// skillInstallGrant is the grant skills.install needs apart from its effects:
// the skill sub-scope family plus a destination, since it declares both kinds.
func skillInstallGrant(effects ...content.Effect) content.Grant {
	return content.Grant{
		Effects: effects,
		Scopes: []content.GrantScope{
			{Kind: content.ResourceContent, ID: "skill"},
			{Kind: content.ResourceDestination, ID: "*"},
		},
	}
}

func TestSkillsInstallDeclaresAConjunction(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("skills.install")
	if !ok {
		t.Fatal("skills.install is not declared")
	}
	if tool.EffectRelation != EffectsConjunctive {
		t.Fatalf("skills.install effect relation = %v, want conjunctive: the fetch AND the write happen on every call", tool.EffectRelation)
	}
	// Every other row is alternative, including the door ADR-0053 was written
	// for. A conjunction that spread by accident would silently narrow tools
	// nobody meant to narrow.
	for _, other := range reg.All() {
		if other.Name == "skills.install" {
			continue
		}
		if other.EffectRelation != EffectsAlternative {
			t.Errorf("%s effect relation = %v, want alternative", other.Name, other.EffectRelation)
		}
	}
}

func TestForGrantWithholdsAConjunctiveToolWhenEitherClassIsRefused(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// BOTH WAYS ROUND, which is the point: neither class alone is enough, and
	// it must not matter which one the policy happens to refuse.
	for _, tc := range []struct {
		name   string
		grant  content.Grant
		offers bool
	}{
		{
			name:   "the fetch is permitted and the write is refused",
			grant:  skillInstallGrant(content.EffectCrossBoundary),
			offers: false,
		},
		{
			name:   "the write is permitted and the fetch is refused",
			grant:  skillInstallGrant(content.EffectMutateReversible),
			offers: false,
		},
		{
			name:   "both are permitted",
			grant:  skillInstallGrant(content.EffectMutateReversible, content.EffectCrossBoundary),
			offers: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offered := containsName(reg.ForGrant(tc.grant), "skills.install")
			if offered != tc.offers {
				t.Fatalf("skills.install offered = %v, want %v; got %v",
					offered, tc.offers, toolNames(reg.ForGrant(tc.grant)))
			}
		})
	}
}

func TestForGrantStillOffersAnAlternativeToolOnOneOfItsClasses(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	// session.run is the door ADR-0053 was written for and it must keep the
	// alternation: an operator who permits only observation still gets lsblk,
	// and the decision moves to execution.
	g := content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "*"}},
	}
	if !containsName(reg.ForGrant(g), "session.run") {
		t.Fatalf("an observe-only grant must still offer session.run; got %v", toolNames(reg.ForGrant(g)))
	}
}

// workers.spawn is the first row whose ARGUMENTS choose the reading: the set
// is declared as alternatives and the worktree ask raises the call to the
// conjunction at execution. The declaration's own relation must stay
// alternative — the offer-time filter reads it, and a plain spawn is pure
// delegation (nocx-xn63t.1 review, blocker 1).
func TestWorkersSpawnInvocationRelationRaisesTheWorktreeAsk(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("workers.spawn")
	if !ok {
		t.Fatal("workers.spawn is not declared")
	}
	if tool.EffectRelation != EffectsAlternative {
		t.Fatalf("workers.spawn declared relation = %v, want alternative: the offer-time question is any-row-not-refused", tool.EffectRelation)
	}
	t.Run("a plain ask is the delegate row alone", func(t *testing.T) {
		relation, effects, ok := tool.InvocationRelation(map[string]any{"command": "claude", "task": "read it"})
		if !ok || relation != EffectsAlternative || len(effects) != 1 || effects[0] != content.EffectDelegate {
			t.Fatalf("plain spawn shape = (%v, %v, %v), want (alternative, [delegate], true)", relation, effects, ok)
		}
	})
	t.Run("a worktree ask is the conjunction", func(t *testing.T) {
		relation, effects, ok := tool.InvocationRelation(map[string]any{
			"command": "claude", "task": "read it", "worktree": map[string]any{"branch": "feat/x"},
		})
		if !ok || relation != EffectsConjunctive || len(effects) != 2 {
			t.Fatalf("worktree ask shape = (%v, %v, %v), want (conjunctive, [delegate mutate-reversible], true)", relation, effects, ok)
		}
		for _, want := range []content.Effect{content.EffectDelegate, content.EffectMutateReversible} {
			if !containsEffectRow(effects, want) {
				t.Fatalf("worktree ask shape = %v, want it to reach %v", effects, want)
			}
		}
	})
}

func containsEffectRow(effects []content.Effect, want content.Effect) bool {
	for _, effect := range effects {
		if effect == want {
			return true
		}
	}
	return false
}
