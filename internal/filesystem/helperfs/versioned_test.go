package helperfs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// conditionalBinding puts a real helper-backed provider behind a real
// filesystem binding and answers the Handle — the same value the Files panel
// and the assistant reach. C27 is about the WIRE and the HOST, so the fixture
// is the production one on both ends: the host service the helper registers,
// the client over the fixture's pipe, the registry's real axis, and
// filesystem.Registry's real use-guard.
func conditionalBinding(t *testing.T) (filesystem.Handle, string) {
	t.Helper()
	root := tempTree(t)
	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root)))}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	bindings := filesystem.New()
	id, err := bindings.Register(p, "s1", "v1:abc", filesystem.Capabilities{
		Axis:      p.HelperAxis,
		Versioned: p.Versioned(),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	h, release, err := bindings.Acquire(id, owningEverything{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)
	return h, root
}

func mustVersioned(t *testing.T, h filesystem.Handle) filesystem.VersionedFiles {
	t.Helper()
	v, err := h.Versioned()
	if err != nil {
		t.Fatalf("Versioned: %v", err)
	}
	return v
}

// TestAConditionalWriteRefusesAFileChangedSinceItWasRead is C27's refusal,
// end to end through the handle: the file is read with its version, somebody
// else writes it, and the commit is refused ON THE HOST with the data the
// outsider wrote still in place. The trailing check that no temporary is left
// behind is the other half of the same claim: refusing a write must not cost
// the host a file.
func TestAConditionalWriteRefusesAFileChangedSinceItWasRead(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	path := filepath.Join(root, "note")
	writeFile(t, path, []byte("first"))

	data, version, err := write.ReadVersion(ctx, path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if string(data) != "first" || version.Size != 5 || version.SHA256 == "" || version.MtimeNs == 0 {
		t.Fatalf("ReadVersion = %q, %+v; want the bytes and a full version of them", data, version)
	}

	// The outsider wins the race. A different length is what makes the
	// refusal unambiguous even on a filesystem whose timestamp granule is
	// coarser than this test.
	writeFile(t, path, []byte("somebody else wrote this"))

	_, err = write.Commit(ctx, path, []byte("second"), filesystem.Expect{Version: &version})
	var changed *filesystem.ErrChangedSinceRead
	if !errors.As(err, &changed) {
		t.Fatalf("Commit over a changed file = %v, want *filesystem.ErrChangedSinceRead", err)
	}
	if changed.Path != path || changed.Reason == "" {
		t.Errorf("refusal = %+v, want the path and the host's reason", changed)
	}
	if got := readFile(t, path); string(got) != "somebody else wrote this" {
		t.Fatalf("the file now holds %q; a refused write must leave the outsider's bytes in place", got)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteInstallsWhenTheFileIsUnchanged is C27's success: the
// same read, no outside change, and the commit installs — answering the
// version the bytes landed with. That version is checked against a fresh read
// of the installed file, because a version that describes something other
// than what is now on disk would be a fence the next commit refuses forever.
func TestAConditionalWriteInstallsWhenTheFileIsUnchanged(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	path := filepath.Join(root, "note")
	writeFile(t, path, []byte("first"))

	_, version, err := write.ReadVersion(ctx, path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	const replacement = "the bytes that replace it"
	installed, err := write.Commit(ctx, path, []byte(replacement), filesystem.Expect{Version: &version})
	if err != nil {
		t.Fatalf("Commit over an unchanged file: %v", err)
	}
	sum := sha256.Sum256([]byte(replacement))
	want := filesystem.Version{Size: int64(len(replacement)), MtimeNs: installed.MtimeNs, SHA256: hex.EncodeToString(sum[:])}
	if installed != want || installed.MtimeNs == 0 {
		t.Fatalf("Commit answered %+v, want the installed bytes' size and digest (%+v)", installed, want)
	}
	if got := readFile(t, path); string(got) != replacement {
		t.Fatalf("the file holds %q, want the committed bytes", got)
	}
	again, readBack, err := write.ReadVersion(ctx, path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion after the commit: %v", err)
	}
	if string(again) != replacement || readBack != installed {
		t.Fatalf("a read after the commit answers %q, %+v; want the committed bytes and the version the commit answered", again, readBack)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteRefusesANameThatIsTaken is the `absent` condition on
// both ends: an occupied name is refused and untouched, a free one is created.
// The refusal must satisfy BOTH readings — errors.As for the panel's message
// and errors.Is(fs.ErrExist) for the upload sink's KeepBoth retry — because
// losing either one silently changes what a collision means to a caller.
func TestAConditionalWriteRefusesANameThatIsTaken(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	taken := filepath.Join(root, "taken")
	writeFile(t, taken, []byte("somebody's file"))

	_, err := write.Commit(ctx, taken, []byte("mine"), filesystem.Expect{Absent: true})
	var exists *filesystem.ErrExists
	if !errors.As(err, &exists) {
		t.Fatalf("Commit(absent) over an existing name = %v, want *filesystem.ErrExists", err)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Error("the refusal does not satisfy errors.Is(fs.ErrExist); the upload sink's KeepBoth retry keys on that sentinel")
	}
	if exists.Path != taken {
		t.Errorf("refusal names %q, want %q", exists.Path, taken)
	}
	if got := readFile(t, taken); string(got) != "somebody's file" {
		t.Fatalf("the existing file now holds %q; expect.absent must replace nothing", got)
	}
	assertNoTemporaries(t, root)

	free := filepath.Join(root, "free")
	version, err := write.Commit(ctx, free, []byte("mine"), filesystem.Expect{Absent: true})
	if err != nil {
		t.Fatalf("Commit(absent) at a free name: %v", err)
	}
	if version.Size != 4 || version.SHA256 == "" {
		t.Fatalf("Commit answered %+v, want the created bytes' version", version)
	}
	if got := readFile(t, free); string(got) != "mine" {
		t.Fatalf("the created file holds %q, want the committed bytes", got)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteRefusesAnOccupiedNameWithoutFollowingTheLink is the
// `absent` condition meeting a symlink. The caller's name is occupied by the
// LINK, and what the link points at is somebody else's file with a different
// name in a different directory.
//
// Following the final component, which is what an unconditional write does,
// would create a second file inside the target's directory — under a name the
// caller never asked for — and answer it as a success. `absent` is a question
// about the name the caller gave, so the last component must stay the caller's.
func TestAConditionalWriteRefusesAnOccupiedNameWithoutFollowingTheLink(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	other := tempTree(t)
	real := filepath.Join(other, "real.conf")
	writeFile(t, real, []byte("theirs"))
	link := filepath.Join(root, "config")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	_, err := write.Commit(ctx, link, []byte("mine"), filesystem.Expect{Absent: true})
	var exists *filesystem.ErrExists
	if !errors.As(err, &exists) {
		t.Fatalf("Commit(absent) over an existing symlink = %v, want *filesystem.ErrExists", err)
	}
	if exists.Path != link {
		t.Errorf("refusal names %q, want the caller's name %q", exists.Path, link)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Error("the refusal does not satisfy errors.Is(fs.ErrExist); the upload sink's KeepBoth retry keys on that sentinel")
	}
	if got := readFile(t, real); string(got) != "theirs" {
		t.Fatalf("the link's target now holds %q; expect.absent must replace nothing", got)
	}
	if got := namesIn(t, other); len(got) != 1 || got[0] != "real.conf" {
		t.Fatalf("the target's directory holds %v, want only real.conf; the write landed beside the link's target", got)
	}
	if info, lerr := os.Lstat(link); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink did not survive the refusal (lstat: %v, mode %v)", lerr, info.Mode())
	}
	assertNoTemporaries(t, root)
	assertNoTemporaries(t, other)
}

// TestAConditionalWriteRefusesAnOccupiedDirectory is the same condition meeting
// a directory. A directory is a taken name, not a non-regular file to be
// reported as something else: the caller asked whether the name is free and it
// is not.
func TestAConditionalWriteRefusesAnOccupiedDirectory(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	dir := filepath.Join(root, "sub")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := write.Commit(testContext(t), dir, []byte("mine"), filesystem.Expect{Absent: true})
	var exists *filesystem.ErrExists
	if !errors.As(err, &exists) {
		t.Fatalf("Commit(absent) over an existing directory = %v, want *filesystem.ErrExists", err)
	}
	if got := namesIn(t, root); len(got) != 1 || got[0] != "sub" {
		t.Fatalf("the directory holds %v, want only sub", got)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteRefusesAnOccupiedDanglingLink is the third face of the
// same name: a symlink to nothing is still a directory entry, so the name is
// taken. Resolving it would find no file and let the write through.
func TestAConditionalWriteRefusesAnOccupiedDanglingLink(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "nowhere"), link); err != nil {
		t.Fatal(err)
	}

	_, err := write.Commit(testContext(t), link, []byte("mine"), filesystem.Expect{Absent: true})
	var exists *filesystem.ErrExists
	if !errors.As(err, &exists) {
		t.Fatalf("Commit(absent) over a dangling symlink = %v, want *filesystem.ErrExists", err)
	}
	if info, lerr := os.Lstat(link); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the dangling link did not survive the refusal (lstat: %v, mode %v)", lerr, info.Mode())
	}
	if _, lerr := os.Lstat(filepath.Join(root, "nowhere")); !errors.Is(lerr, fs.ErrNotExist) {
		t.Fatalf("the refusal created the link's absent target (stat: %v)", lerr)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteRefusesWhenTheFileIsGone is the third way the same
// condition can fail: the file was removed after the read, and the host says
// so rather than resurrecting it under a version it never held.
func TestAConditionalWriteRefusesWhenTheFileIsGone(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	path := filepath.Join(root, "note")
	writeFile(t, path, []byte("first"))

	_, version, err := write.ReadVersion(ctx, path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	removeErr := os.Remove(path)
	if removeErr != nil {
		t.Fatal(removeErr)
	}

	_, err = write.Commit(ctx, path, []byte("second"), filesystem.Expect{Version: &version})
	var changed *filesystem.ErrChangedSinceRead
	if !errors.As(err, &changed) {
		t.Fatalf("Commit over a removed file = %v, want *filesystem.ErrChangedSinceRead", err)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the refused commit created %s (stat: %v)", path, statErr)
	}
	assertNoTemporaries(t, root)
}

// TestAConditionalWriteThroughASymlinkedDirectoryLandsOnTheRealFile is the
// macOS rule from AGENTS.md stated as a test: the tree is reached through a
// symlink, so a spelling that agrees only when both paths happen to be the
// same is wrong here on Linux too rather than on the one platform we ship.
//
// What the caller sees must stay the caller's spelling — the version of the
// file they read through the link — while the bytes land on the real file and
// the link survives.
func TestAConditionalWriteThroughASymlinkedDirectoryLandsOnTheRealFile(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	ctx := testContext(t)
	link := filepath.Join(tempTree(t), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	realPath, linkedPath := filepath.Join(root, "note"), filepath.Join(link, "note")
	writeFile(t, realPath, []byte("first"))

	_, version, err := write.ReadVersion(ctx, linkedPath, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion through a symlinked directory: %v", err)
	}
	_, commitErr := write.Commit(ctx, linkedPath, []byte("second"), filesystem.Expect{Version: &version})
	if commitErr != nil {
		t.Fatalf("Commit through a symlinked directory: %v", commitErr)
	}
	if got := readFile(t, realPath); string(got) != "second" {
		t.Fatalf("the real file holds %q, want the committed bytes", got)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink did not survive the write (lstat: %v, mode %v)", err, info.Mode())
	}
	assertNoTemporaries(t, root)
}

// TestAWholeFileReadRefusesAFileBiggerThanItsBound pins why ReadVersion
// answers a version rather than a prefix: a prefix has no version of the file,
// and a caller that received one would fence a commit on bytes it never saw.
func TestAWholeFileReadRefusesAFileBiggerThanItsBound(t *testing.T) {
	h, root := conditionalBinding(t)
	write := mustVersioned(t, h)
	path := filepath.Join(root, "long")
	const body = "more than four bytes"
	writeFile(t, path, []byte(body))

	_, _, err := write.ReadVersion(testContext(t), path, 4)
	var limit *filesystem.ErrByteLimit
	if !errors.As(err, &limit) {
		t.Fatalf("ReadVersion over its bound = %v, want *filesystem.ErrByteLimit", err)
	}
	if limit.Observed != int64(len(body)) || limit.Limit != 4 {
		t.Errorf("refusal = %+v, want observed %d and limit 4", limit, len(body))
	}
}

// namesIn lists a directory's entries, so a test can state that a refusal added
// nothing to it rather than only that it added no temporary.
func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertNoTemporaries is the "a refusal costs the host nothing" half: every
// write session this seam opens is finished by a commit or an abort, so no
// `.nocx-write-<id>` may survive the call that opened it.
func assertNoTemporaries(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nocx-write-") {
			t.Fatalf("the write left a temporary behind: %s", filepath.Join(root, e.Name()))
		}
	}
}
