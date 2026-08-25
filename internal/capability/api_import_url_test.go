package capability_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/capability"
)

// The URL entrance: the third source, and the one where NOBODY on this side
// holds the bytes yet. What the capability layer owns here is not the HTTP —
// that is apifetch's — but the order: fetch COMPLETELY, then write, so a
// fetch that failed leaves nothing at dest to reason about.

// The export carries a collection variable, because that is what makes the
// import MINT an environment — and the environment is where the route the
// document arrived through has to land.
const urlExport = `{"info":{"name":"acme",` +
	`"schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},` +
	`"variable":[{"key":"baseUrl","value":"https://example.test"}],` +
	`"item":[{"name":"ping","request":{"method":"GET","url":"{{baseUrl}}/ping"}}]}`

// recordingFetcher answers with a fixed document or a fixed refusal, and
// remembers what it was asked for — the URL and the ROUTE, which is the pair
// this method exists to carry.
type recordingFetcher struct {
	doc   string
	err   error
	calls int
	url   string
	route apicoll.Route
}

func (f *recordingFetcher) Fetch(_ context.Context, rawURL string, route apicoll.Route) ([]byte, error) {
	f.calls++
	f.url, f.route = rawURL, route
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.doc), nil
}

func newImportOpWithFetcher(t *testing.T, f apifetch.Fetcher) capability.APIImportOperation {
	t.Helper()
	return capability.NewAPIImportOperation(
		capability.Gate(capability.GateVault, 1, 64, 5*time.Second),
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apiimport.NewOSFS(),
		stubBindWriter{},
		f,
	)
}

// The happy path and the route it carries: the document arrives by URL over
// a connection, and the environment the import mints routes the same way —
// a collection fetched from behind a bastion whose environment said `direct`
// is a collection where every request fails until the person sets by hand
// the thing they had already told the import.
func TestAPIImportService_ImportsAnExportByURLAndKeepsTheRoute(t *testing.T) {
	fetcher := &recordingFetcher{doc: urlExport}
	op := newImportOpWithFetcher(t, fetcher)
	route := apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"}

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		dest := filepath.Join(t.TempDir(), "dest")
		unsup, err := svc.ImportPostmanURL(ctx, "https://acme.test/export.json", route, dest)
		if err != nil {
			t.Fatalf("ImportPostmanURL: %v", err)
		}
		if len(unsup) != 0 {
			t.Errorf("unsupported = %+v, want nothing itemised for an export that converts whole", unsup)
		}
		if fetcher.calls != 1 {
			t.Errorf("the fetcher was called %d times, want once", fetcher.calls)
		}
		if fetcher.url != "https://acme.test/export.json" {
			t.Errorf("fetched %q, want the URL the caller named", fetcher.url)
		}
		if fetcher.route != route {
			t.Errorf("fetched over %+v, want the route the caller named", fetcher.route)
		}
		if _, statErr := os.Lstat(filepath.Join(dest, "nocx-collection.json")); statErr != nil {
			t.Errorf("Lstat(the manifest) = %v, want the imported collection", statErr)
		}

		var env apicoll.Environment
		raw, readErr := os.ReadFile(filepath.Join(dest, "environments", "default.json")) //nolint:gosec // a test-only path under t.TempDir()
		if readErr != nil {
			t.Fatalf("read the minted environment: %v", readErr)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode the minted environment: %v", err)
		}
		if env.Route.Kind != apicoll.RouteConnection || env.Route.ProfileID != "prod-bastion" {
			t.Errorf("the minted environment routes %+v, want the connection the document arrived through", env.Route)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The interval, stated with both ends: from before the fetch until the
// import has arrived, there is NOTHING at dest. A fetch that failed —
// refused, unreachable, not a document — leaves no folder behind, because
// the write does not begin until the last byte is in hand.
func TestAPIImportService_AFailedFetchWritesNothingAtDest(t *testing.T) {
	// The ceiling's own refusal, so this test names the same failure the
	// fetch really produces for a body over apiimport.MaxDocumentBytes
	// rather than a stand-in error.
	refusal := apifetch.ErrTooLarge
	op := newImportOpWithFetcher(t, &recordingFetcher{err: refusal})

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		dest := filepath.Join(t.TempDir(), "dest")
		if _, err := svc.ImportPostmanURL(ctx, "https://acme.test/export.json", apicoll.Route{Kind: apicoll.RouteDirect}, dest); !errors.Is(err, refusal) {
			t.Fatalf("err = %v, want the fetcher's own refusal, not restated as something else", err)
		}
		if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not-exist — a failed fetch writes nothing", dest, statErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A build with no fetcher says so by name rather than pretending. Absence is
// the capability: the renderer draws the entrance from what the backend
// answers, not from what it hopes.
func TestAPIImportService_WithoutAFetcherRefusesTheURLEntranceByName(t *testing.T) {
	op := newImportOpWithFetcher(t, nil)

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		dest := filepath.Join(t.TempDir(), "dest")
		if _, err := svc.ImportPostmanURL(ctx, "https://acme.test/export.json", apicoll.Route{Kind: apicoll.RouteDirect}, dest); !errors.Is(err, capability.ErrImportURLUnavailable) {
			t.Fatalf("err = %v, want ErrImportURLUnavailable", err)
		}
		if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not-exist", dest, statErr)
		}
		// And the paired positive on the same build: the two entrances that
		// need no network still work, so a missing fetcher costs the URL
		// route and nothing else.
		byDoc := filepath.Join(t.TempDir(), "by-document")
		if _, err := svc.ImportPostmanDocument(ctx, urlExport, byDoc); err != nil {
			t.Errorf("ImportPostmanDocument on a build with no fetcher: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A document that fetched cleanly and is not an export still fails at the
// parse, and still leaves nothing behind: the two failures are separate
// sentences and one arrival.
func TestAPIImportService_AFetchedDocumentThatIsNotAnExportLeavesNothing(t *testing.T) {
	op := newImportOpWithFetcher(t, &recordingFetcher{doc: `{"not":"an export"}`})

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		dest := filepath.Join(t.TempDir(), "dest")
		if _, err := svc.ImportPostmanURL(ctx, "https://acme.test/export.json", apicoll.Route{Kind: apicoll.RouteDirect}, dest); err == nil {
			t.Fatal("a fetched document that is not an export was imported")
		}
		if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not-exist", dest, statErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
