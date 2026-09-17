package content

import "testing"

// The asymmetry is the content of this task: a loose matcher is safe for
// NARROWING and unsafe for WIDENING. A refusal cannot permit anything, so it
// can afford a matcher a permit could not; a permit without the effect
// binding turns "any find" into "find . -delete", because the rule applies
// over the row and the effect comes from the concrete call.
//
// It is enforced in validateInvocationRules, so an operator cannot write the
// unsafe form at all — the document does not parse. That is why this test is
// internal: the validator is the boundary, and ParseEffectPolicy asserts the
// same refusals from the outside in rules_test.go.
func TestALooseSelectorMayNarrowAndMayNotWiden(t *testing.T) {
	loosePermit := InvocationRule{
		Selector: InvocationSelector{
			HasFeature: &FeatureRef{Program: "curl", Feature: FeatureWritesOptionNamedPath},
		},
		Decision: DecisionPermit,
	}
	if err := validateInvocationRules([]InvocationRule{loosePermit}); err == nil {
		t.Error("a feature selector was accepted with permit; a loose matcher may only narrow")
	}

	looseRefusal := loosePermit
	looseRefusal.Decision = DecisionRefuse
	if err := validateInvocationRules([]InvocationRule{looseRefusal}); err != nil {
		t.Errorf("a feature selector was rejected with refuse: %v", err)
	}

	unguarded := InvocationRule{
		Selector: InvocationSelector{Program: "df"},
		Decision: DecisionPermit,
	}
	if err := validateInvocationRules([]InvocationRule{unguarded}); err == nil {
		t.Error("a program-wide permit was accepted with no effect it was granted under")
	}

	guarded := unguarded
	guarded.GrantedUnder = EffectObserve
	if err := validateInvocationRules([]InvocationRule{guarded}); err != nil {
		t.Errorf("a program-wide permit bound to one effect was rejected: %v", err)
	}
}
