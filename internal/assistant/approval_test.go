package assistant

// The approval chain's ledger thread (nocx-z9hj4, nocx-5dldy): an escalation
// is RECORDED — the proposal is an action entry whose payload names the exact
// binding, with its own interrupted attempt, and the approved call runs as a
// SUBSEQUENT attempt of that same entry (ADR-0020 decision 4). Asserted
// against a REAL content store by reading the thread back, never by
// asserting the writes (acceptance criterion 5).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
}

func TestApprovalStore_InvocationForPreservesUnresolvedResources(t *testing.T) {
	store := NewApprovalStore()
	invocation := content.Invocation{
		Commands: [][]string{{"cat", "$LOGFILE"}},
		Parsed:   true,
		Resources: content.ResourceReport{Unresolved: []content.UnresolvedResource{{
			Path: "$LOGFILE", Verb: content.ResourceRead,
			Reason: "could not resolve $LOGFILE without executing shell expansion",
		}}},
	}
	ap := Approval{
		RunID: "run-dynamic", Attempt: 1, Tool: "session.run",
		CallID: "call-dynamic", ArgHash: "hash-dynamic",
		CommandInvocation: true, Invocation: invocation,
	}
	store.Request(ap)

	got, parsed, command := store.InvocationFor(ap)
	if !parsed || !command {
		t.Fatalf("InvocationFor flags = parsed %v, command %v, want true/true", parsed, command)
	}
	if len(got.Resources.Unresolved) != 1 ||
		got.Resources.Unresolved[0].Path != "$LOGFILE" {
		t.Fatalf("InvocationFor resources = %+v, want the unresolved shell variable", got.Resources)
	}
	saved := content.InvocationRule{
		Selector: content.InvocationSelector{Exact: invocation.Commands},
		Decision: content.DecisionPermit,
	}
	if saved.Matches(got) {
		t.Fatal("a cloned unresolved invocation matched a standing rule")
	}
}

func TestApprovalStore_DeclinePreservesEffectForStandingWrite(t *testing.T) {
	store := NewApprovalStore()
	ap := Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1",
		ArgHash: "hash-a",
	}
	store.Request(ap)
	store.NoteEffect(Approval{
		RunID: ap.RunID, Attempt: ap.Attempt, Tool: ap.Tool, CallID: ap.CallID,
		ArgHash: ap.ArgHash, Effect: content.EffectObserve,
	})

	if !store.Decline(ap, DeclineCallAlways) {
		t.Fatal("Decline of the pending proposal returned false")
	}
	if effect, ok := store.EffectFor(ap); !ok || effect != content.EffectObserve {
		t.Fatalf("EffectFor after decline = %q/%v, want observe/true for the standing-write path", effect, ok)
	}
	if store.Decline(ap, DeclineCallAlways) {
		t.Fatal("a second Decline of the same proposal won after the first settlement")
	}
}

// ── criterion 5 / nocx-5dldy: the thread, read back from a real store ─────

// TestAsk_EscalationRecordsTheProposalThread: an escalation is a ledger
// fact — one action entry whose payload names tool, effect, arguments and
// the exact binding, and its own attempt closed interrupted. The call that
// is asking has NOT run: no artifact, no completed attempt.
//
// The assertions run INSIDE the question, from the approver's own seam: that
// is the only moment at which the proposal exists and the call has not run,
// and a test that read the store after the answer would be reading a call
// that already happened.
func TestAsk_EscalationRecordsTheProposalThread(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "must not be read yet")

	ledger := realLedger(t)
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	q.before = func() {
		entryID, ok := approvals.EntryIDFor(ap)
		if !ok {
			t.Error("the escalation recorded no proposal entry")
			return
		}
		e, err := ledger.Entry(context.Background(), entryID)
		if err != nil || e == nil {
			t.Errorf("proposal entry: %v (err %v)", e, err)
			return
		}
		if e.Kind != content.EntryAction || e.Intent != "files.read" {
			t.Errorf("proposal entry = kind %q intent %q, want action files.read", e.Kind, e.Intent)
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
			t.Errorf("proposal payload: %v", err)
			return
		}
		if payload.Tool != "files.read" || payload.Approval.RunID != "run-1" ||
			payload.Approval.Attempt != 1 || payload.Approval.CallID != "call_1" ||
			payload.Approval.ArgHash != canonicalArgHash(args) {
			t.Errorf("proposal payload = %+v, want the exact binding recorded", payload)
		}
		if len(e.Executions) != 1 {
			t.Errorf("proposal executions = %+v, want exactly one — the escalation's own attempt", e.Executions)
			return
		}
		ex := e.Executions[0]
		if ex.Attempt != 1 {
			t.Errorf("proposal attempt = %d, want 1", ex.Attempt)
		}
		if ex.TerminationReason == nil || term(ex.TerminationReason) != content.TermInterrupted {
			t.Errorf("proposal attempt closed %v, want interrupted — the call that is asking has not run", ex.TerminationReason)
		}
		if len(ex.Artifacts) != 0 {
			t.Errorf("proposal carried %d artifacts — the call has not run", len(ex.Artifacts))
		}
	}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.RunID = "run-1"
	p.Attempt = 1
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask error = %v, want the approved call to run", err)
	}
	if len(q.asked) != 1 {
		t.Fatalf("the person was asked %d times, want exactly 1", len(q.asked))
	}
}

// TestAsk_AnApprovedCallRunsAsSubsequentAttempt (nocx-5dldy, criterion 2
// yes-half): after the person approves the EXACT proposal, the call runs as
// its OWN execution — attempt 2 of the proposal's own entry, distinguishable
// from the escalation that preceded it (attempt 1, interrupted). The thread is
// read back from the real store.
func TestAsk_AnApprovedCallRunsAsSubsequentAttempt(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")

	ledger := realLedger(t)
	approvals := NewApprovalStore()

	// One proposal, then the answer: the approved call runs in the SAME
	// drive, so the model is asked once for the proposal and once for the
	// answer written from its result.
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.RunID = "run-1"
	p.Attempt = 1
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("the approved call failed: %v", err)
	}

	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	entryID, ok := approvals.EntryIDFor(ap)
	if !ok {
		t.Fatal("no proposal entry recorded")
	}
	e, err := ledger.Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("thread entry: %v (err %v)", e, err)
	}
	if len(e.Executions) != 2 {
		t.Fatalf("thread executions = %+v, want two — the proposal's interrupted attempt and the approved call", e.Executions)
	}
	var proposal, approved *content.Execution
	for i := range e.Executions {
		ex := &e.Executions[i]
		switch ex.Attempt {
		case 1:
			proposal = ex
		case 2:
			approved = ex
		}
	}
	if proposal == nil || proposal.TerminationReason == nil || term(proposal.TerminationReason) != content.TermInterrupted {
		t.Fatalf("attempt 1 = %+v, want the interrupted proposal", proposal)
	}
	if approved == nil || approved.TerminationReason == nil || term(approved.TerminationReason) != content.TermCompleted {
		t.Fatalf("attempt 2 = %+v, want the completed approved call", approved)
	}
	if e.Status != content.EntrySuccess {
		t.Fatalf("thread status = %q, want success", e.Status)
	}
	// CRITERION 3 — the proposal the assistant made, and the call a person
	// approved, BOTH stay `assistant`: a person who allows the call
	// authorised somebody else's intent, they did not submit it
	// (schemaV1's source comment). policy.go writes the ACTION row with
	// SourceAssistant; the approved call runs as attempt 2 of the SAME
	// entry (ADR-0020 decision 4), so no second submit exists to convert,
	// and the store never rewrites source on approval (no update path).
	if e.Source != content.SourceAssistant {
		t.Fatalf("the approved action entry carries source %q, want assistant — approval must not become the person's submission", e.Source)
	}
}

// ── the egress question: one call, one attempt, the exact bytes ────────────

// TestAsk_AnApprovedEgressQuestionCompletesTheThread: a finding withholds the
// result, the person approves, and the SAME call's attempt completes — the
// tool ran once and nothing re-ran (ADR-anntnxdj §2). Unlike a policy
// approval, an egress answer is not a retry: the call never restarted, so
// there is one attempt, and it is the one that produced the withheld bytes.
func TestAsk_AnApprovedEgressQuestionCompletesTheThread(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	const secret = "known-secret-value-123"
	writeFile(t, filepath.Join(dir, "a.txt"), "deploy key: "+secret)

	ledger := realLedger(t)
	approvals := NewApprovalStore()

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	p := askParamsWith(srv.URL, &grant, ledger, approvals, &knownMatcher{value: secret, name: "github-token"}, nil)
	p.RunID = "run-1"
	p.Attempt = 1
	p.Questioner = q
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("the approved egress call failed: %v", err)
	}
	if len(q.egressAsked) != 1 {
		t.Fatalf("the person was asked %d egress questions, want exactly 1", len(q.egressAsked))
	}
	if len(q.egressAsked[0].Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", q.egressAsked[0].Findings)
	}

	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	entryID, ok := approvals.EntryIDFor(ap)
	if !ok {
		t.Fatal("the egress question recorded no proposal entry")
	}
	e, err := ledger.Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("thread entry: %v (err %v)", e, err)
	}
	if len(e.Executions) != 1 {
		t.Fatalf("thread executions = %+v, want one — the call ran once and its result was released, not re-run", e.Executions)
	}
	ex := e.Executions[0]
	if ex.TerminationReason == nil || term(ex.TerminationReason) != content.TermCompleted {
		t.Fatalf("the attempt closed %v, want completed after the answer", ex.TerminationReason)
	}
	if e.Status != content.EntrySuccess {
		t.Fatalf("thread status = %q, want success", e.Status)
	}
}
