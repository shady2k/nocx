package apicoll

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// A manifest from a newer build is refused BEFORE the rest of it is decoded.
// The assertion that this is an ordering and not just an outcome: the rest of
// this manifest cannot decode at all — `name` is a number — so an
// ErrVersionTooNew here can only have come from a check that ran first.
func TestOpen_RefusesAManifestNewerThanThisBuild(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, `{"schemaVersion":2,"name":42,"requests":"neither"}`)
	writeFile(t, root, "req.json", requestJSON("1", "A", "GET", "http://x/a"))

	svc := newService()
	op, err := svc.Open(root)
	h, coll := op.Handle, op.Collection
	if !errors.Is(err, storage.ErrVersionTooNew) {
		t.Fatalf("Open: err = %v, want storage.ErrVersionTooNew", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not name the version it refused", err)
	}
	if h != "" || len(coll.Requests) != 0 {
		t.Errorf("Open handed back handle %q and %d requests for a manifest it refused", h, len(coll.Requests))
	}
}

// The paired success case: the version this build writes opens.
func TestOpen_AcceptsTheCurrentVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	svc := newService()
	if _, err := svc.Open(root); err != nil {
		t.Fatalf("Open at the current version: %v", err)
	}
	if Module.Current != 1 {
		t.Errorf("Module.Current = %d; this test's fixture says 1", Module.Current)
	}
	if Module.Name != "apicoll" {
		t.Errorf("Module.Name = %q, want %q", Module.Name, "apicoll")
	}
}

// A manifest with no version at all is not version 0 treated as current: it
// is refused, because there is no 0→1 migration and inventing one would mean
// reading an unknown format as if it were ours.
func TestOpen_RefusesAManifestWithNoVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, `{"name":"acme"}`)
	svc := newService()
	if _, err := svc.Open(root); err == nil {
		t.Fatal("Open accepted a manifest carrying no schemaVersion")
	}
}

func TestOpen_RefusesAManifestThatIsNotJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, `not json at all`)
	svc := newService()
	if _, err := svc.Open(root); err == nil {
		t.Fatal("Open accepted a manifest that is not JSON")
	}
}

// A manifest that is a symlink is not followed: the same rule as a request
// file, applied to the file that decides whether the folder opens at all.
func TestOpen_DoesNotFollowASymlinkedManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, "placeholder", "")
	outside := filepath.Join(t.TempDir(), "manifest.json")
	writeFile(t, filepath.Dir(outside), "manifest.json", manifestJSON)
	if err := symlink(t, outside, filepath.Join(root, ManifestName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	svc := newService()
	if _, err := svc.Open(root); !errors.Is(err, ErrPathOutsideCollection) {
		t.Errorf("Open with a symlinked manifest: err = %v, want ErrPathOutsideCollection", err)
	}
}
