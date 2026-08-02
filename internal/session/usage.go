package session

import (
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

const usageDocName = "profile-usage.json"

// DocumentUsageStore implements ProfileUsageTracker backed by a
// storage.DocumentStore. Last-used timestamps survive restarts.
// Thread-safe: lazy-loads from the document store on first mutation
// or query, then flushes on every mutation.
type DocumentUsageStore struct {
	mu     sync.Mutex
	store  storage.DocumentStore
	data   map[string]time.Time
	loaded bool
}

// NewDocumentUsageStore creates a ProfileUsageTracker that persists
// last-used timestamps in the given DocumentStore under "profile-usage.json".
func NewDocumentUsageStore(docStore storage.DocumentStore) *DocumentUsageStore {
	return &DocumentUsageStore{
		store: docStore,
	}
}

// ensureLoaded loads persisted data on first access. Idempotent: does
// nothing after the first call or when the document does not exist.
func (s *DocumentUsageStore) ensureLoaded() error {
	if s.loaded {
		return nil
	}

	var raw map[string]time.Time
	found, err := s.store.Read(usageDocName, &raw)
	if err != nil {
		return err
	}
	if found {
		s.data = raw
	} else {
		s.data = make(map[string]time.Time)
	}
	s.loaded = true
	return nil
}

// save flushes the in-memory data to the document store.
func (s *DocumentUsageStore) save() error {
	return s.store.Write(usageDocName, s.data)
}

// SessionOpened records that a session was opened for profileID.
func (s *DocumentUsageStore) SessionOpened(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.ensureLoaded()
	s.data[profileID] = time.Now()

	// Best-effort flush — persistence failures are logged but never
	// block session creation. The in-memory copy is authoritative for
	// the session's lifetime.
	_ = s.save()
}

// SessionClosed records that a session for profileID ended.
func (s *DocumentUsageStore) SessionClosed(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.ensureLoaded()
	s.data[profileID] = time.Now()

	_ = s.save()
}

// LastUsedForProfiles returns the last-used time for each requested
// profile ID. Profiles with no recorded usage are absent from the map.
func (s *DocumentUsageStore) LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}

	result := make(map[string]time.Time, len(profileIDs))
	for _, pid := range profileIDs {
		if t, ok := s.data[pid]; ok {
			result[pid] = t
		}
	}
	return result, nil
}
