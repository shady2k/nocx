package toolendpoint

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	nocxlog "github.com/shady2k/nocx/internal/log"
)

// THE EXCHANGE CROSSES A PROCESS BOUNDARY, AND THE TRACE CROSSES IT WITH IT.
//
// A coordinator's MCP call is served by nocx-helper, which asks this endpoint,
// which dispatches inside the backend. On 2026-09-09 those were three sets of
// log lines with nothing in common, and the failure could only be assembled by
// reading timestamps. The caller's traceparent is what joins them: what we open
// is a CHILD of the frame it names, never a claim to be that frame.
func TestATraceparentFromTheCallerBecomesTheParentOfThisRequest(t *testing.T) {
	var logs strings.Builder
	_, caller := nocxlog.StartSpan(nil) //nolint:staticcheck // a nil context is the "no parent anywhere" case

	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{err: errors.New("the participant's session refused its first line")})
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)

	request := `{"jsonrpc":"2.0","id":7,"method":"workers.spawn","params":{},"traceparent":"` + caller.Traceparent() + `"}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error == nil || response.Error.Code != rpcInternalError {
		t.Fatalf("response error = %+v, want the unclassified failure", response.Error)
	}

	out := logs.String()
	if !strings.Contains(out, "unclassified dispatch failure") {
		t.Fatalf("the failure was not logged at all:\n%s", out)
	}
	for _, want := range []string{
		"trace_id=" + caller.TraceID,
		"parent_span_id=" + caller.SpanID,
		"request_id=7",
		"op=workers.spawn",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the endpoint's log does not carry %q:\n%s", want, out)
		}
	}
	// A CHILD, NOT THE CALLER'S OWN FRAME. Adopting the caller's span id would
	// make two processes claim one frame, and the tree would lose the hop.
	if strings.Contains(out, " span_id="+caller.SpanID+" ") {
		t.Fatalf("the endpoint adopted the caller's span rather than opening a child:\n%s", out)
	}
}

// A CALLER THAT SENT NO TRACEPARENT IS STILL SERVED, under a trace of its own.
// Observability that can refuse service is not observability.
func TestARequestWithoutATraceparentIsServedUnderItsOwnTrace(t *testing.T) {
	var logs strings.Builder
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)

	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("response error = %+v, want a served request", response.Error)
	}
	out := logs.String()
	if !strings.Contains(out, "trace_id=") {
		t.Fatalf("a request with no traceparent got no trace of its own:\n%s", out)
	}
	if strings.Contains(out, "parent_span_id=") {
		t.Fatalf("a root frame was given a parent:\n%s", out)
	}
}

// A MALFORMED HEADER IS NOT A BAD REQUEST. It is a caller whose telemetry we
// cannot join, and the call still runs.
func TestAMalformedTraceparentDoesNotRefuseTheCall(t *testing.T) {
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)

	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{},"traceparent":"not-a-header"}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if response := readResponse(t, conn); response.Error != nil {
		t.Fatalf("a malformed traceparent refused the call: %+v", response.Error)
	}
}

// THE DISPATCHER IS INSIDE THE SPAN. Everything the backend does for this call
// hangs off it, which is the whole reason the id crosses the boundary at all.
func TestTheDispatcherIsGivenTheRequestsSpan(t *testing.T) {
	_, caller := nocxlog.StartSpan(nil) //nolint:staticcheck // no parent anywhere
	dispatch := &testDispatcher{out: `{"held":[]}`}
	cfg := endpointConfig(t, &testAuthorizer{}, dispatch)
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)

	request := `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{},"traceparent":"` + caller.Traceparent() + `"}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = readResponse(t, conn)

	span := nocxlog.SpanFrom(dispatch.lastInvocation().Context)
	if !span.Valid() {
		t.Fatal("the dispatcher was handed a context with no span")
	}
	if span.TraceID != caller.TraceID {
		t.Fatalf("the dispatcher's trace is %q, want the caller's %q", span.TraceID, caller.TraceID)
	}
	if span.ParentSpanID != caller.SpanID {
		t.Fatalf("the dispatcher's parent is %q, want the caller's span %q", span.ParentSpanID, caller.SpanID)
	}
}
