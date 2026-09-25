package session

// The row stream bridge (nocx-2v80t.3.6): the session side of the streamed
// block output. The runtime hands departed rows to its RowStream the moment
// they leave the screen, under its own lock; this bridge is the RowStream,
// and its whole job is to move each hand-off to the wire without ever
// blocking the ingest that produced it and without keeping a copy — the
// owner's decision gives the helper no second buffer, so what the pump
// cannot carry when the time comes is shed, counted, and — for rows — stated
// on the wire as a loss AT THE POSITION it happened.
//
// The queue is a plain FIFO guarded by a mutex rather than a channel — an
// append never blocks, so the runtime's lock is never held waiting on a slow
// pump — and EVERY kind in it is bounded (nocx-2v80t.3.26, AD-10): row
// batches by maxQueuedRowBatches, end markers and clear boundaries together
// by maxQueuedMarkers. The two bounds are separate because what each costs
// to shed is different. A row batch shed is an exact, positioned loss the
// coordinator records. A marker is a fact with no row count: an end marker
// closes a command's block (nocx-2v80t.3.15), a clear boundary records an
// erase (nocx-2v80t.3.17), so markers keep a budget of their own that a
// flood of rows can never spend — only a flood of MARKERS behind a wedged
// wire reaches it. Past it nothing is dropped (nocx-2v80t.3.31): what
// follows folds into one overflow record of fixed size, and every end in it
// still reaches the coordinator — without its fence and closing screen, as
// an end its block is stored as a gap for — and every clear in it is stated
// by the last one. Two clear boundaries with nothing between them are one
// fact, and the second is folded into the first.
//
// Order is the invariant the bridge exists to keep: the runtime emits rows
// and markers in stream order, one FIFO queue preserves it exactly as
// produced, and one pump per session writes the frames in the order it
// dequeues them. A shed batch's loss is attached AT ENQUEUE, to the next
// batch the bridge accepts — never to the next one it happens to DELIVER,
// which may have been queued before the drop — so every delivered batch's
// FromRow minus its LostRows is exactly where the batch before it ended
// (content.AppendBlockRows refuses a loss that names no gap). A marker
// accepted while a loss is still owed is preceded by a loss-only carrier at
// the end of the gap, so the loss lands inside the interval it happened in,
// and an end marker's closing screen lands after it rather than at a
// shortened cursor.

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

// maxQueuedRowBatches bounds the FIFO's row batches: the same depth the
// channel this replaces carried, so a queue this deep behind a wedged pump is
// still the bridge falling a whole queue behind, not a new threshold.
const maxQueuedRowBatches = 256

// maxQueuedMarkers bounds the FIFO's end markers and clear boundaries
// together (nocx-2v80t.3.26). An ordinary session queues one per command
// end or erase and the pump drains them as fast as rows; sixty-four behind a
// wedged wire is sixty-four commands that ended with nothing delivered,
// which is the wire having stopped, not a slow moment. Each accepted marker
// may bring one loss-only carrier ahead of it, and what arrives past the
// budget folds into ONE overflow record (markerOverflow), so the queue never
// holds more than maxQueuedRowBatches + 2*maxQueuedMarkers + 1 emissions.
const maxQueuedMarkers = 64

// markerOverflow is everything the bridge was handed after the marker budget
// filled, folded into one record of fixed size (nocx-2v80t.3.31). Nothing in
// it is dropped: it keeps what each kind needs to be STATED on the wire,
// which is less than the kind itself carried.
//
//   - An END keeps only that it happened. Its fence and its closing screen
//     are what cost memory, and without them it is still a boundary: it is
//     delivered as an end settled WITHOUT its fence (the zero nonce,
//     noFence), which the coordinator resolves to the block then current and
//     stores as a gap — the block says output may be missing, which is true.
//     The alternative, dropping the end, left that block open until detach
//     with every later block queued behind it.
//   - A CLEAR keeps only the LAST one, and which ends came before it: a
//     later erase hides everything an earlier one hid, so the last clear
//     between two ends states them all, in its place among the ends.
//   - ROWS keep their count: every row folded here is shed, and the whole
//     gap is stated once, ahead of the ends, at the end of the gap
//     (rowStreamNext), so the store's arithmetic still names it exactly.
type markerOverflow struct {
	lost       uint64
	next       uint64
	endRow     uint64
	endsBefore uint64
	clear      bool
	endsAfter  uint64
}

// rowEmission is one hand-off from the runtime: a row batch, an interval's
// end marker, or a sighted clear boundary — or the bridge's own loss-only
// carrier, which states a shed loss ahead of a marker. The rows are the
// runtime's gift — freshly copied by the emulator's report, owned by whoever
// takes them next — so the pump marshals them OFF the runtime's lock.
type rowEmission struct {
	end     bool
	clear   bool
	carrier bool
	nonce   sessionruntime.FenceNonce
	from    uint64
	lost    uint64
	rows    []emulator.Row
	closing []emulator.Row
	// noFence is an end marker's settledWithoutFence (nocx-2v80t.3.29).
	noFence bool
	// overflow, when set, makes this emission the queue's one fold of what
	// arrived past the marker budget (nocx-2v80t.3.31).
	overflow *markerOverflow
}

// batch is whether this emission is a runtime row batch: bounded by
// maxQueuedRowBatches, and shed as a positioned loss. A carrier is not one —
// it rides its marker's budget.
func (em rowEmission) batch() bool {
	return !em.end && !em.clear && !em.carrier && em.overflow == nil
}

// marker is whether this emission spends maxQueuedMarkers.
func (em rowEmission) marker() bool { return em.end || em.clear }

// rowBridge is the session's sessionruntime.RowStream. All three methods are
// called with the runtime's lock held: they must not block, must not call
// back into the runtime, and do nothing here but hand off.
type rowBridge struct{ hs *hostSession }

func (b *rowBridge) OutputRows(from uint64, rows []emulator.Row, lost uint64) {
	b.hs.enqueueRowEmission(rowEmission{from: from, lost: lost, rows: rows})
}

func (b *rowBridge) IntervalEnd(nonce sessionruntime.FenceNonce, endRow uint64, closing []emulator.Row, settledWithoutFence bool) {
	b.hs.enqueueRowEmission(rowEmission{end: true, nonce: nonce, from: endRow, closing: closing, noFence: settledWithoutFence})
}

// ClearBoundary hands off one sighted erase-saved-lines (nocx-2v80t.3.17), on
// the same FIFO the rows and the end markers travel, so it reaches the wire
// in the position it occurred and can never be attributed to the wrong side
// of a row.
func (b *rowBridge) ClearBoundary() {
	b.hs.enqueueRowEmission(rowEmission{clear: true})
}

// enqueueRowEmission appends one emission to the FIFO, or sheds it past its
// kind's bound. Shedding is the honest answer to a wedged wire: blocking
// would stall the PTY's ingest under the runtime's lock, and keeping a copy
// is what the owner's decision forbids. It is a COUNTED answer, never a
// silent one.
//
// A ROW BATCH shed joins rowsLostPending — its rows AND whatever loss it was
// itself carrying, because every loss spends the indices it names — and
// the next batch ACCEPTED carries the whole gap as its LostRows. A MARKER
// accepted while a loss is owed is preceded by a loss-only carrier at the
// end of the gap (rowStreamNext), so the loss is stated inside the interval
// it happened in. A MARKER past maxQueuedMarkers is never shed
// (nocx-2v80t.3.31): it opens the queue's overflow record, and from then
// until the pump reaches that record everything handed over folds into it
// (foldOverflowLocked), so order is kept and the memory is one record.
func (s *hostSession) enqueueRowEmission(em rowEmission) {
	s.rowMu.Lock()
	if n := len(s.rowQueue); n > 0 && s.rowQueue[n-1].overflow != nil {
		s.foldOverflowLocked(s.rowQueue[n-1].overflow, em)
		s.rowMu.Unlock()
		return
	}
	switch {
	case em.batch():
		s.rowStreamNext = em.from + uint64(len(em.rows)) // #nosec G115 -- len is never negative
		if s.rowQueuedBatches >= maxQueuedRowBatches {
			s.rowsLostPending += em.lost + uint64(len(em.rows)) // #nosec G115 -- len is never negative
			pending := s.rowsLostPending
			s.rowMu.Unlock()
			s.rowsDropped.Add(1)
			s.log.Warn("session rows not streamed: the bridge queue is full",
				"session", s.id.Session, "fromRow", em.from, "rowsDropped", len(em.rows),
				"lostPending", pending, "droppedBatchesTotal", s.rowsDropped.Load())
			return
		}
		em.lost += s.rowsLostPending
		s.rowsLostPending = 0
		s.rowQueue = append(s.rowQueue, em)
		s.rowQueuedBatches++
	case em.clear && s.rowsLostPending == 0 && len(s.rowQueue) > 0 && s.rowQueue[len(s.rowQueue)-1].clear:
		// The same erase twice with nothing between: one fact, already
		// queued. Folding it sheds nothing.
		s.rowMu.Unlock()
		return
	case s.rowQueuedMarkers >= maxQueuedMarkers:
		o := &markerOverflow{lost: s.rowsLostPending, next: s.rowStreamNext}
		s.rowsLostPending = 0
		s.rowQueue = append(s.rowQueue, rowEmission{overflow: o})
		s.foldOverflowLocked(o, em)
		s.log.Warn("session markers past the bridge's budget: folding what follows until the wire drains",
			"session", s.id.Session, "endMarker", em.end, "clearBoundary", em.clear, "endRow", em.from)
	default:
		if s.rowsLostPending > 0 {
			s.rowQueue = append(s.rowQueue, rowEmission{
				carrier: true, from: s.rowStreamNext, lost: s.rowsLostPending,
			})
			s.rowsLostPending = 0
		}
		s.rowQueue = append(s.rowQueue, em)
		s.rowQueuedMarkers++
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

// foldOverflowLocked folds one hand-off into the overflow record at the
// queue's tail (markerOverflow says what each kind keeps). Called under rowMu.
func (s *hostSession) foldOverflowLocked(o *markerOverflow, em rowEmission) {
	s.markersFolded.Add(1)
	switch {
	case em.end:
		if em.from > o.endRow {
			o.endRow = em.from
		}
		if o.clear {
			o.endsAfter++
		} else {
			o.endsBefore++
		}
	case em.clear:
		// Every end folded so far came before THIS clear, the one that
		// stands for all of them.
		o.endsBefore += o.endsAfter
		o.endsAfter = 0
		o.clear = true
	default:
		s.rowStreamNext = em.from + uint64(len(em.rows)) // #nosec G115 -- len is never negative
		o.lost += em.lost + uint64(len(em.rows))         // #nosec G115 -- len is never negative
		o.next = s.rowStreamNext
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
	switch {
	case em.batch():
		s.rowQueuedBatches--
	case em.marker():
		s.rowQueuedMarkers--
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
	if o := em.overflow; o != nil {
		s.deliverOverflow(o)
		return
	}
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
			NoFence: em.noFence,
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
	// LostRows carries two facts folded into one count, both spending the
	// indices they name: the runtime's own loss (a struck feed immediately
	// before em.from) and whatever the bridge shed between the batch
	// before this one and this one — attached at ENQUEUE, so it is this
	// batch's gap and no other's (enqueueRowEmission).
	doc := proto.OutputRowsDoc{FromRow: em.from, LostRows: em.lost}
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

// deliverOverflow states everything the overflow record folded, in the
// order that keeps it true (markerOverflow): the shed rows as one loss at the
// end of their gap, then every end that came before the last clear, the
// clear, and every end after it. Each end is settled WITHOUT its fence — the
// zero nonce the coordinator resolves to its current block, and noFence, so
// that block is stored as the gap it is. The record is the pump's alone once
// dequeued: a fold only ever reaches the queue's tail, under rowMu.
func (s *hostSession) deliverOverflow(o *markerOverflow) {
	if o.lost > 0 {
		s.deliverRowEmission(rowEmission{carrier: true, from: o.next, lost: o.lost})
	}
	end := rowEmission{end: true, from: max(o.endRow, o.next), noFence: true}
	for range o.endsBefore {
		s.deliverRowEmission(end)
	}
	if o.clear {
		s.deliverRowEmission(rowEmission{clear: true})
	}
	for range o.endsAfter {
		s.deliverRowEmission(end)
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
