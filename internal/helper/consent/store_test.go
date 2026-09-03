package consent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "consent.json")
}

// seedGrantedDocument writes a version-1 document carrying a grant for
// fingerprint directly to disk — the exact shape the accept-write path
// (nocx-1xxa's consent-prompt RPC) persists, which this bead deliberately
// does not own a writer for. The store must read it unchanged: that is the
// contract between the two beads.
func seedGrantedDocument(t *testing.T, dir, fingerprint string) {
	t.Helper()
	doc := `{"version":1,"answers":{"` + fingerprint + `":"granted"}}`
	if err := os.WriteFile(filepath.Join(dir, "consent.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStoreEmptyAnswersNothing: a store with no grants has no answers — the
// fail-closed reading every lookup starts from.
func TestStoreEmptyAnswersNothing(t *testing.T) {
	s := newTestStore(t)
	if ans, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatalf("Lookup on an empty store = %q, want no answer", ans)
	}
}

// TestStoreReadsAGrantedDocument: a grant written by the accept-write path
// (nocx-1xxa) is visible to the lookup — the document format is the
// contract, and Lookup must not need to know who wrote the answer.
func TestStoreReadsAGrantedDocument(t *testing.T) {
	dir := t.TempDir()
	seedGrantedDocument(t, dir, "SHA256:abc")
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	ans, ok := s.Lookup("SHA256:abc")
	if !ok || ans != Granted {
		t.Fatalf("Lookup on a granted document = %q/%v, want granted", ans, ok)
	}
}

// TestStoreCorruptDocumentFailsClosed: a torn or corrupt document grants
// nothing — never a partial grant on the strength of a broken file.
func TestStoreCorruptDocumentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "consent.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if ans, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatalf("corrupt document must read as no answer, got %q", ans)
	}
}

// TestStoreGrantsArePerFingerprint: two host keys are two machines, and
// one grant never leaks into the other (consent design §3.2).
func TestStoreGrantsArePerFingerprint(t *testing.T) {
	dir := t.TempDir()
	seedGrantedDocument(t, dir, "SHA256:one")
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if _, ok := s.Lookup("SHA256:two"); ok {
		t.Fatal("a grant for one host key must not answer for another")
	}
}

// TestStoreEmptyFingerprintNeverGrants: an empty fingerprint is never a
// machine — no document can answer for it, so a host whose key was not
// captured can never resolve to relay on the strength of a shared empty
// key.
func TestStoreEmptyFingerprintNeverGrants(t *testing.T) {
	dir := t.TempDir()
	seedGrantedDocument(t, dir, "")
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if ans, ok := s.Lookup(""); ok {
		t.Fatalf("an empty fingerprint must never read as granted, got %q", ans)
	}
}

// failingDocStore is a DocumentStore whose Write always fails: the
// write-failure half of the accept interval. A grant that cannot be
// persisted must not be believed by the store that failed to persist it —
// otherwise this process would answer granted while the next start answers
// unanswered (consent design §6: an unwritable store never authorizes a
// remote write it cannot show).
type failingDocStore struct{ storage.DocumentStore }

func (failingDocStore) Write(string, any) error { return os.ErrPermission }

// TestGrantPersistsAcrossReopen is the accept-write half of
// TestResolverGrantSurvivesStoreReopen: the grant the panel's RPC persisted
// is still there for a store reconstructed over the same directory — the
// write must be genuinely durable, not a memory-only answer.
func TestGrantPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if err := s.Grant("SHA256:abc"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	again := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	ans, ok := again.Lookup("SHA256:abc")
	if !ok || ans != Granted {
		t.Fatalf("Lookup after reopen = %q/%v, want granted — the grant must survive", ans, ok)
	}
}

// TestGrantEmptyFingerprintRefused is the write-side half of the empty-key
// rule: Grant must refuse to persist an answer under "" — the same filter
// loadLocked applies on the read side, at the one choke point every write
// passes through. A write that accepted "" would let a session whose host
// key was never captured grant every machine at once.
func TestGrantEmptyFingerprintRefused(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if err := s.Grant(""); err == nil {
		t.Fatal("Grant(\"\") = nil, want a refusal")
	}
	if ans, ok := s.Lookup(""); ok {
		t.Fatalf("an empty fingerprint must never read as granted, got %q", ans)
	}
	// And nothing was written: a reopen must not find a phantom grant.
	again := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if _, ok := again.Lookup(""); ok {
		t.Fatal("reopen found an answer under an empty fingerprint that Grant refused")
	}
}

// TestGrantWriteFailureDoesNotGrant: when the document cannot be persisted,
// Grant fails AND the in-memory answer is rolled back — a process that
// could not write the grant must not behave as granted until the next
// start reveals the lie.
func TestGrantWriteFailureDoesNotGrant(t *testing.T) {
	s := NewStore(log.NewSlogAdapter(nil), failingDocStore{storage.NewDocumentStore(t.TempDir())}, "consent.json")
	if err := s.Grant("SHA256:abc"); err == nil {
		t.Fatal("Grant with an unwritable store = nil, want the write error")
	}
	if ans, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatalf("a failed grant must not answer granted, got %q", ans)
	}
}

// TestRevokeForgetsTheMachineAnswer is the uninstall half of the consent
// interval: after remote removal succeeds, the machine is no longer granted.
func TestRevokeForgetsTheMachineAnswer(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if err := s.Grant("SHA256:abc"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := s.Revoke("SHA256:abc"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatal("revoked machine still has a consent answer")
	}
	again := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "consent.json")
	if _, ok := again.Lookup("SHA256:abc"); ok {
		t.Fatal("revoked consent resurrected after reopening the store")
	}
}
