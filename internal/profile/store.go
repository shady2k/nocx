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
	// Roles are the role assignments (bead nocx-e6kn2): each role's one
	// (endpoint, model) pair. They share this document with endpoints
	// because a role names an endpoint — the same reason endpoints share
	// it with profiles (ADR-0030) — and a vault reset's reference sweeps
	// stay one atomic write. An assignment may dangle (deleted endpoint,
	// removed model); resolution reports the dangle instead of resolving
	// to a neighbour.
	Roles []RoleAssignment `json:"roles,omitempty"`
	// DefaultModel is the one pair every role WITHOUT its own assignment
	// resolves through (bead nocx-rikz5). It rides this document for the
	// same reason Roles does — it names an endpoint, and DeleteEndpoint
	// clears it in the endpoint's own write. No `omitempty`: encoding/json
	// does not omit an empty struct, so the tag would claim an absence the
	// encoder never produces. The zero value IS "no default", and an absent
	// key decodes to it, so a document written before this field existed
	// reads back as a person who has chosen nothing — which they had not.
	DefaultModel DefaultModel `json:"defaultModel"`
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
// ErrEndpointModelNotFound is the same class one step further in: the
// endpoint is there and does not offer the named model. It is separate from
// ErrEndpointNotFound because the two send a person to different repairs
// ("that endpoint is gone" vs "that endpoint no longer lists that model"),
// and it is deliberately NOT ErrRoleModelGone — that one is resolution
// REPORTING a dangle it found, this one is a write being REFUSED.
var (
	ErrEndpointIDRequired    = errors.New("endpoint ID is required")
	ErrEndpointExists        = errors.New("endpoint already exists")
	ErrEndpointNotFound      = errors.New("endpoint not found")
	ErrEndpointModelNotFound = errors.New("endpoint does not offer this model")
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
			// The default is a single global convenience with nothing to
			// reassign, so it goes with the endpoint it named (bead
			// nocx-rikz5): it must never point at nothing. A per-role
			// ASSIGNMENT deliberately does NOT go: it is a statement about
			// one role that the person made, and they are entitled to be
			// told it broke rather than to find it silently forgotten
			// (role.go's dangle rule, tested at role_test.go:199). This is
			// the SAME write as the removal — a second write could fail in
			// between and leave the state the interval forbids.
			if d.DefaultModel.EndpointID == id {
				d.DefaultModel = DefaultModel{}
			}
			if err := s.writeLocked(d); err != nil {
				return "", err
			}
			return ref, nil
		}
	}
	return "", nil
}

// LoadRoleAssignments returns every stored role assignment (bead
// nocx-e6kn2). Never nil: a store with no assignments returns an empty
// slice — the wire's roles.list still lists every role, null-assigned.
func (s *JSONStore) LoadRoleAssignments() ([]RoleAssignment, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Roles, nil
}

// LoadDefaultModel returns the chosen default, or the zero value when none
// has been chosen (bead nocx-rikz5) — "unset" is a value here, never an
// error, so a caller can tell a person who chose nothing from a store that
// could not answer.
func (s *JSONStore) LoadDefaultModel() (DefaultModel, error) {
	d, err := s.load()
	if err != nil {
		return DefaultModel{}, err
	}
	return d.DefaultModel, nil
}

// SetDefaultModel replaces the default in ONE write. The empty pair clears
// it, returning every role without its own assignment to the visible
// "no model assigned" failure state; a half-set pair is refused
// (ValidateDefaultModel), because it names nothing.
//
// The EXISTENCE checks live HERE, inside the lock, against the one loaded
// document — not in the capability layer above (bead nocx-rikz5). The store
// is what holds the lock, so only the store can make "check the endpoint,
// check the model, write" a single operation; a caller that loads the
// endpoint list, decides, and then calls this leaves a window in which a
// DeleteEndpoint lands between the two and the write stores a default
// naming an endpoint that is gone — the exact state the design forbids
// ("the default must never point at nothing"). This is the same shape
// DeleteEndpoint already has, where the removal and the clearing of a
// default naming it are one write.
//
// The asymmetry with AssignRole is deliberate and unchanged: a per-role
// assignment is a statement about one role, so a dangling one must survive
// to be reported against that role. The default is a global convenience
// every unassigned role inherits silently, so a dangling one breaks all of
// them at once with nothing naming the choice that did it — it is refused
// at the moment it is written. Resolution still refuses at read time: the
// endpoint can be deleted a moment later, and ResolveRole is the
// truth-teller for that.
func (s *JSONStore) SetDefaultModel(m DefaultModel) error {
	if err := ValidateDefaultModel(m); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	if m.IsSet() {
		if err := offersModelLocked(d, m.EndpointID, m.Model); err != nil {
			return err
		}
	}
	d.DefaultModel = m
	return s.writeLocked(d)
}

// offersModelLocked reports whether the loaded document holds an endpoint
// with this id that offers this model, naming which half is missing. The
// caller MUST hold s.mu, and MUST pass the document it is about to write —
// that identity is the whole point of the check living here.
func offersModelLocked(d *storeData, endpointID, model string) error {
	for i := range d.Endpoints {
		if d.Endpoints[i].ID != endpointID {
			continue
		}
		for _, offered := range d.Endpoints[i].Models {
			if offered.Name == model {
				return nil
			}
		}
		return fmt.Errorf("default model: endpoint %s: %q: %w", endpointID, model, ErrEndpointModelNotFound)
	}
	return fmt.Errorf("default model: %s: %w", endpointID, ErrEndpointNotFound)
}

// AssignRole upserts ONE role's assignment (bead nocx-e6kn2): a role has at
// most one (endpoint, model) pair, so a second assignment for the same role
// REPLACES the first in the same single write. A CLEAR write (both fields
// empty) REMOVES the assignment, returning the role to the visible
// "no model assigned" state. Shape-validates the assignment first
// (ValidateRoleAssignment); whether the endpoint and model still exist is
// deliberately not this write's question — resolution answers it, once, so
// a deletion or model-list update can never race a write into a
// validated-but-stale assignment.
func (s *JSONStore) AssignRole(a RoleAssignment) error {
	if err := ValidateRoleAssignment(a); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	// The empty pair is the CLEAR write: remove the role's assignment so
	// the role is unresolvable again — the visible "no model assigned"
	// failure state. Idempotent: clearing an already-clear role writes
	// nothing.
	if a.EndpointID == "" && a.Model == "" {
		for i, existing := range d.Roles {
			if existing.Role == a.Role {
				d.Roles = append(d.Roles[:i], d.Roles[i+1:]...)
				return s.writeLocked(d)
			}
		}
		return nil
	}
	for i, existing := range d.Roles {
		if existing.Role == a.Role {
			d.Roles[i] = a
			return s.writeLocked(d)
		}
	}
	d.Roles = append(d.Roles, a)
	return s.writeLocked(d)
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
