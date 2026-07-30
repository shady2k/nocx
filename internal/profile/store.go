package profile

import (
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
type CredentialMetadataRepository interface {
	LoadCredentials() ([]Credential, error)
	SaveCredential(c Credential) error
	DeleteCredential(id string) error
}

// JSONStore persists profiles and groups to a single JSON file on disk.
// The file format is:
//
//	{ "profiles": [...], "groups": [...] }
type JSONStore struct {
	docStore storage.DocumentStore
	fileName string
	mu       sync.Mutex
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
	Profiles    []SSHProfile   `json:"profiles,omitempty"`
	Groups      []ProfileGroup `json:"groups,omitempty"`
	Credentials []Credential   `json:"credentials,omitempty"`
}

func (s *JSONStore) load() (*storeData, error) {
	var d storeData
	found, err := s.docStore.Read(s.fileName, &d)
	if err != nil {
		return nil, fmt.Errorf("read profile store: %w", err)
	}
	if !found {
		return &storeData{}, nil
	}
	return &d, nil
}

// writeLocked marshals d to JSON and writes it through the DocumentStore.
// The caller MUST hold s.mu.
func (s *JSONStore) writeLocked(d *storeData) error {
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

	d, err := s.load()
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

	d, err := s.load()
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

	d, err := s.load()
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

	d, err := s.load()
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

	d, err := s.load()
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

	d, err := s.load()
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

// ---------------------------------------------------------------------------
// Connection snapshot (ADR-0015)
// ---------------------------------------------------------------------------

// LoadConnectionSnapshot returns an atomic copy of all profiles and groups
// without credential metadata.
func (s *JSONStore) LoadConnectionSnapshot() (ConnectionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return ConnectionSnapshot{}, err
	}
	return ConnectionSnapshot{Profiles: d.Profiles, Groups: d.Groups}, nil
}

// ReplaceConnectionSnapshot atomically replaces profiles and groups in a
// single write, preserving Credential metadata structurally equal.
func (s *JSONStore) ReplaceConnectionSnapshot(snapshot ConnectionSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	d.Profiles = snapshot.Profiles
	d.Groups = snapshot.Groups
	return s.writeLocked(d)
}
