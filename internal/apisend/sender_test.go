package apisend

// What a user can now do: type a request, press Send, and see what came
// back — status, headers, the decoded body and how long each phase took —
// with the same http:// address rule the assistant obeys applied to the
// connection rather than to the form.

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

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/httppolicy"
)

// testPublicIP is a public, non-routable address (TEST-NET-3): public per
// the rule, so http:// to it must be refused, and nothing ever dials it.
const testPublicIP = "203.0.113.7"

// TestSend_ReturnsStatusHeadersBodyAndTimings is the acceptance criterion.
func TestSend_ReturnsStatusHeadersBodyAndTimings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace", "abc")
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
	got := answered(t, ex, err)
	if got.Status != http.StatusTeapot {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusTeapot)
	}
	if got.Text != `{"ok":true}` {
		t.Errorf("Text = %q, want the decoded body", got.Text)
	}
	if header(got, "X-Trace") != "abc" {
		t.Errorf("X-Trace = %q, want abc", header(got, "X-Trace"))
	}
	if n := headerCount(got, "X-Multi"); n != 2 {
		t.Errorf("X-Multi appeared %d times, want 2 — a repeated header is two rows", n)
	}
	if ex.Timings.Total <= 0 {
		t.Error("Timings.Total = 0, want the elapsed time of the exchange")
	}
	if ex.Timings.TTFB <= 0 {
		t.Error("Timings.TTFB = 0, want the time to the first response byte")
	}
	if ex.Timings.Connect <= 0 {
		t.Error("Timings.Connect = 0, want the dial the route performed")
	}
	if ex.RemoteAddr != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("RemoteAddr = %q, want %q", ex.RemoteAddr, strings.TrimPrefix(srv.URL, "http://"))
	}
	if got.TLSVersion != "" {
		t.Errorf("TLSVersion = %q over http, want empty", got.TLSVersion)
	}
}

// TestSend_OverTLSReportsTheVersion is the https half: the same send, and
// the connection facts a diagnostic needs.
func TestSend_OverTLSReportsTheVersion(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	ex, err := newTrusting(trust(srv)).Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
	got := answered(t, ex, err)
	if got.Text != "secure" {
		t.Errorf("Text = %q, want secure", got.Text)
	}
	if !strings.HasPrefix(got.TLSVersion, "TLS 1.") {
		t.Errorf("TLSVersion = %q, want a TLS 1.x version", got.TLSVersion)
	}
	if ex.Timings.TLS <= 0 {
		t.Error("Timings.TLS = 0, want the handshake time")
	}
}

// trust is a TLS config that trusts one httptest server's own certificate
// and nothing else. It is not a way to skip verification.
func trust(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// TestSend_PublicHTTPRefusedOnTheConnection: the rule is applied to the
// resolved address at connect time, and the socket is never opened.
func TestSend_PublicHTTPRefusedOnTheConnection(t *testing.T) {
	route := &fakeRoute{
		resolve: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP(testPublicIP)}, nil
		},
		dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			t.Errorf("dialled %q — the refusal must precede the socket", addr)
			return nil, errors.New("must not dial")
		},
	}
	s := New(WithRoutes(fixedRoute(route)))

	ex, err := s.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: "http://api.example.com/v1"}, Key{})
	// PHASE `dial`: the name resolved and the rule refused the address it
	// resolved to, so the attempt stopped exactly where a socket would have
	// been opened — and nothing was.
	fail := failedAt(t, ex, err, PhaseDial)
	if !strings.Contains(fail.Reason, "http://") {
		t.Fatalf("reason = %q, want an address-rule refusal naming the scheme", fail.Reason)
	}
}

// TestSend_LoopbackHTTPAllowed is the pair: the same scheme to an address
// the rule permits.
func TestSend_LoopbackHTTPAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
	got := answered(t, ex, err)
	if got.Text != "ok" {
		t.Fatalf("Text = %q, want ok", got.Text)
	}
}

// TestSend_EveryRedirectHopIsChecked: the rule is not a form check, so a
// loopback endpoint that redirects to a public http:// one is refused at the
// hop and the public target is never contacted.
func TestSend_EveryRedirectHopIsChecked(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+testPublicIP+"/v1", http.StatusFound)
	}))
	defer redirector.Close()

	var dialed []string
	var mu sync.Mutex
	route := &fakeRoute{
		resolve: httppolicy.SystemResolver().LookupIP,
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	ex, err := New(WithRoutes(fixedRoute(route))).Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: redirector.URL}, Key{})
	// The PHASE is not asserted here and that is deliberate: the first hop
	// landed, so this is a live exchange that was refused part way through,
	// and which of "dial" and "exchange" describes a refused second hop is
	// a judgement the run does not need. What it needs is that it failed
	// and that the public target was never dialled — both below.
	failed(t, ex, err)
	mu.Lock()
	defer mu.Unlock()
	for _, addr := range dialed {
		if strings.HasPrefix(addr, testPublicIP) {
			t.Fatalf("dialled the public redirect target %q", addr)
		}
	}
}

// TestSend_AuthorizationDroppedOnOriginChange: the credential belongs to the
// origin it was sent to. Its pair — kept within one origin — is
// TestSend_AuthorizationSurvivesSameOriginRedirect.
func TestSend_AuthorizationDroppedOnOriginChange(t *testing.T) {
	var sawAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAtTarget = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-secret" {
			t.Errorf("origin A saw Authorization %q, want the credential", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := New().Send(context.Background(), apicoll.Request{
		Method:  http.MethodGet,
		URL:     redirector.URL,
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer sk-secret", Enabled: true}},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sawAtTarget != "" {
		t.Fatalf("Authorization forwarded across an origin change: %q", sawAtTarget)
	}
}

func TestSend_AuthorizationSurvivesSameOriginRedirect(t *testing.T) {
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

	_, err := New().Send(context.Background(), apicoll.Request{
		Method:  http.MethodGet,
		URL:     srv.URL + "/start",
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer sk-secret", Enabled: true}},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sawAtEnd != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q after a same-origin hop, want it kept", sawAtEnd)
	}
}

// TestSend_UserHeadersFollowTheCredentialRule: a header value can BE the
// credential, so the names the user set are dropped on a crossing too.
func TestSend_UserHeadersFollowTheCredentialRule(t *testing.T) {
	var sawAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAtTarget = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, "landed")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := New().Send(context.Background(), apicoll.Request{
		Method:  http.MethodGet,
		URL:     redirector.URL,
		Headers: []apicoll.Header{{Name: "X-Api-Key", Value: "k", Enabled: true}},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sawAtTarget != "" {
		t.Fatalf("X-Api-Key forwarded across an origin change: %q", sawAtTarget)
	}
}

// TestSend_ProjectsTheModelOntoTheWire: method, query order, disabled rows,
// headers and the two body kinds the sender can send by itself.
func TestSend_ProjectsTheModelOntoTheWire(t *testing.T) {
	var gotMethod, gotQuery, gotBody, gotContentType, gotHeader, gotDisabled string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		gotHeader = r.Header.Get("X-Kept")
		gotDisabled = r.Header.Get("X-Dropped")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	_, err := New().Send(context.Background(), apicoll.Request{
		Method: http.MethodPost,
		URL:    srv.URL + "/x?fixed=1",
		Query: []apicoll.Param{
			{Name: "z", Value: "last", Enabled: true},
			{Name: "off", Value: "no", Enabled: false},
			{Name: "a", Value: "first & second", Enabled: true},
		},
		Headers: []apicoll.Header{
			{Name: "X-Kept", Value: "yes", Enabled: true},
			{Name: "X-Dropped", Value: "no", Enabled: false},
		},
		Body: apicoll.Body{Kind: apicoll.BodyForm, Text: "a=1&b=2"},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	// The user's order, not the alphabet: a request whose parameters come
	// back reordered is not the request they wrote.
	if want := "fixed=1&z=last&a=first+%26+second"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if gotHeader != "yes" || gotDisabled != "" {
		t.Errorf("headers: kept = %q, dropped = %q — a disabled row is not sent", gotHeader, gotDisabled)
	}
	if gotBody != "a=1&b=2" {
		t.Errorf("body = %q, want the form text", gotBody)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want the one the body KIND declares", gotContentType)
	}
}

// TestSend_RawBodyCarriesTheUsersContentType: a raw body declares nothing,
// so the sender adds nothing — guessing would send a header the user did
// not write.
func TestSend_RawBodyCarriesTheUsersContentType(t *testing.T) {
	var gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()

	_, err := New().Send(context.Background(), apicoll.Request{
		Method:  http.MethodPut,
		URL:     srv.URL,
		Headers: []apicoll.Header{{Name: "Content-Type", Value: "application/json", Enabled: true}},
		Body:    apicoll.Body{Kind: apicoll.BodyRaw, Text: `{"a":1}`},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotType != "application/json" || gotBody != `{"a":1}` {
		t.Errorf("content-type = %q, body = %q", gotType, gotBody)
	}
}

// TestSend_RefusesWhatItCannotResolve: this package knows nothing about
// files and nothing about the vault. Both refusals are loud, because a
// silent one would send a request the user believes is something else.
func TestSend_RefusesWhatItCannotResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the request reached the network, want a refusal before it")
	}))
	defer srv.Close()

	cases := []struct {
		name string
		req  apicoll.Request
		want error
	}{
		{
			"a body that names a file",
			apicoll.Request{Method: http.MethodPost, URL: srv.URL, Body: apicoll.Body{Kind: apicoll.BodyFile, FileRef: "payload.json"}},
			ErrFileBody,
		},
		{
			"auth that was never applied",
			apicoll.Request{Method: http.MethodGet, URL: srv.URL, Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Token: "t0k3n"}},
			ErrAuthUnresolved,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New().Send(context.Background(), c.req, Key{})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestSend_RefusesAMalformedRequest: the errors a user can produce by
// typing — and they are RUNS, at phase `compose`, not errors.
//
// The division is the one apisend.Send states: an error is a violation of
// this package's calling contract, which the CALLER can fix without asking
// the user anything (resolve the file, resolve the variable). An address
// that will not parse is not that. A person typed it, pressed Send, and the
// answer belongs on a row beside every other thing they have sent — with
// the phase saying which step never happened.
func TestSend_RefusesAMalformedRequest(t *testing.T) {
	cases := []struct {
		name  string
		req   apicoll.Request
		phase Phase
		want  string
	}{
		{"no URL", apicoll.Request{Method: http.MethodGet}, PhaseCompose, "not an absolute URL"},
		{"a bare path", apicoll.Request{Method: http.MethodGet, URL: "/users"}, PhaseCompose, "not an absolute URL"},
		// An ftp:// URL builds a perfectly good *http.Request; it is the
		// address rule that refuses it, at the step where a socket would
		// have been opened. So this one composes — and its run shows the
		// request line, which is exactly the point of keeping it.
		{"an unsupported scheme", apicoll.Request{Method: http.MethodGet, URL: "ftp://example.com/x"}, PhaseDial, "unsupported URL scheme"},
		{"an unknown body kind", apicoll.Request{Method: http.MethodGet, URL: "http://127.0.0.1:1/x", Body: apicoll.Body{Kind: "graphql"}}, PhaseCompose, "unknown body kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ex, err := New().Send(context.Background(), c.req, Key{})
			fail := failedAt(t, ex, err, c.phase)
			if !strings.Contains(fail.Reason, c.want) {
				t.Fatalf("reason = %q, want it to contain %q", fail.Reason, c.want)
			}
			// EVEN AT COMPOSE THE RUN SHOWS WHAT WAS ASKED FOR. There is
			// no *http.Request to render, so the text is the request as
			// WRITTEN (composeUnsent) — which is the thing a person has to
			// look at to see the address they mistyped.
			if !strings.Contains(ex.Request.Text, c.req.Method) {
				t.Errorf("the run does not show the method:\n%s", ex.Request.Text)
			}
			// The WHOLE address, but only where composeUnsent wrote it. The
			// ftp:// case built a perfectly good *http.Request and was
			// refused at the dial, so its text is the real composition —
			// request-URI plus a Host line, which is what actually would
			// have crossed.
			if c.phase == PhaseCompose && c.req.URL != "" && !strings.Contains(ex.Request.Text, c.req.URL) {
				t.Errorf("the run does not show the address that was refused:\n%s", ex.Request.Text)
			}
			if c.phase != PhaseCompose && !strings.Contains(ex.Request.Text, "Host: ") {
				t.Errorf("a composed run carries no Host line:\n%s", ex.Request.Text)
			}
			if ex.Request.Spans == nil {
				t.Error("Request.Spans is nil; a side with nothing to mark is []")
			}
		})
	}
}

// TestSend_ErrorsDoNotCarryTheQueryString: a token rides a URL often enough
// that an error message is a place one leaks, and an error is written where
// the user did not choose to put it.
func TestSend_ErrorsDoNotCarryTheQueryString(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing is listening now: the dial will be refused

	ex, err := New().Send(context.Background(), apicoll.Request{
		Method: http.MethodGet,
		URL:    "http://" + addr + "/v1?access_token=sk-secret",
	}, Key{})
	fail := failedAt(t, ex, err, PhaseDial)
	if strings.Contains(fail.Reason, "sk-secret") {
		t.Fatalf("the failure reason carries the credential: %v", fail.Reason)
	}
	// REDACTION IS UNCHANGED BY THE NEW SHAPE, and this is where that is
	// checked: the reason is the same redacted sentence the error used to
	// be, now on a run. The request block beside it carries the query
	// string, and legitimately — it is the request the person wrote, on
	// their own screen, with any SECRET in it elided by its spans (§11.2).
	// The one place a credential must never appear is a message written
	// somewhere the user did not choose, which is what this reason is.
}

// fakeRoute is a route whose resolve and dial are the test's.
type fakeRoute struct {
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (r *fakeRoute) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return r.resolve(ctx, host)
}

func (r *fakeRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return r.dial(ctx, network, addr)
}

func (r *fakeRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

// fixedRoute is a route table with one entry, for every id.
func fixedRoute(r Route) Routes {
	return func(context.Context, string) (Route, error) { return r, nil }
}

func header(r Response, name string) string {
	for _, h := range r.Headers {
		if h.Name == name {
			return h.Value
		}
	}
	return ""
}

func headerCount(r Response, name string) int {
	n := 0
	for _, h := range r.Headers {
		if h.Name == name {
			n++
		}
	}
	return n
}

// apicollGet is the shortest request a test can send.
func apicollGet(u string) apicoll.Request {
	return apicoll.Request{Method: http.MethodGet, URL: u}
}

// TestSend_ContentTypeSurvivesA307AcrossOrigins: a 307 carries the body, and
// a header describing the payload is not a credential for the origin. The
// user's other headers still go, so this pins the exception rather than
// weakening the rule.
func TestSend_ContentTypeSurvivesA307AcrossOrigins(t *testing.T) {
	var gotType, gotKey, gotBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotKey = r.Header.Get("X-Api-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := New().Send(context.Background(), apicoll.Request{
		Method: http.MethodPost,
		URL:    redirector.URL,
		Headers: []apicoll.Header{
			{Name: "Content-Type", Value: "application/json", Enabled: true},
			{Name: "X-Api-Key", Value: "k", Enabled: true},
		},
		Body: apicoll.Body{Kind: apicoll.BodyRaw, Text: `{"a":1}`},
	}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody != `{"a":1}` {
		t.Errorf("body = %q after the 307, want it replayed", gotBody)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q after the 307, want it kept — it describes the payload", gotType)
	}
	if gotKey != "" {
		t.Errorf("X-Api-Key = %q across an origin change, want it dropped", gotKey)
	}
}
