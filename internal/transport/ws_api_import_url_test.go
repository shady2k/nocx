package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// api.import.postman takes a URL, and the BACKEND fetches it.
//
// The third entrance is the one where nobody on this side holds the bytes:
// the export is behind a network the backend can reach and the renderer may
// not — a collection published on an intranet, or one reachable only through
// a bastion. These tests drive the real method through the real socket,
// because the handler's dispatch is the only place the wrong entrance can be
// taken silently.

// The route the document arrived through reaches the environment the import
// mints — through the handler, the capability, the fetcher and the writer,
// which is four seams a unit test cannot see across.
func TestAPIImportPostmanURL_TheFetchedCollectionKeepsTheRouteItArrivedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(importDocumentFixture))
	}))
	defer srv.Close()

	// The pool is the only double, and it is one that really carries bytes
	// (tcpLease): the route table, the fetch and the writer are real, so
	// what this watches is a document that genuinely arrived through a
	// connection route.
	_, conn := newAPIWSServerWithPool(t, newAPIFakeBindings(), tcpLease{done: make(chan struct{})})
	dest := filepath.Join(t.TempDir(), "fetched")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"url": srv.URL + "/export.json",
		// insecureTls is SET here on purpose: it must reach nothing.
		"route": map[string]any{"kind": "connection", "profileId": "prod-bastion", "insecureTls": true},
		"dest":  dest,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman by url: %+v", resp.Error)
	}

	env := readAPIEnvironmentFile(t, filepath.Join(dest, "environments", "default.json"))
	if env.Route.Kind != apicoll.RouteConnection || env.Route.ProfileID != "prod-bastion" {
		t.Errorf("the imported environment routes %+v, want the connection the document was fetched through", env.Route)
	}
	// insecureTls is the ENVIRONMENT's own setting and a fetch is not an
	// environment: it is never carried in, whatever the route said.
	if env.Route.InsecureTLS {
		t.Error("insecureTls arrived from the fetch route; it must never")
	}
}

// The refusal a person is most likely to meet: a URL that answers with a
// login page instead of an export. It must NEVER reach the importer, which
// hands anything non-JSON to the curl parser — the word curl in this dialog
// names a thing the person was never offered.
func TestAPIImportPostmanURL_ALoginPageIsRefusedWithoutMentioningCurl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	dest := filepath.Join(t.TempDir(), "fetched")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{"url": srv.URL, "dest": dest}, 1)
	if resp.Error == nil {
		t.Fatal("a login page was imported as a Postman export")
	}
	if strings.Contains(strings.ToLower(resp.Error.Message), "curl") {
		t.Errorf("the refusal mentions curl, which this ask never offered: %s", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "not a Postman export") {
		t.Errorf("refusal = %q, want it to say what is wrong with the address", resp.Error.Message)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want not-exist — a refused fetch writes nothing", dest, err)
	}
}

// A scheme the fetch cannot GET is refused, and refused before any dial —
// file:// is the one a person reaches for when they meant `path`.
func TestAPIImportPostmanURL_RefusesASchemeItCannotGet(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	dest := filepath.Join(t.TempDir(), "fetched")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{"url": "file:///etc/passwd", "dest": dest}, 1)
	if resp.Error == nil {
		t.Fatal("file:///etc/passwd was accepted as an import URL")
	}
	if !strings.Contains(resp.Error.Message, "http") {
		t.Errorf("refusal = %q, want it to name the two schemes an import URL may use", resp.Error.Message)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want not-exist", dest, err)
	}
}

// A server that refuses says so, with its status, and the destination is
// untouched: the fetch is complete before the write begins.
func TestAPIImportPostmanURL_AServerRefusalLeavesNoCollection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	dest := filepath.Join(t.TempDir(), "fetched")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{"url": srv.URL, "dest": dest}, 1)
	if resp.Error == nil {
		t.Fatal("a 401 was imported")
	}
	if !strings.Contains(resp.Error.Message, "401") {
		t.Errorf("refusal = %q, want the status in it", resp.Error.Message)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want not-exist — a failed fetch writes nothing", dest, err)
	}
}

// A connection route whose profile cannot be leased refuses the import
// rather than quietly fetching out of this machine's own interface, around
// the bastion the person named. The test server would answer a direct dial,
// so a fallback would look like success.
func TestAPIImportPostmanURL_AConnectionThatCannotBeLeasedRefusesRatherThanDialsDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(importDocumentFixture))
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	dest := filepath.Join(t.TempDir(), "fetched")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"url":   srv.URL,
		"route": map[string]any{"kind": "connection", "profileId": "prod-bastion"},
		"dest":  dest,
	}, 1)
	if resp.Error == nil {
		t.Fatal("an import through a connection that cannot be leased was fetched directly")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want not-exist", dest, err)
	}
}

// The three entrances are ONE import: the same bytes land as the same
// collection whichever way they arrived.
func TestAPIImportPostman_URLAndDocumentProduceTheSameCollection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(importDocumentFixture))
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	byURL := filepath.Join(t.TempDir(), "by-url")
	byDoc := filepath.Join(t.TempDir(), "by-document")

	if resp := vaultCall(t, conn, "api.import.postman", map[string]any{"url": srv.URL, "dest": byURL}, 1); resp.Error != nil {
		t.Fatalf("import by url: %+v", resp.Error)
	}
	if resp := vaultCall(t, conn, "api.import.postman", map[string]any{"document": importDocumentFixture, "dest": byDoc}, 2); resp.Error != nil {
		t.Fatalf("import by document: %+v", resp.Error)
	}
	urlTree := readCollectionTree(t, byURL)
	docTree := readCollectionTree(t, byDoc)
	if len(urlTree) == 0 {
		t.Fatal("the url import produced no files at all")
	}
	if len(urlTree) != len(docTree) {
		t.Fatalf("file counts differ: by url %d, by document %d", len(urlTree), len(docTree))
	}
	for rel, body := range urlTree {
		other, ok := docTree[rel]
		if !ok {
			t.Errorf("%s is in the url import and not in the document import", rel)
			continue
		}
		if body != other {
			t.Errorf("%s differs between the two routes:\n by url:      %s\n by document: %s", rel, body, other)
		}
	}
	if _, ok := urlTree["nocx-collection.json"]; !ok {
		t.Error("no manifest in the fetched collection; the comparison would prove nothing")
	}
}

// readAPIEnvironmentFile decodes one environment the import minted.
func readAPIEnvironmentFile(t *testing.T, path string) apicoll.Environment {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a test-only path under t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var env apicoll.Environment
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return env
}
