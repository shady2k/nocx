package content

// The agent policy matrix (ADR-0020 §7, as amended 2026-08-16, accepted):
// one row per effect class of decision 6, each row carrying exactly one
// decision — permit | ask | refuse — and the resource scopes the decision
// applies within.
//
// The shape is a matrix, not a rule engine, on purpose: each effect appears
// exactly once, so evaluation needs no conflict resolver and a person sees
// what is permitted without simulating rules.
//
// The rule that survives any amount of flexibility is ADR-0028 decision 4:
// the grant is over resources and effects, NEVER over tool names. This type
// makes that impossible by construction — the row keys are the closed effect
// enum, and operator-supplied JSON is parsed strictly (DisallowUnknownFields),
// so a tool name expressed as a row key, or a tool-kind scope, is an
// unparseable policy, never a rule.
//
// The three presets of the original §7 (ask-every-time, ask-on-mutate,
// autonomous) are deliberately NOT constructors here: a preset IS a specific
// matrix — every row ask; observe permit and the rest ask; every row permit —
// and a person asking for one writes its rows. Production ships the matrix
// alone; the preset-as-matrix equivalence is asserted by the tests, which is
// what "remain expressible in the new form" means (no preset vocabulary rides
// the wire or the store).
//
// This file is EffectPolicy's home; policy.go in this package is the History
// retention policy (an unrelated, pre-existing type) — the name collision is
// deliberate naming, not one type.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Decision is one entry of the matrix: what a run may do for an effect class,
// in the row's scopes — permit the call, ask a person first, or refuse it.
type Decision string

const (
	DecisionPermit Decision = "permit"
	DecisionAsk    Decision = "ask"
	DecisionRefuse Decision = "refuse"
)

func (d Decision) valid() bool {
	switch d {
	case DecisionPermit, DecisionAsk, DecisionRefuse:
		return true
	default:
		return false
	}
}

// EffectRow is one row of the matrix: the decision for one effect class plus
// the resource scopes (including canonical content and workspace sub-scopes)
// the decision applies within. A row with NO stated scope applies within the
// grant's own bound — the run's session scope the mint supplies.
//
// A call naming a resource outside those scopes is never silently re-scoped
// (ADR-0020 decision 6: scope expansion invalidates prior approval), and it
// is not one outcome but two, because the two are different products and the
// layer that owns each is different:
//
//   - Outside this row's EDITABLE scope, which an operator wrote and can
//     widen, the policy ASKS — Verdict{Cause: OutOfScopeRowScope}. The
//     question is answerable: widening the scope makes the same call run.
//   - Outside an IMMUTABLE bound — the run fence the mint supplied, or the
//     narrowed capability the tool holds — the answer is REFUSE,
//     Verdict{Cause: OutOfScopeFence}. No answer a person can give makes the
//     call executable, so asking would promise something the layer below
//     refuses anyway. The fence is therefore checked FIRST.
//
// Neither of those is the enforcement. GrantScope.Contains (resource_scope.go,
// its doc at :64-72) is a policy-time predicate over recorded scope ids and is
// NEVER a filesystem authorization check: it does no provider
// canonicalization, so it cannot see a symlink escape. The capability owns
// that — internal/filesystem/scoped.go refuses out-of-scope reads on canonical
// identity, and it refuses them whether this matrix permitted, asked or
// refused. Two layers, both intact: the policy asks, the fence refuses.
type EffectRow struct {
	Decision Decision
	Scopes   []GrantScope
}

// MarshalJSON emits the EFFECTIVE decision — an unstated row decides ask — and
// a non-nil scopes array, so the wire always carries the canonical rows and
// never a null slice (nil-slice-as-null is a contract-bug class).
func (r EffectRow) MarshalJSON() ([]byte, error) {
	d := r.Decision
	if !d.valid() {
		d = DecisionAsk
	}
	scopes := r.Scopes
	if scopes == nil {
		scopes = []GrantScope{}
	}
	return json.Marshal(struct {
		Decision Decision     `json:"decision"`
		Scopes   []GrantScope `json:"scopes"`
	}{Decision: d, Scopes: scopes})
}

// UnmarshalJSON is strict: an unknown key on a row is an unparseable policy
// (the tool-name rejection by construction), a decision outside the enum is
// an unparseable policy, and a scope must name a known kind with a valid
// canonical id. ResourcePath retains its absolute-path rule, while content
// and workspace scopes are checked by ValidateGrantScope. A tool-kind scope
// is refused outright: a scope over a TOOL id is a rule over a tool name —
// the --no-tools mistake at the settings layer (ADR-0028 decision 4). The
// ledger's kind set keeps ResourceTool for the grant record's own vocabulary;
// a POLICY may not bind an effect to a named tool.
func (r *EffectRow) UnmarshalJSON(b []byte) error {
	var raw struct {
		Decision Decision     `json:"decision"`
		Scopes   []GrantScope `json:"scopes"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("effect row: %w", err)
	}
	if raw.Decision != "" && !raw.Decision.valid() {
		return fmt.Errorf("effect row: decision %q is not permit, ask or refuse", raw.Decision)
	}
	for i, s := range raw.Scopes {
		if s.Kind == ResourceTool {
			return fmt.Errorf("effect row: scope %d: a tool-kind scope names a tool, and the policy is over resources and effects, never tool names", i)
		}
		if err := ValidateGrantScope(s); err != nil {
			return fmt.Errorf("effect row: scope %d: %w", i, err)
		}
	}
	*r = EffectRow{Decision: raw.Decision, Scopes: raw.Scopes}
	return nil
}

// isAbsolutePath is the policy's path form: absolute, host-local. The wire's
// paths are the spelled arguments' lexical domain (the capability enforces
// the canonical identity); the containment check requires both ends absolute.
func isAbsolutePath(p string) bool {
	return len(p) > 0 && p[0] == '/'
}

// validResourceKind reports whether k is a member of the closed ResourceKind
// set the ledger persists (grant_scopes CHECK). A kind added to the ledger
// must be added here or a policy naming it stops parsing.
func validResourceKind(k ResourceKind) bool {
	switch k {
	case ResourceEnvironment, ResourceSession, ResourcePath, ResourceCredential,
		ResourceDestination, ResourceTool, ResourceContent, ResourceWorkspace:
		return true
	default:
		return false
	}
}

// EffectPolicy is the matrix: one row per effect class of the ADR-0020
// lattice. Zero rows decide ask (fail toward asking), so an unstated, empty
// or unparseable policy is a policy that asks — never one that permits.
// EffectPolicy is the matrix plus invocation-specific exceptions. The matrix
// remains the default; matching rules are evaluated against the same
// canonical invocation the assistant effect kernel parsed.
type EffectPolicy struct {
	Observe           EffectRow        `json:"observe"`
	MutateReversible  EffectRow        `json:"mutate-reversible"`
	MutateDestructive EffectRow        `json:"mutate-destructive"`
	PrivilegeChange   EffectRow        `json:"privilege-change"`
	Disclose          EffectRow        `json:"disclose"`
	CrossBoundary     EffectRow        `json:"cross-boundary"`
	Delegate          EffectRow        `json:"delegate"`
	Rules             []InvocationRule `json:"rules,omitempty"`
	floor             *Floor
	// runFence is mint metadata, not operator policy: it is the outer bound
	// of a run, while each row's Scopes remains that effect's selector.
	runFence []GrantScope
}

// rowFor returns the row of one effect; an effect outside the lattice has no
// row and therefore decides ask.
func (p EffectPolicy) rowFor(e Effect) EffectRow {
	switch e {
	case EffectObserve:
		return p.Observe
	case EffectMutateReversible:
		return p.MutateReversible
	case EffectMutateDestructive:
		return p.MutateDestructive
	case EffectPrivilegeChange:
		return p.PrivilegeChange
	case EffectDisclose:
		return p.Disclose
	case EffectCrossBoundary:
		return p.CrossBoundary
	case EffectDelegate:
		return p.Delegate
	default:
		return EffectRow{}
	}
}

// DecisionFor is the matrix's answer for one effect class: the row's
// decision, or ask when the row is unstated — the fail-toward-asking end
// that holds for a matrix nobody has configured.
func (p EffectPolicy) DecisionFor(e Effect) Decision {
	d := p.rowFor(e).Decision
	if !d.valid() {
		return DecisionAsk
	}
	return d
}

// OutOfScopeCause says WHY a resource fell outside, because the two answers
// are different products: a row scope a person can widen is a question, and a
// fence they cannot is a refusal. The empty value is "nothing fell outside" —
// a Verdict carrying a decision the resource layer did not reach.
type OutOfScopeCause string

const (
	// OutOfScopeRowScope: the resource is outside the SELECTED row's own
	// scopes, which an operator wrote and can widen. Editable, so the
	// answer is ask, and the surface may offer to widen the scope.
	OutOfScopeRowScope OutOfScopeCause = "row-scope"
	// OutOfScopeFence: the resource is outside an immutable bound — the run
	// fence the mint supplied, or the narrowed capability. Approval cannot
	// make the call executable, so the answer is refuse and no expansion is
	// offered.
	OutOfScopeFence OutOfScopeCause = "fence"
)

// Verdict is what one evaluation answers. Decision is what happens; Cause and
// Resource are what the surface needs in order to say why, and to offer the
// only answer that would change it.
//
// Resource is the resource that fell outside, in the scope form a row states
// it in — so a widening answer can be written from the verdict alone, without
// re-deriving the scope from the command line a second time.
//
// Trace is HOW the decision was reached, in the order it was taken, and it is
// present only when a caller asked for one (ExplainInvocation). It is nil for
// every caller that wants the outcome and not the reason. When it is present
// it is COMPLETE: it opens on the first thing the evaluator did and closes
// where the verdict returned, and its last step carries this same Decision —
// so a reader never has to wonder whether a step is missing off the end. The
// vocabulary and each step's meaning are in effecttrace.go.
type Verdict struct {
	Decision Decision
	Cause    OutOfScopeCause
	Resource GrantScope
	Trace    []TraceStep
}

// EvaluateInvocation resolves one validated command, and this comment is
// the ONE place the composition order of the layers it crosses is written
// down (ADR-0020 §7 as amended: the matrix decides, the narrowed capability
// enforces). The layers compose most-restrictive-wins, and no layer is an
// exception to the one after it.
//
// The floor runs BEFORE this function and is owned by Floor in floor.go —
// nothing here can answer it. The EFFECT layer comes first here, owned by
// this file's matrix row: an unparsed command asks, a refusing row is final and is
// returned before any rule is read, and a disqualified command receives the
// matrix answer and can never receive a rule exception. Then the SHAPE layer,
// owned by InvocationRule.Matches in rules.go: a rule answers only what shape
// this command line has, a matching permit is an exception to an ask row, and
// among overlapping matching rules the most restrictive wins. A rule whose
// selector covers more than one command line may only NARROW; the one form
// that may widen carries the effect it was granted under, and this loop is
// where that binding is checked against the effect the call classified as. Last the
// RESOURCE layer, owned by resourceVerdict below.
//
// A rule is therefore an exception to the effect layer alone. A permit whose
// invocation names a resource outside its row falls back to asking a person:
// never to something more permissive, and never past a refusal.
//
// The order above is also what the evaluator RECORDS, step by step, when a
// caller asks for a trace (ExplainInvocation) — see effecttrace.go. It is
// recorded here, as each step is taken, rather than derived from the result
// afterwards, because a derivation would be this order written down a second
// time and free to drift from it.
func (p EffectPolicy) EvaluateInvocation(e Effect, inv Invocation, fence []GrantScope) Verdict {
	return p.evaluateInvocation(e, inv, fence, nil)
}

// ExplainInvocation is EvaluateInvocation over the policy's own mint-supplied
// fence, WITH the trace: the same evaluation, answering "why" as well as
// "what". It is the paired opposite of DecisionForInvocation — same fence,
// same answer, the whole of the reasoning instead of none of it — and it
// exists so a surface can explain a decision without owning the order that
// produced it.
func (p EffectPolicy) ExplainInvocation(e Effect, inv Invocation) Verdict {
	tr := &evalTrace{}
	return tr.sealed(p.evaluateInvocation(e, inv, p.runFence, tr))
}

// evaluateInvocation is the ONE implementation of the composition order. tr is
// nil for a caller that wants no trace, and every step call below is then a
// nil check: the traced and untraced paths are the same path, so there is no
// second order to keep in step with this one.
func (p EffectPolicy) evaluateInvocation(e Effect, inv Invocation, fence []GrantScope, tr *evalTrace) Verdict {
	if !inv.Parsed {
		tr.step(TraceStep{
			Kind: TraceUnparsed, Decision: DecisionAsk,
			Detail: "the command could not be read, so no row and no rule can speak about it",
		})
		return Verdict{Decision: DecisionAsk}
	}
	base := p.DecisionFor(e)
	tr.step(TraceStep{Kind: TraceEffectRow, Effect: e, Decision: base})
	if base == DecisionRefuse {
		tr.step(TraceStep{
			Kind: TraceRowRefuses, Effect: e, Decision: base,
			Detail: "the row refuses, and a rule is an exception to the effect layer alone, so no rule was read",
		})
		return Verdict{Decision: base}
	}
	if inv.Disqualified {
		tr.step(TraceStep{
			Kind: TraceDisqualified, Effect: e, Decision: base,
			Detail: "the command uses a shell feature whose effect cannot be determined, so it takes the row's answer and no rule was read",
		})
		return Verdict{Decision: base}
	}
	decision := base
	matched := false
	ruleDecision := DecisionPermit
	for _, rule := range p.Rules {
		if !rule.Matches(inv) {
			continue
		}
		if rule.needsConfirmation() {
			// The SECOND of this loop's two guards, and the other one
			// is below. This rule's selector is loose, so what it
			// covers is whatever the classifier makes of a command
			// line nobody was shown — and it was saved while the
			// classifier read commands differently. It was therefore
			// agreed to on an account of the command that no longer
			// holds, and it stays inert until a person reads what it
			// now means and says so (RulesNeedingConfirmation,
			// ConfirmRule). An EXACT rule is never skipped here: it
			// names the literal command line the person was shown,
			// and that does not move when the classifier learns to
			// see more. Nor is a REFUSAL: only a permit is a claim a
			// later reading can falsify, and inerting a refusal would
			// drop it through to a row that may permit.
			tr.step(TraceStep{
				Kind: TraceRuleStale, RuleID: rule.ID, Decision: rule.Decision,
				Detail: fmt.Sprintf(
					"agreed to under reading %d of commands; commands are read as %d now, so it waits for a person",
					rule.EvaluatorVersion, EvaluatorVersion),
			})
			continue
		}
		if rule.Decision == DecisionPermit && rule.GrantedUnder != "" && rule.GrantedUnder != e {
			// The permit was granted while this command did something
			// milder. It does not reach this call. This is the whole
			// guard, and it lives here rather than in Matches because
			// only this layer is told what the call classified as.
			tr.step(TraceStep{
				Kind: TraceRuleOtherEffect, RuleID: rule.ID,
				Effect: rule.GrantedUnder, Decision: rule.Decision,
				Detail: "granted while the command did something milder, so it does not reach this call",
			})
			continue
		}
		matched = true
		tr.step(TraceStep{Kind: TraceRuleMatched, RuleID: rule.ID, Decision: rule.Decision})
		if restrictiveRank(rule.Decision) > restrictiveRank(ruleDecision) {
			ruleDecision = rule.Decision
		}
	}
	if matched {
		switch {
		case base == DecisionAsk && ruleDecision == DecisionPermit:
			decision = DecisionPermit
		case restrictiveRank(ruleDecision) > restrictiveRank(base):
			decision = ruleDecision
		}
	}
	return p.resourceVerdict(e, decision, namedScopes(inv.Resources), fence, boundKindWise, tr)
}

// DecisionForInvocation is EvaluateInvocation's decision alone, over the
// policy's own mint-supplied fence. It is the retained shape for every caller
// that wants the outcome and not the reason, and it builds no trace: the hot
// path does not pay for an explanation nobody asked for. ExplainInvocation is
// the same question with the reasoning attached.
func (p EffectPolicy) DecisionForInvocation(e Effect, inv Invocation) Decision {
	return p.EvaluateInvocation(e, inv, p.runFence).Decision
}

// EvaluateResources is the RESOURCE layer alone, for a call whose resources a
// DECLARATION resolved rather than a command parser inferred. It is the
// declared half of the same question EvaluateInvocation asks, answered by the
// same function underneath, so the two paths cannot drift into two answers
// again (design §5.2 — one evaluator, one typed cause).
//
// decision is the effect layer's answer for this call, already computed by the
// caller; the resource layer can only make it more restrictive.
//
// The bound differs from a command's, and the difference is deliberate: a
// declaration's resources are RESOLVED and their kinds were already filtered
// by the grant's coverage, so each one must be contained OUTRIGHT — a row that
// names no scope of that kind contains nothing, and the call is out of scope.
// A command's resources are inferred from a command line instead, so they are
// bounded kind-wise (see resourceVerdict).
func (p EffectPolicy) EvaluateResources(e Effect, decision Decision, resources, fence []GrantScope) Verdict {
	return p.resourceVerdict(e, decision, resources, fence, boundOutright, nil)
}

// scopeBound is how a resource set meets a scope set.
type scopeBound int

const (
	// boundKindWise: a scope set narrows only the resource kinds it names,
	// and a kind no scope names is not narrowed. An empty scope set bounds
	// nothing. This is the rule intersectScopeSet applies when a selector
	// meets the fence, and it is what a COMMAND's inferred resources get:
	// anything else would make a session-fenced run refuse every path a
	// command mentions.
	boundKindWise scopeBound = iota
	// boundOutright: every resource must lie inside some scope of the set,
	// and an empty set therefore contains nothing. This is what a
	// DECLARATION's resolved resources get.
	boundOutright
)

// resourceVerdict is the resource layer of both paths, and the one place the
// two causes are decided.
//
// The FENCE IS CHECKED FIRST, and that order is the whole point: the row
// scopes have already had the fence folded into them at mint
// (WithRunScopes), so a resource outside the fence is also outside the row.
// Checking the row first would report every immutable bound as an editable
// one and offer a question whose only useful answer does not exist.
//
// Both checks run for any decision that is not already a refusal, not only
// for a permit. A row-scope miss under an ask row leaves the decision at ask
// and adds the cause the surface needs to offer the widening; a fence miss
// under an ask row turns it into the refusal it always was at the layer
// below, rather than a question answered by "Approve" and then refused.
//
// Like the kernel's own check this is the advisory lexical approximation, not
// the enforcement: the capability resolves canonical identity (ADR-0020 §7),
// and a call this predicate lets through can still be refused by it.
func (p EffectPolicy) resourceVerdict(e Effect, decision Decision, resources, fence []GrantScope, bound scopeBound, tr *evalTrace) Verdict {
	if decision == DecisionRefuse {
		tr.step(TraceStep{
			Kind: TraceResourceNotReached, Effect: e, Decision: decision,
			Detail: "the decision was already a refusal, and the resource layer can only narrow, so no resource was compared",
		})
		return Verdict{Decision: decision}
	}
	if len(fence) > 0 {
		if outside, ok := firstOutside(resources, fence, bound); ok {
			tr.step(TraceStep{
				Kind: TraceResourceOutsideFence, Effect: e, Decision: DecisionRefuse,
				Detail: "outside the run's immutable bound, which no answer widens",
			})
			return Verdict{Decision: DecisionRefuse, Cause: OutOfScopeFence, Resource: outside}
		}
	}
	scopes := p.rowFor(e).Scopes
	if bound == boundOutright && len(scopes) == 0 && len(resources) > 0 {
		// NO scope at all is not a narrow selector somebody could widen: it
		// is authority nobody minted. A grant whose matrix field was never
		// set arrives here, and the only honest answer is the immutable one
		// — there is no bound to expand, so the question would have no
		// answer. Under boundKindWise the same emptiness means the opposite
		// (see the constant's doc), which is why this is asked here and not
		// inside firstOutside.
		tr.step(TraceStep{
			Kind: TraceResourceOutsideFence, Effect: e, Decision: DecisionRefuse,
			Detail: "the row states no scope at all, so there is no bound to widen",
		})
		return Verdict{Decision: DecisionRefuse, Cause: OutOfScopeFence, Resource: resources[0]}
	}
	if outside, ok := firstOutside(resources, scopes, bound); ok {
		tr.step(TraceStep{
			Kind: TraceResourceOutsideRowScope, Effect: e, Decision: DecisionAsk,
			Detail: "outside the row's own scopes, which an operator wrote and can widen",
		})
		return Verdict{Decision: DecisionAsk, Cause: OutOfScopeRowScope, Resource: outside}
	}
	tr.step(TraceStep{Kind: TraceResourceInside, Effect: e, Decision: decision})
	return Verdict{Decision: decision}
}

// firstOutside returns the first resource not contained by scopes, under the
// stated bound. First, not all of them: the verdict names ONE resource,
// because the answer it offers is about that one and a person cannot answer a
// list.
func firstOutside(resources, scopes []GrantScope, bound scopeBound) (GrantScope, bool) {
	if bound == boundKindWise && len(scopes) == 0 {
		return GrantScope{}, false
	}
	for _, resource := range resources {
		bounded := bound == boundOutright
		inside := false
		for _, scope := range scopes {
			if bound == boundKindWise {
				if scope.Kind != resource.Kind {
					continue
				}
				bounded = true
			}
			// The scope is asked WHOLE: rebuilding it from kind and id
			// drops a destination's subdomain marker (design §5.4), and a
			// row that grants a host with its subdomains would then refuse
			// one of them.
			if scope.Contains(resource) {
				inside = true
				break
			}
		}
		if bounded && !inside {
			return resource, true
		}
	}
	return GrantScope{}, false
}

// namedScopes is the command path's resource input: every resource the parser
// named, in the scope form a row states it in. A resource with no scope form
// is dropped rather than compared, because comparing an operand the parser
// could not resolve against a real scope could only ever declare it inside
// one (see scopeKindForVerb on ResourceUnknown).
func namedScopes(report ResourceReport) []GrantScope {
	out := make([]GrantScope, 0, len(report.Resources))
	for _, resource := range report.Resources {
		child, ok := namedResourceScope(resource)
		if !ok {
			continue
		}
		out = append(out, child)
	}
	return out
}

// scopeKindForVerb is the mapping from what a command DOES to a resource to
// the scope kind a row STATES that resource in, and it is exhaustive over
// ResourceVerb by construction: every verb is named, none falls to a default.
// A verb with no scope kind is a resource a row can never bound, which is
// exactly how a destination scope came to govern fetch.url and not curl
// (nocx-c88xr). A verb added to resources.go and not named here reaches the
// default arm, and the default arm is not a quiet fallback: it is what
// TestEveryResourceVerbHasAScopeKind fails on, so the gap is reported rather
// than returned as false in silence.
//
// ResourceUnknown is decided explicitly and deliberately has NO scope kind.
// It is the verb of an UnresolvedResource and never of a resolved Resource
// (internal/assistant/cmdeffect.go emits it on the Unresolved slice alone),
// so it cannot reach namedScopes, which walks Resources. Giving it a kind
// would be worse than useless: the parser is saying it could not
// determine what the operand is, and comparing that undetermined string
// against a real scope could only ever declare it INSIDE one. Uncertainty is
// answered a row earlier instead — ResourceReport.Effect sends any report
// carrying an unresolved entry to WorstEffect(declared).
func scopeKindForVerb(v ResourceVerb) (ResourceKind, bool) {
	switch v {
	case ResourceNetwork:
		// A URL, an ssh destination or a cluster: a place on the network,
		// which is what a destination scope names.
		return ResourceDestination, true
	case ResourceRead, ResourceWrite, ResourceDelete, ResourceExecute, ResourceSource:
		// Everything a command reads, changes, runs or sources into the
		// current shell is a path, and keeps path semantics.
		return ResourcePath, true
	case ResourceUnknown:
		return "", false
	default:
		// A verb this table does not name. Fail toward asking, and toward
		// the test above rather than toward a policy nobody can bound.
		return "", false
	}
}

// namedResourceScope maps one parser-named resource to the canonical scope
// the policy compares: the kind from the verb, the id from the operand the
// parser recorded.
//
// The id is not validated here beyond the path form, because the two kinds
// spell their ids differently and only one of them can be checked cheaply. A
// ResourcePath must be absolute, since Resource.Path also carries relative
// operands and a relative operand has no canonical scope id at all. A
// ResourceDestination is left to destinationContains, which parses it as a
// whole URL — the one predicate both this path and the dialler reach, so an
// operand it cannot parse (an ssh destination, "kubectl cluster") is inside
// no destination scope and the row therefore asks, which is the direction
// this package fails in.
func namedResourceScope(r Resource) (GrantScope, bool) {
	if r.Path == "" {
		return GrantScope{}, false
	}
	kind, ok := scopeKindForVerb(r.Verb)
	if !ok {
		return GrantScope{}, false
	}
	if kind == ResourcePath && !isAbsolutePath(r.Path) {
		return GrantScope{}, false
	}
	return GrantScope{Kind: kind, ID: r.Path}, true
}

func restrictiveRank(d Decision) int {
	switch d {
	case DecisionPermit:
		return 0
	case DecisionAsk:
		return 1
	case DecisionRefuse:
		return 2
	default:
		return 1
	}
}

// WithRule returns a copy with one invocation rule appended. Invalid rules
// are ignored so an operator-supplied invalid decision cannot widen policy.
func (p EffectPolicy) WithRule(rule InvocationRule) EffectPolicy {
	if err := validateInvocationRules([]InvocationRule{rule}); err != nil {
		return p
	}
	p.Rules = append(append([]InvocationRule(nil), p.Rules...), rule)
	return p
}

// RulesNeedingConfirmation returns every rule that is inert because it was
// saved under a different reading of commands, in document order. Skipping is
// not silent: a rule that no longer applies and says nothing about it is a
// permission that quietly stopped working, which is how a person learns to
// distrust the page rather than the rule.
func (p EffectPolicy) RulesNeedingConfirmation() []InvocationRule {
	var stale []InvocationRule
	for _, rule := range p.Rules {
		if rule.needsConfirmation() {
			stale = append(stale, rule)
		}
	}
	return stale
}

// ConfirmRule rewrites one rule's EvaluatorVersion to the current one and
// NOTHING else, returning the new policy. An unknown id returns p unchanged
// and false.
//
// It is deliberately NOT a re-grant. It says "I have read what this now means
// and I still mean it": the selector, the decision and the effect the permit
// was granted under are untouched, and widening any of them is a different
// gesture with a different question attached to it.
func (p EffectPolicy) ConfirmRule(id string) (EffectPolicy, bool) {
	if id == "" {
		return p, false
	}
	for i, rule := range p.Rules {
		if rule.ID != id {
			continue
		}
		rules := append([]InvocationRule(nil), p.Rules...)
		rules[i].EvaluatorVersion = EvaluatorVersion
		p.Rules = rules
		return p, true
	}
	return p, false
}

// RowScopes returns the effective scopes of ONE effect's row — the resource
// bound the row's decision applies within. The policy consumer (the permit/
// ask/refuse decision) checks the resource a call names against THIS set and
// never against the derived Grant.Scopes union, which exists for declaration
// kind-coverage only (see AsGrant).
func (p EffectPolicy) RowScopes(e Effect) []GrantScope {
	return p.rowFor(e).Scopes
}

// rows returns the seven rows in the canonical lattice order — the order the
// ledger's CHECK constraints and the contracts pin.
func (p EffectPolicy) rows() []EffectRow {
	return []EffectRow{
		p.Observe, p.MutateReversible, p.MutateDestructive, p.PrivilegeChange,
		p.Disclose, p.CrossBoundary, p.Delegate,
	}
}

// PermittedEffects derives the grant's effect list from the matrix: every
// effect whose row does NOT refuse. A refusal removes the effect from the run
// before any tool is proposed (the strongest refusal is the one never
// proposed); an ask row keeps the effect declared, because asking is a
// decision a person can answer, not an absence.
func (p EffectPolicy) PermittedEffects() []Effect {
	var out []Effect
	for _, e := range []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
	} {
		if p.DecisionFor(e) != DecisionRefuse {
			out = append(out, e)
		}
	}
	return out
}

// WithRunScopes copies the policy with the run's base scopes kept as a
// separate fence. The fence bounds every row for the run's lifetime; each
// row's scopes are materialized as the intersection of its operator selector
// and that fence, because the kernel consumes row scopes.
func (p EffectPolicy) WithRunScopes(run []GrantScope) EffectPolicy {
	q := p
	q.runFence = append([]GrantScope(nil), run...)
	q.Observe = narrowRow(p.Observe, run)
	q.MutateReversible = narrowRow(p.MutateReversible, run)
	q.MutateDestructive = narrowRow(p.MutateDestructive, run)
	q.PrivilegeChange = narrowRow(p.PrivilegeChange, run)
	q.Disclose = narrowRow(p.Disclose, run)
	q.CrossBoundary = narrowRow(p.CrossBoundary, run)
	q.Delegate = narrowRow(p.Delegate, run)
	return q
}

// RunFence returns the mint-supplied outer bound. It is distinct from every
// row's effective selector: an empty operator selector is expanded to this
// fence, while a stated selector is narrowed by it.
func (p EffectPolicy) RunFence() []GrantScope {
	return append([]GrantScope(nil), p.runFence...)
}

// AsGrant mints a grant with two distinct scope concepts. The run fence is
// the outer bound supplied by the mint; each policy row is the selector for
// that effect, materialized to the fence for the kernel. A selector narrows
// within the fence — it is never unioned with the fence — while an empty
// selector means the row applies to the whole fence. Grant.Scopes is the
// derived, kind-shaped declaration coverage consumed by Registry.ForGrant;
// enforcement is row-shaped and never consults that union. With a fence,
// declaration coverage intentionally includes its resource kinds even when a
// row selector does not, so an offered call can still be refused by the row;
// nocx-tyhel is where offer-time explanation learns to say so.
func (p EffectPolicy) AsGrant(runScopes []GrantScope) Grant {
	effective := p.WithRunScopes(runScopes)
	g := Grant{Version: 1, Policy: effective}
	g.Effects = effective.PermittedEffects()
	if len(effective.runFence) > 0 {
		// Grant scopes are the run fence's declaration coverage. Row
		// selectors remain the per-effect enforcement scopes.
		g.Scopes = appendScopeSet(g.Scopes, effective.runFence)
	} else {
		// Grants without a run fence retain the direct-policy behavior:
		// declaration coverage is derived from the row selectors.
		for _, row := range effective.rows() {
			g.Scopes = appendScopeSet(g.Scopes, row.Scopes)
		}
	}
	return g
}

func narrowRow(row EffectRow, fence []GrantScope) EffectRow {
	var empty bool
	row.Scopes, empty = intersectScopeSet(row.Scopes, fence)
	if empty {
		// A stated selector with no intersection cannot be represented by
		// empty Scopes: empty means unbounded. Refuse the whole effect at
		// mint time so it is never offered as an executable capability.
		row.Decision = DecisionRefuse
	}
	return row
}

func intersectScopeSet(selectors, fence []GrantScope) ([]GrantScope, bool) {
	if len(fence) == 0 {
		return append([]GrantScope(nil), selectors...), false
	}
	if len(selectors) == 0 {
		return append([]GrantScope(nil), fence...), false
	}
	var out []GrantScope
	namedKinds := make(map[ResourceKind]bool, len(selectors))
	fenceKinds := make(map[ResourceKind]bool, len(fence))
	overlappingKinds := make(map[ResourceKind]bool, len(fence))
	for _, selector := range selectors {
		namedKinds[selector.Kind] = true
	}
	// A selector narrows only the fence kinds it names. For a named kind,
	// only overlapping IDs survive; a disjoint selector therefore grants no
	// coverage for that kind. Fence kinds the selector does not name remain
	// bounded by the run fence.
	for _, bound := range fence {
		fenceKinds[bound.Kind] = true
		kindMatched := false
		for _, selector := range selectors {
			if selector.Kind != bound.Kind {
				continue
			}
			kindMatched = true
			switch {
			case selector.Contains(bound):
				out = appendScopeSet(out, []GrantScope{bound})
				overlappingKinds[bound.Kind] = true
			case bound.Contains(selector):
				out = appendScopeSet(out, []GrantScope{selector})
				overlappingKinds[bound.Kind] = true
			}
		}
		if !kindMatched {
			out = appendScopeSet(out, []GrantScope{bound})
		}
	}
	for kind := range namedKinds {
		if fenceKinds[kind] && !overlappingKinds[kind] {
			return out, true
		}
	}
	return out, false
}

// appendScopeSet appends the members of b not already present (kind+id) in a,
// without mutating a. Order of a preserved; added members follow in b's
// order.
func appendScopeSet(a, b []GrantScope) []GrantScope {
	out := make([]GrantScope, 0, len(a)+len(b))
	out = append(out, a...)
	for _, s := range b {
		found := false
		for _, have := range out {
			if have.Kind == s.Kind && have.ID == s.ID {
				found = true
				break
			}
		}
		if !found {
			out = append(out, s)
		}
	}
	return out
}

// SessionOverrides is the per-session overlay: effect decisions and
// invocation rules produced by clicks. It expires with the session and is
// resolved alongside global rules; it is never persisted as a global rule.
type SessionOverrides struct {
	Decisions map[Effect]Decision
	Rules     []InvocationRule
}

// ResolvePolicy is the ONE place the resolution order is stated (ADR-0020
// §7): the session overlay over the workspace policy over the global default.
// The workspace, when one exists, replaces the global matrix wholesale.
// Matching rules from the selected matrix and the session overlay are retained
// together so DecisionForInvocation can apply most-restrictive-wins.
func ResolvePolicy(global EffectPolicy, workspace *EffectPolicy, session SessionOverrides) EffectPolicy {
	out := global
	floor := global.floor
	if workspace != nil {
		out = *workspace
	}
	// The floor is composition-root authority, not matrix state; preserve it
	// even when a workspace replaces the global policy.
	out.floor = floor
	out.Rules = append([]InvocationRule(nil), out.Rules...)
	out.Rules = append(out.Rules, session.Rules...)
	for _, e := range []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
	} {
		d, ok := session.Decisions[e]
		if !ok || !d.valid() {
			continue
		}
		row := out.rowFor(e)
		row.Decision = d
		out.setRow(e, row)
	}
	return out
}

// SetRowDecision returns a copy of the matrix with ONE row's decision
// replaced, keeping that row's scopes. It is how a standing answer from the
// approval prompt is applied (the transport calls it inside the policy
// store's write), and the only exported mutator — a caller reaching into the
// struct fields would be a second place that knows the lattice's shape.
//
// A decision outside the enum is ignored: every layer here fails toward
// asking, and an invalid value must never widen a row.
func (p EffectPolicy) SetRowDecision(e Effect, d Decision) EffectPolicy {
	if !d.valid() {
		return p
	}
	row := p.rowFor(e)
	row.Decision = d
	out := p
	out.setRow(e, row)
	return out
}

// setRow writes one row by effect — the inverse of rowFor, and the only
// mutator on the matrix. Kept beside it so the two switches cannot drift.
func (p *EffectPolicy) setRow(e Effect, row EffectRow) {
	switch e {
	case EffectObserve:
		p.Observe = row
	case EffectMutateReversible:
		p.MutateReversible = row
	case EffectMutateDestructive:
		p.MutateDestructive = row
	case EffectPrivilegeChange:
		p.PrivilegeChange = row
	case EffectDisclose:
		p.Disclose = row
	case EffectCrossBoundary:
		p.CrossBoundary = row
	case EffectDelegate:
		p.Delegate = row
	}
}

// ErrPolicySyntax marks a policy document that cannot be parsed — the
// store's fail-toward-asking signal.
var ErrPolicySyntax = errors.New("agent policy: unparseable")

// ParseEffectPolicy decodes an operator-supplied policy (the settings RPC,
// the persisted store) STRICTLY: unknown keys — a tool name where an effect
// goes — a decision outside the enum, an unknown scope kind, a tool-kind
// scope, a non-absolute path scope or an empty id are all unparseable, and
// the caller treats an unparseable policy as ask-everything rather than
// guessing. This is the ONE gate between a person's expression and the
// policy that runs.
func ParseEffectPolicy(b []byte) (EffectPolicy, error) {
	var p EffectPolicy
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: %v", ErrPolicySyntax, err)
	}
	// A document's rules get their defaults BEFORE the gate sees them: the
	// minted id is what the duplicate check is over, and an unstated source
	// is written rather than absent.
	normalizeInvocationRules(p.Rules)
	if err := validateInvocationRules(p.Rules); err != nil {
		return EffectPolicy{}, fmt.Errorf("%w: %v", ErrPolicySyntax, err)
	}
	return p, nil
}
