package apisend

// The jar, from the user's side: a login that sets a cookie is followed by a
// request that carries it, with no configuration at all. And the property
// that makes the instance cache worth its complexity — the jar is part of
// the Key, so two environments running AT THE SAME TIME share neither a
// cookie nor a dialer.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// TestJar_ALoginCookieIsCarriedByTheNextRequest is the thing a person can
// do: send the login request, then send the next one, and be logged in.
// Nothing is configured, nothing is copied out of a response by hand.
func TestJar_ALoginCookieIsCarriedByTheNextRequest(t *testing.T) {
	var carried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
			_, _ = io.WriteString(w, "ok")
		default:
			if c, err := r.Cookie("session"); err == nil {
				carried = append(carried, c.Value)
			} else {
				carried = append(carried, "")
			}
			_, _ = io.WriteString(w, "me")
		}
	}))
	defer srv.Close()

	c := New()
	k := Key{CookieScope: "collection-a"}
	if _, err := c.Send(context.Background(), apicollGet(srv.URL+"/login"), k); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.Send(context.Background(), apicollGet(srv.URL+"/me"), k); err != nil {
		t.Fatalf("me: %v", err)
	}
	if len(carried) != 1 || carried[0] != "abc123" {
		t.Fatalf("the second request carried %v, want [abc123] — a login must be followed by a "+
			"request that is logged in, with no configuration", carried)
	}
}

// TestJar_DoesNotSurviveARestart pins the decision rather than leaving it to
// be discovered: the jar is per PROCESS. A cookie is credential material,
// and the only place this feature keeps credential material at rest is the
// vault behind the binding document (design §8); a jar file would be a
// second store of credentials that nothing guards. A session cookie is also
// defined to die with the session, and a jar that outlived the process
// would silently turn every one of them into a persistent cookie the server
// never asked for.
//
// The interval, both ends: a cookie exists from the response that set it
// until the server expires it or THIS PROCESS EXITS. The cost is one re-run
// of a login request, which is itself an object in the collection.
func TestJar_DoesNotSurviveARestart(t *testing.T) {
	var carried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			carried = append(carried, c.Value)
		} else {
			carried = append(carried, "")
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/", MaxAge: 86400})
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	k := Key{CookieScope: "collection-a"}
	first := New()
	for range 2 {
		if _, err := first.Send(context.Background(), apicollGet(srv.URL), k); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	// A new Client is this process restarted: same scope, same server, and
	// an explicitly LONG-LIVED cookie, so nothing but the restart can be
	// what dropped it.
	if _, err := New().Send(context.Background(), apicollGet(srv.URL), k); err != nil {
		t.Fatalf("send after restart: %v", err)
	}

	want := []string{"", "abc123", ""}
	if len(carried) != len(want) {
		t.Fatalf("the server saw %v, want %v", carried, want)
	}
	for i := range want {
		if carried[i] != want[i] {
			t.Fatalf("the server saw %v, want %v — the jar is per process and a restart starts empty", carried, want)
		}
	}
}

// TestJar_ConcurrentEnvironmentsShareNeitherCookieNorDialer is the concrete
// reason instances are immutable and cached rather than one client mutated
// per send: with environments IN FLIGHT AT ONCE, a client whose jar or whose
// route were set per call would serve one environment's cookie — or dial one
// environment's bastion — for another.
//
// Three environments, not two, and the third is what makes the cookie half
// mean anything. A and B differ in BOTH halves of the Key, so a client that
// ignored the cookie scope entirely would still keep them apart by route;
// the assertion would pass while the scope did nothing. C shares A's route
// and differs only in scope, so the two halves are pinned separately —
// verified by dropping each half of the Key in turn and watching the
// matching assertion go red.
//
// Run with -race, which is how the gate runs it.
func TestJar_ConcurrentEnvironmentsShareNeitherCookieNorDialer(t *testing.T) {
	r1 := &recordingRoute{Route: localFakeRoute()}
	r2 := &recordingRoute{Route: localFakeRoute()}
	c := New(WithRoutes(func(_ context.Context, id string) (Route, error) {
		if id == "r2" {
			return r2, nil
		}
		return r1, nil
	}))

	envs := []struct {
		cookie string
		key    Key
		route  *recordingRoute
		srv    *httptest.Server
		seen   *seenList
	}{
		{cookie: "a-session", key: Key{RouteID: "r1", CookieScope: "coll-a"}, route: r1},
		{cookie: "b-session", key: Key{RouteID: "r2", CookieScope: "coll-b"}, route: r2},
		// Same route as A, different collection: the scope is the only
		// thing keeping these two apart.
		{cookie: "c-session", key: Key{RouteID: "r1", CookieScope: "coll-c"}, route: r1},
	}
	for i := range envs {
		envs[i].srv, envs[i].seen = cookieEcho(t, envs[i].cookie)
	}

	const rounds = 8
	var wg sync.WaitGroup
	for _, env := range envs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if _, err := c.Send(context.Background(), apicollGet(env.srv.URL), env.key); err != nil {
					t.Errorf("%+v send: %v", env.key, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// No cookie crossed: each server saw only its own, or none at all on
	// the first exchange of its scope.
	for _, env := range envs {
		assertOnly(t, "scope "+env.key.CookieScope, env.seen.values(), env.cookie)
	}

	// No dialer crossed: r2 dialled B's address and nothing else. This is
	// the half a cookie assertion cannot see — a shared client would send
	// the right cookie through the wrong bastion.
	assertOnly(t, "route r2", r2.dialled(), hostPort(t, envs[1].srv.URL))
	for _, addr := range r1.dialled() {
		if addr != hostPort(t, envs[0].srv.URL) && addr != hostPort(t, envs[2].srv.URL) {
			t.Fatalf("route r1 dialled %q, which belongs to neither environment on it", addr)
		}
	}
	if len(r1.dialled()) == 0 {
		t.Fatal("route r1 dialled nothing at all; the test proves nothing")
	}
}

// TestJar_ASecureCookieIsNotSentOverPlainHTTP and its https half, in one
// test because either half alone is worthless: a jar that stored nothing
// passes the http half, and a jar that ignores Secure passes the https one.
//
// It cannot use 127.0.0.1. Go's cookiejar treats loopback as a secure
// origin, exactly as browsers do (net/http/cookiejar.entry.secureMatch), so
// a test against a bare httptest URL asserts the exemption rather than the
// rule. The route resolves example.com — the name httptest's own
// certificate carries — to the loopback address the server is on, which is
// the seam §7.1 already provides for exactly this: WHO RESOLVES THE NAME.
func TestJar_ASecureCookieIsNotSentOverPlainHTTP(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "s3cure", Path: "/", Secure: true})
			_, _ = io.WriteString(w, "ok")
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			_, _ = io.WriteString(w, "session="+c.Value)
		}
	})
	secure := httptest.NewTLSServer(handler)
	defer secure.Close()
	plain := httptest.NewServer(handler)
	defer plain.Close()

	c := newTrusting(trust(secure), WithRoutes(fixedRoute(namedRoute())))
	k := Key{CookieScope: "collection-a"}
	httpsURL := byName(t, secure.URL)
	httpURL := byName(t, plain.URL)

	if _, err := c.Send(context.Background(), apicollGet(httpsURL+"/login"), k); err != nil {
		t.Fatalf("login over https: %v", err)
	}
	ex, err := c.Send(context.Background(), apicollGet(httpsURL+"/me"), k)
	got := answered(t, ex, err)
	if got.Text != "session=s3cure" {
		t.Fatalf("the https follow-up carried %q, want the Secure cookie — the jar stored nothing", got.Text)
	}

	// Same jar, same host, plain http.
	ex, err = c.Send(context.Background(), apicollGet(httpURL+"/me"), k)
	got = answered(t, ex, err)
	if got.Text != "" {
		t.Fatalf("the plain-http request carried %q — a Secure cookie may not leave the channel it was issued for", got.Text)
	}
}

// namedRoute answers for a name that is not loopback and lands on loopback
// anyway. Both halves are needed and for different reasons: the http:// rule
// resolves the name itself and then dials an address literal, while an
// https:// request is dialled BY NAME, so a route that only resolved would
// send the TLS half to whatever the real DNS says example.com is.
func namedRoute() Route {
	d := &net.Dialer{}
	return &fakeRoute{
		resolve: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.IPv4(127, 0, 0, 1)}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}
}

// byName rewrites a test server URL to the name its certificate carries.
func byName(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	u.Host = "example.com:" + u.Port()
	return u.String()
}

// cookieEcho is a server that records the session cookie every request
// carried and sets one of its own.
func cookieEcho(t *testing.T, value string) (*httptest.Server, *seenList) {
	t.Helper()
	seen := &seenList{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			seen.add(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/"})
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

type seenList struct {
	mu sync.Mutex
	vs []string
}

func (s *seenList) add(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vs = append(s.vs, v)
}

func (s *seenList) values() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.vs))
	copy(out, s.vs)
	return out
}

// recordingRoute is a route that remembers every address it was asked to
// dial, so "the dialer did not cross" is an assertion rather than a hope.
type recordingRoute struct {
	Route
	mu    sync.Mutex
	addrs []string
}

func (r *recordingRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.addrs = append(r.addrs, addr)
	r.mu.Unlock()
	return r.Route.DialContext(ctx, network, addr)
}

func (r *recordingRoute) dialled() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.addrs))
	copy(out, r.addrs)
	return out
}

// assertOnly fails when anything other than want appears in got.
func assertOnly(t *testing.T, what string, got []string, want string) {
	t.Helper()
	for _, g := range got {
		if g != want {
			t.Fatalf("%s saw %q among %v, want only %q — it crossed from the other environment", what, g, got, want)
		}
	}
	if len(got) == 0 {
		t.Fatalf("%s saw nothing at all; the test proves nothing", what)
	}
}

// hostPort is the address a route is expected to dial for a test server.
// The http:// rule resolves the name before dialling, so what a route is
// handed is an address literal — which for a test server is its own host.
func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		t.Fatalf("no host in %q: %v", rawURL, err)
	}
	return u.Host
}
