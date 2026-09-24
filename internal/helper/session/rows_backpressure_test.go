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
}

// orderedStallingSink is a Sink whose first SendOutputRows call parks until
// the test releases it, and which otherwise records every delivery — rows
// and end markers alike — in ONE ordered log, so a test can check the order
// between the two kinds rather than only within each.
type orderedStallingSink struct {
	mu      sync.Mutex
	log     []recordedDelivery
	once    sync.Once
	stalled chan struct{}
	release chan struct{}
}

func newOrderedStallingSink() *orderedStallingSink {
	return &orderedStallingSink{stalled: make(chan struct{}), release: make(chan struct{})}
}

func (s *orderedStallingSink) SendSessionData(proto.SessionFrame) error    { return nil }
func (s *orderedStallingSink) SendLifecycleData(proto.SessionFrame) error  { return nil }
func (s *orderedStallingSink) SendNotification(proto.Notification) error   { return nil }
func (s *orderedStallingSink) SendScreenFrame(proto.ScreenDataFrame) error { return nil }

func (s *orderedStallingSink) SendOutputRows(f proto.OutputRowsFrame) error {
	s.once.Do(func() {
		close(s.stalled)
		<-s.release
	})
	var doc proto.OutputRowsDoc
	if err := json.Unmarshal(f.Payload, &doc); err != nil {
		return err
	}
	s.mu.Lock()
	s.log = append(s.log, recordedDelivery{fromRow: f.FromRow, lostRows: doc.LostRows})
	s.mu.Unlock()
	return nil
}

func (s *orderedStallingSink) SendIntervalEnd(f proto.IntervalEndFrame) error {
	s.mu.Lock()
	s.log = append(s.log, recordedDelivery{end: true, fromRow: f.EndRow})
	s.mu.Unlock()
	return nil
}

func (s *orderedStallingSink) SendClearBoundary(proto.ClearBoundaryFrame) error {
	s.mu.Lock()
	s.log = append(s.log, recordedDelivery{clear: true})
	s.mu.Unlock()
	return nil
}

func (s *orderedStallingSink) snapshot() []recordedDelivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedDelivery(nil), s.log...)
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

func TestEndMarkerNeverDropsUnderBackpressure(t *testing.T) {
	sink := newOrderedStallingSink()
	hs := newBridgeOnlySession(t, sink)
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}

	// The pump takes the very first batch and parks delivering it — this is
	// what lets the rest pile up in the queue instead of draining as fast
	// as this test can produce them, the same shape a genuinely wedged
	// connection takes.
	bridge.OutputRows(0, row, 0)
	select {
	case <-sink.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump never reached the stalling sink")
	}

	// Fill the queue past its bound. Every one of these calls is the
	// EMULATOR INGEST SEAM, made under what would be the runtime's own lock
	// in production, and every one must return immediately — this is the
	// invariant the design forbids trading away for anything, backpressure
	// included.
	const overflow = 40
	flooded := make(chan struct{})
	go func() {
		for i := uint64(1); i <= maxQueuedRowBatches+overflow; i++ {
			bridge.OutputRows(i, row, 0)
		}
		close(flooded)
	}()
	select {
	case <-flooded:
	case <-time.After(5 * time.Second):
		t.Fatal("OutputRows blocked while the queue was full — the emulator ingest path must never wait on the wire")
	}

	if got := hs.rowsDropped.Load(); got == 0 {
		t.Fatal("no batch was dropped; the test did not actually exceed the queue's bound")
	}
	wantLost := hs.rowsLostPending.Load()
	if wantLost == 0 {
		t.Fatal("batches were dropped but no rows were counted lost")
	}

	// The end marker: queued despite the row queue sitting at its bound.
	// This hand-off must also return immediately — IntervalEnd is called
	// from the same locked path OutputRows is — and it must NOT be one of
	// the drops just measured above, however full the queue is.
	nonce := sessionruntime.FenceNonce{}
	for i := range nonce {
		nonce[i] = 0xCD
	}
	const endRow = maxQueuedRowBatches + overflow + 1
	ended := make(chan struct{})
	go func() {
		bridge.IntervalEnd(nonce, endRow, []emulator.Row{textRow("closing")})
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("IntervalEnd blocked while the row queue was full")
	}

	close(sink.release)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if last := lastOf(sink.snapshot()); last != nil && last.end {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the end marker never reached the coordinator; delivered so far: %+v", sink.snapshot())
		}
		time.Sleep(2 * time.Millisecond)
	}

	log := sink.snapshot()
	var ends, gotLost int
	var lostRowsTotal uint64
	for _, d := range log {
		if d.end {
			ends++
			continue
		}
		if d.lostRows != 0 {
			gotLost++
			lostRowsTotal += d.lostRows
		}
	}
	if ends != 1 {
		t.Fatalf("got %d end markers, want exactly 1 — an end marker was duplicated or lost", ends)
	}
	if log[len(log)-1].fromRow != endRow || !log[len(log)-1].end {
		t.Fatalf("the end marker was not the LAST delivery (log tail = %+v), want it after every row this test fed", log[len(log)-1])
	}
	if gotLost != 1 {
		t.Fatalf("%d deliveries carried a nonzero LostRows, want exactly 1 — the drop must be reported exactly once", gotLost)
	}
	if lostRowsTotal != wantLost {
		t.Fatalf("the reconciling delivery's LostRows totalled %d, want exactly %d (what the drops actually cost)", lostRowsTotal, wantLost)
	}
}

func lastOf(log []recordedDelivery) *recordedDelivery {
	if len(log) == 0 {
		return nil
	}
	return &log[len(log)-1]
}

// TestBridgeDropsNothingUnderOrdinaryLoad pairs the backpressure test above:
// an ordinary command's output, nowhere near the bridge's bound, costs no
// drop and no lost count, through the real emulator ingest path (unlike the
// test above, which drives the bridge's own seam directly to force
// backpressure deterministically).
func TestBridgeDropsNothingUnderOrdinaryLoad(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 30)
	waitForRows(t, sink, 1, 0)
	if got := hs.rowsDropped.Load(); got != 0 {
		t.Fatalf("rowsDropped = %d under ordinary load, want 0", got)
	}
	if got := hs.rowsLostPending.Load(); got != 0 {
		t.Fatalf("rowsLostPending = %d under ordinary load, want 0", got)
	}
}
