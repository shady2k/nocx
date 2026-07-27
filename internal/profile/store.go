package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/shady2k/nocx/internal/storage"
)

// ProfileRepository is the persistence interface for SSH profile CRUD.
type ProfileRepository interface {
	LoadProfiles() ([]SSHProfile, error)
	SaveProfile(p SSHProfile) error
	DeleteProfile(id string) error
}

// GroupRepository is the persistence interface for profile group CRUD.
type GroupRepository interface {
	LoadGroups() ([]ProfileGroup, error)
	SaveGroup(g ProfileGroup) error
	DeleteGroup(id string) error
}

// CredentialMetadataRepository is the persistence interface for credential
// metadata CRUD. Secrets referenced by SecretID fields are managed by the
// credential.SecretStore, not by this repository (ADR-0011 §2).
// Used by resolver, export, import — read-only or full-replacement paths.
type CredentialMetadataRepository interface {
	LoadCredentials() ([]Credential, error)
	SaveCredential(c Credential) error
	DeleteCredential(id string) error
}

// CredentialMetadataMutator provides atomic patch operations for backend-owned
// credential fields (identity, secret references). Used by transport handlers
// for RPC mutations that must preserve TrustedEndpoints grants.
// ADR-0013: renderer cannot submit or overwrite backend-owned fields.
type CredentialMetadataMutator interface {
	UpdateCredentialIdentity(dto CredentialUpdateDTO) error
	SetCredentialSecretID(id string, secretID string) (old string, err error)
	ClearCredentialSecretID(id string) (old string, err error)
	SetCredentialPassphraseSecretID(id string, passphraseSecretID string) (old string, err error)
	ClearCredentialPassphraseSecretID(id string) (old string, err error)
}

// JSONStore persists profiles and groups to a single JSON file on disk.
// The file format is:
//
//	{ "profiles": [...], "groups": [...] }
type JSONStore struct {
	docStore storage.DocumentStore
	fileName string
	mu       sync.Mutex
	resolver EndpointResolver // For ADR-0013 migration; nil means mark all as requiresReview
}

// NewJSONStore creates a JSONStore rooted at path (convenience constructor
// used by tests and simple wiring). The path's directory component becomes
// the DocumentStore root; the file component is the document name.
func NewJSONStore(path string) *JSONStore {
	return &JSONStore{
		docStore: storage.NewDocumentStore(filepath.Dir(path)),
		fileName: filepath.Base(path),
	}
}

// NewJSONStoreWithDocStore creates a JSONStore that reads and writes the
// named document through the given DocumentStore. Prefer this constructor
// when the DocumentStore is shared across multiple modules (composition-root
// wiring per AD-8).
func NewJSONStoreWithDocStore(docStore storage.DocumentStore, fileName string) *JSONStore {
	return &JSONStore{docStore: docStore, fileName: fileName}
}

type storeData struct {
	SchemaVersion int            `json:"schemaVersion"`
	Profiles      []SSHProfile   `json:"profiles,omitempty"`
	Groups        []ProfileGroup `json:"groups,omitempty"`
	Credentials   []Credential   `json:"credentials,omitempty"`
}


// load reads and optionally migrates the profile document.
// Thread-safe: acquires mutex for migration writes.
func (s *JSONStore) load() (*storeData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *JSONStore) loadLocked() (*storeData, error) {
	// Read raw JSON for migration
	var raw json.RawMessage
	found, err := s.docStore.Read(s.fileName, &raw)
	if err != nil {
		return nil, fmt.Errorf("read profile store: %w", err)
	}
	if !found {
		return &storeData{SchemaVersion: int(profileSchemaVersion)}, nil
	}

	// Extract stored version (default 0 if absent)
	var versionCheck struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &versionCheck); err != nil {
		// If we can't read version, treat as v0
		versionCheck.SchemaVersion = 0
	}

	// Check schema version
	storedVersion := storage.SchemaVersion(versionCheck.SchemaVersion)
	if storedVersion > profileSchemaVersion {
		return nil, fmt.Errorf("profile document version %d is newer than supported %d", 
			storedVersion, profileSchemaVersion)
	}
	
	// Check if migration is needed
	if storedVersion < profileSchemaVersion {
		// Apply migration (using per-store resolver)
		profMod := newProfileModule(s.resolver)
		migrated, err := profMod.Migrate(raw, storage.SchemaVersion(versionCheck.SchemaVersion))
		if err != nil {
			return nil, fmt.Errorf("migrate profile document: %w", err)
		}

		// Write migrated document back to disk atomically
		// If write fails, load still returns error (legacy document remains on disk)
		if err := s.docStore.Write(s.fileName, json.RawMessage(migrated)); err != nil {
			return nil, fmt.Errorf("write migrated document: %w", err)
		}

		// Unmarshal migrated data
		var d storeData
		if err := json.Unmarshal(migrated, &d); err != nil {
			return nil, fmt.Errorf("unmarshal migrated document: %w", err)
		}
		return &d, nil
	}

	// No migration needed, unmarshal current data
	var d storeData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("unmarshal profile document: %w", err)
	}
	return &d, nil
}

// writeLocked marshals d to JSON and writes it through the DocumentStore.
// The caller MUST hold s.mu. Ensures schemaVersion is set to current.
func (s *JSONStore) writeLocked(d *storeData) error {
	d.SchemaVersion = int(profileSchemaVersion)
	return s.docStore.Write(s.fileName, d)
}

func (s *JSONStore) LoadProfiles() ([]SSHProfile, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Profiles, nil
}

func (s *JSONStore) SaveProfile(p SSHProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, existing := range d.Profiles {
		if existing.ID == p.ID {
			d.Profiles[i] = p
			return s.writeLocked(d)
		}
	}
	d.Profiles = append(d.Profiles, p)
	return s.writeLocked(d)
}

func (s *JSONStore) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, existing := range d.Profiles {
		if existing.ID == id {
			d.Profiles = append(d.Profiles[:i], d.Profiles[i+1:]...)
			return s.writeLocked(d)
		}
	}
	return nil
}

func (s *JSONStore) LoadGroups() ([]ProfileGroup, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Groups, nil
}

func (s *JSONStore) SaveGroup(g ProfileGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, existing := range d.Groups {
		if existing.ID == g.ID {
			d.Groups[i] = g
			return s.writeLocked(d)
		}
	}
	d.Groups = append(d.Groups, g)
	return s.writeLocked(d)
}

func (s *JSONStore) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, existing := range d.Groups {
		if existing.ID == id {
			d.Groups = append(d.Groups[:i], d.Groups[i+1:]...)
			return s.writeLocked(d)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Credential CRUD
// ---------------------------------------------------------------------------

func (s *JSONStore) LoadCredentials() ([]Credential, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Credentials, nil
}

func (s *JSONStore) SaveCredential(c Credential) error {
	if c.ID == "" {
		return errors.New("credential ID is required")
	}
	if c.Name == "" {
		return errors.New("credential name is required")
	}
	if c.Username == "" {
		return errors.New("credential username is required")
	}
	// Host binding, enforced here rather than in the transport handler: this is
	// the one path every writer passes through, so a future caller cannot route
	// around it (nocx-wd2m).
	if err := c.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}

	// Update existing or append new.
	for i, existing := range d.Credentials {
		if existing.ID == c.ID {
			d.Credentials[i] = c
			return s.writeLocked(d)
		}
	}
	d.Credentials = append(d.Credentials, c)
	return s.writeLocked(d)
}

func (s *JSONStore) DeleteCredential(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, existing := range d.Credentials {
		if existing.ID == id {
			d.Credentials = append(d.Credentials[:i], d.Credentials[i+1:]...)
			return s.writeLocked(d)
		}
	}
	return nil
}

// UpdateCredentialIdentity updates only the renderer-owned identity fields
// of a credential while preserving backend-owned fields (TrustedEndpoints,
// SecretID, PassphraseSecretID). This is an atomic operation under JSONStore.mu
// to prevent lost-update races with profile/grant saves.
// ADR-0013: the renderer cannot submit or overwrite grants or secret references.
func (s *JSONStore) UpdateCredentialIdentity(dto CredentialUpdateDTO) error {
	if dto.ID == "" {
		return errors.New("credential ID is required")
	}
	if dto.Name == "" {
		return errors.New("credential name is required")
	}
	if dto.Username == "" {
		return errors.New("credential username is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}

	// Find existing credential
	found := false
	for i := range d.Credentials {
		if d.Credentials[i].ID == dto.ID {
			// Update only renderer-owned identity fields
			d.Credentials[i].Name = dto.Name
			d.Credentials[i].Username = dto.Username
			d.Credentials[i].Auth = dto.Auth
			d.Credentials[i].KeyPath = dto.KeyPath
			// Preserve backend-owned fields:
			// - TrustedEndpoints (grants) - unchanged
			// - SecretID - unchanged
			// - PassphraseSecretID - unchanged
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("credential %s not found", dto.ID)
	}

	return s.writeLocked(d)
}

// SetCredentialSecretID atomically updates a credential's SecretID while
// preserving all other fields. Returns the previous SecretID for safe deletion.
func (s *JSONStore) SetCredentialSecretID(id string, secretID string) (old string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return "", err
	}

	for i := range d.Credentials {
		if d.Credentials[i].ID == id {
			oldID := d.Credentials[i].SecretID
			d.Credentials[i].SecretID = secretID
			if err := s.writeLocked(d); err != nil {
				return "", err
			}
			return oldID, nil
		}
	}
	return "", fmt.Errorf("credential %s not found", id)
}

// ClearCredentialSecretID atomically clears a credential's SecretID and
// returns the previous value for safe deletion.
func (s *JSONStore) ClearCredentialSecretID(id string) (old string, err error) {
	return s.SetCredentialSecretID(id, "")
}

// SetCredentialPassphraseSecretID atomically updates a credential's
// PassphraseSecretID and returns the previous value.
func (s *JSONStore) SetCredentialPassphraseSecretID(id string, passphraseSecretID string) (old string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return "", err
	}

	for i := range d.Credentials {
		if d.Credentials[i].ID == id {
			oldID := d.Credentials[i].PassphraseSecretID
			d.Credentials[i].PassphraseSecretID = passphraseSecretID
			if err := s.writeLocked(d); err != nil {
				return "", err
			}
			return oldID, nil
		}
	}
	return "", fmt.Errorf("credential %s not found", id)
}

// ClearCredentialPassphraseSecretID atomically clears a credential's
// PassphraseSecretID and returns the previous value.
func (s *JSONStore) ClearCredentialPassphraseSecretID(id string) (old string, err error) {
	return s.SetCredentialPassphraseSecretID(id, "")
}

// SetEndpointResolver sets the canonical endpoint resolver for ADR-0013 migration.
// Must be called before any load/save operations if migration of legacy bindings is needed.
// If not set, all legacy bindings will be marked as requiresReview.
func (s *JSONStore) SetEndpointResolver(resolver EndpointResolver) {
	s.resolver = resolver
}


// findCredentialLocked finds a credential by ID. Returns nil if not found.
// Caller must hold s.mu.
func (s *JSONStore) findCredentialLocked(d *storeData, id string) *Credential {
	for i := range d.Credentials {
		if d.Credentials[i].ID == id {
			return &d.Credentials[i]
		}
	}
	return nil
}

// removeAllGrantsForProfileLocked removes all grants for a specific profile from all credentials.
// Caller must hold s.mu.
func (s *JSONStore) removeAllGrantsForProfileLocked(d *storeData, profileID string) {
	for i := range d.Credentials {
		newGrants := make([]CredentialTrustedEndpoint, 0, len(d.Credentials[i].TrustedEndpoints))
		for _, g := range d.Credentials[i].TrustedEndpoints {
			if g.ProfileID != profileID {
				newGrants = append(newGrants, g)
			}
		}
		d.Credentials[i].TrustedEndpoints = newGrants
	}
}

// upsertProfileLocked updates existing profile or appends new one.
// Caller must hold s.mu.
func (s *JSONStore) upsertProfileLocked(d *storeData, profile SSHProfile) {
	for i := range d.Profiles {
		if d.Profiles[i].ID == profile.ID {
			d.Profiles[i] = profile
			return
		}
	}
	d.Profiles = append(d.Profiles, profile)
}

// SaveProfileWithGrant atomically saves a profile and manages its credential grant.
// ADR-0013 §2: saving a connection is the only automatic grant operation.
//
// Behavior:
// - credentialID derived from profile.Options.CredentialID
// - if credential assigned, canonical endpoint required (error if missing)
// - removes all grants for this ProfileID from ALL credentials
// - adds exactly one canonical grant to target credential (error if not found)
// - clears requiresReview on profile
// - idempotent: deep-equal before/after snapshot, skip write if unchanged
// - if persistence fails, neither profile nor grants are modified
func (s *JSONStore) SaveProfileWithGrant(
	profile SSHProfile,
	canonicalHost string,
	canonicalPort uint16,
) error {
	if profile.ID == "" {
		return errors.New("profile ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}

	// Derive credentialID from profile
	credentialID := profile.Options.CredentialID

	// Validate: if credential is assigned, endpoint must be valid
	if credentialID != "" {
		if canonicalHost == "" || canonicalPort == 0 {
			return errors.New("canonical endpoint required when credential is assigned")
		}
	}

	// Create immutable before snapshot via JSON round-trip (deep copy)
	beforeJSON, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("snapshot before state: %w", err)
	}
	var before storeData
	if err := json.Unmarshal(beforeJSON, &before); err != nil {
		return fmt.Errorf("copy before state: %w", err)
	}

	// Remove all grants for this ProfileID from ALL credentials
	s.removeAllGrantsForProfileLocked(d, profile.ID)

	// If credential is assigned, require it exists and add grant
	if credentialID != "" {
		cred := s.findCredentialLocked(d, credentialID)
		if cred == nil {
			return fmt.Errorf("credential %s not found", credentialID)
		}
		// Add canonical grant
		cred.TrustedEndpoints = append(cred.TrustedEndpoints, CredentialTrustedEndpoint{
			ProfileID: profile.ID,
			Host:      canonicalHost,
			Port:      canonicalPort,
		})
	}

	// Clear requiresReview (profile is now saved with valid grant or no credential)
	profile.Options.RequiresReview = false

	// Save profile (upsert)
	s.upsertProfileLocked(d, profile)

	// Idempotent check: deep-equal before vs after via JSON
	afterJSON, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("snapshot after state: %w", err)
	}
	
	// Compare JSON bytes (deterministic marshal ensures correct comparison)
	if string(beforeJSON) == string(afterJSON) {
		return nil // No changes, skip write
	}

	return s.writeLocked(d)
}


// DeleteProfileWithGrants atomically deletes a profile and removes all its grants.
// ADR-0013 §2: deleting a profile removes its grants from all credentials.
//
// Behavior:
// - always removes grants for this ProfileID from ALL credentials (fail-safe cleanup)
// - deletes the profile if it exists
// - idempotent: skip write only if neither profile nor grants changed
// - if persistence fails, neither profile nor grants are modified
// - stale grants are cleaned up even if profile is missing (prevents resurrection)
func (s *JSONStore) DeleteProfileWithGrants(profileID string) error {
	if profileID == "" {
		return errors.New("profile ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.loadLocked()
	if err != nil {
		return err
	}

	// Create immutable before snapshot via JSON round-trip
	beforeJSON, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("snapshot before state: %w", err)
	}

	// Always remove all grants for this ProfileID from ALL credentials (fail-safe)
	s.removeAllGrantsForProfileLocked(d, profileID)

	// Delete the profile if it exists
	for i := range d.Profiles {
		if d.Profiles[i].ID == profileID {
			d.Profiles = append(d.Profiles[:i], d.Profiles[i+1:]...)
			break
		}
	}

	// Idempotent check: compare before vs after
	afterJSON, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("snapshot after state: %w", err)
	}
	if string(beforeJSON) == string(afterJSON) {
		return nil // No changes (no profile and no grants), skip write
	}

	return s.writeLocked(d)
}

// ProfileAtomicMutator provides atomic profile save/delete operations that manage
// credential grants. Used by transport handlers for RPC mutations that must
// maintain TrustedEndpoints consistency.
// ADR-0013: saving a connection is the only automatic grant operation.
type ProfileAtomicMutator interface {
	SaveProfileWithGrant(profile SSHProfile, canonicalHost string, canonicalPort uint16) error
	DeleteProfileWithGrants(profileID string) error
}

// EndpointResolver returns the currently set endpoint resolver, or nil if not set.
// ADR-0013: used by connection resolver for grant checks.
func (s *JSONStore) EndpointResolver() EndpointResolver {
	return s.resolver
}
