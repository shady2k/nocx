package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeSink records what the bridge hands the transport, and answers each
// rows delivery with the mark a test chose.
type fakeSink struct {
	mu       sync.Mutex
	attached []session.ID
	detached []session.ID
	rows     []client.OutputRows
	ends     []client.IntervalEnd
	clears   []session.ID
	// incomplete are the helper's overflow markers, by the row they name.
	incomplete         []uint64
	outputStartAnchors []proto.OutputStartScreenAnchor
	lost               []lostBoundary
	lostCh             chan struct{} // signalled on every BlockBoundaryLost, when set
	ended              []session.ID  // sessions the helper reported ended
	answer             func(fromRow uint64, n int) (uint64, bool)
}

func (f *fakeSink) AttachBlockRows(sid session.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, sid)
}

func (f *fakeSink) DetachBlockRows(sid session.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = append(f.detached, sid)
}

func (f *fakeSink) HelperSessionEnded(sid session.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, sid)
}

func (f *fakeSink) BlockRowsArrived(_ session.ID, fromRow, lost uint64, rows []emulator.Row, _ string) (uint64, bool) {
	f.mu.Lock()
	f.rows = append(f.rows, client.OutputRows{FromRow: fromRow, LostRows: lost, Rows: rows})
	answer := f.answer
	f.mu.Unlock()
	return answer(fromRow, len(rows))
}

func (f *fakeSink) BlockIntervalEnded(_ session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, noFence bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ends = append(f.ends, client.IntervalEnd{Nonce: sessionruntime.FenceNonce(nonce), EndRow: endRow, Closing: closing, NoFence: noFence})
}

func (f *fakeSink) BlockIntervalEndedWithAnchor(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, anchor *client.ScreenAnchor, noFence bool) {
	f.BlockIntervalEnded(sid, nonce, endRow, closing, noFence)
	if anchor == nil {
		return
	}
	f.mu.Lock()
	f.ends[len(f.ends)-1].ScreenAnchor = anchor
	f.mu.Unlock()
}

func (f *fakeSink) BlockBoundaryLost(_ context.Context, sid session.ID, nonce [32]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lost = append(f.lost, lostBoundary{sid: sid, nonce: nonce})
	if f.lostCh != nil {
		f.lostCh <- struct{}{}
	}
}

// lostBoundary is one BlockBoundaryLost the sink was told of.
type lostBoundary struct {
	sid   session.ID
	nonce [32]byte
}

func (f *fakeSink) BlockClearBoundary(sid session.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears = append(f.clears, sid)
}

func (f *fakeSink) BlockOutputIncomplete(_ session.ID, fromRow uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incomplete = append(f.incomplete, fromRow)
}

// fakeSource is the attachment's registration half.
type fakeSource struct {
	mu             sync.Mutex
	rows           func(client.OutputRows)
	end            func(client.IntervalEnd)
	clear          func()
	outputStartRow func(client.OutputStartRow)
}

func (s *fakeSource) OnOutputRows(f func(client.OutputRows)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = f
}

func (s *fakeSource) OnIntervalEnd(f func(client.IntervalEnd)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.end = f
}

func (s *fakeSource) OnClearBoundary(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear = f
}

func (s *fakeSource) OnOutputStartRow(f func(client.OutputStartRow)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputStartRow = f
}

func (s *fakeSource) deliverRows(o client.OutputRows) {
	s.mu.Lock()
	f := s.rows
	s.mu.Unlock()
	if f != nil {
		f(o)
	}
}

func (s *fakeSource) deliverEnd(e client.IntervalEnd) {
	s.mu.Lock()
	f := s.end
	s.mu.Unlock()
	if f != nil {
		f(e)
	}
}

func (s *fakeSource) deliverClear() {
	s.mu.Lock()
	f := s.clear
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

// gatedConfirmer blocks every ConfirmWritten until the test releases it, and
// reports each mark it was asked for.
type gatedConfirmer struct {
	asked   chan uint64
	release chan struct{}
}

func (g *gatedConfirmer) ConfirmWritten(ctx context.Context, upToRow uint64) error {
	g.asked <- upToRow
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return nil
}

func rowsN(n int) []emulator.Row { return make([]emulator.Row, n) }

// The read loop must never wait on the helper's answer: a confirmation that
// is still in flight does not stop the next delivery from being handed to
// the transport, and the marks that pile up meanwhile collapse to the
// newest one — the mark is a watermark, so the highest is all of them.
func (f *fakeSink) BlockOutputStartPlaneAttached(session.ID) {}
func (f *fakeSink) BlockOutputStartRow(session.ID, uint64)   {}
func (f *fakeSink) BlockOutputStartRowWithAnchor(_ session.ID, _ uint64, anchor proto.OutputStartScreenAnchor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputStartAnchors = append(f.outputStartAnchors, anchor)
}

func TestTheRowsReadLoopNeverWaitsOnAConfirmation(t *testing.T) {
	// The exclusive mark: the helper's UpToRow is one past the last row it
	// may believe written (proto.ConfirmRowsParams), so ten rows from 0 are
	// confirmed through 10, never through 9.
	sink := &fakeSink{answer: func(from uint64, n int) (uint64, bool) { return from + uint64(n), true }} //nolint:gosec // a test's row count, never negative
	src := &fakeSource{}
	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	stop := bindBlockRowsTo(context.Background(), sink, "s1", src, conf)
	defer stop()

	src.deliverRows(client.OutputRows{FromRow: 0, Rows: rowsN(10)})
	if got := <-conf.asked; got != 10 {
		t.Fatalf("first confirmation asked for %d, want 10", got)
	}
	// The first confirmation is now held open by the helper. Three more
	// deliveries arrive on the read loop; none of them may block.
	src.deliverRows(client.OutputRows{FromRow: 10, Rows: rowsN(5)})
	src.deliverRows(client.OutputRows{FromRow: 15, Rows: rowsN(5)})
	src.deliverRows(client.OutputRows{FromRow: 20, Rows: rowsN(5)})
	sink.mu.Lock()
	n := len(sink.rows)
	sink.mu.Unlock()
	if n != 4 {
		t.Fatalf("the transport received %d deliveries while a confirmation was in flight, want 4", n)
	}

	close(conf.release)
	if got := <-conf.asked; got != 25 {
		t.Fatalf("after the held confirmation, the next one asked for %d, want the newest mark 25 (5, 20 and 25 collapse)", got)
	}
}

// A delivery the transport does not confirm (a store failure) sends nothing,
// and a later lower answer never moves the mark backwards (paired: an
// ordinary confirmed delivery is sent).
func TestAnUnconfirmedDeliverySendsNoMarkAndTheMarkNeverGoesBack(t *testing.T) {
	confirm := true
	var mark uint64
	sink := &fakeSink{answer: func(uint64, int) (uint64, bool) { return mark, confirm }}
	src := &fakeSource{}
	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	close(conf.release)
	stop := bindBlockRowsTo(context.Background(), sink, "s1", src, conf)
	defer stop()

	mark = 7
	src.deliverRows(client.OutputRows{FromRow: 0, Rows: rowsN(8)})
	if got := <-conf.asked; got != 7 {
		t.Fatalf("the ordinary confirmation asked for %d, want 7", got)
	}

	confirm = false
	src.deliverRows(client.OutputRows{FromRow: 8, Rows: rowsN(4)})
	confirm, mark = true, 5 // lower than what was already sent
	src.deliverRows(client.OutputRows{FromRow: 12, Rows: rowsN(1)})
	confirm, mark = true, 20
	src.deliverRows(client.OutputRows{FromRow: 13, Rows: rowsN(8)})
	if got := <-conf.asked; got != 20 {
		t.Fatalf("after an unconfirmed and a lower answer, the next mark sent was %d, want 20", got)
	}
}

// Ends are handed over with their nonce, and stopping detaches the stream
// and unregisters the callbacks: a delivery after stop reaches nobody.
func TestEndsReachTheTransportAndStopDetaches(t *testing.T) {
	sink := &fakeSink{answer: func(uint64, int) (uint64, bool) { return 0, false }}
	src := &fakeSource{}
	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	stop := bindBlockRowsTo(context.Background(), sink, "s1", src, conf)

	var nonce sessionruntime.FenceNonce
	nonce[0] = 0xab
	src.deliverEnd(client.IntervalEnd{Nonce: nonce, EndRow: 42, Closing: rowsN(3), ScreenAnchor: &client.ScreenAnchor{Row: 91, ScreenRevision: 4, BufferKind: "normal", BufferInstance: "7", Cols: 80, Rows: 24}})
	src.deliverClear()
	stop()
	stop() // idempotent
	src.deliverRows(client.OutputRows{FromRow: 42, Rows: rowsN(1)})
	src.deliverClear()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.attached) != 1 || len(sink.detached) != 1 {
		t.Fatalf("attached %v, detached %v: want one of each", sink.attached, sink.detached)
	}
	if len(sink.ends) != 1 || sink.ends[0].EndRow != 42 || sink.ends[0].Nonce[0] != 0xab || len(sink.ends[0].Closing) != 3 || sink.ends[0].ScreenAnchor == nil || sink.ends[0].ScreenAnchor.Row != 91 {
		t.Fatalf("the end reached the transport as %+v", sink.ends)
	}
	if len(sink.clears) != 1 || sink.clears[0] != "s1" {
		t.Fatalf("the clear boundary reached the transport as %v, want one for session s1", sink.clears)
	}
	if len(sink.rows) != 0 {
		t.Fatalf("a delivery after stop reached the transport: %+v", sink.rows)
	}
	if len(sink.clears) != 1 {
		t.Fatalf("a clear boundary after stop reached the transport: %v", sink.clears)
	}
}

// A nil sink or attachment wires nothing and stop is safe.
func TestANilSinkWiresNothing(t *testing.T) {
	stop := bindHeldBlockRows(context.Background(), nil, "s1", nil, nil)
	stop()
}

// The helper's overflow marker (nocx-2v80t.3.36) reaches the transport as
// what it is — the block in flight ending incomplete — and never as a row
// delivery; an ordinary delivery still goes to BlockRowsArrived.
func TestAnIncompleteMarkerIsNotARowDelivery(t *testing.T) {
	sink := &fakeSink{answer: func(from uint64, n int) (uint64, bool) { return from + uint64(n), true }} //nolint:gosec // a test's row count
	src := &fakeSource{}
	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	close(conf.release)
	stop := bindBlockRowsTo(context.Background(), sink, "s1", src, conf)
	defer stop()

	src.deliverRows(client.OutputRows{FromRow: 0, Rows: rowsN(2)})
	src.deliverRows(client.OutputRows{FromRow: 2, Incomplete: true})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != 1 {
		t.Fatalf("the transport received %d row deliveries, want the one ordinary batch", len(sink.rows))
	}
	if len(sink.incomplete) != 1 || sink.incomplete[0] != 2 {
		t.Fatalf("the incomplete marker reached the transport as %v, want once at row 2", sink.incomplete)
	}
}

func TestOutputStartAnchorReachesTransportSink(t *testing.T) {
	sink := &fakeSink{}
	src := &fakeSource{}
	stop := bindBlockRowsTo(context.Background(), sink, "session-1", src, nil)
	defer stop()
	anchor := proto.OutputStartScreenAnchor{Row: 5, ScreenRevision: 9, BufferKind: "alternate", BufferInstance: "2", Cols: 120, Rows: 40}
	src.mu.Lock()
	callback := src.outputStartRow
	src.mu.Unlock()
	callback(client.OutputStartRow{FromRow: 900, Anchor: &anchor})
	if len(sink.outputStartAnchors) != 1 || sink.outputStartAnchors[0] != anchor {
		t.Fatalf("output start anchors = %+v, want %+v (separate from FromRow 900)", sink.outputStartAnchors, anchor)
	}
}

// markConfirmer records every mark the bridge asks the helper for.
type markConfirmer struct {
	mu    sync.Mutex
	marks []uint64
}

func (m *markConfirmer) ConfirmWritten(_ context.Context, upToRow uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks = append(m.marks, upToRow)
	return nil
}

func (m *markConfirmer) reached(want uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mark := range m.marks {
		if mark == want {
			return true
		}
	}
	return false
}

// A RECORDING OUTLIVES THE REQUEST THAT OPENED THE PANE (the integration of
// nocx-3an1s.2.2 with the rows plane). Every call is withdrawable now, so the
// request that opens a pane carries a cancellable context and the transport
// cancels it the moment the handler returns — milliseconds after this bind, and
// long before the person's first command finishes. The confirmer runs for the
// pane's whole life: a mark the helper never receives is a row it keeps in its
// resend window, and that window is finite. When it filled, the helper dropped
// the tail of a 5000-row command and said so with its incomplete marker — the
// block ended at row 3626 and 1374 rows of a person's output were never stored
// (e2e/transcript-scroll-budget.spec.ts, both browsers).
//
// The shape asserted here is the e2e's, below it: a long command's output, its
// store slow as another content write holding the connection would make it,
// delivered after the request has ended — and every row handed on and confirmed
// through the last one.
func TestLongOutputIsRecordedAfterTheRequestThatOpenedThePaneHasEnded(t *testing.T) {
	const (
		batch   = 32
		batches = 125 // 4000 rows: a command past the helper's 20 MB resend window
	)
	// The store is busy: every answer costs a little, as another content write
	// holding the connection would make it.
	sink := &fakeSink{answer: func(from uint64, n int) (uint64, bool) {
		time.Sleep(time.Millisecond)
		return from + uint64(n), true //nolint:gosec // a test's row count, never negative
	}}
	src := &fakeSource{}
	conf := &markConfirmer{}
	ctx, cancelRequest := context.WithCancel(context.Background())
	stop := bindBlockRowsTo(ctx, sink, "s1", src, conf)
	defer stop()
	// The handler returns. This is what the transport does to the request the
	// pane was opened by, and it happens before the person types anything.
	cancelRequest()

	for i := 0; i < batches; i++ {
		src.deliverRows(client.OutputRows{FromRow: uint64(i * batch), Rows: rowsN(batch)}) //nolint:gosec // a test's index
	}

	// Every row reached the transport...
	sink.mu.Lock()
	stored := 0
	for _, delivery := range sink.rows {
		stored += len(delivery.Rows)
	}
	sink.mu.Unlock()
	if stored != batch*batches {
		t.Fatalf("the transport received %d rows, want %d: the tail of a person's output was dropped", stored, batch*batches)
	}
	// ...and the helper was told, through the last row. Without this mark it
	// keeps every row in its resend window, and a window that fills drops them.
	last := uint64(batch * batches) //nolint:gosec // a test's row count
	waittest.WaitFor(t, "the helper to be told every row was written", func() bool {
		return conf.reached(last)
	})
}
