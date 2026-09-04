package content_test

// EffectPolicy.ChangedByRowWrite and WithRowWrite — the question a run
// already in flight is asked when a person moves one of the seven effect rows
// off its answer (nocx-4yjwk.8).
//
// The tests that matter are of two kinds. The "no" cases keep the count from
// becoming the cheap reading — a number that means "how many runs exist". And
// TestARowWriteThatMovesNoRowMovesNoDecision / …MovesADecision are the
// ARGUMENT: they assert against the EVALUATOR that comparing rows is the same
// question as comparing decisions, which is the whole reason this predicate is
// allowed to be a row comparison rather than a probe.

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// askAll is the matrix an unconfigured store serves: seven rows, all asking.
func askAll() content.EffectPolicy { return content.EffectPolicy{} }

// row is one matrix with a single row stated, which is how a person moves one
// answer off its default on the settings page. The switch is the test's own —
// content's setRow is unexported, and a helper is not the place to widen it.
func row(e content.Effect, d content.Decision, scopes ...content.GrantScope) content.EffectPolicy {
	r := content.EffectRow{Decision: d, Scopes: scopes}
	var p content.EffectPolicy
	switch e {
	case content.EffectObserve:
		p.Observe = r
	case content.EffectMutateReversible:
		p.MutateReversible = r
	case content.EffectMutateDestructive:
		p.MutateDestructive = r
	case content.EffectPrivilegeChange:
		p.PrivilegeChange = r
	case content.EffectDisclose:
		p.Disclose = r
	case content.EffectCrossBoundary:
		p.CrossBoundary = r
	case content.EffectDelegate:
		p.Delegate = r
	}
	return p
}

// probes is a spread of the invocations a run can put to its own authority:
// one no standing answer speaks about, one a rule covers, one naming a path,
// and one the parser could not read.
func probes() []content.Invocation {
	return []content.Invocation{
		{Commands: [][]string{{"uname", "-a"}}, Parsed: true},
		{Commands: [][]string{{"df", "-h"}}, Parsed: true},
		{
			Commands:  [][]string{{"cat", "/etc/hosts"}},
			Parsed:    true,
			Resources: content.ResourceReport{Resources: []content.Resource{{Verb: content.ResourceRead, Path: "/etc/hosts"}}},
		},
		{
			Commands: [][]string{{"cat", "/srv/x"}}, Parsed: true,
			Resources: content.ResourceReport{Resources: []content.Resource{{Verb: content.ResourceRead, Path: "/srv/x"}}},
		},
		{Parsed: false},
		{Commands: [][]string{{"df", "-h"}}, Parsed: true, Disqualified: true},
	}
}

// decidesDifferently is what the predicate CLAIMS, asked of the evaluator
// directly: is there an invocation, under any effect, these two answer apart?
func decidesDifferently(before, after content.EffectPolicy) bool {
	effects := []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	}
	for _, inv := range probes() {
		for _, e := range effects {
			if before.DecisionForInvocation(e, inv) != after.DecisionForInvocation(e, inv) {
				return true
			}
		}
	}
	return false
}

// ── the argument: a row comparison IS a decision comparison ────────────────

// Rows equal, rules equal, fence equal — and the evaluator agrees about every
// invocation put to it. This is what licenses the predicate to compare rows
// instead of probing: for one effect the evaluator reads that row's decision,
// that row's scopes, the rules and the fence, and a matrix write moves only
// the first two.
func TestARowWriteThatMovesNoRowMovesNoDecision(t *testing.T) {
	rules := []content.InvocationRule{exactRule("r1", content.DecisionPermit, "df", "-h")}
	before := row(content.EffectObserve, content.DecisionAsk, content.GrantScope{Kind: content.ResourcePath, ID: "/etc"})
	before.Rules = rules
	after := before

	if decidesDifferently(before, after) {
		t.Fatal("the evaluator answers two identical authorities apart")
	}
	if before.ChangedByRowWrite(after) {
		t.Fatal("an unmoved matrix reported a change")
	}
}

// The other end of the same argument: when a row's decision moves, there IS
// an invocation the two answer apart — so the predicate cannot report a
// change nobody could observe.
func TestARowWriteThatMovesARowMovesADecision(t *testing.T) {
	before := askAll()
	after := row(content.EffectObserve, content.DecisionPermit)

	if !decidesDifferently(before, after) {
		t.Fatal("the evaluator answers a moved row the same way, so there would be nothing to report")
	}
	if !before.ChangedByRowWrite(after) {
		t.Fatal("moving the observe row from ask to permit reported no change")
	}
}

// ── the "no" cases, which are what keep the count honest ──────────────────

// Saving the matrix a run is already holding is not a change to that run. This
// is the row-shaped version of the review gesture, and the case the cheap
// reading gets wrong: it would report every live run affected by a write that
// moved nothing.
func TestChangedByRowWrite_AMatrixThatSaysWhatTheRunAlreadyHoldsIsNotAChange(t *testing.T) {
	p := row(content.EffectObserve, content.DecisionPermit)
	if p.ChangedByRowWrite(p) {
		t.Fatal("saving the answer the run already holds reported a change")
	}
}

// A run whose authority already carries the new answer — minted after somebody
// else made the same change — decides nothing differently once the write
// lands, and is not counted.
func TestChangedByRowWrite_AnAuthorityAlreadyCarryingTheNewAnswerIsNotAChange(t *testing.T) {
	held := row(content.EffectDisclose, content.DecisionRefuse)
	next := row(content.EffectDisclose, content.DecisionRefuse)
	if held.ChangedByRowWrite(next) {
		t.Fatal("a run already holding the new answer reported a change")
	}
}

// The places a row states are rewritten as a LIST by the page, so a person who
// removes a place and puts it back sends the same set in another order. That
// is not a change and must not raise the question.
func TestChangedByRowWrite_ReorderingTheSamePlacesIsNotAChange(t *testing.T) {
	etc := content.GrantScope{Kind: content.ResourcePath, ID: "/etc"}
	srv := content.GrantScope{Kind: content.ResourcePath, ID: "/srv"}
	before := row(content.EffectObserve, content.DecisionPermit, etc, srv)
	after := row(content.EffectObserve, content.DecisionPermit, srv, etc)
	if before.ChangedByRowWrite(after) {
		t.Fatal("the same places in another order reported a change")
	}
}

// ── the "yes" cases ───────────────────────────────────────────────────────

// A place taken off a row narrows what the row covers, and the run holding the
// wider one goes on deciding under it.
func TestChangedByRowWrite_TakingAPlaceOffARowIsAChange(t *testing.T) {
	etc := content.GrantScope{Kind: content.ResourcePath, ID: "/etc"}
	srv := content.GrantScope{Kind: content.ResourcePath, ID: "/srv"}
	before := row(content.EffectObserve, content.DecisionPermit, etc, srv)
	after := row(content.EffectObserve, content.DecisionPermit, etc)
	if !before.ChangedByRowWrite(after) {
		t.Fatal("taking a place off a row reported no change")
	}
}

// A STANDING ANSWER DOES NOT SHIELD THE ROW, and this test is why the row
// count cannot be narrowed the way the rule count is.
//
// A permit over `df -h` makes that one command line decide the same before and
// after. It says nothing about every other command line the row governs — a
// selector is always bounded to one command word, so the commands a row alone
// answers for are never an empty set. The run is therefore still deciding
// differently, and reporting otherwise would be the lie in the other
// direction.
func TestChangedByRowWrite_AStandingAnswerOverOneCommandDoesNotShieldTheRow(t *testing.T) {
	before := askAll()
	before.Rules = []content.InvocationRule{exactRule("r1", content.DecisionPermit, "df", "-h")}
	after := row(content.EffectObserve, content.DecisionRefuse)
	after.Rules = before.Rules

	if !before.ChangedByRowWrite(after) {
		t.Fatal("a permit over one command line reported the whole row unmoved")
	}
	if !decidesDifferently(before, after) {
		t.Fatal("the evaluator agrees with the shield, which would make the report wrong")
	}
}

// ── the document the write leaves behind ──────────────────────────────────

// WithRowWrite states the seven rows and touches nothing else. The standing
// answers are the point: a matrix write may not name rules at all, and a
// document that dropped them here would be the whole-document write that
// deleted a person's answers once already (nocx-39bly).
func TestWithRowWrite_StatesTheRowsAndKeepsTheStandingAnswers(t *testing.T) {
	stored := askAll()
	stored.Rules = []content.InvocationRule{exactRule("r1", content.DecisionPermit, "df", "-h")}

	after := stored.WithRowWrite(row(content.EffectObserve, content.DecisionPermit))

	if after.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatalf("observe = %s, want permit", after.DecisionFor(content.EffectObserve))
	}
	if len(after.Rules) != 1 || after.Rules[0].ID != "r1" {
		t.Fatalf("rules = %+v, want the standing answer kept", after.Rules)
	}
	if after.DecisionFor(content.EffectDelegate) != content.DecisionAsk {
		t.Fatalf("delegate = %s, want ask — the write states every row",
			after.DecisionFor(content.EffectDelegate))
	}
}

// The write states ALL seven rows, so a row the person moved back to its
// default is moved back in the document too. A merge that only took the rows
// the write stated would make an answer impossible to take off.
func TestWithRowWrite_ARowMovedBackToItsDefaultIsMovedBack(t *testing.T) {
	stored := row(content.EffectObserve, content.DecisionPermit)
	after := stored.WithRowWrite(askAll())
	if after.DecisionFor(content.EffectObserve) != content.DecisionAsk {
		t.Fatalf("observe = %s, want ask — the write states the row as asking",
			after.DecisionFor(content.EffectObserve))
	}
}
