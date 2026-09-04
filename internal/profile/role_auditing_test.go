package profile

import (
	"errors"
	"testing"
)

// The auditing role (design §7) is the model a person asks to READ one skill
// they already hold. The closed set and the parse are pinned with every other
// role in role_test.go; what is only true of this one is the shape of its
// absence.
//
// Unassigned is a REFUSAL here and a fallback at the CONSUMER, never a silent
// hop inside the resolver. That split is the whole of role.go's rule — a role
// is never re-pointed at another model behind the person's back — and it is
// what makes the audit's note possible at all: the surface can only say "this
// ran on the answering role's model" because this call said no first.
func TestAuditingRoleUnassignedRefusesRatherThanResolvingSomethingElse(t *testing.T) {
	endpoints := []Endpoint{{ID: "e1", Name: "Local", Models: []EndpointModel{{Name: "qwen3"}}}}
	assignments := []RoleAssignment{{Role: RoleAnswering, EndpointID: "e1", Model: "qwen3"}}

	_, _, err := ResolveRole(RoleAuditing, assignments, DefaultModel{}, endpoints)
	if !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("ResolveRole(auditing) = %v; the answering role's pair must not be reached from inside the resolver", err)
	}
}
