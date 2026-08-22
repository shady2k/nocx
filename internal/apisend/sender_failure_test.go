package apisend

// Design §12.1: every external call fails in a test, and each failure is
// paired with "and on an ordinary machine it succeeds" — AGENTS.md testing
// rule 3 is explicit that a "returns an error when…" without its pair is
// half a test, because a call that always fails passes the first half.
//
// The four calls a send makes: resolve the name, open the socket, complete
// the TLS handshake, read the body.
//
// EACH OF THEM ASSERTS A PHASE, and none of them asserts an error, because
// none of them is one any more. A network outcome is an EXCHANGE with a
// failure on it — an attempt that got as far as it got — and the phase is
// the field a surface can act on: "resolve" is a name to fix, "dial" is a
// server to start, "connection" is a bastion to open. `failedAt` also
// checks, for every one of these, that the composed request survived the
// failure; before this change every test below discarded it with the value
// it was asserting about.

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- 1. Resolving the name ---

func TestSend_DNSFailureIsReported(t *testing.T) {
	boom := errors.New("no such host")
	route := &fakeRoute{
		resolve: func(context.Context, string) ([]net.IP, error) { return nil, boom },
		dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			t.Errorf("dialled %q after a resolve failure", addr)
			return nil, errors.New("must not dial")
		},
	}
	ex, err := New(WithRoutes(fixedRoute(route))).Send(context.Background(),
		apicollGet("http://api.example.com/v1"), Key{})
	fail := failedAt(t, ex, err, PhaseResolve)
	if !strings.Contains(fail.Reason, boom.Error()) {
		t.Fatalf("reason = %q, want the resolver's error in it", fail.Reason)
	}
	// AND THE REQUEST IS THERE. This is the acceptance in one line: a send
	// to a name that does not resolve comes back with the text that would
	// have gone out, so the person reading the row can see the address they
	// typed rather than only the sentence saying it did not work.
	if !strings.Contains(ex.Request.Text, "GET /v1 HTTP/1.1") {
		t.Errorf("the failed exchange carries no request line:\n%s", ex.Request.Text)
	}
	if ex.Response != nil {
		t.Error("a resolve failure carries a response")
	}
}

// TestSend_DNSFailureOnAnOrdinaryMachine is the same failure without a fake:
// .invalid is reserved never to resolve (RFC 2606).
func TestSend_DNSFailureOnAnOrdinaryMachine(t *testing.T) {
	ex, err := New().Send(context.Background(),
		apicollGet("http://nocx-no-such-host.invalid/v1"), Key{})
	fail := failedAt(t, ex, err, PhaseResolve)
	if !strings.Contains(fail.Reason, "resolving") {
		t.Fatalf("reason = %q, want it to name the resolve step", fail.Reason)
	}
	if !strings.Contains(ex.Request.Text, "nocx-no-such-host.invalid") {
		t.Errorf("the failed exchange does not show the host that was asked for:\n%s", ex.Request.Text)
	}
}

// TestSend_ResolvesOnAnOrdinaryMachine is the pair: a NAME the machine's own
// resolver answers, sent through the machine's own route.
func TestSend_ResolvesOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	ex, err := New().Send(context.Background(),
		apicollGet("http://localhost:"+port+"/"), Key{})
	got := answered(t, ex, err)
	if got.Text != "ok" {
		t.Fatalf("Text = %q, want ok", got.Text)
	}
	if ex.Timings.DNS <= 0 {
		t.Error("Timings.DNS = 0 — the name was resolved, and the timing says how long it took")
	}
}

// --- 2. Opening the socket ---

func TestSend_ConnectionRefusedIsReported(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens on that port now

	ex, err := New().Send(context.Background(), apicollGet("http://"+addr+"/"), Key{})
	fail := failedAt(t, ex, err, PhaseDial)
	if !strings.Contains(fail.Reason, "refused") {
		t.Fatalf("reason = %q, want a connection refusal", fail.Reason)
	}
	// Nothing answered, so there is no address to report — which is the
	// same fact the phase was derived from.
	if ex.RemoteAddr != "" {
		t.Errorf("RemoteAddr = %q for a dial nothing accepted", ex.RemoteAddr)
	}
}

// TestSend_ConnectsOnAnOrdinaryMachine is the pair: the same address with
// something listening on it.
func TestSend_ConnectsOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if got.Status != http.StatusOK || got.Text != "ok" {
		t.Fatalf("status %d body %q, want 200 ok", got.Status, got.Text)
	}
}

// --- 3. The TLS handshake ---

func TestSend_TLSHandshakeFailureIsReported(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	// No trust configured: the server's self-signed certificate is not one
	// this machine has any reason to accept.
	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	fail := failedAt(t, ex, err, PhaseTLS)
	if !strings.Contains(fail.Reason, "certificate") {
		t.Fatalf("reason = %q, want a certificate failure", fail.Reason)
	}
	// The dial LANDED — which is what makes this a tls failure rather than
	// a dial one, and is the fact a person needs: the host is up, and it is
	// the certificate that is the problem.
	if ex.RemoteAddr == "" {
		t.Error("RemoteAddr is empty for a handshake that reached a server")
	}
	// And the chain is empty, deliberately: net/http hands the trace an
	// empty connection state when the handshake fails. Asserted rather than
	// left to be discovered, because the contract says so to the renderer.
	if len(ex.Certificates) != 0 {
		t.Errorf("Certificates = %d for a refused handshake; the contract says this list is empty here", len(ex.Certificates))
	}
}

// TestSend_TLSHandshakeSucceedsWithTrust is the pair: the same server, the
// same handshake, and the certificate trusted.
func TestSend_TLSHandshakeSucceedsWithTrust(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	ex, err := newTrusting(trust(srv)).Send(context.Background(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if got.Text != "secure" {
		t.Fatalf("Text = %q, want secure", got.Text)
	}
}

// --- 4. Reading the body ---

// TestSend_ServerClosingMidBodyIsReported: a short body is a FAILURE, not a
// shorter answer. The server promises 1000 bytes, sends 7 and hangs up; the
// send must say so rather than hand back seven bytes as if they were the
// response.
func TestSend_ServerClosingMidBodyIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		writeAndClose(buf, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\npartial")
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	// PHASE `exchange`: the connection was open and it broke on it. That is
	// a different remedy from every phase above — nothing about the name,
	// the route or the certificate is wrong — and a run that said only
	// "unexpected EOF" made a person work that out for themselves.
	fail := failedAt(t, ex, err, PhaseExchange)
	if !strings.Contains(fail.Reason, io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("reason = %q, want the unexpected EOF the transport reported", fail.Reason)
	}
	if !strings.Contains(fail.Reason, "reading the response body") {
		t.Fatalf("reason = %q, want it to name the step that failed", fail.Reason)
	}
	// The seven bytes are NOT handed back as a body. A partial answer
	// rendered as an answer is the defect this test has always been about,
	// and it survives the shape change: there is no response at all.
	if ex.Response != nil {
		t.Errorf("a truncated body produced a response: %+v", *ex.Response)
	}
}

// TestSend_ReadsACompleteBodyOnAnOrdinaryMachine is the pair: the same
// promise, kept.
func TestSend_ReadsACompleteBodyOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "7")
		_, _ = io.WriteString(w, "partial")
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if got.Text != "partial" || got.Size != 7 {
		t.Fatalf("Text = %q, Size = %d, want the whole seven bytes", got.Text, got.Size)
	}
	if got.Truncated {
		t.Error("Truncated = true for a body far below the ceiling")
	}
}

// TestSend_CancelledContextStopsTheExchange: the caller's cancellation is
// the only timeout this package has, so it must reach the network — and it
// comes back as a STOP rather than as a failure, because the person who
// started the exchange is the one who ended it.
func TestSend_CancelledContextStopsTheExchange(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-released
	}))
	defer srv.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex, err := New().Send(ctx, apicollGet(srv.URL), Key{})
	fail := failedAt(t, ex, err, PhaseStopped)
	if ex.Outcome != Stopped {
		t.Fatalf("outcome = %q, want stopped — a stop is never a failure", ex.Outcome)
	}
	if !strings.Contains(fail.Reason, context.Canceled.Error()) {
		t.Errorf("reason = %q, want the cancellation named in it", fail.Reason)
	}
	// The request is still on it, which is what makes a stopped run
	// readable: the row says what was being sent when it was stopped.
	if !strings.Contains(ex.Request.Text, "GET / HTTP/1.1") {
		t.Errorf("a stopped exchange carries no request text:\n%s", ex.Request.Text)
	}
}

// --- 5. a bound that elapses ---

// TestSend_ADeadlineThatHasPassedIsATimeoutAndNotAStop: the two ways an
// exchange can be ended from outside look alike underneath — both arrive as
// a context that is done — and they are opposite facts about the run. A
// stop is somebody's decision; a timeout is a bound nobody chose in the
// moment. The phase is where that distinction survives.
//
// THE DEADLINE IS ALREADY PAST, so nothing here waits: the send returns on
// its first look at the context, on any machine, at any speed.
func TestSend_ADeadlineThatHasPassedIsATimeoutAndNotAStop(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	ex, err := New().Send(ctx, apicollGet("https://127.0.0.1:1/v1"), Key{})
	failedAt(t, ex, err, PhaseTimeout)
	if ex.Outcome != Failed {
		t.Errorf("outcome = %q, want failed — nobody stopped this one", ex.Outcome)
	}
}

// TestSend_TheSameRequestWithinItsDeadlineAnswers is the pair: the identical
// bound, not elapsed.
func TestSend_TheSameRequestWithinItsDeadlineAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "in time")
	}))
	defer srv.Close()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	ex, err := New().Send(ctx, apicollGet(srv.URL), Key{})
	if got := answered(t, ex, err); got.Text != "in time" {
		t.Fatalf("Text = %q, want the answer", got.Text)
	}
}

func writeAndClose(buf *bufio.ReadWriter, s string) {
	_, _ = buf.WriteString(s)
	_ = buf.Flush()
}
