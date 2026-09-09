package log

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// THE FORMAT IS THE POINT. These ids exist to be joined on by something that
// is not us, so they are the W3C Trace Context spelling exactly: 32 and 16
// lowercase hex characters, and never the all-zero value the spec reserves as
// "no id".
func TestMintedIDsAreTheSpecsShape(t *testing.T) {
	for i := 0; i < 64; i++ {
		trace := NewTraceID()
		if len(trace) != 32 || !isLowerHex(trace) || trace == zeroTrace {
			t.Fatalf("trace id %q is not 32 lowercase hex or is the reserved zero", trace)
		}
		span := NewSpanID()
		if len(span) != 16 || !isLowerHex(span) || span == zeroSpan {
			t.Fatalf("span id %q is not 16 lowercase hex or is the reserved zero", span)
		}
	}
}

// A ROOT HAS NO PARENT, AND A CHILD HAS EXACTLY ONE. This is the whole of the
// chain: the trace is the exchange and never changes, the span is this frame,
// and the parent names the frame that asked for it.
func TestStartSpanOpensARootAndThenChildrenOfIt(t *testing.T) {
	rootCtx, root := StartSpan(context.Background())
	if root.ParentSpanID != "" {
		t.Fatalf("a root span has a parent: %+v", root)
	}
	if !root.Valid() {
		t.Fatalf("root span is not valid: %+v", root)
	}

	_, child := StartSpan(rootCtx)
	if child.TraceID != root.TraceID {
		t.Fatalf("child left the trace: %q != %q", child.TraceID, root.TraceID)
	}
	if child.ParentSpanID != root.SpanID {
		t.Fatalf("child's parent is %q, want the root's span %q", child.ParentSpanID, root.SpanID)
	}
	if child.SpanID == root.SpanID {
		t.Fatal("child reused its parent's span id")
	}
}

// The context is the carrier, so a span read back is the one that was put in.
func TestSpanFromReadsBackWhatTheContextCarries(t *testing.T) {
	if got := SpanFrom(context.Background()); got.Valid() {
		t.Fatalf("an unmarked context carries a span: %+v", got)
	}
	ctx, span := StartSpan(context.Background())
	if got := SpanFrom(ctx); got != span {
		t.Fatalf("read back %+v, want %+v", got, span)
	}
}

// THE PROCESS BOUNDARY. A traceparent is what crosses it, and what comes back
// out of the parse is what went in.
func TestTraceparentSurvivesFormatAndParse(t *testing.T) {
	_, span := StartSpan(context.Background())
	header := span.Traceparent()
	if !strings.HasPrefix(header, "00-") {
		t.Fatalf("traceparent %q does not declare version 00", header)
	}
	back, ok := ParseTraceparent(header)
	if !ok {
		t.Fatalf("traceparent %q did not parse", header)
	}
	if back.TraceID != span.TraceID || back.SpanID != span.SpanID || back.Sampled != span.Sampled {
		t.Fatalf("parsed %+v, want %+v", back, span)
	}
	if back.Traceparent() != header {
		t.Fatalf("reformatted %q, want %q", back.Traceparent(), header)
	}
}

func TestParseTraceparentRefusesWhatIsNotOne(t *testing.T) {
	for _, bad := range []string{
		"",
		"nonsense",
		"00-" + zeroTrace + "-" + NewSpanID() + "-01",
		"00-" + NewTraceID() + "-" + zeroSpan + "-01",
		"01-" + NewTraceID() + "-" + NewSpanID() + "-01",
		"00-" + NewTraceID() + "-" + NewSpanID(),
		"00-XYZ-" + NewSpanID() + "-01",
	} {
		if _, ok := ParseTraceparent(bad); ok {
			t.Fatalf("%q was accepted as a traceparent", bad)
		}
	}
}

// CONTINUING SOMEBODY ELSE'S TRACE is not the same as adopting their span: the
// header names the caller's frame, and what we open is a child of it.
func TestContinueTraceMakesTheHeaderTheParent(t *testing.T) {
	_, caller := StartSpan(context.Background())

	ctx := ContinueTrace(context.Background(), caller.Traceparent())
	_, ours := StartSpan(ctx)

	if ours.TraceID != caller.TraceID {
		t.Fatalf("the trace was not continued: %q != %q", ours.TraceID, caller.TraceID)
	}
	if ours.ParentSpanID != caller.SpanID {
		t.Fatalf("parent is %q, want the caller's span %q", ours.ParentSpanID, caller.SpanID)
	}
	// A header that is not one leaves the context alone rather than raising:
	// a caller that sent nothing still gets served, under a trace of its own.
	plain := ContinueTrace(context.Background(), "not a traceparent")
	if SpanFrom(plain).Valid() {
		t.Fatalf("a malformed header produced a span: %+v", SpanFrom(plain))
	}
}

// AN EXCHANGE WHOSE ID ALREADY EXISTS needs a trace id that is stable across
// the several frames it takes, and nothing to store it in. The same seed is
// the same trace, a different seed is a different one, and both are the
// spec's shape.
func TestDeterministicTraceIDIsStableAndWellFormed(t *testing.T) {
	a := DeterministicTraceID("run-349")
	if a != DeterministicTraceID("run-349") {
		t.Fatal("the same seed produced two traces")
	}
	if a == DeterministicTraceID("run-350") {
		t.Fatal("two seeds produced one trace")
	}
	if len(a) != 32 || !isLowerHex(a) || a == zeroTrace {
		t.Fatalf("derived trace id %q is not the spec's shape", a)
	}
}

// EVERY RECORD UNDER A SPAN CARRIES IT. This is what makes a failure readable:
// one grep for a trace_id returns the whole exchange, whichever module wrote
// each line.
func TestWithContextCarriesTheWholeChainOntoEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{AddSource: true})))

	rootCtx, root := StartSpan(context.Background())
	childCtx, child := StartSpan(rootCtx)
	childCtx = WithRequestID(childCtx, "agent.approve#7")

	a.WithContext(childCtx).Warn("the enrolment never arrived", "cause", "hello-timeout")

	out := buf.String()
	for _, want := range []string{
		"trace_id=" + root.TraceID,
		"span_id=" + child.SpanID,
		"parent_span_id=" + root.SpanID,
		"request_id=agent.approve#7",
		"cause=hello-timeout",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("record does not carry %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "span_test.go:") {
		t.Fatalf("source names the adapter rather than the caller:\n%s", out)
	}
}

// AN INSTRUMENTED CALL SAYS FOUR THINGS: that it started and with what
// arguments, that it ended, how long it took, and how it went. The 30 silent
// seconds between "worker participant spawned" and "enrolment never arrived"
// are what this exists to remove.
func TestStartInstrumentsACallFromEntryToOutcome(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, lg, end := Start(context.Background(), a, "workers.spawn", "participant", "b37e279b")
	lg.Info("worker participant spawned")
	end(errors.New("enrolment never arrived"))

	out := buf.String()
	for _, want := range []string{
		`msg="workers.spawn: start"`,
		"participant=b37e279b",
		`msg="workers.spawn: failed"`,
		"error=",
		"duration_ms=",
		"op=workers.spawn",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("instrumentation does not carry %q:\n%s", want, out)
		}
	}
	// The line written INSIDE the call belongs to the same span as its start
	// and its end, which is what makes the three one call rather than three
	// events.
	span := SpanFrom(ctx)
	if !span.Valid() {
		t.Fatal("Start did not put a span in the context it returned")
	}
	if strings.Count(out, "span_id="+span.SpanID) != 3 {
		t.Fatalf("the three lines of one call do not share its span:\n%s", out)
	}
}

// A call that succeeds says so, and says nothing about an error it did not
// have.
func TestStartSaysOkWhenNothingFailed(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	_, _, end := Start(context.Background(), a, "session.open")
	end(nil)
	out := buf.String()
	if !strings.Contains(out, `msg="session.open: ok"`) {
		t.Fatalf("a successful call did not say so:\n%s", out)
	}
	if strings.Contains(out, "error=") {
		t.Fatalf("a successful call reported an error:\n%s", out)
	}
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return len(s) > 0
}
