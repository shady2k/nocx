package ssh

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// The installed fact (2026-08-05 delivery-modes design §5.4): backend-owned,
// persisted across restarts, keyed by the RESOLVED destination identity —
// the ssh -G answer for the exact argv (host, port, user, and the
// -F/-o/-J/-l/-p the user typed), never the hostname string. It records the
// protocol version and generation last observed.
//
// It is an INVENTORY, and only that. It is written at domain establishment
// from the generation the far shell names on the authenticated channel
// (internal/transport's recordInstalledFact, nocx-ak2d), and read by
// shell.footprint.status, which lists what nocx wrote and where and offers
// to remove it. Nothing else reads it, and nothing may: the remote command
// is an unconditional bounded loader (2026-08-20 carrier design §4.1), so
// there is no local decision left for a fact to make. §5.4's per-identity
// lookup, and its invalidation when a connection produced no passport, both
// existed to serve the planner that chose between a compact installed line
// and a bootstrap; that planner and that passport are gone, and the two
// methods went with them (nocx-m8jwn.10). The far side owns "is this
// installation valid" now, and answers after the loader has already started.

// InstalledFact is one observed installation: the protocol version, script
// version and generation of the integration bundle committed on the far
// host, as the observation that wrote it reported them.
type InstalledFact struct {
	// Identity is the resolved destination key (IdentityKey). It is stored
	// with the fact so a document can be audited without an external map.
	Identity string `json:"identity"`
	// Protocol is the manifest protocol version, as a string — the wire's
	// canonical spelling ("1"). It is reported by the footprint surface,
	// never compared: nothing local decides anything from this fact.
	Protocol string `json:"protocol"`
	// ScriptVersion is the observed script version, preserved verbatim. The
	// only writer today leaves it EMPTY on purpose — the far shell names a
	// generation, and deriving a version from it is right until the first
	// host whose own manifest was adopted and silently wrong after
	// (internal/transport's resolveAndRecordInstalledFact says why at
	// length). Empty means "not observed".
	ScriptVersion string `json:"scriptVersion"`
	// Generation is the committed generation the far shell named on the
	// authenticated channel (e.g. "v10").
	Generation string `json:"generation"`
	// ObservedAt is when that observation was accepted.
	ObservedAt time.Time `json:"observedAt"`
}

// factDocument is the on-disk envelope. Version 1 is the initial format; a
// document carrying any other version is treated as corrupt — fail-closed
// to "nothing installed" — rather than partially trusted.
type factDocument struct {
	Version int                      `json:"version"`
	Facts   map[string]InstalledFact `json:"facts"`
}

const factDocumentVersion = 1

// InstalledFactStore persists installed facts as one atomic JSON document:
// the inventory shell.footprint.status enumerates.
//
// Fail-closed contract: a missing, corrupt, unreadable or future-versioned
// document reads as "no facts", and a failed write is an error the caller
// logs while the in-memory state stays equal to the durable state. Both
// degrade the same way — the surface lists nothing for that destination,
// never a footprint claim that cannot be proven from disk.
type InstalledFactStore struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu     sync.Mutex
	facts  map[string]InstalledFact
	loaded bool
}

// NewInstalledFactStore creates a store persisting under docName in
// docStore. Callers MUST provide a logger; the store uses it for one-time
// corruption warnings and has no other output path.
func NewInstalledFactStore(logger log.Logger, docStore storage.DocumentStore, docName string) *InstalledFactStore {
	return &InstalledFactStore{
		docStore: docStore,
		docName:  docName,
		log:      logger,
		facts:    make(map[string]InstalledFact),
	}
}

// Record durably persists an observed installation. Only after the write
// succeeds does the in-memory state change, so a failed write leaves All
// omitting the destination — the caller reports the error and the surface
// claims nothing it cannot prove from disk.
func (s *InstalledFactStore) Record(fact InstalledFact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	next := make(map[string]InstalledFact, len(s.facts)+1)
	for k, v := range s.facts {
		next[k] = v
	}
	next[fact.Identity] = fact
	if err := s.writeDoc(next); err != nil {
		return err
	}
	s.facts = next
	return nil
}

// All returns every recorded fact, ordered by identity so the surface that
// enumerates the footprint never depends on Go map iteration order. It is
// the store's only read, and so the only observation seam its tests have.
// Fail-closed: a missing, corrupt or unreadable document is an empty list —
// nothing is claimed installed.
func (s *InstalledFactStore) All() []InstalledFact {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]InstalledFact, 0, len(s.facts))
	for _, f := range s.facts {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// loadLocked reads the document once, on first use. Corruption of any kind
// degrades to an empty store with a one-time warning: never a partially
// trusted fact, never a footprint listed on the strength of a torn file.
func (s *InstalledFactStore) loadLocked() {
	if s.loaded {
		return
	}
	s.facts = make(map[string]InstalledFact)
	var doc factDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("installed-fact store unreadable; treating every host as not installed",
			"document", s.docName, "error", err)
	case found && doc.Version != factDocumentVersion:
		s.log.Warn("installed-fact store has an unknown schema version; treating every host as not installed",
			"document", s.docName, "version", doc.Version)
	case found && doc.Facts != nil:
		s.facts = doc.Facts
	}
	s.loaded = true
}

// writeDoc persists the whole map atomically (DocumentStore.Write: temp
// file, fsync, rename). The map is committed to memory only by the caller
// after this returns nil.
func (s *InstalledFactStore) writeDoc(facts map[string]InstalledFact) error {
	if err := s.docStore.Write(s.docName, factDocument{Version: factDocumentVersion, Facts: facts}); err != nil {
		return fmt.Errorf("installed-fact store: persist %s: %w", s.docName, err)
	}
	return nil
}
