package log

// THE CHAIN, IN THE ONE SPELLING SOMETHING ELSE CAN READ (nocx-4l2a5.1).
//
// A trace names one exchange end to end. A span names one frame of it, and its
// parent names the frame that asked for that one. Together they answer the
// question a log file could not answer here on 2026-09-09: an error came back
// from workers.spawn, thirty seconds of silence preceded it, and nothing on
// either side of the process boundary shared an identifier.
//
// The ids are the W3C Trace Context spelling — 32 and 16 lowercase hex
// characters, and the `traceparent` header — because the decision recorded in
// nocx-4l2a5 is to take the SPEC's identifiers without taking the
// OpenTelemetry SDK. There is no collector on a local-first desktop machine
// and no process to export a span to; what there is, is a log file that has to
// be joinable. Taking the format now is what makes an exporter later a
// question of where the ids go rather than of rewriting every call site.
//
// Nothing here allocates a span object with a lifetime. A span is four
// strings, it travels in the context like everything else that is scoped to a
// request, and it is written into records by WithContext.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// zeroTrace and zeroSpan are the values the spec reserves as "no id". They are
// not merely unlikely from crypto/rand — they are INVALID, and a reader is
// entitled to treat one as an absent id, so nothing here may ever emit one.
const (
	zeroTrace = "00000000000000000000000000000000"
	zeroSpan  = "0000000000000000"

	// traceparentVersion is the only version this writes and the only one it
	// accepts. The spec's forward-compatibility rule says a higher version
	// may be parsed for its first fields; we do not, because nothing here
	// talks to a stranger's tracer — both ends of every boundary we cross are
	// this codebase.
	traceparentVersion = "00"

	sampledFlag   = "01"
	unsampledFlag = "00"
)

// Span is one frame of one exchange.
//
// It is a value, comparable and copyable, because it is an IDENTITY and not a
// resource: there is nothing to close, and two copies of a span are the same
// span. That is what lets it sit in a context and be read back by any module
// downstream without a lock or a lifetime to reason about.
type Span struct {
	// TraceID names the whole exchange and never changes once opened.
	TraceID string
	// SpanID names this frame.
	SpanID string
	// ParentSpanID names the frame that asked for this one, and is empty on
	// a root — the frame where the exchange began.
	ParentSpanID string
	// Sampled is the spec's one flag. Everything is sampled here: there is
	// no exporter to spare and no volume to shed, so a false value would be
	// a claim nothing makes. It is carried because the header has the field
	// and a reader may act on it.
	Sampled bool
}

// Valid reports whether this span may be written down. An id of the wrong
// shape, or the reserved zero, is not a span — it is the absence of one, and
// emitting it would put a value into a log that a reader must then know to
// ignore.
func (s Span) Valid() bool {
	return validTraceID(s.TraceID) && validSpanID(s.SpanID)
}

// Traceparent is the header spelling, for the one place a span has to leave
// this process. An invalid span formats to the empty string rather than to a
// header a far side would then have to reject.
func (s Span) Traceparent() string {
	if !s.Valid() {
		return ""
	}
	flags := unsampledFlag
	if s.Sampled {
		flags = sampledFlag
	}
	return traceparentVersion + "-" + s.TraceID + "-" + s.SpanID + "-" + flags
}

// NewTraceID mints an id for a new exchange.
func NewTraceID() string { return randomHex(16, zeroTrace) }

// NewSpanID mints an id for a new frame.
func NewSpanID() string { return randomHex(8, zeroSpan) }

// randomHex returns n random bytes as lowercase hex, never the reserved
// value. crypto/rand.Read is documented never to return a short read without
// an error, and on this platform it does not fail; a value that somehow came
// back reserved is re-minted rather than emitted, because the ONE property
// every consumer relies on is that a printed id is a real one.
func randomHex(n int, reserved string) string {
	b := make([]byte, n)
	for {
		if _, err := rand.Read(b); err != nil {
			// There is no honest id to return and no caller that could act
			// on an error here, so fall back to something unique-enough and
			// well-formed: the clock. It is worse than random and it is
			// still joinable, which is the whole job.
			return clockHex(n, reserved)
		}
		if s := hex.EncodeToString(b); s != reserved {
			return s
		}
	}
}

// clockHex is the last resort behind randomHex: a monotonic-ish id derived
// from the wall clock, hashed so it does not leak the time it was minted.
func clockHex(n int, reserved string) string {
	sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
	s := hex.EncodeToString(sum[:n])
	if s == reserved {
		s = strings.Repeat("1", n*2)
	}
	return s
}

// DeterministicTraceID derives a trace id from a name an exchange already has.
//
// It exists for the exchange whose identity is NOT ours to mint: an assistant
// run is one exchange spread over several JSON-RPC frames, its run id already
// names it, and there is nowhere to keep a minted trace between the frames.
// Hashing the name gives every frame the same trace without storing anything,
// and the hash is what keeps the id the spec's shape rather than "run-349".
//
// The prefix is a domain separator: two subsystems that both number from 1
// must not collide, and adding one here costs nothing.
func DeterministicTraceID(seed string) string {
	sum := sha256.Sum256([]byte("nocx/trace:" + seed))
	id := hex.EncodeToString(sum[:16])
	if id == zeroTrace {
		return strings.Repeat("1", 32)
	}
	return id
}

// StartSpan opens a frame and returns the context that carries it.
//
// With a span already in the context this opens a CHILD of it: same trace, new
// span, parent set to what was there. With none it opens a root, which is what
// an entry point does — a frame off the wire, a socket request, a run being
// created.
//
// It takes no name. The span is an IDENTITY; what the frame is doing is a fact
// about the operation, and a log line already has room for it — Start binds it
// as `op`. Keeping it out of the span is what stops two spellings of the same
// name existing, one in the record and one in the context.
func StartSpan(ctx context.Context) (context.Context, Span) {
	parent := SpanFrom(ctx)
	span := Span{SpanID: NewSpanID(), Sampled: true}
	if parent.Valid() {
		span.TraceID = parent.TraceID
		span.ParentSpanID = parent.SpanID
	} else {
		span.TraceID = NewTraceID()
	}
	return ContextWithSpan(ctx, span), span
}

// StartTrace opens a root frame under a trace id the caller already has, for
// an exchange whose identity was decided elsewhere — see DeterministicTraceID.
func StartTrace(ctx context.Context, traceID string) (context.Context, Span) {
	if !validTraceID(traceID) {
		return StartSpan(ctx)
	}
	parent := SpanFrom(ctx)
	span := Span{TraceID: traceID, SpanID: NewSpanID(), Sampled: true}
	if parent.Valid() && parent.TraceID == traceID {
		span.ParentSpanID = parent.SpanID
	}
	return ContextWithSpan(ctx, span), span
}

// ContinueTrace adopts a traceparent a caller sent us, so the frame we open
// next is a child of theirs.
//
// It records the far side's span as the context's current span WITHOUT
// claiming to be it: the next StartSpan makes a child, which is exactly the
// relationship. A header that is absent or malformed leaves the context
// untouched — a caller that sent nothing still gets served, under a trace of
// its own, because refusing a request for want of telemetry would be the
// observability trading away the thing it observes.
func ContinueTrace(ctx context.Context, traceparent string) context.Context {
	remote, ok := ParseTraceparent(traceparent)
	if !ok {
		return ctx
	}
	return ContextWithSpan(ctx, remote)
}

// ParseTraceparent reads the header. ok=false means "there was no span here",
// never "this request is bad".
func ParseTraceparent(header string) (Span, bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 {
		return Span{}, false
	}
	version, traceID, spanID, flags := parts[0], parts[1], parts[2], parts[3]
	if version != traceparentVersion {
		return Span{}, false
	}
	if !validTraceID(traceID) || !validSpanID(spanID) {
		return Span{}, false
	}
	if len(flags) != 2 || !lowerHex(flags) {
		return Span{}, false
	}
	// Bit 0 of the flags byte is "sampled"; the rest are reserved and are
	// deliberately not interpreted.
	raw, err := hex.DecodeString(flags)
	if err != nil {
		return Span{}, false
	}
	return Span{TraceID: traceID, SpanID: spanID, Sampled: raw[0]&1 == 1}, true
}

// ContextWithSpan carries a span. An invalid span is not carried: a context
// that would answer SpanFrom with something unprintable is worse than one that
// answers with nothing.
func ContextWithSpan(ctx context.Context, s Span) context.Context {
	if ctx == nil || !s.Valid() {
		return ctx
	}
	return context.WithValue(ctx, spanKey, s)
}

// SpanFrom is the span this context is inside, or the zero Span.
func SpanFrom(ctx context.Context) Span {
	if ctx == nil {
		return Span{}
	}
	if s, ok := ctx.Value(spanKey).(Span); ok {
		return s
	}
	return Span{}
}

// Start instruments one call: it opens a span, says the call began and with
// what, and returns the function that says how it ended.
//
// This is the shape the failure of 2026-09-09 needed. Between "worker
// participant spawned" and "enrolment never arrived" there were thirty seconds
// and no lines at all, so the log could say a step had failed and never which
// step was waiting or for how long. An entry, an exit, a duration and an
// outcome are the four facts that turn that silence into a reading.
//
// The start and end lines are DEBUG. They are per-call and a release build
// does not want them; a dev build has them without asking, which is the other
// half of this epic. What is not debug is the outcome of a call that FAILED —
// that goes out at warn, because a failure nobody configured a level for is a
// failure nobody sees.
//
// Usage, with a named error return so the deferred call reports the real one:
//
//	func (s *spawner) Spawn(ctx context.Context, ...) (_ Spawned, err error) {
//	    ctx, lg, end := log.Start(ctx, s.log, "workers.spawn", "participant", id)
//	    defer func() { end(err) }()
func Start(ctx context.Context, lg Logger, name string, args ...any) (context.Context, Logger, func(error)) {
	if lg == nil {
		lg = NewSlogAdapter(nil)
	}
	ctx, _ = StartSpan(ctx)
	bound := lg.WithContext(ctx).With("op", name)
	bound.Debug(name+": start", args...)
	started := time.Now()
	return ctx, bound, func(err error) {
		ms := time.Since(started).Milliseconds()
		if err != nil {
			bound.Warn(name+": failed", "duration_ms", ms, "error", err)
			return
		}
		bound.Debug(name+": ok", "duration_ms", ms)
	}
}

type spanKeyType struct{ name string }

var spanKey = spanKeyType{"span"}

func validTraceID(s string) bool { return len(s) == 32 && lowerHex(s) && s != zeroTrace }
func validSpanID(s string) bool  { return len(s) == 16 && lowerHex(s) && s != zeroSpan }

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}
