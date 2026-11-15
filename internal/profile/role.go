package profile

import (
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/harness"
)

// ModelRole is a product-defined role whose choices are kept per agent.
type ModelRole string

const (
	RoleAnswering ModelRole = "answering"
	// These roles stay declared for future consumers (ADR-anntnxdj decision 6).
	RoleClassifier  ModelRole = "classifier"
	RoleSummarizing ModelRole = "summarizing"
	RoleAuditing    ModelRole = "auditing"
)

// AllRoles is the closed role set, in the order the roles surface renders
// them (answering first: it is the role a normal ask uses).
func AllRoles() []ModelRole {
	return []ModelRole{RoleAnswering, RoleClassifier, RoleSummarizing, RoleAuditing}
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

// RoleAssignment is one agent's choice for a product role. Empty Model clears it.
type RoleAssignment struct {
	Role    ModelRole `json:"role"`
	AgentID string    `json:"agentId"`
	Model   string    `json:"model"`
}

func ValidateRoleAssignment(a RoleAssignment) error {
	if !ValidModelRole(a.Role) {
		return fmt.Errorf("%q: %w", a.Role, ErrRoleUnknown)
	}
	if a.AgentID == "" {
		return errors.New("role assignment: agent id is required")
	}
	return nil
}

var (
	ErrRoleUnknown   = errors.New("unknown model role")
	ErrRoleModelGone = errors.New("model is no longer offered by the agent")
)

// ResolveRole returns the agent's chosen model. An empty result delegates to
// the agent's default, which the conversation pins from its first answer.
// A missing chosen model is reported, never substituted or forgotten.
func ResolveRole(role ModelRole, agentID string, assignments []RoleAssignment, models []harness.Model) (string, error) {
	if !ValidModelRole(role) {
		return "", fmt.Errorf("role %q: %w", role, ErrRoleUnknown)
	}
	for _, a := range assignments {
		if a.Role != role || a.AgentID != agentID {
			continue
		}
		for _, m := range models {
			if m.ID == a.Model {
				return a.Model, nil
			}
		}
		return "", fmt.Errorf("role %q: model %q for agent %q: %w", role, a.Model, agentID, ErrRoleModelGone)
	}
	return "", nil
}

// RoleRepository persists choices keyed by agent and role.
type RoleRepository interface {
	LoadRoleAssignments() ([]RoleAssignment, error)
	AssignRole(a RoleAssignment) error
}

// RoleDTO lists every product role; null Model means the agent's default.
type RoleDTO struct {
	Role  ModelRole `json:"role"`
	Model *string   `json:"model"`
}
