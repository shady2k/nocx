package apibind

// The binding document, over storage.DocumentStore for the mapping and the
// vault for the values — the house pattern internal/snippet already uses: a
// versioned document with its own storage.Module.
//
// # The invariant, with both ends (design §12.2)
//
//	A vault value for a secret variable exists from before its binding is
//	written until the collection that declares that variable is deleted.
//	Deleting a collection deletes the bindings it owns and the values only
//	those bindings referenced.
//
// The opening end is Bind's ordering; UnbindCollection is the CLOSING
// EVENT, and it is real rather than aspirational.
//
// # Why the value goes first, and why the cleanup is here
//
// §8.2: a vault value with no binding is dead weight nobody can reach; a
// binding naming a value that is not there is a variable that reports
// itself unresolved and blocks the send. The first is the failure we choose
// to risk, so the value is written first.
//
// An earlier draft justified this by claiming reconciliation collects the
// orphan. IT DOES NOT, and the correction matters because it moves work
// into this file. internal/vault/journal.go:119 clears the journal entry
// and KEEPS the secret whenever a catalogue record exists, and CreateNamed
// writes the value and the record IN THE SAME SAVE (vault.go:1122) — so a
// create that returned at all is exactly the state reconciliation treats as
// complete. The vault deletes an orphan only when no record landed AND no
// metadata target was attached, which is never our case. Therefore: every
// value this package strands, this package collects. Best effort, and what
// it cannot collect it says so about.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

// DocumentName is the binding document. It lives beside the app's other
// configuration documents and never inside a collection folder (§8): a
// folder arriving in a pull request must have no way to name a secret, and
// the way that is achieved is that the identifier is not in the folder at
// all.
const DocumentName = "api-bindings.json"

// Module declares this document's own monotonic schema version (ADR-0011
// §6). One version, no migrations: the format is new, and a chain grows
// when a format changes rather than in anticipation.
var Module = storage.Module{Name: "apibind", Current: 1}

// Secrets is the vault, narrowed to the three calls a binding makes. It is
// declared HERE as a consumer contract — the shape internal/capability
// already uses (capability/config.go:30) — so this package depends on what
// it needs rather than on the Vault's whole lifecycle.
//
// CreateNamed rather than Create: an orphaned value is only "collectable
// later" if a person can SEE it, and the Secrets page is where they would.
// A nameless row there is a value nobody can identify and therefore nobody
// will ever remove.
type Secrets interface {
	CreateNamed(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
	Get(ctx context.Context, id credential.SecretID) (credential.Secret, error)
	Delete(ctx context.Context, id credential.SecretID) error
}

// ValueResolver is the read half that never yields an identifier. A caller
// holding one of these can ask what a variable is worth and has nothing to
// hand an id to — which is how §8's property survives the wiring as well as
// the format.
type ValueResolver interface {
	Resolve(ctx context.Context, k Key) (value credential.Secret, found bool, err error)
	Variables(ctx context.Context, collection, environment string) apicoll.Lookup
}

// record is one binding, as it sits in the document. SecretID is the only
// identifier for credential material in this feature, and this struct is
// the only place it is written down.
type record struct {
	Collection  string `json:"collection"`
	Environment string `json:"environment"`
	Variable    string `json:"variable"`
	SecretID    string `json:"secretId"`
}

func (r record) key() Key {
	return Key{Collection: r.Collection, Environment: r.Environment, Variable: r.Variable}
}

type bindingDocument struct {
	SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	Bindings      []record              `json:"bindings"`
}

// JSONStore is the Store over a document and a vault.
type JSONStore struct {
	// mu serialises the read-edit-write of the document. It is held across
	// the vault call in Bind ON PURPOSE: the check for an existing binding,
	// the create and the write must be one critical section, or two
	// concurrent binds of one variable each build on a document read before
	// the other landed and one value is stranded with nobody to collect it.
	//
	// This is a per-store lock on a rare, user-initiated write, not a
	// global gate in front of arbitrary remote latency.
	mu      sync.Mutex
	docs    storage.DocumentStore
	secrets Secrets
	log     log.Logger
}

// Option configures a JSONStore.
type Option func(*JSONStore)

// WithLogger supplies the logger a best-effort cleanup warns through. Nil
// is allowed; nothing here depends on a logger existing.
func WithLogger(l log.Logger) Option { return func(s *JSONStore) { s.log = l } }

// NewStore builds the binding store. The document store decides WHERE the
// document lives — the composition root's choice, not this package's.
func NewStore(docs storage.DocumentStore, secrets Secrets, opts ...Option) *JSONStore {
	s := &JSONStore{docs: docs, secrets: secrets}
	for _, o := range opts {
		o(s)
	}
	return s
}

var (
	_ Store         = (*JSONStore)(nil)
	_ ValueResolver = (*JSONStore)(nil)
)

// Lookup resolves a variable to the stored value's identifier.
//
// "Not bound" is the second return and NOT an error, because it is a normal
// state: it is how an unresolved variable blocks a send (§6.5) rather than
// sending an empty string. A store that could not be READ is a different
// answer again — it has not told you the variable is unbound, and reporting
// it as unbound would send the user to bind something they already had.
func (s *JSONStore) Lookup(k Key) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return "", false, err
	}
	if i := indexOf(doc.Bindings, k); i >= 0 {
		return doc.Bindings[i].SecretID, true, nil
	}
	return "", false, nil
}

// Resolve answers with the VALUE, so the identifier never leaves this
// package. A binding naming a value the vault no longer holds reports
// itself unresolved — §6.5's rule, not a new one — rather than resolving to
// nothing at all.
func (s *JSONStore) Resolve(ctx context.Context, k Key) (credential.Secret, bool, error) {
	id, found, err := s.Lookup(k)
	if err != nil || !found {
		return credential.Secret{}, false, err
	}
	v, err := s.secrets.Get(ctx, credential.SecretID(id))
	if err != nil {
		return credential.Secret{}, false, fmt.Errorf("apibind: read the value bound to %q: %w", k.Variable, err)
	}
	if v.IsEmpty() {
		// The vault's contract: an absent id is an empty Secret and a nil
		// error. That is the repairable half of §8.2 — the binding
		// survived its value, so the variable is unresolved and the send is
		// blocked.
		return credential.Secret{}, false, nil
	}
	return v, true, nil
}

// Variables is the lookup the send path composes with the environment's
// plain values (apicoll.Chain). The caller passes a variable NAME and gets
// a value; there is no parameter through which it could pass an identifier.
//
// The plaintext is copied out INSIDE credential.Secret.Use, which is the
// deliberate copy the type's contract asks for (internal/assistant does the
// same at the same boundary): the value has to become a header, a URL or a
// body, and this is the one place that conversion happens.
//
// ctx rides the closure because apicoll.Lookup takes none — it is the
// contract of substitution, which knows nothing about where an answer comes
// from. The closure is built per send and does not outlive it.
func (s *JSONStore) Variables(ctx context.Context, collection, environment string) apicoll.Lookup {
	return func(name string) (string, bool, error) {
		v, found, err := s.Resolve(ctx, Key{Collection: collection, Environment: environment, Variable: name})
		if err != nil || !found {
			return "", false, err
		}
		var out string
		if err := v.Use(func(b []byte) error { out = string(b); return nil }); err != nil {
			return "", false, err
		}
		return out, true, nil
	}
}

// Bind stores value and records the binding — THE VALUE FIRST, THE BINDING
// SECOND (§8.2, §12.2).
//
// On a replace the same ordering holds and gains a third step: the new
// value lands, the binding is repointed, and only THEN is the old value
// removed. At no instant does the binding name something that is not there.
func (s *JSONStore) Bind(ctx context.Context, k Key, value []byte) error {
	if err := validateKey(k); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readLocked()
	if err != nil {
		return err
	}

	// 1. The value.
	id, err := s.secrets.CreateNamed(ctx, credential.NewSecretBytes(value),
		vault.SecretMeta{Name: labelFor(k), Kind: vault.KindPassword})
	if err != nil {
		return fmt.Errorf("apibind: store the value for %q: %w", k.Variable, err)
	}

	// 2. The binding.
	var previous string
	if i := indexOf(doc.Bindings, k); i >= 0 {
		previous = doc.Bindings[i].SecretID
		doc.Bindings[i].SecretID = string(id)
	} else {
		doc.Bindings = append(doc.Bindings, record{
			Collection: k.Collection, Environment: k.Environment,
			Variable: k.Variable, SecretID: string(id),
		})
	}
	if err := s.writeLocked(doc); err != nil {
		// The value is unreachable and nothing upstream will collect it, so
		// it is collected here. A cleanup failure leaves harmless dead
		// weight and must NOT replace the error the caller needs: their
		// remedy is "the document could not be written", not "a delete
		// failed".
		s.collect(ctx, id, "the binding write failed")
		return fmt.Errorf("apibind: record the binding for %q: %w", k.Variable, err)
	}

	// 3. The replaced value, only now that nothing names it.
	if previous != "" && previous != string(id) && !referenced(doc.Bindings, previous) {
		s.collect(ctx, credential.SecretID(previous), "it was replaced")
	}
	return nil
}

// Unbind removes one binding and the value it alone referenced — THE
// BINDING FIRST. That is Bind's ordering mirrored, and it picks the same
// benign failure: an interruption leaves an unreachable value, never a
// binding naming a value that has already been deleted.
func (s *JSONStore) Unbind(ctx context.Context, k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readLocked()
	if err != nil {
		return err
	}
	i := indexOf(doc.Bindings, k)
	if i < 0 {
		// Absence is the desired end state, so removing what is not there
		// succeeds — the same contract storage.DocumentStore.Delete signs,
		// and what makes an interrupted removal safe to re-run.
		return nil
	}
	id := doc.Bindings[i].SecretID
	doc.Bindings = append(doc.Bindings[:i], doc.Bindings[i+1:]...)
	if err := s.writeLocked(doc); err != nil {
		return fmt.Errorf("apibind: remove the binding for %q: %w", k.Variable, err)
	}
	if referenced(doc.Bindings, id) {
		return nil
	}
	if err := s.secrets.Delete(ctx, credential.SecretID(id)); err != nil {
		return fmt.Errorf("apibind: the binding for %q is gone but its value could not be removed: %w", k.Variable, err)
	}
	return nil
}

// UnbindCollection is §12.2's CLOSING EVENT: deleting a collection removes
// the bindings it owns and the values ONLY THOSE BINDINGS referenced.
//
// The "only" is a real clause, not decoration. A document can name one
// value from two bindings — an earlier build, a hand edit, a future dedup —
// and deleting one collection must not spend another's credential.
func (s *JSONStore) UnbindCollection(ctx context.Context, collection string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readLocked()
	if err != nil {
		return err
	}
	kept := doc.Bindings[:0:0]
	var orphaned []string
	for _, b := range doc.Bindings {
		if b.Collection == collection {
			orphaned = append(orphaned, b.SecretID)
			continue
		}
		kept = append(kept, b)
	}
	if len(orphaned) == 0 {
		return nil
	}
	doc.Bindings = kept
	// Nothing is deleted on the strength of a write that did not land.
	if err := s.writeLocked(doc); err != nil {
		return fmt.Errorf("apibind: remove the bindings of %q: %w", collection, err)
	}

	var stranded []string
	seen := map[string]bool{}
	for _, id := range orphaned {
		if seen[id] || referenced(kept, id) {
			continue
		}
		seen[id] = true
		if err := s.secrets.Delete(ctx, credential.SecretID(id)); err != nil {
			stranded = append(stranded, id)
			s.warn("apibind: could not remove a value whose collection was deleted",
				"secretID", id, "collection", collection, "error", err)
		}
	}
	if len(stranded) > 0 {
		// Deletion is the whole point of this call, so a value that could
		// not be removed is REPORTED rather than logged and forgotten. It
		// is also findable: the leftover carries the collection's name in
		// the vault catalogue.
		return fmt.Errorf("apibind: the bindings of %q are gone but %d of their values could not be removed (%s)",
			collection, len(stranded), strings.Join(stranded, ", "))
	}
	return nil
}

// readLocked reads the document, runs it through the module's version
// protocol, then decodes — the order the protocol requires, and the same
// shape internal/snippet uses. Caller holds s.mu.
func (s *JSONStore) readLocked() (bindingDocument, error) {
	var raw json.RawMessage
	found, err := s.docs.Read(DocumentName, &raw)
	if err != nil {
		return bindingDocument{}, fmt.Errorf("apibind: read %s: %w", DocumentName, err)
	}
	if !found {
		return bindingDocument{SchemaVersion: Module.Current}, nil
	}
	var probe struct {
		SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	}
	if err = json.Unmarshal(raw, &probe); err != nil {
		return bindingDocument{}, fmt.Errorf("apibind: read %s: %w", DocumentName, err)
	}
	migrated, err := Module.Migrate(raw, probe.SchemaVersion)
	if err != nil {
		return bindingDocument{}, fmt.Errorf("apibind: %s: %w", DocumentName, err)
	}
	var doc bindingDocument
	if err = json.Unmarshal(migrated, &doc); err != nil {
		return bindingDocument{}, fmt.Errorf("apibind: read %s: %w", DocumentName, err)
	}
	return doc, nil
}

func (s *JSONStore) writeLocked(doc bindingDocument) error {
	doc.SchemaVersion = Module.Current
	if doc.Bindings == nil {
		doc.Bindings = []record{}
	}
	return s.docs.Write(DocumentName, doc)
}

// collect removes a value nothing can reach any more. Best effort by
// design: what it cannot remove is unreachable dead weight, which is the
// benign half of §8.2, and reporting it here would replace an error the
// caller needs with one they cannot act on.
func (s *JSONStore) collect(ctx context.Context, id credential.SecretID, why string) {
	if err := s.secrets.Delete(ctx, id); err != nil {
		s.warn("apibind: could not remove an unreachable value", "secretID", id, "why", why, "error", err)
	}
}

func (s *JSONStore) warn(msg string, args ...any) {
	if s.log != nil {
		s.log.Warn(msg, args...)
	}
}

func indexOf(records []record, k Key) int {
	for i, r := range records {
		if r.key() == k {
			return i
		}
	}
	return -1
}

func referenced(records []record, id string) bool {
	for _, r := range records {
		if r.SecretID == id {
			return true
		}
	}
	return false
}

// validateKey refuses a key missing any of its three parts. Such a binding
// is one nothing could ever look up — Lookup compares the whole triple — so
// writing it would put a row in the document that only a hand edit reaches,
// and a vault value beside it that nothing names.
func validateKey(k Key) error {
	switch {
	case k.Collection == "":
		return errors.New("apibind: a binding names a collection")
	case k.Environment == "":
		return errors.New("apibind: a binding names an environment")
	case k.Variable == "":
		return errors.New("apibind: a binding names a variable")
	}
	return nil
}

// labelFor is what a person sees on the Secrets page. The collection is
// named by its FOLDER NAME rather than its path: the vault catalogue is not
// the place to record where on this machine somebody keeps their work, and
// the folder name is what they would recognise anyway.
//
// It is a display label and nothing resolves through it — Lookup compares
// the triple in the document. A label that collides with another is
// suffixed by the vault (CreateNamedResolved) or simply repeated; either
// way no binding changes meaning.
func labelFor(k Key) string {
	return fmt.Sprintf("%s / %s / %s", filepath.Base(k.Collection), k.Environment, k.Variable)
}
