package sandbox

import "encoding/json"

const MaxConfigurationBytes = 8 << 20

// ValidateConfigurationSnapshot checks the complete imported typed document
// before callers perform backup journaling or mutate either store.
func ValidateConfigurationSnapshot(snapshot ConfigurationSnapshot) error {
	if err := validateStandard(snapshot.Standard); err != nil {
		return err
	}
	if len(snapshot.Workspaces) > MaxWorkspaceProfiles {
		return &ProfileError{Code: "too_many_workspaces", Field: "workspaces", Index: MaxWorkspaceProfiles}
	}
	bytes := 0
	addBytes := func(n int, field string, index int) error {
		if n < 0 || n > MaxConfigurationBytes-bytes {
			return &ProfileError{Code: "configuration_too_large", Field: field, Index: index}
		}
		bytes += n
		return nil
	}
	for _, path := range snapshot.Standard.ReadOnlyDirs {
		if err := addBytes(len(path), "standard.readOnlyDirs", 0); err != nil {
			return err
		}
	}
	for _, path := range snapshot.Standard.ReadWriteDirs {
		if err := addBytes(len(path), "standard.readWriteDirs", 0); err != nil {
			return err
		}
	}
	if err := addBytes(256, "standard", 0); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(snapshot.Workspaces))
	for i, profile := range snapshot.Workspaces {
		if len(profile.WorkspaceID) == 0 || len(profile.WorkspaceID) > 128 || profile.WorkspaceID == DefaultWorkspaceID {
			return &ProfileError{Code: "invalid_workspace", Field: "workspaces", Index: i}
		}
		for _, ch := range profile.WorkspaceID {
			if ch == 0 || ch == '\n' || ch == '\r' {
				return &ProfileError{Code: "invalid_workspace", Field: "workspaces", Index: i}
			}
		}
		if _, exists := seen[profile.WorkspaceID]; exists {
			return &ProfileError{Code: "duplicate_workspace", Field: "workspaces", Index: i}
		}
		seen[profile.WorkspaceID] = struct{}{}
		if profile.Override != nil {
			if err := validateRoots(*profile.Override); err != nil {
				return &ProfileError{Code: "invalid_workspace_profile", Field: "workspaces", Index: i}
			}
			for _, path := range profile.Override.ReadOnlyDirs {
				if err := addBytes(len(path), "workspaces", i); err != nil {
					return err
				}
			}
			for _, path := range profile.Override.ReadWriteDirs {
				if err := addBytes(len(path), "workspaces", i); err != nil {
					return err
				}
			}
		}
		if err := addBytes(len(profile.WorkspaceID)+256, "workspaces", i); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return &ProfileError{Code: "invalid_configuration", Field: "configuration"}
	}
	if len(raw) > MaxConfigurationBytes {
		return &ProfileError{Code: "configuration_too_large", Field: "configuration"}
	}
	return nil
}
