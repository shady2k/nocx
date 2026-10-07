package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type profileDocStore struct {
	raw      []byte
	writeErr error
}

func (s *profileDocStore) Read(_ string, into any) (bool, error) {
	if s.raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(s.raw, into)
}

func (s *profileDocStore) Write(_ string, doc any) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	s.raw = raw
	return nil
}
func (*profileDocStore) Delete(string) error     { return nil }
func (*profileDocStore) List() ([]string, error) { return nil, nil }

func TestStandardProfileInitialCASValidationAndSnapshots(t *testing.T) {
	doc := &profileDocStore{}
	repo := NewProfileRepository(doc, StandardDocumentName, nil)
	initial, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if initial.SchemaVersion != 1 || initial.Revision != 0 || initial.Enabled || initial.ReadOnlyDirs == nil || initial.ReadWriteDirs == nil {
		t.Fatalf("initial document = %#v", initial)
	}

	roots := ProfileRoots{ReadOnlyDirs: []string{"/readonly"}, ReadWriteDirs: []string{"/work"}}
	stored, err := repo.UpdateStandard(0, true, roots)
	if err != nil {
		t.Fatal(err)
	}
	roots.ReadOnlyDirs[0] = "/mutated-input"
	stored.ReadWriteDirs[0] = "/mutated-output"
	read, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if !read.Enabled || read.Revision != 1 || read.ReadOnlyDirs[0] != "/readonly" || read.ReadWriteDirs[0] != "/work" {
		t.Fatalf("stored snapshot changed through alias: %#v", read)
	}
	read.ReadOnlyDirs[0] = "/mutated-read"
	again, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if again.ReadOnlyDirs[0] != "/readonly" {
		t.Fatalf("read snapshot aliases repository: %#v", again)
	}
	if _, err := repo.UpdateStandard(0, false, ProfileRoots{}); !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	if _, err := repo.UpdateStandard(1, false, ProfileRoots{ReadWriteDirs: []string{"/bad\npath"}}); err == nil {
		t.Fatal("newline path accepted")
	}
	if _, err := repo.UpdateStandard(1, false, ProfileRoots{ReadOnlyDirs: makePaths(MaxProfileRoots + 1)}); err == nil {
		t.Fatal("too many roots accepted")
	}
}

func TestStandardProfileWriteFailureDoesNotAdvanceSnapshot(t *testing.T) {
	boom := errors.New("write failed")
	doc := &profileDocStore{writeErr: boom}
	repo := NewProfileRepository(doc, StandardDocumentName, nil)
	if _, err := repo.UpdateStandard(0, true, ProfileRoots{ReadWriteDirs: []string{"/work"}}); !errors.Is(err, boom) {
		t.Fatalf("write error = %v", err)
	}
	got, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 0 || got.Enabled || len(got.ReadWriteDirs) != 0 {
		t.Fatalf("failed write changed state: %#v", got)
	}
}

func TestWorkspaceProfileInheritanceUsesDetachedStandardSnapshot(t *testing.T) {
	standard := StandardDocument{SchemaVersion: ProfileSchemaVersion, ProfileRoots: ProfileRoots{ReadOnlyDirs: []string{"/standard"}, ReadWriteDirs: []string{}}}
	workspace := WorkspaceProfile{WorkspaceID: "workspace-1", Revision: 7}
	got := EffectiveWorkspaceRoots(workspace, standard)
	got.ReadOnlyDirs[0] = "/changed"
	if standard.ReadOnlyDirs[0] != "/standard" {
		t.Fatalf("effective roots alias standard: %#v", standard)
	}
	if EffectiveWorkspaceRoots(WorkspaceProfile{WorkspaceID: "workspace:default"}, standard).ReadOnlyDirs[0] != "/standard" {
		t.Fatal("default workspace did not inherit standard")
	}
	if _, err := NewProfileRepository(&profileDocStore{}, StandardDocumentName, nil).UpdateWorkspaceProfile(context.Background(), DefaultWorkspaceID, 0, &ProfileRoots{}); !errors.Is(err, ErrWorkspaceProfileUnsupported) {
		t.Fatalf("default override error = %v", err)
	}
}

func makePaths(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = "/root"
	}
	return out
}

func TestStandardProfileRevisionCannotWrap(t *testing.T) {
	raw, err := json.Marshal(StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: ^uint64(0), ProfileRoots: ProfileRoots{ReadOnlyDirs: []string{}, ReadWriteDirs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewProfileRepository(&profileDocStore{raw: raw}, StandardDocumentName, nil)
	if _, err := repo.UpdateStandard(^uint64(0), true, ProfileRoots{}); !errors.Is(err, ErrProfileRevisionExhausted) {
		t.Fatalf("max revision update = %v", err)
	}
}

func TestConfigurationSnapshotRejectsDuplicateWorkspaceIDs(t *testing.T) {
	snapshot := ConfigurationSnapshot{Standard: initialStandard(), Workspaces: []WorkspaceProfile{{WorkspaceID: "workspace-a"}, {WorkspaceID: "workspace-a"}}}
	if err := ValidateConfigurationSnapshot(snapshot); err == nil {
		t.Fatal("duplicate workspace ids accepted")
	}
}
