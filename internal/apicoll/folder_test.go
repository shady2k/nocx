package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requestJSON is what one request file looks like on disk.
func requestJSON(id, name, method, url string) string {
	return `{"id":"` + id + `","name":"` + name + `","method":"` + method + `","url":"` + url +
		`","body":{"kind":"none"},"auth":{"kind":"none"}}`
}

func TestOpen_ListsTwoRequestsWithMethodAndName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "list-users.json", requestJSON("1", "List users", "GET", "http://x/users"))
	writeFile(t, root, "users/create.json", requestJSON("2", "Create user", "POST", "http://x/users"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h, coll := op.Handle, op.Collection
	if h == "" {
		t.Error("Open minted an empty handle")
	}
	if coll.Name != "acme" {
		t.Errorf("collection name = %q, want %q — the name comes from the manifest", coll.Name, "acme")
	}
	if len(coll.Requests) != 2 {
		t.Fatalf("Open listed %d requests, want 2: %+v", len(coll.Requests), coll.Requests)
	}
	want := map[string]RequestRef{
		"list-users.json":   {RelPath: "list-users.json", Name: "List users", Method: "GET"},
		"users/create.json": {RelPath: "users/create.json", Name: "Create user", Method: "POST"},
	}
	for _, got := range coll.Requests {
		w, ok := want[got.RelPath]
		if !ok {
			t.Errorf("unexpected request %q", got.RelPath)
			continue
		}
		if got != w {
			t.Errorf("request %q = %+v, want %+v", got.RelPath, got, w)
		}
	}

	// List is the same answer, re-read rather than cached.
	again, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(again.Requests) != 2 {
		t.Errorf("List returned %d requests, want 2", len(again.Requests))
	}
}

// Contents are re-read on every call, never cached: a file added on disk
// after Open appears in the next List.
func TestList_ReReadsTheFolder(t *testing.T) {
	svc, h, root := openTestCollection(t)
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(coll.Requests) != 0 {
		t.Fatalf("a folder with only a manifest listed %d requests, want 0", len(coll.Requests))
	}
	writeFile(t, root, "later.json", requestJSON("3", "Added later", "PUT", "http://x/"))
	coll, err = svc.List(h)
	if err != nil {
		t.Fatalf("List after the file appeared: %v", err)
	}
	if len(coll.Requests) != 1 || coll.Requests[0].Name != "Added later" {
		t.Errorf("List = %+v, want the file that appeared after Open", coll.Requests)
	}
}

func TestOpen_RefusesAFolderWithNoManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, "list-users.json", requestJSON("1", "List users", "GET", "http://x/users"))

	svc := newService()
	op, err := svc.Open(root)
	h, coll := op.Handle, op.Collection
	if !errors.Is(err, ErrNoManifest) {
		t.Fatalf("Open: err = %v, want ErrNoManifest", err)
	}
	if h != "" {
		t.Error("Open minted a handle for a folder it refused")
	}
	if len(coll.Requests) != 0 {
		t.Errorf("Open returned %d requests for a folder it refused — a refusal is not an empty collection",
			len(coll.Requests))
	}
}

// One bad file must not hide a collection. The malformed file is named, and
// it is named ON the collection rather than in an error, so that a caller
// which returns early on err != nil cannot make the other requests vanish.
func TestOpen_NamesAMalformedRequestAndStillListsTheRest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "good-a.json", requestJSON("1", "A", "GET", "http://x/a"))
	writeFile(t, root, "good-b.json", requestJSON("2", "B", "POST", "http://x/b"))
	writeFile(t, root, "broken.json", `{"id":"3","name":`)
	writeFile(t, root, "unknown-field.json", `{"id":"4","name":"D","surprise":1}`)

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v — one malformed file must not refuse the collection", err)
	}
	coll := op.Collection
	if len(coll.Requests) != 2 {
		t.Errorf("listed %d requests, want the 2 good ones: %+v", len(coll.Requests), coll.Requests)
	}
	if len(coll.Malformed) != 2 {
		t.Fatalf("named %d malformed files, want 2: %+v", len(coll.Malformed), coll.Malformed)
	}
	named := map[string]string{}
	for _, m := range coll.Malformed {
		named[m.RelPath] = m.Reason
		if m.Reason == "" {
			t.Errorf("malformed %q carries no reason", m.RelPath)
		}
	}
	for _, rel := range []string{"broken.json", "unknown-field.json"} {
		if _, ok := named[rel]; !ok {
			t.Errorf("malformed file %q was not named: %+v", rel, coll.Malformed)
		}
	}
}

// Reading a malformed file names it too, rather than returning a zero Request.
func TestReadRequest_RefusesAMalformedFileByName(t *testing.T) {
	svc, h, root := openTestCollection(t)
	writeFile(t, root, "broken.json", `{"id":`)
	_, err := svc.ReadRequest(h, "broken.json")
	if !errors.Is(err, ErrMalformedRequest) {
		t.Fatalf("ReadRequest: err = %v, want ErrMalformedRequest", err)
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestReadRequest_ReportsAMissingFile(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	if _, err := svc.ReadRequest(h, "nope.json"); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("ReadRequest: err = %v, want ErrRequestNotFound", err)
	}
}

// environments/ sits beside the requests (§6.2) and is not one of them.
func TestList_ExcludesTheManifestAndEnvironments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "environments/dev.json", `{"name":"dev","route":{"kind":"direct"}}`)
	writeFile(t, root, "notes.txt", "not a request")
	writeFile(t, root, "req.json", requestJSON("1", "A", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	coll := op.Collection
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "req.json" {
		t.Errorf("listed %+v, want only req.json", coll.Requests)
	}
	if len(coll.Malformed) != 0 {
		t.Errorf("environments and notes.txt were reported as malformed requests: %+v", coll.Malformed)
	}
}

// A request file that is a symlink is not read while listing either: it is
// named as malformed rather than followed.
func TestList_DoesNotFollowASymlinkedRequestFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	// The target is a PERFECTLY VALID request file. If the walker followed
	// the link it would list happily, and a fixture holding garbage would
	// have hidden that behind a decode error that looks like a refusal.
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(outside, []byte(requestJSON("9", "Stolen", "GET", "http://evil/")), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "steal.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeFile(t, root, "req.json", requestJSON("1", "A", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	coll := op.Collection
	for _, r := range coll.Requests {
		if r.RelPath == "steal.json" {
			t.Fatal("a symlinked request file was listed — it was followed")
		}
	}
	if len(coll.Requests) != 1 {
		t.Errorf("listed %+v, want only req.json", coll.Requests)
	}
	if len(coll.Malformed) != 1 || coll.Malformed[0].RelPath != "steal.json" {
		t.Errorf("malformed = %+v, want steal.json named", coll.Malformed)
	}
	for _, r := range coll.Requests {
		if r.Name == "Stolen" {
			t.Fatal("the symlink's target was read: its contents reached the listing")
		}
	}
}

// A handle is not guessable, and two folders opened in one session do not
// get handles anybody could have predicted from each other.
//
// It used to say that two opens of ONE root are two handles. They are one —
// handle_test.go holds that rule and why — and this is what is left of the
// property that test does not cover: the id itself.
func TestOpen_MintsAnUnguessableHandle(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one")
	second := filepath.Join(dir, "two")
	writeFile(t, first, ManifestName, manifestJSON)
	writeFile(t, second, ManifestName, manifestJSON)
	svc := newService()
	one, err := svc.Open(first)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	two, err := svc.Open(second)
	if err != nil {
		t.Fatalf("Open the second folder: %v", err)
	}
	if one.Handle == two.Handle {
		t.Error("two folders were given the same handle")
	}
	if len(one.Handle) < 16 {
		t.Errorf("handle %q is short enough to guess", one.Handle)
	}
}

// A collection is shared through git (§6.1), so its own metadata lives inside
// it. Dot-directories are not the user's requests and are not walked.
func TestList_SkipsDotDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, ".git/objects/pack.json", `{"not":"a request"}`)
	writeFile(t, root, "req.json", requestJSON("1", "A", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	coll := op.Collection
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "req.json" {
		t.Errorf("listed %+v, want only req.json", coll.Requests)
	}
	if len(coll.Malformed) != 0 {
		t.Errorf("files under .git were reported as malformed requests: %+v", coll.Malformed)
	}
}

// A folder with only a manifest is a collection with no requests, and the
// listing says so with an empty list rather than a null one.
func TestList_OfAnEmptyCollectionIsEmptyNotNull(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if coll.Requests == nil {
		t.Error("Requests is nil; it marshals as null, which is not the same answer as none")
	}
}
