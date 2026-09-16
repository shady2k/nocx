package transport

// A connection that names its exchange on the way in (nocx-n14oo.11).
//
// The e2e harness mints one W3C trace id per Playwright test and has to get
// it onto every backend line that test's connection causes, without the
// renderer or the wire growing a second identity for the same thing — so it
// rides in on the query string of the same /session URL, using the spec's
// own header spelling (internal/log/span.go's traceparent, ADR taken by
// nocx-4l2a5). This is the test for the one line that reads it back:
// handleSession's log.ContinueTrace call.

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// bindRoot points log.From's process-wide fallback at lg for the life of the
// test, and restores whatever it was after — mirroring what every real
// composition root does at startup (internal/app/app.go, cmd/nocx-server).
// handleSession's ctx carries no per-request logger of its own (net/http
// gives every request context.Background(), not the one Start(ctx) was
// handed), so From(ctx) falls through to Root() exactly as it does in
// production; a test that skips this asserts against whatever an unrelated
// caller happened to leave in the global, or the untouched package default.
func bindRoot(t *testing.T, lg log.Logger) {
	t.Helper()
	prev := log.Root()
	log.SetRoot(lg)
	t.Cleanup(func() { log.SetRoot(prev) })
}

func dialWithTraceparent(t *testing.T, ws *WSServer, traceparent string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(wsURL(ws))
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if traceparent != "" {
		q := u.Query()
		q.Set("traceparent", traceparent)
		u.RawQuery = q.Encode()
	}
	d := websocket.Dialer{Subprotocols: []string{tokenProtocol(ws.Token())}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { forgetInbox(conn) })
	return conn
}

func hasAttr(r logtest.Record, key, value string) bool {
	for _, a := range r.Attrs {
		if a.Key == key && a.Value.String() == value {
			return true
		}
	}
	return false
}

// A connection that offers a traceparent has every request it sends carry
// that trace — not one the server minted for itself — so a test that opened
// the connection can find its own lines afterwards by trace_id alone.
func TestHandleSession_ContinuesTraceparentFromQuery(t *testing.T) {
	ctx, lg := logtest.New(t)
	bindRoot(t, lg)
	ws := NewWSServer(lg, newRegWithStub(lg))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	const (
		traceID = "0123456789abcdef0123456789abcdef"
		spanID  = "0123456789abcdef"
	)
	traceparent := "00-" + traceID + "-" + spanID + "-01"

	conn := dialWithTraceparent(t, ws, traceparent)
	defer func() { _ = conn.Close() }()

	jsonrpcCall(t, conn, "transport.ping", nil)

	if !logtest.WaitFor(ctx, 2*time.Second, func(r logtest.Record) bool {
		return hasAttr(r, "trace_id", traceID)
	}) {
		t.Fatalf("no log record carried trace_id=%s from the continued traceparent", traceID)
	}
}

// Two connections on the same server must never blend traces: a request on
// one must not carry the trace another connection supplied. This is what a
// shared e2e stand (one backend, many specs over the run) actually relies
// on — a test's own connection is the only thing that should ever say its
// trace_id.
func TestHandleSession_TwoConnectionsKeepSeparateTraces(t *testing.T) {
	ctx, lg := logtest.New(t)
	bindRoot(t, lg)
	ws := NewWSServer(lg, newRegWithStub(lg))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	const (
		traceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		traceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		spanID = "0123456789abcdef"
	)
	connA := dialWithTraceparent(t, ws, "00-"+traceA+"-"+spanID+"-01")
	defer func() { _ = connA.Close() }()
	connB := dialWithTraceparent(t, ws, "00-"+traceB+"-"+spanID+"-01")
	defer func() { _ = connB.Close() }()

	jsonrpcCallWithID(t, connA, "transport.ping", nil, 1)
	jsonrpcCallWithID(t, connB, "transport.ping", nil, 2)

	if !logtest.WaitFor(ctx, 2*time.Second, func(r logtest.Record) bool {
		return hasAttr(r, "trace_id", traceA)
	}) {
		t.Fatalf("connection A's trace %s never appeared", traceA)
	}
	if !logtest.WaitFor(ctx, 2*time.Second, func(r logtest.Record) bool {
		return hasAttr(r, "trace_id", traceB)
	}) {
		t.Fatalf("connection B's trace %s never appeared", traceB)
	}
}

// A connection offering nothing, or garbage, still gets served under a trace
// of its own — ContinueTrace's whole point is that refusing telemetry must
// never mean refusing the request.
func TestHandleSession_NoOrMalformedTraceparentStillGetsServed(t *testing.T) {
	ctx, lg := logtest.New(t)
	bindRoot(t, lg)
	ws := NewWSServer(lg, newRegWithStub(lg))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	for _, tp := range []string{"", "not-a-traceparent"} {
		conn := dialWithTraceparent(t, ws, tp)
		resp := jsonrpcCall(t, conn, "transport.ping", nil)
		if len(resp) == 0 {
			t.Fatalf("traceparent=%q: no response to transport.ping", tp)
		}
		_ = conn.Close()
	}

	if !logtest.WaitFor(ctx, 2*time.Second, func(r logtest.Record) bool {
		return r.Message == "jsonrpc dispatch"
	}) {
		t.Fatalf("dispatch never logged at all")
	}
}
