package profile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestService(t *testing.T) (*ProfileService, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "p.json")
	store := NewJSONStore(storePath)
	return NewProfileService(store), storePath
}

func svc(t *testing.T) *ProfileService {
	t.Helper()
	s, _ := newTestService(t)
	return s
}

func makeTestProfile(id, name, host string) SSHProfile {
	return SSHProfile{
		Base: Base{ID: id, Type: "ssh", Name: name},
		Options: StoredSSHProfileOptions{
			Host: host,
			Port: Ptr(22),
			User: Ptr("testuser"),
		},
	}
}

func makeTestGroup(id, name string, defaults *ProfileDefaults) ProfileGroup {
	return ProfileGroup{ID: id, Name: name, Defaults: defaults}
}

// ---------------------------------------------------------------------------
// SaveProfile
// ---------------------------------------------------------------------------

func TestServiceSaveProfile_CreatesNew(t *testing.T) {
	s := svc(t)
	p := makeTestProfile("ssh:custom:test:1", "test", "example.com")
	if err := s.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	all, _ := s.store.LoadAll()
	if len(all.Profiles) != 1 || all.Profiles[0].ID != "ssh:custom:test:1" {
		t.Fatalf("expected 1 profile, got %d", len(all.Profiles))
	}
}

func TestServiceSaveProfile_UpdatesExisting(t *testing.T) {
	s := svc(t)
	p := makeTestProfile("ssh:custom:test:1", "original", "example.com")
	if err := s.SaveProfile(p); err != nil {
		t.Fatalf("first SaveProfile: %v", err)
	}
	p.Name = "renamed"
	if err := s.SaveProfile(p); err != nil {
		t.Fatalf("second SaveProfile: %v", err)
	}
	all, _ := s.store.LoadAll()
	if len(all.Profiles) != 1 || all.Profiles[0].Name != "renamed" {
		t.Fatalf("profile not updated, got Name=%q", all.Profiles[0].Name)
	}
}

func TestServiceSaveProfile_RejectsEmptyHost(t *testing.T) {
	s := svc(t)
	p := SSHProfile{
		Base:    Base{ID: "ssh:custom:test:1", Type: "ssh", Name: "test"},
		Options: StoredSSHProfileOptions{},
	}
	err := s.SaveProfile(p)
	if err == nil || !contains(err.Error(), "host is required") {
		t.Fatalf("expected host required error, got %v", err)
	}
}

func TestServiceSaveProfile_RejectsEmptyID(t *testing.T) {
	s := svc(t)
	p := makeTestProfile("", "test", "example.com")
	err := s.SaveProfile(p)
	if !errors.Is(err, ErrProfileIDRequired) {
		t.Fatalf("expected ErrProfileIDRequired, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SaveGroup
// ---------------------------------------------------------------------------

func TestServiceSaveGroup_CreatesNew(t *testing.T) {
	s := svc(t)
	g := makeTestGroup("g1", "Prod", nil)
	if err := s.SaveGroup(g); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	all, _ := s.store.LoadAll()
	if len(all.Groups) != 1 || all.Groups[0].ID != "g1" {
		t.Fatalf("expected 1 group, got %d", len(all.Groups))
	}
}

func TestServiceSaveGroup_RejectsUnknownDefaultKeys(t *testing.T) {
	s := svc(t)
	d, err := DecodeDefaults(map[string]any{"typoField": "value"})
	if err != nil {
		t.Fatalf("DecodeDefaults: %v", err)
	}
	g := ProfileGroup{ID: "g1", Name: "Bad", Defaults: &d}
	err = s.SaveGroup(g)
	if err == nil || !contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown keys error, got %v", err)
	}
}

func TestServiceSaveGroup_ValidatesTree(t *testing.T) {
	s := svc(t)
	if err := s.SaveGroup(makeTestGroup("g1", "Parent", nil)); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := makeTestGroup("g2", "Child", nil)
	child.ParentGroupID = "g1"
	if err := s.SaveGroup(child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	// Create cycle: g1 -> g2 -> g1 by updating g1's parent to g2.
	cycle := makeTestGroup("g1", "Cycling", nil)
	cycle.ParentGroupID = "g2"
	err := s.SaveGroup(cycle)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

// ---------------------------------------------------------------------------
// AtomicImport — basic success
// ---------------------------------------------------------------------------

func TestAtomicImport_FullSuccess(t *testing.T) {
	s := svc(t)
	profiles := []SSHProfile{
		makeTestProfile("ssh:custom:p1:1", "web", "web.example.com"),
		makeTestProfile("ssh:custom:p2:1", "db", "db.example.com"),
	}
	groups := []ProfileGroup{makeTestGroup("g1", "Prod", nil)}

	result := s.AtomicImport(profiles, groups)
	if len(result.ImportErrors) > 0 {
		t.Fatalf("unexpected errors: %v", result.ImportErrors)
	}
	if result.ProfilesImported != 2 {
		t.Errorf("ProfilesImported = %d, want 2", result.ProfilesImported)
	}
	if result.GroupsImported != 1 {
		t.Errorf("GroupsImported = %d, want 1", result.GroupsImported)
	}
	all, _ := s.store.LoadAll()
	if len(all.Profiles) != 2 || len(all.Groups) != 1 {
		t.Fatalf("store state: profiles=%d groups=%d",
			len(all.Profiles), len(all.Groups))
	}
}

// ---------------------------------------------------------------------------
// AtomicImport — collision policy
// ---------------------------------------------------------------------------

func TestAtomicImport_ProfileOverwrite(t *testing.T) {
	s := svc(t)
	p1 := makeTestProfile("ssh:custom:p1:1", "original", "original.example.com")
	if err := s.SaveProfile(p1); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	result := s.AtomicImport(
		[]SSHProfile{makeTestProfile("ssh:custom:p1:1", "overwritten", "new.example.com")},
		nil,
	)
	if len(result.ImportErrors) > 0 {
		t.Fatalf("import errors: %v", result.ImportErrors)
	}
	if result.ProfilesImported != 1 {
		t.Errorf("ProfilesImported = %d, want 1", result.ProfilesImported)
	}
	all, _ := s.store.LoadAll()
	if len(all.Profiles) != 1 || all.Profiles[0].Name != "overwritten" {
		t.Errorf("profile not overwritten, got Name=%q", all.Profiles[0].Name)
	}
}

func TestAtomicImport_GroupOverwrite(t *testing.T) {
	s := svc(t)
	g1 := makeTestGroup("g1", "Original", nil)
	if err := s.SaveGroup(g1); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	result := s.AtomicImport(
		nil,
		[]ProfileGroup{makeTestGroup("g1", "Overwritten", nil)},
	)
	if len(result.ImportErrors) > 0 {
		t.Fatalf("import errors: %v", result.ImportErrors)
	}
	all, _ := s.store.LoadAll()
	if len(all.Groups) != 1 || all.Groups[0].Name != "Overwritten" {
		t.Errorf("group not overwritten, got Name=%q", all.Groups[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Transactional import — partial failure leaves store unchanged
// ---------------------------------------------------------------------------

func TestAtomicImport_Transactional_LastRecordFailure(t *testing.T) {
	s, storePath := newTestService(t)

	// Read raw file before import.
	// #nosec G304 -- t.TempDir() path, never user input
	preRaw, err := os.ReadFile(storePath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read before: %v", err)
	}
	if os.IsNotExist(err) {
		preRaw = []byte{}
	}

	profiles := []SSHProfile{
		makeTestProfile("ssh:custom:p1:1", "web", "web.example.com"),
		// Invalid profile — missing host. Fatal: nothing is written.
		{Base: Base{ID: "ssh:custom:p2:1", Type: "ssh", Name: "db"}},
	}
	groups := []ProfileGroup{makeTestGroup("g1", "Prod", nil)}

	result := s.AtomicImport(profiles, groups)
	if len(result.ImportErrors) == 0 {
		t.Fatal("expected import errors for invalid profile, got none")
	}

	// Read raw file after import.
	// #nosec G304 -- t.TempDir() path, never user input
	postRaw, err := os.ReadFile(storePath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read after: %v", err)
	}
	if os.IsNotExist(err) {
		postRaw = []byte{}
	}

	if !bytes.Equal(preRaw, postRaw) {
		t.Error("store file changed byte-for-byte after failed import")
	}
}

// ---------------------------------------------------------------------------
// Snapshot + AtomicReplace
// ---------------------------------------------------------------------------

func TestServiceSnapshot_ReturnsFullState(t *testing.T) {
	s := svc(t)
	p1 := makeTestProfile("ssh:custom:test:1", "one", "one.example.com")
	p2 := makeTestProfile("ssh:custom:test:2", "two", "two.example.com")
	if err := s.SaveProfile(p1); err != nil {
		t.Fatalf("SaveProfile p1: %v", err)
	}
	if err := s.SaveProfile(p2); err != nil {
		t.Fatalf("SaveProfile p2: %v", err)
	}
	if err := s.SaveGroup(makeTestGroup("group-1", "Work", nil)); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Profiles) != 2 {
		t.Errorf("snapshot profiles = %d, want 2", len(snap.Profiles))
	}
	if len(snap.Groups) != 1 {
		t.Errorf("snapshot groups = %d, want 1", len(snap.Groups))
	}
}

// AtomicReplace is the rollback primitive: it must return the store to a
// captured generation EXACTLY — a profile the import added disappears, and
// a profile the import overwrote is restored. A merge can do neither for the
// added profile, which is why this method exists.
func TestServiceAtomicReplace_RestoresCapturedGeneration(t *testing.T) {
	s := svc(t)
	oldProfile := makeTestProfile("ssh:custom:test:1", "old-name", "old.example.com")
	if err := s.SaveProfile(oldProfile); err != nil {
		t.Fatalf("SaveProfile old: %v", err)
	}
	if err := s.SaveGroup(makeTestGroup("group-1", "Work", nil)); err != nil {
		t.Fatalf("SaveGroup old: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Move the store to a NEW generation: overwrite the existing profile and
	// add one that did not exist before — exactly what an import does.
	overwritten := makeTestProfile("ssh:custom:test:1", "new-name", "new.example.com")
	imported := makeTestProfile("ssh:custom:test:9", "imported", "imported.example.com")
	importRes := s.AtomicImport([]SSHProfile{overwritten, imported}, nil)
	if len(importRes.ImportErrors) > 0 {
		t.Fatalf("AtomicImport: %v", importRes.ImportErrors)
	}

	if err := s.AtomicReplace(snap); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}

	all, _ := s.store.LoadAll()
	if len(all.Profiles) != 1 {
		t.Fatalf("profiles after replace = %d, want 1 (imported profile must be gone)", len(all.Profiles))
	}
	if all.Profiles[0].Name != oldProfile.Name {
		t.Errorf("profile name = %q, want %q (overwritten profile must be restored)", all.Profiles[0].Name, oldProfile.Name)
	}
	if len(all.Groups) != 1 || all.Groups[0].Name != "Work" {
		t.Errorf("groups after replace = %+v, want the captured group", all.Groups)
	}
}

func TestServiceAtomicReplace_InvalidStateLeavesStoreUnchanged(t *testing.T) {
	s := svc(t)
	if err := s.SaveProfile(makeTestProfile("ssh:custom:test:1", "keep", "keep.example.com")); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	err := s.AtomicReplace(ConfigSnapshot{Profiles: []SSHProfile{{Base: Base{ID: "ssh:bad"}}}})
	if err == nil {
		t.Fatal("AtomicReplace with a host-less profile succeeded, want error")
	}

	after, _ := s.store.LoadAll()
	if len(after.Profiles) != 1 || after.Profiles[0].Name != "keep" {
		t.Errorf("store changed after failed replace: %+v", after.Profiles)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
