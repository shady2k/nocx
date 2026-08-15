package assistant

// The probe's paired tests, per AGENTS.md rule 2's second example: for every
// "returns an error when…" there is a paired "and on a normal machine it
// succeeds". The fake server speaks the OpenAI chat-completions SSE protocol
// the adapter actually consumes, so these are integration tests of the real
// eino path, not of a stub.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
)

// fakeOpenAIServer is a minimal OpenAI-compatible /chat/completions SSE
// server. It records the last request (body, Authorization, path and the
// custom headers a test names) so a test can assert what the adapter
// actually sent.
type fakeOpenAIServer struct {
	handler  func(w http.ResponseWriter, r *http.Request)
	lastBody atomic.Value // string
	lastAuth atomic.Value // string
	lastPath atomic.Value // string
	lastHdrs atomic.Value // map[string]string
}

func newFakeOpenAI(handler func(w http.ResponseWriter, r *http.Request)) (*fakeOpenAIServer, *httptest.Server) {
	f := &fakeOpenAIServer{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.lastBody.Store(string(body))
		f.lastAuth.Store(r.Header.Get("Authorization"))
		f.lastPath.Store(r.URL.Path)
		hdrs := make(map[string]string)
		for name, values := range r.Header {
			if len(values) > 0 {
				hdrs[name] = values[0]
			}
		}
		f.lastHdrs.Store(hdrs)
		if f.handler != nil {
			f.handler(w, r)
			return
		}
		streamOK(w)
	}))
	return f, srv
}

// streamOK writes one streamed "ok" in two chunks, the way a real streaming
// completion arrives.
func streamOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON("o", ""))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON("k", "stop"))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func chunkJSON(content, finish string) string {
	d := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "probe-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": finish,
		}},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

func (f *fakeOpenAIServer) body() string {
	s, _ := f.lastBody.Load().(string)
	return s
}

func (f *fakeOpenAIServer) auth() string {
	s, _ := f.lastAuth.Load().(string)
	return s
}

func (f *fakeOpenAIServer) path() string {
	s, _ := f.lastPath.Load().(string)
	return s
}

func (f *fakeOpenAIServer) header(name string) string {
	hdrs, _ := f.lastHdrs.Load().(map[string]string)
	// Go canonicalizes header names on the wire (HTTP-Referer → Http-Referer);
	// the accessor canonicalizes too so callers can ask for the name they
	// configured.
	return hdrs[http.CanonicalHeaderKey(name)]
}

func testProbeParams(baseURL string) ProbeParams {
	return ProbeParams{
		Name:    "Local",
		BaseURL: baseURL,
		Key:     credential.NewSecret("sk-test-123"),
		Model:   "probe-model",
	}
}

// TestProbe_SucceedsEndToEnd is the paired positive: on a normal machine
// (here: a normal loopback server) an ask succeeds end to end. It also
// asserts the explain-mode wire facts: the request carries the key as the
// Bearer credential and declares NO tools.
func TestProbe_SucceedsEndToEnd(t *testing.T) {
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.OK {
		t.Fatalf("Probe = %+v, want OK", res)
	}
	if res.Model != "probe-model" {
		t.Fatalf("Model = %q, want probe-model", res.Model)
	}
	if res.Error != "" {
		t.Fatalf("Error = %q, want empty", res.Error)
	}
	if res.ElapsedMS < 0 {
		t.Fatalf("ElapsedMS = %d, want >= 0", res.ElapsedMS)
	}
	if res.At.IsZero() {
		t.Fatal("At is zero, want a wall-clock timestamp")
	}

	// The wire facts of explain mode (design §4.2 acceptance: "Assert on
	// the request the adapter actually sends: its tool list contains only
	// what the grant permits" — here the list is empty).
	body := f.body()
	if strings.Contains(body, `"tools"`) {
		t.Fatalf("request declares tools: %s", body)
	}
	if f.auth() != "Bearer sk-test-123" {
		t.Fatalf("Authorization = %q, want Bearer sk-test-123", f.auth())
	}
	if !strings.HasSuffix(f.path(), "/chat/completions") {
		t.Fatalf("path = %q, want .../chat/completions", f.path())
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request body %q: %v", body, err)
	}
	if req.Model != "probe-model" {
		t.Fatalf("request model = %q, want probe-model", req.Model)
	}
}

// TestProbe_SendsCustomHeadersOnCompletion (bead nocx-lyyk, acceptance 1):
// the completion carries the endpoint's custom headers — the OpenRouter
// HTTP-Referer/X-Title case, verbatim, over the real eino path.
func TestProbe_SendsCustomHeadersOnCompletion(t *testing.T) {
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	p := testProbeParams(srv.URL)
	p.Headers = []Header{
		{Name: "HTTP-Referer", Value: "https://nocx.dev"},
		{Name: "X-Title", Value: "nocx"},
	}
	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.OK {
		t.Fatalf("Probe = %+v, want OK", res)
	}
	if got := f.header("HTTP-Referer"); got != "https://nocx.dev" {
		t.Errorf("completion carried HTTP-Referer %q, want https://nocx.dev", got)
	}
	if got := f.header("X-Title"); got != "nocx" {
		t.Errorf("completion carried X-Title %q, want nocx", got)
	}
}

// TestProbe_ConnectionCheckSendsCustomHeaders (bead nocx-lyyk, acceptance 1):
// the no-model connection check sends the SAME headers on GET /models — a
// Test that passes must mean the real calls will.
func TestProbe_ConnectionCheckSendsCustomHeaders(t *testing.T) {
	var sawXTenant, sawReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("connection check hit %s, want /models", r.URL.Path)
		}
		sawXTenant = r.Header.Get("X-Tenant")
		sawReferer = r.Header.Get("HTTP-Referer")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	p := testProbeParams(srv.URL)
	p.Model = ""
	p.Headers = []Header{
		{Name: "X-Tenant", Value: "tenant-7"},
		{Name: "HTTP-Referer", Value: "https://nocx.dev"},
	}
	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Kind != ProbeConnection || !res.OK {
		t.Fatalf("Probe = %+v, want a successful connection check", res)
	}
	if sawXTenant != "tenant-7" {
		t.Errorf("connection check carried X-Tenant %q, want tenant-7", sawXTenant)
	}
	if sawReferer != "https://nocx.dev" {
		t.Errorf("connection check carried HTTP-Referer %q, want https://nocx.dev", sawReferer)
	}
}

// TestProbe_DialFailure is the mechanical failure path: an unreachable
// address is a probe outcome (OK=false with the dial error), not a Go error.
func TestProbe_DialFailure(t *testing.T) {
	cl := NewClient(nil)
	p := testProbeParams("http://127.0.0.1:1/v1")
	p.BaseURL = "http://127.0.0.1:1/v1" // nothing listens on port 1
	res, err := cl.Probe(context.Background(), p)
	if err != nil {
		t.Fatalf("Probe returned a Go error %v, want a probe outcome", err)
	}
	if res.OK {
		t.Fatalf("Probe = %+v, want !OK for an unreachable host", res)
	}
	if res.Error == "" {
		t.Fatal("Error is empty, want the dial failure")
	}
}

// TestProbe_HTTPError: a refused stream (401) is a probe outcome.
func TestProbe_HTTPError(t *testing.T) {
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	defer srv.Close()

	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe returned a Go error %v, want a probe outcome", err)
	}
	if res.OK {
		t.Fatalf("Probe = %+v, want !OK for a 401", res)
	}
	if !strings.Contains(res.Error, "401") && !strings.Contains(res.Error, "Incorrect API key") {
		t.Fatalf("Error = %q, want the HTTP error", res.Error)
	}
}

// TestProbe_ZeroContent: a stream that completes with no text is a probe
// outcome, not a success — the endpoint answered, but it did not answer.
func TestProbe_ZeroContent(t *testing.T) {
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe returned a Go error %v, want a probe outcome", err)
	}
	if res.OK {
		t.Fatalf("Probe = %+v, want !OK for an empty stream", res)
	}
	if !strings.Contains(res.Error, "no text") {
		t.Fatalf("Error = %q, want the zero-content explanation", res.Error)
	}
}

// TestProbe_StreamingDeliversAnAnswer: the probe's OK result means a real
// streamed answer was received; the same path the ask transaction will use.
func TestProbe_StreamingDeliversAnAnswer(t *testing.T) {
	_, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.OK {
		t.Fatalf("Probe = %+v, want OK", res)
	}
}

// TestProbe_InvalidParams: a probe that cannot run is a Go error, and no
// result is produced.
func TestProbe_InvalidParams(t *testing.T) {
	cl := NewClient(nil)
	p := testProbeParams("http://127.0.0.1:1/v1")
	p.BaseURL = ""
	if _, err := cl.Probe(context.Background(), p); err == nil {
		t.Fatal("Probe with an empty base URL succeeded, want a refusal")
	}
	// An empty model is NOT a refusal any more (nocx-q27y): it is the other
	// question — "can I reach this API with this key" — which needs no
	// model. It must run, and it must say which check it ran, so a caller
	// can never read a connection result as a model result.
	p = testProbeParams("http://127.0.0.1:1/v1")
	p.Model = ""
	res, err := cl.Probe(context.Background(), p)
	if err != nil {
		t.Fatalf("Probe with no model returned a Go error, want a connection check: %v", err)
	}
	if res.Kind != ProbeConnection {
		t.Fatalf("Kind = %q, want %q", res.Kind, ProbeConnection)
	}
	if res.OK {
		t.Fatal("port 1 is not listening; want OK=false with the dial error")
	}
}

// TestProbe_ContextCancelled: a cancelled context is a probe outcome.
func TestProbe_ContextCancelled(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		<-block // hold the response open until the test cancels
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cl := NewClient(nil)
	res, err := cl.Probe(ctx, testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe returned a Go error %v, want a probe outcome", err)
	}
	if res.OK {
		t.Fatalf("Probe = %+v, want !OK for a cancelled context", res)
	}
}

// TestProbe_RedirectToPublicRefused: the guarded client applies to the probe
// path — a server that redirects to a public http:// address produces a
// refused probe.
func TestProbe_RedirectToPublicRefused(t *testing.T) {
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+testPublicIP+"/v1/chat/completions", http.StatusFound)
	})
	defer srv.Close()

	cl := NewClient(nil)
	res, err := cl.Probe(context.Background(), testProbeParams(srv.URL))
	if err != nil {
		t.Fatalf("Probe returned a Go error %v, want a probe outcome", err)
	}
	if res.OK {
		t.Fatalf("Probe = %+v, want !OK for a refused redirect", res)
	}
}

// TestProbeStore_LastWins: the store keeps exactly the last result, and the
// copy returned cannot be mutated through the store.
func TestProbeStore_LastWins(t *testing.T) {
	s := NewProbeStore()
	if s.Last() != nil {
		t.Fatal("Last() on an empty store = non-nil, want nil")
	}
	s.Record(ProbeResult{EndpointName: "first", OK: true, At: time.Now()})
	s.Record(ProbeResult{EndpointName: "second", OK: false, Error: "boom", At: time.Now()})
	got := s.Last()
	if got == nil || got.EndpointName != "second" || got.OK {
		t.Fatalf("Last() = %+v, want the second record", got)
	}
	got.Error = "mutated"
	if s.Last().Error != "boom" {
		t.Fatal("mutating the returned copy changed the store")
	}
}
