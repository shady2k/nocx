package assistant

// The approval chain's ledger thread (nocx-z9hj4, nocx-5dldy): an escalation
// is RECORDED — the proposal is an action entry whose payload names the exact
// binding, with its own interrupted attempt, and the approved call runs as a
// SUBSEQUENT attempt of that same entry (ADR-0020 decision 4). Asserted
// against a REAL content store by reading the thread back, never by
// asserting the writes (acceptance criterion 5). The egress gate's approved
// resume sends the RETAINED result — the exact bytes the person was shown —
// and never re-runs the tool.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// term dereferences an execution's termination reason for assertion.
func term(r *content.TerminationReason) content.TerminationReason {
	if r == nil {
		return ""
	}
	return *r
}

// ── the store seam (criterion 7's source of truth) ────────────────────────

func TestApprovalStore_Seam(t *testing.T) {
	store := NewApprovalStore()
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: "hash-a", EntryID: "entry-proposal"}

	if store.IsPending(ap) || store.IsApproved(ap) {
		t.Fatal("a proposal nobody was asked about is pending or approved")
	}
	// A yes to a question nobody was asked records nothing.
	if store.Approve(ap) {
		t.Fatal("Approve of an unknown proposal returned true — a stale or unknown approval id must not resume anything")
	}
	if store.IsApproved(ap) {
		t.Fatal("Approve of an unknown proposal created an approval")
	}

	store.Request(ap)
	if !store.IsPending(ap) {
		t.Fatal("Requested proposal is not pending")
	}
	if eid, ok := store.EntryIDFor(ap); !ok || eid != "entry-proposal" {
		t.Fatalf("EntryIDFor = %q/%v, want the recorded proposal entry", eid, ok)
	}

	// The wire's approve carries only the five binding fields; the entry the
	// proposal was recorded under is the pending record's own.
	if !store.Approve(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: "hash-a"}) {
		t.Fatal("Approve of the pending proposal returned false")
	}
	if store.IsPending(ap) {
		t.Fatal("an answered proposal is still pending")
	}
	if !store.IsApproved(ap) {
		t.Fatal("the approved proposal is not approved")
	}
	if eid, ok := store.EntryIDFor(ap); !ok || eid != "entry-proposal" {
		t.Fatalf("EntryIDFor after approve = %q/%v, want the proposal entry preserved", eid, ok)
	}
	// A second approve of the same proposal answers nothing new.
	if store.Approve(ap) {
		t.Fatal("a second yes was recorded as a fresh decision")
	}

	// Retention: the withheld bytes live until cleared.
	if _, _, ok := store.RetainedResult(ap); ok {
		t.Fatal("nothing was retained yet")
	}
	store.Retain(ap, "withheld result", false)
	if out, wasErr, ok := store.RetainedResult(ap); !ok || out != "withheld result" || wasErr {
		t.Fatalf("RetainedResult = %q/%v/%v, want the withheld bytes", out, wasErr, ok)
	}
	store.ClearRetained(ap)
	if _, _, ok := store.RetainedResult(ap); ok {
		t.Fatal("cleared retention is still present")
	}
}

// ── criterion 5 / nocx-5dldy: the thread, read back from a real store ─────

// TestAsk_EscalationRecordsTheProposalThread: an escalation is a ledger
// fact — one action entry whose payload names tool, effect, arguments and
// the exact binding, and its own attempt closed interrupted. The call that
// is asking has NOT run: no artifact, no completed attempt.
func TestAsk_EscalationRecordsTheProposalThread(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "must not be read yet")

	ledger := realLedger(t)
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.RunID = "run-1"
	p.Attempt = 1
	err := cl.Ask(context.Background(), p, func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}

	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	entryID, ok := approvals.EntryIDFor(ap)
	if !ok {
		t.Fatal("the escalation recorded no proposal entry")
	}
	e, err := ledger.Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("proposal entry: %v (err %v)", e, err)
	}
	if e.Kind != content.EntryAction || e.Intent != "files.read" {
		t.Fatalf("proposal entry = kind %q intent %q, want action files.read", e.Kind, e.Intent)
	}
	var payload struct {
		Tool     string `json:"tool"`
		Approval struct {
			RunID   string `json:"runId"`
			Attempt int    `json:"attempt"`
			Tool    string `json:"tool"`
			CallID  string `json:"callId"`
			ArgHash string `json:"argHash"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		t.Fatalf("proposal payload: %v", err)
	}
	if payload.Tool != "files.read" || payload.Approval.RunID != "run-1" ||
		payload.Approval.Attempt != 1 || payload.Approval.CallID != "call_1" ||
		payload.Approval.ArgHash != canonicalArgHash(args) {
		t.Fatalf("proposal payload = %+v, want the exact binding recorded", payload)
	}
	if len(e.Executions) != 1 {
		t.Fatalf("proposal executions = %+v, want exactly one — the escalation's own attempt", e.Executions)
	}
	ex := e.Executions[0]
	if ex.Attempt != 1 {
		t.Fatalf("proposal attempt = %d, want 1", ex.Attempt)
	}
	if ex.TerminationReason == nil || term(ex.TerminationReason) != content.TermInterrupted {
		t.Fatalf("proposal attempt closed %v, want interrupted — the call that is asking has not run", ex.TerminationReason)
	}
	if len(ex.Artifacts) != 0 {
		t.Fatalf("proposal carried %d artifacts — the call has not run", len(ex.Artifacts))
	}
}

// TestAsk_ApprovedResumeRunsAsSubsequentAttempt (nocx-5dldy, criterion 2
// yes-half): after the person approves the EXACT proposal, the resume runs
// the call as its OWN execution — attempt 2 of the proposal's own entry,
// distinguishable from the escalation that preceded it (attempt 1,
// interrupted). The thread is read back from the real store.
func TestAsk_ApprovedResumeRunsAsSubsequentAttempt(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")

	ledger := realLedger(t)
	approvals := NewApprovalStore()

	// Request 1: the model proposes the call (escalates). Request 2 (the
	// resume): the model proposes the SAME call (approved, runs). Request 3:
	// the answer.
	var n atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			streamToolCalls(w, toolCallSpec{name: "files.read", args: args})
			return
		}
		streamOK(w)
	}
	f, srv := newFakeOpenAI(handler)
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.RunID = "run-1"
	p.Attempt = 1

	// Pass 1: the escalation.
	err := cl.Ask(context.Background(), p, func(string) error { return nil })
	var want *ApprovalRequestedError
	if !errors.As(err, &want) {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}

	// The person approves the exact proposal (the wire's five-field binding;
	// the store fills the entry from the pending record).
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	entryID, ok := approvals.EntryIDFor(ap)
	if !ok {
		t.Fatal("no proposal entry recorded")
	}
	if !approvals.Approve(ap) {
		t.Fatal("the pending proposal was not approved")
	}

	// Pass 2: the resume. The same messages, the same binding — the call runs.
	if askErr := cl.Ask(context.Background(), p, func(string) error { return nil }); askErr != nil {
		t.Fatalf("the approved resume failed: %v", askErr)
	}
	if f.requests.Load() != 3 {
		t.Fatalf("the engine made %d model requests, want 3 — escalate, resume-call, answer", f.requests.Load())
	}

	e, err := ledger.Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("proposal entry: %v (err %v)", e, err)
	}
	if len(e.Executions) != 2 {
		t.Fatalf("thread executions = %+v, want exactly two — the escalation and the approved call", e.Executions)
	}
	var escalation, approved *content.Execution
	for i := range e.Executions {
		ex := &e.Executions[i]
		switch ex.Attempt {
		case 1:
			escalation = ex
		case 2:
			approved = ex
		}
	}
	if escalation == nil || escalation.TerminationReason == nil || term(escalation.TerminationReason) != content.TermInterrupted {
		t.Fatalf("attempt 1 = %+v, want the interrupted escalation", escalation)
	}
	if approved == nil || approved.TerminationReason == nil || term(approved.TerminationReason) != content.TermCompleted {
		t.Fatalf("attempt 2 = %+v, want the completed approved call — its own execution, distinguishable from the escalation", approved)
	}
	if e.Status != content.EntrySuccess {
		t.Fatalf("thread status = %q, want success", e.Status)
	}
}

// ── the egress gate's approved resume: retained bytes, never a re-run ─────

// TestMiddleware_ApprovedEgressResumeSendsRetained: runWithRetained returns
// the EXACT withheld bytes the person approved sending and never touches the
// capability — a resume that re-ran the tool would repeat the effect and
// could produce a different result than the one approved.
func TestMiddleware_ApprovedEgressResumeSendsRetained(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "the key is sk-proj-abcdefghijklmnopqrstuvwx")

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, ledger, approvals)

	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	approvals.Retain(ap, "withheld: sk-proj-abcdefghijklmnopqrstuvwx", false)

	ran := false
	out, err := mw.runWithRetained(agenttools.Tool{Declaration: agenttools.Declaration{Name: "files.read"}}, "call_1", context.Background(), &countingCapability{called: &ran}, []byte(args))
	if err != nil {
		t.Fatalf("runWithRetained: %v", err)
	}
	if out != "withheld: sk-proj-abcdefghijklmnopqrstuvwx" {
		t.Fatalf("runWithRetained returned %q, want the retained bytes", out)
	}
	if ran {
		t.Fatal("the approved egress resume re-ran the tool — the effect repeated and the result could differ from what was approved")
	}
}

// countingCapability is a capability that records whether it was invoked —
// runWithRetained must never reach it on the retained path.
type countingCapability struct {
	called *bool
}

func (c *countingCapability) Read(context.Context, string) (string, error) {
	*c.called = true
	return "ran", nil
}

// TestAsk_ApprovedEgressResumeThread: an egress finding suspends, the person
// approves, and the resume completes the thread — attempt 2 of the SAME
// entry, the retained result sent, the retention dropped. The finding never
// re-suspends: one question, answered once.
func TestAsk_ApprovedEgressResumeThread(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	const secret = "known-secret-value-123"
	writeFile(t, filepath.Join(dir, "a.txt"), "deploy key: "+secret)

	ledger := realLedger(t)
	approvals := NewApprovalStore()

	var n atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			streamToolCalls(w, toolCallSpec{name: "files.read", args: args})
			return
		}
		streamOK(w)
	}
	_, srv := newFakeOpenAI(handler)
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{value: secret, name: "github-token"}, nil)
	p.RunID = "run-1"
	p.Attempt = 1

	// Pass 1: the finding suspends; the attempt that ran closes interrupted.
	err := cl.Ask(context.Background(), p, func(string) error { return nil })
	var egErr *EgressRequestedError
	if !errors.As(err, &egErr) {
		t.Fatalf("Ask error = %v, want the egress suspension", err)
	}
	if len(egErr.Request.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", egErr.Request.Findings)
	}

	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	entryID, ok := approvals.EntryIDFor(ap)
	if !ok {
		t.Fatal("the egress escalation recorded no proposal entry")
	}
	if _, _, retained := approvals.RetainedResult(ap); !retained {
		t.Fatal("the withheld result was not retained")
	}
	if !approvals.Approve(ap) {
		t.Fatal("the pending egress proposal was not approved")
	}

	// Pass 2: the resume. The tool does not re-run (the retained result is
	// sent as approved), the finding does not re-suspend, the run completes.
	if askErr := cl.Ask(context.Background(), p, func(string) error { return nil }); askErr != nil {
		t.Fatalf("the approved egress resume failed: %v", askErr)
	}
	if _, _, retained := approvals.RetainedResult(ap); retained {
		t.Fatal("the retained result survived the approved resume — the bytes were sent as decided")
	}

	e, err := ledger.Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("thread entry: %v (err %v)", e, err)
	}
	if len(e.Executions) != 2 {
		t.Fatalf("thread executions = %+v, want two — the withheld attempt and the approved one", e.Executions)
	}
	var withheld, approved *content.Execution
	for i := range e.Executions {
		ex := &e.Executions[i]
		switch ex.Attempt {
		case 1:
			withheld = ex
		case 2:
			approved = ex
		}
	}
	if withheld == nil || withheld.TerminationReason == nil || term(withheld.TerminationReason) != content.TermInterrupted {
		t.Fatalf("attempt 1 = %+v, want the interrupted withheld pass", withheld)
	}
	if approved == nil || approved.TerminationReason == nil || term(approved.TerminationReason) != content.TermCompleted {
		t.Fatalf("attempt 2 = %+v, want the completed approved pass", approved)
	}
	if e.Status != content.EntrySuccess {
		t.Fatalf("thread status = %q, want success", e.Status)
	}
}
