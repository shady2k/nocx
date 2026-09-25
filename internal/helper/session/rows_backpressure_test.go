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
	"errors"
	"io"
	"log/slog"
	"strings"
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
	// An end marker's own document: the fence it names and whether it was
	// settled without one — a gap the coordinator stores as one.
	nonce   string
	noFence bool
	// incomplete is a rows document's own statement that the block in
	// flight ended incomplete (nocx-2v80t.3.36).
	incomplete bool
	// raw is a rows document's own payload, for the contract check.
	raw []byte
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
	// failIncomplete makes the first incomplete marker's send fail, the way
	// a dying connection answers it (nocx-2v80t.3.38).
	failIncomplete bool
	failed         chan struct{}
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
	s.mu.Lock()
	fail := doc.Incomplete && s.failIncomplete
	if fail {
		s.failIncomplete = false
	}
	s.mu.Unlock()
	if fail {
		close(s.failed)
		return errors.New("injected: the marker's send failed")
	}
	s.record(recordedDelivery{fromRow: f.FromRow, lostRows: doc.LostRows, rows: len(rows), incomplete: doc.Incomplete, raw: f.Payload})
	return nil
}

func (s *orderedStallingSink) SendIntervalEnd(f proto.IntervalEndFrame) error {
	var doc proto.IntervalEndDoc
	if err := json.Unmarshal(f.Payload, &doc); err != nil {
		return err
	}
	s.record(recordedDelivery{end: true, fromRow: f.EndRow, nonce: doc.Nonce, noFence: doc.NoFence})
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

// bufferOf sets a session's row buffer to hold n one-row batches of textRow
// and no more, measured by the bridge's own accounting.
func bufferOf(hs *hostSession, n int) {
	one := rowEmission{from: 0, rows: []emulator.Row{textRow("x")}}
	hs.rowBufferBytes = int64(n) * emissionBytes(one)
}

// queuedBytes is the bridge's own count of what its queue holds.
func queuedBytes(hs *hostSession) int64 {
	hs.rowMu.Lock()
	defer hs.rowMu.Unlock()
	return hs.rowQueuedBytes
}

// A wedged sink past the helper's buffer (nocx-2v80t.3.36, the owner's
// rule): the block in flight ends INCOMPLETE — one marker, delivered after
// everything the buffer did hold — and nothing after it is recorded until
// the next command starts after the stream is healthy. The ends and clears
// that arrived meanwhile are not folded and not replaced by zero-nonce
// stand-ins: they are gone, and the coordinator settles those commands from
// their own completions. The next end the runtime hands over once the wire
// has drained is carried with its OWN fence, and the rows after it — the
// next command's — are recorded again. The buffer never holds more than its
// configured bytes and the one marker.
func TestAWedgedSinkPastTheBufferEndsTheBlockIncomplete(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	bufferOf(hs, 8)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	stallThePump(t, sink, bridge)

	returnsAtOnce(t, "the flood past the buffer", func() {
		for i := uint64(1); i <= 1000; i++ {
			bridge.OutputRows(i, row, 0)
			if i%10 == 0 {
				bridge.IntervalEnd(endNonce(0xEE), i+1, row, false)
				bridge.ClearBoundary()
			}
		}
	})
	if got, limit := queuedBytes(hs), hs.rowBufferBytes+emissionBytes(rowEmission{incomplete: true}); got > limit {
		t.Fatalf("the buffer holds %d bytes behind a wedged sink, want at most %d", got, limit)
	}

	close(sink.release)
	// The stalled batch, the eight the buffer held, then the one marker.
	log := sink.waitUntil(func(log []recordedDelivery) bool {
		last := lastOf(log)
		return last != nil && last.incomplete
	})
	if len(log) != 1+8+1 {
		t.Fatalf("delivered %d frames before the incomplete marker settled, want 10: %+v", len(log), log)
	}
	if m := log[len(log)-1]; m.fromRow != 9 || m.rows != 0 {
		t.Fatalf("the incomplete marker is %+v, want no rows at 9, the first row not recorded", m)
	}
	// The marker the bridge itself produced, against its contract.
	validateRowSchema(t, loadRowSchema(t, "session.output-rows.schema.json"), log[len(log)-1].raw)

	// The wire has drained. Rows still in flight belong to a command that
	// started while nothing could be recorded, and are not; the next end
	// the runtime hands over is carried with its own fence; the rows after
	// it are the next command's, and are recorded.
	bridge.OutputRows(1001, row, 0)
	bridge.IntervalEnd(endNonce(0xAB), 1002, row, false)
	bridge.OutputRows(1002, row, 0)
	log = sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == 12 })
	if end := log[10]; !end.end || end.nonce != strings.Repeat("ab", 32) || end.fromRow != 1002 {
		t.Fatalf("the first end after recovery arrived as %+v, want its own fence at 1002", end)
	}
	if next := log[11]; next.end || next.fromRow != 1002 || next.rows != 1 {
		t.Fatalf("the next command's rows arrived as %+v, want one row at 1002", next)
	}
	for _, d := range log {
		if d.end && d.nonce == strings.Repeat("00", 32) {
			t.Fatalf("a zero-nonce end reached the coordinator: %+v", d)
		}
		if d.clear {
			t.Fatalf("a clear from inside the overflow reached the coordinator: %+v", d)
		}
	}
	if got := hs.rowsIncomplete.Load(); got != 1 {
		t.Fatalf("rowsIncomplete = %d, want the one overflow", got)
	}
}

// Paired: the same flood within the buffer reaches the wire whole — every
// row, every end with its own fence, every clear — and nothing is said
// incomplete.
func TestAWedgedSinkWithinTheBufferLosesNothing(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	bufferOf(hs, 64)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	stallThePump(t, sink, bridge)

	for i := uint64(1); i <= 10; i++ {
		bridge.OutputRows(i, row, 0)
	}
	bridge.IntervalEnd(endNonce(0xCD), 11, nil, false)
	bridge.ClearBoundary()
	close(sink.release)
	log := sink.waitUntil(func(log []recordedDelivery) bool { return len(log) == 1+10+2 })
	for _, d := range log {
		if d.incomplete {
			t.Fatalf("a flood within the buffer was said incomplete: %+v", log)
		}
	}
	if end := log[11]; !end.end || end.nonce != strings.Repeat("cd", 32) {
		t.Fatalf("the end arrived as %+v, want its own fence", end)
	}
	if !log[12].clear {
		t.Fatalf("the clear arrived as %+v", log[12])
	}
	assertLossesNameTheirGaps(t, log)
}

// A failed send of the incomplete marker does not lose the only statement of
// the loss (nocx-2v80t.3.38): the bridge still owes it, and states it before
// the next thing it delivers — here the end that closes the interval running
// through the overflow — so the coordinator settles that block incomplete
// rather than whole. Paired with the ordinary send, which is not repeated
// (TestAWedgedSinkPastTheBufferEndsTheBlockIncomplete delivers it once).
func TestAFailedIncompleteMarkerIsStatedAgainBeforeTheNextDelivery(t *testing.T) {
	sink := newOrderedStallingSink()
	sink.failIncomplete = true
	sink.failed = make(chan struct{})
	hs := newBridgeOnlySession(t, sink)
	bufferOf(hs, 2)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	stallThePump(t, sink, bridge)

	for i := uint64(1); i <= 10; i++ {
		bridge.OutputRows(i, row, 0)
	}
	close(sink.release)
	<-sink.failed // the marker's one send has failed

	bridge.IntervalEnd(endNonce(0xAB), 11, nil, false)
	log := sink.waitUntil(func(log []recordedDelivery) bool {
		last := lastOf(log)
		return last != nil && last.end
	})
	if len(log) < 2 || !log[len(log)-2].incomplete || log[len(log)-2].fromRow != 3 {
		t.Fatalf("the delivery before the end is %+v, want the owed incomplete marker at 3", log[len(log)-2])
	}
	marks := 0
	for _, d := range log {
		if d.incomplete {
			marks++
		}
	}
	if marks != 1 {
		t.Fatalf("the marker reached the coordinator %d times, want once", marks)
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
	if got := hs.rowsIncomplete.Load(); got != 0 {
		t.Fatalf("rowsIncomplete = %d under ordinary load, want 0", got)
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
