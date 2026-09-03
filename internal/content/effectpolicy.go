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
	"time"
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
// grant's own bound — the run's session scope the mint supplies — and a call
// naming a resource outside the row's scopes is refused, never silently
// re-scoped (ADR-0020 decision 6: scope expansion invalidates prior approval).
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

// DecisionForInvocation resolves one validated command, and this comment is
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
// among overlapping matching rules the most restrictive wins. Last the
// RESOURCE layer, owned by EffectRow.Scopes together with GrantScope.Contains
// in resource_scope.go: the resources the parser named must lie inside the
// SELECTED row's scopes, into which WithRunScopes has already folded the run
// fence — so the fence binds here as part of the row rather than as a second
// list to consult.
//
// A rule is therefore an exception to the effect layer alone. A permit whose
// invocation names a resource outside its row falls back to asking a person:
// never to something more permissive, and never past a refusal.
func (p EffectPolicy) DecisionForInvocation(e Effect, inv Invocation) Decision {
	if !inv.Parsed {
		return DecisionAsk
	}
	base := p.DecisionFor(e)
	if base == DecisionRefuse || inv.Disqualified {
		return base
	}
	decision := base
	matched := false
	ruleDecision := DecisionPermit
	for _, rule := range p.Rules {
		if !rule.Matches(inv) {
			continue
		}
		matched = true
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
	if decision == DecisionPermit && !p.namedResourcesWithinRow(e, inv.Resources) {
		return DecisionAsk
	}
	return decision
}

// namedResourcesWithinRow is the resource layer of DecisionForInvocation:
// every resource the command named must fall inside the selected row's
// scopes. The row is the whole resource authority here because WithRunScopes
// folds the run fence into every row at mint, which is also why the kernel's
// own scope check reads the row and never the derived Grant.Scopes union.
//
// The bound is kind-wise, the same rule intersectScopeSet applies when a
// selector meets the fence: a row narrows only the resource kinds it names,
// and a kind no scope of the row names is not narrowed here. Anything else
// would make a session-fenced run refuse every path a command mentions, since
// a command's resources are inferred from a command line rather than declared
// by a tool — unlike the resolved resources of a declaration, whose kinds the
// grant's coverage already filtered, and which must therefore be contained
// outright.
//
// Like the kernel's check this is the advisory lexical approximation, not the
// enforcement: the capability resolves canonical identity (ADR-0020 §7), and
// a call this predicate lets through can still be refused by it.
func (p EffectPolicy) namedResourcesWithinRow(e Effect, report ResourceReport) bool {
	scopes := p.rowFor(e).Scopes
	if len(scopes) == 0 {
		return true
	}
	for _, resource := range report.Resources {
		child, ok := namedResourceScope(resource)
		if !ok {
			continue
		}
		bounded, inside := false, false
		for _, scope := range scopes {
			if scope.Kind != child.Kind {
				continue
			}
			bounded = true
			if scope.Contains(child) {
				inside = true
				break
			}
		}
		if bounded && !inside {
			return false
		}
	}
	return true
}

// namedResourceScope maps one parser-named resource to the canonical scope
// the policy compares. Only an absolute path has a canonical scope id today;
// Resource.Path also carries relative operands, URLs and ssh destinations,
// and none of those is a ResourcePath — a scope form for them is a change to
// the scope vocabulary, not something to guess at inside a decision.
func namedResourceScope(r Resource) (GrantScope, bool) {
	if !isAbsolutePath(r.Path) {
		return GrantScope{}, false
	}
	return GrantScope{Kind: ResourcePath, ID: r.Path}, true
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
//
// THE MINT IS ALSO WHERE THE DEADLINE COMES FROM (nocx-1z1r1). ADR-0020 §5
// calls the grant "a versioned, EXPIRING capability issued to one agent turn
// or one execution"; the version was stamped here and the expiry was not, so
// every recorded grant carried expires_at = 0 and nothing could compare it
// to a clock. There is exactly one place a run's authority begins, and it is
// this function, so this is where its end is stated: now + GrantLifetime.
// Enforcement is elsewhere and deliberately not a predicate before dispatch
// — agenttools wraps every capability constructor, so an expired grant
// yields no capability at all (ADR-0028 decision 4).
func (p EffectPolicy) AsGrant(runScopes []GrantScope) Grant {
	effective := p.WithRunScopes(runScopes)
	g := Grant{
		Version:   1,
		ExpiresAt: time.Now().Add(GrantLifetime).UnixMilli(),
		Policy:    effective,
	}
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
	if err := validateInvocationRules(p.Rules); err != nil {
		return EffectPolicy{}, fmt.Errorf("%w: %v", ErrPolicySyntax, err)
	}
	return p, nil
}
