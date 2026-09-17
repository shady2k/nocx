package content_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

func TestInvocationRuleMatchesWholeCanonicalInvocation(t *testing.T) {
	invocation := content.Invocation{
		Commands: [][]string{{"ls", "-l", "/tmp"}},
		Parsed:   true,
	}
	rule := content.InvocationRule{
		Selector: content.InvocationSelector{Exact: [][]string{{"ls", "*", "/tmp"}}},
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
		Selector: content.InvocationSelector{Exact: [][]string{{"ls", "*"}}},
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
		Selector: content.InvocationSelector{Exact: [][]string{{"rm", "-rf", "/tmp/scratch"}}},
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
			{Selector: content.InvocationSelector{Exact: invocation.Commands}, Decision: content.DecisionPermit},
			{Selector: content.InvocationSelector{Exact: invocation.Commands}, Decision: content.DecisionRefuse},
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
			Selector: content.InvocationSelector{Exact: [][]string{{"rm", "-rf", "/tmp/scratch"}}},
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
		Selector: content.InvocationSelector{Exact: invocation.Commands},
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
	exact := rule.Selector.Exact
	if len(exact) != 1 || len(exact[0]) != 2 || exact[0][0] != "df" || exact[0][1] != "-h" {
		t.Fatalf("standing rule selector = %#v, want the canonical invocation tokens", rule.Selector)
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
		Selector: content.InvocationSelector{Exact: [][]string{{"cat", "*"}}},
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
		Selector: content.InvocationSelector{Exact: [][]string{{"cat", "*"}}},
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
	// Outside the fence is a REFUSAL, not a question (design §5.3): the run
	// fence is immutable for the run's life, so no answer a person could
	// give would make this call executable and offering the question would
	// promise something the capability refuses anyway.
	if got := fenced.EvaluateInvocation(content.EffectObserve, outside, fenced.RunFence()); got.Decision != content.DecisionRefuse || got.Cause != content.OutOfScopeFence {
		t.Fatalf("out-of-fence invocation = %+v, want refuse with cause fence — the fence bounds every row for the run", got)
	}
	if got := fenced.DecisionForInvocation(content.EffectObserve, outside); got != content.DecisionRefuse {
		t.Fatalf("out-of-fence invocation = %q through the decision-only wrapper, want refuse", got)
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
			Selector: content.InvocationSelector{Exact: [][]string{{"rm", "-rf", "*"}}},
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
		Rules: []content.InvocationRule{{Selector: content.InvocationSelector{Exact: [][]string{{"cat", "*"}}}, Decision: content.DecisionPermit}},
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

// ---------------------------------------------------------------------------
// The selector axis (design §5.5). A loose matcher is safe for NARROWING and
// unsafe for WIDENING, and the three groups below are the three ends of that:
// what the document refuses to express at all, what a widening permit reaches,
// and what a narrowing refusal beats.

func TestAWidenedPermitIsUnparseableWithoutTheEffectItWasGrantedFor(t *testing.T) {
	// The gate is the document, not the operator's care: neither spelling of
	// a loose permit survives ParseEffectPolicy, so no store can hold one.
	unparseable := map[string]string{
		"a program-wide permit with no effect binding": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"program": "df"}, "decision": "permit"}]
		}`,
		"a feature permit": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"hasFeature": {"program": "curl", "feature": "writes-option-named-path"}}, "decision": "permit"}]
		}`,
		"a feature permit bound to an effect, which does not rescue it": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"hasFeature": {"program": "curl", "feature": "writes-option-named-path"}}, "decision": "permit", "grantedUnder": "observe"}]
		}`,
		"two selector fields at once": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"program": "df", "exact": [["df", "-h"]]}, "decision": "refuse"}]
		}`,
		"no selector field at all": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {}, "decision": "refuse"}]
		}`,
		"a feature the classifier does not record": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"hasFeature": {"program": "curl", "feature": "phones-home"}}, "decision": "refuse"}]
		}`,
		"a grantedUnder outside the lattice": `{
			"observe": {"decision": "ask", "scopes": []},
			"rules": [{"id": "r1", "selector": {"program": "df"}, "decision": "permit", "grantedUnder": "readScreen"}]
		}`,
	}
	for name, doc := range unparseable {
		t.Run(name, func(t *testing.T) {
			if _, err := content.ParseEffectPolicy([]byte(doc)); err == nil {
				t.Fatal("the document parsed; the unsafe form must not be expressible")
			}
			// WithRule is the other way in, and it drops what the document
			// refuses rather than admitting it behind the parser's back.
			bad := content.InvocationRule{
				Selector: content.InvocationSelector{Program: "df"},
				Decision: content.DecisionPermit,
			}
			if got := len(content.EffectPolicy{}.WithRule(bad).Rules); got != 0 {
				t.Fatalf("WithRule kept %d rules, want 0 — an invalid rule is not stored", got)
			}
		})
	}

	// And both safe forms DO parse, so the refusals above are the asymmetry
	// and not a validator that rejects everything.
	good := `{
		"observe": {"decision": "ask", "scopes": []},
		"rules": [
			{"id": "r1", "selector": {"program": "df"}, "decision": "permit", "grantedUnder": "observe"},
			{"id": "r2", "selector": {"hasFeature": {"program": "curl", "feature": "writes-option-named-path"}}, "decision": "refuse"},
			{"id": "r3", "selector": {"exact": [["df", "-h"]]}, "decision": "permit"}
		]
	}`
	p, err := content.ParseEffectPolicy([]byte(good))
	if err != nil {
		t.Fatalf("the safe forms did not parse: %v", err)
	}
	if len(p.Rules) != 3 {
		t.Fatalf("parsed %d rules, want 3", len(p.Rules))
	}
}

func TestAProgramPermitCoversEveryArgumentUnderOneEffectAndNoOther(t *testing.T) {
	// "All df commands" is one rule now, where df, df -h and df -h / used to
	// be three — and the same looseness is bounded by the effect the permit
	// was granted for, so it never becomes "all find commands, including the
	// one that deletes".
	askEverything := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
	}
	df := askEverything.WithRule(content.InvocationRule{
		ID:               "df-observe",
		Selector:         content.InvocationSelector{Program: "df"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectObserve,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	for _, command := range [][]string{
		{"df"},
		{"df", "-h"},
		{"df", "-h", "/"},
		{"df", "--output=source"},
	} {
		inv := content.Invocation{Commands: [][]string{command}, Parsed: true}
		if got := df.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionPermit {
			t.Errorf("%v = %q, want permit — one rule covers every argument list", command, got)
		}
	}

	find := askEverything.WithRule(content.InvocationRule{
		ID:               "find-observe",
		Selector:         content.InvocationSelector{Program: "find"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectObserve,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	deleting := content.Invocation{Commands: [][]string{{"find", ".", "-delete"}}, Parsed: true}
	if got := find.DecisionForInvocation(content.EffectMutateDestructive, deleting); got != content.DecisionAsk {
		t.Errorf("find . -delete = %q, want the row's ask — a permit granted while find was reading does not reach a destructive call", got)
	}
	// The same command classified as what the permit was granted for is
	// permitted, so the guard is the effect and not the argument list.
	reading := content.Invocation{Commands: [][]string{{"find", ".", "-name", "*.go"}}, Parsed: true}
	if got := find.DecisionForInvocation(content.EffectObserve, reading); got != content.DecisionPermit {
		t.Errorf("find . -name = %q, want permit — the permit reaches the effect it was granted for", got)
	}
	// A compound line is not an invocation of the program: every subcommand
	// must be that word, or a permit for df would carry rm with it.
	compound := content.Invocation{
		Commands: [][]string{{"df", "-h"}, {"rm", "-rf", "/"}},
		Parsed:   true,
	}
	if got := df.DecisionForInvocation(content.EffectObserve, compound); got != content.DecisionAsk {
		t.Errorf("df -h ; rm -rf / = %q, want ask — a program permit covers that program alone", got)
	}
}

func TestAFeatureRefusalBeatsAProgramPermitForTheSameCall(t *testing.T) {
	// "Permit curl, but never when it writes a file." The refusal matches the
	// FEATURE the classifier recorded, not the spelling -o, so the five ways
	// of writing the same option are one rule.
	p := content.EffectPolicy{
		Observe:       content.EffectRow{Decision: content.DecisionAsk},
		CrossBoundary: content.EffectRow{Decision: content.DecisionAsk},
	}.WithRule(content.InvocationRule{
		ID:               "curl-observe",
		Selector:         content.InvocationSelector{Program: "curl"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectCrossBoundary,
		EvaluatorVersion: content.EvaluatorVersion,
	}).WithRule(content.InvocationRule{
		ID: "curl-writes",
		Selector: content.InvocationSelector{
			HasFeature: &content.FeatureRef{Program: "curl", Feature: content.FeatureWritesOptionNamedPath},
		},
		Decision:         content.DecisionRefuse,
		EvaluatorVersion: content.EvaluatorVersion,
	})

	writing := content.Invocation{
		Commands: [][]string{{"curl", "-o", "/tmp/x", "https://y"}},
		Parsed:   true,
		Resources: content.ResourceReport{
			Features: []string{content.FeatureWritesOptionNamedPath},
		},
	}
	if got := p.EvaluateInvocation(content.EffectCrossBoundary, writing, nil); got.Decision != content.DecisionRefuse {
		t.Fatalf("curl -o = %+v, want refuse — the most restrictive matching rule wins", got)
	}
	// Without the feature the permit is the only matching rule, so the
	// refusal above is the feature and not the program word.
	plain := content.Invocation{
		Commands: [][]string{{"curl", "https://y"}},
		Parsed:   true,
	}
	if got := p.EvaluateInvocation(content.EffectCrossBoundary, plain, nil); got.Decision != content.DecisionPermit {
		t.Fatalf("curl https://y = %+v, want permit — only the writing call is refused", got)
	}
}

func TestADisqualifiedInvocationBypassesEveryRuleIncludingARefusal(t *testing.T) {
	// A disqualified command receives the MATRIX answer and can never receive
	// a rule exception — in either direction. The refusal end is the one that
	// is easy to get wrong, because it looks safe to let it through.
	refusing := content.EffectPolicy{
		Observe: content.EffectRow{Decision: content.DecisionPermit},
	}.WithRule(content.InvocationRule{
		ID: "curl-writes",
		Selector: content.InvocationSelector{
			HasFeature: &content.FeatureRef{Program: "curl", Feature: content.FeatureWritesOptionNamedPath},
		},
		Decision:         content.DecisionRefuse,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	disqualified := content.Invocation{
		Commands:     [][]string{{"curl", "-o", "/tmp/x", "https://y"}},
		Parsed:       true,
		Disqualified: true,
		Resources: content.ResourceReport{
			Features: []string{content.FeatureWritesOptionNamedPath},
		},
	}
	if got := refusing.DecisionForInvocation(content.EffectObserve, disqualified); got != content.DecisionPermit {
		t.Errorf("disqualified invocation = %q, want the row's own permit — rules are bypassed entirely", got)
	}
	// And the same rule DOES reach the qualified form, so the bypass above is
	// the disqualification and not a rule that never matches.
	qualified := disqualified
	qualified.Disqualified = false
	if got := refusing.DecisionForInvocation(content.EffectObserve, qualified); got != content.DecisionRefuse {
		t.Errorf("qualified invocation = %q, want refuse", got)
	}
}

func TestOnlyAnExactSelectorCanBeSavedFromAPrompt(t *testing.T) {
	// The prompt's answer is saved over the command line a person was shown,
	// and no widening form is reachable from it.
	rule, reason := content.StandingRule(content.Invocation{
		Commands: [][]string{{"df", "-h"}},
		Parsed:   true,
	})
	if reason != "" {
		t.Fatalf("standing rule reason = %q, want none", reason)
	}
	if rule.Selector.Program != "" || rule.Selector.HasFeature != nil {
		t.Fatalf("standing rule selector = %#v, want an exact selector alone", rule.Selector)
	}
	if len(rule.Selector.Exact) != 1 {
		t.Fatalf("standing rule exact = %#v, want the one command shown", rule.Selector.Exact)
	}
}

// A rule's provenance is what makes it an object a page can take back: where
// it came from, when, and — the part that has teeth — the reading of commands
// it was agreed to under. Task 1 changed that reading, so a Program permit
// saved before it was agreed to on a false account of what the command does.
func TestAWidenedRuleSavedUnderAnOlderReadingIsInertUntilConfirmed(t *testing.T) {
	askEverything := content.EffectPolicy{
		Observe:          content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible: content.EffectRow{Decision: content.DecisionAsk},
	}
	stale := content.InvocationRule{
		ID:               "df-observe",
		Selector:         content.InvocationSelector{Program: "df"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectObserve,
		CreatedAt:        time.Unix(1700000000, 0).UTC(),
		Source:           content.SourceAnswered,
		EvaluatorVersion: content.EvaluatorVersion - 1,
	}
	// An exact rule names a literal command line the person was shown, so its
	// meaning does not move when the classifier learns to see more: it is not
	// in this danger and is never skipped for its version.
	exact := content.InvocationRule{
		ID:               "uptime-exact",
		Selector:         content.InvocationSelector{Exact: [][]string{{"uptime"}}},
		Decision:         content.DecisionPermit,
		CreatedAt:        time.Unix(1700000000, 0).UTC(),
		Source:           content.SourceAnswered,
		EvaluatorVersion: content.EvaluatorVersion - 1,
	}
	policy := askEverything.WithRule(stale).WithRule(exact)

	df := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}
	if got := policy.DecisionForInvocation(content.EffectObserve, df); got != content.DecisionAsk {
		t.Errorf("df -h = %q, want the row's ask — a widened rule saved under an older reading of commands still applied", got)
	}
	up := content.Invocation{Commands: [][]string{{"uptime"}}, Parsed: true}
	if got := policy.DecisionForInvocation(content.EffectObserve, up); got != content.DecisionPermit {
		t.Errorf("uptime = %q, want permit — an exact rule names the command line it was shown and does not go stale", got)
	}

	needing := policy.RulesNeedingConfirmation()
	if len(needing) != 1 || needing[0].ID != "df-observe" {
		t.Fatalf("RulesNeedingConfirmation() = %+v, want the one stale program rule", needing)
	}

	confirmed, ok := policy.ConfirmRule("df-observe")
	if !ok {
		t.Fatal("ConfirmRule reported no such rule")
	}
	if got := confirmed.DecisionForInvocation(content.EffectObserve, df); got != content.DecisionPermit {
		t.Errorf("df -h after confirming = %q, want permit", got)
	}
	if got := confirmed.RulesNeedingConfirmation(); len(got) != 0 {
		t.Errorf("RulesNeedingConfirmation() after confirming = %+v, want none", got)
	}
	want := stale
	want.EvaluatorVersion = content.EvaluatorVersion
	if got := confirmed.Rules[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("confirmed rule = %+v, want %+v — confirming rewrites the version and NOTHING else", got, want)
	}
	// EffectPolicy is a value everywhere else and stays one: the policy the
	// caller held is not confirmed behind its back.
	if got := policy.Rules[0].EvaluatorVersion; got != content.EvaluatorVersion-1 {
		t.Errorf("the original policy's rule version = %d, want it untouched at %d", got, content.EvaluatorVersion-1)
	}

	if same, ok := policy.ConfirmRule("no-such-rule"); ok || !reflect.DeepEqual(same.Rules, policy.Rules) {
		t.Errorf("ConfirmRule on an unknown id = (%+v, %v), want the policy unchanged and false", same.Rules, ok)
	}
}

// A MISSING evaluator version is UNKNOWN, and unknown is not current: an
// operator-written document that says nothing about the reading it was
// written under behaves exactly like one saved under an older reading.
func TestAWidenedRuleWithNoEvaluatorVersionIsInert(t *testing.T) {
	doc := `{
		"observe": {"decision": "ask", "scopes": []},
		"rules": [{"id": "r1", "selector": {"program": "df"}, "decision": "permit", "grantedUnder": "observe"}]
	}`
	p, err := content.ParseEffectPolicy([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	df := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}
	if got := p.DecisionForInvocation(content.EffectObserve, df); got != content.DecisionAsk {
		t.Errorf("df -h = %q, want the row's ask — an unstated reading is unknown, not current", got)
	}
	needing := p.RulesNeedingConfirmation()
	if len(needing) != 1 || needing[0].ID != "r1" {
		t.Fatalf("RulesNeedingConfirmation() = %+v, want the one rule that says nothing about its reading", needing)
	}
}

// Where a rule came from is a fact about the rule, not about the page showing
// it: a rule a person answered into being and a rule an operator wrote by hand
// are different objects with different trust.
func TestEveryRuleCarriesWhereItCameFrom(t *testing.T) {
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}
	first, err := content.LiteralInvocationRule(inv, content.DecisionPermit)
	if err != nil {
		t.Fatalf("LiteralInvocationRule: %v", err)
	}
	second, err := content.LiteralInvocationRule(inv, content.DecisionPermit)
	if err != nil {
		t.Fatalf("LiteralInvocationRule: %v", err)
	}
	// NEITHER carries an id, and that is the point (nocx-2019q). A rule
	// that has not been stored has no name yet: the id is the DOCUMENT's
	// name for it, minted on the one parse every stored policy crosses
	// (TestADocumentsRulesAreIdentifiedAndWritten is where that is
	// asserted). Minting here named rules that are never stored — the
	// approval prompt builds one per question just to read its Label —
	// and put a second mint in front of the store's, so the id a caller
	// held was not certainly the id the document wore.
	if first.ID != "" || second.ID != "" {
		t.Fatalf("ids %q and %q — a rule is named by the document that stores it, not at creation",
			first.ID, second.ID)
	}
	if first.Source != content.SourceAnswered {
		t.Errorf("source = %q, want %q — a prompt's rule is answered", first.Source, content.SourceAnswered)
	}
	if first.EvaluatorVersion != content.EvaluatorVersion {
		t.Errorf("evaluatorVersion = %d, want the current %d", first.EvaluatorVersion, content.EvaluatorVersion)
	}
	if first.CreatedAt.IsZero() {
		t.Error("createdAt is the zero time on a rule that was just created")
	}
}

// A document IS written, and an operator must be able to hand-write one
// without inventing ids: the id is minted on parse and becomes stable the
// next time the document is saved.
func TestADocumentsRulesAreIdentifiedAndWritten(t *testing.T) {
	doc := `{
		"observe": {"decision": "ask", "scopes": []},
		"rules": [{"selector": {"exact": [["df", "-h"]]}, "decision": "permit"}]
	}`
	p, err := content.ParseEffectPolicy([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Rules) != 1 {
		t.Fatalf("parsed %d rules, want 1", len(p.Rules))
	}
	rule := p.Rules[0]
	if rule.ID == "" {
		t.Fatal("a rule with no id parsed without being given one")
	}
	if rule.Source != content.SourceWritten {
		t.Errorf("source = %q, want %q — a document is written", rule.Source, content.SourceWritten)
	}
	if !rule.CreatedAt.IsZero() {
		t.Errorf("createdAt = %v, want the zero time — a creation time is not invented", rule.CreatedAt)
	}
	// The minted id survives the round trip a save is.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), rule.ID) {
		t.Fatalf("re-marshalled policy %s does not carry the minted id %q", raw, rule.ID)
	}
	back, err := content.ParseEffectPolicy(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := back.Rules[0].ID; got != rule.ID {
		t.Errorf("id after a save = %q, want the stable %q", got, rule.ID)
	}
}

// An id is what a page takes a rule back by, so two rules may not share one:
// a document that does is unparseable, and the error names the id.
func TestADocumentWhoseRulesShareAnIDDoesNotParse(t *testing.T) {
	doc := `{
		"observe": {"decision": "ask", "scopes": []},
		"rules": [
			{"id": "dup", "selector": {"exact": [["df", "-h"]]}, "decision": "permit"},
			{"id": "dup", "selector": {"exact": [["uptime"]]}, "decision": "permit"}
		]
	}`
	_, err := content.ParseEffectPolicy([]byte(doc))
	if err == nil {
		t.Fatal("a document with two rules under one id parsed")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error %q does not name the duplicated id", err)
	}

	bad := `{
		"observe": {"decision": "ask", "scopes": []},
		"rules": [{"id": "r1", "selector": {"exact": [["df"]]}, "decision": "permit", "source": "invented"}]
	}`
	if _, err := content.ParseEffectPolicy([]byte(bad)); err == nil {
		t.Fatal("a rule claiming a source outside the two constants parsed")
	}
}

func TestAStaleRefusalStillRefuses(t *testing.T) {
	// The version guard exists because a PERMIT is a claim about what a
	// command does, and a later reading of commands can falsify that claim.
	// A refusal makes no such claim. A richer reading can only make a loose
	// refusal cover MORE, which is the safe direction — the same asymmetry
	// that lets a HasFeature selector over-match. So a refusal saved under an
	// older reading keeps refusing, and never falls back to a row that
	// permits: a version bump nobody performed is not a place to lose a
	// safety control.
	permitEverything := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionPermit},
		MutateDestructive: content.EffectRow{Decision: content.DecisionPermit},
	}
	stale := content.InvocationRule{
		ID:               "no-curl",
		Selector:         content.InvocationSelector{Program: "curl"},
		Decision:         content.DecisionRefuse,
		CreatedAt:        time.Unix(1700000000, 0).UTC(),
		Source:           content.SourceWritten,
		EvaluatorVersion: content.EvaluatorVersion - 1,
	}
	policy := permitEverything.WithRule(stale)

	curl := content.Invocation{Commands: [][]string{{"curl", "https://example.com"}}, Parsed: true}
	if got := policy.DecisionForInvocation(content.EffectObserve, curl); got != content.DecisionRefuse {
		t.Errorf("curl = %q, want refuse — a refusal saved under an older reading went inert and the permitting row took over", got)
	}
	if got := policy.RulesNeedingConfirmation(); len(got) != 0 {
		t.Errorf("RulesNeedingConfirmation() = %+v, want none — a refusal is never waiting on a person to re-agree to it", got)
	}
}
