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
// pump — and it is bounded in BYTES (nocx-2v80t.3.36, AD-10): rowBufferBytes,
// the person's setting, sent at spawn. Rows, end markers and clear
// boundaries all spend it.
//
// When a hand-off would not fit, the buffer has overflowed, and the owner's
// rule applies: the block in flight ENDS, INCOMPLETE. The bridge keeps ONE
// marker saying so — a rows document with no rows and `incomplete` — rather
// than the rows or markers it cannot hold, and it records nothing until the
// next command starts after the stream is healthy: everything handed over
// until the pump has delivered that marker is dropped (rowsDropping), and
// after it only the rows are, until the runtime's next end marker, which is
// carried with its own fence and closes the interval that was running
// through the overflow (rowsAwaitingBoundary). The rows after it are the
// next command's, recorded again. Nothing is folded and no end is replaced
// by a stand-in: an end or a clear that fell inside the overflow is gone,
// and the coordinator settles each command whose end it never received from
// that command's own completion. A clear lost there stays lost — losing a
// clear is safer than losing output.
//
// Order is the invariant the bridge exists to keep: the runtime emits rows
// and markers in stream order, one FIFO queue preserves it exactly as
// produced, and one pump per session writes the frames in the order it
// dequeues them. Nothing between the runtime and the wire reorders, and
// nothing that is queued is ever dropped, so every delivered batch's FromRow
// minus its LostRows is exactly where the batch before it ended
// (content.AppendBlockRows refuses a loss that names no gap).

import (
	"encoding/hex"
	"encoding/json"
	"unsafe"

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

// rowRecording is the bridge's state: recording, dropping everything until
// the pump has delivered the incomplete marker, or dropping rows until the
// next end marker starts a command the stream can record whole.
type rowRecording uint8

const (
	rowsRecording rowRecording = iota
	rowsDropping
	rowsAwaitingBoundary
)

// rowEmission is one hand-off from the runtime: a row batch, an interval's
// end marker, or a sighted clear boundary — or the bridge's own incomplete
// marker, which states that the buffer overflowed. The rows are the
// runtime's gift — freshly copied by the emulator's report, owned by whoever
// takes them next — so the pump marshals them OFF the runtime's lock.
type rowEmission struct {
	end     bool
	clear   bool
	nonce   sessionruntime.FenceNonce
	from    uint64
	lost    uint64
	rows    []emulator.Row
	closing []emulator.Row
	// noFence is an end marker's settledWithoutFence (nocx-2v80t.3.29).
	noFence bool
	// incomplete makes this emission the buffer's one overflow marker
	// (nocx-2v80t.3.36).
	incomplete bool
	// bytes is what the emission costs the buffer, fixed at enqueue.
	bytes int64
}

// emissionBytes is what one emission costs the row buffer: the rows it
// carries, cell by cell as the emulator hands them over, plus a fixed
// overhead for the emission itself. An estimate of the heap it holds — the
// same order as the truth, never a count that could be gamed by a row of
// empty cells.
func emissionBytes(em rowEmission) int64 {
	const emissionOverhead = int64(unsafe.Sizeof(rowEmission{}))
	n := emissionOverhead
	for _, rows := range [2][]emulator.Row{em.rows, em.closing} {
		for _, r := range rows {
			n += int64(unsafe.Sizeof(r)) + int64(len(r.Cells))*int64(unsafe.Sizeof(emulator.Cell{}))
			for _, c := range r.Cells {
				n += int64(len(c.Grapheme))
			}
		}
	}
	return n
}

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

// enqueueRowEmission appends one emission to the FIFO if the buffer can hold
// it, and otherwise ends the block in flight incomplete (the header's rule).
// Nothing here blocks: an append and a byte count, under rowMu.
func (s *hostSession) enqueueRowEmission(em rowEmission) {
	s.rowMu.Lock()
	batch := !em.end && !em.clear
	if batch {
		s.rowStreamNext = em.from + uint64(len(em.rows)) // #nosec G115 -- len is never negative
	}
	switch s.rowState {
	case rowsDropping:
		s.rowMu.Unlock()
		return
	case rowsAwaitingBoundary:
		if !em.end {
			s.rowMu.Unlock()
			return
		}
	}
	em.bytes = emissionBytes(em)
	budget := s.rowBufferBytes
	if budget <= 0 {
		budget = DefaultRowBufferBytes
	}
	if s.rowQueuedBytes+em.bytes > budget {
		// The first row this buffer does not record: the dropped batch's own
		// first, or where the stream stood when a marker would not fit.
		from := s.rowStreamNext
		if batch {
			from = em.from
		}
		marker := rowEmission{incomplete: true, from: from}
		marker.bytes = emissionBytes(marker)
		s.rowQueue = append(s.rowQueue, marker)
		s.rowQueuedBytes += marker.bytes
		s.rowState = rowsDropping
		s.rowMu.Unlock()
		s.rowsIncomplete.Add(1)
		s.log.Warn("session row buffer overflowed: the block in flight ends incomplete",
			"session", s.id.Session, "fromRow", from, "bufferBytes", budget,
			"overflowsTotal", s.rowsIncomplete.Load())
		s.wakeRows()
		return
	}
	if em.end && s.rowState == rowsAwaitingBoundary {
		// The next command starts after this end, with the stream healthy.
		s.rowState = rowsRecording
	}
	s.rowQueue = append(s.rowQueue, em)
	s.rowQueuedBytes += em.bytes
	s.rowMu.Unlock()
	s.wakeRows()
}

// wakeRows is a buffered wake of one: a pending signal already says "the
// queue is non-empty", so a second one while it is still unread would say
// nothing more — the pump drains to empty before it waits on this channel
// again, so a coalesced wake never leaves an emission unseen.
func (s *hostSession) wakeRows() {
	select {
	case s.rowWake <- struct{}{}:
	default:
	}
}

// dequeueRowEmission pops the FIFO's head, or answers false with nothing
// queued. Popping the incomplete marker is where the stream is healthy
// again: everything queued before it has gone, so from here the bridge waits
// only for the next command to start.
func (s *hostSession) dequeueRowEmission() (rowEmission, bool) {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	if len(s.rowQueue) == 0 {
		return rowEmission{}, false
	}
	em := s.rowQueue[0]
	s.rowQueue[0] = rowEmission{}
	s.rowQueue = s.rowQueue[1:]
	s.rowQueuedBytes -= em.bytes
	if em.incomplete {
		s.rowState = rowsAwaitingBoundary
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
			// The incomplete marker is the only statement of a loss
			// (nocx-2v80t.3.38): one that no subscriber took — its send
			// failed, or nobody was bound — is still owed, and is stated
			// before the next thing delivered, which is what closes the
			// interval that ran through the overflow.
			if s.owedMarker != nil && s.deliverRowEmission(*s.owedMarker) {
				s.owedMarker = nil
			}
			if !s.deliverRowEmission(em) && em.incomplete {
				owed := em
				s.owedMarker = &owed
			}
			continue
		}
		select {
		case <-s.rowWake:
		case <-s.rowsDone:
			return
		}
	}
}

// deliverRowEmission marshals once and sends per subscriber, and answers
// whether at least one subscriber took every frame of it. A send error
// ends nothing here: it already means the connection under that sink is
// dying, and releaseConnection does the teardown — one dead reader must not
// take the others' frames with it.
func (s *hostSession) deliverRowEmission(em rowEmission) bool {
	if em.clear {
		payload, err := json.Marshal(proto.ClearBoundaryDoc{Kind: "clear"})
		if err != nil {
			s.log.Warn("session clear boundary not encodable", "session", s.id.Session, "err", err)
			return false
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
		return len(subs) > 0
	}
	if em.end {
		closing, err := encodedRowsOrNothing(em.closing)
		if err != nil {
			s.log.Warn("session closing screen not encodable", "session", s.id.Session, "err", err)
			return false
		}
		payload, err := json.Marshal(proto.IntervalEndDoc{
			Nonce:   hex.EncodeToString(em.nonce[:]),
			EndRow:  em.from,
			Closing: closing,
			NoFence: em.noFence,
		})
		if err != nil {
			s.log.Warn("session interval end not encodable", "session", s.id.Session, "err", err)
			return false
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
		return len(subs) > 0
	}
	// LostRows is the runtime's own loss (a struck feed immediately before
	// em.from); the incomplete marker carries no rows and says so.
	doc := proto.OutputRowsDoc{FromRow: em.from, LostRows: em.lost, Incomplete: em.incomplete}
	delivered := true
	for start := 0; start <= len(em.rows); start += rowsPerFrame {
		stop := start + rowsPerFrame
		if stop > len(em.rows) {
			stop = len(em.rows)
		}
		var err error
		doc.Rows, err = sessionruntime.EncodeRows(em.rows[start:stop])
		if err != nil {
			s.log.Warn("session rows not encodable", "session", s.id.Session, "err", err)
			return false
		}
		payload, err := json.Marshal(doc)
		if err != nil {
			s.log.Warn("session rows not encodable", "session", s.id.Session, "err", err)
			return false
		}
		s.mu.Lock()
		subs := s.subscribersLocked()
		s.mu.Unlock()
		taken := 0
		for _, sub := range subs {
			if err := sub.sink.SendOutputRows(proto.OutputRowsFrame{
				Session: s.raw, Subscriber: sub.raw, FromRow: doc.FromRow, Payload: payload,
			}); err != nil {
				s.log.Warn("session rows not delivered", "session", s.id.Session,
					"subscriber", sub.id, "fromRow", doc.FromRow, "err", err)
				continue
			}
			taken++
		}
		delivered = delivered && taken > 0
		if stop == len(em.rows) {
			return delivered
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
	return delivered
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
