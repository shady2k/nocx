package assistant

// The third claim of the resume path, and the one the design predicted as
// "approve, then failure" (nocx-4yjwk.3). It is NOT the fence test beside it
// in resume_fence_test.go, and the difference is the whole bead:
//
//   - There, the LEXICAL predicate sees the resource fall outside the fence,
//     so the policy refuses before anything is attempted and the refusal is
//     already an answer.
//   - Here, the lexical predicate sees a path INSIDE the fence — the fence
//     contains the symlink, string for string — so the policy ASKS. A person
//     answers yes. Only the capability, which compares provider-canonical
//     identities, can see that the resource is outside; it refuses at
//     execution time, after the attempt has been opened and after the person
//     has been told the call would run.
//
// The enforcement is right and does not move: the narrowed capability is the
// enforcement (ADR-0028 decision 4), the lexical check is advisory, and the
// fence is what stops the read. What was wrong is the SHAPE of the outcome —
// the capability's refusal arrived as a *ToolFailedError, so the run ended on
// a filesystem sentence about a path, and a person who was asked a question
// and answered it got no answer back.
//
// A refusal is an answer (nocx-uvac6.1) wherever the refusal is raised. These
// two tests assert that at both seams the product has: the kernel, where the
// refusal becomes the call's own result, and the whole run, where the model
// reads it and answers.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// fenceSymlink mints the state both tests are about: a run fenced to one
// directory, holding a symlink whose canonical identity is outside it. The
// returned path is the symlink — the thing a model proposes and a person is
// asked about — and beyond is the body no read may return.
func fenceSymlink(t *testing.T) (grant content.Grant, link, beyond string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp root: %v", err)
	}
	fence := filepath.Join(root, "fence")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{fence, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	beyond = "the bytes past the fence"
	target := filepath.Join(outside, "fenced.txt")
	writeFile(t, target, beyond)
	link = filepath.Join(fence, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}

	// The premise, asserted rather than assumed: the LEXICAL predicate the
	// policy uses says this resource is inside the fence. If it ever stops
	// saying so the policy would refuse before asking, and this test would
	// be about a question that is no longer raised.
	grant = askEveryTimeMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: fence}})
	v := grant.Policy.EvaluateResources(content.EffectObserve, content.DecisionAsk,
		[]content.GrantScope{{Kind: content.ResourcePath, ID: link}}, grant.Policy.RunFence())
	if v.Decision == content.DecisionRefuse {
		t.Fatalf("the lexical predicate already refuses %s (%+v) — the symlink no longer passes it, and this test would be about the other fence case",
			link, v)
	}
	return grant, link, beyond
}

// TEST — the capability's refusal is the call's answer, and the run goes on.
//
// The person is asked, answers yes, and the resumed call meets the fence at
// execution. The assertions are the shape of the outcome, not its absence:
// the refusal is OUR sentence in the call's own slot, no error escapes the
// kernel, and the bytes the fence protects are nowhere in it.
func TestResume_AFenceRefusalAfterApprovalAnswersInsteadOfEndingTheRun(t *testing.T) {
	grant, link, beyond := fenceSymlink(t)
	args := fmt.Sprintf(`{"path":%q}`, link)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)
	run := &resumeRun{t: t}

	out, req := run.invoke(mw, "call_1", args)
	if req == nil {
		t.Fatalf("the symlink inside the fence did not raise a question; it answered %q — there would be no approval to refuse through", out)
	}
	proposal := approveExactly(t, approvals, req)

	// The resume: the same run, the same call, the person's yes on record.
	// Today the capability's ErrOutOfScope leaves the kernel as a
	// *ToolFailedError and the run ends here.
	out, err := mw.kernel.Invoke(context.Background(), "files.read", "call_1", args)
	if err != nil {
		var failed *ToolFailedError
		if errors.As(err, &failed) {
			t.Fatalf("the fenced call ended the run with %v — the capability's refusal must reach the model as the call's own result, not as a fault", err)
		}
		t.Fatalf("the resumed call returned %v, want the out-of-scope refusal as a result", err)
	}
	if want := refusalResult("files.read", RefusedOutOfScope, ""); out != want {
		t.Fatalf("the fenced call answered\n  %q\nwant the out-of-scope refusal\n  %q\n— a refusal for the wrong reason reads identically to one for the right reason",
			out, want)
	}

	// The refusal is not permission: nothing was read, and nothing about the
	// file — its bytes or a filesystem sentence naming it — is in the answer.
	if strings.Contains(out, beyond) {
		t.Fatal("the refusal carried the file's contents: the call reached past the fence and the refusal was cosmetic")
	}
	if strings.Contains(out, "filesystem:") || strings.Contains(out, link) {
		t.Fatalf("the refusal speaks the capability's words rather than ours: %q", out)
	}

	// The partial state after "approved, then refused": the attempt exists
	// and is closed — the escalation's, then the approved execution's — and
	// the yes is still on record, so the fence never consumed it.
	if got := ledger.started(); got != 2 {
		t.Fatalf("the ledger opened %d attempts (%v), want 2 — the escalation and the approved call that met the fence", got, ledger.calls())
	}
	if got := strings.Count(strings.Join(ledger.calls(), " "), "finish:"); got != 2 {
		t.Fatalf("the ledger closed %d attempts (%v), want both — an attempt is an interval and it closes on the refusal too", got, ledger.calls())
	}
	if !approvals.IsApproved(proposal) {
		t.Fatal("the fenced refusal consumed the approval — the fence is not an answer to the person's yes")
	}
	// The read did not happen: no body was recorded for this call, and a
	// recorded body is the only thing a tool call that produced bytes
	// leaves behind (nocx-hp8p2.13). A refusal-as-answer must not become a
	// refusal-as-permission.
	if got := ledger.recordedCaptures(); len(got) != 0 {
		t.Fatalf("the fenced call recorded %d result bodies (%+v), want none — nothing was read", len(got), got)
	}
	if run.asks != 1 {
		t.Fatalf("the person was asked %d times, want exactly the one question the approval answered", run.asks)
	}

	// And the next attempt sees the same thing: the refusal is standing in
	// the only sense that matters here — the fence has not moved, so a
	// second identical call is refused identically rather than re-asked.
	again, againErr := mw.kernel.Invoke(context.Background(), "files.read", "call_1", args)
	if againErr != nil {
		t.Fatalf("the second attempt at the same fenced call returned %v, want the same refusal", againErr)
	}
	if want := refusalResult("files.read", RefusedOutOfScope, ""); again != want {
		t.Fatalf("the second attempt answered %q, want the same refusal %q", again, want)
	}
}

// TEST — the RUN's terminal state, which is the part an error assertion
// cannot see: a run that ends quietly and a run that answers look the same to
// a check that reads only the error. So this one drives the real engine
// against the fake provider and asserts the model was told, answered, and the
// run completed.
func TestAsk_AFenceRefusalAfterApprovalContinuesTheRun(t *testing.T) {
	grant, link, beyond := fenceSymlink(t)
	args := fmt.Sprintf(`{"path":%q}`, link)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	f, srv := newFakeOpenAI(reProposingModel("files.read", args))
	defer srv.Close()

	cl, _, clErr := NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}

	// 1. The ask suspends on the symlink the lexical predicate let through.
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil })
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}

	// 2. The person says yes to exactly that proposal.
	if !approvals.Approve(Approval{
		RunID: asked.Request.RunID, Attempt: asked.Request.Attempt, Tool: asked.Request.Tool,
		CallID: asked.Request.CallID, ArgHash: asked.Request.ArgHash,
	}) {
		t.Fatalf("the exact proposal the kernel asked about (%s %s) was not pending", asked.Request.Tool, asked.Request.CallID)
	}

	// 3. The resume: the fence refuses the approved call, and the run must
	//    reach a terminal state of its own accord — an ANSWER.
	var answer strings.Builder
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(e AskEvent) error {
		if e.Kind == AskAnswer {
			answer.WriteString(e.Text)
		}
		return nil
	})
	var again *ApprovalRequestedError
	if errors.As(err, &again) {
		t.Fatalf("the resume asked about %s again after the person approved it", again.Request.CallID)
	}
	if err != nil {
		t.Fatalf("resume Ask error = %v — the fence's refusal ended the run instead of answering it", err)
	}
	if !strings.Contains(answer.String(), "ok") {
		t.Fatalf("answer = %q, want the model's reply after the refusal — a run that ends quietly is not a run that answered", answer.String())
	}

	// The model was told, in our words, in that call's own slot.
	body := f.body()
	if !strings.Contains(body, "REFUSED") {
		t.Fatalf("the refusal never reached the provider: %s", body)
	}
	for _, w := range []string{"filesystem:", "path outside the grant", "NodeRunError", "node path"} {
		if strings.Contains(body, w) {
			t.Fatalf("the refusal carries %q — the person and the model get our sentence, not the capability's or the framework's: %s", w, body)
		}
	}
	// The fence held: the bytes never left the machine.
	if strings.Contains(body, beyond) {
		t.Fatalf("the protected bytes reached the provider: %s", body)
	}
}
