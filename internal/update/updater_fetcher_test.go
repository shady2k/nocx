package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The fetcher built the way main.go builds it (nocx-1ebrs.36)
// ---------------------------------------------------------------------------

// serveManifest answers the two paths Fetch asks for, with whatever bytes
// the test wants on the wire.
func serveManifest(t *testing.T, manifest, sig []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(manifest)
		case "/manifest.json.sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
}

// A fetcher built EXACTLY as main.go builds it — NewGitHubManifestFetcher(nil)
// — must fetch a manifest from a real HTTP server. On 0.6.0 this construction
// panicked on every check: the nil client was stored and dereferenced in get,
// so no user was ever offered an update (nocx-1ebrs.36). Red first.
func TestGitHubManifestFetcherBuiltAsMainBuildsItFetches(t *testing.T) {
	srv := serveManifest(t, []byte("manifest-body"), []byte("c2lnLWJvZHk="))
	defer srv.Close()

	f := NewGitHubManifestFetcher(nil) // exactly main.go's construction
	f.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fetched, err := f.Fetch(ctx)
	body, sig := fetched.Body, fetched.Signature
	if err != nil {
		t.Fatalf("fetch through the construction main.go uses: %v", err)
	}
	if string(body) != "manifest-body" {
		t.Errorf("manifest body = %q, want %q", body, "manifest-body")
	}
	if string(sig) != "c2lnLWJvZHk=" {
		t.Errorf("signature = %q, want %q", sig, "c2lnLWJvZHk=")
	}
}

// The paired failure path: built the same way, pointed at a server that
// errors, the fetcher must return a typed error — not panic and not swallow.
func TestGitHubManifestFetcherBuiltAsMainBuildsItReportsAServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := NewGitHubManifestFetcher(nil) // exactly main.go's construction
	f.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("a server error must surface as an error, not a nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status, got: %v", err)
	}
}
