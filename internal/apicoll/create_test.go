package apicoll

// "Just make one" (§6.1), asserted as the thing a person does: they name a
// collection and get one back, opened, with somewhere to put an environment.
//
// Every assertion here goes through the SURFACE — the handle Create hands
// back, or a fresh Open of the folder — rather than through a directory
// listing. A test that stats the files proves the writer wrote; only a test
// that opens the collection proves the user has one.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/pathname"
)

// TestCreate_MintsACollectionTheUserCanUse is the happy path and the pair
// every refusal below needs (AGENTS.md testing rule 3). It is deliberately
// the WHOLE motion: name it, get the handle back, write a request through
// that handle, and read it again.
func TestCreate_MintsACollectionTheUserCanUse(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	svc := NewCollections(p)

	made, err := svc.Create("acme")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if made.Handle == "" {
		t.Fatal("Create returned an empty handle; the caller has nothing to address the collection with")
	}
	if made.Collection.Name != "acme" {
		t.Errorf("collection name = %q, want %q", made.Collection.Name, "acme")
	}
	if len(made.Collection.Requests) != 0 {
		t.Errorf("a new collection listed %d requests, want 0", len(made.Collection.Requests))
	}
	want := filepath.Join(p.DataDir(), DefaultCollectionsDirName, "acme")
	if made.Root != want {
		t.Errorf("root = %q, want %q — the default location is decided inside this package", made.Root, want)
	}

	// The handle is a working handle, not a receipt: the renderer's next
	// move after "new collection" is to add a request, and it must not
	// have to open the folder first.
	if err = svc.WriteRequest(made.Handle, "ping.json",
		Request{ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/"}); err != nil {
		t.Fatalf("WriteRequest through the handle Create returned: %v", err)
	}
	coll, err := svc.List(made.Handle)
	if err != nil {
		t.Fatalf("List through the handle Create returned: %v", err)
	}
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "ping.json" {
		t.Errorf("requests = %+v, want the one just written", coll.Requests)
	}
}

// And the created folder is a collection to anybody, not only to the
// service that made it: a second Open of the same path succeeds. Without
// this, Create could hand back a handle for a folder that has no manifest
// and the defect would not appear until the next launch.
func TestCreate_TheFolderOpensAgainFromScratch(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	made, err := NewCollections(p).Create("acme")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	op, err := NewCollections(p).Open(made.Root)
	if err != nil {
		t.Fatalf("Open the collection that was just created: %v", err)
	}
	coll := op.Collection
	if coll.Name != "acme" {
		t.Errorf("name = %q, want %q", coll.Name, "acme")
	}
}

// The environments directory is part of what a new collection IS (§6.2), and
// the observable is what it is FOR: an environment file dropped into it —
// by the user, by a colleague's git pull — lists, without anybody having
// created the directory first.
func TestCreate_LeavesSomewhereToPutAnEnvironment(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	svc := NewCollections(p)
	made, err := svc.Create("acme")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No MkdirAll here on purpose: if Create did not make the directory,
	// this write fails and the test says so.
	env := filepath.Join(made.Root, "environments", "dev.json")
	if err = os.WriteFile(env, []byte(`{"name":"dev","values":{"baseUrl":"http://localhost:3000"},"route":{"kind":"direct"}}`), 0o600); err != nil {
		t.Fatalf("write an environment into the new collection: %v — Create left nowhere to put one", err)
	}

	envs, bad, err := svc.ListEnvironments(made.Handle)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("malformed = %+v, want none", bad)
	}
	if len(envs) != 1 || envs[0].Environment.Name != "dev" {
		t.Fatalf("environments = %+v, want the one just written", envs)
	}
}

// A brand-new collection reports no environments rather than a failure: the
// directory is there and empty, which is a collection nobody has configured
// yet, not a broken one.
func TestCreate_ANewCollectionHasNoEnvironmentsAndThatIsNotAnError(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	svc := NewCollections(p)
	made, err := svc.Create("acme")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	envs, bad, err := svc.ListEnvironments(made.Handle)
	if err != nil {
		t.Fatalf("ListEnvironments on a new collection: %v", err)
	}
	if len(envs) != 0 || len(bad) != 0 {
		t.Errorf("envs = %+v, malformed = %+v, want both empty", envs, bad)
	}
}

// A name is a name, never a path (§13.1). The refusal is by NAME — nothing
// is sanitised, because a name quietly stripped of its slashes creates a
// folder the user did not ask for under a name they did not choose.
func TestCreate_RefusesANameThatIsNotAName(t *testing.T) {
	for name, given := range map[string]string{
		"empty":            "",
		"a slash":          "acme/prod",
		"this directory":   ".",
		"the parent":       "..",
		"a leading dot":    ".hidden",
		"a NUL byte":       "ac\x00me",
		"an absolute path": "/etc/acme",
		"too long":         strings.Repeat("a", pathname.MaxComponentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			p := fakePaths{root: t.TempDir()}
			made, err := NewCollections(p).Create(given)
			if err == nil {
				t.Fatalf("Create(%q) succeeded at %q; a name is not a path", given, made.Root)
			}
			// Nothing was written anywhere under the data directory: a
			// refusal that had already created something would be the
			// silent half-success this rule exists to prevent.
			if entries, statErr := os.ReadDir(filepath.Join(p.DataDir(), DefaultCollectionsDirName)); statErr == nil && len(entries) != 0 {
				t.Errorf("Create(%q) refused and still left %d entries behind", given, len(entries))
			}
		})
	}
}

// Creating over an existing collection is REFUSED, not merged: writing a
// fresh manifest over somebody's folder is data loss wearing the word
// "create". The second end of the interval is asserted too — what was in
// the first collection is still in it afterwards.
func TestCreate_RefusesToCreateOverAnExistingCollection(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	svc := NewCollections(p)

	first, err := svc.Create("acme")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err = svc.WriteRequest(first.Handle, "keep.json",
		Request{ID: "1", Name: "Keep", Method: "GET", URL: "https://example.test/"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	if _, err = svc.Create("acme"); !errors.Is(err, ErrCollectionExists) {
		t.Fatalf("second Create: err = %v, want ErrCollectionExists", err)
	}

	coll, err := svc.List(first.Handle)
	if err != nil {
		t.Fatalf("List after the refused second Create: %v", err)
	}
	if len(coll.Requests) != 1 || coll.Requests[0].Name != "Keep" {
		t.Errorf("requests = %+v, want the first collection's own request untouched", coll.Requests)
	}
}

// The external call this method makes is a directory creation, and it has a
// test where it fails: the place the collections directory must go is
// already a regular file, so no user — root included — can make a directory
// there.
func TestCreate_ReportsAFailureToMakeTheDirectory(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	if err := os.MkdirAll(p.DataDir(), 0o700); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	blocker := filepath.Join(p.DataDir(), DefaultCollectionsDirName)
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	made, err := NewCollections(p).Create("acme")
	if err == nil {
		t.Fatalf("Create succeeded at %q with a regular file in the way", made.Root)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Errorf("err = %v, want it to name the collection that could not be created", err)
	}
}

// A service built without an app directory cannot decide where a new
// collection goes, and says so by name rather than panicking in the
// filesystem. That is the service a caller gets when the composition root
// has no paths to give it — it reads folders the user chose and mints none.
func TestCreate_WithoutAnAppDirectoryRefusesByName(t *testing.T) {
	svc := NewCollections(nil)
	if _, err := svc.Create("acme"); !errors.Is(err, ErrNoDefaultLocation) {
		t.Fatalf("err = %v, want ErrNoDefaultLocation", err)
	}
}
