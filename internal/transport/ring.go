package transport

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// RingCapacity is the per-session replay ring size in bytes.
// 256 KB covers ~10 screens of 132×43 terminal output (~5.7 KB per screen),
// enough to survive a brief WebSocket disconnect without dropping data
// while keeping per-session memory bounded across many tabs.
const RingCapacity = 256 * 1024

// CreditLimit is the per-session in-flight byte cap (AD-10). A subscriber
// stops sending once unacked bytes reach this bound and resumes when an
// ack frees room. Must be less than RingCapacity, otherwise the credit
// never binds and AD-10 is dead code. 64 KB fits two 32 KB PTY reads and
// is ~250 ms of output at 256 KB/s — well within the frontend's 100 ms
// ack throttle so the window drains before it starves.
const CreditLimit = 64 * 1024

// FairChunk bounds the number of bytes one session writes per WebSocket
// message before releasing the shared wsConn mutex (AD-10 fairness).
// A 32 KB PTY read is split into at most 4 frames; between frames any
// other session on the same connection can grab the mutex and send, so a
// flooding tab cannot stall an interactive one by more than one frame
// write. A shared-writer round-robin across sessions is the thorough
// version planned for nocx-2ho.5.
const FairChunk = 8 * 1024

// outputRing is a bounded, byte-offset-keyed replay buffer sitting between
// the session output pump and the WebSocket (AD-9). One ring per session,
// owned by WSServer and stored connection-independently so the ring survives
// a disconnect and a new subscriber can reattach at its last acked offset.
//
// The ring is NOT scrollback — the frontend owns the scrollback (AD-6).
// It is transport-side buffering: enough to replay bytes produced while
// detached, discarded once the frontend has acked them.
//
// Signalling uses a generation channel (changed) instead of sync.Cond so
// that waitForData can select against ctx.Done naturally, avoiding the
// helper-goroutine-and-poll-loop pattern required with sync.Cond.
type outputRing struct {
	mu      sync.Mutex
	changed chan struct{} // generation channel; closed + replaced on every signal
	buf     []byte        // unread bytes; buf[0] corresponds to stream byte offset `base`
	base    uint64        // byte offset of buf[0] in the output stream
	acked   uint64        // furthest acked offset (0 = nothing acked yet)
	closed  bool

	// recorded is the PERSISTENCE CURSOR: how far the backend's own recorder
	// has written this session's bytes durably (nocx-22k1c.1).
	//
	// IT IS NOT AN ACKNOWLEDGEMENT, and the two must never stand in for each
	// other. `acked` means the FRONTEND received the bytes (AD-9); this means
	// the store has them. They are different facts with different invariants,
	// and conflating them would have the ring tell a reconnecting client it
	// already holds bytes it never saw. Two cursors, two names, one rule for
	// each: acks free the ring on the client's behalf, this frees it on
	// nobody's — the bytes are on disk and no client is waiting for them.
	recorded uint64

	// attached is true while a client subscriber owns this session. It is a
	// MIRROR of sessionRx.subscriber with exactly two writers (setSubscriber
	// and clearSubscriber, both in ws.go), and it exists because the ring
	// must know which cursor is allowed to free it without reaching back
	// into the connection state it is deliberately independent of.
	//
	// The interval it draws, both ends named: from the moment the last
	// client detaches until one attaches, the ring's consumer is the
	// recorder, and the source is never throttled while the recorder is
	// keeping up. Inside that interval AD-10 is satisfied as written —
	// nothing is dropped and nothing grows unbounded, because the bytes go
	// to disk instead of to a socket.
	attached bool

	// recording is true while a recorder loop is consuming this ring. It is
	// what makes trim respect the persistence cursor: with a recorder
	// installed, bytes the recorder has not reached are still owed to the
	// STORE, so an ack alone may not free them.
	//
	// That is the same rule AD-10 already applies to a client and it has the
	// same consequence: a recorder that stops keeping up throttles the
	// source, exactly as a client that stops acking does. It is set by the
	// recorder loop and cleared on every exit it has, so a recorder that
	// dies hands the ring back to the acks instead of wedging it against a
	// cursor nothing will move again.
	recording bool

	// holes are the stretches of this session's output that never reached
	// this ring, in stream order, all of them at or after `base`. See
	// ringHole: they are what makes the ring's offsets stay equal to the
	// stream the session actually produced when a stretch of it was lost
	// UPSTREAM of the ring, rather than silently renumbering everything
	// after the loss and joining two stretches that are not adjacent.
	holes []ringHole

	// writeObs is notified with the byte count of every write. ONE slot,
	// deliberately: the run lease is the only consumer, and one run is in
	// flight per lane at a time (the shell runs one foreground job). The
	// lease counts bytes and observes activity through it — a count, never
	// a peek at the bytes (AD-6: the backend never sniffs the stream).
	writeObs func(int)
}

// ringHole is one stretch of the session's output that never reached this
// ring: the execution host reclaimed its own bounded output window past this
// coordinator before the bytes could be carried across (AD-9 on the helper's
// side of the wire). It is emphatically NOT a byte the ring dropped — the
// ring never drops one, and everything below still holds — which is why the
// hole is carried rather than skipped over: the offsets of everything after
// it must go on naming the session's own stream, or a recording would join
// two stretches that are not adjacent and nobody could tell.
//
// It carries its own reason so the recorder can say what happened in the
// store's word rather than in the nearest one to hand (internal/content's
// GapReasonHostWindow versus GapReasonUnrecorded — different facts, different
// remedies).
type ringHole struct {
	// at is the stream offset of the first byte that is missing.
	at uint64
	// n is how many bytes are missing. Never zero: a hole of nothing is not
	// a hole, and `hole` refuses one.
	n uint64
	// reason is why, in internal/content's vocabulary, ready for Skip.
	reason string
}

func newOutputRing() *outputRing { return newOutputRingAt(0) }

// newOutputRingAt makes a ring whose stream coordinate STARTS at base rather
// than at zero (nocx-k6p18.30).
//
// A helper-hosted session's stream offset is the host's, counted from the
// first byte the shell ever produced, and this ring has always used the same
// coordinate — the two coincided because a session was opened by spawning it,
// and a spawn's window base is zero. A session taken back from a helper after
// the coordinator was replaced starts somewhere else entirely: the previous
// coordinator recorded up to `base`, so that is where the recording ends and
// where the next byte belongs. Starting at zero would renumber the second half
// of one stream, which is the second coordinate system
// contracts/session.output.schema.json names as the defect it exists to avoid.
//
// `recorded` starts at base for the same reason and is not merely bookkeeping:
// it is TRUE, the store holds everything before base, and a persistence cursor
// that claimed otherwise would have the ring owe bytes nobody is waiting for.
// `acked` stays zero — nothing has been acked, and a client that attaches will
// ack from the base it is given.
func newOutputRingAt(base uint64) *outputRing {
	return &outputRing{
		changed:  make(chan struct{}),
		base:     base,
		recorded: base,
	}
}

// signal must be called with r.mu held. Closes the existing changed channel
// and replaces it with a fresh one, unblocking all goroutines waiting on it.
func (r *outputRing) signal() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// write appends data to the ring. If the ring is full and no consumer has
// passed the bytes at the front of it, write blocks — this is the AD-10 seam:
// throttle the source, never drop, never grow unbounded.
//
// The ring has up to two consumers and each frees it on its own terms: an
// attached client, through the acks it sends (AD-9), and the backend's own
// recorder, through the persistence cursor. The second is why a session with
// nobody attached no longer freezes after RingCapacity bytes: with no client
// there are no acks, and before the recorder that meant no consumer at all
// (nocx-22k1c.1). AD-10 is unchanged by it — nothing is dropped and nothing
// grows; the bytes go to disk instead of to a socket.
//
// Invariant: the output pump reads at most 32 KB per call, well below
// RingCapacity (256 KB). If a single write ever exceeds RingCapacity the
// loop below would deadlock because free < len(p) would always be true
// and the ring could never trim enough space.
func (r *outputRing) write(p []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for {
		if r.closed {
			return io.ErrClosedPipe
		}

		r.trim()

		free := RingCapacity - len(r.buf)
		if free >= len(p) {
			break
		}

		// Under pressure, and only under pressure, the recorder is allowed
		// to free what it has already kept. Under pressure because a
		// detached client's replay is worth keeping while there is room:
		// reclaiming eagerly would turn every brief disconnect into a reset,
		// when the ring could have replayed the whole gap.
		if r.reclaimRecorded() {
			continue
		}

		// Nothing left to free: park until an ack, a persistence cursor or a
		// detach changes the answer. All three signal.
		ch := r.changed
		r.mu.Unlock()
		<-ch
		r.mu.Lock()
	}

	r.buf = append(r.buf, p...)
	if r.writeObs != nil {
		r.writeObs(len(p))
	}
	r.signal()
	return nil
}

// setWriteObserver installs or clears the per-write byte-count observer
// (nil clears). The observer runs under the ring's lock and must not block.
func (r *outputRing) setWriteObserver(f func(int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeObs = f
}

// trim discards bytes from the front of buf that every live consumer has
// passed: the client has acked them, and — when a recorder is installed — the
// store has them too. The caller must hold r.mu.
//
// The recorder's cursor binds here as well as in reclaimRecorded because the
// two answer different questions. reclaimRecorded is about a ring with no
// client, where the recorder is the only consumer; this is about a ring with
// both, where an ack alone must not free a byte the store has not reached —
// otherwise a client acking faster than the disk would tear a hole in the
// recording, and the recorder would find its own place in the ring gone.
func (r *outputRing) trim() {
	floor := r.acked
	if r.recording && r.recorded < floor {
		floor = r.recorded
	}
	r.advanceBase(floor)
}

// advanceBase moves base up to floor, discarding what every live consumer has
// passed, and reports whether any BYTE was actually freed. The caller must
// hold r.mu.
//
// A floor inside a hole is clamped back to the hole's start, so base always
// names a byte the ring either holds or has held: leaving it in the middle of
// a stretch that never arrived would make bufIndex answer for an offset with
// no byte behind it. The hole itself leaves the ring only when the floor has
// passed the whole of it.
//
// The bool is what the caller acts on and it counts BYTES, not offsets: a
// base that stepped over a hole freed no memory, and write's reclaim loop
// would spin for ever on a "yes" that made no room.
func (r *outputRing) advanceBase(floor uint64) bool {
	if h, inside := r.holeAtLocked(floor); inside {
		floor = h.at
	}
	if floor <= r.base {
		return false
	}
	discard := r.bufIndex(floor)
	if discard > len(r.buf) {
		discard = len(r.buf)
	}
	r.buf = r.buf[discard:]
	r.base = floor
	kept := r.holes[:0]
	for _, h := range r.holes {
		if h.at+h.n > floor {
			kept = append(kept, h)
		}
	}
	r.holes = kept
	return discard > 0
}

// reclaimRecorded frees bytes the RECORDER has durably kept, and reports
// whether it freed any. The caller must hold r.mu.
//
// It refuses while a client is attached, and that refusal is the whole of the
// AD-10 story for an attached session: those bytes are still owed to the
// client, so the source throttles until the client acks, exactly as before.
// With nobody attached nothing is owed to anybody — the bytes are on disk and
// the only reader left is the store's own.
func (r *outputRing) reclaimRecorded() bool {
	if r.attached || r.recorded <= r.base {
		return false
	}
	return r.advanceBase(r.recorded)
}

// recordTo advances the persistence cursor to offset: the recorder has that
// much of the stream durably. Validated exactly as ack is, and for the same
// reason — a cursor running ahead of what was produced would free bytes
// nobody has, and one running backwards is a stale report.
//
// It does NOT touch `acked`. See the field comment: the client's receipt is
// a different fact and stays the client's to state.
func (r *outputRing) recordTo(offset uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w := r.written()
	if offset > w {
		return fmt.Errorf("record offset %d exceeds written %d", offset, w)
	}
	if offset < r.recorded {
		return fmt.Errorf("record offset %d is behind current recorded %d", offset, r.recorded)
	}
	r.recorded = offset
	r.signal()
	return nil
}

// setRecording says whether a recorder loop is consuming this ring. Called by
// that loop and by nothing else; idempotent, and signals on a change so a
// parked writer re-evaluates who its consumers are.
func (r *outputRing) setRecording(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recording == v {
		return
	}
	r.recording = v
	r.signal()
}

// setAttached mirrors the subscriber slot into the ring. Called by
// sessionRx's two mutators and by nothing else.
//
// Detaching signals, because the whole point is a writer that is ALREADY
// blocked: the window closes while the shell is mid-flood, which is exactly
// when the stall used to begin, and a parked writer has to be woken to
// re-evaluate who its consumer is.
func (r *outputRing) setAttached(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attached == v {
		return
	}
	r.attached = v
	r.signal()
}

// written returns the total byte count ever produced — the stream's end
// offset, INCLUDING the stretches the ring never received. The caller must
// hold r.mu.
//
// The holes are counted deliberately: `written` is the session's own
// coordinate and not an inventory of this buffer, and every other cursor in
// the system — the client's ack, the persistence cursor, the recording's
// offsets — is measured against it. A `written` that skipped the missing
// bytes would be a second coordinate system for one stream, which is the
// defect contracts/session.output.schema.json names in its own words.
func (r *outputRing) written() uint64 {
	return r.base + uint64(len(r.buf)) + r.holeBytesLocked()
}

// holeBytesLocked is how many bytes of this ring's stream never arrived. The
// caller must hold r.mu.
func (r *outputRing) holeBytesLocked() uint64 {
	var n uint64
	for _, h := range r.holes {
		n += h.n
	}
	return n
}

// hole declares that n bytes of this session's output never reached the ring,
// at its current write frontier — everything written before this call is
// before the hole and everything written after it is after.
//
// THE FRONTIER IS NOT A CHOICE, it is where the caller stands: the pump that
// reads the session and the reporter of the hole are one goroutine, so the
// bytes before the hole have already been written when this is called and the
// bytes after it have not (internal/helper/client's OnOutputHole says the
// same thing from its end). A hole placed anywhere else would be a hole at an
// offset that is not the hole's.
//
// A hole of zero bytes is refused rather than recorded: there is nothing to
// state, and a zero-width gap is a claim no reader can act on.
func (r *outputRing) hole(n uint64, reason string) {
	if n == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holes = append(r.holes, ringHole{at: r.written(), n: n, reason: reason})
	// The signal is what wakes a consumer parked in waitForData: the stream
	// has moved on even though no byte was added to it.
	r.signal()
}

// bufIndex maps a stream offset onto its position in buf. The caller must
// hold r.mu and must have established that the offset is at or after base,
// below written, and not inside a hole.
func (r *outputRing) bufIndex(offset uint64) int {
	shift := uint64(0)
	for _, h := range r.holes {
		if h.at+h.n <= offset {
			shift += h.n
		}
	}
	// offset - base - shift counts only bytes this buffer holds, and every
	// term is bounded by RingCapacity, which is far below max int.
	return int(offset - r.base - shift) //nolint:gosec
}

// holeAtLocked returns the hole covering offset, if the offset names a byte
// that never arrived. The caller must hold r.mu.
func (r *outputRing) holeAtLocked(offset uint64) (ringHole, bool) {
	for _, h := range r.holes {
		if offset >= h.at && offset < h.at+h.n {
			return h, true
		}
	}
	return ringHole{}, false
}

// nextHoleLocked returns the first hole at or after offset. The caller must
// hold r.mu.
func (r *outputRing) nextHoleLocked(offset uint64) (ringHole, bool) {
	for _, h := range r.holes {
		if h.at >= offset {
			return h, true
		}
	}
	return ringHole{}, false
}

// missingBetween counts the bytes of [from, to) that never arrived. The
// caller must hold r.mu.
func (r *outputRing) missingBetween(from, to uint64) uint64 {
	var n uint64
	for _, h := range r.holes {
		start, end := h.at, h.at+h.n
		if start < from {
			start = from
		}
		if end > to {
			end = to
		}
		if end > start {
			n += end - start
		}
	}
	return n
}

// writtenLocked returns the total byte count ever produced, taking its own
// lock. Safe for external callers that do not hold r.mu.
func (r *outputRing) writtenLocked() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written()
}

// oldestLocked returns the oldest byte offset the ring still holds — the
// lowest offset an attach can ask for and be answered with a resume rather
// than a reset. Takes its own lock, like writtenLocked, so external callers
// need not know about r.mu.
//
// It is what sessions.live reports as replayFrom (nocx-oevq4): a client with
// no offset of its own has to be TOLD where the replayable stream starts,
// because the two answers it could guess are both wrong — 0 is a reset once
// anything has been acked, and `written` silently discards everything the ring
// was keeping for exactly this moment.
func (r *outputRing) oldestLocked() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base
}

// recordedLocked is the ring's PERSISTENCE CURSOR: how far this session's bytes
// have been written durably. It is the recorder loop's own starting point, and
// it is a DIFFERENT FACT from `base` beside it — a session that ran with no
// recorder has a base the acks moved and a cursor still at zero, and the
// distance between them is a hole somebody has to record (nocx-k6p18.2). Takes
// its own lock, like the two above, so external callers need not know about
// r.mu.
func (r *outputRing) recordedLocked() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recorded
}

// snapshot returns all buffered bytes starting from offset. When offset is
// older than the ring's base, needsReset is true and `from` is the current
// written offset (the client must clear and resync). When offset is at or
// past the current end, data is empty and needsReset is false (the caller
// should wait for new data).
//
// The VERDICT — is this offset still in the window — is proto.ResumeAt's, and
// not a second derivation here. The same question is asked on the host by the
// helper's bounded output window (nocx-k6p18.3), and two derivations of one
// predicate agree everywhere anybody looks and disagree somewhere nobody does.
//
// Where a reset RESTARTS is this ring's own and deliberately not taken from
// there, because the two windows lose bytes for different reasons. This ring
// is lossless: a byte leaves it only once a consumer has passed it, so an
// offset below the base is a stale or buggy cursor for bytes the client
// already had, and the honest answer is "clear and resync at now". The
// helper's window is capacity-reclaimed, so an offset below ITS base names
// bytes nobody ever held, and the honest answer there is the oldest byte that
// still exists plus an explicit gap. Same predicate, two windows, and the
// difference is in the caller where it can be read.
func (r *outputRing) snapshot(offset uint64) (data []byte, from uint64, needsReset bool, hole *ringHole) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w := r.written()

	if proto.ResumeAt(proto.StreamOffset(r.base), proto.StreamOffset(w), proto.StreamOffset(offset)).Reset {
		return nil, w, true, nil
	}
	if offset >= w {
		return nil, offset, false, nil
	}
	if h, inside := r.holeAtLocked(offset); inside {
		// `from` is where the stream continues, so a caller that only wants
		// to resume needs nothing more than the offset; a caller that has to
		// SAY what is missing — the recorder — takes the hole itself.
		found := h
		return nil, h.at + h.n, false, &found
	}

	start := r.bufIndex(offset)
	if start >= len(r.buf) {
		return nil, offset, false, nil
	}
	// Never across a hole. Two stretches with a hole between them are not
	// one run, and a caller handed them joined would record them as adjacent
	// at offsets that are not theirs.
	end := len(r.buf)
	if h, ok := r.nextHoleLocked(offset); ok {
		if to := r.bufIndex(h.at); to < end {
			end = to
		}
	}

	out := make([]byte, end-start)
	copy(out, r.buf[start:end])
	return out, offset, false, nil
}

// ack records the furthest byte offset the client confirms having received.
// Validates the offset against what was produced; rejects offsets that run
// ahead of written (client bug or malicious) or go backwards (stale ack).
func (r *outputRing) ack(offset uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w := r.written()

	if offset > w {
		return fmt.Errorf("ack offset %d exceeds written %d", offset, w)
	}
	if offset < r.acked {
		return fmt.Errorf("ack offset %d is behind current acked %d", offset, r.acked)
	}

	if offset > r.acked {
		r.acked = offset
		r.trim()
	}

	r.signal()
	return nil
}

// creditFloor is the offset the credit window measures in-flight bytes FROM,
// for a pump that started at `since`. The caller must hold r.mu.
//
// Three lower bounds, and each is a fact about bytes that cannot be in flight
// to this subscriber:
//
//   - `acked` — the client said it has them. This is what AD-10 has always
//     measured and it is unchanged for an attached, resuming session.
//   - `base` — the ring no longer holds them, so nobody can be sent them. It
//     binds once the recorder has reclaimed past the ack cursor, and it is
//     not a weakening: a client reattaching over reclaimed bytes is told to
//     reset rather than sent them.
//   - `since` — this pump never sent them. A reattached subscriber's baseline
//     is the offset it attached at, which is its own statement of what it
//     already has; the ring-wide ack cursor may be another connection's and
//     older.
//
// Without the last two, a subscriber that reattached after a reset parked on
// a window nothing could ever reopen: it was charged for bytes it was never
// sent, so it sent none, so nothing was acked, so the window stayed shut.
func (r *outputRing) creditFloor(since uint64) uint64 {
	floor := r.acked
	if r.base > floor {
		floor = r.base
	}
	if since > floor {
		floor = since
	}
	return floor
}

// inFlightLocked is how many BYTES this pump has sent between floor and pos
// that are not yet acked. It is the span minus the stretches that never
// arrived, and the subtraction is not a refinement: a hole is bytes nobody
// can ever ack, so charging the window for them parks the pump on a credit
// window that can never reopen — the session goes silent for good the first
// time an execution host loses a window larger than CreditLimit. The caller
// must hold r.mu.
func (r *outputRing) inFlightLocked(floor, pos uint64) uint64 {
	if pos <= floor {
		return 0
	}
	return pos - floor - r.missingBetween(floor, pos)
}

// waitForCredit blocks until fewer than limit of the bytes this pump sent —
// the span from `since`, where it attached, to `pos` — are still unacked, the
// ring is closed, or ctx is cancelled.
//
// The predicate lives here, in one place, rather than being split between
// the ring and its subscriber. An earlier version waited for the client to
// ack *everything* sent — a condition the client can never satisfy, since
// it can only ack bytes it has received, which is at most pos. The window
// therefore never reopened: one burst past the credit limit wedged that
// session's output permanently. Credit is a sliding window, not
// stop-and-wait: it reopens as soon as enough has been acked, not once all
// of it has.
func (r *outputRing) waitForCredit(ctx context.Context, since, pos, limit uint64) (closed bool) {
	r.mu.Lock()

	for !r.closed && r.creditFloor(since) < pos && r.inFlightLocked(r.creditFloor(since), pos) >= limit {
		ch := r.changed
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			r.mu.Lock()
			closed = r.closed
			r.mu.Unlock()
			return closed
		case <-ch:
			r.mu.Lock()
		}
	}

	closed = r.closed
	r.mu.Unlock()
	return closed
}

// isClosed reports whether the session behind this ring is over. Safe for
// external callers; the recorder needs it because its one wait that is not
// on the ring — the poll after a refused write — would otherwise outlive the
// session it was recording.
func (r *outputRing) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *outputRing) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.signal()
}

// wake signals all goroutines blocked on this ring. Safe for external callers.
func (r *outputRing) wake() {
	r.mu.Lock()
	r.signal()
	r.mu.Unlock()
}

// waitForData blocks until new data arrives past pos, the ring is closed,
// or ctx is cancelled. Unlike the previous sync.Cond-based implementation,
// this uses the ring's generation channel to select against ctx.Done
// directly: one allocation per call, no helper goroutine, no polling.
func (r *outputRing) waitForData(ctx context.Context, pos uint64) (closed bool) {
	r.mu.Lock()

	for !r.closed && r.written() <= pos {
		ch := r.changed
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			r.mu.Lock()
			closed = r.closed
			r.mu.Unlock()
			return closed
		case <-ch:
			r.mu.Lock()
		}
	}

	closed = r.closed
	r.mu.Unlock()
	return closed
}
