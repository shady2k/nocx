package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The three RemoteFS contracts the sink cannot derive for itself, asserted
// on the adapter directly. Two of them are reachable through Put and are
// tested there as well; Rename is not — the sink calls it only after
// PosixRename reports ErrPosixRenameUnsupported, which os.Rename never
// does. It is implemented rather than stubbed because RemoteFS states a
// contract, and it is tested here because a contract nobody checks is a
// contract nobody keeps.

// TestOSFS_RenameRefusesAnExistingDestination is the contract os.Rename
// does NOT keep — rename(2) replaces silently — and the reason this method
// is link(2) plus an unlink. The sink's fallback moves the old file ASIDE
// with it (nocx-340t); a Rename that clobbered would destroy the very
// content the fallback exists to preserve.
func TestOSFS_RenameRefusesAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := osFS{}.Rename(src, dst)

	if err == nil {
		t.Fatal("Rename onto an existing destination succeeded; SFTP v3 rename refuses one and the fallback depends on that")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error %v, want one satisfying errors.Is(fs.ErrExist)", err)
	}
	if b, readErr := os.ReadFile(dst); readErr != nil || string(b) != "old" { //nolint:gosec // a path this test built under its own t.TempDir()
		t.Errorf("destination is %q (%v), want the old content untouched", b, readErr)
	}
}

// TestOSFS_RenameMovesWhenTheDestinationIsFree is the paired success: for
// every "returns an error when…" there is a "and on an ordinary directory
// it works" (AGENTS.md). The old name is gone and the new one holds the
// content — a move, not a copy left behind.
func TestOSFS_RenameMovesWhenTheDestinationIsFree(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (osFS{}).Rename(src, dst); err != nil {
		t.Fatalf("Rename onto a free name: %v", err)
	}

	if _, err := os.Lstat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the source still exists (%v); Rename must move, not copy", err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "new" { //nolint:gosec // a path this test built under its own t.TempDir()
		t.Errorf("destination is %q (%v), want the moved content", b, err)
	}
}

// TestOSFS_PosixRenameReplacesAndIsNeverUnsupported is what keeps the
// two-rename fallback — and its window where the destination holds nothing
// — unreachable on this provider. Both halves matter: it replaces, and it
// does not report ErrPosixRenameUnsupported while doing so.
func TestOSFS_PosixRenameReplacesAndIsNeverUnsupported(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	for _, f := range []struct{ path, content string }{{src, "new"}, {dst, "old"}} {
		if err := os.WriteFile(f.path, []byte(f.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := (osFS{}).PosixRename(src, dst); err != nil {
		t.Fatalf("PosixRename: %v", err)
	}

	if b, err := os.ReadFile(dst); err != nil || string(b) != "new" { //nolint:gosec // a path this test built under its own t.TempDir()
		t.Errorf("destination is %q (%v), want the replacement", b, err)
	}
}

// TestOSFS_CreateRefusesAnExistingPath is the O_EXCL half of Create's
// contract: a taken name must refuse, or KeepBoth's reservation would
// truncate a concurrent transfer's temp file (D5). fs.ErrExist is
// deliberately NOT one of the classified refusals — it is the one the
// suffix search is allowed to answer by trying the next name.
func TestOSFS_CreateRefusesAnExistingPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "taken")
	if err := os.WriteFile(p, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := osFS{}.Create(p)
	if err == nil {
		_ = f.Close()
		t.Fatal("Create on a taken name succeeded; O_EXCL is what makes the reservation an arbiter")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error %v, want one satisfying errors.Is(fs.ErrExist)", err)
	}
	if b, readErr := os.ReadFile(p); readErr != nil || string(b) != "mine" { //nolint:gosec // a path this test built under its own t.TempDir()
		t.Errorf("the existing file is now %q (%v), want it untouched", b, readErr)
	}
}

// TestOSFS_RemoveOfAMissingPathReportsNotExist is what the sink reads as
// "already removed" rather than as a stranded file. Getting this wrong
// would have every clean-up report litter that is not there.
func TestOSFS_RemoveOfAMissingPathReportsNotExist(t *testing.T) {
	err := osFS{}.Remove(filepath.Join(t.TempDir(), "never"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error %v, want one satisfying errors.Is(fs.ErrNotExist)", err)
	}
}
