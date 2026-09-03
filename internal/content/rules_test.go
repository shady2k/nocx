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
		ID:           "df-observe",
		Selector:     content.InvocationSelector{Program: "df"},
		Decision:     content.DecisionPermit,
		GrantedUnder: content.EffectObserve,
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
		ID:           "find-observe",
		Selector:     content.InvocationSelector{Program: "find"},
		Decision:     content.DecisionPermit,
		GrantedUnder: content.EffectObserve,
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
		ID:           "curl-observe",
		Selector:     content.InvocationSelector{Program: "curl"},
		Decision:     content.DecisionPermit,
		GrantedUnder: content.EffectCrossBoundary,
	}).WithRule(content.InvocationRule{
		ID: "curl-writes",
		Selector: content.InvocationSelector{
			HasFeature: &content.FeatureRef{Program: "curl", Feature: content.FeatureWritesOptionNamedPath},
		},
		Decision: content.DecisionRefuse,
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
		Decision: content.DecisionRefuse,
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
