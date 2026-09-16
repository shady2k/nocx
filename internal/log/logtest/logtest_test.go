package logtest

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// New's logger is DEBUG regardless of the process's own default, and it
// still runs every record through log.From's machinery — module et al. —
// because it is placed into ctx exactly the way a composition root's
// logger is.
func TestNew_RecordsDebugAndCarriesModule(t *testing.T) {
	ctx, lg := New(t)
	if lg == nil {
		t.Fatal("New returned a nil Logger")
	}
	log.From(ctx).Debug("hello", "n", 1)

	adapter, ok := log.From(ctx).(*log.SlogAdapter)
	if !ok {
		t.Fatalf("log.From(ctx) is not a *log.SlogAdapter: %T", log.From(ctx))
	}
	h, ok := adapter.Handler().(*handler)
	if !ok {
		t.Fatalf("handler is not logtest's own: %T", adapter.Handler())
	}
	recs := h.s.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	if recs[0].Message != "hello" {
		t.Fatalf("message = %q", recs[0].Message)
	}
	if mod, ok := recs[0].attr("module"); !ok || mod != "internal/log/logtest" {
		t.Fatalf("module = %q, %v", mod, ok)
	}
}

// From(ctx) without New's own ctx (a plain test that never called New) is
// not this package's concern; New(t) itself must not panic and must return
// usable values, including for testing.TB satisfied by a *testing.T.
func TestNew_ReturnsUsableContextAndLogger(t *testing.T) {
	ctx, lg := New(t)
	if ctx == nil {
		t.Fatal("New returned a nil context")
	}
	lg.Info("no assertion needed — must not panic")
}

// WaitFor is the condition-wait this package gives a test asserting on a
// line a background goroutine writes, instead of a sleep-then-read that
// races the writer — the shape of the CI race in
// internal/helper/session/ssh_tool_endpoint_test.go this package replaces.
func TestWaitFor_ObservesARecordWrittenByAnotherGoroutine(t *testing.T) {
	ctx, _ := New(t)
	go func() {
		time.Sleep(5 * time.Millisecond)
		log.From(ctx).Warn("late line", "from", "goroutine")
	}()
	ok := WaitFor(ctx, time.Second, func(r Record) bool {
		return r.Message == "late line"
	})
	if !ok {
		t.Fatal("WaitFor did not observe the late record")
	}
}

// WaitFor must give up rather than hang forever when nothing ever matches.
func TestWaitFor_TimesOutWhenNoRecordMatches(t *testing.T) {
	ctx, _ := New(t)
	log.From(ctx).Info("unrelated")
	start := time.Now()
	ok := WaitFor(ctx, 30*time.Millisecond, func(r Record) bool { return r.Message == "never" })
	if ok {
		t.Fatal("WaitFor reported a match that was never written")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("WaitFor returned after %s, before its own timeout", elapsed)
	}
}

// WaitFor on a context New never touched answers false rather than
// panicking — the ctx carries no logger this package's handler backs.
func TestWaitFor_FalseOnAForeignContext(t *testing.T) {
	if WaitFor(context.Background(), 10*time.Millisecond, func(Record) bool { return true }) {
		t.Fatal("WaitFor matched on a context it never built")
	}
}

// Slog is New's *slog.Logger twin, for the many test doubles across
// internal/app built around that concrete type rather than log.Logger.
func TestSlog_ReturnsAWorkingLogger(t *testing.T) {
	sl := Slog(t)
	if sl == nil {
		t.Fatal("Slog returned nil")
	}
	sl.Debug("via slog", "x", 1) // must not panic
}

// The dump itself — grouping by trace, module/request_id in the heading,
// "no trace" last — is proven end to end against a REAL failing test in a
// child `go test` process (testdata/failing is a `testdata` directory, so
// `go test ./...` at the repo root never runs it itself): AC's own words,
// "runs a deliberately failing subtest in a child testing.T harness (or via
// go test of a testdata package from the test)".
func TestDump_FailingTestPrintsGroupedRecords(t *testing.T) {
	out := runTestdata(t, "TestDeliberatelyFails")
	for _, want := range []string{
		"--- FAIL: TestDeliberatelyFails",
		"logtest:",
		"-- trace ", // the deterministic trace this fixture starts under
		"module=",
		"request_id=req-1",
		`"about to fail"`,
		"step=1",
		`"something looked wrong"`,
		"code=42",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failing test's output does not contain %q:\n%s", want, out)
		}
	}
}

func TestDump_PassingTestPrintsNothingExtra(t *testing.T) {
	out := runTestdata(t, "TestPasses")
	if !strings.Contains(out, "--- PASS: TestPasses") {
		t.Fatalf("expected TestPasses to pass:\n%s", out)
	}
	for _, mustNotAppear := range []string{"logtest:", "quiet success"} {
		if strings.Contains(out, mustNotAppear) {
			t.Fatalf("a passing test printed its debug log (%q):\n%s", mustNotAppear, out)
		}
	}
}

func runTestdata(t *testing.T, name string) string {
	t.Helper()
	cmd := exec.Command("go", "test", "-run", "^"+name+"$", "-v", "./testdata/failing/") // #nosec G204 — name is always a literal this file passes, never external input
	out, _ := cmd.CombinedOutput()                                                       // the failing case's own non-zero exit is expected
	return string(out)
}
