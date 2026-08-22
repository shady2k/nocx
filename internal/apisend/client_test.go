package apisend

// A client instance is immutable and cached by Key. The property this pins
// is the one the design gives as the reason: one shared mutable client
// cannot hold a per-environment cookie jar and a per-call route at the same
// time without leaking one environment's cookies — or one environment's
// route — into another environment's request.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInstances_AreCachedByKeyAndOnlyByKey.
func TestInstances_AreCachedByKeyAndOnlyByKey(t *testing.T) {
	c := New(WithRoutes(fixedRoute(localFakeRoute())))
	ctx := context.Background()

	a1, err := c.instanceFor(ctx, Key{RouteID: "r1", CookieScope: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := c.instanceFor(ctx, Key{RouteID: "r1", CookieScope: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Error("the same Key built two instances — the cache is not keyed by what it says")
	}

	for _, k := range []Key{
		{RouteID: "r2", CookieScope: "prod"},
		{RouteID: "r1", CookieScope: "staging"},
	} {
		other, err := c.instanceFor(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if other == a1 {
			t.Errorf("%+v shares an instance with {r1 prod} — a different route or a "+
				"different cookie scope is a different client", k)
		}
	}
}

// TestCookies_StayInsideTheirScope: the jar belongs to the instance, which
// belongs to the CookieScope. A cookie set for one environment is sent back
// for that environment and is invisible to another.
func TestCookies_StayInsideTheirScope(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			seen = append(seen, c.Value)
		} else {
			seen = append(seen, "")
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1", Path: "/"})
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	c := New()
	prod := Key{CookieScope: "prod"}
	staging := Key{CookieScope: "staging"}
	for _, k := range []Key{prod, prod, staging} {
		if _, err := c.Send(context.Background(), apicollGet(srv.URL), k); err != nil {
			t.Fatalf("Send %+v: %v", k, err)
		}
	}
	want := []string{"", "s1", ""}
	if len(seen) != len(want) {
		t.Fatalf("the server saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("the server saw %v, want %v — the second prod send returns prod's "+
				"cookie and staging has never seen one", seen, want)
		}
	}
}

// TestUnknownRouteIsRefusedByName: a request the user routed through a
// connection must never quietly go out of this machine's own interface.
func TestUnknownRouteIsRefusedByName(t *testing.T) {
	ex, err := New().Send(context.Background(),
		apicollGet("http://127.0.0.1:1/"), Key{RouteID: "prod-bastion"})
	fail := failedAt(t, ex, err, PhaseConnection)
	if !strings.Contains(fail.Reason, "prod-bastion") {
		t.Fatalf("reason = %q, want it to name the route it does not know", fail.Reason)
	}
}

// TestNoGlobalGateAcrossNetworkIO: the instance table's lock is held for a
// map lookup and a map insert, never across resolving a route or dialling.
// The proof is a state change, not a duration: one send is parked inside its
// route lookup, and a second send with a different Key must reach the
// network and finish while it is parked. If the gate were held, the second
// send could not start and this test would never see its result.
func TestNoGlobalGateAcrossNetworkIO(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	parked := make(chan struct{})
	release := make(chan struct{})
	routes := func(_ context.Context, id string) (Route, error) {
		if id == "slow" {
			close(parked)
			<-release
		}
		return localFakeRoute(), nil
	}
	c := New(WithRoutes(routes))

	slowDone := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "slow"})
		slowDone <- err
	}()
	<-parked // the slow send is now inside its route lookup

	fastDone := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "fast"})
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("the second send failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		// Not a timing assertion: the pass path does not wait. This is the
		// backstop that turns a deadlock into a named failure instead of a
		// panic ten minutes later.
		t.Fatal("the second send never completed while the first was parked in its route " +
			"lookup — a global gate is held across route resolution")
	}

	close(release)
	if err := <-slowDone; err != nil {
		t.Fatalf("the parked send failed after release: %v", err)
	}
}

// TestRouteFailureIsReportedNotSwallowed: the route table is an external
// call like any other (acquiring an SSH pool lease is one), so it has a
// failing test and a succeeding one.
func TestRouteFailureIsReportedNotSwallowed(t *testing.T) {
	boom := errors.New("no lease available")
	ex, err := New(WithRoutes(func(context.Context, string) (Route, error) {
		return nil, boom
	})).Send(context.Background(), apicollGet("http://127.0.0.1:1/"), Key{})
	if fail := failedAt(t, ex, err, PhaseConnection); !strings.Contains(fail.Reason, boom.Error()) {
		t.Fatalf("reason = %q, want the route table's error in it", fail.Reason)
	}
}

// TestNilRouteIsRefused: a table that answers with nothing is a bug in the
// table, and a nil route would be a panic in the dial rather than a message.
func TestNilRouteIsRefused(t *testing.T) {
	ex, err := New(WithRoutes(func(context.Context, string) (Route, error) {
		return nil, nil
	})).Send(context.Background(), apicollGet("http://127.0.0.1:1/"), Key{RouteID: "empty"})
	if fail := failedAt(t, ex, err, PhaseConnection); !strings.Contains(fail.Reason, "resolved to nothing") {
		t.Fatalf("reason = %q, want a refusal naming the empty route", fail.Reason)
	}
}

// localFakeRoute is this machine's route, built through the same seam a
// connection route uses.
func localFakeRoute() Route {
	return &fakeRoute{
		resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, nil
		},
		dial: (&net.Dialer{}).DialContext,
	}
}

// ── the two seams that replaced two options ──────────────────────────────
//
// WithMaxBytes and WithTLSClientConfig were exported Options that nothing in
// the product set — the deadcode ratchet reported both, and it was right.
// What the tests need is not an option: it is a Client whose ceiling is small
// enough to cross in a test, and one that trusts an httptest server's own
// certificate. Both are fields on Client and both are still read on the
// production path; these set them directly, in the same package, so the
// BEHAVIOUR under test (truncation, TLS) stays testable while the product
// surface keeps only the knobs it actually has.

// newBounded builds a sender whose control-plane ceiling is n bytes.
func newBounded(n int64, opts ...Option) *Client {
	c := New(opts...)
	c.limit = n
	return c
}

// newTrusting builds a sender that trusts cfg and nothing else. It is a way
// to supply trust, never a way to skip verification.
func newTrusting(cfg *tls.Config, opts ...Option) *Client {
	c := New(opts...)
	c.tlsConfig = cfg
	return c
}
