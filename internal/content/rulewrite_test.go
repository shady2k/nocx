package content_test

// EffectPolicy.ChangedByRuleWrite — the question a run already in flight is
// asked when a person takes an answer back: would THIS grant decide
// differently without it (nocx-r4fh8)?
//
// The tests that matter are the two "no" cases. A predicate that answered yes
// for every policy holding the rule would pass a naive suite and would be the
// imprecise reading this design rejected — six runs reported affected by an
// answer governing none of them.

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func exactRule(id string, decision content.Decision, command ...string) content.InvocationRule {
	return content.InvocationRule{
		ID:               id,
		Selector:         content.InvocationSelector{Exact: [][]string{command}},
		Decision:         decision,
		Source:           content.SourceAnswered,
		EvaluatorVersion: content.EvaluatorVersion,
	}
}

// A permit rule an ask row would otherwise refuse to answer: forgetting it
// moves `df -h` from permitted back to asked, so the run holding it is
// deciding differently from the store and must be counted.
func TestChangedByRuleWrite_ForgettingAPermitThatDecidesIsAChange(t *testing.T) {
	p := content.EffectPolicy{Rules: []content.InvocationRule{
		exactRule("r1", content.DecisionPermit, "df", "-h"),
	}}
	if !p.ChangedByRuleWrite(content.RuleWrite{ID: "r1"}) {
		t.Fatalf("forgetting the permit that decides df -h reported no change")
	}
}

// The rule is not in this policy at all — the run started before it was
// written. Forgetting it changes nothing about what this run decides, and a
// run that would go on behaving identically is not "using" the answer.
func TestChangedByRuleWrite_ARuleThisGrantNeverHeldIsNotAChange(t *testing.T) {
	p := content.EffectPolicy{Rules: []content.InvocationRule{
		exactRule("other", content.DecisionPermit, "uname", "-a"),
	}}
	if p.ChangedByRuleWrite(content.RuleWrite{ID: "r1"}) {
		t.Fatalf("forgetting a rule this policy never held reported a change")
	}
}

// The rule IS in the policy and decides nothing: a refusal over the same
// command line wins whether the permit is there or not (the loop takes the
// most restrictive match). This is the case the cheap reading gets wrong.
func TestChangedByRuleWrite_AShadowedRuleDecidesNothingAndIsNotAChange(t *testing.T) {
	p := content.EffectPolicy{Rules: []content.InvocationRule{
		exactRule("permit", content.DecisionPermit, "df", "-h"),
		exactRule("refuse", content.DecisionRefuse, "df", "-h"),
	}}
	if p.ChangedByRuleWrite(content.RuleWrite{ID: "permit"}) {
		t.Fatalf("forgetting a permit a refusal already shadows reported a change")
	}
	// And the refusal is not shadowed: taking IT back uncovers the permit.
	if !p.ChangedByRuleWrite(content.RuleWrite{ID: "refuse"}) {
		t.Fatalf("forgetting the refusal that decides df -h reported no change")
	}
}

// A row that refuses outright is final before any rule is read, so a rule
// under it decides nothing anywhere and forgetting it changes nothing.
func TestChangedByRuleWrite_ARuleUnderARefusingRowIsNotAChange(t *testing.T) {
	p := content.EffectPolicy{Rules: []content.InvocationRule{
		exactRule("r1", content.DecisionPermit, "df", "-h"),
	}}
	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	} {
		row := content.EffectRow{Decision: content.DecisionRefuse}
		p = p.SetRowDecision(e, row.Decision)
	}
	if p.ChangedByRuleWrite(content.RuleWrite{ID: "r1"}) {
		t.Fatalf("a rule under seven refusing rows reported a change; a refusing row is final before any rule is read")
	}
}

// A NEW answer is a change too, and for the same reason: a refusal written
// while runs are in flight reaches none of them.
func TestChangedByRuleWrite_ANewAnswerIsAChangeTheLiveRunsWillNotSee(t *testing.T) {
	var p content.EffectPolicy
	refusal := exactRule("", content.DecisionRefuse, "rm", "-rf", "/tmp/x")
	if !p.ChangedByRuleWrite(content.RuleWrite{Rule: &refusal}) {
		t.Fatalf("writing a refusal over a command nothing else answers reported no change")
	}
}

// A change that moves the selector speaks about BOTH command lines, and the
// probe has to cover both or an edit away from a command would be invisible.
func TestChangedByRuleWrite_AChangedSelectorIsProbedAtBothEnds(t *testing.T) {
	p := content.EffectPolicy{Rules: []content.InvocationRule{
		exactRule("r1", content.DecisionPermit, "df", "-h"),
	}}
	// Same decision, different command line: `df -h` stops being permitted
	// and `df -k` starts. Probing only the incoming selector would miss the
	// first half; probing only the stored one would miss the second.
	moved := exactRule("r1", content.DecisionPermit, "df", "-k")
	if !p.ChangedByRuleWrite(content.RuleWrite{ID: "r1", Rule: &moved}) {
		t.Fatalf("moving a permit from df -h to df -k reported no change")
	}
}

// Re-writing a rule with exactly what it already says changes nothing, so it
// must not raise a question about live runs. This is criterion 2's mechanism:
// a Review — "I have read what this now means and I still mean it" — sends the
// same selector, decision and effect back, and must not ask anybody to stop
// their work.
func TestChangedByRuleWrite_RewritingARuleUnchangedIsNotAChange(t *testing.T) {
	stored := exactRule("r1", content.DecisionPermit, "df", "-h")
	p := content.EffectPolicy{Rules: []content.InvocationRule{stored}}
	same := stored
	if p.ChangedByRuleWrite(content.RuleWrite{ID: "r1", Rule: &same}) {
		t.Fatalf("re-writing a rule with what it already says reported a change")
	}
}
