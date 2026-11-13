package assistant

// The kernel's approver when a service is wired (nocx-a62cl.3.1).
//
// ADR-anntnxdj §3: a person can block a call, and a service owns that. The
// chain's Approver prepares the question exactly as the adapter does, puts it
// to the service, and returns the person's answer to the SAME call — which then
// goes on through the guards and runs once. With no service wired the adapter
// keeps the suspension the framework path still needs, and that is the second
// half of what these tests hold: nothing else about the kernel changed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// recordingQuestioner stands in for the transport's approval service: it keeps
// every question it was asked, records the person's decision in the store the
// guards read (which is what agent.approve does before it wakes the call), and
// answers with the same value — optionally doing something in between, which is
// how "while the question was open" is expressed without a clock.
type recordingQuestioner struct {
	store  *ApprovalStore
	asked  []*ApprovalRequest
	answer Answer
	kind   DeclineKind
	before func()
}

func (q *recordingQuestioner) Ask(_ context.Context, req *ApprovalRequest) Answer {
	q.asked = append(q.asked, req)
	proposal := Approval{
		RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID,
		ArgHash: req.ArgHash, Effect: req.Effect,
	}
	switch q.answer.(type) {
	case Approved:
		q.store.Approve(proposal)
	case Declined:
		q.store.Decline(proposal, q.kind)
	}
	if q.before != nil {
		q.before()
	}
	return q.answer
}

// kernelForQuestioner builds the kernel with the service wired through the
// seam the transport hands its approval store on.
func kernelForQuestioner(t *testing.T, grant content.Grant, ledger AttemptLedger, q Questioner) *effectKernel {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, ledger, nil, &fakeKnownMaterial{}, "run-1", "", 1, "turn-entry", nil, Attachments{}, nil, nil, toolSeams{questioner: q})
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}
	return k
}

// TestAnAskIsPutToTheWiredServiceAndTheSameCallRunsAfterYes is the blocking
// shape itself: Invoke does not return while the question is open, the service
// is handed the prepared question with the exact binding, and the yes releases
// the very call that asked — which runs once.
func TestAnAskIsPutToTheWiredServiceAndTheSameCallRunsAfterYes(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the bytes the person agreed to")
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "the bytes the person agreed to") {
		t.Fatalf("result = %q, want the file the person approved reading", out)
	}
	if len(q.asked) != 1 {
		t.Fatalf("the service was asked %d questions, want exactly 1", len(q.asked))
	}
	req := q.asked[0]
	if req.RunID != "run-1" || req.Attempt != 1 || req.Tool != "files.read" || req.CallID != "call-1" {
		t.Fatalf("question binding = %+v, want run-1/1/files.read/call-1", req)
	}
	if req.ArgHash != canonicalArgHash(args) {
		t.Fatalf("question argHash = %q, want the canonical hash of what runs", req.ArgHash)
	}
	if req.Effect != content.EffectObserve {
		t.Fatalf("question effect = %q, want observe — the gate decided it, not the test", req.Effect)
	}
	// The proposal's own attempt, then the approved call's — the interval
	// ADR-0020 decision 4 states.
	if got := ledger.started(); got != 2 {
		t.Fatalf("ledger opened %d attempts, want 2 (the proposal and the call it approved)", got)
	}
	if !approvals.IsApproved(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}) {
		t.Fatal("the person's yes was not the answer the store holds")
	}
}

// TestTheAnswerReachesTheGuardsThatFollowIt: the file is replaced while the
// question is open — inside the service's own Answer — and the evidence guard
// that runs after it refuses the call. This is what "an answer is not the last
// word" means for the blocking path, and it can only hold if the guards run
// after the answer on the same call.
func TestTheAnswerReachesTheGuardsThatFollowIt(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the bytes the person agreed to")
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}, before: func() {
		writeFile(t, path, "the bytes nobody agreed to")
	}}
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("the changed file returned an error, want the refusal as the call's result: %v", err)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("result = %q, want the evidence refusal", out)
	}
	if strings.Contains(out, "the bytes nobody agreed to") {
		t.Fatal("the refused call read the file's new bytes — the guards ran before the answer")
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d attempts, want only the proposal's — a refused call never becomes an attempt", got)
	}
}

// TestTheServicesNoIsTheCallsResultAndNothingRuns: a decline is an answer, so
// the run goes on with the person's own sentence in the refused call's slot and
// the tool's body is never entered.
func TestTheServicesNoIsTheCallsResultAndNothingRuns(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "never read")
	args := fmt.Sprintf(`{"path":%q}`, path)

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Declined{Reason: PersonDeclineResult("files.read", DeclineCallOnce)}, kind: DeclineCallOnce}
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("a decline must be the call's result, not a run failure: %v", err)
	}
	if !strings.Contains(out, "the person declined your call to files.read") {
		t.Fatalf("result = %q, want the person's refusal in the call's own slot", out)
	}
	if strings.Contains(out, "never read") {
		t.Fatal("a declined call read the file")
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d attempts, want only the proposal's", got)
	}
}

// TestTheChainApproverIsTheServiceWhenOneIsWired: the composition is the seam's
// proof. A wired service is the chain's Approver; without one the adapter is,
// and its suspension is unchanged.
func TestTheChainApproverIsTheServiceWhenOneIsWired(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})
	if _, ok := k.authorityChain(&kernelCall{kernel: k}).Approver.(kernelApprover); !ok {
		t.Fatalf("approver without a service = %T, want the kernelApprover adapter", k.authorityChain(&kernelCall{kernel: k}).Approver)
	}
	k.questioner = &recordingQuestioner{answer: Approved{}}
	if _, ok := k.authorityChain(&kernelCall{kernel: k}).Approver.(serviceApprover); !ok {
		t.Fatalf("approver with a service = %T, want serviceApprover", k.authorityChain(&kernelCall{kernel: k}).Approver)
	}
}

// TestWithNoServiceTheSuspensionIsUnchanged keeps the adapter's path honest:
// the question is still recorded, still prepared the same way, and still leaves
// as the framework's suspension — which is the path nocx-a62cl.3.3 and 3.4
// still serve.
func TestWithNoServiceTheSuspensionIsUnchanged(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "not yet")
	args := fmt.Sprintf(`{"path":%q}`, path)
	approvals := NewApprovalStore()
	k := kernelFor(t, grant, &fakeLedger{})
	k.approvals = approvals

	_, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Invoke error = %v, want the adapter's suspension", err)
	}
	proposal := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}
	if !approvals.IsPending(proposal) {
		t.Fatal("the question was not recorded in the store the person's answer is checked against")
	}
}
