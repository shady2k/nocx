package ssh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

func testFactStore(t *testing.T) (*InstalledFactStore, storage.DocumentStore) {
	t.Helper()
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	return NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json"), doc
}

// observed reads the store the way the product does. All() is the store's
// only read (the per-identity Get went with the delivery planner that was
// its only caller — nocx-m8jwn.10), so a test that asserted through Get
// would be asserting through a seam no user can reach, which is AGENTS.md
// testing rule 1. Every fail-closed assertion below therefore goes through
// the enumeration shell.footprint.status actually calls.
func observed(store *InstalledFactStore, identity string) (InstalledFact, bool) {
	for _, f := range store.All() {
		if f.Identity == identity {
			return f, true
		}
	}
	return InstalledFact{}, false
}

func testFact(identity string) InstalledFact {
	return InstalledFact{
		Identity:      identity,
		Protocol:      "1",
		ScriptVersion: "0.6.0",
		Generation:    "v10",
		ObservedAt:    time.Now(),
	}
}

// TestInstalledFactStore_RoundTrip: Record then All names the same fact.
func TestInstalledFactStore_RoundTrip(t *testing.T) {
	store, _ := testFactStore(t)
	if _, ok := observed(store, "pi@192.168.0.93:22"); ok {
		t.Fatal("empty store reports an installation")
	}
	if err := store.Record(testFact("pi@192.168.0.93:22")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	f, ok := observed(store, "pi@192.168.0.93:22")
	if !ok {
		t.Fatal("the fact is missing from All() after Record")
	}
	if f.Protocol != "1" || f.Generation != "v10" || f.ScriptVersion != "0.6.0" {
		t.Errorf("fact = %+v, want the recorded values preserved verbatim", f)
	}
}

// TestInstalledFactStore_SurvivesRestart: a second store over the same
// document — the shape of an app restart — still answers.
func TestInstalledFactStore_SurvivesRestart(t *testing.T) {
	store, doc := testFactStore(t)
	if err := store.Record(testFact("deploy@10.0.0.1:2222")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	restarted := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	f, ok := observed(restarted, "deploy@10.0.0.1:2222")
	if !ok {
		t.Fatal("fact lost across a restart (new store over the same document)")
	}
	if f.Generation != "v10" {
		t.Errorf("Generation = %q, want v10 preserved", f.Generation)
	}
}

// TestInstalledFactStore_RecordIsDurable: the fact is on disk after Record,
// not only in memory — the document's content reflects the write.
func TestInstalledFactStore_RecordIsDurable(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	store := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	if err := store.Record(testFact("u@host:22")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "installed-facts.json")) // #nosec G304 — the store's own document under the test's temp dir.
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	var docOnDisk factDocument
	if err := json.Unmarshal(raw, &docOnDisk); err != nil {
		t.Fatalf("document is not the versioned envelope: %v", err)
	}
	if _, ok := docOnDisk.Facts["u@host:22"]; !ok {
		t.Errorf("document facts = %+v, want u@host:22 recorded", docOnDisk.Facts)
	}
}

// TestInstalledFactStore_CorruptDocumentFailsClosed: garbage on disk reads
// as "nothing installed", never as a partial trust.
func TestInstalledFactStore_CorruptDocumentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "installed-facts.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt document: %v", err)
	}
	store := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	if got := store.All(); len(got) != 0 {
		t.Fatalf("corrupt document reported %d installations, want 0", len(got))
	}
}

// TestInstalledFactStore_FutureVersionFailsClosed: a document from a newer
// schema is not partially trusted.
func TestInstalledFactStore_FutureVersionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	if err := doc.Write("installed-facts.json", factDocument{
		Version: 99,
		Facts:   map[string]InstalledFact{"pi@host:22": testFact("pi@host:22")},
	}); err != nil {
		t.Fatalf("seed future-version document: %v", err)
	}
	store := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	if got := store.All(); len(got) != 0 {
		t.Fatalf("future-version document reported %d installations, want 0", len(got))
	}
}

// TestInstalledFactStore_WriteFailureLeavesMemoryEqual: when the durable
// write fails, All still omits the destination — the in-memory state never
// diverges from what is on disk.
func TestInstalledFactStore_WriteFailureLeavesMemoryEqual(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	// A directory where the document file must be: DocumentStore.Write's
	// rename cannot replace it, so the write fails.
	if err := os.Mkdir(filepath.Join(dir, "installed-facts.json"), 0o700); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}
	store := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	if err := store.Record(testFact("pi@host:22")); err == nil {
		t.Fatal("Record succeeded against a path that cannot be written")
	}
	if _, ok := observed(store, "pi@host:22"); ok {
		t.Fatal("failed Record left the fact in memory; the footprint surface would name an installation that is not on disk")
	}
}

// TestInstalledFactStore_DocumentShape: the persisted envelope is exactly
// the versioned shape the store reads back — a shape regression is caught
// by the round-trip through JSON.
func TestInstalledFactStore_DocumentShape(t *testing.T) {
	dir := t.TempDir()
	doc := storage.NewDocumentStore(dir)
	store := NewInstalledFactStore(log.NewSlogAdapter(nil), doc, "installed-facts.json")
	if err := store.Record(testFact("pi@host:22")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "installed-facts.json")) // #nosec G304 — the store's own document under the test's temp dir.
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	var docOnDisk factDocument
	if err := json.Unmarshal(raw, &docOnDisk); err != nil {
		t.Fatalf("document is not the versioned envelope: %v", err)
	}
	if docOnDisk.Version != 1 {
		t.Errorf("document version = %d, want 1", docOnDisk.Version)
	}
	if f, ok := docOnDisk.Facts["pi@host:22"]; !ok || f.Generation != "v10" {
		t.Errorf("document facts = %+v, want pi@host:22 with generation v10", docOnDisk.Facts)
	}
}

// TestInstalledFactStore_All: enumeration answers every recorded fact,
// ordered by identity — shell.footprint.status must never depend on Go map
// iteration order — and a store with nothing recorded answers an empty list,
// not nil.
func TestInstalledFactStore_All(t *testing.T) {
	store, _ := testFactStore(t)
	if got := store.All(); len(got) != 0 {
		t.Fatalf("empty store All() = %d facts, want 0", len(got))
	}

	for _, id := range []string{"zeta@h:22", "alpha@h:22", "mid@h:22"} {
		if err := store.Record(testFact(id)); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}
	got := store.All()
	if len(got) != 3 {
		t.Fatalf("All() = %d facts, want 3", len(got))
	}
	for i, want := range []string{"alpha@h:22", "mid@h:22", "zeta@h:22"} {
		if got[i].Identity != want {
			t.Errorf("All()[%d].Identity = %q, want %q (order must be deterministic)", i, got[i].Identity, want)
		}
	}
}
