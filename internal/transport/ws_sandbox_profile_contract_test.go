package transport

// DTO conformance for the sandbox profile/grant wire shapes (design
// 2026-08-23 §4.3). The renderer's types are generated from the schemas in
// contracts/, and these DTOs are the transport's hand-written half: a field
// tag that drifts from the schema fails here rather than reaching a renderer
// that cannot see it (contracts/README.md).

import (
	"testing"

	"github.com/shady2k/nocx/internal/sandbox"
)

func TestSandboxProfileGetDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.profile.get.schema.json")
	validateJSON(t, schema, mustMarshal(sandboxProfileGetResult{
		WorkspaceID:   "ws-1",
		Source:        sandbox.ProfileSourceStandard,
		Revision:      3,
		Inherited:     true,
		WritablePaths: []string{},
		ReadOnlyPaths: []string{},
	}), "sandbox.profile.get standard DTO")
	validateJSON(t, schema, mustMarshal(sandboxProfileGetResult{
		WorkspaceID:   "ws-1",
		Source:        sandbox.ProfileSourceWorkspace,
		Revision:      7,
		Inherited:     false,
		WritablePaths: []string{"/project"},
		ReadOnlyPaths: []string{"/reference"},
	}), "sandbox.profile.get workspace DTO")
}

func TestSandboxProfileSetDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.profile.set.schema.json")
	validateJSON(t, schema, mustMarshal(sandboxProfileSetResult{
		WorkspaceID:   "ws-1",
		Revision:      1,
		WritablePaths: []string{"/project"},
		ReadOnlyPaths: []string{},
	}), "sandbox.profile.set DTO")
}

func TestSandboxProfileDeleteDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.profile.delete.schema.json")
	validateJSON(t, schema, mustMarshal(sandboxProfileDeleteResult{
		WorkspaceID: "ws-1",
	}), "sandbox.profile.delete DTO")
}

func TestSandboxGrantGetDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.grant.get.schema.json")
	validateJSON(t, schema, mustMarshal(sandboxGrantGetResult{
		IssuedAt: 1_750_000_000_000,
		Realized: &sandbox.SessionInfo{
			Backend:       sandbox.BackendLandlock,
			Workspace:     "/home/user/work",
			WritableRoots: []string{"/home/user/work"},
			ReadOnlyRoots: []string{"/usr"},
			HomeProjections: []sandbox.HomeProjection{
				{HostPath: "/home/user/work", RelativePath: "work"},
			},
		},
		Provenance: sandbox.GrantProvenance{
			WorkspaceID:     "ws-1",
			ProfileSource:   sandbox.ProfileSourceWorkspace,
			ProfileRevision: new(int64(42)),
		},
	}), "sandbox.grant.get workspace DTO")

	// The standard-source grant carries the settings revision it realized.
	validateJSON(t, schema, mustMarshal(sandboxGrantGetResult{
		IssuedAt: 1_750_000_000_000,
		Realized: &sandbox.SessionInfo{
			Backend: sandbox.BackendLandlock, Workspace: "/w",
			WritableRoots: []string{}, ReadOnlyRoots: []string{}, HomeProjections: []sandbox.HomeProjection{},
		},
		Provenance: sandbox.GrantProvenance{
			WorkspaceID:     "ws-1",
			ProfileSource:   sandbox.ProfileSourceStandard,
			ProfileRevision: new(int64(0)),
		},
	}), "sandbox.grant.get standard DTO")

	// The grant result is nullable: a null payload is a valid answer.
	validateJSON(t, schema, []byte(`null`), "sandbox.grant.get null DTO")
}
