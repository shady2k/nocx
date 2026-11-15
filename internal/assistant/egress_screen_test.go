package assistant

// The egress screen's acceptance tests (nocx-a62cl.3.3, ADR-anntnxdj §2): what
// actually leaves is inspected by a Screen on the kernel's chain — the tool's
// result AND its error, and a result that fails its own contract too — an
// approval releases exactly the retained bytes without running the tool
// again, and a person's no ends the run with nothing released.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// plantExecutor replaces one tool's executor for the duration of a test. It is
// the seam these tests need: what the tool RETURNS is the subject, and a real
// files.read cannot produce a result outside its own contract or one holding a
// value the vault knows.
func TestADeclinedEgressEndsTheRunAndReleasesNothing(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "deploy key: "+secret)
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	k := kernelFor(t, grant, ledger)
	k.approvals = approvals
	k.known = &knownMatcher{value: secret, name: "github-token"}

	asked := 0
	chain := k.authorityChain(&kernelCall{kernel: k})
	chain.Approver = approverFunc(func(_ context.Context, _ Call, ask Ask) Answer {
		if ask.Egress == nil || len(ask.Egress.Findings) == 0 {
			t.Fatalf("the approver was asked %q without the findings the screen saw", ask.Reason)
		}
		asked++
		return Declined{Reason: "the person will not send that result"}
	})
	pipeline, err := NewPipeline(k.dispatcher, chain)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	outcome, err := pipeline.run(ToolInvocation{
		Context:    context.Background(),
		Method:     "files.read",
		RunContext: k.runCtx,
		Grant:      grant,
		RawParams:  []byte(args),
	}, k.attempt, "call-1")
	var refusal *PipelineRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the declined result returned %v, want the refusal that ends the run", err)
	}
	var declined *ScreenDeclinedError
	if !errors.As(err, &declined) {
		t.Fatalf("the declined result came back as %v with no cause — the kernel would answer the call's slot with it and the run would go on", err)
	}
	if asked != 1 {
		t.Fatalf("the approver was asked %d times, want exactly 1", asked)
	}
	if outcome.output != "" {
		t.Fatalf("the declined result was released: %q", outcome.output)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the refusal carried the material it withheld: %v", err)
	}
	if strings.Contains(refusal.Reason, secret) {
		t.Fatalf("the refusal's sentence carried the material it withheld: %q", refusal.Reason)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d attempts, want the call's one — the tool ran once and its result was refused", got)
	}
}
