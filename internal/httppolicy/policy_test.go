package httppolicy

// This package now owns the address rule, so this suite is where the rule is
// pinned: what a caller can and cannot reach, checked on the connection and
// on every redirect hop, plus the property the extraction exists for — the
// resolve and the dial both go through the caller's route, so a caller whose
// name is resolved somewhere else can obey the same rule.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// testPublicIP is a public, non-routable address (TEST-NET-3) — public per
// the rule, so http:// to it must be refused, and nothing ever dials it.
const testPublicIP = "203.0.113.7"

func TestCheckDestination(t *testing.T) {
	cases := []struct {
		name   string
		scheme string
		host   string
		ips    []net.IP
		wantOK bool
	}{
		{"https public allowed", "https", "api.openai.com", nil, true},
		{"https private allowed", "https", "127.0.0.1", nil, true},
		{"http loopback v4", "http", "127.0.0.1", []net.IP{net.ParseIP("127.0.0.1")}, true},
		{"http loopback other 127", "http", "127.1.2.3", []net.IP{net.ParseIP("127.1.2.3")}, true},
		{"http loopback v6", "http", "::1", []net.IP{net.ParseIP("::1")}, true},
		{"http rfc1918", "http", "10.0.0.5", []net.IP{net.ParseIP("10.0.0.5")}, true},
		{"http rfc1918 172.16", "http", "172.16.4.4", []net.IP{net.ParseIP("172.16.4.4")}, true},
		{"http rfc1918 192.168", "http", "192.168.1.10", []net.IP{net.ParseIP("192.168.1.10")}, true},
		{"http link-local", "http", "169.254.169.254", []net.IP{net.ParseIP("169.254.169.254")}, true},
		{"http v6 link-local", "http", "fe80::1", []net.IP{net.ParseIP("fe80::1")}, true},
		{"http ULA", "http", "fd00::1", []net.IP{net.ParseIP("fd00::1")}, true},
		{"http v4-mapped loopback", "http", "::ffff:127.0.0.1", []net.IP{net.ParseIP("::ffff:127.0.0.1")}, true},
		{"http v4-mapped private", "http", "::ffff:10.1.2.3", []net.IP{net.ParseIP("::ffff:10.1.2.3")}, true},
		{"http public refused", "http", testPublicIP, []net.IP{net.ParseIP(testPublicIP)}, false},
		{"http public name refused", "http", "api.example.com", []net.IP{net.ParseIP("198.51.100.9")}, false},
		{"http mixed public+private refused", "http", "split.example", []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(testPublicIP)}, false},
		{"http no resolution refused", "http", "no-such-host.invalid", nil, false},
		{"http nil address refused", "http", "nil.example", []net.IP{nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckDestination("apisend", c.scheme, c.host, c.ips)
			if c.wantOK && err != nil {
				t.Fatalf("CheckDestination(%s, %s) = %v, want nil", c.scheme, c.host, err)
			}
			if !c.wantOK && err == nil {
				t.Fatalf("CheckDestination(%s, %s) = nil, want a refusal", c.scheme, c.host)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "apisend: ") {
				t.Fatalf("refusal = %v, want it to name the calling component", err)
			}
		})
	}
}

// recordingRoute is a route that resolves and dials however the test says,
// and remembers every address it was asked for. It stands in for the SSH
// route the extraction exists for: a caller whose name is resolved and whose
// socket is opened somewhere other than net.Dialer.
type recordingRoute struct {
	resolve ResolverFunc
	dial    DialerFunc

	mu    sync.Mutex
	dials []string
}

func (r *recordingRoute) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return r.resolve(ctx, host)
}

func (r *recordingRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.dials = append(r.dials, addr)
	r.mu.Unlock()
	return r.dial(ctx, network, addr)
}

func (r *recordingRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

func (r *recordingRoute) dialed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dials...)
}

func newRecordingRoute(resolve ResolverFunc) *recordingRoute {
	if resolve == nil {
		resolve = SystemResolver().LookupIP
	}
	return &recordingRoute{
		resolve: resolve,
		dial:    (&net.Dialer{}).DialContext,
	}
}

// TestRouteOpensEveryConnection is the extraction's own property: the policy
// never dials for itself. Both schemes go through the supplied route, which
// is what lets a connection route dial on the far side without a second
// copy of the rule.
func TestRouteOpensEveryConnection(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer secure.Close()

	route := newRecordingRoute(nil)
	cl := newPolicyClient(Params{Component: "apisend", Route: route, TLSClientConfig: trust(secure)})

	for _, u := range []string{plain.URL, secure.URL} {
		resp, err := cl.Get(u)
		if err != nil {
			t.Fatalf("get %s: %v", u, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := route.dialed(); len(got) != 2 {
		t.Fatalf("route dialled %v, want one dial per request (http AND https)", got)
	}
}

// trust is the test's TLS trust: the httptest server's own certificate, and
// nothing else. It is the seam a caller uses to supply trust for an internal
// endpoint; it is not a way to skip verification.
func trust(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// TestDialsExactlyTheValidatedResolution is reason 1: the guard resolves,
// validates and dials ONE answer. The route maps a name no resolver would
// answer onto the test server's address, and the dial that happens is that
// address — not the name.
func TestDialsExactlyTheValidatedResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	route := newRecordingRoute(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	cl := newPolicyClient(Params{Component: "apisend", Route: route})

	resp, err := cl.Get("http://resolved.invalid:" + port + "/")
	if err != nil {
		t.Fatalf("get via injected resolution: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	want := net.JoinHostPort("127.0.0.1", port)
	if got := route.dialed(); len(got) != 1 || got[0] != want {
		t.Fatalf("dialled %v, want exactly [%s] — the validated address, never the name", got, want)
	}
}

// TestResolveFailureIsReported is the DNS failure path of §12.1. Its pair is
// TestDialsExactlyTheValidatedResolution above, where the same route
// answers and the request succeeds.
func TestResolveFailureIsReported(t *testing.T) {
	boom := errors.New("no such host")
	route := newRecordingRoute(func(context.Context, string) ([]net.IP, error) { return nil, boom })
	cl := newPolicyClient(Params{Component: "apisend", Route: route})

	_, err := cl.Get("http://whatever.invalid/")
	if err == nil {
		t.Fatal("get with a failing resolver succeeded, want the resolve error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolver's error wrapped", err)
	}
	if got := route.dialed(); len(got) != 0 {
		t.Fatalf("dialled %v after a resolve failure, want nothing", got)
	}
}

// TestPublicHTTPRefusedOnTheConnection: the refusal happens at the
// connection, before any socket is opened, and it names the scheme.
func TestPublicHTTPRefusedOnTheConnection(t *testing.T) {
	route := newRecordingRoute(func(_ context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(testPublicIP)}, nil
	})
	cl := newPolicyClient(Params{Component: "apisend", Route: route})

	_, err := cl.Get("http://" + testPublicIP + "/v1")
	if err == nil {
		t.Fatal("public http:// succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "http://") {
		t.Fatalf("refusal = %v, want an address-rule error naming the scheme", err)
	}
	if got := route.dialed(); len(got) != 0 {
		t.Fatalf("dialled %v, want nothing — the refusal precedes the socket", got)
	}
}

// TestLoopbackHTTPAllowed is the pair: the same scheme, an address the rule
// permits, and the request lands.
func TestLoopbackHTTPAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	cl := newPolicyClient(Params{Component: "apisend", Route: Local()})
	resp, err := cl.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback http: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

// TestUnsupportedSchemeRefused: anything that is not http or https is not a
// destination this policy knows how to judge, so it is refused rather than
// waved through.
func TestUnsupportedSchemeRefused(t *testing.T) {
	cl := newPolicyClient(Params{Component: "apisend", Route: Local()})
	_, err := cl.Get("ftp://example.com/x")
	if err == nil || !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("err = %v, want an unsupported-scheme refusal", err)
	}
}

// TestRedirectRecheckedAsANewEndpoint is reason 2: a hop is a new endpoint.
// Loopback → public http:// is refused and the public target is never
// contacted.
func TestRedirectRecheckedAsANewEndpoint(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+testPublicIP+"/v1", http.StatusFound)
	}))
	defer redirector.Close()

	route := newRecordingRoute(nil)
	cl := newPolicyClient(Params{Component: "apisend", Route: route})
	resp, err := cl.Get(redirector.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to public http:// followed, want a refusal")
	}
	for _, addr := range route.dialed() {
		if strings.HasPrefix(addr, testPublicIP) {
			t.Fatalf("dialled the public redirect target %q", addr)
		}
	}
}

// TestCredentialNeverCrossesOriginChange, and its pair: the same hop within
// one origin keeps it.
func TestCredentialNeverCrossesOriginChange(t *testing.T) {
	var sawAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAtTarget = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-secret" {
			t.Errorf("origin A saw Authorization %q, want Bearer sk-secret", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	cl := newPolicyClient(Params{Component: "apisend", Route: Local()})
	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAtTarget != "" {
		t.Fatalf("Authorization forwarded across an origin change: %q", sawAtTarget)
	}
}

func TestCredentialSurvivesSameOriginRedirect(t *testing.T) {
	var sawAtEnd string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/done", http.StatusFound)
			return
		}
		sawAtEnd = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	cl := newPolicyClient(Params{Component: "apisend", Route: Local()})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAtEnd != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q after a same-origin hop, want it kept", sawAtEnd)
	}
}

// TestCustomHeadersFollowTheCredentialRule: a header value can BE the
// credential, so the names the caller declares are dropped on a crossing and
// kept within one origin.
func TestCustomHeadersFollowTheCredentialRule(t *testing.T) {
	var sawAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAtTarget = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "k" {
			t.Errorf("origin A saw X-Api-Key %q, want k", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	cl := newPolicyClient(Params{Component: "apisend", Route: Local()})
	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	req.Header.Set("X-Api-Key", "k")
	req = req.WithContext(WithCustomHeaderNames(req.Context(), []string{"X-Api-Key"}))
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAtTarget != "" {
		t.Fatalf("X-Api-Key forwarded across an origin change: %q", sawAtTarget)
	}
}

// TestRedirectChainIsBounded: the bound is the caller's, and the message
// names it.
func TestRedirectChainIsBounded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	cl := newPolicyClient(Params{Component: "apisend", Route: Local(), MaxRedirects: 3})
	_, err := cl.Get(srv.URL) //nolint:bodyclose // the client returns an error, not a body
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") {
		t.Fatalf("err = %v, want the chain bounded at 3", err)
	}
}

// TestHTTPDialsDirectAndHTTPSKeepsTheRouteProxy is reason 4 in both
// directions: http never asks the route for a proxy; https does.
func TestHTTPDialsDirectAndHTTPSKeepsTheRouteProxy(t *testing.T) {
	proxied := &url.URL{Scheme: "http", Host: "proxy.example:3128"}
	var asked int
	route := NewRoute(SystemResolver(), &net.Dialer{}, func(*http.Request) (*url.URL, error) {
		asked++
		return proxied, nil
	})
	tr := NewTransport(Params{Component: "apisend", Route: route})

	httpsReq, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	u, err := tr.Proxy(httpsReq)
	if err != nil {
		t.Fatalf("proxy https: %v", err)
	}
	if u == nil || u.Host != proxied.Host {
		t.Fatalf("https proxy = %v, want the route's proxy", u)
	}

	httpReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:11434/v1", nil)
	u2, err := tr.Proxy(httpReq)
	if err != nil {
		t.Fatalf("proxy http: %v", err)
	}
	if u2 != nil {
		t.Fatalf("http proxy = %v, want nil (direct dial for http)", u2)
	}
	if asked != 1 {
		t.Fatalf("the route was asked for a proxy %d times, want 1 — http must never ask", asked)
	}
}

// TestNilProxyRouteNeverProxies: a tunnelled route describes a network this
// machine's proxy configuration does not, so nil means direct.
func TestNilProxyRouteNeverProxies(t *testing.T) {
	tr := NewTransport(Params{Component: "apisend", Route: NewRoute(SystemResolver(), &net.Dialer{}, nil)})
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	u, err := tr.Proxy(req)
	if err != nil || u != nil {
		t.Fatalf("proxy = %v, %v, want nil, nil", u, err)
	}
}

// TestEnvironmentProxyIsReadFreshPerRequest: the local route reads the
// environment at call time, not once per process.
func TestEnvironmentProxyIsReadFreshPerRequest(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)

	t.Setenv("HTTPS_PROXY", "http://first.example:3128")
	u, err := EnvironmentProxy(req)
	if err != nil || u == nil || u.Host != "first.example:3128" {
		t.Fatalf("proxy = %v, %v, want first.example:3128", u, err)
	}
	t.Setenv("HTTPS_PROXY", "http://second.example:3128")
	u, err = EnvironmentProxy(req)
	if err != nil || u == nil || u.Host != "second.example:3128" {
		t.Fatalf("proxy = %v, %v, want second.example:3128 — the env is read per request", u, err)
	}
}

func TestOriginIsTheRFC6454Triple(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"http://example.com/x", "http://EXAMPLE.com/y", true},
		{"http://example.com/x", "https://example.com/x", false},
		{"http://example.com/x", "http://example.com:8080/x", false},
		{"http://example.com/x", "http://api.example.com/x", false},
	}
	for _, c := range cases {
		ua, _ := url.Parse(c.a)
		ub, _ := url.Parse(c.b)
		if got := Origin(ua) == Origin(ub); got != c.same {
			t.Errorf("Origin(%s) == Origin(%s) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

// TestSystemResolverAnswersOnAnOrdinaryMachine is the pair to
// TestResolveFailureIsReported: localhost resolves to loopback here, so the
// rule's ordinary case works without an injected resolver.
func TestSystemResolverAnswersOnAnOrdinaryMachine(t *testing.T) {
	ips, err := SystemResolver().LookupIP(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("resolving localhost: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("localhost resolved to nothing")
	}
	for _, ip := range ips {
		if !isPrivateLoopback(ip) {
			t.Fatalf("localhost resolved to %s, which the rule calls public", ip)
		}
	}
}

// newPolicyClient is the assembly every caller of this package performs: a
// guarded transport, plus the redirect rule that belongs to it. There is no
// exported constructor for it — there was one, and nothing outside these
// tests ever called it, so it was a second way to spell what
// internal/apisend and internal/assistant both already do inline. The two
// lines live here instead, where the thing being asserted is the POLICY and
// not the shape of a convenience.
func newPolicyClient(p Params) *http.Client {
	t := NewTransport(p)
	return &http.Client{Transport: t, CheckRedirect: t.CheckRedirect}
}
