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
