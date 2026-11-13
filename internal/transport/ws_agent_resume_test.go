package transport

// The fresh grant after an answer, from the transport's side (nocx-a62cl.3.4,
// ADR-anntnxdj §3).
//
// The authority a call goes on under is minted AGAIN after the person answers,
// from the policy as it stands then — the very write the answer just made
// included. The transport owns that mint (the composition root's, over the
// session's overlay), and this asserts what it HANDS the engine: the run's Ask
// carries a FreshGrant seam, and calling it after "allow in this session"
// returns a grant that permits the effect class.
//
// What is asserted is the seam, because that is the whole of the mechanism on
// this side: the pipeline calls it after the answer, and the kernel's own test
// proves the guards and the capability read what it returns.

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestAgentApprove_TheRunCarriesTheFreshGrantAfterAnAnswer: one run, one
// question, answered "in this session" — and the grant the engine can mint
// after the answer PERMITS that effect class, so the call that was answered
// and every call after it in the same run do not ask again.
func TestAgentApprove_TheRunCarriesTheFreshGrantAfterAnAnswer(t *testing.T) {
	var afterAnswer *content.Grant
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{ask: policyAsk("files.read", "call_1", `{"path":"/repo/a.txt"}`, "hash-a"), after: []string{"done"}},
	}}
	// The seam is read where the pipeline reads it: after the answer.
	client.onAnswer = func(p assistant.AskParams) {
		if p.FreshGrant == nil {
			t.Error("the run carried no fresh-grant seam")
			return
		}
		afterAnswer = p.FreshGrant()
	}
	h := suspendedRunWith(t, askPolicyStore(t), client)

	h.approve(t, "session")
	waittest.WaitForTimeout(t, "the answer to reach the call", 5*time.Second, func() bool { return afterAnswer != nil })

	got := client.params()
	if len(got) != 1 {
		t.Fatalf("the engine was driven %d times, want 1 — the answer reaches the call that asked", len(got))
	}
	if got[0].Grant == nil || got[0].Grant.Policy.DecisionFor(content.EffectObserve) != content.DecisionAsk {
		t.Fatalf("the ask ran under %+v, want a grant that ASKS for observe — otherwise the question below is not the one being answered", got[0].Grant)
	}
	if d := afterAnswer.Policy.DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("the fresh grant decides observe = %q, want permit: the person answered \"allow in this session\" and the run they answered about went on asking (nocx-v94ne)", d)
	}
}
