package session

// The row stream bridge (nocx-2v80t.3.6): the session side of the streamed
// block output. The runtime hands departed rows to its RowStream the moment
// they leave the screen, under its own lock; this bridge is the RowStream,
// and its whole job is to move each hand-off to the wire without ever
// blocking the ingest that produced it and without keeping a copy — the
// owner's decision gives the helper no second buffer, so what the pump
// cannot deliver when the time comes is dropped, counted and logged rather
// than queued somewhere the design forbids.
//
// Order is the invariant the bridge exists to keep: the runtime emits rows
// and end markers in stream order, the hand-off channel preserves it, and
// one pump per session writes the frames. A row can never be attributed to
// the wrong interval, because nothing between the runtime and the wire
// reorders.

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

// rowEmission is one hand-off from the runtime: a row batch or an
// interval's end marker. The rows are the runtime's gift — freshly copied
// by the emulator's report, owned by whoever takes them next — so the pump
// marshals them OFF the runtime's lock.
type rowEmission struct {
	end     bool
	nonce   sessionruntime.FenceNonce
	from    uint64
	lost    uint64
	rows    []emulator.Row
	closing []emulator.Row
}

// rowBridge is the session's sessionruntime.RowStream. BOTH methods are
// called with the runtime's lock held: they must not block, must not call
// back into the runtime, and do nothing here but hand off.
type rowBridge struct{ hs *hostSession }

func (b *rowBridge) OutputRows(from uint64, rows []emulator.Row, lost uint64) {
	b.hs.enqueueRowEmission(rowEmission{from: from, lost: lost, rows: rows})
}

func (b *rowBridge) IntervalEnd(nonce sessionruntime.FenceNonce, endRow uint64, closing []emulator.Row) {
	b.hs.enqueueRowEmission(rowEmission{end: true, nonce: nonce, from: endRow, closing: closing})
}

// enqueueRowEmission hands one emission to the pump, or drops it when the
// pump has fallen a whole queue behind. Dropping is the honest answer to a
// wedged wire — blocking would stall the PTY's ingest under the runtime's
// lock, and keeping a copy is what the owner's decision forbids — and it is
// a COUNTED answer, because a row that silently vanished would be a block
// that lies. The count is what the epic's next step (the resend that reads
// the scrollback against the confirmed-written mark) reconciles; a drop
// never repairs itself.
func (s *hostSession) enqueueRowEmission(em rowEmission) {
	select {
	case s.rowCh <- em:
	default:
		s.rowsDropped.Add(1)
		s.log.Warn("session rows not streamed: the bridge queue is full",
			"session", s.id.Session, "fromRow", em.from, "droppedTotal", s.rowsDropped.Load())
	}
}

// serveRows is the bridge's one pump. It writes each emission to every
// subscriber bound at that moment — rows are the session's stream, and each
// reader holds its own attachment — and ends when the session does. With no
// subscriber bound there is nothing to deliver and nothing to mourn: the
// rows were handed over once by the runtime, ghostty's scrollback is the
// buffer, and the mark the eventual resend reads starts at zero.
func (s *hostSession) serveRows() {
	for {
		select {
		case em := <-s.rowCh:
			s.deliverRowEmission(em)
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
