package transport

import (
	"context"
	"fmt"
	"io"
	"sync"
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
}

func newOutputRing() *outputRing {
	return &outputRing{
		changed: make(chan struct{}),
	}
}

// signal must be called with r.mu held. Closes the existing changed channel
// and replaces it with a fresh one, unblocking all goroutines waiting on it.
func (r *outputRing) signal() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// write appends data to the ring. If the ring is full and nothing has been
// acked to free space, write blocks until an ack or new subscriber trims the
// buffer — this is the AD-10 seam: throttle the source, never drop, never
// grow unbounded. Full credit-based flow control is bead nocx-2ho.4.
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

		if r.acked <= r.base {
			ch := r.changed
			r.mu.Unlock()
			<-ch
			r.mu.Lock()
			continue
		}

		r.trim()
	}

	r.buf = append(r.buf, p...)
	r.signal()
	return nil
}

// trim discards bytes from the front of buf that have been acked.
func (r *outputRing) trim() {
	if r.acked <= r.base {
		return
	}
	discard := r.acked - r.base
	if discard > uint64(len(r.buf)) {
		discard = uint64(len(r.buf))
	}
	if discard == 0 {
		return
	}
	// RingCapacity ≤ max int on all platforms; discard ≤ len(buf) ≤ RingCapacity.
	r.buf = r.buf[int(discard):] //nolint:gosec
	r.base += discard
}

// written returns the total byte count ever produced (base + len(buf)).
// The caller must hold r.mu.
func (r *outputRing) written() uint64 {
	return r.base + uint64(len(r.buf))
}

// writtenLocked returns the total byte count ever produced, taking its own
// lock. Safe for external callers that do not hold r.mu.
func (r *outputRing) writtenLocked() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base + uint64(len(r.buf))
}

// snapshot returns all buffered bytes starting from offset. When offset is
// older than the ring's base, needsReset is true and `from` is the current
// written offset (the client must clear and resync). When offset is at or
// past the current end, data is empty and needsReset is false (the caller
// should wait for new data).
func (r *outputRing) snapshot(offset uint64) (data []byte, from uint64, needsReset bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w := r.written()

	if offset < r.base {
		return nil, w, true
	}
	if offset >= w {
		return nil, offset, false
	}

	// RingCapacity ≤ max int; offset-base ≤ len(buf) ≤ RingCapacity.
	start := int(offset - r.base) //nolint:gosec
	if start >= len(r.buf) {
		return nil, offset, false
	}

	out := make([]byte, len(r.buf)-start)
	copy(out, r.buf[start:])
	return out, offset, false
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

// waitForCredit blocks until fewer than limit of the bytes sent up to pos
// are still unacked, the ring is closed, or ctx is cancelled.
//
// The predicate lives here, in one place, rather than being split between
// the ring and its subscriber. An earlier version waited for the client to
// ack *everything* sent — a condition the client can never satisfy, since
// it can only ack bytes it has received, which is at most pos. The window
// therefore never reopened: one burst past the credit limit wedged that
// session's output permanently. Credit is a sliding window, not
// stop-and-wait: it reopens as soon as enough has been acked, not once all
// of it has.
func (r *outputRing) waitForCredit(ctx context.Context, pos, limit uint64) (closed bool) {
	r.mu.Lock()

	for !r.closed && r.acked < pos && pos-r.acked >= limit {
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
