package assistant

// The egress screen's acceptance tests (nocx-a62cl.3.3, ADR-anntnxdj §2): what
// actually leaves is inspected by a Screen on the kernel's chain — the tool's
// result AND its error, and a result that fails its own contract too — an
// approval releases exactly the retained bytes without running the tool
// again, and a person's no ends the run with nothing released.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// plantExecutor replaces one tool's executor for the duration of a test. It is
// the seam these tests need: what the tool RETURNS is the subject, and a real
// files.read cannot produce a result outside its own contract or one holding a
// value the vault knows.
func plantExecutor(t *testing.T, name string, fn func(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error)) {
	t.Helper()
	original, ok := executors[name]
	if !ok {
		t.Fatalf("no executor registered for %q", name)
	}
	executors[name] = fn
	t.Cleanup(func() { executors[name] = original })
}

// readResult is a files.read result that satisfies the declaration's own
// result contract. text is what the tool "read".
func readResult(path, text string) string {
	return fmt.Sprintf(
		`{"path":%q,"total":%d,"revision":"rev-1","window":{"start":0,"end":%d},"seen":{"start":1,"end":1},"returned":%d,"text":%q}`,
		path, len(text), len(text), len(text), text)
}

// TestAsk_EgressScreensAResultThatFailsItsContract is the coordinator's note
// on this task: a result that fails its result schema returns from
// executePrepared before any Screen runs, and the schema diagnostic quotes the
// result's own property names. Here the extra property IS the vault's value,
// so the diagnostic reported as it comes would carry the material the screen
// exists to withhold. The assertions are both ends: the screen sees the
// invalid bytes first (nothing leaves, the person is asked), and the failure
// finally reported names the tool and not the result.
func TestAsk_EgressScreensAResultThatFailsItsContract(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the planted executor answers instead of reading this")
	args := fmt.Sprintf(`{"path":%q}`, path)

	// Schema-valid fields plus one the declaration's contract forbids, whose
	// NAME is the vault's value. The schema library's diagnostic quotes it.
	invalid := fmt.Sprintf(
		`{"path":%q,"total":1,"revision":"rev-1","window":{"start":0,"end":1},"seen":{"start":1,"end":1},"returned":1,%q:true}`,
		path, secret)
	runs := 0
	plantExecutor(t, "files.read", func(context.Context, agenttools.Capability, json.RawMessage, toolSeams) (string, error) {
		runs++
		return invalid, nil
	})

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{value: secret, name: "github-token"}, nil)
	p.RunID, p.Attempt = "run-1", 1

	// Pass 1: the invalid bytes are screened, and the finding suspends the run
	// before any diagnostic is reported.
	err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
	var suspended *EgressRequestedError
	if !errors.As(err, &suspended) {
		t.Fatalf("Ask error = %v, want the egress suspension — an invalid result must pass the Screens too, not return before them", err)
	}
	if len(suspended.Request.Findings) != 1 || suspended.Request.Findings[0].Source != EgressFindingKnown {
		t.Fatalf("findings = %+v, want the vault's value found in the invalid result", suspended.Request.Findings)
	}
	if runs != 1 {
		t.Fatalf("the tool's body ran %d times, want exactly 1", runs)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("the model made %d requests, want 1 — the invalid result never left for the provider", n)
	}

	// The person approves sending those exact bytes.
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	if !approvals.Approve(ap) {
		t.Fatal("the egress proposal was not pending")
	}

	// Pass 2: the retained bytes are released, they still fail the contract,
	// and the reported failure carries no result content.
	err = cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
	var failed *ToolFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("the approved resume = %v, want the result-contract failure", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the failure carried the result's own content: %v", err)
	}
	if strings.Contains(failed.Message(), secret) {
		t.Fatalf("the model-facing message carried the result's own content: %q", failed.Message())
	}
	if runs != 1 {
		t.Fatalf("the tool's body ran %d times, want 1 — an approval releases the retained bytes and never re-runs the tool", runs)
	}
}

// TestAsk_ApprovingAHeldResultReleasesTheExactBytesOnce is the DONE WHEN's
// second assertion at the model's own seam: the withheld result is released
// byte for byte to the provider on the approved resume, and the tool's body is
// entered exactly once across both passes.
func TestAsk_ApprovingAHeldResultReleasesTheExactBytesOnce(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const secret = "known-secret-value-123"
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the planted executor answers instead of reading this")
	args := fmt.Sprintf(`{"path":%q}`, path)
	held := readResult(path, "deploy key: "+secret)

	runs := 0
	plantExecutor(t, "files.read", func(context.Context, agenttools.Capability, json.RawMessage, toolSeams) (string, error) {
		runs++
		return held, nil
	})

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	var turn int
	f, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			streamToolCalls(w, toolCallSpec{name: "files.read", args: args})
			return
		}
		streamOK(w)
	})
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{value: secret, name: "github-token"}, nil)
	p.RunID, p.Attempt = "run-1", 1

	err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
	var suspended *EgressRequestedError
	if !errors.As(err, &suspended) {
		t.Fatalf("Ask error = %v, want the egress suspension", err)
	}
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	if !approvals.Approve(ap) {
		t.Fatal("the egress proposal was not pending")
	}

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("the approved egress resume failed: %v", err)
	}
	if runs != 1 {
		t.Fatalf("the tool's body ran %d times, want exactly 1 — the approval releases the retained bytes", runs)
	}
	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(f.body()), &sent); err != nil {
		t.Fatalf("decode the model's request: %v", err)
	}
	var toolContent string
	for _, message := range sent.Messages {
		if message.Role == "tool" {
			toolContent = message.Content
		}
	}
	if !strings.Contains(toolContent, held) {
		t.Fatalf("the model received %q, want the retained result byte for byte", toolContent)
	}
	if _, _, retained := approvals.RetainedResult(ap); retained {
		t.Fatal("the retained result survived its release")
	}
}

// TestADeclinedEgressEndsTheRunAndReleasesNothing is the DONE WHEN's third
// assertion: a person's no to the screen's question is not an answer the call
// can go on with. The result IS what the screen decides about, so the refusal
// carries a Cause — the shape the kernel turns into the run's end — and the
// withheld bytes are not handed to anybody.
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
