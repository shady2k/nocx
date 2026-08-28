package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewSlogAdapter_DoesNotPanic(t *testing.T) {
	inner := slog.Default()
	a := NewSlogAdapter(inner)
	a.Debug("test debug")
	a.Info("test info")
	a.Warn("test warn")
	a.Error("test error")
}

func TestSlogAdapter_With(t *testing.T) {
	inner := slog.Default()
	a := NewSlogAdapter(inner)
	b := a.With("key", "value")
	if b == nil {
		t.Fatal("With returned nil")
	}
	b.Info("test with")
}

func TestSlogAdapter_WithContext(t *testing.T) {
	inner := slog.Default()
	a := NewSlogAdapter(inner)
	ctx := context.Background()
	b := a.WithContext(ctx)
	if b == nil {
		t.Fatal("WithContext returned nil")
	}
}

func TestTraceIDFromContext_EmptyWhenNotSet(t *testing.T) {
	ctx := context.Background()
	if id := TraceID(ctx); id != "" {
		t.Fatalf("expected empty, got %q", id)
	}
	if id := RequestID(ctx); id != "" {
		t.Fatalf("expected empty, got %q", id)
	}
}

// THE CHAIN IS CARRIED, NOT RE-DERIVED: a context that was given a trace
// hands it to every logger built from it, however far down it is passed.
// The producer half is what was missing — WithContext read an id nobody set.
func TestWithContext_CarriesTheTraceAndRequestOntoEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{AddSource: true})))

	ctx := WithRequestID(WithTraceID(context.Background(), "run-349"), "agent.approve#7")
	a.WithContext(ctx).Warn("the parked program was ended", "cause", "discarded")

	out := buf.String()
	for _, want := range []string{"trace=run-349", "request=agent.approve#7", "cause=discarded"} {
		if !strings.Contains(out, want) {
			t.Fatalf("record does not carry %q:\n%s", want, out)
		}
	}
	// AND IT NAMES ITSELF: the source is the caller's file, never the
	// adapter's — the whole reason the adapter builds the record by hand.
	if !strings.Contains(out, "log_test.go:") {
		t.Fatalf("source names the adapter rather than the caller:\n%s", out)
	}
}

// An exchange with no ids logs no empty ones: a trace="" on every line of a
// process buries the lines that carry a real one.
func TestWithContext_AttachesNothingWhenThereIsNoChain(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil)))
	a.WithContext(context.Background()).Info("plain")
	if out := buf.String(); strings.Contains(out, "trace=") || strings.Contains(out, "request=") {
		t.Fatalf("empty ids were attached:\n%s", out)
	}
}

// CallPath answers the other question — who asked for this — for the rare
// event whose immediate frame is not the answer.
func TestCallPath_NamesTheChainThatReachedHere(t *testing.T) {
	got := callPathThroughAHelper()
	for _, want := range []string{"callPathThroughAHelper", "TestCallPath_NamesTheChainThatReachedHere"} {
		if !strings.Contains(got, want) {
			t.Fatalf("call path %q does not name %s", got, want)
		}
	}
}

func callPathThroughAHelper() string { return CallPath(0) }
