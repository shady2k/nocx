package log

// EVERY RECORD SAYS WHERE IT CAME FROM (nocx-d6gn4.8.1).
//
// slog computes a record's source from the caller of its Info/Warn/Error,
// and an adapter that forwards to them makes every line in the product point
// at THIS FILE. So the source is useless exactly when it is wanted: a
// warning that names neither the module that wrote it nor the line it is on
// is a sentence to grep the tree for by hand.
//
// The adapter therefore builds the record itself with the real caller's
// program counter, and the handler is constructed with AddSource. One
// change, and every log line in the application answers "which module, which
// function, which line" — including the ones nobody thought to instrument.
//
// CallPath is the same question one step further out, for the rare event
// where the immediate frame is not the answer: who cancelled this, who
// discarded that. It lives here, once, rather than being hand-rolled at
// whichever call site is currently being debugged.

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"
)

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
	WithContext(ctx context.Context) Logger
}

type SlogAdapter struct {
	log *slog.Logger
}

func NewSlogAdapter(log *slog.Logger) *SlogAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &SlogAdapter{log: log}
}

func (a *SlogAdapter) Debug(msg string, args ...any) { a.emit(slog.LevelDebug, msg, args...) }
func (a *SlogAdapter) Info(msg string, args ...any)  { a.emit(slog.LevelInfo, msg, args...) }
func (a *SlogAdapter) Warn(msg string, args ...any)  { a.emit(slog.LevelWarn, msg, args...) }
func (a *SlogAdapter) Error(msg string, args ...any) { a.emit(slog.LevelError, msg, args...) }

// emit builds the record with the PC of whoever called Debug/Info/Warn/Error
// — not of this file — so AddSource names the module that logged rather than
// the adapter that forwarded. Level is checked first: the caller lookup and
// the record are not paid for a line nobody will write.
func (a *SlogAdapter) emit(level slog.Level, msg string, args ...any) {
	ctx := context.Background()
	if !a.log.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	// Skip runtime.Callers, emit, and the exported method that called it.
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	_ = a.log.Handler().Handle(ctx, r)
}

func (a *SlogAdapter) With(args ...any) Logger {
	return &SlogAdapter{log: a.log.With(args...)}
}

// WithContext binds the ids the context is carrying — the trace that names
// the whole exchange and the request that named this frame — onto every
// record this logger writes from here on.
//
// Empty ids are NOT attached: a `trace=""` on every line of a process is
// noise that makes the lines that do carry one harder to find.
func (a *SlogAdapter) WithContext(ctx context.Context) Logger {
	l := a.log
	if id := TraceID(ctx); id != "" {
		l = l.With(slog.String("trace", id))
	}
	if id := RequestID(ctx); id != "" {
		l = l.With(slog.String("request", id))
	}
	return &SlogAdapter{log: l}
}

// THE CHAIN (nocx-d6gn4.8.1). A trace names one EXCHANGE end to end — a run
// and every ask, approval, effect and program that belongs to it, across the
// several JSON-RPC requests it takes. A request names one frame off the
// wire. Both travel in the context, so anything downstream that already
// receives a context can say which exchange it is part of without being told
// separately, and a resume eight seconds later logs under the same trace as
// the ask that parked it.
//
// The seam for this existed and had NO producer: WithContext read a trace id
// out of the context and nothing anywhere put one in, so every record
// carried traceID="". Reading it was free; nobody noticed it said nothing.

// WithTraceID returns a context carrying the exchange's trace id.
func WithTraceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey, id)
}

// WithRequestID returns a context carrying the id of the wire frame being
// served.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestKey, id)
}

// TraceID is the exchange this context belongs to, or empty.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceKey).(string); ok {
		return v
	}
	return ""
}

// RequestID is the wire frame this context is serving, or empty.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestKey).(string); ok {
		return v
	}
	return ""
}

type ctxKeyType struct{ name string }

var (
	traceKey   = ctxKeyType{"trace"}
	requestKey = ctxKeyType{"request"}
)

// CallPath is the chain of calls that reached the caller, innermost first,
// as "function:line < function:line …". The record's own source answers
// "where was this logged"; this answers "who asked for it", which is the
// question an event that arrives from elsewhere in the process poses —
// a cancellation, a discard, a shutdown.
//
// It walks a bounded number of frames and is meant for rare events: once per
// ended run, not once per delta.
func CallPath(skip int) string {
	var pcs [8]uintptr
	n := runtime.Callers(skip+2, pcs[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	var where []string
	for {
		f, more := frames.Next()
		where = append(where, fmt.Sprintf("%s:%d", shortFunc(f.Function), f.Line))
		if !more {
			break
		}
	}
	return strings.Join(where, " < ")
}

// shortFunc drops the module path a reader already knows, keeping
// package.Function.
func shortFunc(fn string) string {
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		return fn[i+1:]
	}
	return fn
}
