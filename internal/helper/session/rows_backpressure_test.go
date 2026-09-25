package session

// The stage review's blocker (nocx-2v80t.3.15, finding 1): the row bridge's
// queue is fed from the seam sessionruntime.RowStream calls under the
// runtime's own lock, so enqueueRowEmission must never block that seam, and
// it must never drop the one emission that closes a command's block — a
// dropped end marker leaves the coordinator's block open forever, with
// nothing left in the ordered stream that could ever close it. This file
// drives that seam directly (rowBridge.OutputRows / .IntervalEnd, exactly
// what sessionruntime.Session calls) against a pump deliberately stalled the
// way a wedged wire stalls it, rather than fighting the real emulator for an
// exact departed-row count.

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// recordedDelivery is one frame the pump actually sent, in delivery order —
// a row batch's FromRow and its decoded LostRows, or an end marker's EndRow.
type recordedDelivery struct {
	end      bool
	clear    bool
	fromRow  uint64
	lostRows uint64
	rows     int
}

// orderedStallingSink is a Sink whose first SendOutputRows call parks until
// the test releases it, and which otherwise records every delivery — rows
// and markers alike — in ONE ordered log, so a test can check the order
// between the kinds rather than only within each. Every recorded delivery
// signals changed, which is what a test waits on: an observable state
// change, never a duration.
type orderedStallingSink struct {
	mu      sync.Mutex
	log     []recordedDelivery
	once    sync.Once
	stalled chan struct{}
	release chan struct{}
	changed chan struct{}
}

func newOrderedStallingSink() *orderedStallingSink {
	return &orderedStallingSink{
		stalled: make(chan struct{}), release: make(chan struct{}), changed: make(chan struct{}, 1),
	}
}

func (s *orderedStallingSink) SendSessionData(proto.SessionFrame) error    { return nil }
func (s *orderedStallingSink) SendLifecycleData(proto.SessionFrame) error  { return nil }
func (s *orderedStallingSink) SendNotification(proto.Notification) error   { return nil }
func (s *orderedStallingSink) SendScreenFrame(proto.ScreenDataFrame) error { return nil }

func (s *orderedStallingSink) record(d recordedDelivery) {
	s.mu.Lock()
	s.log = append(s.log, d)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *orderedStallingSink) SendOutputRows(f proto.OutputRowsFrame) error {
	s.once.Do(func() {
		close(s.stalled)
		<-s.release
	})
	var doc proto.OutputRowsDoc
	if err := json.Unmarshal(f.Payload, &doc); err != nil {
		return err
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(doc.Rows, &rows); err != nil {
		return err
	}
	s.record(recordedDelivery{fromRow: f.FromRow, lostRows: doc.LostRows, rows: len(rows)})
	return nil
}

func (s *orderedStallingSink) SendIntervalEnd(f proto.IntervalEndFrame) error {
	s.record(recordedDelivery{end: true, fromRow: f.EndRow})
	return nil
}

func (s *orderedStallingSink) SendClearBoundary(proto.ClearBoundaryFrame) error {
	s.record(recordedDelivery{clear: true})
	return nil
}

func (s *orderedStallingSink) snapshot() []recordedDelivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedDelivery(nil), s.log...)
}

// waitUntil blocks until done holds over the delivery log, woken by each
// delivery the sink records. A pump that never delivers what the test waits
// for hangs the test into go test's own timeout, which names this frame.
func (s *orderedStallingSink) waitUntil(done func([]recordedDelivery) bool) []recordedDelivery {
	for {
		if log := s.snapshot(); done(log) {
			return log
		}
		<-s.changed
	}
}

// newBridgeOnlySession builds a hostSession with nothing but what the row
// bridge and its pump touch: no runtime, no PTY, no owner. The seam under
// test is the bridge (rows.go), not the emulator that feeds it in
// production — sessionruntime's own contract tests cover that it calls
// OutputRows/IntervalEnd in stream order; this file covers what the bridge
// does with that order under backpressure.
func newBridgeOnlySession(t *testing.T, sink Sink) *hostSession {
	t.Helper()
	hs := &hostSession{
		id:       proto.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"},
		raw:      mintRaw(t),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		subs:     make(map[proto.SubscriberID]*subscriber),
		rowWake:  make(chan struct{}, 1),
		rowsDone: make(chan struct{}),
	}
	hs.subs["coord-1"] = &subscriber{id: "coord-1", raw: mintRaw(t), sink: sink}
	return hs
}

// stallThePump hands the pump its first batch and waits until it parks
// delivering it — what lets everything after pile up in the queue instead of
// draining as fast as the test can produce it, the shape a genuinely wedged
// connection takes. The batch is row 0, one row.
func stallThePump(t *testing.T, sink *orderedStallingSink, bridge *rowBridge) {
	t.Helper()
	bridge.OutputRows(0, []emulator.Row{textRow("x")}, 0)
	select {
	case <-sink.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump never reached the stalling sink")
	}
}

// returnsAtOnce runs one hand-off the way the runtime makes it — under its
// own lock, in production — and fails if it waits on the wire. Every
// RowStream call must return immediately: that is the invariant the design
// forbids trading away for anything, backpressure included.
func returnsAtOnce(t *testing.T, what string, handOff func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		handOff()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked while the queue was full — the emulator ingest path must never wait on the wire", what)
	}
}

// assertLossesNameTheirGaps is the arithmetic the coordinator's store
// enforces (content.AppendBlockRows): every delivered batch's FromRow minus
// its LostRows is exactly where the batch before it ended.
func assertLossesNameTheirGaps(t *testing.T, log []recordedDelivery) {
	t.Helper()
	var next uint64
	started := false
	for _, d := range log {
		if d.end || d.clear {
			continue
		}
		if started && d.fromRow-d.lostRows != next {
			t.Fatalf("a batch at FromRow %d claims %d lost, a gap from %d — but the batch before ended at %d (log %+v)",
				d.fromRow, d.lostRows, d.fromRow-d.lostRows, next, log)
		}
		started = true
		next = d.fromRow + uint64(d.rows) //nolint:gosec // a row count
	}
}

func endNonce(b byte) sessionruntime.FenceNonce {
	var nonce sessionruntime.FenceNonce
	for i := range nonce {
		nonce[i] = b
	}
	return nonce
}

// An end marker is never shed by the ROW bound: however full the row queue
// is, the marker that closes a command's block is queued (nocx-2v80t.3.15).
// And the rows the bound did shed are stated on the wire exactly once, AT
// the gap they left: a loss-only carrier at the end of the gap, immediately
// before the end marker, so the loss lands inside the interval it happened
// in and the closing screen follows it (nocx-2v80t.3.26).
func TestEndMarkerNeverDropsUnderBackpressure(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	stallThePump(t, sink, bridge)

	const overflow = 40
	returnsAtOnce(t, "OutputRows", func() {
		for i := uint64(1); i <= maxQueuedRowBatches+overflow; i++ {
			bridge.OutputRows(i, row, 0)
		}
	})
	if got := hs.rowsDropped.Load(); got != overflow {
		t.Fatalf("rowsDropped = %d, want %d — the test did not exceed the row bound by exactly its overflow", got, overflow)
	}

	const endRow = maxQueuedRowBatches + overflow + 1
	returnsAtOnce(t, "IntervalEnd", func() {
		bridge.IntervalEnd(endNonce(0xCD), endRow, []emulator.Row{textRow("closing")}, false)
	})

	close(sink.release)
	log := sink.waitUntil(func(log []recordedDelivery) bool {
		last := lastOf(log)
		return last != nil && last.end
	})

	ends, flagged := 0, 0
	for _, d := range log {
		if d.end {
			ends++
			continue
		}
		if d.lostRows != 0 {
			flagged++
			if d.fromRow != endRow || d.rows != 0 || d.lostRows != overflow {
				t.Fatalf("the loss was stated as %+v, want a loss-only carrier of %d at %d, the end of the gap", d, overflow, endRow)
			}
		}
	}
	if ends != 1 {
		t.Fatalf("got %d end markers, want exactly 1 — an end marker was duplicated or lost", ends)
	}
	if last := log[len(log)-1]; last.fromRow != endRow || !last.end {
		t.Fatalf("the end marker was not the LAST delivery (log tail = %+v), want it after every row this test fed", last)
	}
	if flagged != 1 {
		t.Fatalf("%d deliveries carried a nonzero LostRows, want exactly 1 — the drop must be reported exactly once", flagged)
	}
	assertLossesNameTheirGaps(t, log)
}

// A drop is stated at the stream position where it happened (nocx-2v80t.3.26,
// finding 6). The bridge used to fold its drops into whichever batch it
// DELIVERED next — here, row 1, queued long before the drop — so the loss was
// claimed at a position with no gap and the store persisted it there. The
// drop belongs to the first batch ACCEPTED after it, and every batch before
// that carries none.
func TestABridgeDropIsStatedWhereItHappened(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	stallThePump(t, sink, bridge)

	const overflow = 40
	returnsAtOnce(t, "OutputRows", func() {
		for i := uint64(1); i <= maxQueuedRowBatches+overflow; i++ {
			bridge.OutputRows(i, row, 0)
		}
	})
	close(sink.release)
	// Everything queued before the drop reaches the wire first.
	sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == maxQueuedRowBatches+1 })

	// The next batch the runtime hands over, now that the queue has room.
	const resumed = maxQueuedRowBatches + overflow + 1
	bridge.OutputRows(resumed, row, 0)
	log := sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == maxQueuedRowBatches+2 })

	for _, d := range log[:len(log)-1] {
		if d.lostRows != 0 {
			t.Fatalf("the batch at %d carries lost=%d, but it was queued BEFORE the drop — the loss is not its gap", d.fromRow, d.lostRows)
		}
	}
	if last := log[len(log)-1]; last.fromRow != resumed || last.lostRows != overflow {
		t.Fatalf("the first batch after the drop was delivered as %+v, want FromRow %d carrying the %d rows shed ahead of it", last, resumed, overflow)
	}
	assertLossesNameTheirGaps(t, log)
}

// The marker queue is bounded under a wedged sink (nocx-2v80t.3.26, finding
// 5, AD-10): end markers and clear boundaries used to append unconditionally,
// so a wedged wire and a shell ending commands — or a program repeating ED3
// — grew the queue without limit. A run of clears with nothing between them
// is one fact and folds into one; past the marker budget an end marker is
// shed and COUNTED; and every hand-off still returns at once.
func TestTheMarkerQueueIsBoundedUnderAWedgedSink(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	stallThePump(t, sink, bridge)

	const clears, ends = 1000, maxQueuedMarkers + 50
	returnsAtOnce(t, "ClearBoundary and IntervalEnd", func() {
		for range clears {
			bridge.ClearBoundary()
		}
		for i := range ends {
			bridge.IntervalEnd(endNonce(byte(i)), 1, []emulator.Row{textRow("closing")}, false)
		}
	})

	hs.rowMu.Lock()
	queued := len(hs.rowQueue)
	hs.rowMu.Unlock()
	if queued > maxQueuedRowBatches+2*maxQueuedMarkers {
		t.Fatalf("the queue holds %d emissions behind a wedged sink, want at most %d", queued, maxQueuedRowBatches+2*maxQueuedMarkers)
	}
	// One clear (the thousand folded) and maxQueuedMarkers-1 ends fit; the
	// rest of the ends are shed and counted.
	if got, want := hs.markersDropped.Load(), uint64(ends-(maxQueuedMarkers-1)); got != want {
		t.Fatalf("markersDropped = %d, want %d — every marker past the budget is counted, and a folded clear is not one", got, want)
	}

	close(sink.release)
	log := sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == 1+maxQueuedMarkers })
	if !log[1].clear {
		t.Fatalf("the first marker delivered is %+v, want the one clear the run folded into", log[1])
	}
	for i, d := range log[2:] {
		if !d.end {
			t.Fatalf("delivery %d is %+v, want an end marker", i+2, d)
		}
	}
}

// Paired with the bound above: markers within the budget, behind the same
// wedged sink, all reach the wire once it clears, and none is counted shed.
func TestMarkersWithinTheBudgetAllReachTheWire(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	stallThePump(t, sink, bridge)

	for i := range maxQueuedMarkers {
		bridge.IntervalEnd(endNonce(byte(i)), 1, nil, false)
	}
	close(sink.release)
	log := sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == 1+maxQueuedMarkers })
	for i, d := range log[1:] {
		if !d.end {
			t.Fatalf("delivery %d is %+v, want an end marker", i+1, d)
		}
	}
	if got := hs.markersDropped.Load(); got != 0 {
		t.Fatalf("markersDropped = %d within the budget, want 0", got)
	}
}

func lastOf(log []recordedDelivery) *recordedDelivery {
	if len(log) == 0 {
		return nil
	}
	return &log[len(log)-1]
}

// TestBridgeDropsNothingUnderOrdinaryLoad pairs the backpressure tests above:
// an ordinary command's output, nowhere near the bridge's bounds, costs no
// drop and no lost count, through the real emulator ingest path (unlike the
// tests above, which drive the bridge's own seam directly to force
// backpressure deterministically).
func TestBridgeDropsNothingUnderOrdinaryLoad(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 30)
	sink.waitFor(1, 0, 0)
	if got := hs.rowsDropped.Load(); got != 0 {
		t.Fatalf("rowsDropped = %d under ordinary load, want 0", got)
	}
	if got := hs.markersDropped.Load(); got != 0 {
		t.Fatalf("markersDropped = %d under ordinary load, want 0", got)
	}
	hs.rowMu.Lock()
	pending := hs.rowsLostPending
	hs.rowMu.Unlock()
	if pending != 0 {
		t.Fatalf("rowsLostPending = %d under ordinary load, want 0", pending)
	}
	for _, f := range sink.rowFrames() {
		var doc proto.OutputRowsDoc
		if err := json.Unmarshal(f.Payload, &doc); err != nil {
			t.Fatalf("decode a row frame: %v", err)
		}
		if doc.LostRows != 0 {
			t.Fatalf("a batch under ordinary load carries lost=%d, want 0", doc.LostRows)
		}
	}
}
