package content

// RuleWrite and EffectPolicy.ChangedByRuleWrite: what ONE edit to the standing
// answers would change about what a policy decides.
//
// It exists for the runs already in flight. A run's grant is minted from the
// policy as it was when the run started and is immutable for the run (ADR-0020
// decision 5), so an answer taken back on the settings page does not reach it.
// That is correct and must stay — it is what makes a grant a grant — but it
// means a person who has just revoked something needs to be told which live
// work is still deciding under the old answer, and the only honest way to say
// that is to ASK EACH RUN'S OWN GRANT. This is the question those runs are
// asked; internal/transport's revocation path is the one caller.
//
// The alternative reading — "every live run holds a grant minted from a policy
// that contained the rule, so every live run is affected" — is cheaper and
// wrong in a way a person cannot check: it reports six runs affected by an
// answer that governs none of them, and a number a person cannot trust is
// worse than no number at all. See runsUnreachedByRuleWrite in
// internal/transport/ws_policy.go, where the count is computed and the choice
// is written down in full.

// RuleWrite is ONE edit to a policy's standing answers, stated so a policy can
// be asked what the edit would do to it.
//
// ID names the rule the write is ABOUT: the rule being replaced, or the rule
// being removed. Rule is what it becomes, and a nil Rule is a forget. A write
// with an empty ID and a non-nil Rule is a NEW answer, which is still a change
// a live run will not see — a refusal written while three runs are in flight
// reaches none of them, and that is exactly the case a person most needs told.
type RuleWrite struct {
	ID   string
	Rule *InvocationRule
}

// ChangedByRuleWrite reports whether this policy would decide differently,
// about the command lines the write speaks about, once the write lands.
//
// THE PROBE, and why it is the whole question rather than a sample of it. A
// rule is an exception to the EFFECT layer and to nothing else
// (EvaluateInvocation states that order once): it can only change the outcome
// for invocations its own selector matches, so the invocations a write can
// possibly change are exactly those covered by the rule it removes, the rule
// it replaces, or the rule it writes. One representative invocation per
// selector is therefore not a sample — every command line a selector covers
// takes the same path through the effect and shape layers, because the layers
// read the selector's shape and never the tokens beyond it.
//
// The probe names no resources, so the RESOURCE layer bounds nothing and the
// comparison measures the effect and shape layers alone. That is deliberate:
// the resource layer is about the command a call actually made, which is not a
// fact any policy holds, and a probe that invented one would answer a question
// about a call nobody placed.
//
// Every effect row is tried, because a rule that states no GrantedUnder speaks
// under all seven and a rule that states one is checked against the effect the
// CALL classified as — neither is knowable from the rule alone.
//
// The comparison is DecisionForInvocation on both sides: the same evaluator, on
// the same fence (the run's own, carried in its grant), differing only in the
// rules. Nothing here re-implements the precedence order, so nothing here can
// drift from it.
func (p EffectPolicy) ChangedByRuleWrite(w RuleWrite) bool {
	after := p.withRuleWrite(w)
	for _, inv := range p.ruleWriteProbes(w) {
		for _, e := range latticeEffects {
			if p.DecisionForInvocation(e, inv) != after.DecisionForInvocation(e, inv) {
				return true
			}
		}
	}
	return false
}

// withRuleWrite returns p with the write applied to its rules, in memory and
// for the length of one question.
//
// It is unexported, and that is the point: the DOCUMENT's version of this
// edit lives in assistant.GlobalPolicyStore, under the store's own lock and
// through content.ParseEffectPolicy's strict gate, where provenance is
// preserved and an id is minted. Exporting a second apply would be a second
// answer to "what does the document become". This one answers a narrower
// question — what would this FROZEN grant decide — and never reaches a store.
//
// Everything but Rules is copied by the struct assignment, the unexported run
// fence included, so the comparison is made under the run's own bound.
func (p EffectPolicy) withRuleWrite(w RuleWrite) EffectPolicy {
	q := p
	rules := make([]InvocationRule, 0, len(p.Rules)+1)
	replaced := false
	for _, rule := range p.Rules {
		if w.ID != "" && rule.ID == w.ID {
			if w.Rule != nil {
				rules = append(rules, *w.Rule)
				replaced = true
			}
			continue
		}
		rules = append(rules, rule)
	}
	if w.Rule != nil && !replaced {
		// A new answer, or a replacement of a rule this grant never held —
		// which is the same thing from the frozen policy's point of view,
		// and is appended for the same reason the store appends: document
		// order is precedence-free (the loop takes the most restrictive
		// match), so position carries no meaning to get wrong here.
		rules = append(rules, *w.Rule)
	}
	q.Rules = rules
	return q
}

// ruleWriteProbes builds one representative invocation per selector the write
// touches: the stored rule wearing the write's id, and the rule the write puts
// there. Both, because a change can move the selector — an answer edited from
// `df -h` to `df -k` speaks about two command lines and reaches runs through
// either.
func (p EffectPolicy) ruleWriteProbes(w RuleWrite) []Invocation {
	var out []Invocation
	if w.ID != "" {
		for _, rule := range p.Rules {
			if rule.ID == w.ID {
				if probe, ok := selectorProbe(rule.Selector); ok {
					out = append(out, probe)
				}
				break
			}
		}
	}
	if w.Rule != nil {
		if probe, ok := selectorProbe(w.Rule.Selector); ok {
			out = append(out, probe)
		}
	}
	return out
}

// selectorProbe is the one invocation that stands for everything a selector
// covers. The three shapes are the three the selector is a closed sum of; a
// selector stating none of them covers nothing, and answers false rather than
// a probe that would match every rule.
func selectorProbe(s InvocationSelector) (Invocation, bool) {
	switch {
	case s.HasFeature != nil:
		return Invocation{
			Commands:  [][]string{{s.HasFeature.Program}},
			Parsed:    true,
			Resources: ResourceReport{Features: []string{s.HasFeature.Feature}},
		}, true
	case s.Program != "":
		return Invocation{Commands: [][]string{{s.Program}}, Parsed: true}, true
	case len(s.Exact) > 0:
		return Invocation{Commands: s.Exact, Parsed: true}, true
	}
	return Invocation{}, false
}
