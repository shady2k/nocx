package skill

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type fakeFS struct {
	inner      OSFileSystem
	failRename error
	failWrite  error
	// failRemove makes the undo of a write fail too, which is how
	// install_test reaches the one arm of the install interval that cannot
	// be closed by code.
	failRemove error
	// failWriteUnder makes only the writes whose path contains this fragment
	// fail. A bundle is several writes and the interesting failure is the
	// THIRD one — failWrite above fails the first and never reaches the case
	// where some of the bundle has already landed.
	failWriteUnder string
	// failMkdirUnder does the same for the directory a support file needs.
	failMkdirUnder string
	mkdirCalls     int
}

func (f *fakeFS) MkdirAll(path string, perm os.FileMode) error {
	f.mkdirCalls++
	if f.failMkdirUnder != "" && strings.Contains(filepath.ToSlash(path), f.failMkdirUnder) {
		return errors.New("permission denied")
	}
	return f.inner.MkdirAll(path, perm)
}

func (f *fakeFS) OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	file, err := f.inner.OpenFile(name, flag, perm)
	if err != nil {
		return file, err
	}
	if f.failWriteUnder != "" && strings.Contains(filepath.ToSlash(name), f.failWriteUnder) {
		return &failingWriteCloser{WriteCloser: file, err: errWriteFailed}, nil
	}
	if f.failWrite == nil {
		return file, err
	}
	return &failingWriteCloser{WriteCloser: file, err: f.failWrite}, nil
}

func (f *fakeFS) Rename(oldPath, newPath string) error {
	if f.failRename != nil {
		return f.failRename
	}
	return f.inner.Rename(oldPath, newPath)
}

func (f *fakeFS) Sync(path string) error { return f.inner.Sync(path) }

func (f *fakeFS) Remove(path string) error {
	if f.failRemove != nil {
		return f.failRemove
	}
	return f.inner.Remove(path)
}

type failingWriteCloser struct {
	io.WriteCloser
	err error
}

func (f *failingWriteCloser) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, f.err
	}
	return len(p) / 2, f.err
}

func TestUpdateLeavesThePreviousVersionOnAFailedRename(t *testing.T) {
	root := t.TempDir()
	fsys := &fakeFS{}
	store := NewStore(fsys, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "original body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}

	fsys.failRename = errors.New("no space left on device")
	if updateErr := store.Update("deploy", "d", "replacement body"); updateErr == nil {
		t.Fatal("want the failed rename reported")
	}

	after, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed update destroyed the previous valid skill")
	}
}

func TestUpdateLeavesThePreviousVersionOnAFailedWrite(t *testing.T) {
	root := t.TempDir()
	fsys := &fakeFS{}
	store := NewStore(fsys, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "original body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}

	fsys.failWrite = errors.New("short write")
	if updateErr := store.Update("deploy", "d", "replacement body"); updateErr == nil {
		t.Fatal("want the failed write reported")
	}

	after, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed update destroyed the previous valid skill")
	}
}

func TestCreateCompletesALeftoverEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)

	if err := store.Create("deploy", "d", "body"); err != nil {
		t.Fatalf("a crash between mkdir and write must not make the name unusable: %v", err)
	}
	got, err := Read([]Root{{Dir: root, Provenance: ProvenanceManaged}}, "deploy", "")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.TrimSpace(string(got.Bytes)) != "body" {
		t.Fatalf("body = %q, want %q", got.Bytes, "body")
	}
	if idx := Discover([]Root{{Dir: root, Provenance: ProvenanceManaged}}); len(idx) != 1 {
		t.Fatalf("want the completed skill discoverable, got %d", len(idx))
	}
}

func TestCreateRefusesAnAuthoredName(t *testing.T) {
	authored, managed := t.TempDir(), t.TempDir()
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: mine", "a")
	store := NewStore(OSFileSystem{}, []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: managed, Provenance: ProvenanceManaged},
	}, nil)

	err := store.Create("deploy", "d", "b")
	if err == nil {
		t.Fatal("want the collision refused")
	}
	if !strings.Contains(err.Error(), "you wrote") {
		t.Fatalf("the refusal must name that the skill is the person's, got %q", err)
	}
}

func TestCreateRefusesABuiltinName(t *testing.T) {
	managed := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{
		{FS: builtinFSForTest(), Provenance: ProvenanceBuiltin},
		{Dir: managed, Provenance: ProvenanceManaged},
	}, nil)

	err := store.Create("skill-authoring", "d", "b")
	if err == nil {
		t.Fatal("want the builtin collision refused")
	}
	if !strings.Contains(err.Error(), "skill-authoring") {
		t.Fatalf("refusal = %q, want the name", err)
	}
}

func TestCreateNormalizesNameAndSanitizesDescription(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("  Deploy-Now ", "line\nfeed\u200b", "body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "deploy-now", "SKILL.md")) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nname: deploy-now\ndescription: \"linefeed\"\n---\nbody"
	if string(data) != want {
		t.Fatalf("SKILL.md = %q, want %q", data, want)
	}
}

func TestWriteValidationRefusesBeforeTouchingFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := &fakeFS{}
	store := NewStore(fsys, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	cases := []struct {
		name string
		desc string
		body string
	}{
		{name: "", desc: "d", body: "b"},
		{name: "-bad", desc: "d", body: "b"},
		{name: "bad/name", desc: "d", body: "b"},
		{name: "ok", desc: "d", body: "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fsys.mkdirCalls
			if err := store.Create(tc.name, tc.desc, tc.body); err == nil {
				t.Fatal("want invalid call refused")
			}
			if fsys.mkdirCalls != before {
				t.Fatal("invalid call touched the filesystem")
			}
		})
	}
}

func TestUpdateAndDeleteMissingTargetsRefuse(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Update("missing", "d", "body"); err == nil {
		t.Fatal("want update of missing target refused")
	}
	if err := store.Delete("missing"); err == nil {
		t.Fatal("want delete of missing target refused")
	}
}

func TestUpdateRefusesMultiplyLinkedFile(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "body"); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "deploy", "SKILL.md")
	linked := filepath.Join(root, "deploy", "linked")
	if err := os.Link(original, linked); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := store.Update("deploy", "d", "replacement"); err == nil {
		t.Fatal("want multiply-linked update refused")
	}
}

func TestDeleteRemovesManagedSkillAndSyncsDirectory(t *testing.T) {
	root := t.TempDir()
	fsys := &fakeFS{}
	store := NewStore(fsys, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "body"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("deploy"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "deploy", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SKILL.md still exists or stat failed: %v", err)
	}
	if got := Discover([]Root{{Dir: root, Provenance: ProvenanceManaged}}); len(got) != 0 {
		t.Fatalf("deleted skill still discoverable: %+v", got)
	}
}

func writeExistingSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n"+frontmatter+"\n---\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func builtinFSForTest() fs.FS {
	return fstest.MapFS{
		"skill-authoring/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: skill-authoring\ndescription: builtin\n---\nbody")},
	}
}

func TestCreateRefusesExistingManagedName(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "body"); err != nil {
		t.Fatal(err)
	}
	if err := store.Create("deploy", "d", "body two"); err == nil {
		t.Fatal("want create of an existing managed skill refused")
	}
}

func TestCreateRefusesAnOversizedSkill(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", strings.Repeat("x", maxSkillFileBytes)); err == nil {
		t.Fatal("want oversized skill refused")
	}
	if _, err := os.Stat(filepath.Join(root, "deploy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized create touched the filesystem: %v", err)
	}
}

func TestUpdateRefusesAnAuthoredName(t *testing.T) {
	authored, managed := t.TempDir(), t.TempDir()
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: mine", "a")
	store := NewStore(OSFileSystem{}, []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: managed, Provenance: ProvenanceManaged},
	}, nil)
	if err := store.Update("deploy", "d", "b"); err == nil || !strings.Contains(err.Error(), "you wrote") {
		t.Fatalf("update error = %v, want authored-name refusal", err)
	}
}

func TestUpdateRefusesASymlinkedSkillFile(t *testing.T) {
	managed, outside := t.TempDir(), t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: managed, Provenance: ProvenanceManaged}}, nil)
	dir := filepath.Join(managed, "deploy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(outside, "SKILL.md")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := store.Update("deploy", "d", "replacement"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("update error = %v, want symlink escape refusal", err)
	}
	got, err := os.ReadFile(foreign) //nolint:gosec // test path is inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "foreign" {
		t.Fatal("symlink target was modified")
	}
}

func TestUpdateRefusesANonRegularSkillFile(t *testing.T) {
	managed := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: managed, Provenance: ProvenanceManaged}}, nil)
	path := filepath.Join(managed, "deploy", "SKILL.md")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Update("deploy", "d", "replacement"); err == nil {
		t.Fatal("want non-regular skill file refused")
	}
}

func TestStoreRefusesASymlinkedManagedRoot(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "managed-skills")
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", "d", "body"); err == nil {
		t.Fatal("want symlinked managed root refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "deploy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was touched: %v", err)
	}
}

func TestRestoreSnapshotRefusesSymlinkedNestedDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "deploy", "references")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	err := store.RestoreSnapshot(Snapshot{
		Managed: []SnapshotTree{{
			Name:  "deploy",
			Files: []SnapshotFile{{Path: "references/guide.md", Bytes: "must not escape"}},
		}},
	})
	if err == nil {
		t.Fatal("want restore to refuse a symlinked nested directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "guide.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore escaped through nested symlink, stat error = %v", err)
	}
}
