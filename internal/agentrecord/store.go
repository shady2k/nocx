package agentrecord

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/storage"
)

// State is where the record reading an agent comes from, and whether there is
// one at all. The set is closed for the reason agentrule.State's is: a value
// nobody wrote a branch for would land in whichever branch was written last,
// and one of these branches decides whether nocx launches an agent.
type State string

const (
	// StateShipped means this build's record describes the agent, which is
	// every agent on every install nobody has edited yet.
	StateShipped State = "shipped"
	// StateUser means the person's own document describes it, in place of the
	// shipped record entirely.
	StateUser State = "user"
	// StateUnreadable means a document of theirs exists and could not be used.
	// It is a state rather than the shipped record because standing aside for
	// the build's copy would tell its author their edit took effect when it
	// did not — and for a launch that is worse than not launching.
	StateUnreadable State = "unreadable"
)

// Entry is one agent as the store answers it: which record is in force, and —
// when there is a problem — why.
type Entry struct {
	// ID is the agent's name.
	ID string
	// State is where the record in force comes from.
	State State
	// Record is the record in force. It is the ZERO value in
	// StateUnreadable, deliberately: the document cannot be read and the
	// shipped record is not what is in force, so handing back either would
	// invite a launch the person did not ask for.
	Record Record
	// Problem is why their document could not be used, in a clause a person
	// can act on. Empty in every other state.
	Problem string
}

// Store is the set of agent records this build knows: the ones it ships,
// embedded in the binary and never written out, and whatever documents a
// person has written beside them under the app directory.
//
// It holds NO cached state, and that is the design rather than an economy:
// every read goes to the disk, so a person editing a record by hand is seen by
// the next launch rather than by the next restart — which is the whole of what
// "edits take effect without a restart" means for a launch record. There is
// consequently nothing to keep under a lock, because there is nothing in
// memory that a write could half-update.
type Store struct {
	docs    storage.DocumentStore
	dir     string
	shipped map[string]Record
}

// New opens the record store under configDir: the records THIS BUILD ships,
// read out of the binary, and the directory a person's own documents live in.
//
// Nothing is written here, and that is the property the acceptance criterion
// is about rather than an incidental: the directory is made so that a person
// who would rather write a document in their editor has somewhere to put it —
// and the path they would otherwise have to guess is the one the Settings
// surface prints — while no shipped record is copied into it, so an upgrade
// still reaches an agent nobody touched.
//
// What IS an error is a wiring mistake, and there are two: no config
// directory, and a shipped record that could not be parsed (which fails at
// package init, before this is reached).
func New(configDir string) (*Store, error) {
	if configDir == "" {
		return nil, errors.New("agentrecord: the record store needs the app's config directory")
	}
	dir := filepath.Join(configDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agentrecord: the agent directory could not be made: %w", err)
	}
	return &Store{
		docs:    storage.NewDocumentStore(dir),
		dir:     dir,
		shipped: shippedRecords,
	}, nil
}

// entry answers which record is in force for one agent, and false when
// neither this build nor the person describes one — which is the honest answer
// for an agent nocx has never seen, and is what keeps a restore from launching
// a name somebody typed.
func (s *Store) entry(id string) (Entry, bool) {
	if s == nil || !storage.ValidDocumentName(id) {
		return Entry{}, false
	}
	shippedRecord, isShipped := s.shipped[id]
	doc, problem := s.read(id)
	switch {
	case problem != nil:
		// A document that cannot be read is unreadable whatever it says, and
		// it is unreadable even for an agent this build ships: the person's
		// file is the one that was meant to be in force.
		// fmt.Sprint rather than asking the error for its text: the value is a
		// clause for a person, never a log line, and the log-context ratchet
		// reads an identifier followed by a `.Error(` as a call site whose
		// context it cannot prove — a false positive the baseline is full of,
		// and one that may not be added to.
		return Entry{ID: id, State: StateUnreadable, Problem: fmt.Sprint(problem)}, true
	case doc == nil:
		if !isShipped {
			return Entry{}, false
		}
		return Entry{ID: id, State: StateShipped, Record: shippedRecord}, true
	default:
		return Entry{
			ID:    id,
			State: StateUser,
			// Builtin is the BUILD's answer and never the document's, so a
			// document cannot make an agent it invented removable-or-not by
			// claiming so; it is true exactly when this build ships the id.
			Record: Record{ID: id, Builtin: isShipped, Document: *doc},
		}, true
	}
}

// read loads one agent's document, if there is one. A document that cannot be
// used is a PROBLEM and never an error, for the reason New gives: a person
// editing a record by hand is the expected way to produce one, and a backend
// that refused to start over it would take away the one repair they have.
func (s *Store) read(id string) (*Document, error) {
	var doc Document
	found, err := s.docs.Read(name(id), &doc)
	switch {
	case err != nil:
		return nil, err
	case !found:
		// An absent document and an EMPTIED one are one answer from the
		// store, and they are two things here. Absent is the default state of
		// every install nobody has touched; emptied is a hand edit that says
		// nothing, and reading it as absent would launch the build's command
		// over a file its author is in the middle of writing.
		//
		// internal/agentrule reaches the same verdict for the same reason.
		// The distinction is not storage's: DocumentStore.Read reads an
		// emptied document as an absent one deliberately, and whether that is
		// a state or an absence is the document owner's question.
		empty, statErr := s.emptied(id)
		switch {
		case statErr != nil:
			return nil, statErr
		case empty:
			return nil, errors.New("the file is empty")
		}
		return nil, nil
	case doc.Version > documentVersion:
		return nil, fmt.Errorf("it was written by a newer nocx: version %d, and this one understands %d", doc.Version, documentVersion)
	}
	if err := doc.validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// emptied reports whether the document is there and holds nothing. It is a
// question about PRESENCE rather than about content, so nothing is read here
// and there stays one reader of the bytes (storage.DocumentStore).
func (s *Store) emptied(id string) (bool, error) {
	fi, err := os.Stat(filepath.Join(s.dir, name(id)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("agentrecord: %s could not be read: %w", name(id), err)
	}
	return fi.Size() == 0, nil
}

// name is the document's file name, built from a name validated at
// construction, which is the only reason this is a one-liner.
func name(id string) string { return id + ".json" }
