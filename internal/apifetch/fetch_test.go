package apifetch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
)

// directRoutes is the table under test: the one route this test needs,
// answered the way apisend answers the empty RouteID.
func directRoutes() apisend.Routes {
	return func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.Local(), nil
	}
}

const export = `{"info":{"name":"acme"},"item":[]}`

func direct() apicoll.Route { return apicoll.Route{Kind: apicoll.RouteDirect} }

func TestFetch_ReturnsTheDocumentOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No Content-Type on purpose: the first byte is what decides.
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	got, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != export {
		t.Errorf("body = %q, want the export", got)
	}
}

func TestFetch_RefusesANonHTTPScheme(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://h/a.json", "/etc/passwd"} {
		t.Run(raw, func(t *testing.T) {
			_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), raw, direct())
			if !errors.Is(err, apifetch.ErrScheme) {
				t.Fatalf("err = %v, want ErrScheme", err)
			}
		})
	}
}

// The scheme is refused BEFORE any dial: the route table is never even
// asked, so nothing could have been opened by the time the refusal comes.
func TestFetch_RefusesASchemeBeforeAnyDial(t *testing.T) {
	asked := false
	routes := func(_ context.Context, _ string) (httppolicy.Route, error) {
		asked = true
		return httppolicy.Local(), nil
	}
	if _, err := apifetch.New(routes, nil).Fetch(context.Background(), "file:///etc/passwd", direct()); !errors.Is(err, apifetch.ErrScheme) {
		t.Fatalf("err = %v, want ErrScheme", err)
	}
	if asked {
		t.Error("the route table was asked for a route before the scheme was refused")
	}
}

func TestFetch_RefusesWhatIsNotADocumentWithoutMentioningCurl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if !errors.Is(err, apifetch.ErrNotADocument) {
		t.Fatalf("err = %v, want ErrNotADocument", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "curl") {
		t.Errorf("the refusal mentions curl, which this ask never offered: %v", err)
	}
}

// An empty body is not a document either, and it is the one shape a
// first-byte loop can walk off the end of.
func TestFetch_RefusesAnEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if !errors.Is(err, apifetch.ErrNotADocument) {
		t.Fatalf("err = %v, want ErrNotADocument", err)
	}
}

// Content-Type is not consulted, in EITHER direction: a server that labels
// its JSON as HTML is believed about the bytes, and one that labels its
// login page as JSON is not.
func TestFetch_DoesNotConsultContentType(t *testing.T) {
	t.Run("json labelled text/html is imported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("\n\t " + export))
		}))
		defer srv.Close()

		got, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if string(got) != export {
			t.Errorf("body = %q, want the export with its leading space skipped", got)
		}
	})
	t.Run("html labelled application/json is refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
		}))
		defer srv.Close()

		if _, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct()); !errors.Is(err, apifetch.ErrNotADocument) {
			t.Fatalf("err = %v, want ErrNotADocument", err)
		}
	})
}

func TestFetch_RefusesABodyOverTheCeiling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		chunk := strings.Repeat("x", 1<<16)
		for written := 1; written <= (16 << 20); written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if !errors.Is(err, apifetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestFetch_ReportsAServerRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the status in the sentence", err)
	}
	if errors.Is(err, apifetch.ErrNotADocument) {
		t.Errorf("a 404 was reported as a document problem: %v", err)
	}
}

// A server that is not there at all: the dial fails, and the refusal says
// which address it was.
func TestFetch_ReportsARefusedConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), addr, direct())
	if err == nil {
		t.Fatal("fetching a closed server succeeded")
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err = %v, want the address in the sentence", err)
	}
}

// The route table's refusal is the fetch's refusal: a connection that cannot
// be leased must never quietly become a direct dial out of this machine,
// around the bastion the person named.
func TestFetch_RefusesWhenTheRouteCannotBeLeased(t *testing.T) {
	dialed := false
	routes := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID == "" {
			dialed = true
			return httppolicy.Local(), nil
		}
		return nil, errors.New("that connection is not available")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	_, err := apifetch.New(routes, nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if err == nil {
		t.Fatal("a connection route that could not be leased was fetched anyway")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("err = %v, want the table's own sentence", err)
	}
	if dialed {
		t.Error("the fetch fell back to the direct route; a fetch through a connection must refuse instead")
	}
}

// A route that does not say how to get there is refused by apisend's own
// derivation rather than by a second one written here.
func TestFetch_RefusesARouteThatNamesNoConnection(t *testing.T) {
	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), "https://example.test/a.json", apicoll.Route{Kind: apicoll.RouteConnection})
	if err == nil {
		t.Fatal("a connection route naming no connection was accepted")
	}
	if !strings.Contains(err.Error(), "which one") {
		t.Errorf("err = %v, want apisend's own refusal", err)
	}
}

// The redirect chain is bounded, which is only true if the policy's
// CheckRedirect is actually attached to the client that follows them.
func TestFetch_StopsAnEndlessRedirectChain(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).Fetch(context.Background(), srv.URL, direct())
	if err == nil {
		t.Fatal("an endless redirect chain was followed to the end")
	}
	if !strings.Contains(err.Error(), "redirects") {
		t.Errorf("err = %v, want the chain bound named", err)
	}
}

// A FETCH IS NOT AN ENVIRONMENT. insecureTls is the environment's own
// setting (apicoll/collection.go), and a route carrying it into a fetch must
// not turn verification off: the https server below is trusted by nobody, so
// a fetch that verifies fails and a fetch that does not would succeed.
func TestFetch_NeverTurnsCertificateVerificationOff(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteDirect, InsecureTLS: true})
	if err == nil {
		t.Fatal("a route saying insecureTls fetched from an untrusted certificate; the fetch turned verification off")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want the certificate failure", err)
	}
}

// A cancelled context stops the fetch rather than the timeout doing it: the
// ask is modal, and a person who closes it is not waiting a minute.
func TestFetch_HonoursACancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := apifetch.New(directRoutes(), nil).Fetch(ctx, srv.URL, direct()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
