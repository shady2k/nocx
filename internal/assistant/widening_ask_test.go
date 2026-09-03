package assistant

// The out-of-scope ask a person can actually answer (design §5.3, nocx-b453p).
//
// Before this, a resource outside an EDITABLE row scope reached a person as an
// ordinary approval question. The three widths that question offers — once, in
// this session, always — widen NOTHING: none of them grows the row scope that
// excluded the resource, so the next identical call asks again, for ever. An
// ask a person cannot usefully answer is worse than a refusal.
//
// So the ask carries WHICH cause excluded the resource and WHICH resource the
// row would have to grow to cover — and, because this is an approval-fatigue
// channel, the asking itself is bounded: one ask per (effect, resource) per
// run, a cap on how many of them one answer may raise, and a declined widening
// that refuses that pair for the rest of the run's life.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// editableRowGrant mints the state §5.3 is about: a run fence a person cannot
// widen, and inside it an operator's row selector they can. A path under the
// fence but outside the selector is the EDITABLE miss — the question.
func editableRowGrant(t *testing.T) (content.Grant, string) {
	t.Helper()
	fence := t.TempDir()
	policy := autonomousMatrix()
	policy.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: filepath.Join(fence, "src")}},
	}
	grant := policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: fence},
	})
	return grant, fence
}

func outsideTheRow(fence string) string { return filepath.Join(fence, "lib", "b.txt") }

func readArgs(path string) string { return `{"path":"` + path + `"}` }

// askFor drives one proposal through the real pipeline and returns the
// approval request it raised, or nil with the refusal the model was handed.
func askFor(t *testing.T, mw *policyMiddleware, callID, args string) (*ApprovalRequest, string) {
	t.Helper()
	out, err := mw.kernel.Invoke(context.Background(), "files.read", callID, args)
	var asked *ApprovalRequestedError
	if errors.As(err, &asked) {
		if asked.Request == nil {
			t.Fatalf("%s: the suspension carried no request", callID)
		}
		return asked.Request, ""
	}
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", callID, err)
	}
	return nil, out
}

// The ask names the cause and the resource the row would have to grow to
// cover. Without both, the widening answer cannot be written from the
// question a person answered.
func TestAsk_OutOfScopeRowScopeCarriesTheCauseAndTheResource(t *testing.T) {
	grant, fence := editableRowGrant(t)
	mw := middlewareFor(t, grant, &fakeLedger{}, NewApprovalStore())
	path := outsideTheRow(fence)

	req, refusal := askFor(t, mw, "call-1", readArgs(path))
	if req == nil {
		t.Fatalf("a path inside the fence and outside the row scope did not ask; it answered %q", refusal)
	}
	if req.OutOfScope == nil {
		t.Fatal("the ask carries no out-of-scope fact, so the prompt cannot offer to widen the row")
	}
	if req.OutOfScope.Cause != content.OutOfScopeRowScope {
		t.Errorf("cause = %q, want %q", req.OutOfScope.Cause, content.OutOfScopeRowScope)
	}
	want := content.GrantScope{Kind: content.ResourcePath, ID: path}
	if req.OutOfScope.Resource != want {
		t.Errorf("resource = %+v, want the resource that fell outside %+v", req.OutOfScope.Resource, want)
	}
}

// An ask that had nothing outside carries no fact at all: a widening offer on
// a question about nothing is the lie this task removes, in the other
// direction.
func TestAsk_AnOrdinaryAskCarriesNoOutOfScopeFact(t *testing.T) {
	policy := allRows(content.DecisionAsk)
	fence := t.TempDir()
	grant := policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: fence},
	})
	mw := middlewareFor(t, grant, &fakeLedger{}, NewApprovalStore())

	req, refusal := askFor(t, mw, "call-1", readArgs(filepath.Join(fence, "a.txt")))
	if req == nil {
		t.Fatalf("an ask row did not ask; it answered %q", refusal)
	}
	if req.OutOfScope != nil {
		t.Fatalf("an ask with nothing outside carries %+v, want no out-of-scope fact", req.OutOfScope)
	}
}

// Deduplication within a run: the SECOND proposal of the same (effect,
// resource) raises no second prompt. A model that keeps proposing the same
// out-of-scope resource must not be able to re-ask its way past a person.
func TestAsk_TheSecondIdenticalOutOfScopeProposalRaisesNoSecondPrompt(t *testing.T) {
	grant, fence := editableRowGrant(t)
	mw := middlewareFor(t, grant, &fakeLedger{}, NewApprovalStore())
	path := outsideTheRow(fence)

	if req, refusal := askFor(t, mw, "call-1", readArgs(path)); req == nil {
		t.Fatalf("the first proposal did not ask; it answered %q", refusal)
	}
	req, refusal := askFor(t, mw, "call-2", readArgs(path))
	if req != nil {
		t.Fatal("the second identical out-of-scope proposal raised a second prompt")
	}
	if want := refusalResult("files.read", RefusedWideningAsked, ""); refusal != want {
		t.Fatalf("refusal = %q, want %q", refusal, want)
	}
}

// A DIFFERENT resource in the same run is a different question and is still
// asked — the dedup is per (effect, resource), never per run.
func TestAsk_ADifferentOutOfScopeResourceIsStillAsked(t *testing.T) {
	grant, fence := editableRowGrant(t)
	mw := middlewareFor(t, grant, &fakeLedger{}, NewApprovalStore())

	if req, refusal := askFor(t, mw, "call-1", readArgs(filepath.Join(fence, "lib", "b.txt"))); req == nil {
		t.Fatalf("the first proposal did not ask; it answered %q", refusal)
	}
	if req, refusal := askFor(t, mw, "call-2", readArgs(filepath.Join(fence, "etc", "c.txt"))); req == nil {
		t.Fatalf("a different resource did not ask; it answered %q", refusal)
	}
}

// The cap, and the sentence. Reaching it silently would be the soft degrade
// AGENTS.md forbids: the person must be TOLD the assistant stopped asking.
func TestAsk_RepeatedWideningAsksAreCappedAndTheRunSaysSo(t *testing.T) {
	grant, fence := editableRowGrant(t)
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)

	for i := 0; i < MaxWideningAsksPerRun; i++ {
		callID := "call-" + string(rune('a'+i))
		path := filepath.Join(fence, "dir"+string(rune('a'+i)), "f.txt")
		if req, refusal := askFor(t, mw, callID, readArgs(path)); req == nil {
			t.Fatalf("ask %d did not ask; it answered %q", i+1, refusal)
		}
	}
	if notices := approvals.RunNotices("run-1"); len(notices) != 0 {
		t.Fatalf("the run says %q before the cap was reached", notices)
	}

	over := filepath.Join(fence, "over", "f.txt")
	req, refusal := askFor(t, mw, "call-over", readArgs(over))
	if req != nil {
		t.Fatalf("ask %d was raised; the cap is %d", MaxWideningAsksPerRun+1, MaxWideningAsksPerRun)
	}
	if want := refusalResult("files.read", RefusedWideningCapped, ""); refusal != want {
		t.Fatalf("refusal = %q, want %q", refusal, want)
	}
	notices := approvals.RunNotices("run-1")
	if len(notices) != 1 || notices[0] != WideningCapSentence() {
		t.Fatalf("the run says %q, want exactly %q", notices, WideningCapSentence())
	}
	if !strings.Contains(notices[0], "stopped asking") {
		t.Fatalf("the sentence %q does not say the assistant stopped asking", notices[0])
	}
}

// A declined widening refuses that (effect, resource) FROM the decline UNTIL
// the run ends — both ends of the interval, per AGENTS.md testing rule 3 — and
// the NEXT run asks again, because the decline was scoped to a run and to
// nothing longer.
func TestAsk_ADeclinedWideningRefusesThatResourceForTheRestOfTheRunAndTheNextRunAsksAgain(t *testing.T) {
	grant, fence := editableRowGrant(t)
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)
	path := outsideTheRow(fence)

	// BEFORE the decline: the pair is asked.
	req, refusal := askFor(t, mw, "call-1", readArgs(path))
	if req == nil {
		t.Fatalf("the first proposal did not ask; it answered %q", refusal)
	}
	approvals.DeclineWidening("run-1", content.EffectObserve, req.OutOfScope.Resource)

	// AFTER it, and for the rest of this run: refused, in the person's name.
	_, refusal = askFor(t, mw, "call-2", readArgs(path))
	if want := refusalResult("files.read", RefusedWideningDeclined, ""); refusal != want {
		t.Fatalf("refusal = %q, want %q", refusal, want)
	}

	// The other end of the interval: a NEW run over the SAME store asks
	// again. A decline that outlived its run would be a standing answer
	// nobody gave.
	next := middlewareForRun(t, grant, &fakeLedger{}, approvals, "run-2")
	if req, refusal := askFor(t, next, "call-3", readArgs(path)); req == nil {
		t.Fatalf("the next run did not ask; it answered %q", refusal)
	}
}

// A refusal caused by the immutable fence is still a refusal, and it never
// becomes a widening question: no answer a person can give makes it
// executable (design §5.3).
func TestAsk_OutsideTheFenceStaysARefusalAndIsNeverAWideningAsk(t *testing.T) {
	grant, _ := editableRowGrant(t)
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)

	req, refusal := askFor(t, mw, "call-1", readArgs("/etc/hosts"))
	if req != nil {
		t.Fatal("a path outside the run fence raised a question a person cannot usefully answer")
	}
	if want := refusalResult("files.read", RefusedOutOfScope, ""); refusal != want {
		t.Fatalf("refusal = %q, want %q", refusal, want)
	}
	if notices := approvals.RunNotices("run-1"); len(notices) != 0 {
		t.Fatalf("a fence refusal spent a widening ask: %q", notices)
	}
}

// middlewareForRun is middlewareFor with an explicit run id — the second run
// over the same process-lifetime approval store, which is the only way to
// assert that a run-scoped decline ends with its run.
func middlewareForRun(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, runID string) *policyMiddleware {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	sessionID := ""
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourceSession {
			sessionID = scope.ID
			break
		}
	}
	mw, err := newPolicyMiddleware(nil, grant, reg, ledger, approvals, &fakeKnownMaterial{}, runID, sessionID, 1, "", nil, Attachments{}, nil, nil)
	if err != nil {
		t.Fatalf("newPolicyMiddleware: %v", err)
	}
	return mw
}
