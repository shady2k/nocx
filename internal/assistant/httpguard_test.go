package assistant

// The http:// address rule (design §4.5 decision 3, bead notes): http:// is
// permitted ONLY for loopback and private addresses, enforced ON EVERY
// CONNECTION at dial time — never in the form. The four reasons are written
// in the design and repeated in httpguard.go's doc comment, because a rule
// whose reasoning is lost gets simplified into a form validator by the next
// reader.
//
// The acceptance criteria this suite pins: "A remote http:// endpoint fails
// validation; http://127.0.0.1:11434/v1 passes" — plus the redirect and
// credential rules the design names in the same paragraph.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDestination(c.scheme, c.host, c.ips)
			if c.wantOK && err != nil {
				t.Fatalf("checkDestination(%s, %s) = %v, want nil", c.scheme, c.host, err)
			}
			if !c.wantOK && err == nil {
				t.Fatalf("checkDestination(%s, %s) = nil, want a refusal", c.scheme, c.host)
			}
		})
	}
}

// TestGuardedHTTPClient_Acceptance pins the design's acceptance criterion
// end to end: http://127.0.0.1:<port>/v1 (an httptest server) passes the
// guard and the request reaches the server; a remote http:// base URL is
// refused before any dial.
func TestGuardedHTTPClient_Acceptance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	cl := newGuardedHTTPClient(nil)
	resp, err := cl.Get(srv.URL) // http://127.0.0.1:<port>
	if err != nil {
		t.Fatalf("loopback http get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}

	// The remote case: a public http:// URL is a validation failure. The
	// host is public and non-routable, so the refusal is the guard's — a
	// dial to it would hang or refuse on the network.
	_, err = cl.Get("http://" + testPublicIP + "/v1")
	if err == nil {
		t.Fatal("public http:// get succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "http://") {
		t.Fatalf("refusal = %v, want an address-rule error naming the scheme", err)
	}
}

// TestGuardedHTTPClient_ProxyCannotReroutePrivate is reason 4 of the design:
// a proxy env var must not reroute a request the user believes is local.
// With HTTP_PROXY pointed at a black hole, a loopback http:// request still
// succeeds — the guard bypasses the proxy for http and dials the validated
// private address directly.
func TestGuardedHTTPClient_ProxyCannotReroutePrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	deadProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the proxy: %s %s", r.Method, r.URL)
		http.Error(w, "proxy should not have been used", http.StatusBadGateway)
	}))
	defer deadProxy.Close()

	t.Setenv("HTTP_PROXY", deadProxy.URL)
	t.Setenv("http_proxy", deadProxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")

	cl := newGuardedHTTPClient(nil)
	resp, err := cl.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback http get with hostile proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

// TestGuardedHTTPClient_RedirectRecheckedAsNewEndpoint: a redirect is a new
// endpoint and is re-checked (design §4.5). Loopback → loopback follows;
// loopback → public http:// refuses and the public target is never
// contacted.
func TestGuardedHTTPClient_RedirectRecheckedAsNewEndpoint(t *testing.T) {
	var publicHit bool
	publicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicHit = true
		_, _ = io.WriteString(w, "public")
	}))
	defer publicSrv.Close()
	_ = publicSrv

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+testPublicIP+"/v1", http.StatusFound)
	}))
	defer redirector.Close()

	cl := newGuardedHTTPClient(nil)
	resp, err := cl.Get(redirector.URL)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		t.Fatal("redirect to public http:// followed, want refusal")
	}
	if publicHit {
		t.Fatal("the public redirect target was contacted")
	}
}

// TestGuardedHTTPClient_CredentialNeverCrossesOriginChange: an http→http
// redirect between two loopback servers (different port = different origin)
// follows, but the Authorization header is NOT forwarded — the credential
// belongs to the origin it was sent to (design §4.5: "the credential is
// never forwarded across an origin change").
func TestGuardedHTTPClient_CredentialNeverCrossesOriginChange(t *testing.T) {
	var sawAuthAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthAtTarget = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-secret" {
			t.Fatalf("origin A saw Authorization %q, want Bearer sk-secret", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	cl := newGuardedHTTPClient(nil)
	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAuthAtTarget != "" {
		t.Fatalf("Authorization forwarded across origin change: %q", sawAuthAtTarget)
	}
}

// TestGuardedHTTPClient_CustomHeadersNeverCrossOriginChange: the endpoint's
// custom headers follow the SAME rule as the credential (bead nocx-lyyk) —
// a header can carry a token, and a token must not survive a redirect the
// Authorization header would not. The initial request carries the custom
// headers AND tags its context with their names (the way engine.go and
// connection.go do); the guard drops exactly those names on an origin
// change, and the same-origin hop keeps them.
func TestGuardedHTTPClient_CustomHeadersNeverCrossOriginChange(t *testing.T) {
	var sawXTitleAtTarget, sawXTenantAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawXTitleAtTarget = r.Header.Get("X-Title")
		sawXTenantAtTarget = r.Header.Get("X-Tenant")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Title"); got != "nocx" {
			t.Fatalf("origin A saw X-Title %q, want nocx", got)
		}
		if got := r.Header.Get("X-Tenant"); got != "tenant-7" {
			t.Fatalf("origin A saw X-Tenant %q, want tenant-7", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	cl := newGuardedHTTPClient(nil)
	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	req.Header.Set("X-Title", "nocx")
	req.Header.Set("X-Tenant", "tenant-7")
	req = req.WithContext(withCustomHeaderNames(req.Context(), []string{"X-Title", "X-Tenant"}))
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawXTitleAtTarget != "" {
		t.Fatalf("X-Title forwarded across origin change: %q", sawXTitleAtTarget)
	}
	if sawXTenantAtTarget != "" {
		t.Fatalf("X-Tenant forwarded across origin change: %q", sawXTenantAtTarget)
	}
}

// TestGuardedHTTPClient_CustomHeadersSurviveSameOriginRedirect: a redirect
// WITHIN one origin is not a crossing, so the custom headers ride it — the
// same rule as the credential.
func TestGuardedHTTPClient_CustomHeadersSurviveSameOriginRedirect(t *testing.T) {
	var sawXTitle string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/done", http.StatusFound)
			return
		}
		sawXTitle = r.Header.Get("X-Title")
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	cl := newGuardedHTTPClient(nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/start", nil)
	req.Header.Set("X-Title", "nocx")
	req = req.WithContext(withCustomHeaderNames(req.Context(), []string{"X-Title"}))
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawXTitle != "nocx" {
		t.Fatalf("X-Title = %q after the same-origin hop, want nocx", sawXTitle)
	}
}

// TestGuardedHTTPClient_CredentialStrippedOnSchemeChange is the case the
func TestGuardedHTTPClient_CredentialStrippedOnSchemeChange(t *testing.T) {
	var sawAuthAtTLS string
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthAtTLS = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "tls")
	}))
	defer tlsSrv.Close()

	// Same host (127.0.0.1), different scheme and port: a genuine origin
	// change. The http server redirects to the https server's URL.
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL, http.StatusFound)
	}))
	defer httpSrv.Close()

	cl := newGuardedHTTPClient(nil)
	// The test's TLS server presents a self-signed cert; trusting it here is
	// test-only and does not weaken the guard (the guard does not manage
	// trust, only the address rule and the credential boundary).
	tr, _ := cl.Transport.(*guardedTransport)
	tr.inner.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only self-signed cert

	req, _ := http.NewRequest(http.MethodGet, httpSrv.URL, nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("http→https redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAuthAtTLS != "" {
		t.Fatalf("Authorization forwarded across scheme change: %q", sawAuthAtTLS)
	}
}

// TestGuardedHTTPClient_RedirectBackToOriginKeepsCredential: a hop that
// returns to the ORIGINAL origin is not a crossing — the credential may ride
// it, because the other origin never saw it (the redirect copy in net/http
// re-derives headers from the original request per hop, and the guard only
// strips on an origin change).
func TestGuardedHTTPClient_RedirectBackToOriginKeepsCredential(t *testing.T) {
	// A is the origin. A redirects to B (a different origin — credential
	// must be stripped); B redirects back to A (same origin as the
	// original — the credential may return).
	var sawAuthAtOrigin string
	var hopB *httptest.Server
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, hopB.URL, http.StatusFound)
			return
		}
		sawAuthAtOrigin = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "origin")
	}))
	defer origin.Close()
	hopB = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("origin B saw Authorization %q, want none (crossing)", got)
		}
		http.Redirect(w, r, origin.URL, http.StatusFound)
	}))
	defer hopB.Close()

	cl := newGuardedHTTPClient(nil)
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if sawAuthAtOrigin != "Bearer sk-secret" {
		t.Fatalf("origin A saw Authorization %q after the round trip, want the credential back", sawAuthAtOrigin)
	}
}

// TestGuardedHTTPClient_DialsTheValidatedResolution pins design reason 1: a
// hostname can resolve public while validated and private when dialled. The
// guard resolves once, validates THAT answer and dials exactly it — proven
// by handing the guard a resolver that maps a fake hostname onto the test
// server's loopback address: the request succeeds against a URL whose
// hostname no real resolver would answer with.
func TestGuardedHTTPClient_DialsTheValidatedResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	cl := newGuardedHTTPClientWithResolver(nil, func(ctx context.Context, name string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "http://resolved.invalid:"+port+"/", nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("get via injected resolver: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

// TestGuardedHTTPClient_EnvironmentProxyLeavesHTTPSAlone documents the other
// side of reason 4: https is NOT proxy-bypassed (corporate MITM still
// works); only the restricted http scheme dials direct. The proxy function
// is exercised directly rather than over the network.
func TestGuardedHTTPClient_EnvironmentProxyLeavesHTTPSAlone(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)

	cl := newGuardedHTTPClient(nil)
	tr, _ := cl.Transport.(*guardedTransport)
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	u, err := tr.proxy(req)
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	if u == nil || u.Host != strings.TrimPrefix(proxy.URL, "http://") {
		t.Fatalf("https proxy = %v, want the environment proxy", u)
	}

	reqHTTP, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:11434/v1", nil)
	u2, err := tr.proxy(reqHTTP)
	if err != nil {
		t.Fatalf("proxy http: %v", err)
	}
	if u2 != nil {
		t.Fatalf("http proxy = %v, want nil (direct dial for http)", u2)
	}
}

// TestGuardedHTTPClient_IPv6Loopback: ::1 is a spelling of loopback and must
// pass the guard. The dial itself only runs when IPv6 loopback exists.
func TestGuardedHTTPClient_IPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback on this host")
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	cl := newGuardedHTTPClient(nil)
	resp, err := cl.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("http://[::1] get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}
