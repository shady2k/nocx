package shellintegration

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestPublisher returns a Publisher over a fresh temp home and the real
// OS filesystem — the same seam real carriers implement.
func newTestPublisher(t *testing.T) (*Publisher, string, FS) {
	t.Helper()
	home := t.TempDir()
	fsys := NewOSFS()
	pub := NewPublisher(testLogger(), fsys, filepath.Join(home, dirName))
	return pub, home, fsys
}

func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 — test-only path built from t.TempDir + fixed names.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func readManifestT(t *testing.T, root string) *Manifest {
	t.Helper()
	m, err := parseManifest(readFileT(t, filepath.Join(root, manifestName)))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

func statModeT(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestPublishCreatesVersionedLayout pins the design §4 layout: root 0700,
// generation dir 0700, data 0600, launch 0700, manifest 0600, and a
// manifest that names exactly the generation with correct hashes, modes and
// sizes.
func TestPublishCreatesVersionedLayout(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	b := testBundle("10")
	b.Files = append(b.Files, BundleFile{Name: launchName, Mode: 0o700, Data: []byte("#!/bin/sh\nexec \"${SHELL:-/bin/sh}\" -l\n")})

	res, err := pub.Publish(b)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !res.Published {
		t.Fatalf("first publish must publish, got %+v", res)
	}
	if res.Generation != "v10" || res.Version != "10" {
		t.Errorf("unexpected result %+v", res)
	}

	if got := statModeT(t, root).Perm(); got != 0o700 {
		t.Errorf("root mode = %04o, want 0700", got)
	}
	if got := statModeT(t, filepath.Join(root, integrationDir, "v10")).Perm(); got != 0o700 {
		t.Errorf("generation dir mode = %04o, want 0700", got)
	}
	if got := statModeT(t, filepath.Join(root, integrationDir, "v10", "nocx.bash")).Perm(); got != 0o600 {
		t.Errorf("data file mode = %04o, want 0600", got)
	}
	if got := statModeT(t, filepath.Join(root, launchName)).Perm(); got != 0o700 {
		t.Errorf("launch mode = %04o, want 0700", got)
	}
	if got := statModeT(t, filepath.Join(root, manifestName)).Perm(); got != 0o600 {
		t.Errorf("manifest mode = %04o, want 0600", got)
	}

	for _, f := range b.Files {
		if f.Name == launchName {
			continue
		}
		got := readFileT(t, filepath.Join(root, integrationDir, "v10", f.Name))
		if string(got) != string(f.Data) {
			t.Errorf("file %s differs from bundle bytes", f.Name)
		}
	}

	m := readManifestT(t, root)
	if m.Protocol != ProtocolVersion || m.Version != "10" || m.Generation != "v10" {
		t.Errorf("manifest header mismatch: %+v", m)
	}
	if len(m.Files) != 3 {
		t.Fatalf("manifest names %d files, want 3", len(m.Files))
	}
	for _, f := range b.Files {
		if f.Name == launchName {
			continue
		}
		mf, ok := m.Files[f.Name]
		if !ok {
			t.Errorf("manifest does not name %s", f.Name)
			continue
		}
		if mf.Hash != hashBytes(f.Data) {
			t.Errorf("%s hash = %s, want %s", f.Name, mf.Hash, hashBytes(f.Data))
		}
		if mf.Mode != "0600" {
			t.Errorf("%s mode = %s, want 0600", f.Name, mf.Mode)
		}
		if mf.Size != int64(len(f.Data)) {
			t.Errorf("%s size = %d, want %d", f.Name, mf.Size, len(f.Data))
		}
	}
}

// TestManifestIsOnlyActivationPointer: exactly one committed manifest names
// exactly one active generation; the older immutable generation may remain
// on disk but is unreachable from it.
func TestManifestIsOnlyActivationPointer(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	if _, err := pub.Publish(testBundle("9")); err != nil {
		t.Fatalf("publish v9: %v", err)
	}
	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("publish v10: %v", err)
	}

	m := readManifestT(t, root)
	if m.Generation != "v10" {
		t.Fatalf("manifest names %s, want v10", m.Generation)
	}
	if got := statModeT(t, filepath.Join(root, integrationDir, "v10", "nocx.bash")); got.Perm() != 0o600 {
		t.Errorf("active generation file mode = %v", got.Perm())
	}
	// The older generation is immutable and unreachable from the manifest.
	if _, err := os.Stat(filepath.Join(root, integrationDir, "v9", "nocx.bash")); err != nil {
		t.Errorf("older generation vanished: %v", err)
	}

	vr, err := pub.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.Installed || vr.Generation != "v10" || vr.Version != "10" {
		t.Errorf("Verify after v10 = %+v", vr)
	}
}

// TestVerifyRequiresEveryFileWithHashAndMode: a matching version string
// alone never proves an installation — a generation whose file was deleted
// or altered is not installed.
func TestVerifyRequiresEveryFileWithHashAndMode(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)
	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	gen := filepath.Join(root, integrationDir, "v10")

	t.Run("baseline", func(t *testing.T) {
		vr, err := pub.Verify()
		if err != nil || !vr.Installed {
			t.Fatalf("baseline Verify = %+v, %v", vr, err)
		}
	})

	t.Run("deleted file", func(t *testing.T) {
		// #nosec G306 — test fixture removal.
		if err := os.Remove(filepath.Join(gen, "nocx.zsh")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		vr, err := pub.Verify()
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if vr.Installed {
			t.Error("Verify reports installed although a manifest file was deleted")
		}
	})

	t.Run("altered bytes", func(t *testing.T) {
		// #nosec G306 — test fixture, intentionally created with restricted permissions.
		if err := os.WriteFile(filepath.Join(gen, "nocx.zsh"), []byte("echo tampered\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		vr, err := pub.Verify()
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if vr.Installed {
			t.Error("Verify reports installed although a manifest file was altered")
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(gen, "nocx.zsh"), readFileT(t, filepath.Join(gen, "nocx.posix")), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		// #nosec G302 — test fixture: wrong mode so Verify must refuse the file.
		if err := os.Chmod(filepath.Join(gen, "nocx.zsh"), 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		vr, err := pub.Verify()
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if vr.Installed {
			t.Error("Verify reports installed although a manifest file has the wrong mode")
		}
	})
}

// TestManifestRenameHappensLast: the manifest is renamed after every file
// it names exists and is fsynced, and after the generation rename. The
// recording FS makes the ordering observable.
func TestManifestRenameHappensLast(t *testing.T) {
	home := t.TempDir()
	rec := &recordingFS{FS: NewOSFS()}
	pub := NewPublisher(testLogger(), rec, filepath.Join(home, dirName))

	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var manifestRename int = -1
	var genRename int = -1
	creates := -1
	for i, op := range rec.ops {
		switch {
		case strings.Contains(op, "rename:") && strings.HasSuffix(op, manifestName):
			manifestRename = i
		case strings.Contains(op, "rename:") && strings.Contains(op, integrationDir):
			genRename = i
		case strings.HasPrefix(op, "create:"):
			creates = i
		}
	}
	if manifestRename < 0 || genRename < 0 {
		t.Fatalf("missing renames in op log: %v", rec.ops)
	}
	if manifestRename <= genRename {
		t.Errorf("manifest rename (op %d) must come after the generation rename (op %d)", manifestRename, genRename)
	}
	if creates > manifestRename {
		t.Errorf("a file create (op %d) happened after the manifest rename (op %d); the manifest must be renamed last", creates, manifestRename)
	}
}

// TestNeverDowngradesNewer: an installed newer protocol-compatible
// generation is never downgraded; equality is not the comparison — the skip
// fires for >=, not just >.
func TestNeverDowngradesNewer(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("publish v10: %v", err)
	}
	before := readFileT(t, filepath.Join(root, manifestName))

	res, err := pub.Publish(testBundle("9"))
	if err != nil {
		t.Fatalf("publish v9: %v", err)
	}
	if res.Published {
		t.Fatalf("older version published over a newer one: %+v", res)
	}
	if res.Reason != "newer-installed" {
		t.Errorf("reason = %q, want newer-installed", res.Reason)
	}
	if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
		t.Error("manifest changed despite the downgrade refusal")
	}

	res, err = pub.Publish(testBundle("10"))
	if err != nil {
		t.Fatalf("publish v10 again: %v", err)
	}
	if res.Published {
		t.Fatalf("equal version re-published: %+v", res)
	}
	if res.Reason != "already-installed" {
		t.Errorf("reason = %q, want already-installed", res.Reason)
	}
	if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
		t.Error("manifest changed despite the equality skip")
	}

	// A newer protocol we do not understand is left strictly alone. The
	// manifest must be VALID (parseable) or the publisher would treat it as
	// naming nothing and publish over it.
	newer := `{"protocol":99,"version":"99","generation":"v99","files":{"nocx.bash":{"hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mode":"0600","size":1}}}`
	if werr := os.WriteFile(filepath.Join(root, manifestName), []byte(newer), 0o600); werr != nil {
		t.Fatalf("write manifest: %v", werr)
	}
	res, err = pub.Publish(testBundle("11"))
	if err != nil {
		t.Fatalf("publish under newer protocol: %v", err)
	}
	if res.Published || res.Reason != "incompatible-protocol" {
		t.Errorf("unexpected result under newer protocol: %+v", res)
	}
	if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != newer {
		t.Error("manifest changed under a newer protocol")
	}
}

// TestPublishOverInvalidManifest: an invalid manifest names nothing, so
// nothing is active and publishing over it is safe.
func TestPublishOverInvalidManifest(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(root, manifestName), []byte(`{"protocol":1,"version":"../evil"}`), 0o600); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("publish over invalid manifest: %v", err)
	}
	m := readManifestT(t, root)
	if m.Version != "10" || m.Generation != "v10" {
		t.Errorf("manifest was not replaced: %+v", m)
	}
}

// TestSymlinkRefusalTable: a symlink anywhere on the path — root,
// manifest.json, launch, lock, tmp, integration, a generation — refuses to
// write and returns a typed reason.
func TestSymlinkRefusalTable(t *testing.T) {
	newRoot := func(t *testing.T) (pub *Publisher, root string) {
		t.Helper()
		home := t.TempDir()
		root = filepath.Join(home, dirName)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		return NewPublisher(testLogger(), NewOSFS(), root), root
	}
	symlinkAt := func(t *testing.T, path, target string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("symlink %s -> %s: %v", path, target, err)
		}
	}
	wantSymlinkError := func(t *testing.T, err error) {
		t.Helper()
		var se *SymlinkError
		if !errors.As(err, &se) {
			t.Fatalf("want SymlinkError, got %T: %v", err, err)
		}
	}

	t.Run("root", func(t *testing.T) {
		home := t.TempDir()
		target := filepath.Join(home, "elsewhere")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		root := filepath.Join(home, dirName)
		if err := os.Symlink(target, root); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		pub := NewPublisher(testLogger(), NewOSFS(), root)
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("manifest.json", func(t *testing.T) {
		pub, root := newRoot(t)
		symlinkAt(t, filepath.Join(root, manifestName), "/etc/hostname")
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("launch", func(t *testing.T) {
		pub, root := newRoot(t)
		b := testBundle("10")
		b.Files = append(b.Files, BundleFile{Name: launchName, Mode: 0o700, Data: []byte("x")})
		symlinkAt(t, filepath.Join(root, launchName), "/etc/hostname")
		_, err := pub.Publish(b)
		wantSymlinkError(t, err)
	})

	t.Run("lock", func(t *testing.T) {
		pub, root := newRoot(t)
		symlinkAt(t, filepath.Join(root, lockName), "/tmp")
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("tmp", func(t *testing.T) {
		pub, root := newRoot(t)
		symlinkAt(t, filepath.Join(root, tmpName), "/tmp")
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("integration", func(t *testing.T) {
		pub, root := newRoot(t)
		symlinkAt(t, filepath.Join(root, integrationDir), "/tmp")
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("generation", func(t *testing.T) {
		pub, root := newRoot(t)
		if err := os.MkdirAll(filepath.Join(root, integrationDir), 0o700); err != nil {
			t.Fatalf("mkdir integration: %v", err)
		}
		symlinkAt(t, filepath.Join(root, integrationDir, "v10"), "/tmp")
		_, err := pub.Publish(testBundle("10"))
		wantSymlinkError(t, err)
	})

	t.Run("verify generation file", func(t *testing.T) {
		pub, root := newRoot(t)
		if err := os.MkdirAll(filepath.Join(root, tmpName), 0o700); err != nil {
			t.Fatalf("mkdir marker: %v", err)
		}
		if _, err := pub.Publish(testBundle("10")); err != nil {
			t.Fatalf("publish: %v", err)
		}
		if err := os.Remove(filepath.Join(root, integrationDir, "v10", "nocx.bash")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		symlinkAt(t, filepath.Join(root, integrationDir, "v10", "nocx.bash"), "/etc/hostname")
		_, err := pub.Verify()
		wantSymlinkError(t, err)
	})

	t.Run("verify manifest", func(t *testing.T) {
		pub, root := newRoot(t)
		if err := os.MkdirAll(filepath.Join(root, tmpName), 0o700); err != nil {
			t.Fatalf("mkdir marker: %v", err)
		}
		if _, err := pub.Publish(testBundle("10")); err != nil {
			t.Fatalf("publish: %v", err)
		}
		if err := os.Remove(filepath.Join(root, manifestName)); err != nil {
			t.Fatalf("remove: %v", err)
		}
		symlinkAt(t, filepath.Join(root, manifestName), "/etc/hostname")
		_, err := pub.Verify()
		wantSymlinkError(t, err)
	})
}

// TestRcFilesNeverTouched: every supported publish path leaves the rc files
// byte-identical (design N4). The publisher never opens them; the snapshot
// pins it.
func TestRcFilesNeverTouched(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	for _, name := range []string{".bashrc", ".bash_profile", ".profile", ".zshrc"} {
		// #nosec G306 — test fixture, intentionally created with restricted permissions.
		if err := os.WriteFile(filepath.Join(home, name), []byte("# existing "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, v := range []string{"1", "2", "3"} {
		if _, err := pub.Publish(testBundle(v)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	for _, name := range []string{".bashrc", ".bash_profile", ".profile", ".zshrc"} {
		if got := readFileT(t, filepath.Join(home, name)); string(got) != "# existing "+name+"\n" {
			t.Errorf("%s was modified: %q", name, got)
		}
	}
}

// TestForeignRootNeverModified: an existing ~/.nocx that is not recognisably
// ours is never modified and never has its mode changed; a root that is ours
// may carry foreign files that are left alone.
func TestForeignRootNeverModified(t *testing.T) {
	t.Run("foreign root refused", func(t *testing.T) {
		home := t.TempDir()
		root := filepath.Join(home, dirName)
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// #nosec G306 — test fixture, intentionally created with restricted permissions.
		if err := os.WriteFile(filepath.Join(root, "user-notes.txt"), []byte("mine"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		pub := NewPublisher(testLogger(), NewOSFS(), root)
		_, err := pub.Publish(testBundle("10"))
		var fe *ForeignRootError
		if !errors.As(err, &fe) {
			t.Fatalf("want ForeignRootError, got %T: %v", err, err)
		}
		if got := readFileT(t, filepath.Join(root, "user-notes.txt")); string(got) != "mine" {
			t.Error("foreign file was modified")
		}
		if got := statModeT(t, root).Perm(); got != 0o750 {
			t.Errorf("foreign root mode changed: %04o", got)
		}
	})

	t.Run("ours with foreign file", func(t *testing.T) {
		pub, root, _ := newTestPublisher(t)
		if _, err := pub.Publish(testBundle("10")); err != nil {
			t.Fatalf("publish: %v", err)
		}
		// #nosec G306 — test fixture, intentionally created with restricted permissions.
		if err := os.WriteFile(filepath.Join(root, "user-notes.txt"), []byte("mine"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := pub.Publish(testBundle("11")); err != nil {
			t.Fatalf("publish over our root: %v", err)
		}
		if got := readFileT(t, filepath.Join(root, "user-notes.txt")); string(got) != "mine" {
			t.Error("foreign file inside our root was modified")
		}
	})
}

// TestAtMostTwoGenerationsAndOneStaging: the footprint converges to two
// generations and no tmp/ leftovers, sweeping AT MOST ONE stale generation
// per attempt (bound 3) — the keep-two policy implied exactly that and did
// not enforce it. Orphaned staging directories are cleared before a new
// slot is opened, and an uncommitted generation at the target version is
// removed by the commit that replaces it.
func TestAtMostTwoGenerationsAndOneStaging(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	for _, v := range []string{"1", "2", "3"} {
		if _, err := pub.Publish(testBundle(v)); err != nil {
			t.Fatalf("publish v%s: %v", v, err)
		}
	}

	// An orphaned staging dir and a crash-leftover generation dir planted
	// before the next publish.
	if err := os.MkdirAll(filepath.Join(root, tmpName, "deadbeef"), 0o700); err != nil {
		t.Fatalf("mkdir orphan staging: %v", err)
	}
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(root, tmpName, "deadbeef", "nocx.bash"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, integrationDir, "v9"), 0o700); err != nil {
		t.Fatalf("mkdir leftover gen: %v", err)
	}
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(root, integrationDir, "v9", "nocx.bash"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write leftover: %v", err)
	}

	if _, err := pub.Publish(testBundle("4")); err != nil {
		t.Fatalf("publish v4: %v", err)
	}

	m := readManifestT(t, root)
	if m.Generation != "v4" {
		t.Fatalf("manifest names %s, want v4", m.Generation)
	}
	// The staging orphan is gone the moment a slot is opened; the
	// generations converge one per attempt.
	tmpEntries, err := os.ReadDir(filepath.Join(root, tmpName))
	if err != nil {
		t.Fatalf("readdir tmp: %v", err)
	}
	if len(tmpEntries) != 0 {
		t.Errorf("tmp/ has %d leftovers after publish: %v", len(tmpEntries), names(tmpEntries))
	}
	for attempt := range 4 {
		gens, rerr := os.ReadDir(filepath.Join(root, integrationDir))
		if rerr != nil {
			t.Fatalf("readdir integration: %v", rerr)
		}
		if len(gens) <= 2 {
			break
		}
		if attempt == 3 {
			t.Fatalf("integration/ still holds %v after four further attempts", names(gens))
		}
		if _, perr := pub.Publish(testBundle("4")); perr != nil {
			t.Fatalf("converging attempt %d: %v", attempt, perr)
		}
	}
	gens, err := os.ReadDir(filepath.Join(root, integrationDir))
	if err != nil {
		t.Fatalf("readdir integration: %v", err)
	}
	if len(gens) != 2 {
		t.Errorf("integration/ has %d generations, want exactly 2: %v", len(gens), names(gens))
	}
	// Cleanup keeps the active generation and the newest other (the planted
	// v9 crash leftover outranks the retired v1/v2/v3).
	for _, g := range gens {
		if g.Name() != "v4" && g.Name() != "v9" {
			t.Errorf("unexpected surviving generation %s", g.Name())
		}
	}
}

// TestUninstallRemovesOnlyManifestOwnedUnmodified: uninstall removes only
// manifest-owned unmodified files, reports a conflict for anything the user
// changed, and never removes ~/.nocx recursively.
func TestUninstallRemovesOnlyManifestOwnedUnmodified(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)

	b := testBundle("2")
	b.Files = append(b.Files, BundleFile{Name: launchName, Mode: 0o700, Data: []byte("#!/bin/sh\nexec /bin/sh\n")})
	if _, err := pub.Publish(testBundle("1")); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := pub.Publish(b); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	// The user edits nocx.bash of the ACTIVE (v2) generation.
	gen2 := filepath.Join(root, integrationDir, "v2")
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(gen2, "nocx.bash"), []byte("user changed this\n"), 0o600); err != nil {
		t.Fatalf("write modified file: %v", err)
	}

	res, err := pub.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// The modified file is a reported conflict and stays.
	if len(res.Conflicts) != 1 || !strings.HasSuffix(res.Conflicts[0], "nocx.bash") {
		t.Errorf("conflicts = %v, want exactly the modified nocx.bash", res.Conflicts)
	}
	if got := readFileT(t, filepath.Join(gen2, "nocx.bash")); string(got) != "user changed this\n" {
		t.Error("modified file was removed or altered")
	}

	// Unmodified manifest-owned files and the manifest itself are removed.
	for _, f := range []string{"nocx.zsh", "nocx.posix"} {
		if _, err := os.Stat(filepath.Join(gen2, f)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still present after uninstall: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, manifestName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("manifest still present after uninstall: %v", err)
	}

	// Never recursive: launch, tmp and the root stay; the older generation
	// (not manifest-owned) is untouched.
	if _, err := os.Stat(filepath.Join(root, launchName)); err != nil {
		t.Errorf("launch removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, tmpName)); err != nil {
		t.Errorf("tmp removed: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, integrationDir, "v1", "nocx.bash")); err != nil {
		t.Errorf("non-manifest-owned generation touched: %v", err)
	}
	if !strings.Contains(strings.Join(res.Removed, ","), "nocx.zsh") {
		t.Errorf("removed list missing nocx.zsh: %v", res.Removed)
	}
}

func TestUninstallWithoutManifestIsNoop(t *testing.T) {
	pub, _, _ := newTestPublisher(t)
	res, err := pub.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall on empty home: %v", err)
	}
	if len(res.Removed) != 0 || len(res.Conflicts) != 0 {
		t.Errorf("unexpected result on empty home: %+v", res)
	}
}

// TestReadonlyHomeFailsCleanly: a read-only $HOME fails with a typed reason
// and writes nothing.
func TestReadonlyHomeFailsCleanly(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	// Recognisably ours so we pass the root check and fail on the first write.
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(root, manifestName), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// The boundary that must fail is under ~/.nocx itself: a read-only
	// $HOME where ~/.nocx already exists and is writable is not a read-only
	// install — nothing outside ~/.nocx is ever written.
	// #nosec G302 — test fixture: read-only root so the publish must fail with ReadonlyError.
	if chmodErr := os.Chmod(root, 0o500); chmodErr != nil {
		t.Fatalf("chmod root readonly: %v", chmodErr)
	}
	// #nosec G302 — test fixture: restore the mode this test changed.
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	pub := NewPublisher(testLogger(), NewOSFS(), root)
	_, err := pub.Publish(testBundle("10"))
	var re *ReadonlyError
	if !errors.As(err, &re) {
		t.Fatalf("want ReadonlyError, got %T: %v", err, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != manifestName {
		t.Errorf("publish wrote into a read-only home: %v", names(entries))
	}

	// Same typed reason when the root does not exist at all.
	home2 := t.TempDir()
	// #nosec G302 — test fixture: read-only home so the publish must fail with ReadonlyError.
	if chmodErr := os.Chmod(home2, 0o500); chmodErr != nil {
		t.Fatalf("chmod home2 readonly: %v", chmodErr)
	}
	// #nosec G302 — test fixture: restore the mode this test changed.
	t.Cleanup(func() { _ = os.Chmod(home2, 0o700) })
	pub2 := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home2, dirName))
	_, err = pub2.Publish(testBundle("10"))
	if !errors.As(err, &re) {
		t.Fatalf("want ReadonlyError for missing root, got %T: %v", err, err)
	}
}

// TestLockStaleRuleBreaks: a lock left by a crashed publisher (dir plus
// nonce, with and without its staging dir) does not strand the next
// publish — K probes elapse, the stale rule applies and the attempt
// converges with no manual cleanup.
//
// What it does NOT assert is elapsed time. The old form of this test
// checked that at least half the bounded wait had passed on the wall clock,
// which is a stopwatch reading: it passes or fails on how busy the machine
// is. The bound is now read where it is decided — the number of probes the
// publisher asked to wait for, and the injected time they added.
func TestLockStaleRuleBreaks(t *testing.T) {
	for _, tc := range []struct{ name, staging string }{
		{"nonce without staging", "deadbeef"},
		{"nonce with staging dir present", "cafebabe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, dirName)
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("mkdir root: %v", err)
			}
			// #nosec G306 — test fixture, intentionally created with restricted permissions.
			if err := os.WriteFile(filepath.Join(root, manifestName), []byte("{}"), 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			// Simulate a crashed holder: lock dir with a nonce naming its
			// staging dir, which may or may not still exist.
			if err := os.MkdirAll(filepath.Join(root, lockName), 0o700); err != nil {
				t.Fatalf("mkdir lock: %v", err)
			}
			// #nosec G306 — test fixture, intentionally created with restricted permissions.
			if err := os.WriteFile(filepath.Join(root, lockName, lockNonceFile), []byte(tc.staging), 0o600); err != nil {
				t.Fatalf("write nonce: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, tmpName, tc.staging), 0o700); err != nil {
				t.Fatalf("mkdir staging: %v", err)
			}

			pub := NewPublisher(testLogger(), NewOSFS(), root)
			clock := newFakeClock().install(pub)
			res, err := pub.Publish(testBundle("10"))
			if err != nil {
				t.Fatalf("publish under stale lock: %v", err)
			}
			if !res.Published {
				t.Fatalf("expected publish, got %+v", res)
			}
			if got := clock.waitCount(); got != lockProbes {
				t.Errorf("the stale rule applied after %d probes, want K = %d", got, lockProbes)
			}
			if got := clock.waited(); got != lockProbeBudget {
				t.Errorf("probing added %v of injected time, want the %v budget", got, lockProbeBudget)
			}
			if _, err := os.Stat(filepath.Join(root, lockName)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("lock dir still present after publish: %v", err)
			}
			// The crashed holder's staging directory is residue the new
			// attempt cleared before opening its own slot (bound 1).
			if _, err := os.Stat(filepath.Join(root, tmpName, tc.staging)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the crashed holder's staging dir survived: %v", err)
			}
		})
	}
}

// TestVerifyOnMissingInstall: an empty or missing root verifies as not
// installed without error.
func TestVerifyOnMissingInstall(t *testing.T) {
	pub, _, _ := newTestPublisher(t)
	vr, err := pub.Verify()
	if err != nil {
		t.Fatalf("Verify on missing root: %v", err)
	}
	if vr.Installed {
		t.Error("Verify reports installed on a missing root")
	}

	home := t.TempDir()
	root := filepath.Join(home, dirName)
	if mkErr := os.MkdirAll(root, 0o700); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	pub2 := NewPublisher(testLogger(), NewOSFS(), root)
	vr, err = pub2.Verify()
	if err != nil {
		t.Fatalf("Verify on empty root: %v", err)
	}
	if vr.Installed {
		t.Error("Verify reports installed on an empty root")
	}
}

// TestManifestEntrySymlinkInvalidates: a manifest whose entry resolves to a
// symlink on disk invalidates the whole manifest — Verify refuses rather
// than reporting a partial install. All three entries are symlinked so the
// check cannot depend on map iteration order.
func TestManifestEntrySymlinkInvalidates(t *testing.T) {
	pub, home, _ := newTestPublisher(t)
	root := filepath.Join(home, dirName)
	if _, err := pub.Publish(testBundle("10")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Point the manifest at a different generation whose files are symlinks.
	m := readManifestT(t, root)
	m.Generation = "v11"
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if mkErr := os.MkdirAll(filepath.Join(root, integrationDir, "v11"), 0o700); mkErr != nil {
		t.Fatalf("mkdir v11: %v", mkErr)
	}
	for name := range m.Files {
		if linkErr := os.Symlink("/etc/hostname", filepath.Join(root, integrationDir, "v11", name)); linkErr != nil {
			t.Fatalf("symlink %s: %v", name, linkErr)
		}
	}
	if writeErr := os.WriteFile(filepath.Join(root, manifestName), data, 0o600); writeErr != nil {
		t.Fatalf("write manifest: %v", writeErr)
	}
	_, err = pub.Verify()
	var se *SymlinkError
	if !errors.As(err, &se) {
		t.Fatalf("want SymlinkError from Verify, got %T: %v", err, err)
	}
}
