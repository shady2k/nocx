package sandbox

import (
	"context"
	"errors"
	"testing"
)

type restoreTestWorkspaceStore struct {
	profiles map[string]WorkspaceProfile
	updates  int
	restores int
}

func (s *restoreTestWorkspaceStore) GetWorkspaceProfile(_ context.Context, id string) (WorkspaceProfile, error) {
	profile, ok := s.profiles[id]
	if !ok {
		return WorkspaceProfile{}, errors.New("missing profile")
	}
	return cloneWorkspaceProfile(profile), nil
}

func (s *restoreTestWorkspaceStore) UpdateWorkspaceProfile(_ context.Context, id string, expected uint64, roots *ProfileRoots) (WorkspaceProfile, error) {
	s.updates++
	profile := s.profiles[id]
	if profile.Revision != expected {
		return WorkspaceProfile{}, ErrProfileConflict
	}
	profile.Revision++
	if roots != nil {
		copied := cloneRoots(*roots)
		profile.Override = &copied
	} else {
		profile.Override = nil
	}
	s.profiles[id] = profile
	return cloneWorkspaceProfile(profile), nil
}

func (s *restoreTestWorkspaceStore) ListWorkspaceProfiles(context.Context) ([]WorkspaceProfile, error) {
	out := make([]WorkspaceProfile, 0, len(s.profiles))
	for _, profile := range s.profiles {
		out = append(out, cloneWorkspaceProfile(profile))
	}
	return out, nil
}

func (s *restoreTestWorkspaceStore) RestoreWorkspaceProfiles(_ context.Context, profiles []WorkspaceProfile) error {
	s.restores++
	for _, imported := range profiles {
		current, ok := s.profiles[imported.WorkspaceID]
		if !ok {
			return &ProfileError{Code: "unknown_workspace", Field: "workspaces"}
		}
		current.Revision++
		current.Override = cloneSandboxOverride(imported.Override)
		s.profiles[imported.WorkspaceID] = current
	}
	return nil
}

func cloneSandboxOverride(roots *ProfileRoots) *ProfileRoots {
	if roots == nil {
		return nil
	}
	copy := cloneRoots(*roots)
	return &copy
}

func TestConfigurationRestoreScopeFencesWritesButAllowsReads(t *testing.T) {
	workspaces := &restoreTestWorkspaceStore{profiles: map[string]WorkspaceProfile{
		"workspace-a": {WorkspaceID: "workspace-a", Revision: 4},
	}}
	repo := NewProfileRepository(&profileDocStore{}, StandardDocumentName, workspaces)
	imported := ConfigurationSnapshot{
		Standard:   StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: 99, Enabled: true, ProfileRoots: ProfileRoots{ReadWriteDirs: []string{"/restored"}}},
		Workspaces: []WorkspaceProfile{{WorkspaceID: "workspace-a", Revision: 99, Override: &ProfileRoots{ReadOnlyDirs: []string{"/workspace"}}}},
	}
	var capability ConfigurationRestorer
	err := repo.WithConfigurationRestore(func(restorer ConfigurationRestorer) error {
		capability = restorer
		if _, err := repo.UpdateStandard(0, true, ProfileRoots{}); !errors.Is(err, ErrProfileRestoreBusy) {
			t.Fatalf("standard mutation during restore = %v", err)
		}
		if err := repo.WithStandardRevision(0, func() error { return nil }); !errors.Is(err, ErrProfileRestoreBusy) {
			t.Fatalf("launch CAS during restore = %v", err)
		}
		if _, err := repo.UpdateWorkspaceProfile(context.Background(), "workspace-a", 4, nil); !errors.Is(err, ErrProfileRestoreBusy) {
			t.Fatalf("workspace mutation during restore = %v", err)
		}
		if workspaces.updates != 0 {
			t.Fatalf("workspace mutation escaped scope fence: %d", workspaces.updates)
		}
		if got, err := repo.GetStandard(); err != nil || got.Revision != 0 {
			t.Fatalf("read during scope = %#v, %v", got, err)
		}
		if got, err := repo.ExportConfiguration(); err != nil || got.Standard.Revision != 0 {
			t.Fatalf("export during scope = %#v, %v", got, err)
		}
		if got, err := repo.GetWorkspaceProfile(context.Background(), "workspace-a"); err != nil || got.Revision != 4 {
			t.Fatalf("workspace read during scope = %#v, %v", got, err)
		}
		if err := restorer.RestoreConfiguration(imported); err != nil {
			return err
		}
		if err := repo.WithConfigurationRestore(func(ConfigurationRestorer) error { return nil }); !errors.Is(err, ErrProfileRestoreBusy) {
			t.Fatalf("nested restore scope = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	standard, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if standard.Revision != 1 || !standard.Enabled || standard.ReadWriteDirs[0] != "/restored" {
		t.Fatalf("restored standard = %#v", standard)
	}
	workspace, err := repo.GetWorkspaceProfile(context.Background(), "workspace-a")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Revision != 5 || workspace.Override == nil || workspaces.restores != 1 {
		t.Fatalf("restored workspace = %#v; restores=%d", workspace, workspaces.restores)
	}
	if err := capability.RestoreConfiguration(imported); !errors.Is(err, ErrProfileRestoreScopeClosed) {
		t.Fatalf("expired restore capability = %v", err)
	}
}

func TestConfigurationRestoreCallbackErrorReleasesScope(t *testing.T) {
	boom := errors.New("restore failed")
	repo := NewStandardProfileRepository(&profileDocStore{})
	if err := repo.WithConfigurationRestore(func(ConfigurationRestorer) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("restore callback error = %v", err)
	}
	if _, err := repo.UpdateStandard(0, true, ProfileRoots{}); err != nil {
		t.Fatalf("mutation after failed restore scope = %v", err)
	}
}

func TestConfigurationRestorePanicReleasesScope(t *testing.T) {
	repo := NewStandardProfileRepository(&profileDocStore{})
	func() {
		defer func() { _ = recover() }()
		_ = repo.WithConfigurationRestore(func(ConfigurationRestorer) error { panic("restore panic") })
	}()
	if _, err := repo.UpdateStandard(0, true, ProfileRoots{}); err != nil {
		t.Fatalf("mutation after panic = %v", err)
	}
}
