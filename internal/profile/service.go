package profile

import "fmt"

// ImportResult
// ---------------------------------------------------------------------------

// ImportResult reports the outcome of an import operation.
type ImportResult struct {
	ProfilesImported int      `json:"profilesImported"`
	GroupsImported   int      `json:"groupsImported"`
	ImportErrors     []string `json:"importErrors,omitempty"`
}

// ---------------------------------------------------------------------------
// ProfileService — single write path for profiles and groups
// ---------------------------------------------------------------------------

// ProfileService provides a single write path for profiles and groups that
// every writer goes through: ordinary CRUD, the Tabby importer, the
// configuration importer. Validation lives here once, not once per caller.
type ProfileService struct {
	store *JSONStore
}

// NewProfileService creates a ProfileService backed by the given store.
func NewProfileService(store *JSONStore) *ProfileService {
	return &ProfileService{store: store}
}

// ---------------------------------------------------------------------------
// CRUD with validation
// ---------------------------------------------------------------------------

// SaveProfile creates or updates a profile.
func (s *ProfileService) SaveProfile(p SSHProfile) error {
	if p.ID == "" {
		return ErrProfileIDRequired
	}
	if p.Options.Host == "" {
		return fmt.Errorf("%s: host is required", p.ID)
	}

	// Sync BehaviorOnSessionEnd from Base to Options for storage.
	if p.BehaviorOnSessionEnd != "" {
		v := p.BehaviorOnSessionEnd
		p.Options.BehaviorOnSessionEnd = &v
	} else {
		p.Options.BehaviorOnSessionEnd = nil
	}

	// Load current state.
	storeData, err := s.store.LoadAll()
	if err != nil {
		return fmt.Errorf("load store: %w", err)
	}

	// Check if this is a create or update.
	for i, existing := range storeData.Profiles {
		if existing.ID == p.ID {
			storeData.Profiles[i] = p
			return s.store.WriteAll(storeData)
		}
	}

	storeData.Profiles = append(storeData.Profiles, p)
	return s.store.WriteAll(storeData)
}

// SaveGroup creates or updates a group. Validates unknown default keys
// and group tree integrity after the write.
func (s *ProfileService) SaveGroup(g ProfileGroup) error {
	if g.ID == "" {
		return ErrGroupIDRequired
	}

	// Validate group defaults if present.
	if g.Defaults != nil {
		if err := g.Defaults.Validate(); err != nil {
			return fmt.Errorf("%s: %w", g.ID, err)
		}
	}

	storeData, err := s.store.LoadAll()
	if err != nil {
		return fmt.Errorf("load store: %w", err)
	}

	for i, existing := range storeData.Groups {
		if existing.ID == g.ID {
			storeData.Groups[i] = g
			// Validate group tree after the change.
			if err := ValidateGroupTree(storeData.Groups); err != nil {
				return fmt.Errorf("group tree invalid after save: %w", err)
			}
			return s.store.WriteAll(storeData)
		}
	}

	storeData.Groups = append(storeData.Groups, g)
	if err := ValidateGroupTree(storeData.Groups); err != nil {
		return fmt.Errorf("group tree invalid after save: %w", err)
	}
	return s.store.WriteAll(storeData)
}

// ---------------------------------------------------------------------------
// Atomic import — build new document in memory, validate whole, write once
// ---------------------------------------------------------------------------

// AtomicImport merges profiles and groups into the store atomically. On any
// validation failure the store is unchanged. The import:
//
//  1. Loads current store state.
//  2. Merges profiles (overwrite on duplicate ID).
//  3. Merges groups (overwrite on duplicate ID, validates group tree).
//  4. Validates the full result.
//  5. Writes once.
func (s *ProfileService) AtomicImport(profiles []SSHProfile, groups []ProfileGroup) *ImportResult {
	data, err := s.prepareImport(profiles, groups)
	if err != nil {
		return &ImportResult{ImportErrors: []string{err.Error()}}
	}
	if err := s.store.WriteAll(data); err != nil {
		return &ImportResult{ImportErrors: []string{fmt.Sprintf("write store: %v", err)}}
	}
	return &ImportResult{ProfilesImported: len(profiles), GroupsImported: len(groups)}
}

// ValidateImport checks the complete merge without writing. Importers call it
// before minting secrets; AtomicImport repeats it against the current store.
func (s *ProfileService) ValidateImport(profiles []SSHProfile, groups []ProfileGroup) error {
	_, err := s.prepareImport(profiles, groups)
	return err
}

func (s *ProfileService) prepareImport(profiles []SSHProfile, groups []ProfileGroup) (*storeData, error) {
	data, err := s.store.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("load store: %w", err)
	}
	profileIDs := make(map[string]int, len(data.Profiles))
	for i, p := range data.Profiles {
		profileIDs[p.ID] = i
	}
	seenProfiles := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		if p.ID == "" {
			return nil, ErrProfileIDRequired
		}
		if seenProfiles[p.ID] {
			return nil, fmt.Errorf("duplicate imported profile ID: %s", p.ID)
		}
		seenProfiles[p.ID] = true
		if p.Options.Host == "" {
			return nil, fmt.Errorf("%s: host is required", p.ID)
		}
		if p.BehaviorOnSessionEnd != "" {
			v := p.BehaviorOnSessionEnd
			p.Options.BehaviorOnSessionEnd = &v
		} else {
			p.Options.BehaviorOnSessionEnd = nil
		}
		if i, exists := profileIDs[p.ID]; exists {
			data.Profiles[i] = p
		} else {
			data.Profiles = append(data.Profiles, p)
		}
	}
	groupIDs := make(map[string]int, len(data.Groups))
	for i, g := range data.Groups {
		groupIDs[g.ID] = i
	}
	seenGroups := make(map[string]bool, len(groups))
	for _, g := range groups {
		if g.ID == "" {
			return nil, ErrGroupIDRequired
		}
		if seenGroups[g.ID] {
			return nil, fmt.Errorf("duplicate imported group ID: %s", g.ID)
		}
		seenGroups[g.ID] = true
		if g.Defaults != nil {
			if err := g.Defaults.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", g.ID, err)
			}
		}
		if i, exists := groupIDs[g.ID]; exists {
			data.Groups[i] = g
		} else {
			data.Groups = append(data.Groups, g)
		}
	}
	if err := ValidateGroupTree(data.Groups); err != nil {
		return nil, fmt.Errorf("group tree: %w", err)
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Snapshot + AtomicReplace — whole-store capture and restore
// ---------------------------------------------------------------------------

// ConfigSnapshot is the full profiles-and-groups state of the store,
// captured by Snapshot and written back by AtomicReplace.
type ConfigSnapshot struct {
	Profiles []SSHProfile
	Groups   []ProfileGroup
}

// Snapshot returns the store's entire current state. It is the read side of
// the export restore operation's rollback: the state captured here is what
// AtomicReplace writes back when an import fails after its commit point.
// The snapshot is a fresh read from the store; mutating it does not affect
// the store.
func (s *ProfileService) Snapshot() (ConfigSnapshot, error) {
	data, err := s.store.LoadAll()
	if err != nil {
		return ConfigSnapshot{}, fmt.Errorf("snapshot store: %w", err)
	}
	return ConfigSnapshot{
		Profiles: data.Profiles,
		Groups:   data.Groups,
	}, nil
}

// AtomicReplace validates the given state and, when valid, writes it as the
// ENTIRE store in one atomic write — the inverse of AtomicImport's merge.
// On any validation failure the store is unchanged.
//
// This is the rollback counterpart the export restore operation needs: a
// merged import cannot be undone with more merges, because a profile or
// group the import added survives every subsequent merge. Nothing outside
// the restore operation may call it.
func (s *ProfileService) AtomicReplace(snap ConfigSnapshot) error {
	d := &storeData{
		Profiles: make([]SSHProfile, len(snap.Profiles)),
		Groups:   make([]ProfileGroup, len(snap.Groups)),
	}
	copy(d.Profiles, snap.Profiles)
	copy(d.Groups, snap.Groups)

	for i := range d.Profiles {
		p := &d.Profiles[i]
		if p.ID == "" {
			return fmt.Errorf("profile with empty ID cannot be restored")
		}
		if p.Options.Host == "" {
			return fmt.Errorf("%s: host is required", p.ID)
		}
		// Sync BehaviorOnSessionEnd from Base to Options for storage,
		// exactly as SaveProfile and AtomicImport do.
		if p.BehaviorOnSessionEnd != "" {
			v := p.BehaviorOnSessionEnd
			p.Options.BehaviorOnSessionEnd = &v
		} else {
			p.Options.BehaviorOnSessionEnd = nil
		}
	}
	for _, g := range d.Groups {
		if g.ID == "" {
			return fmt.Errorf("group with empty ID cannot be restored")
		}
		if g.Defaults != nil {
			if err := g.Defaults.Validate(); err != nil {
				return fmt.Errorf("%s: %w", g.ID, err)
			}
		}
	}
	if err := ValidateGroupTree(d.Groups); err != nil {
		return fmt.Errorf("group tree invalid: %w", err)
	}
	return s.store.WriteAll(d)
}
