package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/pathname"
)

// writeFile drops raw bytes at root/rel, creating parents. Tests build their
// folders as a user (or a pull request) would: files, not method calls.
func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// manifestJSON is the smallest manifest that opens.
const manifestJSON = `{"schemaVersion":1,"name":"acme"}`

func TestReadRequest_RefusesEscapingTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	// Valid request JSON on purpose: a garbage fixture would be refused by
	// the decoder even if the symlink WERE followed, and the test would then
	// be asserting nothing about following.
	if err := os.WriteFile(outside, []byte(requestJSON("9", "Stolen", "GET", "http://evil/")), 0o600); err != nil {
		t.Fatalf("seed the file outside: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "users"), 0o750); err != nil {
		t.Fatalf("mkdir users: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "users", "steal.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeFile(t, root, ManifestName, manifestJSON)

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle

	for _, rel := range []string{"../../id_ed25519", outside, "users/steal.json"} {
		if _, err := svc.ReadRequest(h, rel); !errors.Is(err, ErrPathOutsideCollection) {
			t.Errorf("ReadRequest(%q) err = %v, want ErrPathOutsideCollection — a collection "+
				"from a pull request must not read files outside itself", rel, err)
		}
	}
}

// openTestCollection builds a folder with a manifest and returns the service,
// the handle and the root.
func openTestCollection(t *testing.T) (*service, HandleID, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	return svc, h, root
}

func TestWriteRequest_RefusesEscapingTheRoot(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	absolute := filepath.Join(t.TempDir(), "elsewhere.json")

	for _, rel := range []string{"../escape.json", "a/../../escape.json", absolute} {
		if err := svc.WriteRequest(h, rel, Request{Name: "x"}); !errors.Is(err, ErrPathOutsideCollection) {
			t.Errorf("WriteRequest(%q) err = %v, want ErrPathOutsideCollection", rel, err)
		}
		if _, err := os.Lstat(absolute); err == nil {
			t.Errorf("WriteRequest(%q) created %s — refused must mean nothing was written", rel, absolute)
		}
	}
}

// A write through a symlink must not reach the link's target. This is the
// guard internal/storage/document.go:159 already applies to the final
// component; a collection folder also needs it on every directory in the
// path, which is what the second case here asserts.
func TestWriteRequest_DoesNotFollowSymlinks(t *testing.T) {
	svc, h, root := openTestCollection(t)

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "id_ed25519")
	if err := os.WriteFile(outsideFile, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "leaf.json")); err != nil {
		t.Fatalf("symlink leaf: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "dir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	for _, rel := range []string{"leaf.json", "dir/planted.json"} {
		if err := svc.WriteRequest(h, rel, Request{Name: "x"}); !errors.Is(err, ErrPathOutsideCollection) {
			t.Errorf("WriteRequest(%q) err = %v, want ErrPathOutsideCollection", rel, err)
		}
	}

	// Both ends of the invariant: refused, AND the target is untouched.
	//
	// outsideFile is this test's own t.TempDir path, written three lines up;
	// there is no untrusted input anywhere near it. Wrapping it in
	// filepath.Clean would also silence gosec, and would be worse — a no-op
	// call whose only purpose is to quiet a linter reads as safety code and
	// hides that a judgement was made here at all.
	got, err := os.ReadFile(outsideFile) //nolint:gosec // a path this test created under its own t.TempDir
	if err != nil {
		t.Fatalf("re-read the target: %v", err)
	}
	if string(got) != "PRIVATE KEY" {
		t.Errorf("target contents = %q, want the original — a refused write wrote anyway", got)
	}
	if _, err := os.Lstat(filepath.Join(outsideDir, "planted.json")); err == nil {
		t.Error("a file was planted in the symlinked directory — the write followed the link")
	}
}

// A symlinked directory must not be read through either.
func TestReadRequest_DoesNotFollowDirectorySymlinks(t *testing.T) {
	svc, h, root := openTestCollection(t)
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.json"), []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "dir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	if _, err := svc.ReadRequest(h, "dir/secret.json"); !errors.Is(err, ErrPathOutsideCollection) {
		t.Errorf("ReadRequest through a symlinked directory: err = %v, want ErrPathOutsideCollection", err)
	}
}

// Refused means refused: a path is never cleaned into a legal one.
func TestPaths_AreRefusedNotClamped(t *testing.T) {
	svc, h, root := openTestCollection(t)
	writeFile(t, root, "req.json", `{"id":"1","name":"one","method":"GET","url":"http://x/","body":{"kind":"none"},"auth":{"kind":"none"}}`)

	// Each of these would name req.json after a filepath.Clean. None may.
	for _, rel := range []string{"./req.json", "sub/../req.json", "/req.json", "a//req.json"} {
		if _, err := svc.ReadRequest(h, rel); errors.Is(err, nil) {
			t.Errorf("ReadRequest(%q) succeeded — the path was clamped rather than refused", rel)
		} else if !errors.Is(err, ErrPathOutsideCollection) && !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("ReadRequest(%q) err = %v, want a refusal", rel, err)
		}
	}
}

// The manifest, the environments folder and anything that is not a .json
// request file are not request paths. Without this, WriteRequest is a way to
// overwrite the manifest through the request surface.
func TestRequestPaths_AreJSONFilesThatAreNotTheManifest(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	for _, rel := range []string{ManifestName, "environments/dev.json", "notes.txt", "", "sub/"} {
		if err := svc.WriteRequest(h, rel, Request{Name: "x"}); !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("WriteRequest(%q) err = %v, want ErrNotARequestPath", rel, err)
		}
		if _, err := svc.ReadRequest(h, rel); !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("ReadRequest(%q) err = %v, want ErrNotARequestPath", rel, err)
		}
	}
}

// The root may be replaced between open and read (§13.1). The handle is
// re-validated per operation, so the failure is reported rather than served
// out of whatever now sits at that path.
func TestOperations_ReportARootReplacedAfterOpen(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "req.json", `{"id":"1","name":"one","method":"GET","url":"http://x/","body":{"kind":"none"},"auth":{"kind":"none"}}`)

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	if _, err := svc.ReadRequest(h, "req.json"); err != nil {
		t.Fatalf("ReadRequest before the swap: %v", err)
	}

	// Swap a different directory into the same path: a new inode, same name.
	impostor := filepath.Join(parent, "impostor")
	writeFile(t, impostor, ManifestName, manifestJSON)
	writeFile(t, impostor, "req.json", `{"id":"9","name":"planted","method":"GET","url":"http://evil/","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	if err := os.Rename(root, filepath.Join(parent, "moved-away")); err != nil {
		t.Fatalf("move the root away: %v", err)
	}
	if err := os.Rename(impostor, root); err != nil {
		t.Fatalf("swap the impostor in: %v", err)
	}

	if _, err := svc.ReadRequest(h, "req.json"); !errors.Is(err, ErrRootChanged) {
		t.Errorf("ReadRequest after the swap: err = %v, want ErrRootChanged", err)
	}
	if _, err := svc.List(h); !errors.Is(err, ErrRootChanged) {
		t.Errorf("List after the swap: err = %v, want ErrRootChanged", err)
	}
	if err := svc.WriteRequest(h, "req.json", Request{Name: "x"}); !errors.Is(err, ErrRootChanged) {
		t.Errorf("WriteRequest after the swap: err = %v, want ErrRootChanged", err)
	}
	// A folder made now would be made in the impostor: the check is on the
	// operation, so a method that writes has it as much as one that reads.
	if _, err := svc.CreateFolder(h, "", "users"); !errors.Is(err, ErrRootChanged) {
		t.Errorf("CreateFolder after the swap: err = %v, want ErrRootChanged", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "users")); err == nil {
		t.Error("CreateFolder made a folder in the impostor after the swap")
	}
}

func TestOperations_ReportARootThatHasGone(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove the root: %v", err)
	}
	if _, err := svc.List(h); !errors.Is(err, ErrRootChanged) {
		t.Errorf("List after the root was deleted: err = %v, want ErrRootChanged", err)
	}
}

func TestMethods_RefuseAnUnknownHandle(t *testing.T) {
	svc := newService()
	h := HandleID("not-a-handle")
	if _, err := svc.List(h); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("List: err = %v, want ErrUnknownHandle", err)
	}
	if _, err := svc.ReadRequest(h, "a.json"); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("ReadRequest: err = %v, want ErrUnknownHandle", err)
	}
	if err := svc.WriteRequest(h, "a.json", Request{}); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("WriteRequest: err = %v, want ErrUnknownHandle", err)
	}
	// The refusal for a handle that is not open is THIS one for every
	// method, including the one that creates: a second way to say it would
	// be a second sentence a surface has to learn.
	if _, err := svc.CreateFolder(h, "", "users"); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("CreateFolder: err = %v, want ErrUnknownHandle", err)
	}
}

// Open refuses a root that is not an absolute, clean path: the folder the
// user chose is named once, exactly, and never rewritten.
func TestOpen_RefusesANonAbsoluteRoot(t *testing.T) {
	svc := newService()
	for _, root := range []string{"relative/coll", "", "."} {
		if _, err := svc.Open(root); err == nil {
			t.Errorf("Open(%q) succeeded, want a refusal", root)
		}
	}
}

func TestOpen_RefusesARootThatIsNotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := newService()
	if _, err := svc.Open(file); err == nil {
		t.Error("Open on a regular file succeeded, want a refusal")
	}
	if _, err := svc.Open(filepath.Join(dir, "missing")); err == nil {
		t.Error("Open on a missing directory succeeded, want a refusal")
	}
}

// The user may legitimately choose a symlinked folder — they named it, so it
// is theirs. Open resolves it once and that resolved directory is the
// collection from then on.
func TestOpen_AcceptsASymlinkedRootTheUserChose(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	writeFile(t, real, ManifestName, manifestJSON)
	writeFile(t, real, "req.json", `{"id":"1","name":"one","method":"GET","url":"http://x/","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	link := filepath.Join(t.TempDir(), "chosen")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	svc := newService()
	op, err := svc.Open(link)
	if err != nil {
		t.Fatalf("Open a symlinked root: %v", err)
	}
	h, coll := op.Handle, op.Collection
	if len(coll.Requests) != 1 {
		t.Errorf("listed %d requests through the symlinked root, want 1", len(coll.Requests))
	}
	if _, err := svc.ReadRequest(h, "req.json"); err != nil {
		t.Errorf("ReadRequest through the symlinked root: %v", err)
	}
}

// ...and the second end of that: repointing the symlink is a replaced root.
// The directory opened is still there and still itself, so identity alone
// would say nothing had happened while the user's collection had been swapped
// underneath the name they chose.
func TestOperations_ReportASymlinkedRootRepointed(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	writeFile(t, real, ManifestName, manifestJSON)
	other := filepath.Join(t.TempDir(), "other")
	writeFile(t, other, ManifestName, manifestJSON)
	writeFile(t, other, "planted.json", `{"id":"9","name":"planted","method":"GET","url":"http://evil/","body":{"kind":"none"},"auth":{"kind":"none"}}`)

	link := filepath.Join(t.TempDir(), "chosen")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	svc := newService()
	op, err := svc.Open(link)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove the symlink: %v", err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatalf("repoint the symlink: %v", err)
	}

	if _, err := svc.List(h); !errors.Is(err, ErrRootChanged) {
		t.Errorf("List after the symlinked root was repointed: err = %v, want ErrRootChanged", err)
	}
}

// A collection is meant to be put under git and shared (§6.1), so a name is
// only usable if it is usable on every platform this ships to. `con` is a
// device on Windows, at any extension, and a folder called `con` made here
// is a collection a colleague there cannot check out at all.
//
// Refused by name, never rewritten: a request quietly saved as `_con.json`
// is a file the user cannot find under the name they typed.
func TestPaths_RefuseANameNoWindowsMachineCanTake(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	for _, rel := range []string{"con.json", "prn.json", "com1.json", "users/aux.json", "docs./req.json", "LPT9.json"} {
		if err := svc.WriteRequest(h, rel, Request{ID: "1", Name: "x", Method: "GET", URL: "http://x/"}); !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("WriteRequest(%q) err = %v, want ErrNotARequestPath", rel, err)
		}
		if _, err := os.Lstat(filepath.Join(rootOf(t, svc, h), filepath.FromSlash(rel))); err == nil {
			t.Errorf("WriteRequest(%q) wrote it anyway", rel)
		}
		if _, err := svc.ReadRequest(h, rel); !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("ReadRequest(%q) err = %v, want ErrNotARequestPath", rel, err)
		}
		if err := svc.DeleteRequest(h, rel); !errors.Is(err, ErrNotARequestPath) {
			t.Errorf("DeleteRequest(%q) err = %v, want ErrNotARequestPath", rel, err)
		}
	}

	for _, name := range []string{"con", "PRN", "com9", "docs.", "docs ", "a:b", `a\b`} {
		_, err := svc.CreateFolder(h, "", name)
		if !errors.Is(err, ErrInvalidFolderName) {
			t.Errorf("CreateFolder(%q) err = %v, want ErrInvalidFolderName", name, err)
		}
		// A sentence a surface can show, not a bare sentinel.
		if err != nil && len(err.Error()) <= len(ErrInvalidFolderName.Error()) {
			t.Errorf("the refusal for %q is %q, which says nothing beyond the sentinel", name, err)
		}
		if _, statErr := os.Lstat(filepath.Join(rootOf(t, svc, h), name)); statErr == nil {
			t.Errorf("CreateFolder(%q) made it anyway", name)
		}
	}

	for _, rel := range []string{"environments/con.json", "environments/nul.json"} {
		if err := svc.WriteEnvironment(h, rel, Environment{Name: "dev", Route: Route{Kind: RouteDirect}}); !errors.Is(err, ErrNotAnEnvironmentPath) {
			t.Errorf("WriteEnvironment(%q) err = %v, want ErrNotAnEnvironmentPath", rel, err)
		}
	}
}

// The other end of every refusal above, on the same service: the ordinary
// names those refusals are meant to leave alone still work end to end.
func TestPaths_AcceptTheNamesPeopleActuallyUse(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf(`CreateFolder("users"): %v`, err)
	}
	// `console` is not `con`, and `com10` is not `com1`: the rule is the
	// device list, not anything that looks like it.
	for _, name := range []string{"console", "com10", "lpt", "api.example.com"} {
		if _, err := svc.CreateFolder(h, "", name); err != nil {
			t.Errorf("CreateFolder(%q): %v", name, err)
		}
	}
	req := Request{ID: "1", Name: "Create user", Method: "POST", URL: "http://x/users"}
	if err := svc.WriteRequest(h, "users/create.json", req); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if got, err := svc.ReadRequest(h, "users/create.json"); err != nil || got.ID != "1" {
		t.Fatalf("ReadRequest: %+v, %v", got, err)
	}
	if err := svc.WriteEnvironment(h, "environments/dev.json", Environment{Name: "dev", Route: Route{Kind: RouteDirect}}); err != nil {
		t.Fatalf("WriteEnvironment: %v", err)
	}
}

// A path has to be bounded as a whole, not only per component: thirty-two
// legal folder names in a row are a path no Windows checkout will take.
// The bounds live with the name rule (internal/pathname) so the importer
// that MINTS a path and the store that ACCEPTS one cannot hold two numbers.
func TestPaths_AreBoundedInDepthAndInTotalLength(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	deep := strings.Repeat("ab/", pathname.MaxDepth) + "x.json"
	if err := svc.WriteRequest(h, deep, Request{ID: "1", Name: "x", Method: "GET", URL: "http://x/"}); !errors.Is(err, ErrNotARequestPath) {
		t.Errorf("WriteRequest at %d components: err = %v, want ErrNotARequestPath", pathname.MaxDepth+1, err)
	}
	long := strings.Repeat(strings.Repeat("a", 99)+"/", 3) + "x.json"
	if len(long) <= pathname.MaxRelPathBytes {
		t.Fatalf("the fixture is %d bytes and does not exceed the bound", len(long))
	}
	if err := svc.WriteRequest(h, long, Request{ID: "1", Name: "x", Method: "GET", URL: "http://x/"}); !errors.Is(err, ErrNotARequestPath) {
		t.Errorf("WriteRequest at %d bytes: err = %v, want ErrNotARequestPath", len(long), err)
	}

	// And the paired success, one component inside each bound.
	ok := strings.Repeat("ab/", pathname.MaxDepth-1) + "x.json"
	for _, dir := range strings.Split(strings.TrimSuffix(ok, "/x.json"), "/") {
		_ = dir
	}
	if err := os.MkdirAll(filepath.Join(rootOf(t, svc, h), filepath.FromSlash(strings.TrimSuffix(ok, "/x.json"))), 0o750); err != nil {
		t.Fatalf("mkdir the deep folder: %v", err)
	}
	if err := svc.WriteRequest(h, ok, Request{ID: "1", Name: "x", Method: "GET", URL: "http://x/"}); err != nil {
		t.Errorf("WriteRequest at %d components and %d bytes: %v", pathname.MaxDepth, len(ok), err)
	}
}

// rootOf is the folder behind an open handle, for tests that check the disk
// directly rather than through the service.
func rootOf(t *testing.T, svc *service, h HandleID) string {
	t.Helper()
	hd, err := svc.resolve(h)
	if err != nil {
		t.Fatalf("resolve the handle: %v", err)
	}
	return hd.root
}
