package app

import (
	"context"
	"sync"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/client"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// THE STREAMED BLOCK OUTPUT, wired (nocx-2v80t.3.6 + nocx-2v80t.3.7). The
// helper streams the rows that leave a session's screen, and each command's
// end, on one ordered channel; the transport's block stream appends the rows
// of a kept command to its block in history and seals it at the
// authenticated end. This file is the one place the two halves meet: it
// registers the helper client's callbacks on an attachment and hands every
// delivery to the transport, and it carries the transport's "written up to
// here" answer back to the helper as its confirmed mark.
//
// Why the confirmation has its own goroutine: the row callbacks run on the
// helper client's read loop, and ConfirmWritten is a request whose response
// arrives on that same loop — calling it from the callback would wait for a
// reply the waiting goroutine itself has to read. So the callback only
// records the newest mark, and one goroutine per attachment sends it. Marks
// are coalesced latest-wins: the mark is a watermark, so sending only the
// highest one is sending all of them, and a slow helper never makes the read
// loop wait.

// blockRowsSink is the transport's half, as this file needs it.
// *transport.WSServer satisfies it; a nil sink wires nothing.
type blockRowsSink interface {
	AttachBlockRows(sid session.ID)
	DetachBlockRows(sid session.ID)
	// HelperSessionEnded settles the session's streaming because THE
	// HELPER reported the session's end (ADR-0076) — sealed, unlike the
	// coordinator's own detach, which changes no block.
	HelperSessionEnded(sid session.ID)
	BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row, lostCause string) (writtenUpTo uint64, confirm bool)
	BlockIntervalEnded(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, noFence bool)
	// BlockBoundaryLost settles the block a boundary would have closed when
	// its delivery to the helper finally failed (nocx-2v80t.3.29); the pane's
	// completion downlink reports it (boundaryLossTo).
	BlockBoundaryLost(ctx context.Context, sid session.ID, nonce [32]byte)
	// BlockClearBoundary is one sighted erase-saved-lines (nocx-2v80t.3.17),
	// on the same ordered callback sequence as the two above.
	BlockClearBoundary(sid session.ID)
	BlockOutputStartPlaneAttached(sid session.ID)
	SetBlockOutputReplay(sid session.ID, replay func() error)
	BlockOutputStartRow(sid session.ID, fromRow uint64)
	// BlockOutputIncomplete is the helper's one marker that its row buffer
	// overflowed (nocx-2v80t.3.36), on the same ordered sequence.
	BlockOutputIncomplete(sid session.ID, fromRow uint64)
}
type blockRowsConfirmationSink interface {
	AttachBlockRowsWithConfirmation(sid session.ID, confirm func(uint64))
}

// confirmer is the helper-facing half of one attachment's rows stream.
type confirmer interface {
	ConfirmWritten(ctx context.Context, upToRow uint64) error
}

// bindHeldBlockRows registers the rows callbacks of one attachment with the
// transport and starts its confirmer. The returned stop detaches the stream
// (the transport closes what is still open) and ends the confirmer; it is
// idempotent and is bound to the session's lifetime by the caller. The
// attachment's rows plane was held from before its attach
// (holdRowsBeforeAttach), so the frames that arrived in between reach the
// stream once it is bound; a nil hold binds the attachment directly.
func bindHeldBlockRows(ctx context.Context, sink blockRowsSink, sid session.ID, attached *client.AttachedSession, held *heldRows) func() {
	return bindHeldBlockRowsAfter(ctx, sink, sid, attached, held, nil)
}

// bindHeldBlockRowsAfter is bindHeldBlockRows for a re-adopted pane: the held
// rows reach the stream once replayed closes — the leg applied the window the
// helper handed it (heldRows.bindAfter). A nil replayed releases at the bind.
func bindHeldBlockRowsAfter(ctx context.Context, sink blockRowsSink, sid session.ID, attached *client.AttachedSession, held *heldRows, replayed <-chan struct{}) func() {
	if sink == nil || attached == nil {
		return func() {}
	}
	if held == nil {
		return bindBlockRowsTo(ctx, sink, sid, attached, attached)
	}
	return held.bindAfter(ctx, sink, sid, attached, replayed)
}

// rowsSource is the registration half of an attachment, split out so a test
// can drive the bridge without a helper.
type rowsSource interface {
	OnOutputRows(func(client.OutputRows))
	OnIntervalEnd(func(client.IntervalEnd))
	OnClearBoundary(func())
	OnOutputStartRow(func(client.OutputStartRow))
}

func bindBlockRowsTo(ctx context.Context, sink blockRowsSink, sid session.ID, src rowsSource, conf confirmer) func() {
	ctx, cancel := context.WithCancel(ctx)
	marks := newMarkSlot()
	sink.BlockOutputStartPlaneAttached(sid)
	var replay func() error
	var replayNow func() error
	if requester, ok := conf.(interface{ ReplayOutputRows(context.Context) error }); ok {
		replay = func() error { marks.requestReplay(); return nil }
		replayNow = func() error { return requester.ReplayOutputRows(ctx) }
	}
	sink.SetBlockOutputReplay(sid, replay)
	if withConfirmation, ok := sink.(blockRowsConfirmationSink); ok {
		withConfirmation.AttachBlockRowsWithConfirmation(sid, marks.offer)
	} else {
		sink.AttachBlockRows(sid)
	}
	go func() {
		for {
			mark, replayRequested, ok := marks.next(ctx)
			if !ok {
				return
			}
			if replayRequested {
				if replayNow != nil {
					if err := replayNow(); err != nil && ctx.Err() == nil {
						nocxlog.From(ctx).Warn("block rows retained-window replay request failed",
							"session", string(sid), "error", err)
					}
				}
				continue
			}
			if err := conf.ConfirmWritten(ctx, mark); err != nil && ctx.Err() == nil {
				// A refused or lost confirmation is not fatal: the helper's
				// mark stays behind, which is the safe direction — the resend
				// (nocx-zg3k3.5.3) offers those rows again.
				nocxlog.From(ctx).Warn("block rows confirmation not delivered",
					"session", string(sid), "upToRow", mark, "error", err)
			}
		}
	}()

	src.OnOutputRows(func(o client.OutputRows) {
		if o.Incomplete {
			sink.BlockOutputIncomplete(sid, o.FromRow)
			return
		}
		if up, confirm := sink.BlockRowsArrived(sid, o.FromRow, o.LostRows, o.Rows, o.LostCause); confirm {
			marks.offer(up)
		}
	})
	src.OnIntervalEnd(func(e client.IntervalEnd) {
		sink.BlockIntervalEnded(sid, [32]byte(e.Nonce), e.EndRow, e.Closing, e.NoFence)
	})
	src.OnClearBoundary(func() {
		sink.BlockClearBoundary(sid)
	})
	src.OnOutputStartRow(func(mark client.OutputStartRow) {
		sink.BlockOutputStartRow(sid, mark.FromRow)
	})

	var once sync.Once
	return func() {
		once.Do(func() {
			src.OnOutputRows(nil)
			src.OnIntervalEnd(nil)
			src.OnClearBoundary(nil)
			src.OnOutputStartRow(nil)
			sink.SetBlockOutputReplay(sid, nil)
			sink.DetachBlockRows(sid)
			cancel()
		})
	}
}

// heldRows is an attachment's rows plane from the instant the attachment
// exists until the transport's stream for its session is bound
// (nocx-zg3k3.5.11). The helper binds a new subscriber and wakes its row pump
// before it answers the attach, so the rows a returning coordinator is owed —
// the read-back from its confirmed mark, and live rows that departed since —
// can arrive before Attach has even returned; and the stream is bound only
// after, once the attach says what it resumed. Registered through
// client.ObserveBeforeAttach, the hold takes every frame from the first one:
// until bind it keeps them in arrival order, bind hands them to the stream
// once the stream exists (after its re-bind of the store's open block), and
// from then on each frame passes straight through. Before this the frames
// reached an attachment nobody was watching yet and were dropped without a
// word — the loaded restart acceptance sealed its block at 193 of 300 rows.
//
// It is a rowsSource, so the ordinary bridge (bindBlockRowsTo) registers its
// consumers on it exactly as it would on the attachment.
type heldRows struct {
	mu             sync.Mutex
	bound          bool
	held           []heldFrame
	rows           func(client.OutputRows)
	end            func(client.IntervalEnd)
	clear          func()
	outputStartRow func(client.OutputStartRow)
}

// heldFrame is one rows-plane frame, of whichever kind, in arrival order.
type heldFrame struct {
	rows           *client.OutputRows
	end            *client.IntervalEnd
	clear          bool
	outputStartRow *client.OutputStartRow
}

// observe registers the hold on an attachment's rows plane.
func (h *heldRows) observe(src rowsSource) {
	src.OnOutputRows(h.takeRows)
	src.OnIntervalEnd(h.takeEnd)
	src.OnClearBoundary(h.takeClear)
	src.OnOutputStartRow(h.takeOutputStartRow)
}

// holdRowsBeforeAttach is the attach option that registers a hold, and the
// hold it will register. A nil sink streams no rows, so nothing is held.
func holdRowsBeforeAttach(sink blockRowsSink) (client.AttachOption, *heldRows) {
	if sink == nil {
		return func(*client.AttachedSession) {}, nil
	}
	h := &heldRows{}
	return client.ObserveBeforeAttach(func(a *client.AttachedSession) { h.observe(a) }), h
}

// takeRows, takeEnd and takeClear are the attachment's observers. They run
// on the connection's read loop and call the stream under the hold's lock, so
// a frame arriving while bind releases the held ones waits its turn behind
// them.
func (h *heldRows) takeRows(o client.OutputRows) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.bound {
		h.held = append(h.held, heldFrame{rows: &o})
		return
	}
	if h.rows != nil {
		h.rows(o)
	}
}

func (h *heldRows) takeEnd(e client.IntervalEnd) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.bound {
		h.held = append(h.held, heldFrame{end: &e})
		return
	}
	if h.end != nil {
		h.end(e)
	}
}

func (h *heldRows) takeClear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.bound {
		h.held = append(h.held, heldFrame{clear: true})
		return
	}
	if h.clear != nil {
		h.clear()
	}
}

func (h *heldRows) takeOutputStartRow(mark client.OutputStartRow) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.bound {
		h.held = append(h.held, heldFrame{outputStartRow: &mark})
		return
	}
	if h.outputStartRow != nil {
		h.outputStartRow(mark)
	}
}

func (h *heldRows) OnOutputStartRow(f func(client.OutputStartRow)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outputStartRow = f
}

// OnOutputRows, OnIntervalEnd and OnClearBoundary make the hold the bridge's
// rowsSource: the consumers the bridge registers are the stream's.
func (h *heldRows) OnOutputRows(f func(client.OutputRows)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rows = f
}

func (h *heldRows) OnIntervalEnd(f func(client.IntervalEnd)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.end = f
}

func (h *heldRows) OnClearBoundary(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clear = f
}

// release delivers what was held, in arrival order, and lets every later
// frame pass straight through.
func (h *heldRows) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.held {
		switch {
		case f.rows != nil:
			if h.rows != nil {
				h.rows(*f.rows)
			}
		case f.end != nil:
			if h.end != nil {
				h.end(*f.end)
			}
		case f.clear:
			if h.clear != nil {
				h.clear()
			}
		case f.outputStartRow != nil:
			if h.outputStartRow != nil {
				h.outputStartRow(*f.outputStartRow)
			}
		}
	}
	h.held = nil
	h.bound = true
}

// bindAfter binds the transport's stream to the held attachment now — the
// re-bind of the store's open block and the consumers, through the ordinary
// bridge — answers the bridge's stop, and keeps holding the rows plane until ready closes: a
// re-adopted pane's replayed lifecycle window applied (nocx-zg3k3.5.11). The
// read-back the helper sends at the attach starts at the command's first row,
// and the command's block may be opened only by a start frame that window
// carries — one the coordinator that went away left to this one. Released
// before that, the rows find no block, are dropped unconfirmed, and the
// read-back is not sent twice. Everything arriving meanwhile is held behind
// them, so the stream sees one order. A nil ready releases at once: a fresh
// open, with no window to replay.
func (h *heldRows) bindAfter(ctx context.Context, sink blockRowsSink, sid session.ID, conf confirmer, ready <-chan struct{}) func() {
	stop := bindBlockRowsTo(ctx, sink, sid, h, conf)
	if ready == nil {
		h.release()
		return stop
	}
	go func() {
		<-ready
		h.release()
	}()
	return stop
}

// boundaryLossTo is the completion downlink's report of a lost boundary,
// routed to the transport's block stream for the session the downlink was
// bound to — the helper session id, which is the transport's session id for
// a hosted pane (bindHeldBlockRows' own sid). A nil sink routes nothing: a pane
// whose rows are not streamed has no block for a loss to settle.
func boundaryLossTo(sink blockRowsSink) client.BoundaryLost {
	if sink == nil {
		return nil
	}
	return func(ctx context.Context, sessionID string, fence [32]byte) {
		sink.BlockBoundaryLost(ctx, session.ID(sessionID), fence)
	}
}

// markSlot holds the newest confirmed mark not yet sent. offer never blocks;
// next waits for a mark higher than the last one it returned.
type markSlot struct {
	mu              sync.Mutex
	pending         uint64
	has             bool
	sent            uint64
	replayRequested bool
	wake            chan struct{}
}

func newMarkSlot() *markSlot { return &markSlot{wake: make(chan struct{}, 1)} }

func (m *markSlot) offer(mark uint64) {
	m.mu.Lock()
	if mark > m.sent && (!m.has || mark > m.pending) {
		m.pending, m.has = mark, true
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *markSlot) requestReplay() {
	m.mu.Lock()
	m.replayRequested = true
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *markSlot) next(ctx context.Context) (mark uint64, replayRequested, ok bool) {
	for {
		m.mu.Lock()
		if m.replayRequested {
			m.replayRequested = false
			m.mu.Unlock()
			return 0, true, true
		}
		if m.has {
			mark := m.pending
			m.has, m.sent = false, mark
			m.mu.Unlock()
			return mark, false, true
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, false, false
		case <-m.wake:
		}
	}
}
