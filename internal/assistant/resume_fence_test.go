package assistant

// The resume path's two claims, which the design (§5.3) marked "to verify
// before implementation" and nobody has since. They are NOT one claim, and
// conflating them is how a privilege escalation hides:
//
//   1. An EXACT approval is honoured. A call suspended for a person's answer,
//      answered yes, and then resumed unchanged must not raise the question a
//      second time. If it does, the answer means nothing and a person answers
//      for ever — the ask-forever loop the resume exists to end.
//
//   2. A narrowed CAPABILITY FENCE still refuses, whatever approval the call
//      carries. content.OutOfScopeRowScope is an operator's selector, which a
//      person may widen, so the policy ASKS; content.OutOfScopeFence is the
//      run's immutable bound, and NO answer a person can give makes the
//      resource reachable. The failure to rule out is a resume that treats an
//      approval as sufficient and skips the fence — a yes reaching past a
//      bound that exists precisely because no yes may reach past it.
//
// The two are a pair in the AGENTS.md sense: (2) is a "refuses when…" and (1)
// is its "and on a normal machine it succeeds".
//
// The seam is effectKernel.Invoke — "the carrier-neutral entry point, and the
// only one" — because it returns the suspension as OUR typed value, where the
// eino adapter beside it has already translated it into a framework interrupt.
// The file-version re-validation the same resume performs
// (RefusedFileChanged) is asserted here on its passing side; its failing side
// is TestMiddlewareApprovalRefusesChangedFileBeforeExecution, and this test
// sits alongside that mechanism rather than beside a second one.

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

// resumeRun counts the QUESTIONS A PERSON WAS SHOWN across a run's whole
// life, not per call. The count is the assertion: a test reading only the
// happy return cannot tell "asked once" from "asked twice", and "asked
// twice" is the entire defect.
type resumeRun struct {
	t    *testing.T
	asks int
}

// invoke drives one proposal through the kernel and returns either the
// model-facing result or the question it raised — never both.
func (r *resumeRun) invoke(mw *policyMiddleware, callID, args string) (string, *ApprovalRequest) {
	r.t.Helper()
	out, err := mw.kernel.Invoke(context.Background(), "files.read", callID, args)
	var asked *ApprovalRequestedError
	if errors.As(err, &asked) {
		r.asks++
		if asked.Request == nil {
			r.t.Fatalf("%s: the suspension carried no request", callID)
		}
		return "", asked.Request
	}
	if err != nil {
		r.t.Fatalf("%s: unexpected error: %v", callID, err)
	}
	return out, nil
}

// approveExactly records the yes agent.approve records: the five binding
// fields of the proposal the kernel actually asked about, and nothing else.
func approveExactly(t *testing.T, approvals *ApprovalStore, req *ApprovalRequest) Approval {
	t.Helper()
	ap := Approval{
		RunID:   req.RunID,
		Attempt: req.Attempt,
		Tool:    req.Tool,
		CallID:  req.CallID,
		ArgHash: req.ArgHash,
	}
	if !approvals.Approve(ap) {
		t.Fatalf("the exact proposal the kernel asked about (%s %s) was not pending", req.Tool, req.CallID)
	}
	return ap
}

// TEST A — the exact approval is honoured on resume, and the person is asked
// ONCE for the whole life of the run.
//
// The invariant has both ends: from the moment the approval is recorded until
// the call's result returns, no second question is raised — asserted as a
// count over the whole run, not as the absence of an error on one call.
func TestResume_AnExactApprovalIsHonouredAndRaisesNoSecondAsk(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the approved bytes")
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)
	run := &resumeRun{t: t}

	// The question. An ask row suspends the call and binds the request to
	// the exact proposal.
	out, req := run.invoke(mw, "call_1", args)
	if req == nil {
		t.Fatalf("an ask row did not ask; it answered %q", out)
	}
	if req.OutOfScope != nil {
		t.Fatalf("an in-scope proposal carried an out-of-scope fact %+v — this test would then be about the wrong question", req.OutOfScope)
	}
	proposal := approveExactly(t, approvals, req)

	// The re-validation the resume performs is not vacuous: the approval
	// captured this path's identity, so the RefusedFileChanged check below
	// has something to compare. Without this the resume could be skipping
	// the check entirely and the test could not tell.
	versions, captured := approvals.ApprovedFileVersions(proposal)
	if !captured || len(versions) != 1 || versions[0].Path != path {
		t.Fatalf("approved file versions = %+v (captured=%v), want exactly the proposed path %s", versions, captured, path)
	}

	// The resume: the same run, the same attempt, the same call, re-driven
	// through the whole pipeline. The person answered; the call must run,
	// and the question must not be raised again.
	out, again := run.invoke(mw, "call_1", args)
	if again != nil {
		t.Fatalf("the resume asked about %s %s again after the person approved it — a person can never get past this question",
			again.Tool, again.CallID)
	}
	if !strings.Contains(out, "the approved bytes") {
		t.Fatalf("the resumed call returned %q, want the file's contents — the approval must let the call PROCEED, not merely stop asking", out)
	}
	if run.asks != 1 {
		t.Fatalf("the person was asked %d times over this run, want exactly once", run.asks)
	}

	// The other end of the interval: the yes still covers the exact
	// proposal, and the version it was given for is still the one on disk.
	// (Its failing half is TestMiddlewareApprovalRefusesChangedFileBeforeExecution.)
	if !approvals.IsApproved(proposal) {
		t.Fatal("the approval did not survive the call it authorised")
	}
	if err := approvals.VerifyApprovedFileVersions(proposal); err != nil {
		t.Fatalf("the approved path no longer verifies after the call: %v", err)
	}

	// Two attempts and no more: the escalation's own interrupted attempt,
	// and the approved execution. A third would be a second ask that
	// somehow recorded itself.
	if got := ledger.started(); got != 2 {
		t.Fatalf("the ledger opened %d attempts (%v), want 2 — the escalation and the approved call", got, ledger.calls())
	}
}

// fencedResume mints the state Test B is about: one run answered under a wide
// capability, resumed under a NARROWED one. inside is what the resumed run may
// reach; the returned path is a real file outside it and inside the original.
func fencedResume(t *testing.T) (wide content.Grant, narrowedFence string, path string) {
	t.Helper()
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	path = filepath.Join(outside, "b.txt")
	writeFile(t, path, "beyond the fence")
	wide = askEveryTimeMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: root},
	})
	return wide, inside, path
}

// TEST B — a narrowed capability fence refuses THROUGH the approval.
//
// The call carries the strongest answer a person can give: the exact yes to
// this very proposal, AND the standing "allow in this session" on the row
// itself, so the matrix says permit. Neither reaches past the fence, and the
// refusal must name the fence — a refusal for the right reason and a refusal
// for the wrong reason are the same string to an assertion that reads only
// the decision.
//
// The invariant is a span, and both ends are asserted: the resource lies
// outside the fence from before the approval is presented until after the
// call has returned, and nothing was attempted anywhere in between.
func TestResume_ANarrowedFenceRefusesThroughAnApproval(t *testing.T) {
	wide, inside, path := fencedResume(t)
	args := fmt.Sprintf(`{"path":%q}`, path)

	approvals := NewApprovalStore()
	run := &resumeRun{t: t}

	// The wide run really asks, and the person really answers yes — this is
	// an approval a person gave, not one a test fabricated.
	out, req := run.invoke(middlewareFor(t, wide, &fakeLedger{}, approvals), "call_1", args)
	if req == nil {
		t.Fatalf("the wide run did not ask; it answered %q — there would be no approval to refuse through", out)
	}
	proposal := approveExactly(t, approvals, req)

	// The resume, under a NARROWED capability: the same run and the same
	// call, re-minted the way the transport mints a resumed grant — through
	// ResolvePolicy with the session's new overlay — with a fence that no
	// longer covers the resource. The overlay is the strongest standing
	// answer there is: observe now PERMITS.
	narrowed := content.ResolvePolicy(askEveryTimeMatrix(), nil, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.DecisionPermit},
	}).AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: inside},
	})
	if narrowed.Policy.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatalf("the resumed matrix decides %q for observe, want permit — the test must be about the fence and not about a missing yes",
			narrowed.Policy.DecisionFor(content.EffectObserve))
	}

	// The cause, asserted specifically, at both ends of the call.
	outsideTheFence := func(when string) {
		t.Helper()
		v := narrowed.Policy.EvaluateResources(content.EffectObserve, content.DecisionPermit,
			[]content.GrantScope{{Kind: content.ResourcePath, ID: path}}, narrowed.Policy.RunFence())
		if v.Decision != content.DecisionRefuse || v.Cause != content.OutOfScopeFence {
			t.Fatalf("%s: verdict = %+v, want %q for cause %q — a fence miss is not an editable row miss",
				when, v, content.DecisionRefuse, content.OutOfScopeFence)
		}
	}
	outsideTheFence("before the resume")

	ledger := &fakeLedger{}
	if !approvals.IsApproved(proposal) {
		t.Fatal("the approval did not reach the resume — the test would then prove nothing about the fence")
	}
	out, asked := run.invoke(middlewareFor(t, narrowed, ledger, approvals), "call_1", args)

	if asked != nil {
		t.Fatalf("the fenced call raised a question (cause %+v): no answer a person can give widens a fence, so asking is a prompt whose only useful answer does not exist",
			asked.OutOfScope)
	}
	if want := refusalResult("files.read", RefusedOutOfScope, ""); out != want {
		t.Fatalf("the fenced call answered\n  %q\nwant the out-of-scope refusal\n  %q\n— a refusal for the wrong reason reads identically to one for the right reason",
			out, want)
	}
	if strings.Contains(out, "beyond the fence") {
		t.Fatal("the refusal carried the file's contents: the call reached past the fence and the refusal was cosmetic")
	}
	if got := ledger.started(); got != 0 {
		t.Fatalf("the fenced call opened %d attempts (%v), want none — a fenced call must not run", got, ledger.calls())
	}
	if run.asks != 1 {
		t.Fatalf("the person was asked %d times, want exactly the one question the wide run raised", run.asks)
	}

	// The other end: the yes is still on record and was still not enough,
	// and the resource is still outside the fence. The approval never
	// became the thing that decided this call.
	if !approvals.IsApproved(proposal) {
		t.Fatal("the fenced refusal consumed the approval — the fence is not an answer to the person's yes")
	}
	outsideTheFence("after the call returned")
}
