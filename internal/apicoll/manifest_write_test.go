package apicoll

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// MarshalManifest is the format's one spelling, offered to a caller that
// cannot use a document store — an importer assembling a folder in a staging
// directory. So what it has to prove is not that it marshals, but that what
// it produces is a manifest THIS package opens, and that it is the same file
// this package writes for itself.

// The happy path, asserted through the surface: bytes on disk, folder opens,
// name comes back.
func TestMarshalManifest_ProducesAManifestThisPackageOpens(t *testing.T) {
	raw, err := MarshalManifest("acme")
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, string(raw))
	writeFile(t, root, "ping.json", requestJSON("r1", "ping", "GET", "https://example.test/"))

	op, err := newService().Open(root)
	if err != nil {
		t.Fatalf("Open a folder holding MarshalManifest's own bytes: %v", err)
	}
	coll := op.Collection
	if coll.Name != "acme" {
		t.Errorf("collection name = %q, want %q", coll.Name, "acme")
	}
	if len(coll.Requests) != 1 {
		t.Errorf("requests = %+v, want the one written beside the manifest", coll.Requests)
	}
	if len(coll.Malformed) != 0 {
		t.Errorf("the manifest's own bytes made %d files malformed: %+v", len(coll.Malformed), coll.Malformed)
	}
}

// One format, one owner, stated as bytes: a collection this package creates
// and a collection an importer assembles carry the SAME manifest file. If
// these two ever diverge there are two spellings of the format again, which
// is the defect this function exists to close (nocx-1qtef).
func TestMarshalManifest_IsTheSameFileNewDefaultCollectionWrites(t *testing.T) {
	p := fakePaths{root: t.TempDir()}
	root, err := NewDefaultCollection(p, "acme")
	if err != nil {
		t.Fatalf("NewDefaultCollection: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(root, ManifestName)) //nolint:gosec // reads a manifest this test just created under t.TempDir()
	if err != nil {
		t.Fatalf("read the created manifest: %v", err)
	}
	marshalled, err := MarshalManifest("acme")
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	if string(written) != string(marshalled) {
		t.Errorf("the created manifest is\n%s\nand MarshalManifest produces\n%s", written, marshalled)
	}
}

// The name and the version and nothing else (§6.2). The version is THIS
// build's, which is the half the broken importer got wrong: a manifest
// carrying no version is refused with "no migration from version 0", and a
// file nobody can open is not a collection.
func TestMarshalManifest_CarriesTheNameTheVersionAndNothingElse(t *testing.T) {
	raw, err := MarshalManifest("acme")
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["name"] != "acme" {
		t.Errorf("name = %v, want %q", got["name"], "acme")
	}
	v, ok := got["schemaVersion"].(float64)
	if !ok || int(v) != int(Module.Current) {
		t.Errorf("schemaVersion = %v, want %d", got["schemaVersion"], Module.Current)
	}
	if len(got) != 2 {
		t.Errorf("the manifest carries %d fields, want exactly name and schemaVersion: %s", len(got), raw)
	}
}
