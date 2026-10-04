package session

// Review of round 6 (codex, nocx-2v80t.3.54), findings 4 and 5: stopGrace
// did not bound a send already in flight — a pump that entered
// deliverRowEmission before stop() armed the drain took the unbounded branch
// and stop() waited on the drain forever — and abandonment multiplied: the
// `abandoned` flag was set only on the owed-marker path, so N queued rows
// cost N grace periods and N blocked goroutines, and the rows were lost
// with no count and no statement. These tests drive hostSession.stop — the
// real seam, with the real pump — against sinks whose sends park on gates
// the test opens, so every pass condition is an observed event, never a
// duration.

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// wedgeFirstSink is a Sink whose first SendOutputRows parks until firstGate
// closes and every later one parks forever — a wire that wedges mid-stream
// and never answers again. attempts records each send ENTERED, in order,
// which is the observable "nothing further is attempted after the first
// abandoned send" reads; a wedged send never completes, so no delivery is
// ever recorded.
type wedgeFirstSink struct {
	mu          sync.Mutex
	attempts    []uint64
	entered     chan struct{}
	enteredOnce sync.Once
	firstGate   chan struct{}
	never       chan struct{} // deliberately never closed by any test
}

func newWedgeFirstSink() *wedgeFirstSink {
	return &wedgeFirstSink{
		entered:   make(chan struct{}),
		firstGate: make(chan struct{}),
		never:     make(chan struct{}),
	}
}

func (s *wedgeFirstSink) SendSessionData(proto.SessionFrame) error    { return nil }
func (s *wedgeFirstSink) SendLifecycleData(proto.SessionFrame) error  { return nil }
func (s *wedgeFirstSink) SendNotification(proto.Notification) error   { return nil }
func (s *wedgeFirstSink) SendScreenFrame(proto.ScreenDataFrame) error { return nil }

func (s *wedgeFirstSink) SendOutputRows(f proto.OutputRowsFrame) error {
	s.mu.Lock()
	first := len(s.attempts) == 0
	s.attempts = append(s.attempts, f.FromRow)
	s.mu.Unlock()
	s.enteredOnce.Do(func() { close(s.entered) })
	if first {
		<-s.firstGate
		return nil
	}
	<-s.never
	return nil
}

func (s *wedgeFirstSink) SendIntervalEnd(proto.IntervalEndFrame) error     { return nil }
func (s *wedgeFirstSink) SendClearBoundary(proto.ClearBoundaryFrame) error { return nil }
func (s *wedgeFirstSink) SendEffectFrame(proto.EffectFrame) error          { return nil }

func (s *wedgeFirstSink) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

// A row send blocked BEFORE stop() is ever called must not hang it: the pump
// entered deliverRowEmission through the unbounded branch, so no drain arm
// reaches into that call. stop() must still return within one bound, attempt
// nothing further, and count the in-flight row and everything still queued
// as stated losses (finding 4).
func TestStopReturnsWhenARowSendWasBlockedBeforeStop(t *testing.T) {
	sink := newWedgeFirstSink()
	hs, _ := stopTestSession(t, sink)
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}

	bridge.OutputRows(1, row, 0)
	select {
	case <-sink.entered: // the pump is inside the send stop() cannot reach
	case <-time.After(hangLimit):
		t.Fatal("the pump never entered the first row's send")
	}
	bridge.OutputRows(2, row, 0)
	bridge.OutputRows(3, row, 0)

	stopBounded(t, hs)

	if got := sink.attemptCount(); got != 1 {
		t.Fatalf("the pump attempted %d send(s), want exactly 1 — nothing may be attempted after the send stop() abandoned", got)
	}
	if got := hs.rowsIncomplete.Load(); got != 3 {
		t.Fatalf("rowsIncomplete = %d, want 3 — the in-flight row and the two still queued are each a counted loss", got)
	}
}

// After the FIRST abandoned drain-time send nothing more may be attempted,
// and every row behind it must be a counted loss rather than one more grace
// period and one more blocked goroutine apiece (finding 5). The first row's
// send is released only once stop() has armed the drain, so the pump's next
// send is a drain-time send — the one the bound exists for.
func TestStopAttemptsNothingAfterTheFirstAbandonedDrainSend(t *testing.T) {
	sink := newWedgeFirstSink()
	hs, _ := stopTestSession(t, sink)
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}

	bridge.OutputRows(1, row, 0)
	select {
	case <-sink.entered:
	case <-time.After(hangLimit):
		t.Fatal("the pump never entered the first row's send")
	}
	bridge.OutputRows(2, row, 0)
	bridge.OutputRows(3, row, 0)

	done := make(chan struct{})
	go func() {
		hs.stop()
		close(done)
	}()
	// The drain is armed inside stop(); observing it is the event that
	// makes the pump's next send a drain-time send.
	deadline := time.Now().Add(hangLimit)
	for !hs.drainRequested() {
		if time.Now().After(deadline) {
			t.Fatal("stop() never armed the row drain")
		}
		time.Sleep(time.Millisecond)
	}
	close(sink.firstGate)

	select {
	case <-done:
	case <-time.After(hangLimit):
		t.Fatal("hostSession.stop never returned")
	}

	if got := sink.attemptCount(); got != 2 {
		t.Fatalf("the pump attempted %d send(s), want exactly 2 — row 1 and the first drain-time row; nothing after the first abandoned send", got)
	}
	if got := hs.rowsIncomplete.Load(); got != 2 {
		t.Fatalf("rowsIncomplete = %d, want 2 — the abandoned drain-time row and the row behind it, counted as stated losses", got)
	}
}

// Paired ordinary path: the same shape with a sink that answers — rows
// queued before stop() are delivered whole, nothing is abandoned, and the
// loss count stays at zero.
func TestStopWithRowsQueuedDeliversThemAndCountsNoLoss(t *testing.T) {
	sink := newOrderedStallingSink()
	close(sink.release) // an ordinary healthy sink: every send proceeds at once
	hs, _ := stopTestSession(t, sink)
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}

	bridge.OutputRows(1, row, 0)
	bridge.OutputRows(2, row, 0)
	sink.waitUntil(func(log []recordedDelivery) bool { return len(log) >= 2 })

	stopBounded(t, hs)

	if got := hs.rowsIncomplete.Load(); got != 0 {
		t.Fatalf("rowsIncomplete = %d, want 0 — a drain whose sink answers loses nothing", got)
	}
	if got := len(sink.snapshot()); got != 2 {
		t.Fatalf("%d delivery log entries, want 2 — both queued rows delivered", got)
	}
}
