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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/httppolicy"
)

// Sender sends one request through the route named by k and returns the
// response as a value.
//
// used names the secrets this request carries — their NAMES and their
// VALUES — and it is variadic because a request with no credential is the
// ordinary case rather than a special one. It is what makes the raw
// diagnostic of §11 possible at all: the values were substituted by the
// caller that resolved them, so this package cannot know which bytes are a
// credential unless it is told. Nothing here stores them, and nothing here
// puts one on the wire to the renderer — see spans.go.
type Sender interface {
	Send(ctx context.Context, r apicoll.Request, k Key, used ...NamedSecret) (Exchange, error)
}

// Outcome is how one exchange ended. Three and not two: a stop is not a
// failure — it is the answer arriving on purpose — and a caller that had to
// infer the difference from a reason string would word and tone somebody's
// own Stop as something that went wrong.
type Outcome string

const (
	// Answered — a response came back and its body was read.
	Answered Outcome = "answered"
	// Failed — it did not.
	Failed Outcome = "failed"
	// Stopped — the person who started it stopped it.
	Stopped Outcome = "stopped"
)

// Phase is WHERE an exchange stopped, as a closed set. It is a POSITION on
// the way to an answer rather than a verdict, which is why a stopped
// exchange carries one too.
type Phase string

const (
	// PhaseCompose — the request could not be built at all. The one phase
	// with no request text, because there was none to compose.
	PhaseCompose Phase = "compose"
	// PhaseResolve — the name did not resolve.
	PhaseResolve Phase = "resolve"
	// PhaseDial — nothing accepted a connection.
	PhaseDial Phase = "dial"
	// PhaseConnection — the ROUTE was unusable: no lease on the SSH
	// profile, its dial bound elapsed, or a name only the far side can
	// resolve. Distinct from PhaseDial because the thing to fix is
	// different — the connection, not the server.
	PhaseConnection Phase = "connection"
	// PhaseTLS — the handshake did not complete.
	PhaseTLS Phase = "tls"
	// PhaseExchange — the connection was open and the exchange broke on it:
	// a truncated body, a malformed response.
	PhaseExchange Phase = "exchange"
	// PhaseTimeout — a bound elapsed.
	PhaseTimeout Phase = "timeout"
	// PhaseStopped — the person pressed Stop.
	PhaseStopped Phase = "stopped"
)

// Failure is how the attempt ended when it did not answer. Present for a
// Failed AND for a Stopped exchange: both are asks about the same thing —
// how far did it get — and only the Outcome says how to read it.
type Failure struct {
	Phase Phase
	// Reason is what went wrong in this package's words, already redacted:
	// sendError unwraps the *url.Error whose message is the whole URL, and
	// redact drops the userinfo and the query string.
	Reason string
}

// Exchange is ONE ATTEMPT TO SEND, and it exists from the moment Send is
// called rather than from the moment an answer arrives.
//
// That is the whole difference from what this package used to return. A run
// that WAS its answer could not exist before the answer did: there was
// nothing to show while a request was in flight, nothing to name a running
// exchange by, and a failure was not a run at all — it came back as an
// error, one sentence, while this function was holding the composed request,
// the placements, the phases and the remote address at the moment it failed
// and dropping every one of them on the floor.
//
// So everything the attempt REACHED is here whatever the outcome, and only
// Response waits for an answer. An error from Send now means one thing and
// one thing only: a violation of this package's calling contract (ErrFileBody,
// ErrAuthUnresolved) — a request it refuses to send rather than a send that
// did not work.
type Exchange struct {
	Outcome Outcome
	// Request is what went out, segmented — PRESENT WHATEVER THE OUTCOME,
	// because it is composed and placed before anything is dialled. Empty
	// only at PhaseCompose, where there was nothing to compose.
	Request Raw
	// RemoteAddr is the address that answered the dial, "" when nothing
	// did. Its presence is also what distinguishes a dial that never landed
	// from a connection that broke later (phaseOf).
	RemoteAddr string
	// Timings are the phases AS FAR AS THE ATTEMPT GOT — one never reached
	// is 0, which is the honest answer rather than an absence.
	Timings Timings
	// Certificates is the chain the server presented, leaf first. Never nil.
	// EMPTY for a failure at PhaseTLS, and that is a real limit: net/http
	// hands the trace an empty connection state when the handshake fails,
	// and recovering the chain from a rejected handshake would mean turning
	// verification off and re-implementing it here — the second X.509
	// implementation Certificate's own doc refuses.
	Certificates []Certificate
	// Response is what came back, and nil unless the outcome is Answered.
	// Nil rather than a zero value: a zeroed response is an HTTP 0 with an
	// empty body, which a reader cannot tell from a real one.
	Response *Response
	// Failure is why it ended, and nil exactly when the outcome is
	// Answered.
	Failure *Failure
}

// Response is what came back. Text, Binary, Lossy and Truncated are four
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
	TLSVersion string
	// TLSCipherSuite is the negotiated suite's name, and "" when the
	// exchange was not over TLS.
	TLSCipherSuite string
	// Raw is the RESPONSE SIDE of §11's segmented text — what came back,
	// with the bounded known-plaintext search run over it.
	//
	// One side, not two. The request side is on the Exchange, because the
	// sender composes it before it dials: a field that only a Response
	// could carry would take the request text away from exactly the runs
	// that need it most, the ones that never got a response at all.
	Raw Raw
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

// Send performs one request and returns the EXCHANGE — what was attempted,
// how far it got, and what came back if anything did.
//
// It returns an error for one thing only: a request this package refuses to
// send at all (ErrFileBody, ErrAuthUnresolved), which is a violation of its
// calling contract and is fixed by editing the request. Every network
// outcome — a name that does not resolve, a refused port, a rejected
// certificate, a body that stops half way, a cancellation — is an Exchange
// with a Failure on it and a nil error, because all of them are things that
// HAPPENED TO AN ATTEMPT TO SEND and the attempt is what the caller needs.
func (c *Client) Send(ctx context.Context, r apicoll.Request, k Key, used ...NamedSecret) (Exchange, error) {
	// The tracer is built first so it can be handed to a failure that
	// happens before there is anything to trace — it answers zeroes, which
	// is what "never reached" means for a phase.
	tr := &tracer{}

	req, custom, bodyText, err := buildRequest(ctx, r)
	if err != nil {
		if errors.Is(err, ErrFileBody) || errors.Is(err, ErrAuthUnresolved) {
			return Exchange{}, err
		}
		// Everything else buildRequest refuses — an address that will not
		// parse, one that is not absolute, a method net/http will not take
		// — is a send a person attempted and the attempt is the run. There
		// is no request text for it, and the phase says why.
		return settled(Raw{Spans: []Span{}}, tr, 0, PhaseCompose, err), nil
	}

	// The raw request is composed and PLACED before the send, against the
	// full text; what crosses is bounded afterwards. That order is the
	// whole mechanism: the placement is what the sender did and the text is
	// what fitted, so MarkRequest can report the difference as damage
	// rather than printing the prefix of a live credential (§11.1).
	//
	// It is also what makes a failed run readable: this value exists from
	// here to every return below, so no exit path can be missing it.
	composed := composeRequest(req, bodyText)
	sent := MarkRequest(c.bound(composed), locate(composed, used))

	cl, err := c.instanceFor(ctx, k)
	if err != nil {
		// Nothing instanceFor can fail at has put a byte on a wire: the
		// route table refusing the id, the pool refusing a lease, the jar
		// refusing to exist. The phase is the ROUTE's whatever the error
		// says underneath — except a cancellation, which is a stop
		// wherever it lands.
		phase := PhaseConnection
		if errors.Is(err, context.Canceled) {
			phase = PhaseStopped
		}
		return settled(sent, tr, 0, phase, err), nil
	}

	// The custom header names ride the context so the credential rule can
	// drop exactly those names on an origin change, and the tracer rides it
	// so the route can report the phases httptrace cannot see.
	ctx = httppolicy.WithCustomHeaderNames(req.Context(), custom)
	ctx = context.WithValue(ctx, traceKey{}, tr)
	req = req.WithContext(httptrace.WithClientTrace(ctx, tr.hooks()))

	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		return settled(sent, tr, time.Since(start),
			phaseOf(err, tr, PhaseExchange), sendError(req.Method, req.URL, err)), nil
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := captureBody(resp.Body, c.limit)
	if err != nil {
		// The connection was open and the read broke on it, so the fallback
		// is the exchange — but a cancellation here is still a stop, and
		// phaseOf is the one place that decides which.
		return settled(sent, tr, time.Since(start), phaseOf(err, tr, PhaseExchange),
			fmt.Errorf("%s: reading the response body of %s %s: %w",
				component, req.Method, redact(req.URL), err)), nil
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
	}
	certs := tr.certificates()
	if resp.TLS != nil {
		out.TLSVersion = tls.VersionName(resp.TLS.Version)
		out.TLSCipherSuite = tls.CipherSuiteName(resp.TLS.CipherSuite)
		certs = describeChain(resp.TLS.PeerCertificates)
	}
	// The response is searched on exactly the text that crosses, so there
	// is nothing on this side for a bound to damage — which is the
	// asymmetry §11.3 names: a placement exists independently of the text,
	// a match does not.
	out.Raw = SearchResponse(c.bound(composeResponse(out)), used)

	return Exchange{
		Outcome:      Answered,
		Request:      sent,
		RemoteAddr:   tr.remote(),
		Timings:      tr.timings(total),
		Certificates: certs,
		Response:     &out,
	}, nil
}

// settled builds the exchange for an attempt that did not answer. It is the
// ONE constructor of a non-answered Exchange, so the invariants the schema
// states — a request block whatever the outcome, a never-nil certificate
// list, a failure exactly when there is no response — hold by construction
// rather than by five call sites remembering them.
func settled(sent Raw, tr *tracer, total time.Duration, phase Phase, err error) Exchange {
	out := Exchange{
		Outcome:      Failed,
		Request:      sent,
		RemoteAddr:   tr.remote(),
		Timings:      tr.timings(total),
		Certificates: tr.certificates(),
		Failure:      &Failure{Phase: phase, Reason: err.Error()},
	}
	if phase == PhaseStopped {
		out.Outcome = Stopped
	}
	return out
}

// phaseOf says WHERE an attempt stopped, from the error together with what
// the tracer saw — a remote address means the dial landed, a TLS time means
// the handshake started, and the route reports its own resolve and dial
// because httptrace cannot see either through a connection.
//
// The order is the whole of it, and each step is ahead of the next for a
// reason:
//
//   - a cancellation is a STOP wherever it lands, so it is asked first and
//     nothing below can relabel a person's own Stop as a failure;
//   - the ROUTE's own refusals come next, including its dial bound: a person
//     whose bastion is the problem is not helped by being told "timeout";
//   - a resolve failure outranks a timeout, because a DNS lookup that timed
//     out is still a name that did not resolve;
//   - a handshake that started and produced no first byte is a TLS failure
//     even when the error underneath is an ordinary network one.
//
// fallback is the phase for an error nothing here recognises, and it is the
// caller's because it depends on where the call was: past the dial, an
// unrecognised failure is the exchange's.
func phaseOf(err error, tr *tracer, fallback Phase) Phase {
	switch {
	case errors.Is(err, context.Canceled):
		return PhaseStopped
	case errors.Is(err, ErrNoConnection), errors.Is(err, ErrNoSSHLease),
		errors.Is(err, ErrSSHDialTimeout), errors.Is(err, ErrNameResolvedRemotely):
		return PhaseConnection
	}
	var dns *net.DNSError
	if errors.As(err, &dns) || tr.resolveFailed() {
		return PhaseResolve
	}
	if isTLSFailure(err) || (tr.tlsStarted() && !tr.answered()) {
		return PhaseTLS
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		return PhaseTimeout
	}
	if tr.dialFailed() || tr.remote() == "" {
		return PhaseDial
	}
	return fallback
}

// isTLSFailure reports whether the handshake itself is what went wrong. The
// types rather than the message: a certificate failure spelt differently by
// a future Go release would silently become an "exchange" failure if this
// matched on words.
func isTLSFailure(err error) bool {
	var (
		verify   *tls.CertificateVerificationError
		alert    tls.AlertError
		record   tls.RecordHeaderError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &alert) || errors.As(err, &record) ||
		errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// bound is the ceiling applied to what this package puts on the control
// plane. It is the SAME ceiling as the body capture's, because it answers
// the same question — how much of an arbitrary remote thing may cross —
// and a second number here would be a second answer (§12.3).
//
// A cut can land inside a rune, so the result is made valid UTF-8 again:
// invalid bytes in a JSON string are not a diagnostic, they are a payload
// the renderer cannot decode at all.
func (c *Client) bound(s string) string {
	limit := c.limit
	if limit <= 0 || limit > ceiling {
		limit = ceiling
	}
	if int64(len(s)) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "\uFFFD")
}

// composeRequest renders the request as the text of §11: the request line,
// the Host, the headers this client set, and the body.
//
// It is what the sender COMPOSED rather than a capture of the bytes on the
// wire, and the difference is worth naming: the headers net/http adds for
// itself on the way out — Accept-Encoding, User-Agent, Content-Length — are
// not shown here, because this package did not write them and a diagnostic
// that invented them would be describing a request the user cannot edit.
// Line endings are LF: this is text for a person to read, not a frame to
// replay.
func composeRequest(req *http.Request, bodyText string) string {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteByte(' ')
	b.WriteString(req.URL.RequestURI())
	b.WriteString(" HTTP/1.1\n")
	b.WriteString("Host: ")
	b.WriteString(req.URL.Host)
	b.WriteByte('\n')
	writeHeaderLines(&b, headerRows(req.Header))
	b.WriteByte('\n')
	b.WriteString(bodyText)
	return b.String()
}

// composeResponse renders what came back. A binary body is the SENTENCE
// files.read gives and never base64 — base64 here would be exactly the bulk
// payload in JSON that AD-1 prohibits, arriving through a side door
// (§12.3).
func composeResponse(r Response) string {
	var b strings.Builder
	// Sized once: a 2 MiB body grown by doubling allocates several copies
	// of itself, and this runs on every send.
	b.Grow(len(r.Text) + 64*len(r.Headers) + 64)
	b.WriteString("HTTP/1.1 ")
	b.WriteString(strconv.Itoa(r.Status))
	if text := http.StatusText(r.Status); text != "" {
		b.WriteByte(' ')
		b.WriteString(text)
	}
	b.WriteByte('\n')
	writeHeaderLines(&b, r.Headers)
	b.WriteByte('\n')
	if r.Binary {
		fmt.Fprintf(&b, "binary body, %d bytes", r.Size)
		return b.String()
	}
	b.WriteString(r.Text)
	return b.String()
}

// headerRows flattens request headers into the model's row shape, sorted,
// through the one function that already answers this for the response side.
func headerRows(h http.Header) []apicoll.Header { return responseHeaders(h) }

func writeHeaderLines(b *strings.Builder, rows []apicoll.Header) {
	for _, h := range rows {
		b.WriteString(h.Name)
		b.WriteString(": ")
		b.WriteString(h.Value)
		b.WriteByte('\n')
	}
}

// buildRequest projects the model onto an http.Request and reports the
// canonical names of the headers the user set, which the credential rule
// needs per redirect hop.
func buildRequest(ctx context.Context, r apicoll.Request) (*http.Request, []string, string, error) {
	if r.Auth.Kind != "" && r.Auth.Kind != apicoll.AuthNone {
		return nil, nil, "", fmt.Errorf("%w (kind %q, variable %q)", ErrAuthUnresolved, r.Auth.Kind, r.Auth.Var)
	}
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: parsing URL %q: %w", component, r.URL, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return nil, nil, "", fmt.Errorf("%s: %q is not an absolute URL", component, r.URL)
	}
	appendQuery(u, r.Query)

	bodyText, contentType, err := requestBody(r.Body)
	if err != nil {
		return nil, nil, "", err
	}
	var body io.Reader
	if bodyText != "" {
		body = strings.NewReader(bodyText)
	}

	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	// NewRequestWithContext derives ContentLength and GetBody from a
	// *strings.Reader, and GetBody is what lets a 307 replay the body.
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", component, err)
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
	return req, custom, bodyText, nil
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

// requestBody turns the model's body into the TEXT that goes out and the
// content type the KIND declares. A raw body declares nothing — the user's
// own Content-Type header is the only answer, and guessing one would send a
// header they did not write. A JSON body declares application/json, because
// there the user has said which format it is; that is the whole difference
// between the two kinds.
//
// The text is returned rather than a reader because the raw diagnostic
// needs the same bytes the request carries, and a reader can be read once.
func requestBody(b apicoll.Body) (string, string, error) {
	switch b.Kind {
	case "", apicoll.BodyNone:
		return "", "", nil
	case apicoll.BodyRaw:
		return b.Text, "", nil
	case apicoll.BodyJSON:
		// The kind IS the declaration, so the header comes from it. A
		// Content-Type the user wrote themselves still wins: the caller
		// only fills this in when the request has none (sendRequest).
		return b.Text, "application/json", nil
	case apicoll.BodyForm:
		return b.Text, "application/x-www-form-urlencoded", nil
	case apicoll.BodyFile:
		return "", "", fmt.Errorf("%w (fileRef %q)", ErrFileBody, b.FileRef)
	default:
		return "", "", fmt.Errorf("%s: unknown body kind %q", component, b.Kind)
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
	// resolveErr and dialErr are WHICH STEP THE ROUTE REPORTED A FAILURE
	// AT, and they exist because the error alone cannot say. The policy
	// resolves http:// names itself and dials an address literal, so a
	// resolve failure there is not a *net.DNSError by the time it reaches
	// the caller; and a route that dials on the far side is not net.Dialer
	// at all. The wrapper that already times both steps is the one place
	// that knows, so it records it (phaseOf reads it).
	resolveErr bool
	dialErr    bool
	// certs is the chain the server presented, captured from the handshake
	// rather than only from a completed response — so a run that got past
	// TLS and broke afterwards still says which certificate it trusted.
	certs []*x509.Certificate
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

// setRemote records the address the dial landed on. It is written by both
// the dial wrapper and httptrace's GotConn — the same value from two
// vantage points, and the earlier one is what a failed handshake has.
func (t *tracer) setRemote(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.remoteAddr = addr
}

func (t *tracer) failedResolve() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolveErr = true
}

func (t *tracer) failedDial() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dialErr = true
}

func (t *tracer) resolveFailed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resolveErr
}

func (t *tracer) dialFailed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dialErr
}

// tlsStarted reports whether a handshake began — the second half of "a TLS
// time means the handshake started".
func (t *tracer) tlsStarted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.tlsStart.IsZero()
}

// answered reports whether a first response byte ever arrived. A failure
// after that point is the exchange's rather than the handshake's, however
// the handshake is timed.
func (t *tracer) answered() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ttfb > 0
}

// certificates describes the chain the handshake saw. Never nil: a plain
// http exchange presents none and that is [].
func (t *tracer) certificates() []Certificate {
	t.mu.Lock()
	chain := t.certs
	t.mu.Unlock()
	return describeChain(chain)
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
		// The state is EMPTY when the handshake failed — net/http passes a
		// zero ConnectionState with the error — so this captures the chain
		// for a run that got past TLS and broke afterwards, and never for
		// one that was refused at it. contracts/api.request.send says the
		// same thing to the renderer rather than leaving it to be
		// discovered from an empty list.
		TLSHandshakeDone: func(state tls.ConnectionState, _ error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.tlsStart.IsZero() {
				t.tlsTime = time.Since(t.tlsStart)
			}
			if len(state.PeerCertificates) > 0 {
				t.certs = state.PeerCertificates
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

// Certificate is one certificate from the chain a server presented, in the
// fields a person reads when they are deciding whether to trust it.
//
// DESCRIBED, NEVER RAW. The panel gets strings this package derived; it does
// not get the DER and does not parse anything. Two reasons, and the second is
// the load-bearing one: a renderer that parsed certificates would be a second
// X.509 implementation in the product, and the fingerprint — the one field
// somebody compares against a value their colleague read out over the phone —
// must be computed once, by the side that saw the bytes.
type Certificate struct {
	Subject   string
	Issuer    string
	NotBefore string
	NotAfter  string
	// DNSNames and IPAddresses are the SANs, which is what a name is
	// actually checked against — the CN in the subject has not been the
	// answer since 2017 and showing it alone is how people conclude a
	// certificate is fine when the host is not on it.
	DNSNames    []string
	IPAddresses []string
	// SelfSigned is true when the subject and the issuer are the same name.
	// It is a description of THIS certificate and never a verdict about the
	// connection: a self-signed leaf is exactly what an environment that
	// accepts self-signed certificates is for.
	SelfSigned bool
	// Fingerprint is the SHA-256 of the DER, lower-case hex in colon-
	// separated pairs — the spelling `openssl x509 -fingerprint -sha256`
	// prints, so the value on screen can be compared with the one a person
	// has in a terminal without either of them reformatting anything.
	Fingerprint string
}

// describeChain renders the presented chain, leaf first. A cap, because the
// list rides one JSON-RPC result and a hostile server may present many: ten
// is more than any real chain and the panel says nothing about what it did
// not receive, because the chain it shows is the chain that was used.
func describeChain(chain []*x509.Certificate) []Certificate {
	const maxChain = 10
	if len(chain) > maxChain {
		chain = chain[:maxChain]
	}
	out := make([]Certificate, 0, len(chain))
	for _, c := range chain {
		ips := make([]string, 0, len(c.IPAddresses))
		for _, ip := range c.IPAddresses {
			ips = append(ips, ip.String())
		}
		sum := sha256.Sum256(c.Raw)
		out = append(out, Certificate{
			Subject:     c.Subject.String(),
			Issuer:      c.Issuer.String(),
			NotBefore:   c.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:    c.NotAfter.UTC().Format(time.RFC3339),
			DNSNames:    append([]string{}, c.DNSNames...),
			IPAddresses: ips,
			SelfSigned:  c.Subject.String() == c.Issuer.String(),
			Fingerprint: hexPairs(sum[:]),
		})
	}
	return out
}

// hexPairs renders bytes as `ab:cd:ef…`, which is how every tool that prints
// a fingerprint prints one.
func hexPairs(b []byte) string {
	var sb strings.Builder
	for i, x := range b {
		if i > 0 {
			sb.WriteByte(':')
		}
		sb.WriteString(hex.EncodeToString([]byte{x}))
	}
	return sb.String()
}
