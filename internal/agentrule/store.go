package agentrule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/storage"
)

// Store is the on-disk set of a person's own rules: one document per agent,
// under the app directory, plus what each of them means right now.
//
// It is constructed once, at the composition root, from the rules THIS BUILD
// ships — the only thing that knows what a valid document looks like for an
// agent is the build that would read it.
type Store struct {
	docs storage.DocumentStore
	dir  string

	mu     sync.RWMutex
	agents []string
	states map[string]state
}

// New opens the store under configDir, one document per agent this build ships
// a rule for, and loads whatever is already there.
//
// A document that cannot be used is NOT an error here: it is the StateUnreadable
// state, and the pane it concerns answers unknown. A person editing a rule by
// hand is the expected way to produce one, and a backend that refused to start
// over it would take away the one repair they have.
//
// What IS an error is a wiring mistake, and there is exactly one shape of it:
// an agent name that could not be a file name. Those names come from the
// drivers this build carries, they become file names the moment this store
// opens, and a wiring mistake belongs to process start.
func New(configDir string, shipped []agentdriver.Document) (*Store, error) {
	if configDir == "" {
		return nil, errors.New("agentrule: the rule store needs the app's config directory")
	}
	dir := filepath.Join(configDir, DirName)
	// The directory is made, and NO document is: a person who would rather
	// write a rule in their editor needs somewhere to put it, and the path
	// they would have to guess is the one the Settings page prints. Making it
	// is not the falsifier about writing a shipped rule to disk — nothing is
	// written here, and the page a person reads afterwards finds an empty
	// directory rather than a copy of the binary's rules.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agentrule: the rule directory could not be made: %w", err)
	}
	s := &Store{
		docs:   storage.NewDocumentStore(dir),
		dir:    dir,
		agents: make([]string, 0, len(shipped)),
		states: make(map[string]state, len(shipped)),
	}
	for _, doc := range shipped {
		agent := doc.Agent
		if !agentName.MatchString(agent) {
			return nil, fmt.Errorf("agentrule: %q is not a name this store can keep a document under", agent)
		}
		if _, dup := s.states[agent]; dup {
			return nil, fmt.Errorf("agentrule: two shipped rules for %q, which is two answers to one question", agent)
		}
		text, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("agentrule: the shipped rule for %q does not marshal: %w", agent, err)
		}
		s.states[agent] = state{shipped: string(text), enabled: true}
		s.agents = append(s.agents, agent)
	}
	sort.Strings(s.agents)
	for _, agent := range s.agents {
		st := s.load(agent)
		st.where = classify(st)
		s.states[agent] = st
	}
	return s, nil
}

// Directory is where the documents live. The Settings surface shows it so a
// person can open the file in an editor, which is half of what this store is
// for.
func (s *Store) Directory() string { return s.dir }

// Rules returns every agent this build ships a rule for, in agent order, with
// the state of the person's half beside it.
//
// It is a copy, taken under the lock: a surface redrawing a list while somebody
// edits a rule must not see half of one edit and half of the next.
func (s *Store) Rules() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.agents))
	for _, agent := range s.agents {
		out = append(out, s.entryLocked(agent))
	}
	return out
}

// RuleState answers the registry's question for one agent: the rule the person
// wrote, or why there is not one. It is the same derivation Rules projects, so
// a pane and the page about it cannot come to disagree.
//
// An agent this store was not built for answers with the zero value, which is
// "they wrote nothing" — the shipped rule. The registry never asks about one
// (it checks its own set first), and an unknown agent has no shipped rule to
// replace, so the honest answer is the same either way.
func (s *Store) RuleState(agent string) agentdriver.RuleState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[agent]
	if !ok {
		return agentdriver.RuleState{}
	}
	return agentdriver.RuleState{
		Driver: st.driver,
		Off:    st.where == StateDisabled,
		Broken: st.where == StateUnreadable,
	}
}

// Set records the person's own rule for an agent, replacing whatever they had.
// The document is validated by compiling it — the same construction the
// shipped rules go through — and a document that could never answer is refused
// here, while the person is looking at it.
//
// The switch is preserved: a rule written while detection is off leaves it off,
// because editing a rule is not switching one on.
func (s *Store) Set(agent, rule string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[agent]
	if !ok {
		return fmt.Errorf("agentrule: %q is not an agent this build ships a rule for", agent)
	}
	if strings.TrimSpace(rule) == "" {
		return fmt.Errorf("agentrule: an empty document is not a rule; delete it to go back to the shipped one")
	}
	driver, err := agentdriver.Compile([]byte(rule))
	if err != nil {
		return fmt.Errorf("agentrule: %s", clause(err))
	}
	if named := driver.Agent(); named != agent {
		return fmt.Errorf("agentrule: the document names %q, and this one is %q's", named, agent)
	}
	return s.commit(agent, json.RawMessage(rule), st.enabled)
}

// SetEnabled switches detection for an agent. Off is a STATE and not a
// deletion: the person's document stays where it is, so switching back on
// returns exactly the rule they wrote rather than the shipped one.
//
// Switching on when there is no document of theirs removes the file, because a
// document that overrides nothing is a file that exists to be misread; the
// state it leaves is StateShipped, which is what "nothing of mine is in the
// way" means.
func (s *Store) SetEnabled(agent string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[agent]
	if !ok {
		return fmt.Errorf("agentrule: %q is not an agent this build ships a rule for", agent)
	}
	if st.problem != "" {
		// The document cannot be read, so rewriting it would destroy the text
		// its author is mid-way through repairing. Deleting is the way back.
		return fmt.Errorf("agentrule: this document could not be used, so switching detection would replace it; delete it to go back to the shipped rule")
	}
	return s.commit(agent, st.raw, enabled)
}

// Delete removes the person's document for an agent, which restores the
// shipped rule with detection on. It is the one operation that puts the build's
// rule back, and it is the only one that does: switching detection off is not
// this, and neither is editing a rule.
//
// Deleting a document that is not there succeeds: the end state asked for is
// "the shipped rule", and that is what an agent nobody edited is already on.
func (s *Store) Delete(agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[agent]; !ok {
		return fmt.Errorf("agentrule: %q is not an agent this build ships a rule for", agent)
	}
	return s.commit(agent, nil, true)
}

// commit writes the document the state calls for and then publishes what is
// ON DISK — read back, not assumed. The order matters for the same reason it
// does in internal/agentapproval: a failed write must never leave this process
// reading a rule that is not there, because the next start would read a
// different one. Reading back is the other half of that, and it is also what
// makes the text a surface is shown the text the file holds: the store writes
// JSON through one marshaller, so a person's own spacing is normalised by a
// save from the product — which is why a hand edit that nobody saves from here
// keeps every byte it was given.
func (s *Store) commit(agent string, raw json.RawMessage, enabled bool) error {
	doc := document{Version: documentVersion, Rule: raw}
	if !enabled {
		off := false
		doc.Enabled = &off
	}
	switch {
	case raw == nil && enabled:
		if err := s.docs.Delete(s.name(agent)); err != nil {
			return err
		}
	default:
		if err := s.docs.Write(s.name(agent), doc); err != nil {
			return err
		}
	}
	reloaded := s.load(agent)
	reloaded.where = classify(reloaded)
	s.states[agent] = reloaded
	return nil
}

// load reads one agent's document, if there is one, and derives its state.
// Every failure is a state and never an error: see New.
//
// It builds the state from scratch rather than updating one, because it is
// also what runs after a write: what remains from before is the shipped rule,
// which no document can change.
func (s *Store) load(agent string) state {
	st := state{shipped: s.states[agent].shipped, enabled: true}
	var doc document
	found, err := s.docs.Read(s.name(agent), &doc)
	switch {
	case err != nil:
		// A file we cannot read is not an absent one. The person wrote
		// something; standing aside for the shipped rule would tell them it
		// took effect.
		st.problem = clause(err)
	case !found:
		// An absent document and an EMPTIED one are one answer from the store,
		// and they are two things here. Absent is the default state of every
		// install nobody has touched; emptied is a hand edit that says
		// nothing, and reading it as absent would put the shipped rule back
		// without telling the person whose file it was that their edit is not
		// in force.
		empty, statErr := s.emptied(agent)
		if statErr != nil {
			st.problem = statErr.Error()
		} else if empty {
			st.problem = "the file is empty"
		}
	case doc.Version > documentVersion:
		st.problem = fmt.Sprintf("it was written by a newer nocx: version %d, and this one understands %d", doc.Version, documentVersion)
	default:
		st.enabled = doc.enabled()
		if len(doc.Rule) > 0 {
			st.raw = doc.Rule
			driver, err := agentdriver.Compile(doc.Rule)
			switch {
			case err != nil:
				st.problem = clause(err)
			case driver.Agent() != agent:
				st.problem = fmt.Sprintf("it names %q, and this document is %q's", driver.Agent(), agent)
			default:
				st.driver = driver
			}
		}
	}
	return st
}

// emptied reports whether the document is there and holds nothing.
//
// It is a question about PRESENCE rather than about content, and the store is
// asked both: one owns what a document says, this one only distinguishes "no
// document" from "a document with nothing in it". Nothing is read here, so
// there stays one reader of the bytes (storage.DocumentStore).
func (s *Store) emptied(agent string) (bool, error) {
	fi, err := os.Stat(s.path(agent))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("agentrule: %s could not be read: %w", s.name(agent), err)
	}
	return fi.Size() == 0, nil
}

// entryLocked projects one agent's state onto what a surface reads.
func (s *Store) entryLocked(agent string) Entry {
	st := s.states[agent]
	e := Entry{Agent: agent, State: st.where, Problem: st.problem}
	if st.where == StateUnreadable {
		return e
	}
	if st.raw != nil {
		e.Document = string(st.raw)
		return e
	}
	e.Document = st.shipped
	return e
}

// name is the document's file name. It is built from a name validated at
// construction, which is the only reason this is a one-liner.
func (s *Store) name(agent string) string { return agent + ".json" }

// path is the document's own path, used for the presence question alone —
// content goes through storage.DocumentStore, which owns the same join.
func (s *Store) path(agent string) string { return filepath.Join(s.dir, s.name(agent)) }
