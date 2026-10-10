package session

// The ingest hot path's speed budget (nocx-zg3k3.5.8), gated on the SHIPPED
// pump (nocx-zg3k3.5.9, stage-review finding 2). The gate this replaces
// lived in internal/sessionruntime over a test RowStream that re-implemented
// the helper's split/encode loop, so a regression in this package's
// deliverRowEmission — the code production actually runs — left the gate
// unchanged. It also measured the WHOLE path at once, where the emulator's
// ingest dwarfs the pump's work: the pump's own share of that total is a
// fraction of a percent, so a pump-side regression could hide inside the
// margin forever. Here the budget holds the pump's OWN window: the real
// ghostty port at 120x40 and the real runtime take the feed in (the
// production ingest half, outside the window), the session's own rowBridge
// queues every batch, and the sink's first send parks the pump until the
// window opens — so the measured region is exactly the shipped
// serveRows → deliverRowEmission work: the rowsPerFrame split, EncodeRows,
// the rows-document marshal and the subscriber fan-out, per MiB of output
// fed. Only the socket is absent: the sink counts what a wire writer would
// write, and writes nothing.
//
// The feed is fixed: about one MiB of numbered, styled, full-width lines at
// 120x40 — the same shape internal/transport's bounds test floods with, one
// column short of wrap so every line stays one row. No fence rides the feed:
// the budget is the cost of STREAMING DEPARTED ROWS OUT, not the
// once-per-command rendezvous of sealing one.
//
// What gates and what only reports is the owner's decision of 2026-09-29
// (run preflight, point 5, on nocx-zg3k3.5): the budget GATES on allocations
// and bytes allocated per MiB fed — numbers that do not depend on the
// machine — and THROUGHPUT IS MEASURED AND REPORTED, NEVER GATED (AGENTS.md:
// a test may not depend on timing; CI machines differ from this one).
// TestPumpEncodingStaysWithinItsBudget holds the gate;
// BenchmarkPumpEncoding reports allocs/MiB and B/MiB for the next baseline
// re-measurement, and the whole-path wall clock as a report only.

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// budgetFeedBytes is how much output one feed carries: a few MiB would do,
// but the budget holds the PUMP's window, whose mass scales with the rows —
// one MiB is far past the point where per-call constants amortize away and
// keeps the stand's queue (the whole feed's rows held while the sink is
// parked) a few tens of MiB.
const budgetFeedBytes = 1 << 20

// budgetFeed builds the fixed feed once per process: numbered, styled,
// full-width lines at 120 columns (119 to stay one short of wrap), a
// truecolour foreground over the whole line the way a program's colourised
// output arrives. Built outside the timed regions; a lazily-built package
// value, so the rest of the suite pays nothing for it.
var budgetFeed = sync.OnceValue(func() []byte {
	var b strings.Builder
	for i := 0; b.Len() < budgetFeedBytes; i++ {
		// rNNNNNN + padding to column 119, all under one SGR colour; the
		// padding spaces carry the style, so nothing trims them and every
		// row is genuinely full-width on the wire.
		fmt.Fprintf(&b, "\x1b[38;2;90;64;200mr%06d%-111s\x1b[0m\r\n", i, "")
	}
	return []byte(b.String())
})

// budgetSink is the pump's sink: a counter, not a decoder. Production's own
// sink (internal/helper/host) writes the payload's bytes to the wire and
// decodes nothing, so this one counts frames and payload bytes and decodes
// nothing either — a decode here would charge the measured path a cost
// production never pays. Its first rows send PARKS until the test opens the
// gate, which is what holds the whole feed's pump work inside the measured
// window: while the sink is parked the pump cannot drain, so everything the
// feed produced waits in the bridge's queue.
type budgetSink struct {
	gate         chan struct{}
	gateOnce     sync.Once
	mu           sync.Mutex
	frames       int
	payloadBytes int64
	// incomplete counts the bridge's own overflow markers: anything above
	// zero means the stand lost rows and measured a truncated stream.
	incomplete int
}

func newBudgetSink() *budgetSink {
	return &budgetSink{gate: make(chan struct{})}
}

// openGate releases the parked pump. Idempotent, so the warm-up and the
// teardown can call it unconditionally.
func (s *budgetSink) openGate() {
	s.gateOnce.Do(func() { close(s.gate) })
}

func (s *budgetSink) SendSessionData(proto.SessionFrame) error   { return nil }
func (s *budgetSink) SendLifecycleData(proto.SessionFrame) error { return nil }
func (s *budgetSink) SendNotification(proto.Notification) error  { return nil }
func (s *budgetSink) SendScreenFrame(proto.ScreenDataFrame) error {
	return nil
}

func (s *budgetSink) SendOutputRows(f proto.OutputRowsFrame) error {
	<-s.gate
	s.mu.Lock()
	s.frames++
	s.payloadBytes += int64(len(f.Payload)) //nolint:gosec // len is never negative
	if bytes.Contains(f.Payload, []byte(`"incomplete":true`)) {
		// The bridge's overflow marker: rows the stand LOST while the sink
		// was parked. A byte scan, not a decode — no allocation on the
		// measured path — and a gate that fails loudly instead of
		// measuring a truncated stream (the row buffer is sized to make
		// this unreachable; this proves it).
		s.incomplete++
	}
	s.mu.Unlock()
	return nil
}

func (s *budgetSink) SendIntervalEnd(proto.IntervalEndFrame) error     { return nil }
func (s *budgetSink) SendClearBoundary(proto.ClearBoundaryFrame) error { return nil }
func (s *budgetSink) SendEffectFrame(proto.EffectFrame) error          { return nil }

// budgetRawID is the fixed wire identity the stand's subscriber carries —
// mintRaw's bytes, without the testing.T a benchmark has none of.
var budgetRawID = func() (raw [16]byte) {
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return raw
}()

// buildBudgetPumpSession builds the production chain over a fresh emulator:
// the real ghostty port at 120x40, the real runtime over it, and the
// session's own rowBridge bound as its row stream, with the pump running and
// one bound subscriber whose sink is the parked counting double. The row
// buffer is far larger than the whole feed's row content, so the stand never
// measures the bridge's overflow path — the person's setting bounds
// production, not this test. The returned stop ends the pump, opens the
// sink's gate and closes the emulator.
func buildBudgetPumpSession(logger *slog.Logger) (*hostSession, *sessionruntime.Session, *budgetSink, func(), error) {
	proc := newRawReaderFakeProcess()
	rt, screen, err := newSessionRuntime(defaultScreen, proc, "00000000000000000000000000000000", 120, 40, 0, 0)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("session: build the runtime: %w", err)
	}
	hs := &hostSession{
		id:      proto.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"},
		raw:     budgetRawID,
		proc:    proc,
		runtime: rt,
		log:     logger,
		now:     time.Now,
		subs:    make(map[proto.SubscriberID]*subscriber),
		rowWake: make(chan struct{}, 1),
		// Larger than both simultaneous owners of the whole feed (FIFO and
		// retained resend window): pool capacity must not truncate the
		// pump work under measurement.
		rowBufferBytes: 128 << 20,
		rowsDone:       make(chan struct{}),
	}
	sink := newBudgetSink()
	hs.subs["coord-1"] = &subscriber{id: "coord-1", raw: budgetRawID, sink: sink}
	rt.SetRowStream(&rowBridge{hs: hs})
	go hs.serveRows()
	stop := func() {
		sink.openGate()
		close(hs.rowsDone)
		screen.Close()
	}
	return hs, rt, sink, stop, nil
}

// feedBudgetPump feeds the whole fixed feed through the runtime the way the
// carrier hands bytes: chunks of at most MaxIngestBytes. The sink's parked
// first send holds the pump off while this runs, so the feed's whole pump
// work waits in the bridge's queue when it returns.
func feedBudgetPump(rt *sessionruntime.Session) error {
	feed := budgetFeed()
	for len(feed) > 0 {
		n := min(len(feed), sessionruntime.MaxIngestBytes)
		if err := rt.Ingest(feed[:n]); err != nil {
			return fmt.Errorf("session: ingest a %d-byte chunk: %w", n, err)
		}
		feed = feed[n:]
	}
	return nil
}

// measureBudgetPumpWindow is the gate's own measurement: with the feed fully
// queued and the pump parked on the sink, it opens the gate, waits for the
// pump's own statement that its queue reached empty with nothing owed
// (requestRowsDrain arms exactly that one-shot and wakes the pump so it does
// not wait for some other event to notice), and answers the allocations the
// pump spent per MiB fed. The wait allocates nothing: the window contains
// the pump's work and nothing else's.
func measureBudgetPumpWindow(hs *hostSession, sink *budgetSink) (allocsPerMiB, bytesPerMiB float64, frames int, payload int64, incomplete int) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	sink.openGate()
	<-hs.requestRowsDrain()
	runtime.ReadMemStats(&after)
	sink.mu.Lock()
	frames, payload, incomplete = sink.frames, sink.payloadBytes, sink.incomplete
	sink.mu.Unlock()
	mib := float64(len(budgetFeed())) / (1 << 20)
	return float64(after.Mallocs-before.Mallocs) / mib,
		float64(after.TotalAlloc-before.TotalAlloc) / mib, frames, payload, incomplete
}

// BenchmarkPumpEncoding measures the shipped pump's own window — the row
// bridge's queue, the split, EncodeRows, the rows-document marshal and the
// subscriber fan-out — per four-MiB-equivalent of output fed: allocs/MiB and
// B/MiB (the numbers the gate holds), plus the whole iteration's ns/op and
// MiB/s as a REPORT that is never gated (the ingest half shares that wall
// clock, and AGENTS.md forbids gating on timing).
//
// Re-measuring a baseline (then re-deriving the budget constants below):
//
//	go test -tags gtk3 -run '^$' -bench BenchmarkPumpEncoding \
//		-benchtime 10x ./internal/helper/session
func BenchmarkPumpEncoding(b *testing.B) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	var allocs, bytes, mallocs, bytesAlloc float64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		hs, rt, sink, stop, err := buildBudgetPumpSession(quiet)
		if err != nil {
			b.Fatal(err)
		}
		if feedErr := feedBudgetPump(rt); feedErr != nil {
			stop()
			b.Fatal(feedErr)
		}
		b.StartTimer()
		a, by, frames, _, incomp := measureBudgetPumpWindow(hs, sink)
		b.StopTimer()
		if incomp != 0 {
			b.Fatalf("the stand's row buffer overflowed (%d incomplete markers): the benchmark measured a truncated stream", incomp)
		}
		stop()
		if frames == 0 {
			b.Fatal("the feed produced no frames: the pump measured nothing")
		}
		allocs += a
		bytes += by
		mallocs += a
		bytesAlloc += by
	}
	b.ReportMetric(allocs/float64(b.N), "allocs/MiB")
	b.ReportMetric(bytes/float64(b.N), "B/MiB")
	if elapsed := b.Elapsed(); elapsed > 0 {
		mibPerOp := float64(len(budgetFeed())) / (1 << 20)
		b.ReportMetric(mibPerOp*float64(b.N)/elapsed.Seconds(), "MiB/s")
	}
}

// The budget: the measured baseline on this tree plus a stated margin, held
// by TestPumpEncodingStaysWithinItsBudget. Both numbers came off THIS tree
// with BenchmarkPumpEncoding on 2026-09-29, on the x86-64 Linux box this
// worktree runs on (AMD Ryzen 5 8600G), with:
//
//	go test -tags gtk3 -run '^$' -bench BenchmarkPumpEncoding \
//		-benchtime 10x ./internal/helper/session
//
// The margin is 25%, the brief's stated figure. A change that crosses either
// number has put a new allocation on the shipped pump's path — the
// regression the budget exists to catch — and the fix is to remove it, not
// to raise the budget: raising one re-measures the baseline with the
// benchmark, says in a bead why the new shape is right, and dates the new
// numbers the same way these are dated.
const (
	// baseline 2026-09-29: 52,879 allocs/MiB and 5,568,257 B/MiB, so each
	// gate is that number plus 25%, rounded up. Measured in the pump's own
	// window — the bridge queue, the split, EncodeRows, the document
	// marshal and the fan-out, with the ingest half parked outside it.
	budgetAllocsPerMiB = 67_000 // baseline 52,879 allocs/MiB, +25%
	budgetBytesPerMiB  = 7_000_000
)

// TestPumpEncodingStaysWithinItsBudget runs the same feed the benchmark
// does, once, and FAILS when allocations or bytes allocated per MiB fed
// exceed the budget above. The numbers are allocation counts, not timings —
// they do not depend on this machine's speed, so the check is legitimate
// everywhere the suite runs (the owner's decision of 2026-09-29, run
// preflight point 5). Throughput is deliberately NOT asserted.
func TestPumpEncodingStaysWithinItsBudget(t *testing.T) {
	if raceDetector {
		t.Skip("allocation budget not measured under -race: the race detector instruments and adds allocations, " +
			"so this run would measure the detector, not the shipped path; `make test-alloc-budgets` runs it without -race")
	}
	// One warm-up feed on its own session, so the measured feed pays only
	// the path's own costs and not the process's one-time charges (the
	// emulator library's init, the JSON encoder's type caches, the pump
	// goroutine's first schedule).
	warmHS, warmRT, warmSink, stopWarm, err := buildBudgetPumpSession(logtest.Slog(t))
	if err != nil {
		t.Fatal(err)
	}
	if feedErr := feedBudgetPump(warmRT); feedErr != nil {
		stopWarm()
		t.Fatal(feedErr)
	}
	allocs, _, warmFrames, _, warmIncomp := measureBudgetPumpWindow(warmHS, warmSink)
	stopWarm()
	if warmIncomp != 0 {
		t.Fatalf("the warm-up stand's row buffer overflowed (%d incomplete markers)", warmIncomp)
	}
	t.Logf("warm-up: %.0f allocs/MiB over %d frames", allocs, warmFrames)
	if warmFrames == 0 {
		t.Fatal("the warm-up feed produced no frames: the pump measured nothing")
	}

	hs, rt, sink, stop, err := buildBudgetPumpSession(logtest.Slog(t))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if feedErr := feedBudgetPump(rt); feedErr != nil {
		t.Fatal(feedErr)
	}
	allocsPerMiB, bytesPerMiB, frames, streamed, incomp := measureBudgetPumpWindow(hs, sink)
	if frames == 0 {
		t.Fatal("the feed produced no frames: the pump measured nothing")
	}
	if incomp != 0 {
		t.Fatalf("the stand's row buffer overflowed (%d incomplete markers): the gate measured a truncated stream, not the pump", incomp)
	}
	t.Logf("fed %.2f MiB through the shipped pump: %.0f allocs/MiB, %.0f B/MiB, %d frames, %.2f MiB of payload",
		float64(len(budgetFeed()))/(1<<20), allocsPerMiB, bytesPerMiB, frames, float64(streamed)/(1<<20))

	if allocsPerMiB > budgetAllocsPerMiB {
		t.Fatalf("the shipped pump spent %.0f allocs/MiB, over the budget of %d (+25%% over the %s baseline): a new allocation has landed on the pump path",
			allocsPerMiB, budgetAllocsPerMiB, "2026-09-29")
	}
	if bytesPerMiB > budgetBytesPerMiB {
		t.Fatalf("the shipped pump allocated %.0f B/MiB, over the budget of %d (+25%% over the %s baseline): a new copy has landed on the pump path",
			bytesPerMiB, budgetBytesPerMiB, "2026-09-29")
	}
}
