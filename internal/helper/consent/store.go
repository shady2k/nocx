// Package consent is the per-machine relay-tier consent for the remote
// helper (remote-helper design D8; the 2026-08-10 footprint-consent design
// §3.2, §3.3, §5.3).
//
// One machine, one answer: the answer is keyed by the remote host's public
// key — the fingerprint observed and verified when the connection was
// dialed — never the hostname, the profile or the route. The same machine
// reached directly or through a bastion is one answer; two machines that
// spell themselves the same are two; a rotated host key asks again.
//
// Two stores, two questions (§3.3): Store answers what the user permitted;
// InstallStore answers what is installed. Neither is derived from the
// other, and file presence never implies consent.
package consent

import (
	"sync"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// Answer is the stored relay-tier answer for one machine.
type Answer string

const (
	// Granted — the user accepted the helper for this machine (D8): the
	// machine may run the helper, and the next git.open installs it.
	Granted Answer = "granted"
	// Denied — the user declined. The machine is never asked again and is
	// never silently upgraded. This bead has no writer for it (the ask
	// surface is nocx-1xxa's); the resolver honours the value so a later
	// writer changes behaviour without touching the decision.
	Denied Answer = "denied"
)

// answerDocument is the on-disk envelope. Version 1 is the initial format; a
// document carrying any other version is treated as corrupt — fail-closed
// to "no answers" — rather than partially trusted.
type answerDocument struct {
	Version int               `json:"version"`
	Answers map[string]Answer `json:"answers"`
}

const answerDocumentVersion = 1

// Store persists per-machine relay-tier answers as one atomic JSON document
// (the InstalledFactStore shape): load-once, fail-closed, temp-file+fsync
// writes. A missing, corrupt, unreadable or future-versioned document reads
// as "no answers" — a torn file never grants anything, and an unwritable
// store can never authorize a remote write it cannot show (consent design
// §6).
type Store struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu      sync.Mutex
	answers map[string]Answer
	loaded  bool
}

// NewStore creates a store persisting under docName in docStore. Callers
// MUST provide a logger; the store uses it for one-time corruption warnings
// and has no other output path.
func NewStore(logger log.Logger, docStore storage.DocumentStore, docName string) *Store {
	return &Store{
		docStore: docStore,
		docName:  docName,
		log:      logger,
		answers:  make(map[string]Answer),
	}
}

// Lookup reports the stored answer for the machine identified by the host
// public-key fingerprint. A missing, corrupt or unreadable document is no
// answer — the ask is raised, never a grant assumed.
func (s *Store) Lookup(fingerprint string) (Answer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	a, ok := s.answers[fingerprint]
	return a, ok
}

// The accept-write half — persisting a grant — deliberately lives with its
// caller (nocx-1xxa, the git panel's consent prompt): this store is reached
// from main() through the lookup the resolver performs and the footprint
// surface reads, and a write primitive with no caller would fail the
// deadcode ratchet (the fourth time this plan produced a function whose
// caller lives in a later bead). The document format below is the contract
// that caller writes; Lookup reads it unchanged.

// loadLocked reads the document once, on first use. Corruption of any kind
// degrades to an empty store with a one-time warning: never a partially
// trusted grant, never a grant on the strength of a torn file.
//
// An answer keyed by "" is dropped, not read: consent under an empty
// fingerprint would make every machine share one answer — the exact defect
// consent exists to prevent. The accept-write path (nocx-1xxa) must refuse
// to persist one; this filter is the second half of that rule, applied at
// the one choke point every lookup passes through.
func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.answers = make(map[string]Answer)
	var doc answerDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("helper-consent store unreadable; treating every machine as unanswered",
			"document", s.docName, "error", err)
	case found && doc.Version != answerDocumentVersion:
		s.log.Warn("helper-consent store has an unknown schema version; treating every machine as unanswered",
			"document", s.docName, "version", doc.Version)
	case found && doc.Answers != nil:
		for k, v := range doc.Answers {
			if k != "" {
				s.answers[k] = v
			}
		}
	}
	s.loaded = true
}

// loadLocked's document vocabulary (answerDocument, answerDocumentVersion,
// Granted/Denied) is the accept-write contract: nocx-1xxa's consent-prompt
// RPC persists the answer in this exact shape, and Lookup reads whatever
// that caller wrote without needing to know it.
