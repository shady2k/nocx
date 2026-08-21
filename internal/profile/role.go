package profile

import (
	"errors"
	"fmt"
)

// ModelRole is a NAMED model assignment — one (endpoint, model) pair a
// feature asks for by name (bead nocx-e6kn2). A feature never names a model
// id: the assistant asks for the answering role, the classifier bead
// (nocx-kpy23) will ask for the classifier role, and the assignment changes
// in ONE place — the roles surface — while the consumers stay ignorant.
//
// The set is CLOSED and defined by the product, never by the user: a role
// is requested by a feature, and a feature only knows the names compiled
// into it, so a user-invented role name would have nobody to ask for it.
// Adding a third role is one const here plus the feature that consumes it.
type ModelRole string

const (
	// RoleAnswering is the model the assistant speaks with — the role the
	// ask transaction resolves (design §5, the ask run's "endpoint and
	// model as they were at the time").
	RoleAnswering ModelRole = "answering"
	// RoleClassifier is the SECOND model that will judge proposed tool
	// calls (its own bead, nocx-kpy23). It exists here because the
	// classifier cannot even be stated without a role to say it with; it
	// has no consumer in this build, and its row is still assignable —
	// visible now, consumed by the bead that follows.
	RoleClassifier ModelRole = "classifier"
)

// AllRoles is the closed role set, in the order the roles surface renders
// them (answering first: it is the role a normal ask uses).
func AllRoles() []ModelRole {
	return []ModelRole{RoleAnswering, RoleClassifier}
}

// ValidModelRole reports whether v is a value this build recognises. An
// unrecognised value is refused at write time — the set is closed, so a
// stored assignment must never name a role nobody can ask for.
func ValidModelRole(v ModelRole) bool {
	for _, r := range AllRoles() {
		if v == r {
			return true
		}
	}
	return false
}

// ParseModelRole maps a wire string to a ModelRole, refusing unknown names.
func ParseModelRole(s string) (ModelRole, error) {
	r := ModelRole(s)
	if !ValidModelRole(r) {
		return "", ErrRoleUnknown
	}
	return r, nil
}

// RoleAssignment is the stored mapping of one role to one (endpoint, model)
// pair: the endpoint's id and the model id that endpoint offers. The model
// is referenced by the id the API understands, never by the picker alias —
// the assignment is what runs, the alias is only for display.
//
// An assignment may reference an endpoint or model that no longer exists:
// DeleteEndpoint and UpdateEndpoint do not chase assignments (the delete's
// reference sweep is about SECRETS, and a role names an endpoint, not a
// credential). A DefaultModel is the exception and is cleared by the delete
// — it is one global convenience with nothing to reassign, while an
// assignment is a statement about one role that the person made and is
// entitled to be told about. The dangle is deliberate — a deleted endpoint or removed
// model leaves the role RESOLUTION a refusal that names what disappeared,
// never a silent hop to a neighbour — and the roles surface shows the row
// as no longer assignable. Resolution is the truth-teller; see ResolveRole.
type RoleAssignment struct {
	Role       ModelRole `json:"role"`
	EndpointID string    `json:"endpointId"`
	Model      string    `json:"model"`
}

// ValidateRoleAssignment checks the SHAPE of an assignment before it is
// stored: a known role, and the (endpoint, model) pair EITHER both present
// or both empty — a role is assigned to exactly one (endpoint, model) pair,
// or it is unassigned, and a half-assignment has no meaning. The empty pair
// is the CLEAR write: it removes the role's assignment, returning the role
// to the visible "no model assigned" failure state.
//
// Whether the endpoint and model still exist is a question NOT checked here
// — it is answered at resolution time, once, by ResolveRole, so a deletion
// or an endpoint update that removes a model cannot race a write into a
// validated-but-stale assignment and the dangle stays a visible refusal
// instead of a write-time band-aid.
func ValidateRoleAssignment(a RoleAssignment) error {
	if !ValidModelRole(a.Role) {
		return fmt.Errorf("%q: %w", a.Role, ErrRoleUnknown)
	}
	if (a.EndpointID == "") != (a.Model == "") {
		return errors.New("role assignment: endpoint id and model must be provided together (or both empty to clear the role)")
	}
	return nil
}

// DefaultModel is the ONE (endpoint, model) pair a person names once, used
// by every role that has no assignment of its own (bead nocx-rikz5). It is
// not a fallback the product invents — that is what nocx-e6kn2 forbids,
// because then nobody can say which model answered. It is a choice the
// person made, reused; the distinction is the whole reason this is a stored
// value rather than "the first model of the first endpoint".
//
// The zero value means "no default", and it is the state a fresh profile is
// in. Both fields set or neither: a half-set default names nothing.
type DefaultModel struct {
	EndpointID string `json:"endpointId"`
	Model      string `json:"model"`
}

// IsSet reports whether a default has been chosen. Both fields are required
// together, so a half-set value is not "partly set" — it is not set.
func (d DefaultModel) IsSet() bool { return d.EndpointID != "" && d.Model != "" }

// ValidateDefaultModel checks the SHAPE before storing: both present, or
// both empty. The empty pair is the CLEAR write — it removes the default and
// returns every unassigned role to the visible failure state. Whether the
// endpoint and model still exist is deliberately NOT this check's question,
// for the same reason ValidateRoleAssignment does not ask it: resolution
// answers it, once, so a deletion cannot race a write into a
// validated-but-stale default.
func ValidateDefaultModel(d DefaultModel) error {
	if (d.EndpointID == "") != (d.Model == "") {
		return errors.New("default model: endpoint id and model must be provided together (or both empty to clear the default)")
	}
	return nil
}

// The resolution refusals. Each names WHAT is missing so the ask surface can
// repeat it to a person; a role is never silently re-pointed at something
// else (bead acceptance: a model or endpoint that disappears leaves the role
// unresolvable and says so).
var (
	// ErrRoleUnknown is the refusal for a role name outside the closed set.
	ErrRoleUnknown = errors.New("unknown model role")
	// ErrRoleUnassigned is the refusal when no assignment exists for the
	// role: the visible failure an unassigned role must be, never a silent
	// fallback to some other model.
	ErrRoleUnassigned = errors.New("no model assigned")
	// ErrRoleEndpointGone is the refusal when the assigned endpoint was
	// deleted. Wrapped with the endpoint id so the message names it.
	ErrRoleEndpointGone = errors.New("endpoint no longer exists")
	// ErrRoleModelGone is the refusal when the assigned model is no longer
	// offered by the assigned endpoint (an update replaced the model list).
	// Wrapped with the endpoint name so the message names it.
	ErrRoleModelGone = errors.New("model is no longer offered by the endpoint")
)

// ResolveRole is THE ONE role resolver in the product (bead nocx-e6kn2:
// "a role resolves in one place"). It maps a role to its assigned
// (endpoint, model) pair — the role's own assignment, or failing that the
// person's default (bead nocx-rikz5) — or refuses with a reason naming
// exactly what is missing. The default is a new INPUT, never a second
// resolution path: it is read once by the caller and handed in, so there is
// still exactly one place where a role becomes an (endpoint, model) pair. Every consumer — the ask transaction, the classifier, the roles
// surface's preview — goes through here, so a reassignment is picked up by
// every feature at once and no feature can grow a private second answer
// that disagrees with the first the day the role is reassigned.
//
// The refusals, and why each is a refusal rather than a repair:
//
//   - unknown role      → a feature asked for a name the build does not
//     know; that is a bug in the caller, and it is told so.
//   - unassigned        -> the product's rule: a role with no model of its
//     own AND no default is a VISIBLE failure where the feature is used.
//     Falling back to some other model would make it unknowable which
//     model answered; the default is not that fallback, because the person
//     authored it.
//   - endpoint gone     -> the assigned endpoint was deleted. Resolving to
//     a neighbour endpoint would quietly change the provider behind the
//     person's back.
//   - model gone        -> the assigned model was removed from the
//     endpoint. Resolving to the next model on the list is the same lie.
func ResolveRole(role ModelRole, assignments []RoleAssignment, def DefaultModel, endpoints []Endpoint) (Endpoint, string, error) {
	if !ValidModelRole(role) {
		return Endpoint{}, "", fmt.Errorf("role %q: %w", role, ErrRoleUnknown)
	}
	// The role's own assignment first: an override is only an override if it
	// outranks the thing it overrides.
	var a *RoleAssignment
	for i := range assignments {
		if assignments[i].Role == role {
			a = &assignments[i]
			break
		}
	}
	// Then the default, as an assignment this role did not write. Below this
	// point the two are indistinguishable ON PURPOSE: an endpoint that is
	// gone is gone whichever named it, and a default is never repaired into a
	// neighbour any more than an assignment is.
	if a == nil && def.IsSet() {
		a = &RoleAssignment{Role: role, EndpointID: def.EndpointID, Model: def.Model}
	}
	if a == nil {
		return Endpoint{}, "", fmt.Errorf("role %q: %w", role, ErrRoleUnassigned)
	}
	var ep *Endpoint
	for i := range endpoints {
		if endpoints[i].ID == a.EndpointID {
			ep = &endpoints[i]
			break
		}
	}
	if ep == nil {
		return Endpoint{}, "", fmt.Errorf("role %q: the assigned endpoint %q %w", role, a.EndpointID, ErrRoleEndpointGone)
	}
	for _, m := range ep.Models {
		if m.Name == a.Model {
			return *ep, a.Model, nil
		}
	}
	return Endpoint{}, "", fmt.Errorf("role %q: the assigned model %q %w (endpoint %q)", role, a.Model, ErrRoleModelGone, ep.Name)
}

// RoleRepository is the persistence interface for role assignments. Roles
// ride the same JSON document as endpoints (ADR-0030), so the JSON store
// satisfies it; the interface exists so the capability seam can depend on
// the abstraction, not the file.
type RoleRepository interface {
	// LoadRoleAssignments returns every stored assignment. Never nil: an
	// empty store is an empty slice.
	LoadRoleAssignments() ([]RoleAssignment, error)
	// AssignRole upserts ONE role's assignment: a role has at most one
	// (endpoint, model) pair. Shape-validates the assignment first.
	AssignRole(a RoleAssignment) error
	// LoadDefaultModel returns the stored default, or the zero value when
	// none has been chosen. Never an error for "unset" — unset is a value.
	LoadDefaultModel() (DefaultModel, error)
	// SetDefaultModel replaces the default. The empty pair clears it. A
	// set pair must name an endpoint the store holds AND a model that
	// endpoint offers, or the write is REFUSED with nothing stored
	// (ErrEndpointNotFound / ErrEndpointModelNotFound) — unlike
	// AssignRole, which lets a per-role assignment dangle on purpose. The
	// check belongs to the implementation because it must happen under the
	// same lock as the write: a default that names nothing must not be
	// storable through a window between a caller's check and this call.
	SetDefaultModel(d DefaultModel) error
}

// RoleDTO is the wire form of one role row (contracts/roles.list.schema.json
// and roles.assign's cross-file reference): the role always present, and the
// assigned endpointId and model, null when the role has no assignment. The
// wire lists EVERY role in the closed set — a role with no assignment is a
// row with nulls, never an absent row — so the surface shows the unassigned
// state as the first-class failure it is.
type RoleDTO struct {
	Role       ModelRole `json:"role"`
	EndpointID *string   `json:"endpointId"`
	Model      *string   `json:"model"`
}
