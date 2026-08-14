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
