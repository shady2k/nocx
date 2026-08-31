package session

import (
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The bounded output window: D1's promise and its bound in one object.
//
// # Why it is not internal/transport's ring
//
// The coordinator's outputRing answers the same question about offsets, and
// that question — is this offset still in the window, and where does a reader
// restart — has ONE owner, proto.ResumeAt, which both call. What could not be
// shared is the buffer itself, and the reason is a trade the two make in
// opposite directions on purpose:
//
//	outputRing  LOSSLESS. A byte leaves it only after a consumer passed it,
//	            and a full ring THROTTLES the source (AD-10). Its consumers
//	            are an attached client's acks and the recorder's persistence
//	            cursor, and it knows about both.
//	window      CAPACITY-RECLAIMED. The oldest bytes are discarded rather
//	            than the source throttled (D8's amendment to AD-10), because
//	            a three-hour build must continue while nobody is watching
//	            rather than stop because nobody is watching. It knows NOTHING
//	            about its readers: no subscriber map, no ack, no lease, no
//	            minimum-cursor rule. A reader asks from an offset; if it is
//	            at or above the base it is served, otherwise it is told the
//	            base and what it lost.
//
// Everything the ring carries beyond the bytes exists to serve losslessness,
// so extending it into this would have meant carrying that machinery to a
// place with no use for it and then disabling it. What is genuinely one
// concept — the decision rule — is one function, in proto, called by both.
//
// # The representation, and why it is pages
//
// D8 names the obligation exactly: "a representation whose allocation is
// measurable and reclaimable — fixed-size pages or a chunk deque — and a bound
// on any single pull response". A grows-by-append, trims-by-reslice buffer
// meets neither. `append` may keep a backing array far larger than the live
// slice; resliding the front does not release it; and copying every retained
// byte to answer one read would transiently double a 4 MiB window to deliver
// 8 KiB. Fixed pages give both: allocation is len(pages)×pageSize, a reclaimed
// page is dropped from the slice and collected, and a read never crosses a
// page boundary.

// pageSize is one page of the window, and also the largest number of bytes one
// read can return. 32 KiB is the PTY pump's own read size, so the common case
// is one page per read with no partial-page arithmetic, and it is well under
// proto.MaxFrameBytes so a page always fits in one data frame.
const pageSize = 32 * 1024

// window is one session's bounded output window. Safe for concurrent use: one
// writer (the PTY pump) and any number of readers (the per-subscriber pumps).
type window struct {
	mu sync.Mutex
	// maxPages is the bound, in pages. Retention is therefore between
	// (maxPages-1)×pageSize and maxPages×pageSize bytes: a page is reclaimed
	// whole, because reclaiming part of one is what would require a copy.
	maxPages int
	// pages holds the retained bytes. pages[0] begins exactly at base — only
	// whole pages are dropped — and every page but the last is full.
	pages [][]byte
	// tail is the number of valid bytes in the last page.
	tail int
	// base is the offset of the oldest byte still held; written is the total
	// ever produced. base ≤ written from construction until close: base only
	// advances over bytes that were written, and written only grows.
	base    proto.StreamOffset
	written proto.StreamOffset

	// gate wakes readers parked at the end of the stream. It is a separate
	// object rather than a channel field so that the window and a subscriber
	// wait the same way; see gate.go.
	*gate
	closed bool
}

// newWindow builds a window bounded to about bound bytes. The bound is
// rounded UP to whole pages, so the window never holds less than asked for;
// callers clamp the requested bound to the helper's floor, ceiling and
// aggregate budget before getting here.
func newWindow(bound int64) *window {
	pages := int((bound + pageSize - 1) / pageSize)
	if pages < 1 {
		pages = 1
	}
	return &window{maxPages: pages, gate: newGate()}
}

// write appends produced bytes. It NEVER blocks and never fails: that is the
// whole difference between this and a lossless ring, and it is what makes D1's
// promise implementable without a disk on the host. When the bound is reached
// the oldest page is dropped and base advances over it; a reader standing in
// the dropped range learns so on its next read, as a reset naming the hole.
func (w *window) write(p []byte) {
	if len(p) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	for len(p) > 0 {
		if len(w.pages) == 0 || w.tail == pageSize {
			w.pages = append(w.pages, make([]byte, pageSize))
			w.tail = 0
		}
		n := copy(w.pages[len(w.pages)-1][w.tail:], p)
		w.tail += n
		// copy returns 0 ≤ n ≤ pageSize; a stream offset is 64-bit.
		w.written += proto.StreamOffset(n) //nolint:gosec
		p = p[n:]

		// Reclaim eagerly rather than on a low-water mark: an eager reclaim
		// is what keeps allocation at the bound instead of at the peak, and
		// there is no reader whose replay could be preserved by waiting —
		// this window owes nobody the bytes it drops.
		for len(w.pages) > w.maxPages {
			w.pages[0] = nil // release the page rather than only unlinking it
			w.pages = w.pages[1:]
			w.base += pageSize
		}
	}
	w.signal()
}

// read serves one reader standing at offset. It returns at most one page, and
// the Resume that says where the reader actually stands: resumed at its own
// offset, or reset to the base with the gap it lost (proto.ResumeAt owns that
// decision). A reset answer carries no bytes — the reader must clear its
// decoder and its screen first, because replay cannot begin inside a UTF-8
// sequence spliced onto a different stream position.
//
// No data and no reset means the reader is caught up: it waits on changed().
func (w *window) read(offset proto.StreamOffset) ([]byte, proto.Resume) {
	w.mu.Lock()
	defer w.mu.Unlock()

	r := proto.ResumeAt(w.base, w.written, offset)
	if r.Reset || r.From >= w.written {
		return nil, r
	}

	// base ≤ r.From < written, and written − base never exceeds
	// maxPages × pageSize — the bound the window was built with, which is
	// itself an int. The difference therefore fits an int on every platform.
	rel := int(r.From - w.base) //nolint:gosec
	page := rel / pageSize
	within := rel % pageSize
	avail := pageSize - within
	if page == len(w.pages)-1 {
		avail = w.tail - within
	}
	if avail <= 0 {
		return nil, r
	}
	out := make([]byte, avail)
	copy(out, w.pages[page][within:within+avail])
	return out, r
}

// span is the window's current extent, both ends named: the oldest offset that
// still exists and the total ever produced. It is what the inventory reports,
// and what a reader with no position of its own attaches at.
func (w *window) span() (base, written proto.StreamOffset) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.base, w.written
}

// allocated is the window's RESIDENT byte count — the number D8 insists the
// bound be asserted on, because a slice's length is not resident memory. It is
// reported when a session ends, because the memory this design spends is spent
// on somebody else's machine, and a bound nobody ever measures is a bound
// nobody can tell was wrong.
func (w *window) allocated() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pages) * pageSize
}

// changed returns the channel that closes on the next write or on close. A
// reader parked at the end of the stream waits on it, which is how a pump
// waits on an observable state change rather than on a duration.
func (w *window) changed() <-chan struct{} { return w.wait() }

// close ends the window: readers parked on changed() are woken and isClosed
// reports true from then on. The BYTES are not discarded — the process ended,
// what it printed did not stop existing, and a reader attaching after an exit
// must still be able to read the last thing the shell said.
func (w *window) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	w.signal()
}

func (w *window) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}
