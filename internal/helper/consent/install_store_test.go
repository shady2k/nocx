package consent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

func newTestInstallStore(t *testing.T) *InstallStore {
	t.Helper()
	return NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "helper-installs.json")
}

func testInstall(fp string) Install {
	return Install{
		Fingerprint: fp,
		Identity:    "u@host:22",
		Path:        "/home/u/.nocx/helper/v1-linux-amd64-abc/",
		Hash:        "abc",
		InstalledAt: time.Now().UTC(),
	}
}

// TestInstallStoreEmptyListsNothing: nothing observed is nothing installed —
// the footprint surface's fail-closed reading.
func TestInstallStoreEmptyListsNothing(t *testing.T) {
	s := newTestInstallStore(t)
	if got := s.All(); len(got) != 0 {
		t.Fatalf("All on an empty store = %v, want none", got)
	}
}

// TestInstallStoreRecordThenAll: a recorded install is listed, with the
// identity the footprint screen shows the user.
func TestInstallStoreRecordThenAll(t *testing.T) {
	s := newTestInstallStore(t)
	if err := s.Record(testInstall("SHA256:one")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := s.All()
	if len(got) != 1 || got[0].Fingerprint != "SHA256:one" || got[0].Identity != "u@host:22" {
		t.Fatalf("All after Record = %+v, want the recorded install", got)
	}
}

// TestInstallStoreRecordEmptyFingerprintRefused: an observation with no
// machine key would list under nothing; refuse it like a grant.
func TestInstallStoreRecordEmptyFingerprintRefused(t *testing.T) {
	s := newTestInstallStore(t)
	if err := s.Record(testInstall("")); err == nil {
		t.Fatal("Record with an empty fingerprint must fail")
	}
}

// TestInstallStoreIsDurableAcrossInstances: the listing is persisted, so a
// restart still shows what was installed (the never-connect surface reads
// it without a dial).
func TestInstallStoreIsDurableAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if err := store.Record(testInstall("SHA256:one")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	again := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if got := again.All(); len(got) != 1 {
		t.Fatalf("install observation lost across store instances: %v", got)
	}
}

// TestInstallStoreCorruptDocumentFailsClosed: a torn document lists
// nothing — the footprint surface must never claim an install on the
// strength of a broken file.
func TestInstallStoreCorruptDocumentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper-installs.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if got := s.All(); len(got) != 0 {
		t.Fatalf("corrupt document must list nothing, got %v", got)
	}
}

// TestInstallStoreOneMachineTwoHomes: one machine may carry a helper
// footprint in more than one home directory (consent design §3.2 — consent
// is per machine, not per account); each install location is its own row.
func TestInstallStoreOneMachineTwoHomes(t *testing.T) {
	s := newTestInstallStore(t)
	one := testInstall("SHA256:same")
	one.Path = "/home/u/.nocx/helper/v1-linux-amd64-abc/"
	two := testInstall("SHA256:same")
	two.Path = "/home/other/.nocx/helper/v1-linux-amd64-abc/"
	if err := s.Record(one); err != nil {
		t.Fatalf("Record one: %v", err)
	}
	if err := s.Record(two); err != nil {
		t.Fatalf("Record two: %v", err)
	}
	if got := s.All(); len(got) != 2 {
		t.Fatalf("one machine, two homes: All = %d rows, want 2", len(got))
	}
}

// TestInstallStoreRemoveForgetsOneRow: removing an observed install clears
// exactly that row — the machine AND the install directory name it, because
// one machine can carry a helper footprint in more than one home (consent
// design §3.2) — and leaves every other row listed.
func TestInstallStoreRemoveForgetsOneRow(t *testing.T) {
	s := newTestInstallStore(t)
	one := testInstall("SHA256:one")
	two := testInstall("SHA256:two")
	two.Path = "/home/other/.nocx/helper/v1-linux-amd64-def/"
	if err := s.Record(one); err != nil {
		t.Fatalf("Record one: %v", err)
	}
	if err := s.Record(two); err != nil {
		t.Fatalf("Record two: %v", err)
	}
	if err := s.Remove("SHA256:one", one.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := s.All()
	if len(got) != 1 || got[0].Fingerprint != "SHA256:two" {
		t.Fatalf("All after Remove = %+v, want only the other machine's row", got)
	}
	// The same fingerprint under a DIFFERENT path is a different row: the
	// machine's other-home install is untouched.
	if err := s.Remove("SHA256:one", "/home/u/.nocx/helper/other/"); err != nil {
		t.Fatalf("Remove non-matching path: %v", err)
	}
	if got := s.All(); len(got) != 1 {
		t.Fatalf("Remove with a non-matching path dropped a row: %v", got)
	}
}

// TestInstallStoreRemoveIsIdempotent: removing a row that is not there is a
// no-op, not an error — clicking remove twice, or uninstalling a host whose
// observation was already cleared, never fails.
func TestInstallStoreRemoveIsIdempotent(t *testing.T) {
	s := newTestInstallStore(t)
	if err := s.Remove("SHA256:never", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err != nil {
		t.Fatalf("Remove on an empty store: %v", err)
	}
	if err := s.Record(testInstall("SHA256:one")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Remove("SHA256:one", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	if err := s.Remove("SHA256:one", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err != nil {
		t.Fatalf("second Remove (already gone): %v", err)
	}
}

// TestInstallStoreRemoveIsDurable: the cleared observation survives a store
// reconstruction — the next start must not resurrect a removed helper on
// the footprint screen.
func TestInstallStoreRemoveIsDurable(t *testing.T) {
	dir := t.TempDir()
	store := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if err := store.Record(testInstall("SHA256:one")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Remove("SHA256:one", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	again := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if got := again.All(); len(got) != 0 {
		t.Fatalf("removed observation resurrected across store instances: %v", got)
	}
}

// failSecondWrite is a DocumentStore whose Write fails on its second call,
// then behaves normally: the shape a Remove failure takes after a
// successful Record.
type failSecondWrite struct {
	storage.DocumentStore
	writes int
}

func (f *failSecondWrite) Write(name string, doc any) error {
	f.writes++
	if f.writes == 2 {
		return os.ErrPermission
	}
	return f.DocumentStore.Write(name, doc)
}

// TestInstallStoreRemoveFailureLeavesMemoryUnchanged: a failed persist is
// not a removal — the in-memory listing stays as it was (and the document
// with it), so the footprint surface keeps showing the row until the store
// can durably forget it.
func TestInstallStoreRemoveFailureLeavesMemoryUnchanged(t *testing.T) {
	dir := t.TempDir()
	ds := &failSecondWrite{DocumentStore: storage.NewDocumentStore(dir)}
	s := NewInstallStore(log.NewSlogAdapter(nil), ds, "helper-installs.json")
	if err := s.Record(testInstall("SHA256:one")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Remove("SHA256:one", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err == nil {
		t.Fatal("Remove against a failing write must fail")
	}
	if got := s.All(); len(got) != 1 {
		t.Fatalf("failed Remove changed the in-memory listing: %v", got)
	}
	again := NewInstallStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "helper-installs.json")
	if got := again.All(); len(got) != 1 {
		t.Fatalf("failed Remove changed the durable document: %v", got)
	}
	// Healed: the same store can still forget the row.
	if err := s.Remove("SHA256:one", "/home/u/.nocx/helper/v1-linux-amd64-abc/"); err != nil {
		t.Fatalf("Remove after heal: %v", err)
	}
}

// TestInstallStoreRemoveMachineForgetsEveryHome proves helper uninstall
// clears all observed generations for one machine, not only the row clicked.
func TestInstallStoreRemoveMachineForgetsEveryHome(t *testing.T) {
	s := newTestInstallStore(t)
	one := testInstall("SHA256:same")
	two := one
	two.Path = "/home/other/.nocx/helper/v2-linux-amd64-def/"
	other := testInstall("SHA256:other")
	if err := s.Record(one); err != nil {
		t.Fatalf("Record one: %v", err)
	}
	if err := s.Record(two); err != nil {
		t.Fatalf("Record two: %v", err)
	}
	if err := s.Record(other); err != nil {
		t.Fatalf("Record other: %v", err)
	}
	if err := s.RemoveMachine("SHA256:same"); err != nil {
		t.Fatalf("RemoveMachine: %v", err)
	}
	got := s.All()
	if len(got) != 1 || got[0].Fingerprint != "SHA256:other" {
		t.Fatalf("All after RemoveMachine = %+v, want only the other machine", got)
	}
}
