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
// the resource scopes (paths, sessions — the closed ResourceKind set) the
// decision applies within. A row with NO stated scope applies within the
// grant's own bound — the run's session scope the mint supplies — and a call
// naming a resource outside the row's scopes is refused, never silently
// re-scoped (ADR-0020 decision 6: scope expansion invalidates prior
// approval).
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
// an unparseable policy, and a scope must name a known kind with a non-empty
// id — or an absolute path, the both-ends-absolute contract the policy's
// lexical containment check assumes. A tool-kind scope is refused outright:
// a scope over a TOOL id is a rule over a tool name — the --no-tools mistake
// at the settings layer (ADR-0028 decision 4). The ledger's kind set keeps
// ResourceTool for the grant record's own vocabulary; a POLICY may not bind
// an effect to a named tool.
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
		if !validResourceKind(s.Kind) {
			return fmt.Errorf("effect row: scope %d: kind %q is not a resource kind", i, s.Kind)
		}
		if s.ID == "" {
			return fmt.Errorf("effect row: scope %d: empty resource id", i)
		}
		if s.Kind == ResourcePath && !isAbsolutePath(s.ID) {
			return fmt.Errorf("effect row: scope %d: path %q is not absolute", i, s.ID)
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
		ResourceDestination, ResourceTool:
		return true
	default:
		return false
	}
}

// EffectPolicy is the matrix: one row per effect class of the ADR-0020
// lattice. Zero rows decide ask (fail toward asking), so an unstated, empty
// or unparseable policy is a policy that asks — never one that permits.
type EffectPolicy struct {
	Observe           EffectRow `json:"observe"`
	MutateReversible  EffectRow `json:"mutate-reversible"`
	MutateDestructive EffectRow `json:"mutate-destructive"`
	PrivilegeChange   EffectRow `json:"privilege-change"`
	Disclose          EffectRow `json:"disclose"`
	CrossBoundary     EffectRow `json:"cross-boundary"`
	Delegate          EffectRow `json:"delegate"`
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

// WithRunScopes copies the policy with the run's base scopes added to EVERY
// row: a grant is minted per run, the run bound holds for every effect
// whatever scopes the operator stated. Rows keep their operator scopes; the
// union is additive, never a replacement.
func (p EffectPolicy) WithRunScopes(run []GrantScope) EffectPolicy {
	q := p
	q.Observe.Scopes = appendScopeSet(p.Observe.Scopes, run)
	q.MutateReversible.Scopes = appendScopeSet(p.MutateReversible.Scopes, run)
	q.MutateDestructive.Scopes = appendScopeSet(p.MutateDestructive.Scopes, run)
	q.PrivilegeChange.Scopes = appendScopeSet(p.PrivilegeChange.Scopes, run)
	q.Disclose.Scopes = appendScopeSet(p.Disclose.Scopes, run)
	q.CrossBoundary.Scopes = appendScopeSet(p.CrossBoundary.Scopes, run)
	q.Delegate.Scopes = appendScopeSet(p.Delegate.Scopes, run)
	return q
}

// AsGrant mints the grant a run executes under (ADR-0020 decision 5: the
// workspace mints the default grant from its policy — amended §7 makes that
// policy the matrix). runScopes are the run's own bound (the transport mints
// with the run's session). The minted grant's ROWS carry the effective scopes
// (operator scopes + run scopes), and its Effects are derived from the rows —
// the matrix is the ONE source of "what may this run do". A grant built any
// other way is authority nobody minted.
//
// Grant.Scopes is the derived union of every row's effective scopes, and it
// exists for ONE consumer only: the declaration filter's resource-KIND
// coverage. Enforcement never consults the union — a call is judged against
// the SELECTED EFFECT's row scopes, so an observe row scoped to /home refuses
// a call on /etc even when another row covers /etc. The split is deliberate:
// declaration is kind-shaped, enforcement is row-shaped.
func (p EffectPolicy) AsGrant(runScopes []GrantScope) Grant {
	effective := p.WithRunScopes(runScopes)
	g := Grant{Version: 1, Policy: effective}
	g.Effects = effective.PermittedEffects()
	for _, row := range effective.rows() {
		g.Scopes = appendScopeSet(g.Scopes, row.Scopes)
	}
	return g
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

// SessionOverrides is the per-session overlay: what a person answered "in
// this session" to, one decision per effect class. It is NOT a third matrix
// — it is produced by clicks rather than authored, so it carries no scopes
// and has no notion of an unstated row. An effect absent from the map is
// not an answer, and therefore never a permit.
type SessionOverrides map[Effect]Decision

// ResolvePolicy is the ONE place the resolution order is stated (ADR-0020 §7
// as amended): the session overlay over the workspace policy over the global
// default. The workspace, when one exists, REPLACES the global wholesale; the
// session overlay then applies per row on top of whichever won, changing that
// row's decision and nothing else — never its scopes, which the overlay has
// no way to express. Today there is no workspace grant source — nocx-mp2vd
// owns that seam — so callers pass nil and the global resolves.
//
// An override whose decision is outside the enum is ignored rather than
// applied: this function fails toward asking like every other layer, and an
// invalid value must never be able to widen a row.
func ResolvePolicy(global EffectPolicy, workspace *EffectPolicy, session SessionOverrides) EffectPolicy {
	out := global
	if workspace != nil {
		out = *workspace
	}
	for _, e := range []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
	} {
		d, ok := session[e]
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
	return p, nil
}
