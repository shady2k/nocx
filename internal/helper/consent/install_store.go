package consent

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// Install is one observed helper installation on one machine (consent
// design §3.3's "what is on this machine" — an observation, never an
// authorization). It is recorded only after deploy.Ensure succeeds, so the
// footprint surface can never claim a footprint that was not written
// remotely.
type Install struct {
	// Fingerprint is the consent key — the remote host's public-key
	// fingerprint — so the row ties to the same machine the answer does.
	Fingerprint string `json:"fingerprint"`
	// Identity is the destination identity (user@host:port) the footprint
	// screen displays.
	Identity string `json:"identity"`
	// Path is the versioned install directory on the remote host, e.g.
	// ~/.nocx/helper/1-linux-amd64-<hash>/.
	Path string `json:"path"`
	// Hash is the content hash of the installed binary (D7, D21).
	Hash string `json:"hash"`
	// InstalledAt is when this backend observed the install complete.
	InstalledAt time.Time `json:"installedAt"`
}

// installDocument is the on-disk envelope. Any other version is corrupt —
// fail-closed to "nothing installed".
type installDocument struct {
	Version  int       `json:"version"`
	Installs []Install `json:"installs"`
}

const installDocumentVersion = 1

// InstallStore persists observed helper installations as one atomic JSON
// document — the memory that lets the footprint surface list the helper
// footprint without connecting. Same fail-closed contract as Store: a
// missing, corrupt, unreadable or future-versioned document lists nothing.
type InstallStore struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu       sync.Mutex
	installs map[string]Install // keyed by fingerprint+path: one row per install location
	loaded   bool
}

// installKey names one install location: the machine (host-key fingerprint)
// and the install directory on it. One machine may carry a helper footprint
// in more than one home directory (consent design §3.2 — consent is per
// machine, not per account), so the fingerprint alone cannot name a row;
// fingerprint+path can, and the row still ties to the machine's answer.
func installKey(in Install) string {
	return in.Fingerprint + "\x00" + in.Path
}

// NewInstallStore creates a store persisting under docName in docStore.
// Callers MUST provide a logger; the store uses it for one-time corruption
// warnings and has no other output path.
func NewInstallStore(logger log.Logger, docStore storage.DocumentStore, docName string) *InstallStore {
	return &InstallStore{
		docStore: docStore,
		docName:  docName,
		log:      logger,
		installs: make(map[string]Install),
	}
}

// Record durably persists an observed installation. An empty fingerprint is
// refused — an observation with no machine key would list under nothing.
// Only after the write succeeds does the in-memory state change, so a
// failed write leaves the listing as it was and the next start reads the
// durable truth.
func (s *InstallStore) Record(install Install) error {
	if install.Fingerprint == "" {
		return fmt.Errorf("helper-install store: refusing to record an install with an empty host-key fingerprint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	next := make(map[string]Install, len(s.installs)+1)
	for k, v := range s.installs {
		next[k] = v
	}
	next[installKey(install)] = install
	if err := s.writeDoc(next); err != nil {
		return err
	}
	s.installs = next
	return nil
}

// All returns every recorded install, ordered by identity so the footprint
// surface never depends on Go map iteration order. Same fail-closed reading
// as the rest of the store: a missing or corrupt document is an empty list.
func (s *InstallStore) All() []Install {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]Install, 0, len(s.installs))
	for _, in := range s.installs {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

func (s *InstallStore) loadLocked() {
	if s.loaded {
		return
	}
	s.installs = make(map[string]Install)
	var doc installDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("helper-install store unreadable; listing nothing as installed",
			"document", s.docName, "error", err)
	case found && doc.Version != installDocumentVersion:
		s.log.Warn("helper-install store has an unknown schema version; listing nothing as installed",
			"document", s.docName, "version", doc.Version)
	case found && doc.Installs != nil:
		for _, in := range doc.Installs {
			s.installs[installKey(in)] = in
		}
	}
	s.loaded = true
}

// writeDoc persists the whole set atomically (DocumentStore.Write: temp
// file, fsync, rename). The set is committed to memory only by the caller
// after this returns nil.
func (s *InstallStore) writeDoc(installs map[string]Install) error {
	list := make([]Install, 0, len(installs))
	for _, in := range installs {
		list = append(list, in)
	}
	if err := s.docStore.Write(s.docName, installDocument{Version: installDocumentVersion, Installs: list}); err != nil {
		return fmt.Errorf("helper-install store: persist %s: %w", s.docName, err)
	}
	return nil
}
