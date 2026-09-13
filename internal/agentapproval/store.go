// Package agentapproval stores the human answer that admits one executable
// identity to one tool-endpoint scope, ON ONE MACHINE. It is deliberately
// separate from the runtime process-tree pin: the document remembers what a
// person permitted, while the pin proves which live tree is connecting now.
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

// Answer is the remembered answer for one executable, domain and scope.
type Answer string

const (
	// Granted admits the executable identity to the named scope on the named
	// machine.
	Granted Answer = "granted"
	// Denied remembers that the person refused the scope. A denial is never
	// silently retried.
	Denied Answer = "denied"
)

// DomainKind is the closed set of machines an answer can be about. It is a
// kind rather than a flag because the two answer different questions: a local
// answer is about THIS machine, and an ssh answer is about somebody else's.
type DomainKind string

const (
	// DomainLocal is the machine the coordinator runs on.
	DomainLocal DomainKind = "local"
	// DomainSSH is a machine reached over an ssh connection this backend
	// authenticated.
	DomainSSH DomainKind = "ssh"
)

// Domain is the TRUST DOMAIN an answer was given in: which machine's agent the
// person admitted. It is half of the durable key, beside the executable and
// the scope, and it exists because the other two cannot tell machines apart:
// the same path and the same bytes on two hosts, or under two accounts on one
// host, is one key without it — so a yes given for one machine would admit an
// agent on another (nocx-50w7p.16).
//
// EVERY FIELD IS DERIVED BY THE BACKEND from the session's own route — its
// kind, its host, the account its connection authenticated as, and the host
// key that connection was accepted under. Nothing here may come from a probe,
// from a caller, or from anything the agent says about itself: a value the
// admitted party supplies is a value the admitted party chooses.
type Domain struct {
	Kind    DomainKind `json:"kind"`
	Host    string     `json:"host,omitempty"`
	Account string     `json:"account,omitempty"`
	// HostKey is the SHA256 fingerprint of the remote host's public key as
	// observed and verified when the connection was dialed — the same machine
	// identity the helper-consent answer is keyed by. It is IN the key for the
	// reason ADR-0023 gives for consent: a machine whose key changed is a
	// different answer to give, so the stored yes stops matching and the
	// question is asked again.
	HostKey string `json:"hostKey,omitempty"`
}

// LocalDomain is the domain of the machine this backend runs on.
func LocalDomain() Domain { return Domain{Kind: DomainLocal} }

// valid reports whether the domain is complete enough to key an answer. An ssh
// domain missing any of its three facts is INVALID rather than defaulted or
// keyed on what is present: an empty host key is a session whose key was never
// observed (a stub channel, a session that never dialed), and keying an answer
// on "" would make every such session one machine — the same "never grants on a
// shared empty key" rule the helper-consent store already follows.
func (d Domain) Valid() bool {
	switch d.Kind {
	case DomainLocal:
		return d.Host == "" && d.Account == "" && d.HostKey == ""
	case DomainSSH:
		return d.Host != "" && d.Account != "" && d.HostKey != ""
	default:
		return false
	}
}

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

// approvalKey is the durable identity of one answer: what was admitted, the
// machine it was admitted on, and the scope it reaches. It is a struct rather
// than a joined string because these are exactly the three things the document
// carries, and a key that must be re-parsed is a key that can drift from what
// it was written with.
type approvalKey struct {
	executable Executable
	domain     Domain
	scope      string
}

type approvalRecord struct {
	Executable Executable `json:"executable"`
	Domain     Domain     `json:"domain"`
	Scope      string     `json:"scope"`
	Answer     Answer     `json:"answer"`
}

type approvalDocument struct {
	Version   int              `json:"version"`
	Approvals []approvalRecord `json:"approvals"`
}

// approvalDocumentVersion 2 is the version that carries a domain. A version 1
// document is REFUSED, by the rule this store already had for a version it does
// not know, and the reason is not tidiness: its records name no machine, and
// nocx is greenfield, so there is nothing to be compatible with — importing
// them would key somebody's yes to whichever machine asked next, which is the
// defect this version exists to end. A person re-answers once.
const approvalDocumentVersion = 2

// Store persists explicit approvals in an atomic document. A missing,
// unreadable, malformed or version-unknown document is an empty store.
type Store struct {
	docStore storage.DocumentStore
	docName  string
	log      log.Logger

	mu        sync.Mutex
	approvals map[approvalKey]Answer
	loaded    bool
}

// NewStore creates a process-safe, load-once approval store.
func NewStore(logger log.Logger, docStore storage.DocumentStore, docName string) *Store {
	return &Store{
		docStore:  docStore,
		docName:   docName,
		log:       logger,
		approvals: make(map[approvalKey]Answer),
	}
}

// Lookup returns the explicit remembered answer for the exact executable,
// machine and scope. Invalid input is always an unanswered lookup — an
// incomplete ssh domain included, because an answer that was given for a
// machine nobody can name is an answer for no machine.
func (s *Store) Lookup(executable Executable, domain Domain, scope string) (Answer, bool) {
	if !executable.valid() || !domain.Valid() || scope == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	answer, ok := s.approvals[approvalKey{executable: executable, domain: domain, scope: scope}]
	return answer, ok
}

// Record remembers either Granted or Denied. The in-memory answer changes only
// after the atomic document write succeeds, so a failed write never authorizes
// a connection in this process while losing the answer on restart.
func (s *Store) Record(executable Executable, domain Domain, scope string, answer Answer) error {
	if !executable.valid() {
		return errors.New("agent approval: invalid executable identity")
	}
	if !domain.Valid() {
		return errors.New("agent approval: invalid trust domain")
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
	k := approvalKey{executable: executable, domain: domain, scope: scope}
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
// It carries the domain, because a surface that showed two machines' answers
// alike could not offer to unmake one of them.
type Record struct {
	Executable Executable `json:"executable"`
	Domain     Domain     `json:"domain"`
	Scope      string     `json:"scope"`
	Answer     Answer     `json:"answer"`
}

// List returns every remembered answer, ordered by path then digest then
// domain then scope so a surface renders the same list twice running. A map's
// order is not one, and a list that reshuffles under a person deciding what to
// forget is a list they cannot use.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	records := make([]Record, 0, len(s.approvals))
	for k, answer := range s.approvals {
		records = append(records, Record{
			Executable: k.executable,
			Domain:     k.domain,
			Scope:      k.scope,
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
		if a.Domain.Kind != b.Domain.Kind {
			return a.Domain.Kind < b.Domain.Kind
		}
		if a.Domain.Host != b.Domain.Host {
			return a.Domain.Host < b.Domain.Host
		}
		if a.Domain.Account != b.Domain.Account {
			return a.Domain.Account < b.Domain.Account
		}
		if a.Domain.HostKey != b.Domain.HostKey {
			return a.Domain.HostKey < b.Domain.HostKey
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
// The answer is what goes; nothing else does. The store reaches no further
// than the record: an agent already running goes on running, and nothing here
// touches a process. What a caller must add is how a live admission ends — a
// connection admitted under an answer that has gone keeps it until something
// closes the connection (ADR-0058), and that closing lives with whoever holds
// the connections, not here.
//
// A false second result is "there was nothing to forget", which is a SUCCESS
// and not an error: a second click, or a page whose read predates somebody
// else's forget, asked for a state that already holds.
func (s *Store) Forget(executable Executable, domain Domain, scope string) (bool, error) {
	if !executable.valid() || !domain.Valid() || strings.TrimSpace(scope) == "" {
		return false, errors.New("agent approval: invalid executable identity, trust domain or scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	k := approvalKey{executable: executable, domain: domain, scope: scope}
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

func (s *Store) writeLocked() error {
	records := make([]approvalRecord, 0, len(s.approvals))
	for k, answer := range s.approvals {
		records = append(records, approvalRecord{
			Executable: k.executable,
			Domain:     k.domain,
			Scope:      k.scope,
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
	s.approvals = make(map[approvalKey]Answer)
	var doc approvalDocument
	found, err := s.docStore.Read(s.docName, &doc)
	switch {
	case err != nil:
		s.log.Warn("agent approval store unreadable; treating every executable as unanswered",
			"document", s.docName, "error", err)
	case found && doc.Version != approvalDocumentVersion:
		// A version 1 document lands here: its records name no machine, so
		// nothing in it can be keyed for the machine that asks next.
		s.log.Warn("agent approval store has an unknown schema version; treating every executable as unanswered",
			"document", s.docName, "version", doc.Version)
	case found:
		loaded := make(map[approvalKey]Answer, len(doc.Approvals))
		valid := true
		for _, record := range doc.Approvals {
			if !record.Executable.valid() || !record.Domain.Valid() || strings.TrimSpace(record.Scope) == "" ||
				(record.Answer != Granted && record.Answer != Denied) {
				valid = false
				break
			}
			k := approvalKey{executable: record.Executable, domain: record.Domain, scope: record.Scope}
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
