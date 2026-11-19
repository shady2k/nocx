// Package hostmode is the integration method a host keeps: auto, raw, script
// or helper, keyed by the remote host's public-key fingerprint.
//
// ADR-727g63tr decision 1, and ADR-0105 before it: which method nocx uses to
// integrate with a machine is a fact OF THE HOST, not of a saved connection
// and not of the pane it opened. The connect-time ask (ADR-0069) writes its
// answer here for a saved connection and a hand-typed one alike; the open path
// reads it back by the fingerprint the dial observed; the connection editor
// reads and writes the same record. Two connections to one machine therefore
// resolve one mode, and a saved connection has no copy of its own.
//
// IT IS NOT THE MACHINE'S CONSENT. Consent (internal/helper/consent) answers
// "may a binary be deployed on this machine" and is written only by choosing
// the helper; this record answers "which method does this host use" and is
// written by every answer, raw and script included. Two questions, two owners
// (AD-8), stored beside each other and never derived from one another.
//
// TWO SURFACES WRITE IT and they are two views of one record, not two
// records: the connect-time ask writes the answer it was given, by the
// fingerprint it named, and the connection editor reads and writes the same
// record for the host a saved connection points at.
//
// AND IT IS NOT A MODE FOR A CONNECTION THAT NEVER DIALED. The record is keyed
// by a host-key fingerprint, which exists only after a dial has seen a key, so
// a host nobody has connected to has no mode — it is auto, "not yet answered"
// (ADR-0033), and the connect-time ask is what it means.
package hostmode

import (
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// Mode is one integration method. The four values are the product's own
// vocabulary from ADR-0069, spelled once here — a saved connection no longer
// keeps a copy of its own (ADR-727g63tr decision 1).
type Mode string

const (
	// Auto is not an answer: the host has not said which method it uses, so
	// the connect-time ask decides (ADR-0033). It is what a host with no
	// record reads as.
	Auto Mode = "auto"
	// Raw is no integration — a plain terminal, nothing written to the host.
	Raw Mode = "raw"
	// Script is the shell integration nocx ships, without a compiled binary.
	Script Mode = "script"
	// Helper is the deployed nocx helper on the far host.
	Helper Mode = "helper"
)

// Parse reports whether s names a method this build can record, and which.
func Parse(s string) (Mode, bool) {
	switch m := Mode(s); m {
	case Raw, Script, Helper:
		return m, true
	}
	return "", false
}

// DeliversScripts reports whether m publishes the shell bundle and
// integrates at session open — the open-time gate, and the ONE owner of that
// question (AD-8). The empty string is an unanswered destination (a direct
// host, an ad-hoc open, a local session) and answers as the default does; an
// unrecognised value fails closed and never integrates.
//
// It lives on the mode rather than at either call site because it had two and
// they disagreed. internal/ssh gated the open on `mode == "" || mode ==
// "script"` while internal/transport decided whether a session had even
// REQUESTED integration with `... == "script"`, each a separate literal of the
// same rule. Changing the default to auto (ADR-0033) made the second one false
// for every unconfigured connection, so sessions integrated and then reported
// nothing — the status notification never fired, because the axis had one
// meaning in the gate and another in the reporter.
//
// raw is the ONLY answer that means "nothing", and an unrecognised value joins
// it by failing closed. Every other mode integrates, helper included: the tiers
// are additive, not alternative (§5.2 — "declining a deployed binary must not
// also decline shell scripts — different risks", and the inverse holds for the
// same reason).
func (m Mode) DeliversScripts() bool {
	switch m {
	case "", Auto, Script, Helper:
		return true
	default:
		return false
	}
}

// ErrEmptyFingerprint refuses a record under "": a mode keyed by nothing
// would make every host share one answer — the exact defect the fingerprint
// key exists to prevent.
var ErrEmptyFingerprint = errors.New("host mode: refusing a record under an empty host-key fingerprint")

// ErrNotAnAnswer refuses to record auto. Auto means "not yet answered"
// (ADR-0033), and a record is only ever an answer: a caller that means "ask me
// again" removes the record instead (Remove), which is what the connection
// editor's auto does.
var ErrNotAnAnswer = errors.New("host mode: auto is not an answer and is not recorded")

// ErrNotSaved reports that an answer was kept for this run but could not be
// persisted. It is not a refusal of the answer: the store keeps it in memory
// so the open the answer was given for may proceed, and returns this error so
// the caller can say that the host will have to be asked again after a
// restart. A store whose write fails is never silently a store that forgot.
var ErrNotSaved = errors.New("host mode: the answer could not be persisted")

// modeDocument is the on-disk envelope. Any other version is corrupt —
// fail-closed to "no records", which is every host at auto.
type modeDocument struct {
	Version int             `json:"version"`
	Modes   map[string]Mode `json:"modes"`
}

const modeDocumentVersion = 1

// Store persists the host records as one atomic JSON document, the shape the
// consent store beside it already uses: load-once, fail-closed, temp-file and
// fsync writes. A missing, corrupt, unreadable or future-versioned document
// reads as no records — every host at auto — because a torn file must never
// answer a question about somebody's machine.
type Store struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu     sync.Mutex
	modes  map[string]Mode
	loaded bool
	// watchers are the parties told about a change: the far helper's registry
	// is the first (ADR-727g63tr decision 2's decision revision), and the map
	// is keyed by a minted id because a caller may subscribe twice with two
	// closures over the same state.
	watchers    map[uint64]func()
	nextWatcher uint64
}

// NewStore creates a store persisting under docName in docStore. Callers MUST
// provide a logger; the store uses it for its one-time corruption warnings and
// has no other output path.
func NewStore(logger log.Logger, docStore storage.DocumentStore, docName string) *Store {
	return &Store{
		docStore: docStore,
		docName:  docName,
		log:      logger,
		modes:    make(map[string]Mode),
	}
}

// Lookup reports the host's recorded method. A host with no record is not an
// error and has no mode: the second result says which of the two this is, and
// a caller with no record reads auto (ADR-0033).
func (s *Store) Lookup(fingerprint string) (Mode, bool) {
	if s == nil || fingerprint == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	m, ok := s.modes[fingerprint]
	return m, ok
}

// Set records the host's method, keyed by the fingerprint the dial observed.
//
// # The failed write keeps the answer, and that is the point
//
// A consent record is rolled back when it cannot be persisted (its own store
// says why: a grant this process could not persist must not be believed here
// and forgotten on the next start). This store answers the OTHER way for the
// connect-time ask, for one stated reason: the value being written is a
// person's answer to a question they were asked in the middle of opening a
// connection, and losing it would put them back in front of the same dialog —
// the open they answered for would ask again, forever, on a store that cannot
// write. So the answer is kept in memory, the open proceeds with it, and
// ErrNotSaved travels back so the product can say the host will have to be
// asked again after a restart.
//
// The editor's write is Persist, not this, and the difference is who can retry
// (see Persist).
func (s *Store) Set(fingerprint string, mode Mode) error {
	return s.record(fingerprint, mode, false)
}

// record is the single-fingerprint write used by the connect-time answer.
// durable controls whether a failed write updates memory for this run.
func (s *Store) record(fingerprint string, mode Mode, durable bool) error {
	if fingerprint == "" {
		return ErrEmptyFingerprint
	}
	if mode == Auto {
		return ErrNotAnAnswer
	}
	if _, ok := Parse(string(mode)); !ok {
		return fmt.Errorf("host mode: %q is not an integration method", mode)
	}
	s.mu.Lock()
	s.loadLocked()
	previous, recorded := s.modes[fingerprint]
	next := make(map[string]Mode, len(s.modes)+1)
	for k, v := range s.modes {
		next[k] = v
	}
	next[fingerprint] = mode
	werr := s.writeDoc(next)
	changed := !recorded || previous != mode
	if werr != nil {
		if !durable {
			s.modes = next
		}
		watchers := s.watchersLocked()
		s.mu.Unlock()
		// A durable write that failed changed nothing; a non-durable one kept
		// the answer for this run and that IS the change the subscribers need
		// to see.
		if changed && !durable {
			notify(watchers)
		}
		return fmt.Errorf("%w: %s: %v", ErrNotSaved, s.docName, werr)
	}
	s.modes = next
	watchers := s.watchersLocked()
	s.mu.Unlock()
	if changed {
		notify(watchers)
	}
	return nil
}

// PersistMany changes the records for a host in one document write. An Auto
// mode removes every supplied fingerprint; other modes set every one. A
// failed write leaves both the durable document and in-memory map unchanged.
func (s *Store) PersistMany(fingerprints []string, mode Mode) error {
	if len(fingerprints) == 0 {
		return nil
	}
	if mode != Auto {
		if _, ok := Parse(string(mode)); !ok {
			return fmt.Errorf("host mode: %q is not an integration method", mode)
		}
	}
	for _, fp := range fingerprints {
		if fp == "" {
			return ErrEmptyFingerprint
		}
	}
	s.mu.Lock()
	s.loadLocked()
	next := make(map[string]Mode, len(s.modes)+len(fingerprints))
	for k, v := range s.modes {
		next[k] = v
	}
	changed := false
	for _, fp := range fingerprints {
		previous, recorded := next[fp]
		if mode == Auto {
			if recorded {
				changed = true
			}
			delete(next, fp)
			continue
		}
		if !recorded || previous != mode {
			changed = true
		}
		next[fp] = mode
	}
	if err := s.writeDoc(next); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s: %v", ErrNotSaved, s.docName, err)
	}
	s.modes = next
	watchers := s.watchersLocked()
	s.mu.Unlock()
	if changed {
		notify(watchers)
	}
	return nil
}

// Watch registers fn to be called after every change this store records: a
// mode set, a mode replaced, a record removed, and an answer that could not be
// persisted but was kept for this run. It answers the function that ends the
// subscription; calling that twice is harmless.
//
// THE CALLBACK IS MADE OUTSIDE THE STORE'S LOCK, and that is load-bearing: the
// far helper's registry re-reads the mode from this store in the callback, so a
// notification made under the mutex would deadlock the party the notification
// exists for.
func (s *Store) Watch(fn func()) (stop func()) {
	if s == nil || fn == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.watchers == nil {
		s.watchers = make(map[uint64]func())
	}
	s.nextWatcher++
	id := s.nextWatcher
	s.watchers[id] = fn
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.watchers, id)
			s.mu.Unlock()
		})
	}
}

// watchersLocked snapshots the subscribers so the notification can run after
// the lock is released. Called with the mutex held.
func (s *Store) watchersLocked() []func() {
	if len(s.watchers) == 0 {
		return nil
	}
	out := make([]func(), 0, len(s.watchers))
	for _, fn := range s.watchers {
		out = append(out, fn)
	}
	return out
}

func notify(watchers []func()) {
	for _, fn := range watchers {
		fn()
	}
}

// writeDoc persists the whole set atomically (storage.DocumentStore's
// temp-file, fsync, rename). Callers commit the returned set to memory.
func (s *Store) writeDoc(modes map[string]Mode) error {
	if err := s.docStore.Write(s.docName, modeDocument{Version: modeDocumentVersion, Modes: modes}); err != nil {
		return fmt.Errorf("host mode: persist %s: %w", s.docName, err)
	}
	return nil
}

// loadLocked reads the document once, on first use. Corruption of any kind
// degrades to no records with a one-time warning: never a partially trusted
// answer about somebody's machine.
//
// A record keyed by "" is dropped, not read: a mode under an empty fingerprint
// would be every host's mode, which is the defect the fingerprint key exists
// to prevent. Set refuses to write one; this filter is the second half of the
// same rule.
func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.modes = make(map[string]Mode)
	var doc modeDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("host mode store unreadable; treating every host as not yet answered",
			"document", s.docName, "error", err)
	case found && doc.Version != modeDocumentVersion:
		s.log.Warn("host mode store has an unknown schema version; treating every host as not yet answered",
			"document", s.docName, "version", doc.Version)
	case found && doc.Modes != nil:
		for k, v := range doc.Modes {
			if k == "" {
				continue
			}
			if _, ok := Parse(string(v)); !ok {
				continue
			}
			s.modes[k] = v
		}
	}
	s.loaded = true
}
