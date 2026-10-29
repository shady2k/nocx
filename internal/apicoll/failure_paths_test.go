package apicoll

// Design §12.1, AGENTS.md testing rule 3: every external call this package
// makes has a test where it fails, and each of those has its "and on an
// ordinary machine it succeeds" partner.
//
// failure_test.go already covers the calls that fail on ONE FILE — an
// unreadable manifest, an unreadable request, a store whose write refuses.
// This file covers the three the folder itself fails at, which are a
// different question and reach different code: the DIRECTORY cannot be
// listed, the DIRECTORY refuses a write, and the directory the handle was
// minted on has been swapped for something else.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// skipIfRoot: mode bits do not deny root, so a permission test run as root
// asserts nothing at all. Named once rather than repeated, and it SKIPS
// rather than passing quietly — a permission test that silently succeeded
// under root would be a green test for an unexercised path.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny this process")
	}
}

// chmodDir changes a directory's mode for the duration of the test and puts
// it back, so t.TempDir's cleanup can still remove the tree.
func chmodDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	//nolint:gosec // G302 is about files: a DIRECTORY needs its execute bit, and
	// 0o600 would leave a tree t.TempDir's cleanup could not walk or remove.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// seedCollection builds a folder with a manifest and one request in it, and
// returns the root. Both halves of every pair below start from exactly this,
// so the only difference between a failure and its partner is the failure.
func seedCollection(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "req.json", requestJSON("1", "one", "GET", "http://x/a"))
	return root
}

// ─── 1. the collection folder cannot be read ───────────────────────────────

// TestOpen_AnUnlistableFolderIsReportedNotAnsweredAsEmpty is the soft degrade
// AGENTS.md forbids, asked of this folder: a directory that can be TRAVERSED
// but not LISTED (mode 0o300, which is what `chmod 0300` gives you) lets the
// manifest and every known file be read while WalkDir's ReadDir on the root
// refuses. Open and List must report that failure, not claim that the
// request still on disk is absent (nocx-cpok3).
func TestOpen_AnUnlistableFolderIsReportedNotAnsweredAsEmpty(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	chmodDir(t, root, 0o300) // write + traverse, no read: openable, not listable

	if _, err := newService().Open(root); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Open on an unlistable folder = %v, want the listing permission error", err)
	}
}

// List must also report a listing failure after the collection was opened.
// Traverse permission still lets ReadRequest read the known request by name.
func TestList_AnUnlistableFolderIsReportedNotAnsweredAsEmpty(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	chmodDir(t, root, 0o300)
	if _, err := svc.ReadRequest(op.Handle, "req.json"); err != nil {
		t.Fatalf("ReadRequest by name in an unlistable folder: %v", err)
	}
	if _, err := svc.List(op.Handle); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("List on an unlistable folder = %v, want the listing permission error", err)
	}
}

// TestOpen_AFolderThatCannotBeTraversedIsReported is the harder deny (0o000):
// nothing inside can be opened at all. It must not come back as ErrNoManifest
// — "that folder is not a collection" invites the caller to offer to create
// one over the top of the user's files.
func TestOpen_AFolderThatCannotBeTraversedIsReported(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	chmodDir(t, root, 0o000)

	if _, err := newService().Open(root); err == nil {
		t.Fatal("Open succeeded on a folder this process cannot read at all")
	} else if errors.Is(err, ErrNoManifest) {
		t.Errorf("an unreadable folder was reported as having no manifest: %v", err)
	}
}

// TestOpen_ListsTheFolderOnceItIsReadable pairs the unlistable case above
// with the SAME folder, manifest and request once listing is permitted.
func TestOpen_ListsTheFolderOnceItIsReadable(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	chmodDir(t, root, 0o300)
	if _, err := newService().Open(root); err == nil {
		t.Fatal("Open succeeded on an unlistable folder; the pair below would prove nothing")
	}

	//nolint:gosec // G302 is about files; a directory needs its execute bit.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open on a readable folder: %v", err)
	}
	h, coll := op.Handle, op.Collection
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "req.json" {
		t.Fatalf("Requests = %+v, want the one request on disk", coll.Requests)
	}
	if len(coll.Malformed) != 0 {
		t.Fatalf("Malformed = %+v, want no malformed refs in a listable folder", coll.Malformed)
	}
	listed, err := svc.List(h)
	if err != nil {
		t.Fatalf("List on a readable folder: %v", err)
	}
	if len(listed.Requests) != 1 || listed.Requests[0].RelPath != "req.json" || len(listed.Malformed) != 0 {
		t.Fatalf("List = %+v, want the one request and no malformed refs", listed)
	}
	got, err := svc.ReadRequest(h, "req.json")
	if err != nil {
		t.Fatalf("ReadRequest through the handle: %v", err)
	}
	if got.Name != "one" {
		t.Fatalf("read back %+v, want the request on disk", got)
	}
}

// ─── 2. the collection folder refuses a write ──────────────────────────────

// TestWriteRequest_ReportsAReadOnlyFolder: a collection checked out read-only
// — a pull request's worktree, a mounted share, a folder somebody chmodded —
// must refuse the save and SAY SO. A write that reported success here would
// leave the user's edit only in the form.
func TestWriteRequest_ReportsAReadOnlyFolder(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	chmodDir(t, root, 0o500) // read + traverse, no write

	if err := svc.WriteRequest(h, "new.json", Request{ID: "2", Name: "two", Method: "GET", URL: "http://x/b"}); err == nil {
		t.Fatal("WriteRequest reported success into a read-only folder")
	}
	if _, statErr := os.Lstat(filepath.Join(root, "new.json")); statErr == nil {
		t.Error("the file exists although the folder is read-only")
	}
}

// TestWriteRequest_WritesOnceTheFolderIsWritableAgain is the pair, on the
// same handle and the same path: the refusal above is the folder's, not this
// package refusing every write.
func TestWriteRequest_WritesOnceTheFolderIsWritableAgain(t *testing.T) {
	skipIfRoot(t)
	root := seedCollection(t)
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	r := Request{ID: "2", Name: "two", Method: "GET", URL: "http://x/b"}

	chmodDir(t, root, 0o500)
	if refused := svc.WriteRequest(h, "new.json", r); refused == nil {
		t.Fatal("WriteRequest succeeded into a read-only folder; the pair proves nothing")
	}
	//nolint:gosec // G302 is about files; a directory needs its execute bit.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if wrote := svc.WriteRequest(h, "new.json", r); wrote != nil {
		t.Fatalf("WriteRequest into the same folder, now writable: %v", wrote)
	}
	got, readErr := svc.ReadRequest(h, "new.json")
	if readErr != nil {
		t.Fatalf("ReadRequest: %v", readErr)
	}
	if got.URL != r.URL || got.Name != r.Name {
		t.Fatalf("read back %+v, want the request that was written", got)
	}
}

// ─── 3. the root swapped underneath the handle ─────────────────────────────

// TestOperations_ReportARootReplacedByASymlinkToTheSameDirectory is the swap
// identity alone cannot see, and the one handle.go's "Lstat, not Stat"
// carries in a comment with no test under it. The chosen name still resolves
// to the directory that was opened, and that directory is still itself — so
// EvalSymlinks agrees and os.SameFile through a Stat would agree too. What
// changed is that the NAME is now a link somebody else controls, and
// repointing it later is a swap this package would never see afterwards.
func TestOperations_ReportARootReplacedByASymlinkToTheSameDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "req.json", requestJSON("1", "one", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle

	// Move the real directory aside and put a symlink to it at the name.
	real := filepath.Join(parent, "real")
	if err := os.Rename(root, real); err != nil {
		t.Fatalf("move the root aside: %v", err)
	}
	if err := os.Symlink(real, root); err != nil {
		t.Fatalf("symlink at the root's name: %v", err)
	}

	if _, err := svc.List(h); !errors.Is(err, ErrRootChanged) {
		t.Errorf("List: err = %v, want ErrRootChanged — the root's NAME is now a symlink", err)
	}
	if _, err := svc.ReadRequest(h, "req.json"); !errors.Is(err, ErrRootChanged) {
		t.Errorf("ReadRequest: err = %v, want ErrRootChanged", err)
	}
	if err := svc.WriteRequest(h, "req.json", Request{ID: "9"}); !errors.Is(err, ErrRootChanged) {
		t.Errorf("WriteRequest: err = %v, want ErrRootChanged", err)
	}
}

// TestHandle_IsUsableUntilTheRootChangesAndNotAfterwards states the interval
// handle.go declares with BOTH ends in one test, because that is what the
// invariant says and a test of either end alone is half of it: a handle is
// usable from Open UNTIL the directory it was opened on stops being that
// directory, and from that moment every method reports ErrRootChanged.
func TestHandle_IsUsableUntilTheRootChangesAndNotAfterwards(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "req.json", requestJSON("1", "one", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle

	// The open end: every method answers, repeatedly, while nothing moves.
	for i := 0; i < 3; i++ {
		if _, err := svc.List(h); err != nil {
			t.Fatalf("List #%d before the swap: %v", i, err)
		}
		if _, err := svc.ReadRequest(h, "req.json"); err != nil {
			t.Fatalf("ReadRequest #%d before the swap: %v", i, err)
		}
		if err := svc.WriteRequest(h, "req.json", Request{ID: "1", Name: "one", Method: "GET", URL: "http://x/a"}); err != nil {
			t.Fatalf("WriteRequest #%d before the swap: %v", i, err)
		}
	}

	// The closing end.
	impostor := filepath.Join(parent, "impostor")
	writeFile(t, impostor, ManifestName, manifestJSON)
	if err := os.Rename(root, filepath.Join(parent, "moved-away")); err != nil {
		t.Fatalf("move the root away: %v", err)
	}
	if err := os.Rename(impostor, root); err != nil {
		t.Fatalf("swap the impostor in: %v", err)
	}
	if _, err := svc.List(h); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("List after the swap: err = %v, want ErrRootChanged", err)
	}
}
