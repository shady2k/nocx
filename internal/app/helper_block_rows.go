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
	BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row) (writtenUpTo uint64, confirm bool)
	BlockIntervalEnded(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, noFence bool)
	// BlockBoundaryLost settles the block a boundary would have closed when
	// its delivery to the helper finally failed (nocx-2v80t.3.29); the pane's
	// completion downlink reports it (boundaryLossTo).
	BlockBoundaryLost(sid session.ID, nonce [32]byte)
	// BlockClearBoundary is one sighted erase-saved-lines (nocx-2v80t.3.17),
	// on the same ordered callback sequence as the two above.
	BlockClearBoundary(sid session.ID)
}
type blockRowsConfirmationSink interface {
	AttachBlockRowsWithConfirmation(sid session.ID, confirm func(uint64))
}

// confirmer is the helper-facing half of one attachment's rows stream.
type confirmer interface {
	ConfirmWritten(ctx context.Context, upToRow uint64) error
}

// bindBlockRows registers the rows callbacks of one attachment with the
// transport and starts its confirmer. The returned stop detaches the stream
// (the transport closes what is still open) and ends the confirmer; it is
// idempotent and is bound to the session's lifetime by the caller.
func bindBlockRows(ctx context.Context, sink blockRowsSink, sid session.ID, attached *client.AttachedSession) func() {
	if sink == nil || attached == nil {
		return func() {}
	}
	return bindBlockRowsTo(ctx, sink, sid, attached, attached)
}

// rowsSource is the registration half of an attachment, split out so a test
// can drive the bridge without a helper.
type rowsSource interface {
	OnOutputRows(func(client.OutputRows))
	OnIntervalEnd(func(client.IntervalEnd))
	OnClearBoundary(func())
}

func bindBlockRowsTo(ctx context.Context, sink blockRowsSink, sid session.ID, src rowsSource, conf confirmer) func() {
	ctx, cancel := context.WithCancel(ctx)
	marks := newMarkSlot()
	if withConfirmation, ok := sink.(blockRowsConfirmationSink); ok {
		withConfirmation.AttachBlockRowsWithConfirmation(sid, marks.offer)
	} else {
		sink.AttachBlockRows(sid)
	}
	go func() {
		for {
			mark, ok := marks.next(ctx)
			if !ok {
				return
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
		if up, confirm := sink.BlockRowsArrived(sid, o.FromRow, o.LostRows, o.Rows); confirm {
			marks.offer(up)
		}
	})
	src.OnIntervalEnd(func(e client.IntervalEnd) {
		sink.BlockIntervalEnded(sid, [32]byte(e.Nonce), e.EndRow, e.Closing, e.NoFence)
	})
	src.OnClearBoundary(func() {
		sink.BlockClearBoundary(sid)
	})

	var once sync.Once
	return func() {
		once.Do(func() {
			src.OnOutputRows(nil)
			src.OnIntervalEnd(nil)
			src.OnClearBoundary(nil)
			sink.DetachBlockRows(sid)
			cancel()
		})
	}
}

// boundaryLossTo is the completion downlink's report of a lost boundary,
// routed to the transport's block stream for the session the downlink was
// bound to — the helper session id, which is the transport's session id for
// a hosted pane (bindBlockRows' own sid). A nil sink routes nothing: a pane
// whose rows are not streamed has no block for a loss to settle.
func boundaryLossTo(sink blockRowsSink) client.BoundaryLost {
	if sink == nil {
		return nil
	}
	return func(sessionID string, fence [32]byte) {
		sink.BlockBoundaryLost(session.ID(sessionID), fence)
	}
}

// markSlot holds the newest confirmed mark not yet sent. offer never blocks;
// next waits for a mark higher than the last one it returned.
type markSlot struct {
	mu      sync.Mutex
	pending uint64
	has     bool
	sent    uint64
	wake    chan struct{}
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

func (m *markSlot) next(ctx context.Context) (uint64, bool) {
	for {
		m.mu.Lock()
		if m.has {
			mark := m.pending
			m.has, m.sent = false, mark
			m.mu.Unlock()
			return mark, true
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, false
		case <-m.wake:
		}
	}
}
