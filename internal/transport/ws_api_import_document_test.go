package transport

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// api.import.postman takes the DOCUMENT, not only a path.
//
// `path` names a file on the machine running Go. In the desktop app that is
// also the person's machine, which is why nobody noticed; reached over a
// forwarded port (`make dev-web` forwards both ports over SSH) it names a
// file on the SERVER, which is almost never what a person means about an
// export they just downloaded. The bytes are the general case: the renderer
// has them, and bytes reach a backend wherever it runs.
//
// These tests drive the real method through the real socket, because that is
// the only test that can report a route the handler never took.

const importDocumentFixture = `{
      "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "variable": [{"key": "token", "value": "sk-secret-value", "type": "secret"}],
      "item": [{"name": "ping", "request": {"method": "GET", "url": "https://example.test/ping"}}]
    }`

// The document route, off the real socket, against the schema — the third
// check in contracts/README.md's table and the one that matters.
func TestAPIImportPostmanDocument_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.import.postman.schema.json")
	bindings := newAPIFakeBindings()
	_, conn := newAPIWSServer(t, bindings)

	dest := filepath.Join(t.TempDir(), "imported")
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"document": importDocumentFixture,
		"dest":     dest,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman by document: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "api.import.postman result")

	// And the imported folder opens, the same pairing the path route's
	// happy path makes: you import an export in order to work in it.
	openAPICollection(t, conn, dest, 2)

	if bindings.count() != 1 {
		t.Errorf("bound values = %d, want 1 — the secret must reach the binding store on this route too", bindings.count())
	}
}

// Both, or neither, is refused BY NAME with a sentence saying which. A
// silent precedence rule would make one of the two parameters do nothing on
// a call that named both, and the caller would never learn which.
func TestAPIImportPostman_PathAndDocumentAreExclusiveAndOneIsRequired(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	doc := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(doc, []byte(importDocumentFixture), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "imported")

	both := vaultCall(t, conn, "api.import.postman", map[string]any{
		"path": doc, "document": importDocumentFixture, "dest": dest,
	}, 1)
	if both.Error == nil {
		t.Fatal("path and document together were accepted; one of the two would then be silently ignored")
	}
	if both.Error.Code != -32602 {
		t.Errorf("path+document: code = %d, want -32602", both.Error.Code)
	}
	if !strings.Contains(both.Error.Message, "path") || !strings.Contains(both.Error.Message, "document") {
		t.Errorf("path+document refusal = %q, want it to name both parameters", both.Error.Message)
	}

	neither := vaultCall(t, conn, "api.import.postman", map[string]any{"dest": dest}, 2)
	if neither.Error == nil {
		t.Fatal("an import naming no export at all was accepted")
	}
	if neither.Error.Code != -32602 {
		t.Errorf("neither path nor document: code = %d, want -32602", neither.Error.Code)
	}
	if !strings.Contains(neither.Error.Message, "path") || !strings.Contains(neither.Error.Message, "document") {
		t.Errorf("empty refusal = %q, want it to name both routes so the caller learns there are two", neither.Error.Message)
	}

	// Neither refusal wrote anything: a params refusal never reaches the
	// importer, so there is no folder to leave behind.
	if _, err := os.Lstat(dest); err == nil {
		t.Errorf("%s exists; a refused params object must not have run an import", dest)
	}
}

// A document over the wire bound is refused with a sentence that NAMES the
// bound and points at `path` as the route for a large export — otherwise the
// person is told only that their export is too big, with nowhere to go.
func TestAPIImportPostmanDocument_OverTheBoundNamesItAndPointsAtPath(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	dest := filepath.Join(t.TempDir(), "imported")
	huge := strings.Repeat("x", maxAPIImportDocumentRunes+1)
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{"document": huge, "dest": dest}, 1)
	if resp.Error == nil {
		t.Fatal("a document over the bound was accepted")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", resp.Error.Code)
	}
	bound := strconv.Itoa(maxAPIImportDocumentRunes)
	if !strings.Contains(resp.Error.Message, bound) {
		t.Errorf("refusal = %q, want it to name the bound %s", resp.Error.Message, bound)
	}
	if !strings.Contains(resp.Error.Message, "path") {
		t.Errorf("refusal = %q, want it to point at path as the route for a large export", resp.Error.Message)
	}
}

// The two routes are ONE import: the same bytes must land as the same
// collection, whichever way they arrived. Same document, one written to a
// file and named by path, one carried inline — compared file by file.
func TestAPIImportPostman_DocumentAndPathProduceTheSameCollection(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	src := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(src, []byte(importDocumentFixture), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}

	byPath := filepath.Join(t.TempDir(), "by-path")
	byDoc := filepath.Join(t.TempDir(), "by-document")

	if resp := vaultCall(t, conn, "api.import.postman", map[string]any{"path": src, "dest": byPath}, 1); resp.Error != nil {
		t.Fatalf("import by path: %+v", resp.Error)
	}
	if resp := vaultCall(t, conn, "api.import.postman", map[string]any{"document": importDocumentFixture, "dest": byDoc}, 2); resp.Error != nil {
		t.Fatalf("import by document: %+v", resp.Error)
	}

	pathTree := readCollectionTree(t, byPath)
	docTree := readCollectionTree(t, byDoc)

	if len(pathTree) == 0 {
		t.Fatal("the path import produced no files at all")
	}
	if len(pathTree) != len(docTree) {
		t.Fatalf("file counts differ: by path %d, by document %d", len(pathTree), len(docTree))
	}
	for rel, body := range pathTree {
		other, ok := docTree[rel]
		if !ok {
			t.Errorf("%s is in the path import and not in the document import", rel)
			continue
		}
		// Byte for byte, deliberately: apiimport mints a request id as a
		// hash of the file's relative path (names.go), so nothing in an
		// imported collection is minted per run. A comparison that
		// normalised anything here would be hiding a difference rather
		// than tolerating one.
		if body != other {
			t.Errorf("%s differs between the two routes:\n by path:     %s\n by document: %s", rel, body, other)
		}
	}
	if _, ok := pathTree["nocx-collection.json"]; !ok {
		t.Error("no manifest in the imported collection; the comparison would prove nothing")
	}
}

// readCollectionTree reads every file under root, keyed by path relative to
// root, so two imported folders can be compared whole rather than by the one
// file the test happened to think of.
func readCollectionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(p) //nolint:gosec // a test-only path under t.TempDir()
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		out[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
