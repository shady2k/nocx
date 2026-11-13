package agenttools

// nocx-ykjai. ADR-0053 reads a declaration's effect set as ALTERNATIVES a call
// selects between — session.run is lsblk OR rm -rf — and ForGrant offers a row
// when ANY of its classes is not refused. A CONJUNCTION is the other reading:
// every class is reached on a successful call, so the strictest decides.
// workers.spawn raises it per invocation, and the declaration itself must stay
// alternative because the offer-time filter reads the declaration.

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

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
