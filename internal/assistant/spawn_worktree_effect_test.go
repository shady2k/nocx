package assistant

// The worktree ask's policy half (nocx-xn63t.1 review, blocker 1).
//
// workers.spawn declared EffectDelegate alone, so a grant that permitted
// delegation and refused filesystem mutation still got a checkout created:
// the mutate-reversible row the ask actually reaches was never consulted.
// ADR-0053 as amended (nocx-ykjai) is the governing record, and the
// coordinator's decision binds the shape: the declaration carries delegate
// and mutate-reversible as ALTERNATIVES — a plain spawn is pure delegation
// and stays reachable for a delegate-only grant — while the worktree ask
// RAISES the call to the conjunction of the two rows at execution, where
// the arguments exist. These tests hold both halves through both dispatch
// paths that execute the tool: the tool endpoint's (the product's minted
// coordinators, permit-or-refuse) and the kernel's verdict machinery
// (where an ask becomes a question to a person).

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

const (
	spawnPlainParams    = `{"command":"claude","task":"read it"}`
	spawnWorktreeParams = `{"command":"claude","task":"read it","worktree":{"branch":"feat/x"}}`
)

func spawnGrantForTest(t *testing.T, policy content.EffectPolicy) content.Grant {
	t.Helper()
	return policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	})
}

func dispatchSpawn(t *testing.T, rec *fakeWorkerRecord, policy content.EffectPolicy, params string) error {
	t.Helper()
	dispatcher := toolDispatcherForTest(t, rec)
	_, err := dispatcher.Dispatch(workerInvocationForTest("workers.spawn", spawnGrantForTest(t, policy), params))
	return err
}

// spawnEffectRefusal reports the per-call effect gate's own refusal: a
// *DispatchRefusalError naming a refused effect row. An error from anywhere
// else on the path — a narrow, the executor, a malformed param — is not one,
// so an acceptance of this shape cannot pass for the refusal.
func spawnEffectRefusal(err error) (*DispatchRefusalError, bool) {
	var refusal *DispatchRefusalError
	if !errors.As(err, &refusal) {
		return nil, false
	}
	if !strings.Contains(refusal.Reason, "refuses") {
		return nil, false
	}
	return refusal, true
}

func TestAWorktreeSpawnIsRefusedWhenTheGrantRefusesItsMutation(t *testing.T) {
	rec := &fakeWorkerRecord{}
	policy := autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionRefuse)
	err := dispatchSpawn(t, rec, policy, spawnWorktreeParams)
	refusal, ok := spawnEffectRefusal(err)
	if !ok {
		t.Fatalf("worktree spawn under a mutation-refusing grant: err = %v, want the effect refusal naming mutate-reversible", err)
	}
	if !strings.Contains(refusal.Reason, "mutate-reversible") {
		t.Fatalf("refusal = %q, want it to name the refused mutate-reversible row", refusal.Reason)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("the refused spawn reached the executor and registered %+v — the checkout would already exist", rec.registered)
	}
}

func TestAPlainSpawnStaysReachableUnderTheSameGrant(t *testing.T) {
	// The same delegate-permitting, mutation-refusing grant must keep the
	// plain spawn offered and executable: the coordinator's decision is
	// that requiring mutation may not take delegation away.
	rec := &fakeWorkerRecord{}
	policy := autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionRefuse)
	err := dispatchSpawn(t, rec, policy, spawnPlainParams)
	if _, isRefusal := spawnEffectRefusal(err); isRefusal {
		t.Fatalf("plain spawn refused by the effect gate: %v — requiring mutation took delegation away", err)
	}
	if len(rec.registered) != 1 {
		t.Fatalf("plain spawn under a mutation-refusing grant registered %d workers, want 1", len(rec.registered))
	}
}

func TestAWorktreeSpawnRunsWhenTheGrantCoversBothRows(t *testing.T) {
	rec := &fakeWorkerRecord{}
	err := dispatchSpawn(t, rec, autonomousMatrix(), spawnWorktreeParams)
	if _, isRefusal := spawnEffectRefusal(err); isRefusal {
		t.Fatalf("worktree spawn refused under a grant covering both rows: %v", err)
	}
	if len(rec.registered) != 1 {
		t.Fatalf("worktree spawn under a both-rows grant registered %d workers, want 1", len(rec.registered))
	}
	if rec.registered[0].Worktree == nil || rec.registered[0].Worktree.Branch != "feat/x" {
		t.Fatalf("the passing worktree spawn lost its ask: %+v", rec.registered[0].Worktree)
	}
}

func TestARefusedDelegateRowRefusesEvenAPlainSpawn(t *testing.T) {
	// The mirror of the reviewed defect, and the reason the per-call shape
	// carries the rows and not only the relation: a plain spawn IS the
	// delegate row — delegate alone — so a grant refusing delegation refuses
	// it however permissive its mutation row is. Under the declared
	// alternatives read as a whole, mutate-reversible would keep the tool
	// alive and delegation would run without the row that names it.
	rec := &fakeWorkerRecord{}
	policy := autonomousMatrix().SetRowDecision(content.EffectDelegate, content.DecisionRefuse)
	err := dispatchSpawn(t, rec, policy, spawnPlainParams)
	refusal, ok := spawnEffectRefusal(err)
	if !ok {
		t.Fatalf("plain spawn under a delegation-refusing grant: err = %v, want the effect refusal naming delegate", err)
	}
	if !strings.Contains(refusal.Reason, "delegate") {
		t.Fatalf("refusal = %q, want it to name the refused delegate row", refusal.Reason)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("the refused spawn reached the executor and registered %+v", rec.registered)
	}
}

// TestAWorktreeSpawnIsDecidedAcrossBothRaisedRows holds the kernel half:
// with the relation raised to the conjunction, the verdict is the strictest
// across delegate and mutate-reversible — an ask on the mutation row is an
// ask about the whole call, and a refusal on it refuses, where the old
// single-row declaration could produce neither.
func TestAWorktreeSpawnIsDecidedAcrossBothRaisedRows(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	tool, ok := reg.Lookup("workers.spawn")
	if !ok {
		t.Fatal("workers.spawn is not declared")
	}
	args := map[string]any{
		"command":  "claude",
		"task":     "read it",
		"worktree": map[string]any{"branch": "feat/x"},
	}
	for _, tc := range []struct {
		name   string
		policy content.EffectPolicy
		want   policyOutcome
	}{
		{
			name:   "an ask on the mutation row asks about the whole call",
			policy: autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionAsk),
			want:   policyAsk,
		},
		{
			name:   "a refusal on the mutation row refuses the call",
			policy: autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionRefuse),
			want:   policyRefuse,
		},
		{
			name:   "both rows permitted permits",
			policy: autonomousMatrix(),
			want:   policyPermit,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := tc.policy.AsGrant([]content.GrantScope{
				{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
			})
			mw := middlewareFor(t, grant, &fakeLedger{}, nil)
			// prepare's own application of the derived shape, applied here
			// because this test exercises the verdict, not prepare: the
			// endpoint tests above hold prepare and the gate together.
			if tool.InvocationRelation == nil {
				t.Fatal("workers.spawn declares no InvocationRelation — the worktree raise cannot be derived")
			}
			relation, _, ok := tool.InvocationRelation(args)
			if !ok {
				t.Fatal("the worktree ask derived no shape")
			}
			raised := tool
			raised.EffectRelation = relation
			resources, resErr := raised.ResolveResources(args, mw.kernel.runCtx)
			if resErr != nil {
				t.Fatalf("ResolveResources: %v", resErr)
			}
			outcome, _, _, _ := mw.kernel.decideInvocationWithReason(raised, resources, true, content.Invocation{Parsed: true})
			if outcome != tc.want {
				t.Fatalf("outcome = %v, want %v", outcome, tc.want)
			}
		})
	}
}

// TestAPlainSpawnDecidesOnItsOwnRow pins the other half of the coordinator's
// decision at the verdict: the plain ask keeps its alternative reading, so a
// mutation row set to refuse never asks about a spawn that does not mutate.
func TestAPlainSpawnDecidesOnItsOwnRow(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	tool, ok := reg.Lookup("workers.spawn")
	if !ok {
		t.Fatal("workers.spawn is not declared")
	}
	args := map[string]any{"command": "claude", "task": "read it"}
	policy := autonomousMatrix().SetRowDecision(content.EffectMutateReversible, content.DecisionRefuse)
	grant := policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	})
	mw := middlewareFor(t, grant, &fakeLedger{}, nil)
	relation, _, ok := tool.InvocationRelation(args)
	if !ok || relation != agenttools.EffectsAlternative {
		t.Fatalf("plain spawn relation = %v (ok %v), want the alternative reading", relation, ok)
	}
	raised := tool
	raised.EffectRelation = relation
	resources, resErr := raised.ResolveResources(args, mw.kernel.runCtx)
	if resErr != nil {
		t.Fatalf("ResolveResources: %v", resErr)
	}
	if outcome, _, _, _ := mw.kernel.decideInvocationWithReason(raised, resources, true, content.Invocation{Parsed: true}); outcome != policyPermit {
		t.Fatalf("plain spawn under a mutation-refusing grant = %v, want permit", outcome)
	}
}
