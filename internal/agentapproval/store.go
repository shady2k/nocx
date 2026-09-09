// Package agentapproval stores the human answer that admits one executable
// identity to one tool-endpoint scope. It is deliberately separate from the
// runtime process-tree pin: the document remembers what a person permitted,
// while the pin proves which live tree is connecting now.
package agentapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// Answer is the remembered answer for one executable and scope.
type Answer string

const (
	// Granted admits the executable identity to the named scope.
	Granted Answer = "granted"
	// Denied remembers that the person refused the scope. A denial is never
	// silently retried.
	Denied Answer = "denied"
)

// Executable is the identity shown to the person and used as the durable key.
// Path alone is insufficient: replacing a binary at the same path must ask
// again. SHA256 is the content digest of the executable observed at enrolment.
type Executable struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (e Executable) valid() bool {
	if e.Path == "" || !filepath.IsAbs(e.Path) || len(e.SHA256) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(e.SHA256)
	return err == nil
}

// IdentityForExecutable resolves a command name exactly as the backend can see
// it, then hashes the bytes at that path. A missing or unreadable executable is
// a refusal, never an identity guessed from the command's spelling.
func IdentityForExecutable(command string) (Executable, error) {
	if command == "" {
		return Executable{}, errors.New("agent approval: executable is empty")
	}
	path := command
	if !filepath.IsAbs(path) {
		resolved, err := exec.LookPath(command)
		if err != nil {
			return Executable{}, fmt.Errorf("agent approval: resolve executable %q: %w", command, err)
		}
		path = resolved
	}
	return IdentityForPath(path)
}

// IdentityForPath returns the absolute path and content digest of one
// executable. It does not defend against a same-uid process that can replace
// the approved executable and arrange a matching digest; the runtime pin and
// the OS process permissions remain separate enforcement layers.
func IdentityForPath(path string) (Executable, error) {
	if path == "" {
		return Executable{}, errors.New("agent approval: executable path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Executable{}, fmt.Errorf("agent approval: canonical executable path: %w", err)
	}
	data, err := os.ReadFile(absolute) // #nosec G304 — the path is resolved executable identity.
	if err != nil {
		return Executable{}, fmt.Errorf("agent approval: read executable %q: %w", absolute, err)
	}
	digest := sha256.Sum256(data)
	return Executable{Path: absolute, SHA256: hex.EncodeToString(digest[:])}, nil
}

type approvalRecord struct {
	Executable Executable `json:"executable"`
	Scope      string     `json:"scope"`
	Answer     Answer     `json:"answer"`
}

type approvalDocument struct {
	Version   int              `json:"version"`
	Approvals []approvalRecord `json:"approvals"`
}

const approvalDocumentVersion = 1

// Store persists explicit approvals in an atomic document. A missing,
// unreadable, malformed or future-versioned document is an empty store.
type Store struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu        sync.Mutex
	approvals map[string]Answer
	loaded    bool
}

// NewStore creates a process-safe, load-once approval store.
func NewStore(logger log.Logger, docStore storage.DocumentStore, docName string) *Store {
	return &Store{
		docStore:  docStore,
		docName:   docName,
		log:       logger,
		approvals: make(map[string]Answer),
	}
}

// Lookup returns the explicit remembered answer for the exact executable and
// scope. Invalid input is always an unanswered lookup.
func (s *Store) Lookup(executable Executable, scope string) (Answer, bool) {
	if !executable.valid() || scope == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	answer, ok := s.approvals[key(executable, scope)]
	return answer, ok
}

// Record remembers either Granted or Denied. The in-memory answer changes only
// after the atomic document write succeeds, so a failed write never authorizes
// a connection in this process while losing the answer on restart.
func (s *Store) Record(executable Executable, scope string, answer Answer) error {
	if !executable.valid() {
		return errors.New("agent approval: invalid executable identity")
	}
	if strings.TrimSpace(scope) == "" {
		return errors.New("agent approval: scope is empty")
	}
	if answer != Granted && answer != Denied {
		return fmt.Errorf("agent approval: unknown answer %q", answer)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	k := key(executable, scope)
	previous, existed := s.approvals[k]
	s.approvals[k] = answer
	if err := s.writeLocked(); err != nil {
		if existed {
			s.approvals[k] = previous
		} else {
			delete(s.approvals, k)
		}
		return err
	}
	return nil
}

// Record is one remembered answer, for a person to read back and reconsider.
type Record struct {
	Executable Executable `json:"executable"`
	Scope      string     `json:"scope"`
	Answer     Answer     `json:"answer"`
}

// List returns every remembered answer, ordered by path then digest then
// scope so a surface renders the same list twice running. A map's order is
// not one, and a list that reshuffles under a person deciding what to forget
// is a list they cannot use.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	records := make([]Record, 0, len(s.approvals))
	for k, answer := range s.approvals {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		records = append(records, Record{
			Executable: Executable{Path: parts[0], SHA256: parts[1]},
			Scope:      parts[2],
			Answer:     answer,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.Executable.Path != b.Executable.Path {
			return a.Executable.Path < b.Executable.Path
		}
		if a.Executable.SHA256 != b.Executable.SHA256 {
			return a.Executable.SHA256 < b.Executable.SHA256
		}
		return a.Scope < b.Scope
	})
	return records
}

// Forget removes one remembered answer, so a decision a person made once is
// one they can unmake. Without it the store is write-only from the product's
// side: a denial is never silently retried, deliberately, which is only
// defensible while there is somewhere to reconsider it (nocx-6jbad).
//
// The answer is what goes; nothing else does. Forgetting does not reach into
// a live pane, and it does not have to: the admit check reads this store on
// every call, so the next tool call from an agent whose answer is gone is
// refused, and the agent itself goes on running without nocx's tools —
// exactly the state a denial produces.
//
// A false second result is "there was nothing to forget", which is a SUCCESS
// and not an error: a second click, or a page whose read predates somebody
// else's forget, asked for a state that already holds.
func (s *Store) Forget(executable Executable, scope string) (bool, error) {
	if !executable.valid() || strings.TrimSpace(scope) == "" {
		return false, errors.New("agent approval: invalid executable identity or scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	k := key(executable, scope)
	previous, existed := s.approvals[k]
	if !existed {
		return false, nil
	}
	delete(s.approvals, k)
	if err := s.writeLocked(); err != nil {
		s.approvals[k] = previous
		return false, err
	}
	return true, nil
}

func key(executable Executable, scope string) string {
	return executable.Path + "\x00" + executable.SHA256 + "\x00" + scope
}

func (s *Store) writeLocked() error {
	records := make([]approvalRecord, 0, len(s.approvals))
	for k, answer := range s.approvals {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			return errors.New("agent approval: internal key is malformed")
		}
		records = append(records, approvalRecord{
			Executable: Executable{Path: parts[0], SHA256: parts[1]},
			Scope:      parts[2],
			Answer:     answer,
		})
	}
	if err := s.docStore.Write(s.docName, approvalDocument{
		Version:   approvalDocumentVersion,
		Approvals: records,
	}); err != nil {
		return fmt.Errorf("agent approval: persist %s: %w", s.docName, err)
	}
	return nil
}

func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.approvals = make(map[string]Answer)
	var doc approvalDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("agent approval store unreadable; treating every executable as unanswered",
			"document", s.docName, "error", err)
	case found && doc.Version != approvalDocumentVersion:
		s.log.Warn("agent approval store has an unknown schema version; treating every executable as unanswered",
			"document", s.docName, "version", doc.Version)
	case found:
		loaded := make(map[string]Answer, len(doc.Approvals))
		valid := true
		for _, record := range doc.Approvals {
			if !record.Executable.valid() || strings.TrimSpace(record.Scope) == "" ||
				(record.Answer != Granted && record.Answer != Denied) {
				valid = false
				break
			}
			k := key(record.Executable, record.Scope)
			if _, duplicate := loaded[k]; duplicate {
				valid = false
				break
			}
			loaded[k] = record.Answer
		}
		if valid {
			s.approvals = loaded
		} else {
			s.log.Warn("agent approval store contains an invalid record; treating every executable as unanswered",
				"document", s.docName)
		}
	}
	s.loaded = true
}
