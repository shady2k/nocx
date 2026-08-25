package assistant

// The resume, as a REAL provider drives it (found from a live run: a person
// answered the same question over and over and the assistant never moved).
//
// The transport's resume does not restore an eino checkpoint — it calls Ask
// again with the ORIGINAL messages, so the model re-rolls its response and
// the provider mints a FRESH tool-call id for it. The approval is keyed by
// the id the first roll carried, so it matches nothing and the run asks
// again. Forever.
//
// The existing tests cannot see it: callThenAnswer serves the tool call on
// the FIRST completion only, so their resume never re-proposes anything.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// reProposingModel is a provider that keeps proposing the same call until it
// sees the tool's result, minting a new call id each time — what an
// OpenAI-compatible endpoint does.
func reProposingModel(name, args string) func(w http.ResponseWriter, r *http.Request) {
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"role":"tool"`) {
			streamOK(w)
			return
		}
		n++
		streamToolCalls(w, toolCallSpec{name: name, args: args, id: fmt.Sprintf("call_%d", n)})
	}
}

func TestAsk_ApprovedProposalResumesWhenTheModelReRolls(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(reProposingModel("files.read", args))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}

	// 1. The ask suspends: the person is asked about the first roll's call.
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil })
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}

	// 2. The person says yes to exactly that proposal — what agent.approve
	//    puts in the store.
	if !approvals.Approve(Approval{
		RunID:   asked.Request.RunID,
		Attempt: asked.Request.Attempt,
		Tool:    asked.Request.Tool,
		CallID:  asked.Request.CallID,
		ArgHash: asked.Request.ArgHash,
	}) {
		t.Fatal("the exact proposal the middleware asked about was not pending")
	}

	// 3. The resume: the same run, re-driven. The person answered; the call
	//    must run, and the person must NOT be asked the same question again.
	var got strings.Builder
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(e AskEvent) error {
		if e.Kind == AskAnswer {
			got.WriteString(e.Text)
		}
		return nil
	})
	var again *ApprovalRequestedError
	if errors.As(err, &again) {
		t.Fatalf("the resume asked the SAME question again (call id %q, approved %q): a person can never get past it",
			again.Request.CallID, asked.Request.CallID)
	}
	if err != nil {
		t.Fatalf("resume Ask error = %v, want the approved call to run", err)
	}
}

// twoProposalsThenAnswer is a provider that proposes ONE call per
// completion, a different resource each time, and answers once it has seen
// a tool result for the second. Each proposal carries its own call id,
// because that is what a provider does — and it is what makes the
// difference between "the resume restored the approved proposal" and "the
// model was asked to propose it again" visible.
func twoProposalsThenAnswer(name, firstArgs, secondArgs string) func(w http.ResponseWriter, r *http.Request) {
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			streamToolCalls(w, toolCallSpec{name: name, args: firstArgs, id: "call_1"})
		case 2:
			streamToolCalls(w, toolCallSpec{name: name, args: secondArgs, id: "call_2"})
		default:
			streamOK(w)
		}
	}
}

// TestAsk_ASessionAnswerStopsTheNextProposalAsking (nocx-v94ne): one run,
// two proposals of the SAME effect class. The person answers the first
// "allow in this session" — which is the answer that says stop asking me
// this — and the second one runs without a question.
//
// The two grants are what the transport really hands the engine across a
// suspension: the run asks under the workspace matrix, and the resumed
// attempt is minted AGAIN through content.ResolvePolicy with the session's
// new overlay (runGrantFor). Before nocx-v94ne the resume carried the grant
// minted with the question, so the answer could not reach the run it was
// given in, and every further call asked again.
func TestAsk_ASessionAnswerStopsTheNextProposalAsking(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first file")
	writeFile(t, filepath.Join(dir, "b.txt"), "second file")
	firstArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	secondArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(twoProposalsThenAnswer("files.read", firstArgs, secondArgs))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}

	// The question: the first proposal escalates.
	err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil })
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want the approval-requested suspension", err)
	}
	if asked.Request.Effect != content.EffectObserve {
		t.Fatalf("the proposal's effect = %q, want observe — the class the session answer is about", asked.Request.Effect)
	}

	// The answer: yes to this proposal, AND "in this session" for its
	// effect class. Both halves land where the product puts them — the
	// exact yes in the approval store, the standing part in the session
	// overlay the next grant is minted through.
	if !approvals.Approve(Approval{
		RunID: asked.Request.RunID, Attempt: asked.Request.Attempt, Tool: asked.Request.Tool,
		CallID: asked.Request.CallID, ArgHash: asked.Request.ArgHash,
	}) {
		t.Fatal("the exact proposal the middleware asked about was not pending")
	}
	answered := content.ResolvePolicy(askEveryTimeMatrix(), nil, content.SessionOverrides{
		content.EffectObserve: content.DecisionPermit,
	}).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})

	// The resume, under the re-minted grant: the restored call runs, the
	// model proposes a SECOND read, and nobody is asked about it.
	resumed := askParams(srv.URL, &answered, ledger, approvals)
	if askErr := cl.Ask(context.Background(), resumed, func(AskEvent) error { return nil }); askErr != nil {
		var again *ApprovalRequestedError
		if errors.As(askErr, &again) && again.Request != nil {
			t.Fatalf("the run asked again about %s %s after \"allow in this session\" — the answer never reaches the run it was given in (nocx-v94ne)",
				again.Request.Tool, again.Request.CallID)
		}
		t.Fatalf("the resumed run failed: %v", askErr)
	}
	// Both reads happened: the approved one and the one nobody was asked
	// about. Three starts — the escalation's own interrupted attempt, the
	// approved call, and the second proposal.
	if got := ledger.started(); got != 3 {
		t.Fatalf("the ledger recorded %d attempts (%v), want 3 — the escalation, the approved call, and the second read that was never asked about", got, ledger.calls())
	}
}

// TestAsk_TerminalRunsLeaveNoCheckpoint: the continuation exists for
// exactly as long as the question is open. ADR-0028 makes checkpoints
// process-lifetime state deleted on terminalization, and eino v0.9.13
// deletes nothing itself — the store's owner is responsible, which the
// framework says in as many words. So a suspended run holds one, and a run
// that has completed or failed holds none.
func TestAsk_TerminalRunsLeaveNoCheckpoint(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "approved read")
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(reProposingModel("files.read", args))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	impl, isClient := cl.(*client)
	if !isClient {
		t.Fatalf("newClient returned %T, want the concrete client whose store this asserts on", cl)
	}
	held := func() bool {
		_, ok := impl.checkpoints.resumable("run-1")
		return ok
	}

	// Suspended: the checkpoint is the only thing that can carry this run
	// past the person's answer, so it stays.
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the ask did not suspend")
	}
	if !held() {
		t.Fatal("a suspended run kept no continuation — nothing could resume it")
	}

	// Completed: the approved resume runs the call and the answer arrives.
	asked := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: canonicalArgHash(args)}
	if !approvals.Approve(asked) {
		t.Fatal("the proposal was not pending")
	}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("the approved resume failed: %v", err)
	}
	if held() {
		t.Fatal("a COMPLETED run kept its continuation: a checkpoint nobody may resume is a copy of the run held for the life of the process")
	}

	// Failed: the endpoint is gone, and a run that cannot even reach the
	// model leaves nothing behind either.
	srv.Close()
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, ledger, approvals), func(AskEvent) error { return nil }); err == nil {
		t.Fatal("the ask against a closed endpoint did not fail")
	}
	if held() {
		t.Fatal("a FAILED run kept a continuation")
	}
}

// TestAsk_TwoQuestionsInOneRunBothGetPast: the owner's live run, twice
// through. A person answers one question, the run goes on, the model
// proposes something else, they answer that one too, and the run finishes.
//
// The second half is the part a single-suspension test cannot see: the
// resume writes a checkpoint of its OWN when it suspends again, over the
// same run id, naming the new branch. If it did not, the second question
// would be the one nobody could ever get past.
func TestAsk_TwoQuestionsInOneRunBothGetPast(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first file")
	writeFile(t, filepath.Join(dir, "b.txt"), "second file")
	firstArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	secondArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(twoProposalsThenAnswer("files.read", firstArgs, secondArgs))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, ledger, approvals)

	answerOne := func(t *testing.T, which string) *ApprovalRequest {
		t.Helper()
		err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
		var asked *ApprovalRequestedError
		if !errors.As(err, &asked) || asked.Request == nil {
			t.Fatalf("%s: Ask error = %v, want the approval-requested suspension", which, err)
		}
		if !approvals.Approve(Approval{
			RunID: asked.Request.RunID, Attempt: asked.Request.Attempt, Tool: asked.Request.Tool,
			CallID: asked.Request.CallID, ArgHash: asked.Request.ArgHash,
		}) {
			t.Fatalf("%s: the proposal the middleware asked about was not pending", which)
		}
		return asked.Request
	}

	first := answerOne(t, "the first question")
	second := answerOne(t, "the second question")
	if first.CallID == second.CallID {
		t.Fatalf("both questions carried call id %q — they are two different proposals", first.CallID)
	}
	if second.Arguments != secondArgs {
		t.Fatalf("the second question was about %s, want the second read %s", second.Arguments, secondArgs)
	}

	// Both answered: the run finishes, and nobody is asked a third time.
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("after both answers the run still did not finish: %v", err)
	}
}
