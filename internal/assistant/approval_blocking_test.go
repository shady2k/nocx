package assistant

// The blocking approval, as the real provider drives it (nocx-a62cl.3.4).
//
// A person's answer reaches the very call that asked (ADR-anntnxdj §3), so
// there is no second drive for a model to re-roll a proposal into, and the
// authority the call goes on under is minted AFTER the answer — which is what
// makes "allow in this session" reach the call that was just answered and every
// call after it in the same run.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// twoProposalsThenAnswer is a provider that proposes ONE call per completion, a
// different resource each time, and answers once it has seen a tool result for
// the second.
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

// TestAsk_ASessionAnswerReachesTheCallThatAsked (nocx-v94ne, ADR-anntnxdj §3):
// one run, two proposals of the SAME effect class. The person answers the first
// "allow in this session" — which is the answer that says stop asking me this —
// and the second one runs without a question.
//
// The answer reaches the very call that asked because the fresh grant is minted
// AFTER the answer, from the policy as it stands then, and the guards and the
// capability read it. The next call of the same run is minted from the same
// policy, which is why the person is not asked twice.
func TestAsk_ASessionAnswerReachesTheCallThatAsked(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first file")
	writeFile(t, filepath.Join(dir, "b.txt"), "second file")
	firstArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	secondArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(twoProposalsThenAnswer("files.read", firstArgs, secondArgs))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}

	// What the transport mints after the answer: the same matrix with the
	// session's new overlay resolved in — runGrantFor's own rule.
	answered := content.ResolvePolicy(askEveryTimeMatrix(), nil, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.DecisionPermit},
	}).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})

	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.Questioner = q
	p.FreshGrant = func() *content.Grant { return &answered }

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if len(q.asked) != 1 {
		t.Fatalf("the person was asked %d times, want exactly 1 — the second read runs under the grant the answer minted", len(q.asked))
	}
	if q.asked[0].Effect != content.EffectObserve {
		t.Fatalf("the question's effect = %q, want observe — the class the session answer is about", q.asked[0].Effect)
	}
	// Both reads happened: the approved one and the one nobody was asked
	// about. Three starts — the escalation's own interrupted attempt, the
	// approved call, and the second proposal.
	if got := ledger.started(); got != 3 {
		t.Fatalf("the ledger recorded %d attempts (%v), want 3 — the escalation, the approved call, and the second read that was never asked about", got, ledger.calls())
	}
}

// TestAsk_EveryQuestionOfOneRunIsAnsweredInTurn: two proposals, two questions,
// both answered, and the run finishes. The second question is about the second
// proposal and comes after the first call settled — one open question at a time
// is the turn's order (ADR-anntnxdj §2), and the model's second proposal is
// only proposed after the first call's result reached it.
func TestAsk_EveryQuestionOfOneRunIsAnsweredInTurn(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "first file")
	writeFile(t, filepath.Join(dir, "b.txt"), "second file")
	firstArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))
	secondArgs := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "b.txt"))

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(twoProposalsThenAnswer("files.read", firstArgs, secondArgs))
	defer srv.Close()

	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	p := askParams(srv.URL, &grant, ledger, approvals)
	p.Questioner = q
	// The authority does not move between the answers, so every proposal of
	// the run is asked about.
	p.FreshGrant = func() *content.Grant { return &grant }

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("after both answers the run still did not finish: %v", err)
	}
	if len(q.asked) != 2 {
		t.Fatalf("the person was asked %d times, want 2 — one question per proposal", len(q.asked))
	}
	if q.asked[0].CallID == q.asked[1].CallID {
		t.Fatalf("both questions carried call id %q — they are two different proposals", q.asked[0].CallID)
	}
	if q.asked[1].Arguments != secondArgs {
		t.Fatalf("the second question was about %s, want the second read %s", q.asked[1].Arguments, secondArgs)
	}
}
