package log

import (
	"context"
	"log/slog"
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
	if id := traceIDFromContext(ctx); id != "" {
		t.Fatalf("expected empty, got %q", id)
	}
}
