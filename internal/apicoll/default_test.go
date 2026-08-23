package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePaths is a storage.Paths whose three roles land under one test root.
type fakePaths struct{ root string }

func (p fakePaths) ConfigDir() string { return filepath.Join(p.root, "config") }
func (p fakePaths) DataDir() string   { return filepath.Join(p.root, "data") }
func (p fakePaths) CacheDir() string  { return filepath.Join(p.root, "cache") }

// "Just make one" works without the user answering where (§6.1): the default
// location is derived inside this package from the app's data directory, so
// no caller names a path in order to get one.
func TestNewDefaultCollection_CreatesACollectionThatOpens(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	root, err := NewDefaultCollection(p, "acme")
	if err != nil {
		t.Fatalf("NewDefaultCollection: %v", err)
	}
	want := filepath.Join(p.DataDir(), DefaultCollectionsDirName, "acme")
	if root != want {
		t.Errorf("root = %q, want %q", root, want)
	}

	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open the collection that was just created: %v", err)
	}
	coll := op.Collection
	if coll.Name != "acme" {
		t.Errorf("name = %q, want %q", coll.Name, "acme")
	}
	if len(coll.Requests) != 0 {
		t.Errorf("a new collection listed %d requests, want 0", len(coll.Requests))
	}
}

// The second end of the invariant: the folder exists from the moment
// NewDefaultCollection returns until somebody deletes it, and a second call
// with the same name refuses rather than writing over the first one's
// manifest.
func TestNewDefaultCollection_RefusesToClobberAnExistingOne(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	root, err := NewDefaultCollection(p, "acme")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	svc := newService()
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	if err := svc.WriteRequest(h, "keep.json", Request{ID: "1", Name: "Keep", Method: "GET", URL: "http://x/"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	if _, err := NewDefaultCollection(p, "acme"); !errors.Is(err, ErrCollectionExists) {
		t.Fatalf("second NewDefaultCollection: err = %v, want ErrCollectionExists", err)
	}
	if _, err := svc.ReadRequest(h, "keep.json"); err != nil {
		t.Errorf("the first collection's request is gone after the refused second create: %v", err)
	}
}

// The name is a folder name, not a path. Anything that could reach outside
// the collections directory is refused rather than sanitised.
func TestNewDefaultCollection_RefusesANameThatIsAPath(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	for _, name := range []string{"", "..", ".", "a/b", "../escape", "/absolute", "a\x00b", strings.Repeat("x", 300)} {
		root, err := NewDefaultCollection(p, name)
		if err == nil {
			t.Errorf("NewDefaultCollection(%q) succeeded at %q, want a refusal", name, root)
		}
	}
	// Nothing was created outside the collections directory.
	entries, err := os.ReadDir(p.DataDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the data dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != DefaultCollectionsDirName {
			t.Errorf("a refused name created %q under the data dir", e.Name())
		}
	}
}

// The external call fails: the data directory cannot be created because a
// file already sits where it must go. Paired with the success above.
func TestNewDefaultCollection_ReportsAFailureToCreateTheFolder(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	if err := os.MkdirAll(p.DataDir(), 0o750); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	blocker := filepath.Join(p.DataDir(), DefaultCollectionsDirName)
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatalf("seed the blocker: %v", err)
	}
	if _, err := NewDefaultCollection(p, "acme"); err == nil {
		t.Fatal("NewDefaultCollection succeeded with a file in place of the collections directory")
	}
}
