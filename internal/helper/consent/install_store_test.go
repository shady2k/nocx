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
