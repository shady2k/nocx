package content_test

import (
	"strings"
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

func TestStandingRuleRejectsUnresolvedInvocationAndNeverMatchesItLater(t *testing.T) {
	invocation := content.Invocation{
		Commands: [][]string{{"cat", "$LOGFILE"}},
		Parsed:   true,
		Resources: content.ResourceReport{Unresolved: []content.UnresolvedResource{{
			Path: "$LOGFILE", Verb: content.ResourceRead,
			Reason: "could not resolve $LOGFILE without executing shell expansion",
		}}},
	}
	rule, reason := content.StandingRule(invocation)
	if reason == "" || !strings.Contains(reason, "$LOGFILE") {
		t.Fatalf("standing rule = %#v, reason = %q, want refusal naming the unresolved variable", rule, reason)
	}
	if rule.Matches(invocation) {
		t.Fatal("an unresolved invocation matched the rule it must not mint")
	}

	saved := content.InvocationRule{
		Pattern:  invocation.Commands,
		Decision: content.DecisionPermit,
	}
	if saved.Matches(invocation) {
		t.Fatal("a saved rule matched a later invocation whose shell expansion may differ")
	}
}

func TestStandingRuleUsesCanonicalSingleInvocationLabel(t *testing.T) {
	rule, reason := content.StandingRule(content.Invocation{
		Commands: [][]string{{"df", "-h"}},
		Parsed:   true,
	})
	if reason != "" {
		t.Fatalf("standing rule reason = %q, want none", reason)
	}
	if got := rule.Label(); got != "df -h" {
		t.Fatalf("standing rule label = %q, want canonical invocation %q", got, "df -h")
	}
	if len(rule.Pattern) != 1 || len(rule.Pattern[0]) != 2 ||
		rule.Pattern[0][0] != "df" || rule.Pattern[0][1] != "-h" {
		t.Fatalf("standing rule pattern = %#v, want the canonical invocation tokens", rule.Pattern)
	}
}

func TestStandingRuleRefusesInvocationsItCannotShowCompletely(t *testing.T) {
	tests := []struct {
		name   string
		inv    content.Invocation
		reason string
	}{
		{
			name:   "exec wrapper",
			inv:    content.Invocation{Commands: [][]string{{"sudo", "df", "-h"}}, Parsed: true, Disqualified: true},
			reason: "wrapper",
		},
		{
			name:   "compound command",
			inv:    content.Invocation{Commands: [][]string{{"df", "-h"}, {"rm", "-rf", "/"}}, Parsed: true, Disqualified: true},
			reason: "more than one command",
		},
		{
			name:   "unparsed",
			inv:    content.Invocation{Parsed: false},
			reason: "could not be parsed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, reason := content.StandingRule(test.inv)
			if reason == "" || !strings.Contains(reason, test.reason) {
				t.Fatalf("standing rule = %#v, reason = %q, want reason containing %q", rule, reason, test.reason)
			}
		})
	}
}

// ── the rule is the shape layer, never the resource layer ──────────────────
//
// A matching rule answers "what shape is this command line". The resources
// the command names still face the selected effect's row scopes and, through
// them, the run fence. Both ends of that interval are asserted in each test:
// the same rule and the same row permit the invocation that stays inside.

func TestPermittingRuleDoesNotCoverAResourceOutsideTheRowScopes(t *testing.T) {
	rule := content.InvocationRule{
		Pattern:  [][]string{{"cat", "*"}},
		Decision: content.DecisionPermit,
	}
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionAsk,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo"}},
		},
		Rules: []content.InvocationRule{rule},
	}
	inside := content.Invocation{
		Commands:  [][]string{{"cat", "/repo/notes.txt"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/repo/notes.txt", Verb: content.ResourceRead}}},
	}
	outside := content.Invocation{
		Commands:  [][]string{{"cat", "/etc/shadow"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/etc/shadow", Verb: content.ResourceRead}}},
	}
	if got := p.DecisionForInvocation(content.EffectObserve, inside); got != content.DecisionPermit {
		t.Fatalf("in-scope invocation = %q, want permit — the rule's exception holds inside the row", got)
	}
	if got := p.DecisionForInvocation(content.EffectObserve, outside); got != content.DecisionAsk {
		t.Fatalf("out-of-scope invocation = %q, want the row's own ask — a rule is not an exception to the resource layer", got)
	}
}

func TestPermittingRuleDoesNotCoverAResourceOutsideTheRunFence(t *testing.T) {
	rule := content.InvocationRule{
		Pattern:  [][]string{{"cat", "*"}},
		Decision: content.DecisionPermit,
	}
	// Every row permits and states no selector of its own, so the run fence
	// is the only bound on the resources — and it still binds.
	p := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionPermit},
		MutateReversible:  content.EffectRow{Decision: content.DecisionPermit},
		MutateDestructive: content.EffectRow{Decision: content.DecisionPermit},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionPermit},
		Disclose:          content.EffectRow{Decision: content.DecisionPermit},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionPermit},
		Delegate:          content.EffectRow{Decision: content.DecisionPermit},
		Rules:             []content.InvocationRule{rule},
	}
	fenced := p.WithRunScopes([]content.GrantScope{{Kind: content.ResourcePath, ID: "/repo"}})
	inside := content.Invocation{
		Commands:  [][]string{{"cat", "/repo/notes.txt"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/repo/notes.txt", Verb: content.ResourceRead}}},
	}
	outside := content.Invocation{
		Commands:  [][]string{{"cat", "/etc/shadow"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/etc/shadow", Verb: content.ResourceRead}}},
	}
	if got := fenced.DecisionForInvocation(content.EffectObserve, inside); got != content.DecisionPermit {
		t.Fatalf("in-fence invocation = %q, want permit", got)
	}
	if got := fenced.DecisionForInvocation(content.EffectObserve, outside); got != content.DecisionAsk {
		t.Fatalf("out-of-fence invocation = %q, want ask — the fence bounds every row for the run", got)
	}
	// Without the fence the same policy permits the same command: the fence,
	// not the command shape, is what closed it.
	if got := p.DecisionForInvocation(content.EffectObserve, outside); got != content.DecisionPermit {
		t.Fatalf("unfenced invocation = %q, want permit — an unfenced permit row bounds no path", got)
	}
}

func TestResourceLayerRefusalIsPerEffectRowNotTheWholeMatrix(t *testing.T) {
	// The row consulted is the SELECTED effect's row. A destructive call
	// naming a path the observe row would have allowed is decided by the
	// destructive row's scopes, so one row's bound never answers for another.
	p := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo"}},
		},
		MutateDestructive: content.EffectRow{
			Decision: content.DecisionAsk,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/repo/scratch"}},
		},
		Rules: []content.InvocationRule{{
			Pattern:  [][]string{{"rm", "-rf", "*"}},
			Decision: content.DecisionPermit,
		}},
	}
	scratch := content.Invocation{
		Commands:  [][]string{{"rm", "-rf", "/repo/scratch/build"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/repo/scratch/build", Verb: content.ResourceDelete}}},
	}
	source := content.Invocation{
		Commands:  [][]string{{"rm", "-rf", "/repo/src"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/repo/src", Verb: content.ResourceDelete}}},
	}
	if got := p.DecisionForInvocation(content.EffectMutateDestructive, scratch); got != content.DecisionPermit {
		t.Fatalf("scratch delete = %q, want permit — inside the destructive row's scope", got)
	}
	if got := p.DecisionForInvocation(content.EffectMutateDestructive, source); got != content.DecisionAsk {
		t.Fatalf("source delete = %q, want ask — the observe row's wider path does not answer for destructive", got)
	}
}

func TestResourceLayerNeverWidensARefusingRowOrAResourceOfAnUnboundKind(t *testing.T) {
	// Two ends the resource layer must not move: a refusing row stays
	// refused whatever the named resource is, and a row that bounds no path
	// at all narrows no path (the kind-wise rule the fence intersection
	// already uses).
	inv := content.Invocation{
		Commands:  [][]string{{"cat", "/etc/shadow"}},
		Parsed:    true,
		Resources: content.ResourceReport{Resources: []content.Resource{{Path: "/etc/shadow", Verb: content.ResourceRead}}},
	}
	refusing := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionRefuse,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/etc"}},
		},
		Rules: []content.InvocationRule{{Pattern: [][]string{{"cat", "*"}}, Decision: content.DecisionPermit}},
	}
	if got := refusing.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionRefuse {
		t.Fatalf("refusing row = %q, want refuse — refusal stays final", got)
	}
	sessionOnly := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionPermit,
			Scopes:   []content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}},
		},
	}
	if got := sessionOnly.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionPermit {
		t.Fatalf("session-scoped row = %q, want permit — a row that bounds no path bounds no path", got)
	}
}
