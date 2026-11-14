package profile

import "testing"

func TestAuditingRoleDoesNotUseAnsweringChoice(t *testing.T) {
	model, err := ResolveRole(RoleAuditing, "pi", []RoleAssignment{{Role: RoleAnswering, AgentID: "pi", Model: "answer"}}, nil)
	if err != nil || model != "" {
		t.Fatalf("auditing: %q %v", model, err)
	}
}
