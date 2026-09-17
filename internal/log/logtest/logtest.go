// Package logtest is the debug log a failing test reads instead of guessing
// (nocx-n14oo.10).
//
// internal/app's tests used to hand production code discardLogger() — a
// bare io.Discard sink, shared by every caller, with nothing to read back —
// so a failure showed only the assertion that tripped and none of the DEBUG
// lines that led to it. logtest.New gives each test its OWN buffer, at
// DEBUG regardless of the process's build-tag default (internal/log's
// DefaultLevel), and prints it ONLY when the test that owned it failed:
// t.Cleanup checks t.Failed() and dumps every record, grouped by trace, via
// t.Log — which Go attaches to the failing test's own output, in the right
// place, with no extra plumbing.
package logtest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// Record is one line this test's logger wrote, as the dump reads it back:
// the record's own timestamp is not kept — a test dump has no wall clock
// worth reading, only the order records arrived and what they said.
type Record struct {
	Level   slog.Level
	Message string
	// Attrs includes whatever log.From(ctx) bound automatically — module,
	// trace_id, span_id, request_id — beside whatever the call site passed.
	Attrs []slog.Attr
}

func (r Record) attr(key string) (string, bool) {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value.String(), true
		}
	}
	return "", false
}

// store is the state shared by every handler derived from one test's
// logger — the original and every .With/.WithContext descendant log.From
// produces — so a record written through any of them lands in the same
// buffer and wakes the same waiters.
type store struct {
	mu      sync.Mutex
	cond    *sync.Cond
	records []Record
}

func newStore() *store {
	s := &store{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *store) append(r Record) {
	s.mu.Lock()
	s.records = append(s.records, r)
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *store) snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out
}

// waitFor blocks until a record matching match has been recorded, or
// timeout elapses. It is a condition wait, not a sleep: a caller asserting
// on a line a background goroutine writes must not guess how long that
// takes, and must not read the buffer once and hope the write already
// happened (nocx-n14oo.10, the CI race in
// internal/helper/session/ssh_tool_endpoint_test.go this replaces).
func (s *store) waitFor(timeout time.Duration, match func(Record) bool) bool {
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		for _, r := range s.records {
			if match(r) {
				return true
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		// sync.Cond has no timed wait, so a timer stands in for the
		// deadline: it wakes the same Wait a real record's Broadcast would,
		// and the loop above re-checks either way.
		timer := time.AfterFunc(remaining, func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
		s.cond.Wait()
		timer.Stop()
	}
}

// handler is the slog.Handler every logger this package hands out is built
// on. Debug is always enabled — logtest's whole point is to capture what
// the process's own level would have thrown away — and Handle appends to
// the shared store rather than writing text anywhere.
type handler struct {
	s     *store
	attrs []slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	h.s.append(Record{Level: r.Level, Message: r.Message, Attrs: attrs})
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &handler{s: h.s, attrs: merged}
}

// WithGroup is a no-op: nothing under log.From ever opens a group (it binds
// ids with With, never WithGroup), so there is nothing for this test sink to
// represent — a group here would need a key prefix this package's own
// reader does not need either.
func (h *handler) WithGroup(string) slog.Handler { return h }

// New returns a context carrying a logger private to t — DEBUG level,
// writing into a buffer only THIS test's Cleanup ever reads — and that same
// logger, for a caller that wants it directly rather than through
// log.From(ctx). If t fails, every record is printed via t.Log at cleanup,
// grouped by trace_id (records with none last); a passing test prints
// nothing extra.
func New(t testing.TB) (context.Context, log.Logger) {
	t.Helper()
	s := newStore()
	lg := log.NewSlogAdapter(slog.New(&handler{s: s}))
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, s.snapshot())
		}
	})
	return log.WithLogger(context.Background(), lg), lg
}

// Slog is New for a caller that takes a *slog.Logger rather than
// log.Logger — the shape most of internal/app's test doubles were built to,
// before this package existed to give them anything else. It shares New's
// buffer-per-test and dump-on-failure behaviour; the two are the same sink
// wearing the two types production code already has, not a second
// mechanism (AGENTS.md: "do not add a second logger type").
func Slog(t testing.TB) *slog.Logger {
	t.Helper()
	s := newStore()
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, s.snapshot())
		}
	})
	return slog.New(&handler{s: s})
}

// WaitFor blocks until the logger ctx carries (log.From) has recorded one
// record matching match, or timeout elapses. It answers false, without
// panicking, for a context this package did not build — a ctx from a plain
// context.Background(), or one whose logger came from somewhere else — so a
// caller that reaches for this on the wrong context gets "no", not a crash.
func WaitFor(ctx context.Context, timeout time.Duration, match func(Record) bool) bool {
	adapter, ok := log.From(ctx).(*log.SlogAdapter)
	if !ok {
		return false
	}
	h, ok := adapter.Handler().(*handler)
	if !ok {
		return false
	}
	return h.s.waitFor(timeout, match)
}

// WaitForSlog is WaitFor for a logger obtained from Slog rather than New —
// a test fixture built around a shared *slog.Logger (a package-level
// "standLogger" var is the shape internal/helper/session's stand uses) has
// no ctx to hand WaitFor, only the logger itself.
func WaitForSlog(sl *slog.Logger, timeout time.Duration, match func(Record) bool) bool {
	h, ok := sl.Handler().(*handler)
	if !ok {
		return false
	}
	return h.s.waitFor(timeout, match)
}

// RecordsSlog is a race-free snapshot of everything sl has recorded so far
// — the replacement for reading a shared bytes.Buffer's .String() while a
// background goroutine may still be writing into it (the CI race in
// internal/helper/session/ssh_tool_endpoint_test.go this package exists to
// close: the buffer had one writer, the logger, and one reader, the test,
// with nothing ordering the two).
func RecordsSlog(sl *slog.Logger) []Record {
	h, ok := sl.Handler().(*handler)
	if !ok {
		return nil
	}
	return h.s.snapshot()
}

// TextSlog joins RecordsSlog's snapshot into one string, one formatted line
// per record — a drop-in for the `bytes.Buffer.String()` a test asserted
// substrings against before, minus the race that came from reading the
// buffer the logger itself was still writing.
func TextSlog(sl *slog.Logger) string {
	var b strings.Builder
	for _, r := range RecordsSlog(sl) {
		b.WriteString(format(r))
		b.WriteByte('\n')
	}
	return b.String()
}

// dump prints every record via t.Log, grouped by trace_id in the order each
// trace first appeared; records carrying no trace_id print last, under
// their own heading, because "no trace" is itself a fact worth grouping
// rather than scattering through the traced ones.
func dump(t testing.TB, records []Record) {
	t.Helper()
	if len(records) == 0 {
		return
	}
	groups := map[string][]Record{}
	var order []string
	var untraced []Record
	for _, r := range records {
		tid, ok := r.attr("trace_id")
		if !ok || tid == "" {
			untraced = append(untraced, r)
			continue
		}
		if _, seen := groups[tid]; !seen {
			order = append(order, tid)
		}
		groups[tid] = append(groups[tid], r)
	}
	t.Logf("logtest: %d debug record(s) from the failed test", len(records))
	for _, tid := range order {
		t.Logf("-- trace %s --", tid)
		for _, r := range groups[tid] {
			t.Log(format(r))
		}
	}
	if len(untraced) > 0 {
		t.Logf("-- no trace --")
		for _, r := range untraced {
			t.Log(format(r))
		}
	}
}

// idAttrs are folded into the heading (module, request_id) or the grouping
// itself (trace_id, span_id, parent_span_id) rather than printed twice.
var idAttrs = map[string]bool{
	"module": true, "request_id": true,
	"trace_id": true, "span_id": true, "parent_span_id": true,
}

func format(r Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-5s", r.Level)
	if mod, ok := r.attr("module"); ok {
		fmt.Fprintf(&b, " module=%s", mod)
	}
	if rid, ok := r.attr("request_id"); ok {
		fmt.Fprintf(&b, " request_id=%s", rid)
	}
	fmt.Fprintf(&b, " %q", r.Message)
	for _, a := range r.Attrs {
		if idAttrs[a.Key] {
			continue
		}
		fmt.Fprintf(&b, " %s=%s", a.Key, a.Value.String())
	}
	return b.String()
}
