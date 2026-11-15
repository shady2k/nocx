package profile

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestRoleChoicesAreKeptPerAgentAndSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s := NewJSONStore(path)
	for _, a := range []RoleAssignment{{Role: RoleAnswering, AgentID: "pi", Model: "one"}, {Role: RoleAnswering, AgentID: "omp", Model: "two"}, {Role: RoleAnswering, AgentID: "pi", Model: "three"}} {
		if err := s.AssignRole(a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewJSONStore(path).LoadRoleAssignments()
	if err != nil {
		t.Fatal(err)
	}
	for agent, want := range map[string]string{"pi": "three", "omp": "two"} {
		model, resolveErr := ResolveRole(RoleAnswering, agent, got, []harness.Model{{ID: want}})
		if resolveErr != nil || model != want {
			t.Fatalf("%s: %q, %v", agent, model, resolveErr)
		}
	}
	if clearErr := s.AssignRole(RoleAssignment{Role: RoleAnswering, AgentID: "pi"}); clearErr != nil {
		t.Fatal(clearErr)
	}
	got, err = s.LoadRoleAssignments()
	if err != nil || len(got) != 1 || got[0].AgentID != "omp" {
		t.Fatalf("clear: %+v %v", got, err)
	}
}

func TestRoleResolutionUnassignedUsesAgentDefaultAndRemovedModelRefuses(t *testing.T) {
	as := []RoleAssignment{{Role: RoleAnswering, AgentID: "pi", Model: "chosen"}}
	for _, role := range AllRoles() {
		model, err := ResolveRole(role, "other", as, nil)
		if err != nil || model != "" {
			t.Fatalf("unassigned: %q %v", model, err)
		}
	}
	_, err := ResolveRole(RoleAnswering, "pi", as, []harness.Model{{ID: "neighbour"}})
	if !errors.Is(err, ErrRoleModelGone) {
		t.Fatalf("removed: %v", err)
	}
	if as[0].Model != "chosen" {
		t.Fatal("repaired assignment")
	}
	if _, err := ResolveRole("unknown", "pi", as, nil); !errors.Is(err, ErrRoleUnknown) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestAssignRoleRejectsUnknownRoleAndMissingAgent(t *testing.T) {
	s := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	for _, a := range []RoleAssignment{{Role: "unknown", AgentID: "pi", Model: "m"}, {Role: RoleAnswering, Model: "m"}, {Role: RoleAnswering}} {
		if err := s.AssignRole(a); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
}
