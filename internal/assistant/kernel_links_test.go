package assistant

// The kernel's composition as points (nocx-a62cl.3.2). What these tests hold
// is the ORDER the record fixes and what each link is for: a file changed
// between the question and the answer is refused with no effect, an approved
// widening reaches the capability, and every link of the kernel's chain is
// load-bearing — remove one and a call that should have been stopped is not.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// runKernelCall composes one call's chain the way invokeClassified does, drops
// one link when drop names it, and runs the call through the pipeline. It is
// the seam a link-removal test needs: the composition is the kernel's own
// (authorityChain), and only the link list is edited.
func runKernelCall(t *testing.T, k *effectKernel, name, callID, args, drop string) (dispatchOutcome, error) {
	t.Helper()
	chain := k.authorityChain(&kernelCall{kernel: k})
	if drop != "" {
		dropChainLink(t, &chain, drop)
	}
	pipeline, err := NewPipeline(k.dispatcher, chain)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	return pipeline.run(ToolInvocation{
		Context:    context.Background(),
		Method:     name,
		RunContext: k.runCtx,
		Grant:      k.grant,
		RawParams:  []byte(args),
	}, k.attempt, callID)
}

// guardLinkName is the test's own name for a guard, so a link can be removed
// by what it is rather than by its position.
func guardLinkName(guard Guard) string {
	switch guard.(type) {
	case kernelGrantGuard:
		return "grant guard"
	case kernelFloorGuard:
		return "floor guard"
	case kernelEvidenceGuard:
		return "evidence guard"
	default:
		return ""
	}
}

func dropChainLink(t *testing.T, chain *AuthorityChain, link string) {
	t.Helper()
	switch link {
	case "policy":
		if len(chain.PreCalls) != 1 {
			t.Fatalf("the chain carries %d policy points, want 1", len(chain.PreCalls))
		}
		chain.PreCalls = nil
		return
	case "approver":
		if chain.Approver == nil {
			t.Fatal("the chain carries no approver")
		}
		chain.Approver = nil
		return
	case "attempt":
		if chain.Attempts == nil {
			t.Fatal("the chain carries no attempt step")
		}
		chain.Attempts = nil
		return
	case "executor":
		if chain.execute == nil {
			t.Fatal("the chain carries no executor")
		}
		chain.execute = nil
		return
	}
	kept := make([]Guard, 0, len(chain.Guards))
	removed := false
	for _, guard := range chain.Guards {
		if guardLinkName(guard) == link {
			removed = true
			continue
		}
		kept = append(kept, guard)
	}
	if !removed {
		t.Fatalf("the chain carries no %q link", link)
	}
	chain.Guards = kept
}

// TestTheKernelChainComposesEveryLink is the composition assertion the stage's
// third DONE WHEN asks for: each link the record assigns to the kernel is in
// the chain, once, in the order it runs. A link dropped from authorityChain
// fails here before any behaviour is exercised.
func TestTheKernelChainComposesEveryLink(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})
	chain := k.authorityChain(&kernelCall{kernel: k})

	if len(chain.PreCalls) != 1 {
		t.Fatalf("policy points = %d, want exactly one (classification, matrix, standing rules and declines, row scope)", len(chain.PreCalls))
	}
	if _, ok := chain.PreCalls[0].(kernelPolicy); !ok {
		t.Fatalf("policy point is %T, want kernelPolicy", chain.PreCalls[0])
	}
	if _, ok := chain.Approver.(kernelApprover); !ok {
		t.Fatalf("approver is %T, want kernelApprover", chain.Approver)
	}
	wantGuards := []string{"grant guard", "floor guard", "evidence guard"}
	if len(chain.Guards) != len(wantGuards) {
		t.Fatalf("guards = %d, want %d (%v)", len(chain.Guards), len(wantGuards), wantGuards)
	}
	for i, want := range wantGuards {
		if got := guardLinkName(chain.Guards[i]); got != want {
			t.Fatalf("guard %d is %q, want %q — the grant and the floors are read before the evidence", i, got, want)
		}
	}
	if _, ok := chain.Attempts.(kernelAttempts); !ok {
		t.Fatalf("attempt step is %T, want kernelAttempts", chain.Attempts)
	}
	if chain.execute == nil {
		t.Fatal("the chain carries no executor — the kernel's own runtime is where the tool runs once")
	}
}

// TestAFileChangedBetweenTheQuestionAndTheAnswerIsRefusedWithNoEffect is the
// stage's first DONE WHEN: the evidence is read AFTER the answer, on a fresh
// reading, so the wait cannot be used to swap the file. Nothing ran, nothing
// was read, and the person's yes was not consumed by the refusal.
func TestAFileChangedBetweenTheQuestionAndTheAnswerIsRefusedWithNoEffect(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the bytes the person agreed to")
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	k := kernelFor(t, grant, ledger)
	k.approvals = approvals

	_, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Invoke error = %v, want the approval suspension", err)
	}
	proposal := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}
	if !approvals.Approve(proposal) {
		t.Fatal("the proposal the kernel asked about was not pending")
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d attempts before the answer, want the proposal's one", got)
	}

	// The wait is the window: the file is replaced while the question is open.
	writeFile(t, path, "the bytes nobody agreed to")

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("the changed file returned an error, want the refusal as the call's result: %v", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, path) {
		t.Fatalf("result = %q, want a refusal naming the file whose approval no longer holds", out)
	}
	if strings.Contains(out, "the bytes nobody agreed to") {
		t.Fatal("the refused call read the file's new bytes — the evidence was checked before the answer, or not at all")
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d attempts, want only the proposal's — a refused call never becomes an attempt", got)
	}
	if !approvals.IsApproved(proposal) {
		t.Fatal("the refusal consumed the person's yes — the evidence changed, not their answer")
	}
}

// TestAnApprovedWideningReachesTheCapability is the stage's second DONE WHEN:
// the question names the resource that fell outside the row's editable
// selector, the answer widens the row, and the SAME call then runs — the
// capability is constructed on the grant as it stands after the answer, not on
// the one the question was asked under.
func TestAnApprovedWideningReachesTheCapability(t *testing.T) {
	fenceRoot := t.TempDir()
	rowRoot := filepath.Join(fenceRoot, "row")
	if err := os.MkdirAll(rowRoot, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", rowRoot, err)
	}
	outside := filepath.Join(fenceRoot, "outside.txt")
	writeFile(t, outside, "the widened read")
	args := fmt.Sprintf(`{"path":%q}`, outside)

	policy := autonomousMatrix()
	policy.Observe.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: rowRoot}}
	fence := []content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: fenceRoot},
	}
	grant := policy.AsGrant(fence)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	k := kernelFor(t, grant, ledger)
	k.approvals = approvals

	_, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Invoke error = %v, want the widening question", err)
	}
	fact := asked.Request.OutOfScope
	if fact == nil || fact.Cause != content.OutOfScopeRowScope {
		t.Fatalf("question out-of-scope fact = %+v, want the editable row scope", fact)
	}
	if fact.Resource != (content.GrantScope{Kind: content.ResourcePath, ID: outside}) {
		t.Fatalf("question names %+v, want the resource the row would have to cover", fact.Resource)
	}

	// The answer: the row grows by that resource, and the exact proposal is
	// approved — what agent.approve does, in the store the transport owns.
	widened, err := WidenRowScope(policy, content.EffectObserve, fact.Resource)
	if err != nil {
		t.Fatalf("widen the row: %v", err)
	}
	proposal := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}
	if !approvals.Approve(proposal) {
		t.Fatal("the widening question was not pending")
	}

	// The resumed run holds the widened grant, exactly as the transport mints
	// one per run from the policy it just wrote.
	resumed := kernelFor(t, widened.AsGrant(fence), ledger)
	resumed.approvals = approvals
	out, err := resumed.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("the approved widening did not reach the call: %v", err)
	}
	if !strings.Contains(out, "the widened read") {
		t.Fatalf("result = %q, want the file the widened row now covers", out)
	}
}

// TestEveryLinkOfTheKernelChainIsLoadBearing removes one link at a time from
// the kernel's own composition and asserts the call the link was there to stop
// is no longer stopped. The full chain is run first in every case, so each
// assertion is a change the link made rather than a property of the scenario.
func TestEveryLinkOfTheKernelChainIsLoadBearing(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		grant, dir := testDirGrant(t, askEveryTimeMatrix())
		path := filepath.Join(dir, "a.txt")
		writeFile(t, path, "the policy point asked first")
		args := fmt.Sprintf(`{"path":%q}`, path)

		k := kernelFor(t, grant, &fakeLedger{})
		k.approvals = NewApprovalStore()
		if _, err := runKernelCall(t, k, "files.read", "call-1", args, ""); err == nil {
			t.Fatal("the full chain ran a call the policy asks about")
		}
		out, err := runKernelCall(t, k, "files.read", "call-1", args, "policy")
		if err != nil {
			t.Fatalf("without the policy point: %v — the call was still stopped", err)
		}
		if !strings.Contains(out.output, "the policy point asked first") {
			t.Fatalf("without the policy point the call returned %q, want the file it should not have read", out.output)
		}
	})

	t.Run("approver", func(t *testing.T) {
		grant, dir := testDirGrant(t, askEveryTimeMatrix())
		path := filepath.Join(dir, "a.txt")
		writeFile(t, path, "must not run")
		args := fmt.Sprintf(`{"path":%q}`, path)

		k := kernelFor(t, grant, &fakeLedger{})
		k.approvals = NewApprovalStore()
		_, err := runKernelCall(t, k, "files.read", "call-1", args, "")
		var asked *ApprovalRequestedError
		if !errors.As(err, &asked) {
			t.Fatalf("the full chain did not suspend: %v", err)
		}
		_, err = runKernelCall(t, k, "files.read", "call-1", args, "approver")
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "no approver") {
			t.Fatalf("without the approver = %v, want the pipeline's unanswered Ask", err)
		}
	})

	t.Run("grant guard", func(t *testing.T) {
		grant, dir := testDirGrant(t, autonomousMatrix())
		writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
		grant.ExpiresAt = time.Now().Add(-time.Millisecond).UnixMilli()
		args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

		k := kernelFor(t, grant, &fakeLedger{})
		_, err := runKernelCall(t, k, "files.read", "call-1", args, "")
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "expired") {
			t.Fatalf("the full chain = %v, want the expired-authority refusal answer", err)
		}
		_, err = runKernelCall(t, k, "files.read", "call-1", args, "grant guard")
		if err == nil {
			t.Fatal("without the grant guard the call ran on an expired grant")
		}
		if errors.As(err, &refusal) && strings.Contains(refusal.Reason, "expired") {
			t.Fatalf("without the grant guard the call still answered the expired-authority refusal: %v", err)
		}
	})

	t.Run("floor guard", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "config", "nocx")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", configDir, err)
		}
		protected := filepath.Join(configDir, "agent-policy.json")
		writeFile(t, protected, "the policy document")
		grant := floorPermissivePolicy().WithFloor(content.NewFloor(configDir, filepath.Join(t.TempDir(), "data", "nocx"))).AsGrant([]content.GrantScope{
			{Kind: content.ResourceSession, ID: "session-floor"},
			{Kind: content.ResourcePath, ID: "/"},
		})
		args := fmt.Sprintf(`{"path":%q}`, protected)

		k := kernelFor(t, grant, &fakeLedger{})
		_, err := runKernelCall(t, k, "files.read", "call-1", args, "")
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) || !strings.Contains(strings.ToLower(refusal.Reason), "floor") {
			t.Fatalf("the full chain = %v, want the floor refusal answer", err)
		}
		out, err := runKernelCall(t, k, "files.read", "call-1", args, "floor guard")
		if err != nil {
			t.Fatalf("without the floor guard: %v", err)
		}
		if !strings.Contains(out.output, "the policy document") {
			t.Fatalf("without the floor guard the call returned %q, want the protected file's bytes", out.output)
		}
	})

	t.Run("evidence guard", func(t *testing.T) {
		grant, dir := testDirGrant(t, askEveryTimeMatrix())
		path := filepath.Join(dir, "a.txt")
		writeFile(t, path, "before")
		args := fmt.Sprintf(`{"path":%q}`, path)

		ledger := &fakeLedger{}
		approvals := NewApprovalStore()
		k := kernelFor(t, grant, ledger)
		k.approvals = approvals
		if _, err := k.Invoke(context.Background(), "files.read", "call-1", args); err == nil {
			t.Fatal("the full chain ran the call without asking")
		}
		if !approvals.Approve(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}) {
			t.Fatal("the proposal was not pending")
		}
		writeFile(t, path, "after")

		_, err := runKernelCall(t, k, "files.read", "call-1", args, "")
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "changed since approval") {
			t.Fatalf("the full chain = %v, want the changed-file refusal answer", err)
		}
		if strings.Contains(refusal.Reason, "after") {
			t.Fatalf("the refusal carried the file's new bytes: %q", refusal.Reason)
		}
		out, err := runKernelCall(t, k, "files.read", "call-1", args, "evidence guard")
		if err != nil {
			t.Fatalf("without the evidence guard: %v", err)
		}
		if !strings.Contains(out.output, "after") {
			t.Fatalf("without the evidence guard the call returned %q, want the changed file's bytes", out.output)
		}
	})

	t.Run("attempt", func(t *testing.T) {
		grant, dir := testDirGrant(t, autonomousMatrix())
		path := filepath.Join(dir, "a.txt")
		writeFile(t, path, "in scope")
		args := fmt.Sprintf(`{"path":%q}`, path)

		ledger := &fakeLedger{}
		k := kernelFor(t, grant, ledger)
		if _, err := runKernelCall(t, k, "files.read", "call-1", args, ""); err != nil {
			t.Fatalf("the full chain: %v", err)
		}
		if got := ledger.started(); got != 1 {
			t.Fatalf("the full chain opened %d attempts, want 1", got)
		}
		ledger = &fakeLedger{}
		k = kernelFor(t, grant, ledger)
		if _, err := runKernelCall(t, k, "files.read", "call-2", args, "attempt"); err != nil {
			t.Fatalf("without the attempt step: %v", err)
		}
		if got := ledger.started(); got != 0 {
			t.Fatalf("without the attempt step the ledger opened %d attempts, want 0 — the tool ran unrecorded", got)
		}
	})

	t.Run("executor", func(t *testing.T) {
		grant, dir := testDirGrant(t, autonomousMatrix())
		path := filepath.Join(dir, "a.txt")
		writeFile(t, path, "in scope")
		args := fmt.Sprintf(`{"path":%q}`, path)

		k := kernelFor(t, grant, &fakeLedger{})
		if _, err := runKernelCall(t, k, "files.read", "call-1", args, ""); err != nil {
			t.Fatalf("the full chain: %v", err)
		}
		if _, err := runKernelCall(t, k, "files.read", "call-1", args, "executor"); err == nil {
			t.Fatal("without the executor the call reported success")
		}
	})
}
