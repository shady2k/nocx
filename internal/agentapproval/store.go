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
	"strconv"
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

// IdentityForPID is available for callers that have a kernel-stamped process
// id. The /proc link is the process's executable rather than a mutable PATH
// lookup; callers still need the live process-tree pin for pid-reuse safety.
func IdentityForPID(pid int) (Executable, error) {
	if pid <= 0 {
		return Executable{}, fmt.Errorf("agent approval: invalid executable pid %d", pid)
	}
	path, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return Executable{}, fmt.Errorf("agent approval: read executable for pid %d: %w", pid, err)
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
