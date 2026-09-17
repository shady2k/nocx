package content

// The trace vocabulary: what a decision is made OF, so a page can tell a
// person why theirs came out the way it did (nocx-8nktm).
//
// EvaluateInvocation crosses three layers in a fixed order and its doc comment
// is the ONE place that order is written down. A surface cannot reconstruct it
// from a Verdict — the outcome does not say whether a rule lost or was never
// read — and it must not try: a second implementation of the precedence order
// in the renderer would agree with this one everywhere anyone looked and
// disagree somewhere nobody did, which is the "am I in an ssh context" failure
// AGENTS.md records. So the evaluator RECORDS its steps as it takes them.
// Nothing else may derive one, here or over the wire.
//
// A step names EFFECTS, RULES and RESOURCES. It never names a tool
// (ADR-0028 decision 4): the policy is over resources and effects, and an
// explanation of it inherits that vocabulary — a trace that named a tool would
// be a tool-shaped configuration surface arriving through the back door.

// TraceKind is one thing the evaluator did, in the closed set of things it
// can do. The set is closed on purpose: a surface switches on it to choose a
// sentence and an affordance, and an open vocabulary would leave a step with
// no sentence at all.
//
// The three groups below are the three layers, in the order they are crossed.
type TraceKind string

const (
	// ── the EFFECT layer ──────────────────────────────────────────────
	//
	// TraceUnparsed: the command could not be read at all, so no row was
	// consulted and no rule could speak about a shape nobody parsed. The
	// answer is ask, and this is the whole trace.
	TraceUnparsed TraceKind = "unparsed"
	// TraceEffectRow: the matrix row for this effect was consulted.
	// Effect names the row and Decision is what it holds — the ask an
	// unstated row decides included. It is always the first step of a
	// parsed evaluation, because the matrix decides first.
	TraceEffectRow TraceKind = "effect-row"
	// TraceRowRefuses: the row refuses, and a rule is an exception to the
	// effect layer alone — never past a refusal. So the rules were NOT
	// READ. A person whose standing permit did not apply is being told it
	// was never reached, which is a different sentence from "it lost".
	TraceRowRefuses TraceKind = "row-refuses"
	// TraceDisqualified: the command's own text puts it beyond a rule's
	// reach — a shell feature whose effect cannot be determined — so it
	// takes the matrix answer and the rules were NOT READ. The row did not
	// refuse; that is what makes this a different fact from the step
	// above.
	TraceDisqualified TraceKind = "disqualified"

	// ── the SHAPE layer ───────────────────────────────────────────────
	//
	// TraceRuleMatched: a rule's selector covered this invocation and it
	// counted. RuleID names it and Decision is what it decides; among
	// several, the evaluator keeps the most restrictive, which is visible
	// as the decision the resource step is handed.
	TraceRuleMatched TraceKind = "rule-matched"
	// TraceRuleStale: a loose PERMIT saved under an earlier reading of
	// commands. It matched and was skipped: it was agreed to on an account
	// of the command that no longer holds, and it stays inert until a
	// person reads what it now means and says so. Detail names the two
	// readings.
	TraceRuleStale TraceKind = "rule-stale"
	// TraceRuleOtherEffect: a widening permit whose GrantedUnder is not
	// the effect this call classified as. It matched and was skipped —
	// the permit was granted while the command did something milder and
	// does not reach this call. Effect carries the effect it WAS granted
	// under, which is the half a person needs in order to see the gap.
	TraceRuleOtherEffect TraceKind = "rule-other-effect"

	// ── the RESOURCE layer ────────────────────────────────────────────
	//
	// TraceResourceInside: every resource the call named lay inside the
	// fence and inside the row's scopes, so the decision the layers above
	// reached stands. Decision is that decision.
	TraceResourceInside TraceKind = "resource-inside"
	// TraceResourceOutsideFence: a resource lies outside an IMMUTABLE
	// bound — the run fence the mint supplied, or the narrowed capability.
	// The answer is refuse and no answer a person can give changes it, so
	// a surface offers no widening here. Verdict.Resource names the one
	// resource.
	TraceResourceOutsideFence TraceKind = "resource-outside-fence"
	// TraceResourceOutsideRowScope: a resource lies outside the SELECTED
	// row's own scopes, which an operator wrote and can widen. The answer
	// is ask, and the question is answerable — this is the one step a
	// surface may offer to act on.
	TraceResourceOutsideRowScope TraceKind = "resource-outside-row-scope"
	// TraceResourceNotReached: the decision was already a refusal when the
	// resource layer was entered, and that layer can only make a decision
	// more restrictive. No resource was compared, and saying one was
	// inside would be a claim nobody checked.
	TraceResourceNotReached TraceKind = "resource-not-reached"
)

// TraceStep is one step, as it was taken. The fields are the vocabulary of
// the layers and nothing else: which effect row, which rule, what was
// decided, and prose for the half a reader cannot get from the other three.
//
// The zero value of a field means "this step is not about that": a resource
// step names no rule, a row step names no rule, and a step that decided
// nothing on its own carries no Decision — except that the LAST step always
// carries the verdict's decision (see Verdict.Trace).
//
// The json tags are the wire form (contracts/policy.explain.schema.json): a
// step travels as it is, because the renderer must not have to rebuild one.
// Every field but Kind is omitted when it is the zero value, so the shape a
// surface receives says which of them this step is about.
type TraceStep struct {
	// Kind is what the evaluator did.
	Kind TraceKind `json:"kind"`
	// RuleID names the rule this step is about, on the three rule steps.
	RuleID string `json:"ruleId,omitempty"`
	// Effect is the row's effect on TraceEffectRow, and the effect a
	// widening permit was GRANTED UNDER on TraceRuleOtherEffect.
	Effect Effect `json:"effect,omitempty"`
	// Decision is what this step decided or read.
	Decision Decision `json:"decision,omitempty"`
	// Detail is prose for what the typed fields cannot carry — the two
	// readings a stale rule sits between, or why a bound cannot be
	// widened. It never names a tool.
	Detail string `json:"detail,omitempty"`
}

// evalTrace is the recorder the evaluator writes into as it decides, and the
// reason the order is not written down twice: there is no builder that walks
// a Verdict afterwards, because such a builder would have to know the order.
//
// A nil *evalTrace records nothing and allocates nothing, which is what makes
// the trace opt-in: EvaluateInvocation and DecisionForInvocation — the
// retained shapes for every caller that wants the outcome and not the reason —
// pass nil and pay one nil check per step.
type evalTrace struct {
	steps []TraceStep
}

// step appends one step. It is a method on a possibly-nil receiver so the
// evaluator has no `if tracing` branches to get wrong: the untraced path and
// the traced path are the same path.
func (t *evalTrace) step(s TraceStep) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, s)
}

// sealed closes the trace onto the verdict it explains. It is called exactly
// once, where the evaluation returns to its caller, and that is the far end of
// the invariant: from the first step until sealed, the trace is being written
// and is nobody's to read; after it, the trace is complete and its LAST step
// carries the verdict's own decision.
func (t *evalTrace) sealed(v Verdict) Verdict {
	if t == nil {
		return v
	}
	v.Trace = t.steps
	return v
}
