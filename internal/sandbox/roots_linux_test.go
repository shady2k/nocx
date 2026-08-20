//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// On a package-store distribution the FHS list grants nothing: /etc is a
// forest of symlinks into the store, and Landlock resolves the target. The
// enforcement smoke on such a host passed 34 of 35 checks and failed exactly
// one — "read /etc/hosts: permission denied" — because /etc/hosts resolves to
// a store path no rule covered (nocx-263da).
func TestSystemReadOnlyRoots_IncludesThePackageStoreWhenItExists(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(base, "not-a-store")

	restore := linuxPackageStoreRoots
	t.Cleanup(func() { linuxPackageStoreRoots = restore })
	linuxPackageStoreRoots = []string{store, absent}

	roots := systemReadOnlyRoots()
	if !inSet(store, roots) {
		t.Errorf("an existing package store is not a system read-only root: %v", roots)
	}
	if inSet(absent, roots) {
		t.Errorf("a store that does not exist was claimed anyway: %v", roots)
	}
	// The FHS set is not replaced by it.
	if !inSet("/usr", roots) {
		t.Errorf("/usr left the system read-only set: %v", roots)
	}
}

// A store root is read-only and must never be reachable as a writable grant,
// exactly like /usr.
func TestWritableRootIsProtected_CoversThePackageStore(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := linuxPackageStoreRoots
	t.Cleanup(func() { linuxPackageStoreRoots = restore })
	linuxPackageStoreRoots = []string{store}

	sysRoots, err := canonicalSystemRoots()
	if err != nil {
		t.Fatalf("canonicalSystemRoots: %v", err)
	}
	if !writableRootIsProtected(store, sysRoots) {
		t.Errorf("the package store may be granted writable; it is a system read-only root")
	}
}
