// Package apisend sends one API-testing request and returns what came back,
// bounded. It is the executor of design §7: ONE HTTP client implementation
// whose route is supplied — net.Dialer locally, a lease on the SSH pool
// through a connection — obeying the shared http:// address rule in
// internal/httppolicy rather than a second copy of it.
//
// It knows nothing about files, nothing about the vault and nothing about
// the collection folder. A body that names a file and an auth that names a
// variable are both resolved by the caller, before the request reaches here.
package apisend

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/httppolicy"
)

// Sender sends one request through the route named by k and returns the
// response as a value.
type Sender interface {
	Send(ctx context.Context, r apicoll.Request, k Key) (Response, error)
}

// Response is what the run shows. Text, Binary, Lossy and Truncated are four
// separate facts because they are four separate sentences in the UI, and
// collapsing any two of them loses one.
type Response struct {
	Status  int
	Headers []apicoll.Header
	// Text is the decoded body, always valid UTF-8, and EMPTY when Binary:
	// the run says "binary body, N bytes" and never base64.
	Text string
	// Binary is a NUL byte among the bytes actually read — the files.read
	// heuristic, labelled as one.
	Binary bool
	// Lossy is true when the bytes read were not valid UTF-8 and invalid
	// sequences were replaced. Distinct from Binary: a NUL-free latin-1
	// body is lossy text, not a binary body.
	Lossy bool
	// Truncated is true iff the ceiling was reached and one further byte was
	// readable — the body shown is a prefix.
	Truncated bool
	// Size is the number of body bytes actually read and kept, which is the
	// ceiling when Truncated. It is not a claim about what the server holds:
	// a Content-Length can lie and a chunked response declares nothing.
	Size       int64
	Timings    Timings
	TLSVersion string
	RemoteAddr string
}

// Timings are the phases of one exchange. On a redirect chain DNS, Connect
// and TLS describe the LAST hop's connection — the one the body came from —
// TTFB is measured from the start of the exchange to the first byte of the
// response that was returned, and Total adds the body read. A reused
// connection has a zero DNS and Connect, which is the honest answer:
// nothing was resolved and nothing was dialled.
type Timings struct {
	DNS     time.Duration
	Connect time.Duration
	TLS     time.Duration
	TTFB    time.Duration
	Total   time.Duration
}

// ErrFileBody is a body the sender cannot send: resolving a file reference
// means resolving a path inside a collection folder, and a collection folder
// is a hostile path (design §13.1) whose resolution belongs to the package
// that owns the folder. The caller opens the file and hands over the bytes.
var ErrFileBody = errors.New(component + ": a body that names a file must be resolved by the caller; this package knows nothing about files")

// ErrAuthUnresolved is auth the sender cannot apply. A collection file names
// a VARIABLE, never a secret (design §8), and the binding from a variable
// name to a stored value lives in internal/apibind. Sending the variable's
// NAME as though it were the credential would be worse than refusing, and
// sending nothing at all while the user believes they are authenticated
// would be the silent degrade AGENTS.md forbids.
var ErrAuthUnresolved = errors.New(component + ": auth names a variable this package cannot resolve; the caller resolves it and sets the header")

var _ Sender = (*Client)(nil)

// Send performs one request and captures the response.
func (c *Client) Send(ctx context.Context, r apicoll.Request, k Key) (Response, error) {
	req, custom, err := buildRequest(ctx, r)
	if err != nil {
		return Response{}, err
	}
	cl, err := c.instanceFor(ctx, k)
	if err != nil {
		return Response{}, err
	}

	// The custom header names ride the context so the credential rule can
	// drop exactly those names on an origin change, and the tracer rides it
	// so the route can report the phases httptrace cannot see.
	tr := &tracer{}
	ctx = httppolicy.WithCustomHeaderNames(req.Context(), custom)
	ctx = context.WithValue(ctx, traceKey{}, tr)
	req = req.WithContext(httptrace.WithClientTrace(ctx, tr.hooks()))

	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		return Response{}, sendError(req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := captureBody(resp.Body, c.limit)
	if err != nil {
		return Response{}, fmt.Errorf("%s: reading the response body of %s %s: %w",
			component, req.Method, redact(req.URL), err)
	}
	total := time.Since(start)

	out := Response{
		Status:    resp.StatusCode,
		Headers:   responseHeaders(resp.Header),
		Text:      body.Text,
		Binary:    body.Binary,
		Lossy:     body.Lossy,
		Truncated: body.Truncated,
		Size:      body.Size,
		Timings:   tr.timings(total),
	}
	if resp.TLS != nil {
		out.TLSVersion = tls.VersionName(resp.TLS.Version)
	}
	out.RemoteAddr = tr.remote()
	return out, nil
}

// buildRequest projects the model onto an http.Request and reports the
// canonical names of the headers the user set, which the credential rule
// needs per redirect hop.
func buildRequest(ctx context.Context, r apicoll.Request) (*http.Request, []string, error) {
	if r.Auth.Kind != "" && r.Auth.Kind != apicoll.AuthNone {
		return nil, nil, fmt.Errorf("%w (kind %q, variable %q)", ErrAuthUnresolved, r.Auth.Kind, r.Auth.Var)
	}
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: parsing URL %q: %w", component, r.URL, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return nil, nil, fmt.Errorf("%s: %q is not an absolute URL", component, r.URL)
	}
	appendQuery(u, r.Query)

	body, contentType, err := requestBody(r.Body)
	if err != nil {
		return nil, nil, err
	}

	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	// NewRequestWithContext derives ContentLength and GetBody from a
	// *strings.Reader, and GetBody is what lets a 307 replay the body.
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", component, err)
	}

	var custom []string
	for _, h := range r.Headers {
		if !h.Enabled || h.Name == "" {
			continue
		}
		req.Header.Add(h.Name, h.Value)
		// Every header the user typed is treated as potentially
		// credential-bearing, because in this feature it is: the user types
		// the endpoint's headers, and Azure's api-key header IS the key.
		// Content-Type is the one exception, and not by guesswork — it
		// describes the payload rather than the caller, and a 307 that
		// carries the body across an origin needs it to stay describable.
		if name := http.CanonicalHeaderKey(h.Name); name != "Content-Type" {
			custom = append(custom, name)
		}
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, custom, nil
}

// appendQuery appends the enabled parameters IN THE USER'S ORDER. Encoding
// through url.Values would sort them, and a request whose parameters come
// back reordered is a request the user did not write — order is part of what
// they are testing.
func appendQuery(u *url.URL, params []apicoll.Param) {
	var b strings.Builder
	b.WriteString(u.RawQuery)
	for _, p := range params {
		if !p.Enabled || p.Name == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Name))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	u.RawQuery = b.String()
}

// requestBody turns the model's body into a reader and the content type the
// KIND declares. A raw body declares nothing — the user's
// own Content-Type header is the only answer, and guessing one would send a
// header they did not write.
func requestBody(b apicoll.Body) (io.Reader, string, error) {
	switch b.Kind {
	case "", apicoll.BodyNone:
		return nil, "", nil
	case apicoll.BodyRaw:
		return strings.NewReader(b.Text), "", nil
	case apicoll.BodyForm:
		return strings.NewReader(b.Text), "application/x-www-form-urlencoded", nil
	case apicoll.BodyFile:
		return nil, "", fmt.Errorf("%w (fileRef %q)", ErrFileBody, b.FileRef)
	default:
		return nil, "", fmt.Errorf("%s: unknown body kind %q", component, b.Kind)
	}
}

// responseHeaders flattens the response headers into the model's row shape,
// ordered by name so two runs of one request compare. Enabled is true
// because every header that arrived is present; the field carries the
// request-side meaning of a row the user keeps but has switched off.
func responseHeaders(h http.Header) []apicoll.Header {
	out := make([]apicoll.Header, 0, len(h))
	for name, values := range h {
		for _, v := range values {
			out = append(out, apicoll.Header{Name: name, Value: v, Enabled: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// sendError reports a failed exchange without repeating the URL the client
// put in it. net/http wraps every failure in a *url.Error whose message is
// the WHOLE url, query string included, so wrapping that verbatim would
// undo the redaction; the cause is unwrapped and re-wrapped instead, which
// keeps errors.Is reaching the real reason (a net.OpError, a TLS failure, a
// cancelled context) and loses only the layer that leaked.
func sendError(method string, u *url.URL, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return fmt.Errorf("%s: %s %s: %w", component, method, redact(u), err)
}

// redact keeps a failure message from becoming the place a credential leaks:
// userinfo and the query string are where a token most often rides a URL,
// and an error is written to a log the user did not choose.
func redact(u *url.URL) string {
	c := *u
	c.User = nil
	if c.RawQuery != "" {
		c.RawQuery = "…"
	}
	return c.String()
}

// traceKey carries the tracer on the request context.
type traceKey struct{}

// tracer collects the phases of one exchange from two sources: httptrace,
// which sees TLS, the first byte and the connection; and the route wrapper,
// which sees the resolve and the dial that httptrace cannot. Its callbacks
// run on the transport's goroutines, so every field is behind the mutex.
type tracer struct {
	mu         sync.Mutex
	dns        time.Duration
	connect    time.Duration
	tlsTime    time.Duration
	ttfb       time.Duration
	dnsStart   time.Time
	tlsStart   time.Time
	firstStart time.Time
	remoteAddr string
}

// traceFrom returns the tracer on ctx, or a throwaway one when there is
// none — a route may be dialled by something other than a send, and a nil
// tracer would be a panic in the dial path.
func traceFrom(ctx context.Context) *tracer {
	if t, ok := ctx.Value(traceKey{}).(*tracer); ok && t != nil {
		return t
	}
	return &tracer{}
}

func (t *tracer) setDNS(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dns = d
}

func (t *tracer) setConnect(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connect = d
}

func (t *tracer) hooks() *httptrace.ClientTrace {
	t.mu.Lock()
	t.firstStart = time.Now()
	t.mu.Unlock()
	return &httptrace.ClientTrace{
		// The DNS hooks fire only when net.Dialer performs the lookup —
		// which is the https case. For http:// the policy resolves first
		// and dials an address literal, and the route wrapper reports that
		// lookup instead. The two cases are exclusive, so both write the
		// same field and neither overwrites the other.
		DNSStart: func(httptrace.DNSStartInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.dnsStart = time.Now()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.dnsStart.IsZero() {
				t.dns = time.Since(t.dnsStart)
			}
		},
		TLSHandshakeStart: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.tlsStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.tlsStart.IsZero() {
				t.tlsTime = time.Since(t.tlsStart)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if info.Conn != nil && info.Conn.RemoteAddr() != nil {
				t.remoteAddr = info.Conn.RemoteAddr().String()
			}
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.ttfb = time.Since(t.firstStart)
		},
	}
}

func (t *tracer) timings(total time.Duration) Timings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Timings{DNS: t.dns, Connect: t.connect, TLS: t.tlsTime, TTFB: t.ttfb, Total: total}
}

func (t *tracer) remote() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.remoteAddr
}
