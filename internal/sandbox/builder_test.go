package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootConflictChecksRunBeforeAncestorCoalescing(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "root")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	roParent := buildRoot{path: parent, access: ReadOnly, kind: DirectoryRoot, provenance: StandardRoot}
	rwChild := buildRoot{path: child, access: ReadWrite, kind: DirectoryRoot, provenance: LaunchDeltaRoot}
	if err := validateRootConflicts([]buildRoot{roParent, rwChild}); err != nil {
		t.Fatalf("RW child within RO parent should be representable: %v", err)
	}
	roChild := buildRoot{path: child, access: ReadOnly, kind: DirectoryRoot, provenance: LaunchDeltaRoot}
	rwParent := buildRoot{path: parent, access: ReadWrite, kind: DirectoryRoot, provenance: StandardRoot}
	if err := validateRootConflicts([]buildRoot{rwParent, roChild}); err == nil {
		t.Fatal("RO child inside RW ancestor must be refused")
	}
}

func TestPinnedRootRejectsRetargetedPathIdentity(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	alias := filepath.Join(base, "alias")
	for _, p := range []string{first, second} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	want, err := identity(canonical)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := openPinned(canonical, DirectoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	if err = verifyPinned(fd, want, DirectoryRoot); err != nil {
		t.Fatalf("original pinned identity did not verify: %v", err)
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	retargeted, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	got, err := identity(retargeted)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("retargeted alias unexpectedly names the prepared object")
	}
	if err := verifyPinned(fd, want, DirectoryRoot); err != nil {
		t.Fatalf("retargeting changed the already pinned object: %v", err)
	}
}

func TestReservedDirectoryCanonicalizesBeforeFirstCreation(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(alias, "nocx", "state")
	reserved, err := canonicalReserved(requested)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(actual, "nocx", "state")
	if reserved != want {
		t.Fatalf("reserved alias = %q, want %q", reserved, want)
	}
	if err = os.MkdirAll(want, 0o700); err != nil {
		t.Fatal(err)
	}
	created, err := canonicalReserved(requested)
	if err != nil || created != reserved {
		t.Fatalf("creation changed reserved identity: %q, %v", created, err)
	}
	broken := filepath.Join(base, "broken")
	if err := os.Symlink(filepath.Join(base, "absent"), broken); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalReserved(filepath.Join(broken, "state")); err == nil {
		t.Fatal("broken reserved symlink accepted as missing directory")
	}
}

func TestRuntimeBaseRejectsSharedDirectoryAndSymlink(t *testing.T) {
	base := t.TempDir()
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- deliberately unsafe temporary fixture must be rejected by createRuntime.
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if runtime, err := createRuntime(shared); err == nil {
		_ = os.RemoveAll(runtime.Root)
		t.Fatal("world-writable runtime base accepted")
	}
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	if runtime, err := createRuntime(alias); err == nil {
		_ = os.RemoveAll(runtime.Root)
		t.Fatal("symlink runtime base accepted")
	}
}
