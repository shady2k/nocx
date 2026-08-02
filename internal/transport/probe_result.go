package transport

import (
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Probe result identity — the full key from spec §6
// ---------------------------------------------------------------------------

// ProbeResultIdentity keys a probe result. Every component is load-bearing:
// changing any one makes the answer wrong in a different way, and a partial
// key is a cache that lies.
//
// Key fields (spec §6):
//
//	Endpoint — the resolved dial target (host:port)
//	HostKeyFingerprint — the host public-key fingerprint observed at probe time

// Username — the effective username after all inheritance and ~/.ssh/config
// AuthPolicy — the auth mode string (AuthMode value or "auto")
// Timestamp — when the probe was performed
type ProbeResultIdentity struct {
	Endpoint           string    `json:"endpoint"`
	HostKeyFingerprint string    `json:"hostKeyFingerprint"`
	Username           string    `json:"username"`
	AuthPolicy         string    `json:"authPolicy"`
	Timestamp          time.Time `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// ProbeResultRecord — one stored probe result
// ---------------------------------------------------------------------------

// ProbeResultRecord is one stored probe result. It holds the full identity
// and the outcome.
type ProbeResultRecord struct {
	Identity ProbeResultIdentity `json:"identity"`
	Outcome  ProbeOutcome        `json:"outcome"`
	Detail   string              `json:"detail,omitempty"`

	// Invalidation bookkeeping — not part of the identity, not serialised.
	ProfileID    string    `json:"-"`
	CredentialID string    `json:"-"`
	ConfigMtime  time.Time `json:"-"` // ~/.ssh/config mtime at probe time
	GroupChain   []string  `json:"-"` // ordered group IDs in chain
}

// ---------------------------------------------------------------------------
// Retention, clearing, export/import policy (spec §6)
// ---------------------------------------------------------------------------
//
// Retention:
//
//	Probe results are operational evidence for the **current process
//	lifetime only**. They survive until the process exits or the user
//	explicitly clears them. There is no time-based expiry (TTL) because
//	freshness is determined by identity-key match, not by age: the moment
//	any input dimension changes, the identity changes and the old result
//	is not found by a full-identity lookup. A TTL would eject results
//	that are still valid for their identity — a comment-only ~/.ssh/config
//	edit would purge every result, forcing forty re-probes for zero
//	informational gain.
//
//	This is deliberately NOT profile configuration (contrast with
//	profiles.json, which persists across restarts). Probe results are
//	diagnostic evidence whose meaning expires with the inputs they were
//	derived from — preserving them across restarts would create a window
//	in which a stale result left from before a credential rotation or
//	config edit survives a process relaunch but the inputs that produced
//	it cannot be reconstructed without replaying the exact file state.
//
// Manual clearing:
//
//	ProbeResultStore.Clear() removes every stored result.
//	ProbeResultStore.InvalidateForProfile(profileID) and
//	InvalidateForCredential(credentialID) remove only results touching
//	that profile or credential — scoped clearing for when the user edits
//	a single entity.
//
// Export / import:
//
//	Store.Export() serialises every stored result as a JSON array of
//	ProbeResultRecord. Store.Import() merges records by
//	identity-fingerprint: an incoming record with the same identity
//	replaces the stored entry. Records with empty Endpoint are skipped.
//
//	Export/import is exposed as an API on the store, not as dedicated
//	RPC methods. The rotation wave (wave 8) wires frontend-accessible
//	RPCs if the UI demands live querying; the existing export.backup
//	path feeds the store's Export through a narrow interface if a
//	forensic snapshot is needed. Neither is built here because §6
//	specifies the *behaviour*, not the transport — and the ownership
//	rule (operational evidence, not profile configuration) is satisfied
//	by the documented policy above.
//
// Distinction: "locked" vs "rejected" (brief §41)
//
//	classifyProbeError does NOT map any error to an OutcomeLocked outcome.
//	SSH servers signal account lockout through generic authentication
//	failure — the same error type as a wrong password — so there is no
//	reliable signal to distinguish them. Guessing "locked" means telling
//	someone their account is locked when it is not. If a future server
//	reports lockout through a distinguishable mechanism (e.g. a
//	disconnect message or a specific keyboard-interactive challenge),
//	that maps to its own outcome — until then, a locked account produces
//	OutcomeRejected.

// ---------------------------------------------------------------------------
// ProbeResultStore — in-memory result store
// ---------------------------------------------------------------------------

// ProbeResultStore stores probe results.
// Thread-safe. Not persisted — process lifetime only (see policy above).
//
// Lookup requires the FULL identity including HostKeyFingerprint:
// this is a record store, not a freshness cache. The caller must know
// the fingerprint to find a stored result. Pre-probe cache reuse is
// deliberately unsupported — without the fingerprint, a Lookup could
// return an "accepted" result that is stale because the host key
// changed, directly violating §6.
type ProbeResultStore struct {
	mu      sync.RWMutex
	records []ProbeResultRecord
}

// NewProbeResultStore creates an empty store.
func NewProbeResultStore() *ProbeResultStore {
	return &ProbeResultStore{}
}

// Lookup returns a stored result whose identity matches exactly.
// All identity fields (including HostKeyFingerprint and Timestamp) must
// match. Additionally, the invalidation guards are checked:
//   - ConfigMtime must match (if the stored result has a non-zero one)
//   - GroupChain must match (if the stored result has one)
//
// Returns nil when no matching result exists.
func (s *ProbeResultStore) Lookup(identity ProbeResultIdentity, configMtime time.Time, groupChain []string) *ProbeResultRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, rec := range s.records {
		if !identitiesEqual(rec.Identity, identity) {
			continue
		}
		// Config mtime change → stale.
		if !rec.ConfigMtime.IsZero() && !rec.ConfigMtime.Equal(configMtime) {
			continue
		}
		// Group chain change → stale.
		if rec.GroupChain != nil && !slices.Equal(rec.GroupChain, groupChain) {
			continue
		}
		r := rec
		return &r
	}
	return nil
}

// Store adds or replaces a probe result. A record with a matching identity
// replaces the stored entry.
func (s *ProbeResultStore) Store(rec ProbeResultRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, r := range s.records {
		if identitiesEqual(r.Identity, rec.Identity) {
			s.records[i] = rec
			return
		}
	}
	s.records = append(s.records, rec)
}

// InvalidateForProfile removes every result for the given profile ID.
func (s *ProbeResultStore) InvalidateForProfile(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.records[:0]
	for _, r := range s.records {
		if r.ProfileID != profileID {
			filtered = append(filtered, r)
		}
	}
	s.records = filtered
}

// InvalidateForCredential removes every result for the given credential ID.
func (s *ProbeResultStore) InvalidateForCredential(credentialID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.records[:0]
	for _, r := range s.records {
		if r.CredentialID != credentialID {
			filtered = append(filtered, r)
		}
	}
	s.records = filtered
}

// Clear removes every stored result.
func (s *ProbeResultStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = nil
}

// Len returns the number of stored results.
func (s *ProbeResultStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// List returns a copy of all stored results.
func (s *ProbeResultStore) List() []ProbeResultRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProbeResultRecord, len(s.records))
	copy(out, s.records)
	return out
}

// Export serialises all stored results as indented JSON.
func (s *ProbeResultStore) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s.records, "", "  ")
}

// Import merges records from JSON. Each incoming record is Stored — if an
// existing record has the same identity it is replaced. Records with empty
// Endpoint are silently skipped.
//
// Invalidation guard fields (ConfigMtime, GroupChain) are tagged `json:"-"`,
// so an export-import round trip loses the original invalidation context.
// Import therefore stamps each record with a sentinel mtime and empty group
// chain: Lookup will never match them against any real caller's values,
// preventing stale imported evidence from being treated as fresh.
func (s *ProbeResultStore) Import(data []byte) error {
	var incoming []ProbeResultRecord
	if err := json.Unmarshal(data, &incoming); err != nil {
		return err
	}
	for _, rec := range incoming {
		if rec.Identity.Endpoint == "" {
			continue
		}
		// Sentinels: no real mtime equals Unix epoch+1ns, and no real
		// group chain matches an empty slice. Imported records are
		// forensic snapshots, not reusable cache.
		rec.ConfigMtime = time.Unix(0, 1)
		rec.GroupChain = []string{}
		s.Store(rec)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func identitiesEqual(a, b ProbeResultIdentity) bool {
	return a.Endpoint == b.Endpoint &&
		a.HostKeyFingerprint == b.HostKeyFingerprint &&

		a.Username == b.Username &&
		a.AuthPolicy == b.AuthPolicy &&
		a.Timestamp.Equal(b.Timestamp)
}
