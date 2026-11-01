package shellintegration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestOSFSMkdirModesExact: directories 0700 and data 0600 are applied
// exactly, never left to umask. With the default 022 umask, a 0666-style
// create would yield 0644 — the exact-mode assertions catch that.
func TestOSFSMkdirModesExact(t *testing.T) {
	home := t.TempDir()
	fsys := NewOSFS()

	dir := filepath.Join(home, "d")
	if err := fsys.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if got := statModeT(t, dir).Perm(); got != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", got)
	}

	f, err := fsys.Create(filepath.Join(dir, "f"), 0o600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := statModeT(t, filepath.Join(dir, "f")).Perm(); got != 0o600 {
		t.Errorf("file mode = %04o, want 0600", got)
	}
}

// TestOSFSLstatSeesSymlink: the seam reports a symlink as a symlink so the
// publisher can refuse to write through it.
func TestOSFSLstatSeesSymlink(t *testing.T) {
	home := t.TempDir()
	fsys := NewOSFS()
	target := filepath.Join(home, "target")
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	info, err := fsys.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Lstat did not report the symlink mode")
	}
}

// TestOSFSReadDirLstatSemantics: ReadDir reports entries with lstat
// semantics; a symlink entry is never followed.
func TestOSFSReadDirLstatSemantics(t *testing.T) {
	home := t.TempDir()
	fsys := NewOSFS()
	if err := fsys.Mkdir(filepath.Join(home, "d"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(home, "d", "evil")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	entries, err := fsys.ReadDir(filepath.Join(home, "d"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Mode()&os.ModeSymlink == 0 {
		t.Error("ReadDir followed or hid the symlink entry")
	}
}

// TestReadDirSkipsAnEntryRemovedWhileListing: two publishers share the root,
// and one finishing its publish removes its lock and staging entries while
// the other is still reading the directory. The listing is taken first and
// each name is lstat'ed after; a name that is gone by then is not a failed
// read — the directory was read, and that entry no longer exists
// (nocx-1ebrs.66.1.34: `first publisher: readdir …: lstat …`).
func TestReadDirSkipsAnEntryRemovedWhileListing(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "kept"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write kept: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "staged"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	// The listing the reader holds when the other publisher removes its entry.
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if rmErr := os.Remove(filepath.Join(home, "staged")); rmErr != nil {
		t.Fatalf("remove staged: %v", rmErr)
	}
	infos, err := dirInfos(entries)
	if err != nil {
		t.Fatalf("a removed entry failed the read: %v", err)
	}
	if len(infos) != 1 || infos[0].Name() != "kept" {
		got := make([]string, 0, len(infos))
		for _, i := range infos {
			got = append(got, i.Name())
		}
		t.Fatalf("entries = %v, want only the one still present", got)
	}
}

// A failure that is not a disappearance is still a failed read: permission,
// I/O and everything else must surface, or a directory this process cannot
// read would look empty.
func TestReadDirKeepsAnEntryItCannotStat(t *testing.T) {
	home := t.TempDir()
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(home, "f"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write f: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	denied := dirEntryFunc{name: "f", info: func() (fs.FileInfo, error) {
		return nil, &fs.PathError{Op: "lstat", Path: "f", Err: fs.ErrPermission}
	}}
	if _, err := dirInfos(append(entries, denied)); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("a permission failure read as: %v, want it returned", err)
	}
}

type dirEntryFunc struct {
	name string
	info func() (fs.FileInfo, error)
}

func (e dirEntryFunc) Name() string               { return e.name }
func (e dirEntryFunc) IsDir() bool                { return false }
func (e dirEntryFunc) Type() fs.FileMode          { return 0 }
func (e dirEntryFunc) Info() (fs.FileInfo, error) { return e.info() }

// TestOSFSRenameAndRemove: the seam's rename is atomic (dst replaced) and
// remove reports fs.ErrNotExist for absent paths.
func TestOSFSRenameAndRemove(t *testing.T) {
	home := t.TempDir()
	fsys := NewOSFS()
	src := filepath.Join(home, "src")
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(home, "dst")
	// #nosec G306 — test fixture, intentionally created with restricted permissions.
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	if err := fsys.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := readFileT(t, dst); string(got) != "payload" {
		t.Errorf("dst = %q after rename", got)
	}
	if _, err := fsys.Lstat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("src still present after rename: %v", err)
	}
	if err := fsys.Remove(dst); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := fsys.Remove(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("second Remove = %v, want fs.ErrNotExist", err)
	}
}

// TestOSFSSyncDirIsABoundary: SyncDir on a real directory succeeds (the
// publisher fsyncs tmp, staging, integration and the root); the durability
// scope is stated, not assumed.
func TestOSFSSyncDirIsABoundary(t *testing.T) {
	home := t.TempDir()
	fsys := NewOSFS()
	if err := fsys.SyncDir(home); err != nil {
		t.Fatalf("SyncDir on a directory: %v", err)
	}
}
