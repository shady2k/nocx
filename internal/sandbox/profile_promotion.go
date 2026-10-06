package sandbox

import (
	"context"
	"math"
	"path/filepath"
	"strings"
)

// PromoteDirectory is the event resolver's profile write. Both captured clocks
// are checked under the existing configuration lock; inherited roots cannot be
// silently copied from a newer standard document. Existing process grants are
// neither accepted nor changed by this API.
func (r *ProfileRepository) PromoteDirectory(ctx context.Context, workspaceID string, expectedStandard, expectedWorkspace uint64, access Access, directory string) (StandardDocument, WorkspaceProfile, error) {
	if access != ReadOnly && access != ReadWrite {
		return StandardDocument{}, WorkspaceProfile{}, ErrProfileConflict
	}
	var standard StandardDocument
	var workspace WorkspaceProfile
	err := r.lock.withWrite(func() error {
		var err error
		standard, err = r.getStandardUnlocked()
		if err != nil {
			return err
		}
		if standard.Revision != expectedStandard {
			return ErrProfileConflict
		}
		roots := cloneRoots(standard.ProfileRoots)
		isStandard := workspaceID == "" || workspaceID == DefaultWorkspaceID
		if isStandard {
			workspace.WorkspaceID = workspaceID
		}
		if !isStandard {
			if r.workspaces == nil {
				return ErrWorkspaceProfileUnsupported
			}
			workspace, err = r.workspaces.GetWorkspaceProfile(ctx, workspaceID)
			if err != nil {
				return err
			}
			if workspace.Revision != expectedWorkspace {
				return ErrProfileConflict
			}
			roots = EffectiveWorkspaceRoots(workspace, standard)
		} else if expectedWorkspace != 0 {
			return ErrProfileConflict
		}
		covered := func(root string) bool {
			return root == directory || root == string(filepath.Separator) || strings.HasPrefix(directory, root+string(filepath.Separator))
		}
		for _, root := range roots.ReadWriteDirs {
			if covered(root) {
				return nil
			}
		}
		if access == ReadOnly {
			for _, root := range roots.ReadOnlyDirs {
				if covered(root) {
					return nil
				}
			}
			roots.ReadOnlyDirs = append(roots.ReadOnlyDirs, directory)
		} else {
			for index, root := range roots.ReadOnlyDirs {
				if root == directory {
					roots.ReadOnlyDirs = append(roots.ReadOnlyDirs[:index], roots.ReadOnlyDirs[index+1:]...)
					break
				}
			}
			roots.ReadWriteDirs = append(roots.ReadWriteDirs, directory)
		}
		if err = validateRoots(roots); err != nil {
			return err
		}
		if isStandard {
			if standard.Revision == math.MaxUint64 {
				return ErrProfileRevisionExhausted
			}
			standard = StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: standard.Revision + 1, Enabled: standard.Enabled, ProfileRoots: roots}
			if err = r.doc.Write(r.name, standard); err != nil {
				return &ProfileError{Code: "document_write_failed", Field: "standard", cause: err}
			}
			return nil
		}
		workspace, err = r.workspaces.UpdateWorkspaceProfile(ctx, workspaceID, expectedWorkspace, &roots)
		return err
	})
	if err != nil {
		return StandardDocument{}, WorkspaceProfile{}, err
	}
	return cloneStandard(standard), cloneWorkspaceProfile(workspace), nil
}
