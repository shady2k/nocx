package log

// THE LOGGER TRAVELS IN THE CONTEXT (owner decision, 2026-09-16, nocx-n14oo.9).
//
// Measured the same day: 22 of 814 log call sites in this module remembered
// to carry the exchange (WithContext / log.Start) by hand. The other 792
// compiled, passed review and logged a line with no module, no request id
// and no trace — because remembering is optional and nothing enforces it.
//
// From(ctx) is the fix: a call site that wants to log asks the context for
// its logger rather than holding one, so it cannot forget the ids — there is
// nothing to forget, they are already bound onto whatever WithLogger put in
// ctx. The composition root builds the one real logger for the process and
// calls WithLogger once, near main(); every context derived from what it
// hands down (context.WithValue, a child span, a request scope) answers
// From with a logger that already knows the trace it is part of.
//
// A global mutable *slog.Logger was the alternative and was rejected: tests
// in one test binary must not share one log buffer (see internal/log/logtest,
// nocx-n14oo.10), and a singleton is exactly the shared, mutable, ambient
// state a per-test buffer needs to not be. The context is already the seam
// every one of these 814 call sites' enclosing functions has — they take a
// ctx to make the store or network call the log line is about — so carrying
// the logger alongside it costs nothing new.
import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
)

type loggerKeyType struct{ name string }

var loggerKey = loggerKeyType{"logger"}

// WithLogger returns a context carrying lg: every log.From call beneath it —
// unless a nested call replaces it again — finds lg rather than the process
// root. A nil lg is a no-op, since a context that would make From panic is
// worse than one that falls through to the root.
func WithLogger(ctx context.Context, lg Logger) context.Context {
	if lg == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, lg)
}

// root is the fallback From returns when ctx carries no logger of its own —
// a background goroutine, a context.Background() nobody has wired yet, a
// test that has not called logtest.New. It is process-wide by design (see
// the file doc): SetRoot is meant to be called once, at the composition
// root, before any goroutine that might call From starts, and From reads it
// through an atomic.Pointer so a logger installed after other goroutines are
// already running is still visible to them without a data race.
var root atomic.Pointer[Logger]

func init() {
	var lg Logger = NewSlogAdapter(nil)
	root.Store(&lg)
}

// SetRoot installs the process's fallback logger. The composition roots
// (internal/app, cmd/nocx-server, cmd/nocx-helper) call it once, alongside
// putting the same logger in the context they hand down — the context is
// the path almost everything takes, and this is the net under it, for the
// rare caller with no context or one nobody has wired to carry a logger yet.
func SetRoot(lg Logger) {
	if lg == nil {
		return
	}
	root.Store(&lg)
}

// Root returns the process's current fallback logger.
func Root() Logger { return *root.Load() }

// From returns the logger this context carries, or the process root when it
// carries none — see WithLogger and SetRoot. Every record the returned
// logger writes carries `module` (the Go package of whoever called From,
// derived automatically) and, when ctx carries them, `trace_id`, `span_id`
// and `request_id` (WithContext, span.go). A caller's own attributes are
// untouched: From adds nothing to what it is asked to log, only to what it
// is asked to log AS.
func From(ctx context.Context) Logger {
	lg := Root()
	if ctx != nil {
		if v, ok := ctx.Value(loggerKey).(Logger); ok && v != nil {
			lg = v
		}
	}
	bound := lg.WithContext(ctx)
	if mod := callerModule(); mod != "" {
		bound = bound.With("module", mod)
	}
	return bound
}

// modulePrefix is trimmed from a call site's package path so `module` reads
// "internal/transport", not the whole module path — a reader of the log
// already knows which repository it is reading.
const modulePrefix = "github.com/shady2k/nocx/"

// callerModule names the package of From's caller. It reuses CallPath's
// frame-walking (runtime.Callers/CallersFrames) rather than hand-rolling a
// second one, skipped to land on the caller of From itself: 0 is
// runtime.Callers, 1 is callerModule, 2 is From, 3 is From's caller.
func callerModule() string {
	var pcs [1]uintptr
	n := runtime.Callers(3, pcs[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	f, _ := frames.Next()
	return packageOf(f.Function)
}

// packageOf turns a runtime function name — e.g.
// "github.com/shady2k/nocx/internal/helper/session.(*Manager).Open" — into
// its full package path, "internal/helper/session".
//
// shortFunc (span.go's neighbour in log.go) is not reused here on purpose:
// it keeps only the last path element for a human reading "who logged
// this", which collides — this module has both internal/session and
// internal/helper/session, and shortFunc's "session.Foo" cannot tell a
// reader, or the ratchet, which one wrote a line.
func packageOf(fn string) string {
	dir, lastSeg := fn, ""
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		dir, lastSeg = fn[:i], fn[i+1:]
	} else {
		lastSeg = fn
	}
	dot := strings.Index(lastSeg, ".")
	if dot < 0 {
		// No receiver/function suffix found — not a shape this should see
		// from runtime.CallersFrames, but fall back to the trimmed name
		// rather than a panic.
		return strings.TrimPrefix(fn, modulePrefix)
	}
	pkg := lastSeg[:dot]
	full := pkg
	if dir != fn {
		full = dir + "/" + pkg
	}
	return strings.TrimPrefix(full, modulePrefix)
}
