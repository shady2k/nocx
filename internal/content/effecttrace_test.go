package content_test

// The decision explains itself, in the order it was taken (nocx-8nktm).
//
// Every assertion here is about ORDER and about the STEP THAT WAS ACTUALLY
// TAKEN, never about a set of facts a reader could assemble from the outcome.
// A page has to tell a person why their rule did not apply, and "it lost" and
// "it was never read" are different sentences; so are "the reading of commands
// moved under it" and "it was granted for something milder". A trace that
// collapsed either pair would be an explanation that lies in exactly the cases
// a person asks about.

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// kinds is the trace as a reader sees it: the order, and nothing else. Every
// assertion below compares against a literal sequence, because a set of steps
// is not an explanation.
func kinds(v content.Verdict) []content.TraceKind {
	out := make([]content.TraceKind, 0, len(v.Trace))
	for _, s := range v.Trace {
		out = append(out, s.Kind)
	}
	return out
}

func sameKinds(got []content.TraceKind, want ...content.TraceKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// askEverything is the matrix a person starts from: nothing is permitted and
// nothing is refused, so every step below is one a rule or a resource took.
func askEverything() content.EffectPolicy {
	return content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
	}
}

func TestAPermittedCallNamesTheRowTheRuleAndTheResourceInThatOrder(t *testing.T) {
	p := askEverything().WithRule(content.InvocationRule{
		ID:               "df-answered",
		Selector:         content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision:         content.DecisionPermit,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}

	v := p.ExplainInvocation(content.EffectObserve, inv)
	if v.Decision != content.DecisionPermit {
		t.Fatalf("decision = %q, want permit", v.Decision)
	}
	if !sameKinds(kinds(v),
		content.TraceEffectRow, content.TraceRuleMatched, content.TraceResourceInside) {
		t.Fatalf("trace = %v, want the row consulted, then the rule that permitted, then the resource layer", kinds(v))
	}
	if v.Trace[0].Effect != content.EffectObserve || v.Trace[0].Decision != content.DecisionAsk {
		t.Errorf("row step = %+v, want the observe row and the ask it holds", v.Trace[0])
	}
	if v.Trace[1].RuleID != "df-answered" || v.Trace[1].Decision != content.DecisionPermit {
		t.Errorf("rule step = %+v, want the rule that permitted, named", v.Trace[1])
	}
	if v.Trace[2].Decision != content.DecisionPermit {
		t.Errorf("resource step = %+v, want the decision the layers above reached", v.Trace[2])
	}
}

func TestARefusingRowSaysTheRulesWereNotConsulted(t *testing.T) {
	// A person whose standing permit for `df -h` did not apply needs to learn
	// that the rules were never reached — not that theirs lost a contest it
	// was never in.
	p := askEverything()
	p.Observe = content.EffectRow{Decision: content.DecisionRefuse}
	p = p.WithRule(content.InvocationRule{
		ID:               "df-answered",
		Selector:         content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision:         content.DecisionPermit,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}

	v := p.ExplainInvocation(content.EffectObserve, inv)
	if v.Decision != content.DecisionRefuse {
		t.Fatalf("decision = %q, want refuse", v.Decision)
	}
	if !sameKinds(kinds(v), content.TraceEffectRow, content.TraceRowRefuses) {
		t.Fatalf("trace = %v, want the row consulted and then the rules NOT consulted", kinds(v))
	}
	for _, step := range v.Trace {
		if step.RuleID != "" {
			t.Fatalf("step %+v names a rule; a refusing row is read before any rule is", step)
		}
	}
}

func TestADisqualifiedCommandSaysTheRulesWereNotConsulted(t *testing.T) {
	// The other short circuit, and it is a DIFFERENT fact: the row did not
	// refuse — the command's own text put it beyond a rule's reach.
	p := askEverything().WithRule(content.InvocationRule{
		ID:               "df-answered",
		Selector:         content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision:         content.DecisionPermit,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true, Disqualified: true}

	v := p.ExplainInvocation(content.EffectObserve, inv)
	if v.Decision != content.DecisionAsk {
		t.Fatalf("decision = %q, want the row's ask", v.Decision)
	}
	if !sameKinds(kinds(v), content.TraceEffectRow, content.TraceDisqualified) {
		t.Fatalf("trace = %v, want the row consulted and then the command disqualified", kinds(v))
	}
}

func TestASkippedRuleNamesTheGuardThatSkippedIt(t *testing.T) {
	// Two rules match, both are skipped, and the two reasons must not
	// collapse into one "did not match": one waits for a person to re-read
	// what it now means, the other will never apply to this effect at all.
	p := askEverything().WithRule(content.InvocationRule{
		ID:               "df-stale",
		Selector:         content.InvocationSelector{Program: "df"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectObserve,
		EvaluatorVersion: content.EvaluatorVersion - 1,
	}).WithRule(content.InvocationRule{
		ID:               "df-elsewhere",
		Selector:         content.InvocationSelector{Program: "df"},
		Decision:         content.DecisionPermit,
		GrantedUnder:     content.EffectObserve,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}

	v := p.ExplainInvocation(content.EffectMutateDestructive, inv)
	if v.Decision != content.DecisionAsk {
		t.Fatalf("decision = %q, want the row's ask — neither skipped rule reaches this call", v.Decision)
	}
	if !sameKinds(kinds(v),
		content.TraceEffectRow, content.TraceRuleStale, content.TraceRuleOtherEffect,
		content.TraceResourceInside) {
		t.Fatalf("trace = %v, want the two guards told apart, in document order", kinds(v))
	}
	if v.Trace[1].RuleID != "df-stale" || v.Trace[1].Detail == "" {
		t.Errorf("stale step = %+v, want the rule named and the reading said", v.Trace[1])
	}
	if v.Trace[2].RuleID != "df-elsewhere" || v.Trace[2].Effect != content.EffectObserve {
		t.Errorf("widening step = %+v, want the rule named and the effect it was granted under", v.Trace[2])
	}
}

func TestTheResourceStepTellsAFenceFromARowScope(t *testing.T) {
	// The two are different products: one is a question a person can answer
	// by widening a row, one is a bound no answer reaches. A trace that said
	// only "out of scope" would offer the wrong affordance for half of them.
	outside := content.Invocation{
		Commands: [][]string{{"cat", "/etc/hosts"}},
		Parsed:   true,
		Resources: content.ResourceReport{
			Resources: []content.Resource{{Path: "/etc/hosts", Verb: content.ResourceRead}},
		},
	}

	editable := askEverything()
	editable.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	v := editable.ExplainInvocation(content.EffectObserve, outside)
	if v.Decision != content.DecisionAsk || v.Cause != content.OutOfScopeRowScope {
		t.Fatalf("verdict = %+v, want ask out of the row's own scopes", v)
	}
	if !sameKinds(kinds(v), content.TraceEffectRow, content.TraceResourceOutsideRowScope) {
		t.Fatalf("trace = %v, want the resource step to name the row scope", kinds(v))
	}

	fenced := askEverything()
	fenced.Observe = content.EffectRow{Decision: content.DecisionPermit}
	fenced = fenced.WithRunScopes([]content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}})
	v = fenced.ExplainInvocation(content.EffectObserve, outside)
	if v.Decision != content.DecisionRefuse || v.Cause != content.OutOfScopeFence {
		t.Fatalf("verdict = %+v, want refuse outside the run fence", v)
	}
	if !sameKinds(kinds(v), content.TraceEffectRow, content.TraceResourceOutsideFence) {
		t.Fatalf("trace = %v, want the resource step to name the fence", kinds(v))
	}
}

func TestTheTraceClosesOnTheStepThatDecided(t *testing.T) {
	// The interval's far end, stated as an interval: a trace opens when
	// evaluation begins and CLOSES when the verdict returns, and its last
	// step is the one that decided — it carries the decision the verdict
	// carries. Every path out of the evaluator is walked here, so a path
	// added later without a step fails this rather than going unexplained.
	refusing := askEverything()
	refusing.Observe = content.EffectRow{Decision: content.DecisionRefuse}

	scoped := askEverything()
	scoped.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}

	ruleRefuses := askEverything().WithRule(content.InvocationRule{
		ID:               "df-never",
		Selector:         content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision:         content.DecisionRefuse,
		EvaluatorVersion: content.EvaluatorVersion,
	})

	df := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}
	reading := content.Invocation{
		Commands: [][]string{{"cat", "/etc/hosts"}},
		Parsed:   true,
		Resources: content.ResourceReport{
			Resources: []content.Resource{{Path: "/etc/hosts", Verb: content.ResourceRead}},
		},
	}

	cases := map[string]struct {
		policy content.EffectPolicy
		inv    content.Invocation
	}{
		"unparsed":         {askEverything(), content.Invocation{}},
		"row refuses":      {refusing, df},
		"disqualified":     {askEverything(), content.Invocation{Commands: [][]string{{"df"}}, Parsed: true, Disqualified: true}},
		"plain row":        {askEverything(), df},
		"out of row scope": {scoped, reading},
		"rule refuses":     {ruleRefuses, df},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := c.policy.ExplainInvocation(content.EffectObserve, c.inv)
			if len(v.Trace) == 0 {
				t.Fatalf("no trace at all for %+v", v)
			}
			last := v.Trace[len(v.Trace)-1]
			if last.Decision != v.Decision {
				t.Fatalf("trace closes on %+v, but the verdict decided %q", last, v.Decision)
			}
		})
	}
}

func TestTheHotPathPaysForNoTrace(t *testing.T) {
	// A trace is opt-in, and the retained shape stays the retained shape:
	// every caller that wants the outcome and not the reason allocates
	// nothing.
	p := askEverything().WithRule(content.InvocationRule{
		ID:               "df-answered",
		Selector:         content.InvocationSelector{Exact: [][]string{{"df", "-h"}}},
		Decision:         content.DecisionPermit,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	inv := content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}

	if v := p.EvaluateInvocation(content.EffectObserve, inv, nil); v.Trace != nil {
		t.Errorf("EvaluateInvocation built a trace nobody asked for: %v", v.Trace)
	}
	if got := p.DecisionForInvocation(content.EffectObserve, inv); got != content.DecisionPermit {
		t.Errorf("DecisionForInvocation = %q, want permit — the retained shape is unchanged", got)
	}
}
