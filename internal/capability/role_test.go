package capability_test

// The role surface of ConfigService (bead nocx-e6kn2): the ONE resolution
// path. A role assigned through the service resolves through the service —
// an assignment made in one op run is picked up by the next ResolveRole,
// and the refusal rules (unassigned, endpoint gone, model gone) surface
// through the seam the ask handler actually holds.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/profile"
)

func newRoleEnv(t *testing.T) (capability.ConfigOperation, *profile.JSONStore) {
	t.Helper()
	dir := t.TempDir()
	store := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	configGate, vaultGate, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(storePlan(configGate, vaultGate),
		store, store, store, store, newProfileService(t), nil, nil, nil)
	return op, store
}

func TestRoles_AssignAndResolveThroughOperation(t *testing.T) {
	op, _ := newRoleEnv(t)
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		if err := svc.AssignRole(profile.RoleAssignment{Role: profile.RoleAnswering, AgentID: "pi", Model: "chosen"}); err != nil {
			return err
		}
		model, err := svc.ResolveRole(profile.RoleAnswering, "pi", []harness.Model{{ID: "chosen"}})
		if err != nil {
			return err
		}
		if model != "chosen" {
			t.Fatalf("model %q", model)
		}
		_, err = svc.ResolveRole(profile.RoleAnswering, "pi", nil)
		if !errors.Is(err, profile.ErrRoleModelGone) {
			t.Fatalf("removed: %v", err)
		}
		model, err = svc.ResolveRole(profile.RoleAnswering, "other", nil)
		if err != nil || model != "" {
			t.Fatalf("unassigned: %q %v", model, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
