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
	CreateProfile(p SSHProfile) error
	UpdateProfile(p SSHProfile) error
	DeleteProfile(id string) error
}

// GroupRepository is the persistence interface for profile group CRUD.
type GroupRepository interface {
	LoadGroups() ([]ProfileGroup, error)
	CreateGroup(g ProfileGroup) error
	UpdateGroup(g ProfileGroup) error
	DeleteGroup(id string) error
}

// EndpointRepository is the persistence interface for AI endpoint CRUD.
// DeleteEndpoint returns the removed endpoint's credential reference so
// the caller can delete the material itself (ADR-0030).
type EndpointRepository interface {
	LoadEndpoints() ([]Endpoint, error)
	CreateEndpoint(e Endpoint) error
	UpdateEndpoint(e Endpoint) error
	DeleteEndpoint(id string) (string, error)
}

// JSONStore persists profiles, groups and AI endpoints to a single JSON
// file on disk. The file format is:
//
//	{ "profiles": [...], "groups": [...], "endpoints": [...] }
//
// Endpoints live in the same document as profiles on purpose (ADR-0030,
// ADR-0031): the bulk secret-reference sweeps a vault reset performs must
// be one atomic write, and a second document would split them.
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
	Profiles  []SSHProfile   `json:"profiles,omitempty"`
	Groups    []ProfileGroup `json:"groups,omitempty"`
	Endpoints []Endpoint     `json:"endpoints,omitempty"`
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

// LoadAll returns the full document state — all profiles and groups.
// Used by the domain service for atomic import operations, where the
// caller needs a consistent snapshot of the entire store.
func (s *JSONStore) LoadAll() (*storeData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// WriteAll atomically replaces the entire store document. Used by the
// domain service for transactional import: build the new document in
// memory, validate it whole, write once.
func (s *JSONStore) WriteAll(d *storeData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(d)
}

func (s *JSONStore) LoadProfiles() ([]SSHProfile, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	// Profiles already on disk in the old dense shape (written before the
	// presence-aware format) load with every field implicitly "not set" —
	// zero/false values become nil pointers. This is correct behaviour: the
	// old format could not distinguish "explicitly false" from "absent", so
	// inheriting the group default is the right fallback. No migration shim
	// is needed per AGENTS.md — this is a greenfield project.
	for i := range d.Profiles {
		if d.Profiles[i].Options.BehaviorOnSessionEnd != nil {
			d.Profiles[i].BehaviorOnSessionEnd = *d.Profiles[i].Options.BehaviorOnSessionEnd
		}
	}
	return d.Profiles, nil
}

// ErrProfileIDRequired, ErrProfileExists and ErrProfileNotFound make
// create and update distinguishable.
var (
	ErrProfileIDRequired = errors.New("profile ID is required")
	ErrProfileExists     = errors.New("profile already exists")
	ErrProfileNotFound   = errors.New("profile not found")
)

// CreateProfile stores a new profile. It refuses an empty ID and refuses
// to overwrite an existing one.
func (s *JSONStore) CreateProfile(p SSHProfile) error {
	if p.ID == "" {
		return ErrProfileIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range d.Profiles {
		if existing.ID == p.ID {
			return fmt.Errorf("%s: %w", p.ID, ErrProfileExists)
		}
	}
	// Sync BehaviorOnSessionEnd from Base to Options for storage.
	if p.BehaviorOnSessionEnd != "" {
		v := p.BehaviorOnSessionEnd
		p.Options.BehaviorOnSessionEnd = &v
	} else {
		p.Options.BehaviorOnSessionEnd = nil
	}
	d.Profiles = append(d.Profiles, p)
	return s.writeLocked(d)
}

// UpdateProfile replaces a stored profile. It fails if the profile does not
// exist — unlike the old SaveProfile, which silently created one.
func (s *JSONStore) UpdateProfile(p SSHProfile) error {
	if p.ID == "" {
		return ErrProfileIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	// Sync BehaviorOnSessionEnd from Base to Options for storage.
	if p.BehaviorOnSessionEnd != "" {
		v := p.BehaviorOnSessionEnd
		p.Options.BehaviorOnSessionEnd = &v
	} else {
		p.Options.BehaviorOnSessionEnd = nil
	}
	for i, existing := range d.Profiles {
		if existing.ID == p.ID {
			d.Profiles[i] = p
			return s.writeLocked(d)
		}
	}
	return fmt.Errorf("%s: %w", p.ID, ErrProfileNotFound)
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

// ErrGroupIDRequired, ErrGroupExists and ErrGroupNotFound make
// create and update distinguishable.
var (
	ErrGroupIDRequired = errors.New("group ID is required")
	ErrGroupExists     = errors.New("group already exists")
	ErrGroupNotFound   = errors.New("group not found")
)

// CreateGroup stores a new group. It refuses an empty ID and refuses
// to overwrite an existing one. It also validates that the group's defaults
// contain no unknown keys.
func (s *JSONStore) CreateGroup(g ProfileGroup) error {
	if g.ID == "" {
		return ErrGroupIDRequired
	}
	if g.Defaults != nil {
		if err := g.Defaults.Validate(); err != nil {
			return fmt.Errorf("%s: %w", g.ID, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range d.Groups {
		if existing.ID == g.ID {
			return fmt.Errorf("%s: %w", g.ID, ErrGroupExists)
		}
	}
	d.Groups = append(d.Groups, g)
	// Validate the group tree with the new group included.
	if err := ValidateGroupTree(d.Groups); err != nil {
		return err
	}
	return s.writeLocked(d)
}

// UpdateGroup replaces a stored group. It fails if the group does not exist.
// It also validates that the updated defaults contain no unknown keys.
func (s *JSONStore) UpdateGroup(g ProfileGroup) error {
	if g.ID == "" {
		return ErrGroupIDRequired
	}
	if g.Defaults != nil {
		if err := g.Defaults.Validate(); err != nil {
			return fmt.Errorf("%s: %w", g.ID, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for i, existing := range d.Groups {
		if existing.ID == g.ID {
			d.Groups[i] = g
			// Validate the updated group tree.
			if err := ValidateGroupTree(d.Groups); err != nil {
				return err
			}
			return s.writeLocked(d)
		}
	}
	return fmt.Errorf("%s: %w", g.ID, ErrGroupNotFound)
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

// DeleteGroupAtomic removes a group and promotes its children to root
// in a single atomic write. Returns ErrGroupNotFound when the group
// does not exist.
func (s *JSONStore) DeleteGroupAtomic(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}

	found := false
	for i, existing := range d.Groups {
		if existing.ID == id {
			d.Groups = append(d.Groups[:i], d.Groups[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s: %w", id, ErrGroupNotFound)
	}

	// Promote children to root.
	for i := range d.Groups {
		if d.Groups[i].ParentGroupID == id {
			d.Groups[i].ParentGroupID = ""
		}
	}

	// Validate the mutated tree before writing.
	if err := ValidateGroupTree(d.Groups); err != nil {
		return err
	}

	return s.writeLocked(d)
}

// ApplyGroups applies one or more group updates atomically: loads the full
// document, applies every change in memory under a single lock, validates the
// group tree, and writes once. Returns ErrGroupNotFound when any group ID in
// the slice does not exist in the current store.
func (s *JSONStore) ApplyGroups(groups []ProfileGroup) error {
	if len(groups) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}

	// Validate and apply each change. All validation runs under the same lock
	// so the state seen during validation is exactly the state that gets written.
	byID := make(map[string]int, len(d.Groups))
	for i, g := range d.Groups {
		byID[g.ID] = i
	}

	for _, g := range groups {
		if g.ID == "" {
			return ErrGroupIDRequired
		}
		if g.Defaults != nil {
			if err := g.Defaults.Validate(); err != nil {
				return fmt.Errorf("%s: %w", g.ID, err)
			}
		}
		idx, ok := byID[g.ID]
		if !ok {
			return fmt.Errorf("%s: %w", g.ID, ErrGroupNotFound)
		}
		d.Groups[idx] = g
	}

	// Validate the mutated tree before writing.
	if err := ValidateGroupTree(d.Groups); err != nil {
		return err
	}

	return s.writeLocked(d)
}

// ErrEndpointIDRequired, ErrEndpointExists and ErrEndpointNotFound make
// endpoint create and update distinguishable.
var (
	ErrEndpointIDRequired = errors.New("endpoint ID is required")
	ErrEndpointExists     = errors.New("endpoint already exists")
	ErrEndpointNotFound   = errors.New("endpoint not found")
)

func (s *JSONStore) LoadEndpoints() ([]Endpoint, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Endpoints, nil
}

// CreateEndpoint stores a new endpoint. It refuses an empty ID, refuses to
// overwrite an existing one, and validates the record (ValidateEndpoint) —
// the same in-store validation CreateGroup performs.
func (s *JSONStore) CreateEndpoint(e Endpoint) error {
	if e.ID == "" {
		return ErrEndpointIDRequired
	}
	if err := ValidateEndpoint(e); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range d.Endpoints {
		if existing.ID == e.ID {
			return fmt.Errorf("%s: %w", e.ID, ErrEndpointExists)
		}
	}
	d.Endpoints = append(d.Endpoints, e)
	return s.writeLocked(d)
}

// UpdateEndpoint replaces a stored endpoint. It fails if the endpoint does
// not exist — the same create/update split profiles use — and validates
// the record.
func (s *JSONStore) UpdateEndpoint(e Endpoint) error {
	if e.ID == "" {
		return ErrEndpointIDRequired
	}
	if err := ValidateEndpoint(e); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for i, existing := range d.Endpoints {
		if existing.ID == e.ID {
			d.Endpoints[i] = e
			return s.writeLocked(d)
		}
	}
	return fmt.Errorf("%s: %w", e.ID, ErrEndpointNotFound)
}

// DeleteEndpoint removes the endpoint record and, in the SAME write, clears
// its credential reference from every remaining record — profile options,
// group defaults and other endpoints (clearSecretRefLocked). It is the
// metadata-first half of deleting an endpoint's key (ADR-0011 §4,
// ADR-0030): nothing may keep pointing at material that is about to be
// deleted, and separate per-record writes could fail halfway.
//
// It returns the removed endpoint's credential reference so the caller can
// delete the material itself. Idempotent: deleting an absent id reports no
// reference and succeeds, exactly like DeleteProfile.
func (s *JSONStore) DeleteEndpoint(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return "", err
	}
	for i, existing := range d.Endpoints {
		if existing.ID == id {
			ref := existing.CredentialRef
			d.Endpoints = append(d.Endpoints[:i], d.Endpoints[i+1:]...)
			clearSecretRefLocked(d, ref)
			if err := s.writeLocked(d); err != nil {
				return "", err
			}
			return ref, nil
		}
	}
	return "", nil
}

// LoadConnectionSnapshot returns one locked copy of profiles and groups.
func (s *JSONStore) LoadConnectionSnapshot() (ConnectionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return ConnectionSnapshot{}, err
	}
	return ConnectionSnapshot{Profiles: d.Profiles, Groups: d.Groups}, nil
}

// ReplaceConnectionSnapshot validates and replaces profiles and groups in one
// document write, preserving any credential metadata carried by profiles.
func (s *JSONStore) ReplaceConnectionSnapshot(snapshot ConnectionSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateGroupTree(snapshot.Groups); err != nil {
		return err
	}
	groupIDs := make(map[string]struct{}, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		groupIDs[group.ID] = struct{}{}
	}
	for _, p := range snapshot.Profiles {
		if p.Group == "" {
			continue
		}
		if _, ok := groupIDs[p.Group]; !ok {
			return fmt.Errorf("profile %q references unknown group %q", p.ID, p.Group)
		}
	}
	// Defensive: validate every stored forward list through the single authority.
	for _, p := range snapshot.Profiles {
		if p.Options.Forwards != nil && len(*p.Options.Forwards) > 0 {
			if err := ValidForwards(*p.Options.Forwards); err != nil {
				return fmt.Errorf("profile %q: %w", p.ID, err)
			}
		}
	}
	d, err := s.load()
	if err != nil {
		return err
	}
	d.Profiles = snapshot.Profiles
	d.Groups = snapshot.Groups
	return s.writeLocked(d)
}
