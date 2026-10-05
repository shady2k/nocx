package session

// The row stream bridge (nocx-2v80t.3.6): the session side of the streamed
// block output. The runtime hands departed rows to its RowStream the moment
// they leave the screen, under its own lock. The shared row byte pool accounts
// the wire FIFO, the retained resend window, closing emissions and dropped
// end records; simultaneous owners of the same copy spend the bytes twice.
// This bridge never blocks the ingest that produced a hand-off.
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
	"time"
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
	end          bool
	clear        bool
	outputStart  bool
	markSequence uint64
	nonce        sessionruntime.FenceNonce
	from         uint64
	lost         uint64
	rows         []emulator.Row
	closing      []emulator.Row
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

// retainedRowSpan owns the original cells at their absolute departure indices.
// The shared row pool accounts this ownership independently of the wire FIFO.
type retainedRowSpan struct {
	from  uint64
	rows  []emulator.Row
	bytes int64
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

func (b *rowBridge) OutputStartRow(from uint64) {
	b.hs.rowMu.Lock()
	b.hs.outputStartRow = from
	b.hs.outputStartKnown = true
	b.hs.outputStartSequence++
	sequence := b.hs.outputStartSequence
	b.hs.rowMu.Unlock()
	b.hs.enqueueRowEmission(rowEmission{outputStart: true, from: from, markSequence: sequence})
}

// enqueueRowEmission appends one emission to the FIFO if the buffer can hold
// it, and otherwise ends the block in flight incomplete (the header's rule).
// Nothing here blocks: an append and a byte count, under rowMu.
func (s *hostSession) enqueueRowEmission(em rowEmission) {
	s.rowMu.Lock()
	batch := !em.end && !em.clear && !em.outputStart
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
	if s.rowPool.limit != budget {
		s.rowPool.configure(budget)
	}
	retainedBytes := int64(0)
	if !s.rowPool.charge(rowOwnerFIFO, em.bytes) {
		// The first row this buffer does not record: the dropped batch's own
		// first, or where the stream stood when a marker would not fit.
		from := s.rowStreamNext
		if batch {
			from = em.from
		}
		marker := rowEmission{incomplete: true, from: from}
		marker.bytes = emissionBytes(marker)
		if !s.rowPool.charge(rowOwnerOverflowMarker, marker.bytes) {
			// markerReserve is part of the pool invariant; reaching this means
			// accounting was corrupted rather than ordinary exhaustion.
			s.rowMu.Unlock()
			panic("row byte pool could not fit its reserved incomplete marker")
		}
		s.rowQueue = append(s.rowQueue, marker)
		s.rowQueuedBytes += marker.bytes
		s.rowState = rowsDropping
		s.rowMu.Unlock()
		s.rowsIncomplete.Add(1)
		s.rowBufferOverflows.Add(1)
		s.log.Warn("session row buffer overflowed: the block in flight ends incomplete",
			"session", s.id.Session, "fromRow", from, "bufferBytes", budget,
			"overflowsTotal", s.rowBufferOverflows.Load())
		s.wakeRows()
		return
	}
	if batch && len(em.rows) != 0 {
		retainedBytes = emissionBytes(rowEmission{rows: em.rows})
		if !s.rowPool.charge(rowOwnerResend, retainedBytes) {
			s.rowPool.release(rowOwnerFIFO, em.bytes)
			from := em.from
			marker := rowEmission{incomplete: true, from: from}
			marker.bytes = emissionBytes(marker)
			if !s.rowPool.charge(rowOwnerOverflowMarker, marker.bytes) {
				s.rowMu.Unlock()
				panic("row byte pool could not fit its reserved incomplete marker")
			}
			s.rowQueue = append(s.rowQueue, marker)
			s.rowQueuedBytes += marker.bytes
			s.rowState = rowsDropping
			s.rowMu.Unlock()
			s.rowsIncomplete.Add(1)
			s.rowBufferOverflows.Add(1)
			s.log.Warn("session row buffer overflowed: resend window exhausted; the block in flight ends incomplete",
				"session", s.id.Session, "fromRow", from, "bufferBytes", budget,
				"overflowsTotal", s.rowBufferOverflows.Load())
			s.wakeRows()
			return
		}
		s.resendWindow = append(s.resendWindow, retainedRowSpan{from: em.from, rows: em.rows, bytes: retainedBytes})
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

// requestRowsDrain arms a one-shot signal that the pump closes the moment it
// next finds its own queue empty with nothing owed, and wakes the pump so it
// does not wait for some other event to notice (nocx-2v80t.3.52).
//
// Called from stop() right after runtime.Fail returns, this is not a race
// with more work arriving the way rowsDone's own close used to be: Fail is
// the runtime's last word — nothing it marks unavailable can ever hand the
// bridge another emission — so the queue this arms against can only ever
// shrink from here, never grow again. Waiting for it is therefore waiting
// for a state that is approached monotonically, not raced for: whatever Fail
// just enqueued (a forged sighting's held rows, nocx-2v80t.3.47) and any
// marker already owed before it are what the pump has left to resolve, and
// the caller may safely act — remove subscribers, end the pump — only once
// this closes.
func (s *hostSession) requestRowsDrain() <-chan struct{} {
	s.rowMu.Lock()
	done := s.rowsDrainDone
	if done == nil {
		done = make(chan struct{})
		s.rowsDrainDone = done
	}
	s.rowMu.Unlock()
	s.wakeRows()
	return done
}

// signalRowsDrainedIfWaiting fires a pending drain request the instant the
// pump discovers its queue is empty with nothing owed — called from exactly
// one place in serveRows's loop, the place both are already known to be
// true, never guessed at. One-shot: cleared the moment it fires, so the
// ordinary, recurring "nothing to do right now" moment that has nothing to
// do with shutdown signals nothing.
func (s *hostSession) signalRowsDrainedIfWaiting() {
	s.rowMu.Lock()
	done := s.rowsDrainDone
	s.rowsDrainDone = nil
	s.rowMu.Unlock()
	if done != nil {
		close(done)
	}
}

// drainRequested answers whether stop() is waiting on requestRowsDrain right
// now — the one question deliverForPump and the owed-marker gate need to
// tell "still worth retrying forever" from "shutdown is waiting on this and
// nothing will ever wake it again" (nocx-2v80t.3.52, round 2).
func (s *hostSession) drainRequested() bool {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	return s.rowsDrainDone != nil
}

// The shutdown drain's give-up machinery (nocx-2v80t.3.54). drainRequested
// tells the pump "stop() is waiting"; these tell it "the waiting ended":
// either the pump's own bounded send ran past stopGrace, or stop()'s wait
// on the drain ran out and abandonRowsDrain ended the drain by fiat.
// Either way the latch is one-way — nothing further is attempted, and
// every emission walked past afterwards is stated as a loss rather than
// silently dropped.

// drainAbandoned answers whether the give-up latch is set.
func (s *hostSession) drainAbandoned() bool {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	return s.rowDrainAbandoned
}

// latchDrainAbandoned sets the give-up latch: one drain-time send already
// ran past its bound — the sink is not merely slow, it is wedged — so
// nothing further in this drain is attempted.
func (s *hostSession) latchDrainAbandoned() {
	s.rowMu.Lock()
	s.rowDrainAbandoned = true
	s.rowMu.Unlock()
}

// abandonRowsDrain is stop()'s one bound on the drain wait. It runs only
// when that bound ran out with the pump unreachable — stuck inside a send
// that took deliverForPump's unbounded branch before the drain was armed,
// which no arm can interrupt — and it ends the drain by fiat: it latches
// the give-up flag, so a pump that much later unblocks attempts nothing
// more; it states the in-flight send's loss by name; and it drops and
// states every still-queued emission. The queue is emptied here exactly
// once, so the pump's own walk-past-and-count can never state a loss
// twice: whichever side reaches an emission first states it, and the
// other sees it gone. The buffer's own incomplete markers are not
// re-counted — their loss was stated at the overflow, before delivery was
// ever attempted.
func (s *hostSession) abandonRowsDrain() {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	s.rowDrainAbandoned = true
	if s.rowSending {
		if s.rowInFlight.incomplete {
			s.log.Warn("session row pump: the send in flight at shutdown was the buffer's own incomplete marker; its loss was already counted",
				"session", s.id.Session)
		} else {
			s.rowLossCountedSeq = s.rowSendSeq
			s.stateRowLoss(s.rowInFlight)
		}
	}
	for {
		em, ok := s.dequeueRowEmissionLocked()
		if !ok {
			break
		}
		owner := rowOwnerFIFO
		if em.incomplete {
			owner = rowOwnerOverflowMarker
		}
		s.rowPool.release(owner, em.bytes)
		if em.incomplete {
			s.log.Warn("session row pump: a queued incomplete marker was never delivered at shutdown; its loss was already counted",
				"session", s.id.Session, "fromRow", em.from)
			continue
		}
		s.stateRowLoss(em)
	}
}

// deliverForPump is deliverRowEmission, bounded ONLY once stop() is
// draining. The Sink interface takes no context and its one production
// implementation (internal/helper/host.Host.write) is a plain,
// deadline-less io.Writer call behind a connection-wide writer mutex —
// there is no cancellation this package can reach into for an in-flight
// send, and no deadline a per-session drain may set: the wire is one
// connection shared by every session on it (D12), and one session's end
// must not poison another's writes. Outside a drain this calls
// deliverRowEmission directly, unbounded, exactly as before: a
// slow-but-alive subscriber is still worth an unbounded wait during
// ordinary operation, and bounding every send would be a behaviour change
// this bug does not ask for.
//
// During a drain, a send that never returns would hang stop() — and every
// session teardown behind it — forever, with no way to interrupt it.
// Bounded to stopGrace, the SAME grace the process's own tail already
// gets from owner.stop, rather than a new invented duration: past it the
// delivery is abandoned rather than waited on further, the drain's
// give-up latch is set, and the pump attempts nothing after it.
//
// Every send is registered for its duration (beginRowSend/endRowSend),
// whichever branch runs it: the sweep that ends an overdue drain by fiat
// must be able to name and count the one send it can no longer wait for.
func (s *hostSession) deliverForPump(em rowEmission) (delivered, gaveUp bool) {
	if !s.drainRequested() {
		s.beginRowSend(em)
		ok := s.deliverRowEmission(em)
		s.endRowSend()
		return ok, false
	}
	s.beginRowSend(em)
	defer s.endRowSend()
	done := make(chan bool, 1)
	go func() { done <- s.deliverRowEmission(em) }()
	select {
	case ok := <-done:
		return ok, false
	case <-time.After(stopGrace):
		s.log.Warn("session row pump: a delivery at shutdown did not return within the grace period; abandoning it",
			"session", s.id.Session, "incomplete", em.incomplete, "end", em.end)
		return false, true
	}
}

// beginRowSend names the emission the pump is about to put on the wire, so
// abandonRowsDrain can count a send it can no longer wait for; rowSendSeq
// numbers the send, which is how the pump returning from the very send the
// sweep stated recognises its loss as already stated (countRowLossOnce).
func (s *hostSession) beginRowSend(em rowEmission) {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	s.rowSendSeq++
	s.rowSending = true
	s.rowInFlight = em
}

// endRowSend clears the in-flight registration; the seq stays, which is
// what lets a sweep that ran DURING the send be attributed to it.
func (s *hostSession) endRowSend() {
	s.rowMu.Lock()
	s.rowSending = false
	s.rowMu.Unlock()
}

// stateRowLoss states one emission the drain gave up on, through the same
// counter and log class the buffer's overflow uses: the statement is the
// same — the block in flight ends without these rows — and rowsIncomplete
// is the session's ONE loss counter, not a second one beside it.
func (s *hostSession) stateRowLoss(em rowEmission) {
	s.rowsIncomplete.Add(1)
	kind := "rows"
	switch {
	case em.end:
		kind = "interval end"
	case em.clear:
		kind = "clear boundary"
	}
	s.log.Warn("session row pump: output was not delivered at shutdown and is counted as a loss",
		"session", s.id.Session, "kind", kind, "fromRow", em.from,
		"lossesTotal", s.rowsIncomplete.Load())
}

// countRowLossOnce states em's loss unless it was already stated for this
// very send by abandonRowsDrain: the sweep names the in-flight send by
// seq, and whichever side reaches the loss first, it is stated exactly
// once.
func (s *hostSession) countRowLossOnce(em rowEmission) {
	s.rowMu.Lock()
	mine := s.rowLossCountedSeq == s.rowSendSeq
	s.rowLossCountedSeq = 0
	s.rowMu.Unlock()
	if !mine {
		s.stateRowLoss(em)
	}
}

// releaseRowEmission ends the FIFO/in-flight owner's charge after delivery.
func (s *hostSession) releaseRowEmission(em rowEmission) {
	s.rowMu.Lock()
	owner := rowOwnerFIFO
	if em.incomplete {
		owner = rowOwnerOverflowMarker
	}
	s.rowPool.release(owner, em.bytes)
	s.rowMu.Unlock()
}

// dequeueRowEmission pops the FIFO's head, or answers false with nothing
// queued. Popping the incomplete marker is where the stream is healthy
// again: everything queued before it has gone, so from here the bridge waits
// only for the next command to start.
func (s *hostSession) dequeueRowEmission() (rowEmission, bool) {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	return s.dequeueRowEmissionLocked()
}

// dequeueRowEmissionLocked is dequeueRowEmission for a caller already
// holding rowMu — abandonRowsDrain's sweep drains the queue under the lock
// it holds rather than re-taking it per emission.
func (s *hostSession) dequeueRowEmissionLocked() (rowEmission, bool) {
	if len(s.rowQueue) == 0 {
		return rowEmission{}, false
	}
	em := s.rowQueue[0]
	s.rowQueue[0] = rowEmission{}
	s.rowQueue = s.rowQueue[1:]
	s.rowQueuedBytes -= em.bytes
	// The charge follows the emission while it is in flight; releasing here
	// would open capacity while deliverRowEmission still holds its rows.
	if em.incomplete {
		s.rowState = rowsAwaitingBoundary
	}
	return em, true
}

// serveRows is the bridge's one pump. It writes each emission to every
// subscriber bound at that moment — rows are the session's stream, and each
// reader holds its own attachment — and ends when the session does. With no
// subscriber bound there is nothing to deliver and nothing to mourn: the
// rows were handed over once by the runtime; the retained helper window
// is the buffer the eventual resend reads from the confirmed watermark.
//
// The owed marker is a gate, not a courtesy retry: while one is owed nothing
// else is dequeued, however many attempts it takes, because the marker is the
// only statement of the loss it names and anything delivered ahead of it
// would overtake that statement (nocx-2v80t.3.49). A failed attempt parks on
// the next wake rather than spinning — attach (session.go) wakes this pump
// the moment a new subscriber binds, precisely so a marker owed to "nobody
// bound" is retried without waiting for the next row or end to arrive.
//
// Shutdown drains by CONSTRUCTION, not by select order (nocx-2v80t.3.52):
// stop() (session.go) calls requestRowsDrain after runtime.Fail returns —
// Fail is itself one of the events that can still hand this bridge a row
// (nocx-2v80t.3.47) — and waits for the signal signalRowsDrainedIfWaiting
// fires below, before it ever removes a subscriber or closes rowsDone. A
// racing select that could take the rowsDone arm while a wake sat unread
// beside it, dropping whatever the wake was for, is exactly the defect this
// replaces: rowsDone is only closed once the caller has already SEEN the
// queue reach empty with nothing owed, so its own close settles nothing that
// still mattered.
//
// A drain must still terminate whatever the sink does (nocx-2v80t.3.52,
// round 2), or stop() — and every session teardown behind it — hangs on a
// dead or wedged coordinator connection forever. Two failure modes, two
// answers:
//
//   - A send that FAILS (returns promptly, refused): once draining, the
//     owed-marker gate stops retrying it forever. The loss is already
//     stated — enqueueRowEmission counted it and logged it the instant the
//     buffer overflowed, before delivery was ever attempted — so giving up
//     here restates nothing; it only stops waiting for a subscriber that is
//     not coming back.
//   - A send that BLOCKS (never returns): deliverForPump bounds it to
//     stopGrace once draining, and the FIRST send that runs past that
//     bound latches the drain's give-up flag (drainAbandoned) — the sink
//     is not merely slow, it is wedged. Past the latch nothing is
//     attempted: no second abandoned goroutine racing the first one's
//     eventual, unordered write, and no stopGrace per remaining item on a
//     sink already known to be wedged. Every emission walked past then is
//     stated as a loss (stateRowLoss), and stop() carries the same two
//     ends on its own side: its wait on the drain is bounded by the same
//     grace, and when a send that predates the arm never returns, its
//     sweep (abandonRowsDrain) ends the drain by fiat and does the
//     counting the pump cannot reach.
func (s *hostSession) serveRows() {
	for {
		if s.owedMarker != nil {
			// Whether the drain was already requested when this attempt
			// STARTED: only such an attempt is the drain's own. One that
			// began before stop() armed the drain and failed after it gives
			// the marker no attempt at shutdown at all, so it parks on the
			// wake requestRowsDrain sends, and the drain's attempt follows
			// (nocx-2v80t.3.49).
			drainAttempt := s.drainRequested()
			delivered, gaveUp := false, false
			if !s.drainAbandoned() {
				delivered, gaveUp = s.deliverForPump(*s.owedMarker)
				if gaveUp {
					s.latchDrainAbandoned()
				}
			}
			switch {
			case delivered:
				s.releaseRowEmission(*s.owedMarker)
				s.owedMarker = nil
			case s.drainAbandoned() || drainAttempt:
				if !gaveUp {
					s.log.Warn("session row pump: the owed incomplete marker could not be delivered at shutdown; the loss was already counted",
						"session", s.id.Session)
				}
				s.releaseRowEmission(*s.owedMarker)
				s.owedMarker = nil
			default:
				select {
				case <-s.rowWake:
					continue
				case <-s.rowsDone:
					return
				}
			}
		}
		// The coordinator's return owes a retained-window resend before
		// anything still queued delivers: the resent rows are older than
		// everything the bridge currently holds. During a shutdown
		// drain the resend is pointless — the reader is going
		// away, and the next one attaches a pump of its own.
		s.rowMu.Lock()
		resendDue := s.resendDue
		s.rowMu.Unlock()
		if resendDue && !s.drainRequested() {
			if s.resendFromScrollback() {
				s.rowMu.Lock()
				s.resendDue = false
				s.rowMu.Unlock()
			}
		}
		em, ok := s.dequeueRowEmission()
		if !ok {
			// owedMarker is guaranteed nil here: either it was nil on entry,
			// or the branch above just cleared it before falling through to
			// this dequeue in the same iteration. So this is exactly the
			// "queue empty, nothing owed" instant requestRowsDrain waits for.
			s.signalRowsDrainedIfWaiting()
			select {
			case <-s.rowWake:
			case <-s.rowsDone:
				return
			}
			continue
		}
		delivered, gaveUp := false, false
		if !s.drainAbandoned() {
			delivered, gaveUp = s.deliverForPump(em)
			if gaveUp {
				s.latchDrainAbandoned()
			}
		}
		if !delivered && em.incomplete {
			owed := em
			s.owedMarker = &owed
		} else if !delivered && (gaveUp || s.drainRequested()) {
			// Not delivered at shutdown: stated as a loss, exactly once —
			// unless the sweep already stated this very send's loss by
			// name. A send that fails promptly outside a drain stays
			// unstated, as before: the pump keeps retrying it the
			// ordinary way, on the next wake. An end is kept by name
			// either way: the boundary is the boundary, and the
			// coordinator's return owes its marker however the drop
			// happened.
			s.countRowLossOnce(em)
			s.rowMu.Lock()
			s.resendDue = true
			s.rowMu.Unlock()
			s.keepDroppedEnd(em)
		} else if !delivered {
			// Nobody took it: the scrollback is the buffer, and the
			// coordinator's return owes a read-back (nocx-zg3k3.5.3). An
			// end is kept by name — the boundary's own identity — so the
			// return can be handed the marker again.
			s.rowMu.Lock()
			s.resendDue = true
			s.rowMu.Unlock()
			s.keepDroppedEnd(em)
		}
		// An undelivered incomplete marker remains charged while owedMarker
		// owns it; other emissions release only after their send resolves.
		if delivered || !em.incomplete {
			s.releaseRowEmission(em)
		}
	}
}

// droppedEnd is one interval end the pump dropped for want of a subscriber:
// the boundary's own identity, kept so the coordinator's return can be
// handed the marker again (nocx-zg3k3.5.3). The pump alone holds these.
type droppedEnd struct {
	nonce   sessionruntime.FenceNonce
	endRow  uint64
	noFence bool
}

// maxResendEnds bounds the dropped ends one resend carries. One per command
// that ended while nobody watched; more means commands ran for a coordinator
// that was away for the whole span the scrollback could answer for anyway.
const maxResendEnds = 64

// resendFromScrollback delivers the unconfirmed span from the helper's bounded
// retained window, in original absolute-index order, interleaving dropped end
// markers. It never reads the mutable emulator history: absent retained rows
// are helper-pool loss and are stated incomplete at their first missing index.
func (s *hostSession) resendFromScrollback() bool {
	// Count first: runtime callbacks append their retained spans while holding
	// the runtime lock, so every departure at or below d is present in the
	// following window snapshot. Departures after d are filtered below.
	d := s.runtime.DepartedRowCount()
	s.rowMu.Lock()
	ends := append([]droppedEnd(nil), s.resendEnds...)
	spans := append([]retainedRowSpan(nil), s.resendWindow...)
	// Emissions still queued have not reached any reader yet. If a new
	// attachment caused this replay, stop just before the first such row;
	// the queue will carry that suffix after the retained prefix and neither
	// duplicate it nor let it overtake the replay.
	queuedFrom := d
	for _, em := range s.rowQueue {
		if !em.end && !em.clear && !em.outputStart && em.from < queuedFrom {
			queuedFrom = em.from
			break
		}
	}
	s.rowMu.Unlock()
	if queuedFrom < d {
		d = queuedFrom
	}
	// Read the watermark last. An ack racing the snapshot may leave harmless
	// extra copied rows, but can never make the snapshot claim a reclaimed
	// prefix is missing.
	s.mu.Lock()
	mark := s.rowsConfirmed
	subs := s.subscribersLocked()
	s.mu.Unlock()
	if len(subs) == 0 {
		return false
	}
	// The output mark is position, not authority, but a replacement reader
	// needs it before the retained-window rows so the coordinator can join
	// those rows to an authenticated Start without a lifecycle-envelope field.
	s.rowMu.Lock()
	outputStartRow, outputStartKnown := s.outputStartRow, s.outputStartKnown
	outputStartSequence := s.outputStartSequence
	s.rowMu.Unlock()
	if outputStartKnown {
		for _, sub := range subs {
			// The replay mark stands in for every queued mark at or below this
			// snapshot. Advancing the subscriber's sequence here prevents a
			// delayed live emission from duplicating it after retained rows.
			if sub.outputStartSequence < outputStartSequence {
				sub.outputStartSequence = outputStartSequence
			}
			if sink, ok := sub.sink.(interface {
				SendOutputStartRow(proto.OutputStartRowFrame) error
			}); ok {
				if err := sink.SendOutputStartRow(proto.OutputStartRowFrame{Session: s.raw, Subscriber: sub.raw, FromRow: outputStartRow}); err != nil {
					s.log.Warn("session output-start replay mark not delivered", "session", s.id.Session, "subscriber", sub.id, "fromRow", outputStartRow, "err", err)
				}
			}
		}
	}
	if mark > d {
		mark = d
	}
	// Flatten only the bounded retained window. These cells were copied by
	// the runtime when they departed; they are never re-read or relabelled
	// from the mutable, reflowable live history.
	indexed := make(map[uint64]emulator.Row)
	for _, span := range spans {
		for i, row := range span.rows {
			index := span.from + uint64(i) //nolint:gosec // slice index cannot be negative
			if index >= mark && index < d {
				indexed[index] = row
			}
		}
	}
	if mark >= d && len(ends) == 0 {
		return true
	}
	s.log.Debug("session row resend: retained window", "session", s.id.Session, "mark", mark, "departed", d, "ends", len(ends), "rows", len(indexed))
	// Stream order is rows below a boundary, then the boundary itself. A
	// row absent from the bounded window is helper-pool exhaustion, not live
	// scrollback pruning: state ADR-0075's incomplete marker at that exact
	// position, then continue with any later retained original rows.
	endIndex := 0
	for at := mark; at < d; {
		if endIndex < len(ends) && ends[endIndex].endRow <= at {
			e := ends[endIndex]
			if !s.deliverRowEmission(rowEmission{end: true, nonce: e.nonce, from: e.endRow, noFence: e.noFence}) {
				return false
			}
			endIndex++
			continue
		}
		if row, ok := indexed[at]; ok {
			rows := []emulator.Row{row}
			next := at + 1
			for next < d {
				if endIndex < len(ends) && ends[endIndex].endRow == next {
					break
				}
				r, exists := indexed[next]
				if !exists {
					break
				}
				rows = append(rows, r)
				next++
			}
			if !s.sendResentRows(subs, at, 0, "", rows) {
				return false
			}
			at = next
			continue
		}
		// One marker per contiguous hole. Its position is the first row the
		// helper could not retain; subsequent rows resume only where retained.
		from := at
		for at < d {
			if endIndex < len(ends) && ends[endIndex].endRow <= at {
				break
			}
			if _, ok := indexed[at]; ok {
				break
			}
			at++
		}
		if !s.sendIncompleteResend(subs, from) {
			return false
		}
	}
	for endIndex < len(ends) {
		e := ends[endIndex]
		if !s.deliverRowEmission(rowEmission{end: true, nonce: e.nonce, from: e.endRow, noFence: e.noFence}) {
			return false
		}
		endIndex++
	}
	s.rowMu.Lock()
	s.resendEnds = nil
	s.rowMu.Unlock()
	return true
}

func (s *hostSession) sendIncompleteResend(subs []*subscriber, from uint64) bool {
	payload, err := json.Marshal(proto.OutputRowsDoc{FromRow: from, Incomplete: true})
	if err != nil {
		return false
	}
	delivered := len(subs) > 0
	for _, sub := range subs {
		if err := sub.sink.SendOutputRows(proto.OutputRowsFrame{Session: s.raw, Subscriber: sub.raw, FromRow: from, Payload: payload}); err != nil {
			delivered = false
		}
	}
	return delivered
}

// keepDroppedEnd records one undelivered interval end by name, the record
// the coordinator's return is handed again (nocx-zg3k3.5.3). Every drop
// path goes through this: the boundary is the boundary whatever took the
// frame.
func (s *hostSession) keepDroppedEnd(em rowEmission) {
	if !em.end {
		return
	}
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	if s.rowPool.limit == 0 {
		limit := s.rowBufferBytes
		if limit <= 0 {
			limit = DefaultRowBufferBytes
		}
		s.rowPool.configure(limit)
	}
	charge := int64(unsafe.Sizeof(droppedEnd{}))
	for {
		if len(s.resendEnds) < maxResendEnds && s.rowPool.charge(rowOwnerDroppedEnds, charge) {
			break
		}
		if len(s.resendEnds) == 0 {
			return
		}
		s.resendEnds[0] = droppedEnd{}
		s.resendEnds = s.resendEnds[1:]
		s.rowPool.release(rowOwnerDroppedEnds, charge)
	}
	s.resendEnds = append(s.resendEnds, droppedEnd{
		nonce: em.nonce, endRow: em.from, noFence: em.noFence,
	})
}

// sendResentRows writes one resend as the ordinary rows frames, split at the
// same bound a live batch is, to every subscriber bound at this moment.
func (s *hostSession) sendResentRows(subs []*subscriber, from, lost uint64, cause string, rows []emulator.Row) bool {
	delivered := true
	for start := 0; start <= len(rows); start += rowsPerFrame {
		stop := start + rowsPerFrame
		if stop > len(rows) {
			stop = len(rows)
		}
		encoded, err := sessionruntime.EncodeRows(rows[start:stop])
		if err != nil {
			s.log.Warn("session row resend: rows not encodable", "session", s.id.Session, "err", err)
			return false
		}
		payload, err := json.Marshal(proto.OutputRowsDoc{FromRow: from, LostRows: lost, LostCause: cause, Rows: encoded})
		if err != nil {
			s.log.Warn("session row resend: rows not encodable", "session", s.id.Session, "err", err)
			return false
		}
		taken := 0
		for _, sub := range subs {
			if err := sub.sink.SendOutputRows(proto.OutputRowsFrame{
				Session: s.raw, Subscriber: sub.raw, FromRow: from, Payload: payload,
			}); err != nil {
				s.log.Warn("session row resend: not delivered", "session", s.id.Session,
					"subscriber", sub.id, "fromRow", from, "err", err)
				continue
			}
			taken++
		}
		delivered = delivered && taken > 0
		from += uint64(stop - start) //nolint:gosec // slice arithmetic, never negative
		if stop == len(rows) {
			return delivered
		}
		lost = 0
	}
	return delivered
}

// deliverRowEmission marshals once and sends per subscriber, and answers
// whether at least one subscriber took every frame of it. A send error
// ends nothing here: it already means the connection under that sink is
// dying, and releaseConnection does the teardown — one dead reader must not
// take the others' frames with it.
func (s *hostSession) deliverRowEmission(em rowEmission) bool {
	if em.outputStart {
		s.mu.Lock()
		subs := s.subscribersLocked()
		s.mu.Unlock()
		for _, sub := range subs {
			if sub.outputStartSequence >= em.markSequence {
				continue
			}
			sink, ok := sub.sink.(interface {
				SendOutputStartRow(proto.OutputStartRowFrame) error
			})
			if !ok {
				s.log.Warn("session output-start mark not delivered: sink lacks row-mark support", "session", s.id.Session, "subscriber", sub.id, "fromRow", em.from)
				continue
			}
			if err := sink.SendOutputStartRow(proto.OutputStartRowFrame{Session: s.raw, Subscriber: sub.raw, FromRow: em.from}); err != nil {
				s.log.Warn("session output-start mark not delivered", "session", s.id.Session, "subscriber", sub.id, "fromRow", em.from, "err", err)
			}
		}
		return len(subs) > 0
	}
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
	// ONE subscribers snapshot for the whole emission, not one per split
	// frame. A re-bind landing between two frames of the same batch used to
	// redirect the batch's TAIL to the newcomer, who then also received the
	// returned walk's re-delivery of those very rows from below — the same
	// index line delivered twice, out of order, the tail-after-walk seam's
	// loaded residual (nocx-zg3k3.5.11): the walk owns the newcomer's past,
	// the emission belongs to whoever was bound when it started.
	s.mu.Lock()
	s.rowMu.Lock()
	resendDue := s.resendDue
	s.rowMu.Unlock()
	if resendDue && !em.incomplete {
		// A reader attached after the pump's resend check but before this
		// snapshot must not receive a newer queued row before its retained
		// prefix. Returning undelivered leaves the queue's retained copy in
		// place; the next pump turn performs the replay first.
		s.mu.Unlock()
		return false
	}
	subs := s.subscribersLocked()
	s.mu.Unlock()
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
		doc.FromRow += uint64(stop - start) //nolint:gosec // slice arithmetic, never negative
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
	s.mu.Unlock()
	// A high watermark is not proof when the helper has a hole at the head.
	// Reclaim only the contiguous retained prefix beginning at the last
	// proven watermark; an ack beyond a hole leaves both bytes and mark intact.
	proven := s.reclaimRetainedPrefix(upToRow)
	s.mu.Lock()
	if proven > s.rowsConfirmed {
		s.rowsConfirmed = proven
	}
	s.mu.Unlock()
	return nil
}

func (s *hostSession) reclaimRetainedPrefix(upTo uint64) uint64 {
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	if upTo <= s.resendReclaimed {
		return s.resendReclaimed
	}
	start, cursor := s.resendReclaimed, s.resendReclaimed
	// First find how much prefix the retained records actually prove. Do
	// Do not mutate accounting until the proof boundary is known.
	for _, span := range s.resendWindow {
		end := span.from + uint64(len(span.rows)) //nolint:gosec // slice index cannot be negative
		if span.from > cursor || end <= cursor {
			break
		}
		if upTo < end {
			cursor = upTo
			break
		}
		cursor = end
		if cursor >= upTo {
			break
		}
	}
	if cursor == start {
		return start
	}
	target := cursor
	for len(s.resendWindow) > 0 && start < target {
		span := &s.resendWindow[0]
		end := span.from + uint64(len(span.rows)) //nolint:gosec // slice index cannot be negative
		if target < end {
			cut := int(target - span.from) //nolint:gosec // bounded by span length
			remaining := span.rows[cut:]
			newBytes := emissionBytes(rowEmission{rows: remaining})
			s.rowPool.release(rowOwnerResend, span.bytes-newBytes)
			span.from, span.rows, span.bytes = target, remaining, newBytes
			break
		}
		s.rowPool.release(rowOwnerResend, span.bytes)
		start = end
		s.resendWindow[0] = retainedRowSpan{}
		s.resendWindow = s.resendWindow[1:]
	}
	s.resendReclaimed = target
	return target
}
