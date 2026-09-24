package session

// The row stream bridge (nocx-2v80t.3.6): the session side of the streamed
// block output. The runtime hands departed rows to its RowStream the moment
// they leave the screen, under its own lock; this bridge is the RowStream,
// and its whole job is to move each hand-off to the wire without ever
// blocking the ingest that produced it and without keeping a copy — the
// owner's decision gives the helper no second buffer, so a ROW BATCH the
// pump cannot carry when the time comes is dropped, counted and folded into
// the LostRows of whichever row batch reaches the wire next.
//
// An END MARKER is never one of those drops (nocx-2v80t.3.15, the stage
// review's blocker): a dropped end marker would leave the coordinator's
// block open forever, with nothing left in the stream that could ever close
// it, so enqueueRowEmission always queues one, whatever the queue holds. The
// queue is therefore a plain FIFO guarded by a mutex rather than a bounded
// channel — an append never blocks, so the runtime's lock is never held
// waiting on a slow pump — and only ROW BATCHES are bounded and droppable;
// an end marker's append is unconditional.
//
// Order is the invariant the bridge exists to keep: the runtime emits rows
// and end markers in stream order, one FIFO queue preserves it exactly as
// produced, and one pump per session writes the frames in the order it
// dequeues them. A row can never be attributed to the wrong interval,
// because nothing between the runtime and the wire reorders, and a dropped
// row batch cannot open a gap the next delivery does not name: the runtime's
// own FromRow already advanced past what was dropped, so the drop is
// reported as an EXACT LostRows count on the next row batch actually sent,
// never invented and never rounded (ws_block_rows.go's discontinuity check
// is exact about it — content.AppendBlockRows refuses a FromRow gap whose
// LostRows does not measure it precisely).

import (
	"encoding/hex"
	"encoding/json"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// rowsPerFrame bounds one rows document before it is encoded. A batch the
// runtime hands over is at most one feed's departures, and a feed is bounded
// (sessionruntime.MaxIngestBytes), but the JSON a row costs depends on the
// geometry and the content, so the pump splits batches at a row count whose
// document is far below the carrier's bound at every geometry the product
// sizes: 32 dense full-width rows at 200 columns encode to a fraction of
// MaxOutputRowsPayloadBytes, with the style runs included. A batch of none
// is still carried: a struck feed with no readable rows is a marker that
// must reach the coordinator, so the bound bounds only the split.
const rowsPerFrame = 32

// maxQueuedRowBatches bounds the FIFO's row batches (never its end markers):
// the same depth the channel this replaces carried, so a queue this deep
// behind a wedged pump is still the bridge falling a whole queue behind, not
// a new threshold.
const maxQueuedRowBatches = 256

// rowEmission is one hand-off from the runtime: a row batch, an interval's
// end marker, or a sighted clear boundary. The rows are the runtime's gift —
// freshly copied by the emulator's report, owned by whoever takes them next
// — so the pump marshals them OFF the runtime's lock.
type rowEmission struct {
	end     bool
	clear   bool
	nonce   sessionruntime.FenceNonce
	from    uint64
	lost    uint64
	rows    []emulator.Row
	closing []emulator.Row
}

// rowBridge is the session's sessionruntime.RowStream. All three methods are
// called with the runtime's lock held: they must not block, must not call
// back into the runtime, and do nothing here but hand off.
type rowBridge struct{ hs *hostSession }

func (b *rowBridge) OutputRows(from uint64, rows []emulator.Row, lost uint64) {
	b.hs.enqueueRowEmission(rowEmission{from: from, lost: lost, rows: rows})
}

func (b *rowBridge) IntervalEnd(nonce sessionruntime.FenceNonce, endRow uint64, closing []emulator.Row) {
	b.hs.enqueueRowEmission(rowEmission{end: true, nonce: nonce, from: endRow, closing: closing})
}

// ClearBoundary hands off one sighted erase-saved-lines (nocx-2v80t.3.17), on
// the same FIFO the rows and the end markers travel, so it reaches the wire
// in the position it occurred and can never be attributed to the wrong side
// of a row.
func (b *rowBridge) ClearBoundary() {
	b.hs.enqueueRowEmission(rowEmission{clear: true})
}

// enqueueRowEmission appends one emission to the FIFO, or — for a ROW BATCH
// only, and only past maxQueuedRowBatches — drops it. Dropping is the honest
// answer to a wedged wire: blocking would stall the PTY's ingest under the
// runtime's lock, and keeping a copy is what the owner's decision forbids.
// It is a COUNTED and RECONCILED answer, never a silent one: the exact
// number of rows this batch carried joins rowsLostPending, which the next
// row batch actually delivered reports as its own LostRows (deliverRowEmission),
// so the coordinator sees precisely the gap the drop opened rather than a
// FromRow that jumped for no stated reason.
//
// An END MARKER, and a CLEAR BOUNDARY beside it, never take this path: they
// always append, because the bound exists to shed load, and shedding the one
// frame that closes a command's block — or the one frame recording that its
// saved lines were erased — would either leave that block open forever with
// nothing left in the stream that could ever close it (nocx-2v80t.3.15), or
// leave a real erase unrecorded, which the record-never-deletes design
// (nocx-zg3k3.10.3) makes the ONLY chance to hide what it bounds. Neither
// append can block — it is a mutex-guarded slice append, not an I/O wait —
// so exempting them costs the ingest nothing.
func (s *hostSession) enqueueRowEmission(em rowEmission) {
	s.rowMu.Lock()
	if !em.end && !em.clear && s.rowQueuedBatches >= maxQueuedRowBatches {
		s.rowMu.Unlock()
		s.rowsDropped.Add(1)
		s.rowsLostPending.Add(uint64(len(em.rows))) //nolint:gosec // a row count, not a byte count
		s.log.Warn("session rows not streamed: the bridge queue is full",
			"session", s.id.Session, "fromRow", em.from, "rowsDropped", len(em.rows),
			"droppedBatchesTotal", s.rowsDropped.Load())
		return
	}
	s.rowQueue = append(s.rowQueue, em)
	if !em.end && !em.clear {
		s.rowQueuedBatches++
	}
	s.rowMu.Unlock()
	// A buffered wake of one: a pending signal already says "the queue is
	// non-empty", so a second one while it is still unread would say
	// nothing more — the pump drains to empty before it waits on this
	// channel again, so a coalesced wake never leaves an emission unseen.
	select {
	case s.rowWake <- struct{}{}:
	default:
	}
}

// dequeueRowEmission pops the FIFO's head, or answers false with nothing
// queued.
func (s *hostSession) dequeueRowEmission() (rowEmission, bool) {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	if len(s.rowQueue) == 0 {
		return rowEmission{}, false
	}
	em := s.rowQueue[0]
	s.rowQueue[0] = rowEmission{}
	s.rowQueue = s.rowQueue[1:]
	if !em.end && !em.clear {
		s.rowQueuedBatches--
	}
	return em, true
}

// serveRows is the bridge's one pump. It writes each emission to every
// subscriber bound at that moment — rows are the session's stream, and each
// reader holds its own attachment — and ends when the session does. With no
// subscriber bound there is nothing to deliver and nothing to mourn: the
// rows were handed over once by the runtime, ghostty's scrollback is the
// buffer, and the mark the eventual resend reads starts at zero.
func (s *hostSession) serveRows() {
	for {
		if em, ok := s.dequeueRowEmission(); ok {
			s.deliverRowEmission(em)
			continue
		}
		select {
		case <-s.rowWake:
		case <-s.rowsDone:
			return
		}
	}
}

// deliverRowEmission marshals once and sends per subscriber. A send error
// ends nothing here: it already means the connection under that sink is
// dying, and releaseConnection does the teardown — one dead reader must not
// take the others' frames with it.
func (s *hostSession) deliverRowEmission(em rowEmission) {
	if em.clear {
		payload, err := json.Marshal(proto.ClearBoundaryDoc{Kind: "clear"})
		if err != nil {
			s.log.Warn("session clear boundary not encodable", "session", s.id.Session, "err", err)
			return
		}
		s.mu.Lock()
		subs := s.subscribersLocked()
		s.mu.Unlock()
		for _, sub := range subs {
			if err := sub.sink.SendClearBoundary(proto.ClearBoundaryFrame{
				Session: s.raw, Subscriber: sub.raw, Payload: payload,
			}); err != nil {
				s.log.Warn("session clear boundary not delivered", "session", s.id.Session,
					"subscriber", sub.id, "err", err)
			}
		}
		return
	}
	if em.end {
		closing, err := encodedRowsOrNothing(em.closing)
		if err != nil {
			s.log.Warn("session closing screen not encodable", "session", s.id.Session, "err", err)
			return
		}
		payload, err := json.Marshal(proto.IntervalEndDoc{
			Nonce:   hex.EncodeToString(em.nonce[:]),
			EndRow:  em.from,
			Closing: closing,
		})
		if err != nil {
			s.log.Warn("session interval end not encodable", "session", s.id.Session, "err", err)
			return
		}
		s.mu.Lock()
		subs := s.subscribersLocked()
		s.mu.Unlock()
		for _, sub := range subs {
			// The sink encodes the frame; Payload is the document bytes,
			// exactly as the rows branch hands them over.
			if err := sub.sink.SendIntervalEnd(proto.IntervalEndFrame{
				Session: s.raw, Subscriber: sub.raw, EndRow: em.from, Payload: payload,
			}); err != nil {
				s.log.Warn("session interval end not delivered", "session", s.id.Session,
					"subscriber", sub.id, "endRow", em.from, "err", err)
			}
		}
		return
	}
	// LostRows carries two facts folded into one count, both exact: the
	// runtime's own em.lost (a struck feed immediately before em.from) and
	// whatever the bridge itself dropped since the last row batch it
	// actually delivered (rowsLostPending, swapped and cleared here so a
	// drop is reported exactly once, on the very next delivery). Both name
	// the same gap — rows the coordinator's FromRow arithmetic must account
	// for or refuse the delivery as discontinuous — so they add.
	lost := em.lost + s.rowsLostPending.Swap(0)
	doc := proto.OutputRowsDoc{FromRow: em.from, LostRows: lost}
	for start := 0; start <= len(em.rows); start += rowsPerFrame {
		stop := start + rowsPerFrame
		if stop > len(em.rows) {
			stop = len(em.rows)
		}
		var err error
		doc.Rows, err = sessionruntime.EncodeRows(em.rows[start:stop])
		if err != nil {
			s.log.Warn("session rows not encodable", "session", s.id.Session, "err", err)
			return
		}
		payload, err := json.Marshal(doc)
		if err != nil {
			s.log.Warn("session rows not encodable", "session", s.id.Session, "err", err)
			return
		}
		s.mu.Lock()
		subs := s.subscribersLocked()
		s.mu.Unlock()
		for _, sub := range subs {
			if err := sub.sink.SendOutputRows(proto.OutputRowsFrame{
				Session: s.raw, Subscriber: sub.raw, FromRow: doc.FromRow, Payload: payload,
			}); err != nil {
				s.log.Warn("session rows not delivered", "session", s.id.Session,
					"subscriber", sub.id, "fromRow", doc.FromRow, "err", err)
			}
		}
		if stop == len(em.rows) {
			return
		}
		doc.FromRow += uint64(stop - start) // #nosec G115 -- slice arithmetic, never negative
		// The gap LostRows states sits immediately before em.from — the
		// batch's own first row — and belongs to the FIRST split frame
		// alone; a later frame of the SAME batch starts exactly where the
		// one before it left off, with no gap of its own, so it must not
		// keep repeating the first frame's count (that would claim the same
		// loss again for every split and fail the coordinator's exact
		// FromRow-minus-LostRows check).
		doc.LostRows = 0
	}
}

// encodedRowsOrNothing encodes closing rows for an end marker, or nothing:
// json.RawMessage(nil) marshals as null, and null is the contract's answer
// for a screen that could not be read — an answer, never an omission. An
// encode FAILURE is neither: it is returned, and the caller says so rather
// than shipping an end marker whose closing screen quietly became null.
func encodedRowsOrNothing(rows []emulator.Row) (json.RawMessage, error) {
	if rows == nil {
		return nil, nil
	}
	raw, err := sessionruntime.EncodeRows(rows)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// confirmRows advances the session's confirmed-written mark: the
// coordinator's acknowledgement that every row and end marker up to
// upToRow is written where it must survive. The mark only ever moves
// forward — an acknowledgement behind it is a stale redelivery of one that
// landed, and is answered ok rather than refused, the way an idempotent ack
// is — and one ahead of what the session ever departed is refused by name,
// because confirming rows that were never sent would make the next resend
// skip output nobody holds.
func (s *hostSession) confirmRows(sink Sink, id proto.SubscriberID, upToRow uint64) error {
	s.mu.Lock()
	sub, ok := s.subs[id]
	if !ok || sub.sink != sink {
		s.mu.Unlock()
		return ErrNotAttached
	}
	departed := s.runtime.DepartedRowCount()
	if upToRow > departed {
		s.mu.Unlock()
		return ErrConfirmAhead
	}
	if upToRow > s.rowsConfirmed {
		s.rowsConfirmed = upToRow
	}
	s.mu.Unlock()
	return nil
}
