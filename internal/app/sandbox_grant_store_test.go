package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/settings"
)

type sandboxSettingsFake struct {
	snapshot settings.SettingsSnapshot
	appended *settings.PathList
	path     string
}

func (f *sandboxSettingsFake) AppendSandboxPath(profile *settings.PathList, path string) (int, error) {
	f.appended, f.path = profile, path
	return 8, nil
}

func (f *sandboxSettingsFake) GetSnapshot() (settings.SettingsSnapshot, error) {
	return f.snapshot, nil
}

type sandboxProfilesFake struct {
	profiles map[string]*content.WorkspaceSandboxProfile
	missing  bool
}

func (f *sandboxProfilesFake) WorkspaceSandboxProfile(_ context.Context, workspaceID string) (*content.WorkspaceSandboxProfile, error) {
	if f.missing {
		return nil, content.ErrNoSuchWorkspace
	}
	profile := f.profiles[workspaceID]
	if profile == nil {
		return nil, nil
	}
	copy := *profile
	copy.WritablePaths = append([]string(nil), profile.WritablePaths...)
	copy.ReadOnlyPaths = append([]string(nil), profile.ReadOnlyPaths...)
	return &copy, nil
}

func (f *sandboxProfilesFake) SetWorkspaceSandboxProfile(_ context.Context, workspaceID string, expectedRevision int64, profile content.WorkspaceSandboxProfile) (int64, error) {
	current := f.profiles[workspaceID]
	var revision int64
	if current != nil {
		revision = current.Revision
	}
	if revision != expectedRevision {
		return 0, content.ErrSandboxProfileRevision
	}
	profile.Revision = revision + 1
	f.profiles[workspaceID] = &profile
	return profile.Revision, nil
}

func TestSandboxGrantStorePromotesDefaultWorkspaceIntoStandardProfile(t *testing.T) {
	settingsStore := &sandboxSettingsFake{}
	store := sandboxGrantStore{registry: settingsStore}

	revision, err := store.PromoteSandboxPath(content.DefaultWorkspaceID, sandbox.AccessReadOnly, "/safe")
	if err != nil || revision != 8 {
		t.Fatalf("PromoteSandboxPath = %d, %v; want 8, nil", revision, err)
	}
	if settingsStore.appended != settings.SandboxAllowedReadOnlyPaths || settingsStore.path != "/safe" {
		t.Fatalf("standard promotion = %#v, %q", settingsStore.appended, settingsStore.path)
	}
}

func TestSandboxGrantStoreMaterializesWorkspaceProfileFromStandard(t *testing.T) {
	base := t.TempDir()
	standardWritable := makeDirectory(t, filepath.Join(base, "standard-write"))
	standardReadOnly := makeDirectory(t, filepath.Join(base, "standard-read"))
	promoted := makeDirectory(t, filepath.Join(base, "promoted"))
	settingsStore := &sandboxSettingsFake{snapshot: settings.SettingsSnapshot{Values: map[string]any{
		settings.SandboxAllowedWritablePaths.Key(): []string{standardWritable},
		settings.SandboxAllowedReadOnlyPaths.Key(): []string{standardReadOnly},
	}}}
	profiles := &sandboxProfilesFake{profiles: make(map[string]*content.WorkspaceSandboxProfile)}
	store := sandboxGrantStore{registry: settingsStore, layout: func() sandboxWorkspaceProfiles { return profiles }}

	revision, err := store.PromoteSandboxPath("workspace-1", sandbox.AccessReadWrite, promoted)
	if err != nil || revision != 1 {
		t.Fatalf("PromoteSandboxPath = %d, %v; want 1, nil", revision, err)
	}
	profile := profiles.profiles["workspace-1"]
	if profile == nil || len(profile.WritablePaths) != 2 || len(profile.ReadOnlyPaths) != 1 {
		t.Fatalf("materialized profile = %#v", profile)
	}
	if profile.WritablePaths[0] != standardWritable || profile.WritablePaths[1] != promoted || profile.ReadOnlyPaths[0] != standardReadOnly {
		t.Fatalf("materialized paths = %#v", profile)
	}
}

func TestSandboxGrantStoreRefusesDeletedWorkspace(t *testing.T) {
	profiles := &sandboxProfilesFake{profiles: make(map[string]*content.WorkspaceSandboxProfile), missing: true}
	store := sandboxGrantStore{
		registry: &sandboxSettingsFake{},
		layout:   func() sandboxWorkspaceProfiles { return profiles },
	}
	if _, err := store.PromoteSandboxPath("gone", sandbox.AccessReadOnly, "/safe"); !errors.Is(err, sandbox.ErrAccessGrantUnavailable) {
		t.Fatalf("PromoteSandboxPath error = %v, want ErrAccessGrantUnavailable", err)
	}
}

func makeDirectory(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return canonical
}
