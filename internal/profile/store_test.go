package profile

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// TestSaveProfileWithGrant_AddsGrant verifies that saving a profile with credential
// and canonical endpoint adds a TrustedEndpoint grant to the credential.
func TestSaveProfileWithGrant_AddsGrant(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// Create credential first
	cred := Credential{
		ID:       "c1",
		Name:     "prod-key",
		Username: "admin",
		Auth:     "publicKey",
	}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	// Save profile with grant
	profile := SSHProfile{
		Base: Base{
			ID:   "p1",
			Type: "ssh",
			Name: "prod",
		},
		Options: SSHProfileOptions{
			Host:         "prod.example.com",
			Port:         22,
			CredentialID: "c1",
		},
	}
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("SaveProfileWithGrant: %v", err)
	}

	// Verify grant was added
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1 credential, got %d", len(creds))
	}
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(creds[0].TrustedEndpoints))
	}
	grant := creds[0].TrustedEndpoints[0]
	if grant.ProfileID != "p1" {
		t.Errorf("grant profileId = %q, want p1", grant.ProfileID)
	}
	if grant.Host != "prod.example.com" {
		t.Errorf("grant host = %q, want prod.example.com", grant.Host)
	}
	if grant.Port != 22 {
		t.Errorf("grant port = %d, want 22", grant.Port)
	}
}

// TestSaveProfileWithGrant_MissingCredentialFails verifies that saving a profile
// with non-existent credential fails.
func TestSaveProfileWithGrant_MissingCredentialFails(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	profile := SSHProfile{
		Base: Base{
			ID:   "p1",
			Type: "ssh",
			Name: "prod",
		},
		Options: SSHProfileOptions{
			Host:         "prod.example.com",
			Port:         22,
			CredentialID: "nonexistent",
		},
	}
	err := store.SaveProfileWithGrant(profile, "prod.example.com", 22)
	if err == nil {
		t.Fatal("want error for non-existent credential, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestSaveProfileWithGrant_EmptyEndpointFails verifies that saving a profile
// with credential but empty endpoint fails.
func TestSaveProfileWithGrant_EmptyEndpointFails(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	profile := SSHProfile{
		Base: Base{
			ID:   "p1",
			Type: "ssh",
			Name: "prod",
		},
		Options: SSHProfileOptions{
			Host:         "prod.example.com",
			Port:         22,
			CredentialID: "c1",
		},
	}
	err := store.SaveProfileWithGrant(profile, "", 0)
	if err == nil {
		t.Fatal("want error for empty endpoint, got nil")
	}
}

// countingDocStore wraps DocumentStore and counts Write calls.
type countingDocStore struct {
	storage.DocumentStore
	writeCount int
}

func (c *countingDocStore) Write(name string, doc any) error {
	c.writeCount++
	return c.DocumentStore.Write(name, doc)
}

// TestSaveProfileWithGrant_Idempotent verifies that saving unchanged profile
// does not trigger write via fake DocumentStore with write counter.
func TestSaveProfileWithGrant_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	realStore := storage.NewDocumentStore(tmpDir)
	countingStore := &countingDocStore{DocumentStore: realStore}
	
	store := NewJSONStoreWithDocStore(countingStore, "profiles.json")

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	profile := SSHProfile{
		Base: Base{
			ID:   "p1",
			Type: "ssh",
			Name: "prod",
		},
		Options: SSHProfileOptions{
			Host:         "prod.example.com",
			Port:         22,
			CredentialID: "c1",
		},
	}

	// First save: should write
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("first save: %v", err)
	}
	firstWriteCount := countingStore.writeCount
	if firstWriteCount == 0 {
		t.Fatal("first save did not write")
	}

	// Second save (unchanged): should NOT write
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if countingStore.writeCount != firstWriteCount {
		t.Errorf("idempotent save triggered write: before=%d, after=%d", 
			firstWriteCount, countingStore.writeCount)
	}

	// Verify data still correct
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Errorf("want 1 grant (idempotent), got %d", len(creds[0].TrustedEndpoints))
	}
}

// TestSaveProfileWithGrant_NoCredentialNoGrant verifies that saving a profile
// without credential does not create grants.
func TestSaveProfileWithGrant_NoCredentialNoGrant(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	// Save profile without credential
	profile := SSHProfile{
		Base: Base{
			ID:   "p1",
			Type: "ssh",
			Name: "prod",
		},
		Options: SSHProfileOptions{
			Host: "prod.example.com",
			Port: 22,
		},
	}
	if err := store.SaveProfileWithGrant(profile, "", 0); err != nil {
		t.Fatalf("SaveProfileWithGrant: %v", err)
	}

	// Verify no grant
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("want 0 grants for no credential, got %d", len(creds[0].TrustedEndpoints))
	}
}

// TestSaveProfileWithGrant_EndpointChangeReplaces verifies that changing endpoint
// for same credential replaces grant (no duplicate).
func TestSaveProfileWithGrant_EndpointChangeReplaces(t *testing.T) {
	tmpDir := t.TempDir()
	realStore := storage.NewDocumentStore(tmpDir)
	countingStore := &countingDocStore{DocumentStore: realStore}
	store := NewJSONStoreWithDocStore(countingStore, "profiles.json")

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "c1",
		},
	}

	// First save with endpoint A
	if err := store.SaveProfileWithGrant(profile, "hostA.example.com", 22); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Verify grant added
	creds, _ := store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(creds[0].TrustedEndpoints))
	}
	if creds[0].TrustedEndpoints[0].Host != "hostA.example.com" {
		t.Errorf("first grant host = %q, want hostA", creds[0].TrustedEndpoints[0].Host)
	}

	// Second save with endpoint B (changed)
	if err := store.SaveProfileWithGrant(profile, "hostB.example.com", 22); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// Verify grant replaced (not duplicated)
	creds, _ = store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant (replaced), got %d", len(creds[0].TrustedEndpoints))
	}
	if creds[0].TrustedEndpoints[0].Host != "hostB.example.com" {
		t.Errorf("second grant host = %q, want hostB", creds[0].TrustedEndpoints[0].Host)
	}
}

// TestSaveProfileWithGrant_CredentialSwitchMovesGrant verifies that changing
// credential from A to B moves grant from A to B.
func TestSaveProfileWithGrant_CredentialSwitchMovesGrant(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	credA := Credential{ID: "cA", Name: "keyA", Username: "u", Auth: "publicKey"}
	credB := Credential{ID: "cB", Name: "keyB", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(credA); err != nil {
		t.Fatalf("SaveCredential A: %v", err)
	}
	if err := store.SaveCredential(credB); err != nil {
		t.Fatalf("SaveCredential B: %v", err)
	}

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "cA",
		},
	}

	// Save with credential A
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("save with A: %v", err)
	}

	// Verify grant on A
	creds, _ := store.LoadCredentials()
	for _, c := range creds {
		if c.ID == "cA" && len(c.TrustedEndpoints) != 1 {
			t.Fatalf("want 1 grant on cA, got %d", len(c.TrustedEndpoints))
		}
		if c.ID == "cB" && len(c.TrustedEndpoints) != 0 {
			t.Fatalf("want 0 grants on cB, got %d", len(c.TrustedEndpoints))
		}
	}

	// Switch to credential B
	profile.Options.CredentialID = "cB"
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("save with B: %v", err)
	}

	// Verify grant moved: A has 0, B has 1
	creds, _ = store.LoadCredentials()
	for _, c := range creds {
		if c.ID == "cA" && len(c.TrustedEndpoints) != 0 {
			t.Errorf("cA should have 0 grants after switch, got %d", len(c.TrustedEndpoints))
		}
		if c.ID == "cB" && len(c.TrustedEndpoints) != 1 {
			t.Errorf("cB should have 1 grant after switch, got %d", len(c.TrustedEndpoints))
		}
	}
}

// TestSaveProfileWithGrant_CredentialRemovalRevokesGrant verifies that removing
// credential from profile revokes grant.
func TestSaveProfileWithGrant_CredentialRemovalRevokesGrant(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "c1",
		},
	}

	// Save with credential
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("save with cred: %v", err)
	}

	// Verify grant exists
	creds, _ := store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(creds[0].TrustedEndpoints))
	}

	// Remove credential from profile
	profile.Options.CredentialID = ""
	if err := store.SaveProfileWithGrant(profile, "", 0); err != nil {
		t.Fatalf("save without cred: %v", err)
	}

	// Verify grant revoked
	creds, _ = store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("want 0 grants after credential removal, got %d", len(creds[0].TrustedEndpoints))
	}
}

// TestSaveProfileWithGrant_NonexistentCredentialNoPersist verifies that attempting
// to save with nonexistent credential does not persist profile or grants.
func TestSaveProfileWithGrant_NonexistentCredentialNoPersist(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// Don't create credential

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "nonexistent",
		},
	}

	// Attempt save with nonexistent credential
	err := store.SaveProfileWithGrant(profile, "prod.example.com", 22)
	if err == nil {
		t.Fatal("want error for nonexistent credential, got nil")
	}

	// Verify profile not persisted
	profiles, _ := store.LoadProfiles()
	if len(profiles) != 0 {
		t.Errorf("profile should not be persisted after failed save, got %d", len(profiles))
	}
}

// failingDocStore wraps DocumentStore and fails Write after N calls.
type failingDocStore struct {
	storage.DocumentStore
	failAfter int
	writeCount int
}

func (f *failingDocStore) Write(name string, doc any) error {
	f.writeCount++
	if f.writeCount > f.failAfter {
		return errors.New("simulated write failure")
	}
	return f.DocumentStore.Write(name, doc)
}

// TestSaveProfileWithGrant_WriteFailureLeavesUnchanged verifies that DocumentStore
// write failure leaves persisted profile+grants unchanged.
func TestSaveProfileWithGrant_WriteFailureLeavesUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	realStore := storage.NewDocumentStore(tmpDir)
	// Fail on second write (first write is credential save)
	failingStore := &failingDocStore{DocumentStore: realStore, failAfter: 1}
	store := NewJSONStoreWithDocStore(failingStore, "profiles.json")

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	// Verify credential persisted
	creds, _ := store.LoadCredentials()
	if len(creds) != 1 {
		t.Fatalf("credential should be persisted")
	}

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "c1",
		},
	}

	// Attempt save - should fail on write
	err := store.SaveProfileWithGrant(profile, "prod.example.com", 22)
	if err == nil {
		t.Fatal("want write error, got nil")
	}

	// Verify credential grants unchanged (should still be 0)
	creds, _ = store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("grants should be unchanged after write failure, got %d", len(creds[0].TrustedEndpoints))
	}

	// Verify profile not persisted
	profiles, _ := store.LoadProfiles()
	if len(profiles) != 0 {
		t.Errorf("profile should not be persisted after write failure, got %d", len(profiles))
	}
}

// TestDeleteProfileWithGrants_RemovesGrants verifies that deleting a profile
// removes all grants for that ProfileID from all credentials.
func TestDeleteProfileWithGrants_RemovesGrants(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	cred := Credential{ID: "c1", Name: "key", Username: "u", Auth: "publicKey"}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "prod"},
		Options: SSHProfileOptions{
			Host: "prod.example.com", Port: 22, CredentialID: "c1",
		},
	}

	// Save profile with grant
	if err := store.SaveProfileWithGrant(profile, "prod.example.com", 22); err != nil {
		t.Fatalf("SaveProfileWithGrant: %v", err)
	}

	// Verify grant exists
	creds, _ := store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant before delete, got %d", len(creds[0].TrustedEndpoints))
	}

	// Delete profile with grants
	if err := store.DeleteProfileWithGrants("p1"); err != nil {
		t.Fatalf("DeleteProfileWithGrants: %v", err)
	}

	// Verify grant removed
	creds, _ = store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("want 0 grants after delete, got %d", len(creds[0].TrustedEndpoints))
	}

	// Verify profile deleted
	profiles, _ := store.LoadProfiles()
	if len(profiles) != 0 {
		t.Errorf("profile should be deleted, got %d", len(profiles))
	}
}

// TestDeleteProfileWithGrants_StaleGrantCleanup verifies that deleting non-existent
// profile still cleans up stale grants (fail-safe).
func TestDeleteProfileWithGrants_StaleGrantCleanup(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// Manually create credential with stale grant (no profile)
	cred := Credential{
		ID: "c1", Name: "key", Username: "u", Auth: "publicKey",
		TrustedEndpoints: []CredentialTrustedEndpoint{
			{ProfileID: "nonexistent", Host: "h", Port: 22},
		},
	}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	// Delete non-existent profile
	if err := store.DeleteProfileWithGrants("nonexistent"); err != nil {
		t.Fatalf("DeleteProfileWithGrants: %v", err)
	}

	// Verify stale grant cleaned up
	creds, _ := store.LoadCredentials()
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("stale grant should be cleaned up, got %d", len(creds[0].TrustedEndpoints))
	}
}

// TestDeleteProfileWithGrants_Idempotent verifies that deleting already-deleted
// profile does not trigger write.
func TestDeleteProfileWithGrants_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	realStore := storage.NewDocumentStore(tmpDir)
	countingStore := &countingDocStore{DocumentStore: realStore}
	store := NewJSONStoreWithDocStore(countingStore, "profiles.json")

	// Delete non-existent profile twice
	if err := store.DeleteProfileWithGrants("p1"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	firstWriteCount := countingStore.writeCount

	if err := store.DeleteProfileWithGrants("p1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}

	// Second delete should not trigger write (no-op)
	if countingStore.writeCount != firstWriteCount {
		t.Errorf("idempotent delete triggered write: before=%d, after=%d",
			firstWriteCount, countingStore.writeCount)
	}
}
