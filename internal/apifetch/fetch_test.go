package apifetch_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// THE ROUTE PARAMETER IS NOT DECORATIVE. Everything below is one claim in
// four parts: the route a person chose names the route table's own id, the
// bytes travel over the route the table answered with, a connection that
// cannot be leased REFUSES rather than going out of this machine, and no
// route ever verifies less than normal.

// recordingRoutes is the route table as a witness: it remembers every id it
// was asked for, in order, and answers each from a table the test supplies.
// A route the test did not name is refused the way apisend refuses an
// unknown id — never with a direct route, which is the fallback these tests
// exist to forbid.
func recordingRoutes(t *testing.T, answers map[string]httppolicy.Route) (apisend.Routes, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	fn := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		mu.Lock()
		asked = append(asked, routeID)
		mu.Unlock()
		r, ok := answers[routeID]
		if !ok {
			return nil, fmt.Errorf("no route named %q", routeID)
		}
		return r, nil
	}
	return fn, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// tunnelledRoute stands in for what a connection lease produces: it dials
// somewhere of its OWN choosing — the far side of a tunnel, here a local
// server the test holds — and reports the addresses that dial will reach,
// which is httppolicy.Route's whole contract (dial.go). It counts its dials
// so a test can assert the bytes really went through it.
type tunnelledRoute struct {
	target string // the host:port every dial actually reaches
	dials  atomic.Int64
}

func (r *tunnelledRoute) LookupIP(_ context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return nil, fmt.Errorf("tunnelledRoute: %q is resolved on the far side", host)
}

func (r *tunnelledRoute) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	r.dials.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, r.target)
}

func (r *tunnelledRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

// forbiddenRoute is the direct route in a test about a CONNECTION: reaching
// it at all is the defect, so every method on it fails the test rather than
// returning something the fetch could use.
type forbiddenRoute struct{ t *testing.T }

func (r forbiddenRoute) LookupIP(context.Context, string) ([]net.IP, error) {
	r.t.Error("the fetch resolved a name on the direct route; a fetch through a connection must refuse instead")
	return nil, errors.New("forbidden")
}

func (r forbiddenRoute) DialContext(context.Context, string, string) (net.Conn, error) {
	r.t.Error("the fetch dialled the direct route; that is a request on this machine's own interface, around the bastion the person named")
	return nil, errors.New("forbidden")
}

func (r forbiddenRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

// The id is apisend's own derivation (RouteIDFor) and not a second spelling
// written here: a connection route reaches the table as exactly
// `connection:<profileId>`, once.
func TestFetch_UsesTheRouteIdOfTheChosenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	routes, asked := recordingRoutes(t, map[string]httppolicy.Route{
		"connection:prod-bastion": httppolicy.Local(),
		"":                        forbiddenRoute{t},
	})

	got, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != export {
		t.Errorf("body = %q, want the export", got)
	}
	if want := []string{"connection:prod-bastion"}; !slices.Equal(asked(), want) {
		t.Fatalf("the route table was asked for %v, want %v", asked(), want)
	}
}

// The direct route is asked for by the id apisend gives it — the empty one —
// so a direct import and no route at all are one route rather than two.
func TestFetch_UsesTheEmptyRouteIdForADirectFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	routes, asked := recordingRoutes(t, map[string]httppolicy.Route{"": httppolicy.Local()})

	if _, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL, direct()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if want := []string{""}; !slices.Equal(asked(), want) {
		t.Fatalf("the route table was asked for %v, want %v", asked(), want)
	}
}

// THE PAIRED POSITIVE, and the strongest form of "not decorative": the
// document really arrives over the route the table answered with. The
// server is reachable ONLY through that route's dialer — the direct route
// fails the test if it is touched — so a fetch that got the bytes got them
// through the connection the person chose.
func TestFetch_TravelsOverTheRouteTheTableAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	tunnel := &tunnelledRoute{target: strings.TrimPrefix(srv.URL, "http://")}
	routes, _ := recordingRoutes(t, map[string]httppolicy.Route{
		"connection:prod-bastion": tunnel,
		"":                        forbiddenRoute{t},
	})

	got, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != export {
		t.Errorf("body = %q, want the export", got)
	}
	if n := tunnel.dials.Load(); n != 1 {
		t.Errorf("the connection route dialled %d times, want once — the bytes did not travel over the route the person chose", n)
	}
}

// The refusal this whole task exists for, in apisend's own words. A
// connection that cannot be leased ends the fetch: the direct route is never
// asked for, its dialer fails the test if it is reached, and the server the
// URL names fails the test if a request arrives at all — the three ways a
// fallback could show itself.
func TestFetch_RefusesRatherThanDiallingDirectlyWhenTheConnectionIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request reached the server after the connection was refused; the fetch went around the bastion")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var asked []string
	routes := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		mu.Lock()
		asked = append(asked, routeID)
		mu.Unlock()
		if routeID == "" {
			// The fallback, made available on purpose so that taking it is
			// a test failure rather than a connection refused by luck.
			return forbiddenRoute{t}, nil
		}
		return nil, apisend.ErrNoConnection
	}

	_, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if !errors.Is(err, apisend.ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection — a fetch that fell back would go around the bastion", err)
	}
	if !strings.Contains(err.Error(), "connection that is not available") {
		t.Errorf("err = %v, want the table's own sentence rather than a second vocabulary for it", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"connection:prod-bastion"}; !slices.Equal(asked, want) {
		t.Fatalf("the route table was asked for %v, want %v — the direct route was reached for", asked, want)
	}
}

// And the same refusal seen from the other end: on an ordinary machine, with
// the same profile leasable, the same call succeeds. A suite of only
// failures cannot report a feature that never works.
func TestFetch_SucceedsOverTheSameConnectionWhenItIsAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	down := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID == "connection:prod-bastion" {
			return nil, apisend.ErrNoConnection
		}
		return httppolicy.Local(), nil
	}
	up := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID == "connection:prod-bastion" {
			return httppolicy.Local(), nil
		}
		return nil, errors.New("no route named " + routeID)
	}
	route := apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"}

	if _, err := apifetch.New(down, nil).Fetch(context.Background(), srv.URL, route); !errors.Is(err, apisend.ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection while the connection is down", err)
	}
	got, err := apifetch.New(up, nil).Fetch(context.Background(), srv.URL, route)
	if err != nil {
		t.Fatalf("Fetch over an available connection: %v", err)
	}
	if string(got) != export {
		t.Errorf("body = %q, want the export", got)
	}
}

// A FETCH IS NOT AN ENVIRONMENT, over a CONNECTION as well. The behavioural
// half of the insecureTls claim is asserted for the direct route above; this
// is the same assertion for the route that has a person's bastion behind it,
// because "no TLSClientConfig on any route, ever" is not a claim one route
// can carry.
func TestFetch_NeverTurnsCertificateVerificationOffOnAConnectionRouteEither(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	tunnel := &tunnelledRoute{target: strings.TrimPrefix(srv.URL, "https://")}
	routes, _ := recordingRoutes(t, map[string]httppolicy.Route{
		"connection:prod-bastion": tunnel,
		"":                        forbiddenRoute{t},
	})

	_, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion", InsecureTLS: true})
	if err == nil {
		t.Fatal("a connection route saying insecureTls fetched from an untrusted certificate; the fetch turned verification off")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want the certificate failure", err)
	}
}

// unleasableRoute is the shape apisend's OWN table produces for a connection
// it cannot lease: the table hands back a route and the refusal arrives at
// the dial (routes.go's connectionRoute.dial), because the lease is taken
// when it is needed rather than when the route is looked up. A test that
// only ever saw the table itself refuse would be asserting a path production
// does not take.
type unleasableRoute struct{}

func (unleasableRoute) LookupIP(_ context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return nil, fmt.Errorf("unleasableRoute: %q is resolved on the far side", host)
}

func (unleasableRoute) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: connection %q: no SSH pool is wired into this sender",
		apisend.ErrNoConnection, "prod-bastion")
}

func (unleasableRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

// The refusal in the shape it really arrives in: the lease fails at the
// dial, and it ENDS the fetch. The sentinel survives every wrapping between
// the dialer and the caller — a surface that cannot tell "that connection is
// not available" from "the server refused" offers the wrong remedy — and the
// server the URL names never sees a request, which is what "never dialled
// around the bastion" means from the far end.
func TestFetch_RefusesWhenTheConnectionCannotBeLeasedAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request reached the server after the lease failed; the fetch went around the bastion")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	routes, asked := recordingRoutes(t, map[string]httppolicy.Route{
		"connection:prod-bastion": unleasableRoute{},
		"":                        forbiddenRoute{t},
	})

	_, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if !errors.Is(err, apisend.ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection through every wrapping between the dialer and here", err)
	}
	if want := []string{"connection:prod-bastion"}; !slices.Equal(asked(), want) {
		t.Fatalf("the route table was asked for %v, want %v — the direct route was reached for", asked(), want)
	}
}
