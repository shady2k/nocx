// Package consent is the per-machine helper-tier consent for the remote
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
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// Answer is the stored helper-tier answer for one machine.
type Answer string

const (
	// Granted — the user accepted the helper for this machine (D8): the
	// machine may run the helper, and the next git.open installs it.
	Granted Answer = "granted"
	// Denied — the user declined. The machine is never asked again and is
	// never silently upgraded. Written by Deny, from the connect-time ask
	// (ADR-0068); the resolver has honoured the value since before that
	// writer existed, so this changed behaviour without touching the
	// decision (nocx-j49sp).
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

// Store persists per-machine helper-tier answers as one atomic JSON document
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

// ErrEmptyFingerprint refuses a grant under "": consent under an empty
// fingerprint would make every machine share one answer — the exact defect
// consent exists to prevent. The accept-write path refuses before anything
// is loaded or written; loadLocked's drop of "" keys is the second half of
// the same rule, applied at the one choke point every lookup passes
// through.
var ErrEmptyFingerprint = errors.New("helper consent: refusing a grant under an empty host fingerprint")

// Grant records that the machine identified by the remote host's public-key
// fingerprint has been raised to the helper tier (D8): the user accepted the
// helper for this host from the git panel's consent prompt. Consent is per
// machine — keyed by the host key, never the session, the tab or the
// account (consent design §3.2) — and the answer persists for the next
// git.open, even across a store reconstruction.
//
// The in-memory answer is committed only when the document write
// succeeded: a grant this process could not persist must not be believed
// here and forgotten on the next start — an unwritable store never
// authorizes a remote write it cannot show (consent design §6).
func (s *Store) Grant(fingerprint string) error {
	return s.setAnswer(fingerprint, Granted)
}

// Deny records that the machine identified by the remote host's public-key
// fingerprint declined the helper (D8, the connect-time ask — ADR-0068,
// owner's decision 2026-09-16). The resolver already honours Denied: a
// denied machine is Refused, never asked again and never silently upgraded
// (consent.go's Resolve). What was missing was a writer — the store modelled
// the answer and the resolver read it, but nothing on any surface ever wrote
// it (nocx-j49sp: "Denied is currently unreachable from any surface"). The
// connect-time ask is that writer.
//
// Same persistence discipline as Grant: the in-memory answer is committed
// only when the document write succeeded, so a decline this process could
// not persist is not believed here and re-asked on the next start rather
// than silently forgotten as answered.
func (s *Store) Deny(fingerprint string) error {
	return s.setAnswer(fingerprint, Denied)
}

// setAnswer is Grant and Deny's shared write: one machine, one answer,
// whichever value it is. Splitting the two would risk the two ever writing
// through different paths — the exact "two owners of one decision" AD-8
// forbids — for a difference that is one value wide.
func (s *Store) setAnswer(fingerprint string, answer Answer) error {
	if fingerprint == "" {
		return ErrEmptyFingerprint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	prev, existed := s.answers[fingerprint]
	s.answers[fingerprint] = answer
	if err := s.writeDocLocked(); err != nil {
		// Roll the in-memory answer back: a failed persist is not an
		// answer. The map must never report what the document does not.
		if existed {
			s.answers[fingerprint] = prev
		} else {
			delete(s.answers, fingerprint)
		}
		return err
	}
	return nil
}

// Revoke removes the answer for one machine. It is idempotent, and the
// in-memory map changes only after the atomic document write succeeds.
func (s *Store) Revoke(fingerprint string) error {
	if fingerprint == "" {
		return ErrEmptyFingerprint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	prev, existed := s.answers[fingerprint]
	if !existed {
		return nil
	}
	delete(s.answers, fingerprint)
	if err := s.writeDocLocked(); err != nil {
		s.answers[fingerprint] = prev
		return err
	}
	return nil
}

// writeDocLocked persists the answers document atomically (the
// DocumentStore's temp-file+fsync discipline): a torn write never leaves a
// half-grant, and the next start reads either the whole document or none
// of it.
func (s *Store) writeDocLocked() error {
	if err := s.docStore.Write(s.docName, answerDocument{
		Version: answerDocumentVersion,
		Answers: s.answers,
	}); err != nil {
		return fmt.Errorf("helper consent: persist %s: %w", s.docName, err)
	}
	return nil
}

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
