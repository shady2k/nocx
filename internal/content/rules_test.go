package content_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestInvocationRuleMatchesWholeCanonicalInvocation(t *testing.T) {
	invocation := content.Invocation{
		Commands: [][]string{{"ls", "-l", "/tmp"}},
		Parsed:   true,
	}
	rule := content.InvocationRule{
		Pattern:  [][]string{{"ls", "*", "/tmp"}},
		Decision: content.DecisionPermit,
	}
	if !rule.Matches(invocation) {
		t.Fatal("the token wildcard did not match the canonical invocation")
	}
	if rule.Matches(content.Invocation{Commands: [][]string{{"lsof"}}, Parsed: true}) {
		t.Fatal("a rule for ls matched lsof")
	}
	if rule.Matches(content.Invocation{
		Commands: [][]string{{"ls"}, {"rm", "-rf", "/tmp"}},
		Parsed:   true,
	}) {
		t.Fatal("a single-command rule matched a compound invocation")
	}
}

func TestInvocationRuleTrailingWildcardDoesNotSpanExtraArguments(t *testing.T) {
	rule := content.InvocationRule{
		Pattern:  [][]string{{"ls", "*"}},
		Decision: content.DecisionPermit,
	}
	invocation := content.Invocation{
		Commands: [][]string{{"ls", "-la", "/tmp"}},
		Parsed:   true,
	}
	if rule.Matches(invocation) {
		t.Fatal("a trailing wildcard matched an extra argument; patterns are fixed-arity")
	}
}

func TestInvocationRuleNeverMatchesUnsoundCanonicalInvocations(t *testing.T) {
	rule := content.InvocationRule{
		Pattern:  [][]string{{"rm", "-rf", "/tmp/scratch"}},
		Decision: content.DecisionPermit,
	}
	for _, invocation := range []content.Invocation{
		{Commands: [][]string{{"sudo", "rm", "-rf", "/tmp/scratch"}}, Parsed: true},
		{Commands: [][]string{{"rm", "-rf", "/tmp/scratch"}}, Parsed: true, Disqualified: true},
		{Parsed: false},
	} {
		if rule.Matches(invocation) {
			t.Fatalf("rule matched unsound invocation: %+v", invocation)
		}
	}
}

func TestEffectPolicyInvocationRulesMostRestrictiveWins(t *testing.T) {
	invocation := content.Invocation{Commands: [][]string{{"rm", "-rf", "/tmp/scratch"}}, Parsed: true}
	p := content.EffectPolicy{
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
		Rules: []content.InvocationRule{
			{Pattern: invocation.Commands, Decision: content.DecisionPermit},
			{Pattern: invocation.Commands, Decision: content.DecisionRefuse},
		},
	}
	if got := p.DecisionForInvocation(content.EffectMutateDestructive, invocation); got != content.DecisionRefuse {
		t.Fatalf("overlapping rules resolved to %q, want refuse", got)
	}
}

func TestEffectPolicyUnparseableInvocationAsks(t *testing.T) {
	p := content.EffectPolicy{
		MutateDestructive: content.EffectRow{Decision: content.DecisionPermit},
		Rules: []content.InvocationRule{{
			Pattern:  [][]string{{"rm", "-rf", "/tmp/scratch"}},
			Decision: content.DecisionPermit,
		}},
	}
	if got := p.DecisionForInvocation(content.EffectMutateDestructive, content.Invocation{Disqualified: true}); got != content.DecisionAsk {
		t.Fatalf("unparseable invocation resolved to %q, want ask", got)
	}
}

func TestLiteralInvocationRuleRejectsPatternCharacters(t *testing.T) {
	invocation := content.Invocation{
		Commands: [][]string{{"rm", "*"}},
		Parsed:   true,
	}
	rule, err := content.LiteralInvocationRule(invocation, content.DecisionPermit)
	if err == nil {
		t.Fatalf("literal rule for %q was minted: %+v", invocation.Commands, rule)
	}
}
