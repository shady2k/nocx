package sessionruntime

// The ingest window's speed budget (nocx-zg3k3.5.8), restored by the round-2
// review of nocx-zg3k3.5.9. The stage's criterion is about the INGEST hot
// path — taking a program's output in through the real emulator and
// capturing its departed rows — which carries ~99% of the whole path's cost
// (8.4M allocs/MiB against the pump's 53k), and which the pump-only gate in
// internal/helper/session cannot see. This file gates THAT window; the
// pump's own window stays gated where the pump lives
// (internal/helper/session/rows_budget_test.go). Two windows, one
// implementation each: this one drives the production code and stops at the
// rows the RowStream hands out — the stream is a counter, not a second
// encoding, so nothing here re-implements work the shipped code does
// elsewhere.
//
// The path measured is the production one, over the real emulator
// (libghostty-vt behind its port) and the real runtime, with no PTY: the
// carrier's bytes arrive through [Session.Ingest] in [MaxIngestBytes]
// chunks, the emulator departs rows off the live rectangle, and the drain
// hands each batch to this session's [RowStream] — all synchronously inside
// the feed, so the MemStats window opens before it and closes after.
//
// The feed is fixed: about four MiB of numbered, styled, full-width lines
// at 120x40 — the same shape the real chain test floods with
// (internal/transport/ws_block_rows_bounds_test.go's styledFloodCommand),
// one column short of wrap so every line stays one row. No fence rides the
// feed: the budget is the cost of TAKING OUTPUT IN AND CAPTURING ITS
// DEPARTED ROWS, not the once-per-command rendezvous of sealing one.
//
// What gates and what only reports is the owner's decision of 2026-09-29
// (run preflight, point 5, on nocx-zg3k3.5): the budget GATES on
// allocations and bytes allocated per MiB fed — numbers that do not depend
// on the machine — and THROUGHPUT IS MEASURED AND REPORTED, NEVER GATED
// (AGENTS.md: a test may not depend on timing; CI machines differ from this
// one). [TestIngestCaptureStaysWithinItsBudget] holds the gate;
// [BenchmarkIngestCapture] reports ns/op, MiB/s, allocs/MiB and B/MiB for
// the next baseline re-measurement.

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// ingestFeedBytes is how much output one feed carries: a few MiB, large
// enough that per-call constants amortize away and small enough that the
// whole package suite stays quick with the budget test in it.
const ingestFeedBytes = 4 << 20

// ingestGeometry is 120x40, the geometry this bead names.
func ingestGeometry() Geometry {
	return harnessGeometry(120, 40)
}

// ingestFeed builds the fixed feed once per process: numbered, styled,
// full-width lines at 120 columns (119 to stay one short of wrap), a
// truecolour foreground over the whole line the way a program's colourised
// output arrives. Built outside the timed regions; a lazily-built package
// value, so the rest of the suite pays nothing for it.
var ingestFeed = sync.OnceValue(func() []byte {
	var b strings.Builder
	for i := 0; b.Len() < ingestFeedBytes; i++ {
		// rNNNNNN + padding to column 119, all under one SGR colour; the
		// padding spaces carry the style, so nothing trims them and every
		// row is genuinely full-width on the wire.
		fmt.Fprintf(&b, "\x1b[38;2;90;64;200mr%06d%-111s\x1b[0m\r\n", i, "")
	}
	return []byte(b.String())
})

// ingestCounter is the [RowStream] the benchmark and the budget test bind:
// a measurement seam at the exact boundary where the runtime hands departed
// rows over, and nothing more. It counts what arrived (rows, losses,
// batches) so the gate can tell the feed was captured whole, and allocates
// nothing per batch — an encoding here would charge the ingest window for
// work this window does not own (the pump's, gated separately).
type ingestCounter struct {
	mu    sync.Mutex
	rows  uint64
	lost  uint64
	batch int
}

func (c *ingestCounter) OutputRows(from uint64, rows []emulator.Row, lost uint64) {
	c.mu.Lock()
	c.rows += uint64(len(rows)) //nolint:gosec // a row count
	c.lost += lost
	c.batch++
	c.mu.Unlock()
}

func (c *ingestCounter) IntervalEnd(nonce FenceNonce, endRow uint64, closing []emulator.Row, settledWithoutFence bool) {
}

func (c *ingestCounter) ClearBoundary() {}

func (c *ingestCounter) snapshot() (rows, lost uint64, batch int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows, c.lost, c.batch
}

// newIngestWindowSession builds the real chain over a fresh emulator: the
// real ghostty port at 120x40 and the real runtime over it, with the
// counting stream bound as the row stream. The returned stop closes the
// emulator; the harness terminal and the direct reply sink are this
// package's own test instruments, and the feed asks the program nothing, so
// the reply sink is never written.
func newIngestWindowSession() (*Session, *ingestCounter, func(), error) {
	g := ingestGeometry()
	term := newHarnessTerminal(g)
	screen, err := ghostty.New(g)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sessionruntime: build the emulator: %w", err)
	}
	s, err := New(Config{
		Incarnation:  Incarnation{Session: "ingest-budget", Generation: 1},
		Geometry:     g,
		Terminal:     term,
		Emulator:     screen,
		Completeness: CompletenessComplete,
		Replies:      directReplySink{term: term},
	})
	if err != nil {
		screen.Close()
		return nil, nil, nil, fmt.Errorf("sessionruntime: build the runtime: %w", err)
	}
	counter := &ingestCounter{}
	s.SetRowStream(counter)
	return s, counter, screen.Close, nil
}

// feedIngestWindow feeds the whole fixed feed through the session the way
// the carrier hands bytes: chunks of at most MaxIngestBytes. The row stream
// is handed every departed batch synchronously inside these calls, so the
// caller's MemStats window contains the whole ingest-and-capture cost.
func feedIngestWindow(s *Session) error {
	feed := ingestFeed()
	for len(feed) > 0 {
		n := min(len(feed), MaxIngestBytes)
		if err := s.Ingest(feed[:n]); err != nil {
			return fmt.Errorf("sessionruntime: ingest a %d-byte chunk: %w", n, err)
		}
		feed = feed[n:]
	}
	return nil
}

// BenchmarkIngestCapture measures the ingest window — the carrier's bytes
// through the real emulator, the departure drain, the capture of the
// departed rows and the hand-off to the row stream — per four-MiB feed, and
// reports it per MiB of output fed: ns/op (the framework's own), MiB/s,
// allocs/MiB and B/MiB.
//
// Throughput here is a REPORT, never a gate: this machine is not CI, and
// AGENTS.md forbids a test that depends on timing. The gate lives in
// TestIngestCaptureStaysWithinItsBudget, over the machine-independent
// numbers.
//
// Re-measuring a baseline (then re-deriving the budget constants below):
//
//	go test -tags gtk3 -run '^$' -bench BenchmarkIngestCapture \
//		-benchtime 10x ./internal/sessionruntime
func BenchmarkIngestCapture(b *testing.B) {
	feed := ingestFeed()
	mibPerOp := float64(len(feed)) / (1 << 20)
	var mallocs, bytesAlloc uint64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, counter, stop, err := newIngestWindowSession()
		if err != nil {
			b.Fatal(err)
		}
		// The setup and the counters' before-reading sit outside the
		// timed region; only the feed itself is measured. MemStats counts
		// every goroutine, and the runtime spawns none of its own, so the
		// delta is the feed's.
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b.StartTimer()
		feedErr := feedIngestWindow(s)
		b.StopTimer()
		runtime.ReadMemStats(&after)
		stop()
		if feedErr != nil {
			b.Fatal(feedErr)
		}
		if _, _, batch := counter.snapshot(); batch == 0 {
			b.Fatal("the feed captured no departed batches: the window measured nothing")
		}
		mallocs += after.Mallocs - before.Mallocs
		bytesAlloc += after.TotalAlloc - before.TotalAlloc
	}
	b.ReportMetric(float64(mallocs)/float64(b.N)/mibPerOp, "allocs/MiB")
	b.ReportMetric(float64(bytesAlloc)/float64(b.N)/mibPerOp, "B/MiB")
	if elapsed := b.Elapsed(); elapsed > 0 {
		b.ReportMetric(mibPerOp*float64(b.N)/elapsed.Seconds(), "MiB/s")
	}
}

// The budget: the measured baseline on this tree plus a stated margin, held
// by TestIngestCaptureStaysWithinItsBudget. Both numbers came off THIS tree
// with BenchmarkIngestCapture on 2026-09-29, on the x86-64 Linux box this
// worktree runs on (AMD Ryzen 5 8600G), with:
//
//	go test -tags gtk3 -run '^$' -bench BenchmarkIngestCapture \
//		-benchtime 10x ./internal/sessionruntime
//
// The margin is 25%, the brief's stated figure. A change that crosses
// either number has put a new allocation on the ingest-and-capture path —
// the regression the budget exists to catch — and the fix is to remove it,
// not to raise the budget: raising one re-measures the baseline with the
// benchmark, says in a bead why the new shape is right, and dates the new
// numbers the same way these are dated.
const (
	// baseline 2026-09-29: 8,368,759 allocs/MiB and 219,635,855 B/MiB, so
	// each gate is that number plus 25%, rounded up. Measured over the
	// ingest window only — the real emulator's take-in and the capture of
	// its departed rows, ending at the RowStream hand-off.
	budgetAllocsPerMiB = 10_470_000 // baseline 8,368,759 allocs/MiB, +25%
	budgetBytesPerMiB  = 275_000_000
)

// TestIngestCaptureStaysWithinItsBudget runs the same feed the benchmark
// does, once, and FAILS when allocations or bytes allocated per MiB fed
// exceed the budget above. The numbers are allocation counts, not timings —
// they do not depend on this machine's speed, so the check is legitimate
// everywhere the suite runs (the owner's decision of 2026-09-29, run
// preflight point 5). Throughput is deliberately NOT asserted.
func TestIngestCaptureStaysWithinItsBudget(t *testing.T) {
	if raceDetector {
		t.Skip("allocation budget not measured under -race: the race detector instruments and adds allocations, " +
			"so this run would measure the detector, not the shipped path; `make test-alloc-budgets` runs it without -race")
	}
	// One warm-up feed on its own session, so the measured feed pays only
	// the path's own costs and not the process's one-time charges (the
	// emulator library's init, the JSON encoder's type caches).
	warm, warmCounter, stopWarm, err := newIngestWindowSession()
	if err != nil {
		t.Fatal(err)
	}
	if feedErr := feedIngestWindow(warm); feedErr != nil {
		stopWarm()
		t.Fatal(feedErr)
	}
	stopWarm()
	if _, _, batch := warmCounter.snapshot(); batch == 0 {
		t.Fatal("the warm-up feed captured no departed batches: the window measured nothing")
	}

	s, counter, stop, err := newIngestWindowSession()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = feedIngestWindow(s)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	rows, lost, batch := counter.snapshot()
	if batch == 0 {
		t.Fatal("the feed captured no departed batches: the window measured nothing")
	}
	if lost != 0 {
		t.Fatalf("the capture stated %d losses: the window measured a loss, not the path", lost)
	}

	mib := float64(len(ingestFeed())) / (1 << 20)
	allocsPerMiB := float64(after.Mallocs-before.Mallocs) / mib
	bytesPerMiB := float64(after.TotalAlloc-before.TotalAlloc) / mib
	t.Logf("fed %.2f MiB: %.0f allocs/MiB, %.0f B/MiB, %d batches, %d rows captured",
		mib, allocsPerMiB, bytesPerMiB, batch, rows)

	if allocsPerMiB > budgetAllocsPerMiB {
		t.Fatalf("the ingest-and-capture path spent %.0f allocs/MiB, over the budget of %d (+25%% over the %s baseline): a new allocation has landed on the path",
			allocsPerMiB, budgetAllocsPerMiB, "2026-09-29")
	}
	if bytesPerMiB > budgetBytesPerMiB {
		t.Fatalf("the ingest-and-capture path allocated %.0f B/MiB, over the budget of %d (+25%% over the %s baseline): a new copy has landed on the path",
			bytesPerMiB, budgetBytesPerMiB, "2026-09-29")
	}
}
