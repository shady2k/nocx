package apisend

// Design §12.1: every external call fails in a test, and each failure is
// paired with "and on an ordinary machine it succeeds" — AGENTS.md testing
// rule 3 is explicit that a "returns an error when…" without its pair is
// half a test, because a call that always fails passes the first half.
//
// The four calls a send makes: resolve the name, open the socket, complete
// the TLS handshake, read the body.

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
	_, err := New(WithRoutes(fixedRoute(route))).Send(context.Background(),
		apicollGet("http://api.example.com/v1"), Key{})
	if err == nil {
		t.Fatal("send with a failing resolver succeeded")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolver's error", err)
	}
}

// TestSend_DNSFailureOnAnOrdinaryMachine is the same failure without a fake:
// .invalid is reserved never to resolve (RFC 2606).
func TestSend_DNSFailureOnAnOrdinaryMachine(t *testing.T) {
	_, err := New().Send(context.Background(),
		apicollGet("http://nocx-no-such-host.invalid/v1"), Key{})
	if err == nil {
		t.Fatal("send to an unresolvable name succeeded")
	}
	if !strings.Contains(err.Error(), "resolving") {
		t.Fatalf("err = %v, want it to name the resolve step", err)
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

	got, err := New().Send(context.Background(),
		apicollGet("http://localhost:"+port+"/"), Key{})
	if err != nil {
		t.Fatalf("send to localhost by name: %v", err)
	}
	if got.Text != "ok" {
		t.Fatalf("Text = %q, want ok", got.Text)
	}
	if got.Timings.DNS <= 0 {
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

	_, err = New().Send(context.Background(), apicollGet("http://"+addr+"/"), Key{})
	if err == nil {
		t.Fatal("send to a closed port succeeded")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want a connection refusal", err)
	}
}

// TestSend_ConnectsOnAnOrdinaryMachine is the pair: the same address with
// something listening on it.
func TestSend_ConnectsOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	got, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
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
	_, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	if err == nil {
		t.Fatal("handshake with an untrusted certificate succeeded")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate failure", err)
	}
}

// TestSend_TLSHandshakeSucceedsWithTrust is the pair: the same server, the
// same handshake, and the certificate trusted.
func TestSend_TLSHandshakeSucceedsWithTrust(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	got, err := New(WithTLSClientConfig(trust(srv))).Send(context.Background(), apicollGet(srv.URL), Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
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

	_, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	if err == nil {
		t.Fatal("a truncated body was accepted as a response")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want the unexpected EOF the transport reported", err)
	}
	if !strings.Contains(err.Error(), "reading the response body") {
		t.Fatalf("err = %v, want it to name the step that failed", err)
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

	got, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "partial" || got.Size != 7 {
		t.Fatalf("Text = %q, Size = %d, want the whole seven bytes", got.Text, got.Size)
	}
	if got.Truncated {
		t.Error("Truncated = true for a body far below the ceiling")
	}
}

// TestSend_CancelledContextStopsTheExchange: the caller's cancellation is
// the only timeout this package has, so it must reach the network.
func TestSend_CancelledContextStopsTheExchange(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-released
	}))
	defer srv.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Send(ctx, apicollGet(srv.URL), Key{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func writeAndClose(buf *bufio.ReadWriter, s string) {
	_, _ = buf.WriteString(s)
	_ = buf.Flush()
}
