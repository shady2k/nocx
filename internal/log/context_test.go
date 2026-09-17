package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// From(ctx) is the whole point of this file: a call site logs through the
// context and gets module, trace, span and request id without asking for any
// of them by name.
func TestFrom_CarriesModuleTraceSpanAndRequestOntoEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	inner := NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil)))

	traced, span := StartTrace(context.Background(), DeterministicTraceID("run-349"))
	ctx := WithRequestID(traced, "agent.approve#7")
	ctx = WithLogger(ctx, inner)

	From(ctx).Warn("the parked program was ended", "cause", "discarded")

	out := buf.String()
	for _, want := range []string{
		"module=internal/log",
		"trace_id=" + span.TraceID,
		"span_id=" + span.SpanID,
		"request_id=agent.approve#7",
		"cause=discarded",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("record does not carry %q:\n%s", want, out)
		}
	}
}

// The call site never passes module, request_id, trace_id or span_id itself
// — From derives all four. This is the negative of the test above: a plain
// call with only the caller's own attribute still gets module.
func TestFrom_DerivesModuleWithNoCallSiteArgument(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))))

	From(ctx).Info("plain line")

	if out := buf.String(); !strings.Contains(out, "module=internal/log") {
		t.Fatalf("record does not carry module:\n%s", out)
	}
}

// With no logger in the context, From falls back to the process root rather
// than panicking or writing nowhere.
func TestFrom_NoLoggerInContext_ReturnsRoot(t *testing.T) {
	var buf bytes.Buffer
	prev := Root()
	t.Cleanup(func() { SetRoot(prev) })
	SetRoot(NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))))

	From(context.Background()).Info("root fallback")

	if out := buf.String(); !strings.Contains(out, "root fallback") {
		t.Fatalf("From(ctx with no logger) did not use the root:\n%s", out)
	}
}

// A nil context behaves like one carrying nothing: From still answers,
// through the root, instead of panicking on ctx.Value.
func TestFrom_NilContext_ReturnsRootWithoutPanicking(t *testing.T) {
	var buf bytes.Buffer
	prev := Root()
	t.Cleanup(func() { SetRoot(prev) })
	SetRoot(NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))))

	//nolint:staticcheck // exercising From's nil-ctx guard deliberately
	From(nil).Info("nil ctx")

	if out := buf.String(); !strings.Contains(out, "nil ctx") {
		t.Fatalf("From(nil) did not use the root:\n%s", out)
	}
}

// WithLogger(ctx, nil) is a no-op: it must not make From panic or silently
// swallow every record from there down.
func TestWithLogger_Nil_IsANoOp(t *testing.T) {
	ctx := WithLogger(context.Background(), nil)
	if got := From(ctx); got == nil {
		t.Fatal("From returned nil after WithLogger(ctx, nil)")
	}
}

// A nested WithLogger call replaces what an outer one bound — the innermost
// scope's logger is the one From finds, same as any other context value.
func TestWithLogger_NestedCallReplacesTheOuterLogger(t *testing.T) {
	var outerBuf, innerBuf bytes.Buffer
	outer := NewSlogAdapter(slog.New(slog.NewTextHandler(&outerBuf, nil)))
	inner := NewSlogAdapter(slog.New(slog.NewTextHandler(&innerBuf, nil)))

	ctx := WithLogger(context.Background(), outer)
	ctx = WithLogger(ctx, inner)

	From(ctx).Info("innermost wins")

	if outerBuf.Len() != 0 {
		t.Fatalf("outer logger received a record it should not have:\n%s", outerBuf.String())
	}
	if !strings.Contains(innerBuf.String(), "innermost wins") {
		t.Fatalf("inner logger did not receive the record:\n%s", innerBuf.String())
	}
}

// packageOf is what keeps two same-named packages apart — internal/session
// and internal/helper/session both end in ".../session", and shortFunc alone
// (span.go's CallPath helper) cannot tell them apart. This is the case
// callerModule exists to get right.
func TestPackageOf_KeepsSameNamedPackagesApart(t *testing.T) {
	cases := map[string]string{
		"github.com/shady2k/nocx/internal/session.(*Reg).Open":               "internal/session",
		"github.com/shady2k/nocx/internal/helper/session.(*Manager).Open":    "internal/helper/session",
		"github.com/shady2k/nocx/internal/transport.(*WSServer).openSession": "internal/transport",
		"github.com/shady2k/nocx/cmd/nocx-server.main":                       "cmd/nocx-server",
	}
	for fn, want := range cases {
		if got := packageOf(fn); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", fn, got, want)
		}
	}
}
