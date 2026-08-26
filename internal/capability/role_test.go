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
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

func newRoleEnv(t *testing.T) (capability.ConfigOperation, *profile.JSONStore) {
	t.Helper()
	dir := t.TempDir()
	store := profile.NewJSONStore(filepath.Join(dir, "profiles.json"))
	configGate, vaultGate, _, _, _, _ := testGates()
	op := capability.NewConfigOperation(configGate, vaultGate, testLane(),
		store, store, store, store, newProfileService(t), nil, nil, nil)
	return op, store
}

func TestRoles_AssignThenResolveThroughTheOperation(t *testing.T) {
	op, _ := newRoleEnv(t)
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		ep := testEndpoint()
		if _, err := svc.CreateEndpoint(ctx, ep, credential.Secret{}); err != nil {
			return err
		}
		return svc.AssignRole(profile.RoleAssignment{
			Role: profile.RoleAnswering, EndpointID: ep.ID, Model: ep.Models[0].Name,
		})
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	// A LATER call resolves what the earlier call wrote: the ask handler's
	// exact shape.
	var ep profile.Endpoint
	var model string
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		ep, model, err = svc.ResolveRole(profile.RoleAnswering)
		return err
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Errorf("resolved model = %q, want gpt-4o-mini", model)
	}
	if ep.Name != "OpenAI" {
		t.Errorf("resolved endpoint = %q, want OpenAI", ep.Name)
	}
}

func TestRoles_UnassignedSurfacesAsARefusalThroughTheSeam(t *testing.T) {
	op, _ := newRoleEnv(t)
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, _, err := svc.ResolveRole(profile.RoleAnswering)
		return err
	})
	if !errors.Is(err, profile.ErrRoleUnassigned) {
		t.Fatalf("ResolveRole(unassigned) through the op = %v, want ErrRoleUnassigned", err)
	}
}

func TestRoles_ListIncludesStoredAssignmentsOnly(t *testing.T) {
	op, _ := newRoleEnv(t)
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.AssignRole(profile.RoleAssignment{
			Role: profile.RoleClassifier, EndpointID: "endpoint:custom:x:1", Model: "m",
		})
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	var got []profile.RoleAssignment
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var err error
		got, err = svc.ListRoleAssignments()
		return err
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Role != profile.RoleClassifier {
		t.Fatalf("assignments = %+v, want the classifier row", got)
	}
}

func TestRoles_EndpointDeleteLeavesTheDangleVisible(t *testing.T) {
	op, store := newRoleEnv(t)
	ep := profile.Endpoint{
		ID: "endpoint:custom:doomed:1", Name: "Doomed", BaseURL: "http://127.0.0.1:1/v1",
		Schema: profile.EndpointSchemaOpenAICompatible,
		Models: []profile.EndpointModel{{Name: "gpt-4o"}},
	}
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		if _, err := svc.CreateEndpoint(ctx, ep, credential.Secret{}); err != nil {
			return err
		}
		if err := svc.AssignRole(profile.RoleAssignment{Role: profile.RoleAnswering, EndpointID: ep.ID, Model: "gpt-4o"}); err != nil {
			return err
		}
		return svc.DeleteEndpoint(ctx, ep.ID)
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// The assignment survives the delete (a role names an endpoint, not a
	// secret); resolution now refuses and names the endpoint.
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, _, err := svc.ResolveRole(profile.RoleAnswering)
		return err
	})
	if !errors.Is(err, profile.ErrRoleEndpointGone) {
		t.Fatalf("ResolveRole after endpoint delete = %v, want ErrRoleEndpointGone", err)
	}
	if stored, _ := store.LoadRoleAssignments(); len(stored) != 1 {
		t.Fatalf("assignments after delete = %+v, want the dangle kept so the row can say what happened", stored)
	}
}

func TestRoles_RefusesAnUnknownRoleName(t *testing.T) {
	op, _ := newRoleEnv(t)
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		return svc.AssignRole(profile.RoleAssignment{Role: "invented", EndpointID: "e", Model: "m"})
	})
	if !errors.Is(err, profile.ErrRoleUnknown) {
		t.Fatalf("AssignRole(invented) through the op = %v, want ErrRoleUnknown", err)
	}
}
