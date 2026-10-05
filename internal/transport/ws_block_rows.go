package transport

// The block rows stream (nocx-2v80t.3.7): the coordinator half of the
// streamed block output. The helper (where the shell runs) holds the PTY and
// the emulator; this is where its departed rows become a command's block in
// history — appended as they arrive, sealed at the interval's authenticated
// end, acknowledged so the helper knows how far the store actually holds.
//
// ── What authenticates what ───────────────────────────────────────────────
//
// The keep decision answers to the lifecycle publisher's ATTEMPT OPEN fact:
// that fact is the command's authenticated start, and the entry it names is
// the attempt id — syncLifecycleLedger already writes shell rows under the
// attempt id, so the join between a streamed interval and its history entry
// needs no second registry. The interval's END is authenticated by the
// fence: the completion the kernel accepts carries the fence the shell
// minted (the publisher serialises it hex-encoded on the completed fact),
// the helper stamps the same fence on its interval end, and equality of a
// 32-byte nonce is the whole check. The two arrive on different channels in
// either order (ADR-0024 decision 7), so an end whose fence has not been
// seen yet waits — bounded — for its completion, and a completion whose end
// has not arrived yet is remembered for it.
//
// ── What is deliberately inert today ──────────────────────────────────────
//
// The attempt-fact hook below is production-wired: PublishLifecycle already
// runs on every authenticated command. The rows SOURCE is not — it arrives
// from the helper's client callbacks, and the composition root registers
// this stream with a session only when it attaches those callbacks (the
// wiring of both halves, post-merge). Until a source is attached the whole
// stream is a no-op: no block opens, no row is written, nothing is notified.
// That is the safe direction — a block that opens and can never close is the
// defect, not a block that never opens.

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"unsafe"

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecommit"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// blockOutputStore is everything this stream asks of the content store, and
// nothing else. content's own repository satisfies it; a test's fake
// satisfies it without a database — the same seam shape
// SessionOutputRecorder keeps.
type blockOutputStore interface {
	OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error)
	AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error
	CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error)
	// RecordClearBoundary records one sighted erase-saved-lines as a cursor
	// a read applies rather than a mark on every entry it hides
	// (nocx-2v80t.3.17).
	RecordClearBoundary(ctx context.Context, in content.RecordClearBoundary) (content.ClearBoundaryRecorded, error)
	// OpenBlockRowsForSession is the re-adopt read (ADR-0076 decision 3):
	// the open block the session still holds, with the artifact's own row
	// cursor. The zero value (no entry id) means none stands open.
	OpenBlockRowsForSession(ctx context.Context, sessionID string) (content.OpenBlockRowsEntry, error)
}

// maxPendingEnds bounds the interval ends parked while their completion is
// in flight. One per outstanding command is already generous; more means the
// lifecycle channel stopped delivering completions, and an unbounded park
// would hide exactly that.
const maxPendingEnds = 8

// maxCloseAttempts is how many times one interval's close may be attempted
// before the stream SETTLES it: the first attempt and two retries. A retry
// exists for a real shape — an append that was still in flight when the close
// read the artifact's cursor, a store that refused once — and two of them
// cover it. Past that the bound is the point: a store that keeps refusing, or
// a stream with no store wired at all, must not leave a block open, retried on
// every later delivery forever, with the client never told the command ended
// (nocx-2v80t.3.9). The number is a bound rather than a policy statement, the
// way every other bound in this file is.
const maxCloseAttempts = 3

// blockStream is the per-session state. Everything in it exists only while a
// rows source is attached, and all of it dies with the session.
type blockStream struct {
	mu sync.Mutex
	// sources are the sessions whose helper callbacks are wired.
	sources map[session.ID]struct{}
	// open are the session's open blocks by attempt id — kept ones carry a
	// real block, refused ones only remember the refusal.
	open map[session.ID]map[string]*openBlock
	// current is the interval whose rows are currently being delivered. A
	// later authenticated start has an open block too, but stays queued until
	// the current interval's end promotes it.
	current map[session.ID]*openBlock
	// queued names an eagerly opened block that must not receive rows until
	// current closes. Lifecycle facts can cross the helper's ordered rows
	// carrier, so this is a start reservation rather than a second current.
	queued map[session.ID]string
	// queuedEnds are interval ends that arrived for a queued block before the
	// prior current interval promoted it. They cannot close the queued block
	// early: its own rows still have to follow the prior interval's end.
	queuedEnds map[session.ID][]pendingEnd
	// closedThrough is the first absolute row not already owned by a
	// completed interval. Replayed rows below it stay confirmed but cannot
	// enter the following block.
	closedThrough map[session.ID]uint64
	// beyond are the rows that streamed AFTER an interval's end marker but
	// BEFORE the authenticated completion that names its fence. The fence
	// and the rows travel on different carriers (ADR-0024 decision 7), so
	// this window is ordinary: the end marker's own EndRow is the boundary
	// the moment it arrives, and a row at or above it belongs to the
	// interval that follows, never to the block the ended interval closes
	// over. beyondThrough is the exclusive end of what beyond holds, so a
	// delivery below it is the same rows arriving again.
	beyond        map[session.ID][]pendingRows
	beyondThrough map[session.ID]uint64
	// ends are interval ends parked until their completion publishes the
	// fence that names them.
	ends map[session.ID][]pendingEnd
	// fences are the completions that arrived before their end did: fence
	// hex → attempt id, kept so the end marker can still be matched.
	fences map[session.ID]map[string]string
	// confirmers are the helper-facing watermarks. A deferred open uses the
	// same callback once its rows have actually reached the store.
	confirmers map[session.ID]func(uint64)
	// waiting names an authenticated attempt whose ledger row has not become
	// durable yet. Rows arriving in this interval must not be acknowledged.
	waiting  map[session.ID]string
	pending  map[session.ID][]pendingRows
	flushing map[session.ID]bool
	// opening names the attempt whose OpenBlockOutput call is in flight for
	// the session, empty when none is (nocx-2v80t.3.48): a second caller
	// checks it by attempt id, not just by presence, so a close or an abandon
	// racing it can tell exactly which attempt's install to distrust.
	opening map[session.ID]string
	// pendingCloses are authenticated ends held behind a deferred append.
	// They are drained only after every pending row reaches the store.
	pendingCloses map[session.ID][]pendingEnd
	closing       map[session.ID]bool
	// closeTries counts the close attempts one interval's end has already
	// spent, keyed by the end's own nonce. It exists only to make a close
	// that keeps failing TERMINAL (maxCloseAttempts): past the bound the
	// stream settles the block instead of retrying it on every later
	// delivery for the life of the session (nocx-2v80t.3.9).
	closeTries map[session.ID]map[string]uint8
	// entered names the attempts whose block an environment entry sealed
	// while the attempt itself runs on (nocx-2v80t.3.24): the local `ssh`
	// whose child said hello. The lifecycle still holds that attempt open —
	// it completes for real once the parent reclaims the lane, with the
	// status the client exited with — so the lane's fact names it OPEN again
	// when the parent is back, and that must not open its block a second
	// time. Cleared by the attempt's own completion or abandonment, and by
	// detach.
	entered map[session.ID]map[string]struct{}
	// reopen queues the opens asked for while another was in flight
	// (nocx-2v80t.3.42, nocx-2v80t.3.48). Three callers ask for a block's
	// open — the submit, the shell's authenticated start and the
	// ledger.bind that makes the row durable — and the store answers
	// ErrNoSuchEntry until the bind has landed, so the request that arrives
	// during an open is often the only one that can succeed. A single slot
	// used to remember only the LAST of them: with A's open in flight and B
	// then C asking, C overwrote B and B was never made. Each is now kept,
	// in order, and popped one at a time as the open in flight (and each one
	// after it) finishes — never dropped. Cleared by detach.
	reopen map[session.ID][]string
	// closedWhileOpening names the attempts that were told block.closed — by
	// an ordinary close or by abandonAttempt — before their OWN open ever
	// installed a block for them (nocx-2v80t.3.48): either the session's
	// current opening attempt right now (opening[sid]), or one still queued
	// behind it in reopen[sid], not yet even attempted. Several queued
	// attempts can each be closed independently while one slow open occupies
	// the session — a fast command started and finished behind it — so this
	// is a set per session, not one string. Marked the moment the close
	// finds the attempt in either place (markClosedWhileOpeningLocked), kept
	// under the attempt's own id so it survives a queued attempt's later
	// transition into "opening" (dequeueNextOpenLocked) unchanged, and
	// consumed at install time: a match means the store's answer arrived too
	// late to matter, and is discarded rather than resurrecting a block the
	// renderer was already told is gone. Cleared by that consumption and by
	// detach.
	closedWhileOpening map[session.ID]map[string]struct{}
	// lost are the fences whose boundary this coordinator accepted and could
	// never tell the helper (nocx-2v80t.3.29), newest last, bounded by
	// maxPendingEnds. A block whose fence is here was sealed as a gap, or is
	// sealed as one the moment its fence is published; an end marker that
	// names one after all (an attempt that timed out had landed) is dropped,
	// never parked — a parked end holds every later row back as the next
	// interval's. Cleared by detach.
	lost map[session.ID][]string
	// unrecorded names the sessions whose stream is not being recorded
	// (nocx-2v80t.3.36): a buffer overflowed — the helper's, which said so
	// with its incomplete marker, or this stream's own — at the row
	// unrecordedFrom names. Until the next end marker arrives, rows are
	// confirmed and not kept, and a command whose completion arrives with no
	// end of its own waiting is settled incomplete from that completion; the
	// first end that arrives closes its block incomplete and ends the state.
	unrecorded map[session.ID]uint64
	// budgets is each session's buffer in bytes, fixed at attach from the
	// server's configured value: what may be held here while the store is
	// slow before the block in flight ends incomplete.
	budgets map[session.ID]int64
	// flushingBytes is what a session's batch costs the buffer while
	// flushPendingRows is delivering it to the store (nocx-2v80t.3.49).
	// takeForFlushLocked removes the batch from bs.pending and sets this in
	// the same lock hold — the one writer in production — so
	// requeuePendingRows never sees it twice, but the rows have not landed
	// either — AppendBlockRows can block for as long as the store is slow —
	// so heldBytesLocked counts it from here, set to the batch's own cost
	// before the first call and walked down by exactly what each call lands,
	// until the batch is fully delivered (reaching zero) or a failure moves
	// what remains back into bs.pending (requeuePendingRows, which clears
	// this the same call).
	flushingBytes map[session.ID]int64
	// attachSeq is the stream's one monotonic attachment counter: attach()
	// stamps it into attachGen[sid], so every attachment — of any session —
	// gets a value no earlier attachment ever held (nocx-2v80t.3.53). It is
	// what lets detach reclaim the per-session stamp: a reclaimed value can
	// never be minted again, so a re-attach under the same id cannot accept
	// an open reserved under an older generation.
	attachSeq uint64
	// attachGen is each session's CURRENT attachment generation, stamped by
	// every attach() including the first (nocx-2v80t.3.51): a session that
	// detaches and re-attaches under the same id gets a new generation, and
	// an open reserved under an old one is checked against the CURRENT value
	// at install, not merely against sources[sid]'s presence — a detach
	// followed by a re-attach leaves sources[sid] populated again, so
	// presence alone cannot tell the two attachments apart. Reclaimed by
	// detach (nocx-2v80t.3.53); attachSeq keeps the next stamp unique.
	attachGen map[session.ID]uint64
	// orphanSeals are the rows a discarded open's own cleanup CloseBlockRows
	// failed to seal (nocx-2v80t.3.51): the attempt is already fully
	// forgotten everywhere else in this stream's bookkeeping and the
	// renderer was already told kept:false, so without this the store would
	// hold the row open forever with nothing left to name it. Retried
	// opportunistically at the next close (closeBlockRowsNow) or flush
	// (drainPendingCloses) and forced at detach, up to maxCloseAttempts per
	// row; past that it is logged as a stated, permanent loss and dropped —
	// retryOrphanSeals owns all of it. Cleared by detach once it has taken
	// its own forced pass.
	orphanSeals map[session.ID][]*orphanSeal
}

// orphanSeal is one row retryOrphanSeals still owes a seal, and how many
// times sealing it has already been tried.
type orphanSeal struct {
	entry      string
	artifactID string
	tries      uint8
}

// DefaultBlockRowsBufferBytes is the coordinator's buffer when nothing is
// configured (nocx-2v80t.3.36): sized like the helper's, since it holds what
// the helper's buffer sends.
const DefaultBlockRowsBufferBytes int64 = 20 << 20

// MinBlockRowsBufferBytes is the coordinator buffer's floor
// (nocx-2v80t.3.38), the helper's own (internal/helper/session's
// MinRowBufferBytes): 4 MiB holds one closing screen of 400 × 250 cells, so
// an end marker can always be held whole. A setting below it gets it.
const MinBlockRowsBufferBytes int64 = 4 << 20

// heldRowsBytes is what rows cost the coordinator's buffer: the cells as the
// emulator hands them over, plus the row headers — the helper's own measure
// (internal/helper/session's emissionBytes), so both buffers count alike.
func heldRowsBytes(rows []emulator.Row) int64 {
	var n int64
	for _, r := range rows {
		n += int64(unsafe.Sizeof(r)) + int64(len(r.Cells))*int64(unsafe.Sizeof(emulator.Cell{}))
		for _, c := range r.Cells {
			n += int64(len(c.Grapheme))
		}
	}
	return n
}

// pendingRowsBytes is what a whole batch of deliveries costs the buffer —
// heldRowsBytes summed over each one — the measure beginFlushLocked counts
// a batch's in-flight cost by (nocx-2v80t.3.49): every queue this stream
// holds rows or closing screens in, and a batch blocked in the store is one
// of them even though it left bs.pending to get there.
func pendingRowsBytes(pending []pendingRows) int64 {
	var n int64
	for _, d := range pending {
		n += heldRowsBytes(d.rows)
	}
	return n
}

// beginFlushLocked records a batch as flushingBytes (nocx-2v80t.3.51).
// Production reaches it through takeForFlushLocked alone — the one helper
// that extracts a batch on its way to flushPendingRows — and a test staging
// an in-flight batch calls it to stand in for that caller.
func (bs *blockStream) beginFlushLocked(sid session.ID, batch []pendingRows) {
	if bs.flushingBytes == nil {
		bs.flushingBytes = make(map[session.ID]int64)
	}
	bs.flushingBytes[sid] = pendingRowsBytes(batch)
}

// takeAllPendingRows is takeForFlushLocked's endRow for "the whole queue":
// no delivery begins at or beyond the largest absolute row, so the split
// would take everything and leave nothing behind — and is skipped entirely,
// so the whole-queue take keeps the batch's own slice.
const takeAllPendingRows uint64 = math.MaxUint64

// takeForFlushLocked removes the session's pending batch — every delivery
// below endRow, or the whole queue for takeAllPendingRows — from bs.pending
// and counts it in flight, in the SAME lock hold the caller is already in
// (nocx-2v80t.3.51). It is the only way a batch leaves bs.pending on its way
// to flushPendingRows: while each caller extracted by hand, the count was a
// separate step a call site could forget, and the review round proved it by
// commenting the count out of one call site with every test staying green.
// What a split leaves past endRow stays in bs.pending, counted where it
// waits.
func (bs *blockStream) takeForFlushLocked(sid session.ID, endRow uint64) []pendingRows {
	batch := bs.pending[sid]
	delete(bs.pending, sid)
	if endRow != takeAllPendingRows {
		var remaining []pendingRows
		batch, remaining = splitPendingRowsAt(batch, endRow)
		if len(remaining) > 0 {
			bs.pending[sid] = remaining
		}
	}
	bs.beginFlushLocked(sid, batch)
	return batch
}

// heldBytesLocked is what the session's buffer holds now: the rows waiting
// for the store, the rows held past a parked boundary, the closing screens of
// ends waiting behind them, and whatever batch flushPendingRows currently has
// in flight to the store (flushingBytes) — a queue by another name, since it
// is rows this stream is holding until the store answers, and ADR-0075's
// "every queue" rule counts it exactly as it counts the others. Derived from
// what is held, so it cannot drift from it.
func (bs *blockStream) heldBytesLocked(sid session.ID) int64 {
	n := bs.flushingBytes[sid]
	for _, set := range [][]pendingRows{bs.pending[sid], bs.beyond[sid]} {
		for _, d := range set {
			n += heldRowsBytes(d.rows)
		}
	}
	for _, set := range [][]pendingEnd{bs.pendingCloses[sid], bs.ends[sid], bs.queuedEnds[sid]} {
		for _, e := range set {
			n += heldRowsBytes(e.closing)
		}
	}
	return n
}

// holdEndLocked is holdLocked for an end about to be queued — parked for its
// completion, held behind a deferred append, or queued for a block not yet
// current (nocx-2v80t.3.38). An end is never dropped: it is the boundary of a
// real command. What the buffer cannot hold is its closing screen, so an end
// whose screen would take the buffer past its bound is kept without it and
// closes its block incomplete — the owner's rule, for the block in flight.
func (s *WSServer) holdEndLocked(sid session.ID, e *pendingEnd) {
	if len(e.closing) == 0 {
		return
	}
	bs := s.blockStream
	if bs.heldBytesLocked(sid)+heldRowsBytes(e.closing) <= bs.budgets[sid] {
		return
	}
	s.log.Warn("block rows buffer overflowed: an end's closing screen is not kept, its block ends incomplete",
		"session", sid, "endRow", e.endRow, "closingRows", len(e.closing), "bufferBytes", bs.budgets[sid])
	e.closing = nil
	e.incomplete = true
}

// holdLocked answers whether rows may be held for the session: false when
// they would take its buffer past its bound, in which case the buffer has
// overflowed and the stream is not recorded from fromRow on (unrecorded).
func (s *WSServer) holdLocked(sid session.ID, fromRow uint64, rows []emulator.Row) bool {
	bs := s.blockStream
	if bs.heldBytesLocked(sid)+heldRowsBytes(rows) <= bs.budgets[sid] {
		return true
	}
	bs.markUnrecordedLocked(sid, fromRow)
	s.log.Warn("block rows buffer overflowed: the block in flight ends incomplete",
		"session", sid, "fromRow", fromRow, "bufferBytes", bs.budgets[sid])
	return false
}

func (bs *blockStream) markUnrecordedLocked(sid session.ID, fromRow uint64) {
	if bs.unrecorded == nil {
		bs.unrecorded = make(map[session.ID]uint64)
	}
	if _, already := bs.unrecorded[sid]; !already {
		bs.unrecorded[sid] = fromRow
	}
}

// SetBlockRowsBufferBytes sets the coordinator's buffer for sessions attached
// from now on (nocx-2v80t.3.36); a session keeps the value it was attached
// with. Zero or less restores the default.
func (s *WSServer) SetBlockRowsBufferBytes(n int64) {
	s.blockRowsBufferBytes.Store(n)
}

// BlockOutputIncomplete is the helper's one marker that its row buffer
// overflowed at fromRow (nocx-2v80t.3.36): the block in flight ends
// incomplete, and nothing is recorded until the next command starts after
// the stream is healthy. No block is resolved here — "whichever is current"
// is not an answer while another close may be in flight — so each is settled
// by its own fence: from its completion, or by the first end that follows.
//
// A completion may arrive BEFORE its output does (ADR-0024 decision 7), so
// its fence may already be here, waiting for an end marker, when this marker
// arrives (nocx-2v80t.3.38). The marker comes in stream order after every
// end the helper did deliver, so a fence still waiting now is an interval
// whose end the overflow took: it is settled here, incomplete, by its own
// fence — and its end, should the helper ever carry one, is dropped as a
// lost fence's. Otherwise that block would stay current and take the rows
// of the next command after recovery.
func (s *WSServer) BlockOutputIncomplete(sid session.ID, fromRow uint64) {
	// Owner: this stream, inside one delivery of the helper's rows plane.
	// Closing event: the delivery's return — its appends, and any close it
	// drains, are the last writes it causes.
	ctx := log.WithLogger(context.Background(), s.log)
	bs := s.blockStream
	bs.mu.Lock()
	if _, sourced := bs.sources[sid]; !sourced {
		bs.mu.Unlock()
		return
	}
	bs.markUnrecordedLocked(sid, fromRow)
	type waiting struct{ attempt, fence string }
	var settle []waiting
	for fence, attempt := range bs.fences[sid] {
		if attempt == "" || bs.isLostLocked(sid, fence) {
			continue
		}
		if block := bs.open[sid][attempt]; block == nil || block.settled {
			continue
		}
		bs.rememberLostLocked(sid, fence)
		settle = append(settle, waiting{attempt: attempt, fence: fence})
	}
	bs.mu.Unlock()
	s.log.Warn("helper row buffer overflowed: the block in flight ends incomplete",
		"session", sid, "fromRow", fromRow, "settledByFence", len(settle))
	for _, w := range settle {
		s.closeBlockRows(ctx, sid, w.attempt, fromRow, nil, w.fence, true)
	}
}

type openBlock struct {
	attempt    string
	entry      string
	artifactID string
	kept       bool
	// rows is the artifact's own cursor: the exclusive end of everything
	// this stream has successfully appended to it. The store enforces that
	// cursor (a delivery behind it is refused, a jump ahead of it must name
	// what went missing), so this is the same number it holds, and it exists
	// so the CLOSE can place its closing screen where the artifact really is
	// rather than where the interval's index says it should be
	// (nocx-2v80t.3.9). Guarded by blockStream.mu.
	rows uint64
	// floor is the absolute index the artifact's stored span begins at —
	// the store's FirstRow, read at the re-bind. The stored span is
	// [floor, rows): a delivery reaching below the floor is the block's
	// own head (rows that departed before its open, offered again by the
	// resend, nocx-zg3k3.5.3 Round 8) and goes back to the store as a
	// prepend; the trim and every confirmation cover [floor, rows) and
	// never a span the artifact does not hold. Guarded by blockStream.mu.
	floor uint64
	// held is the block's own below-floor head, offered again by the
	// resend while the chain could not yet join the stored span: a
	// prepend must END at the floor, and the resend's batches may land
	// inside the gap (the loaded R33 run: [0,32) refused against a
	// floor of 57, [32,57) accepted, and the second batch's ack leapt
	// over the refused [0,32) — nocx-zg3k3.5.3 Round 9). The chain
	// grows downward/continuations until it reaches the floor, then
	// joins in one prepend. Bounded by the gap itself; never
	// confirmed while held; dropped only where the block seals without
	// it (the same honest loss as before this chain existed). Guarded
	// by blockStream.mu.
	heldFrom uint64
	held     []emulator.Row
	// settled is whether this block has been said closed — by its own end,
	// a lost boundary or the session's detach, whichever came first. The
	// one that sets it seals and says block.closed; any other finds it set
	// and does neither (nocx-2v80t.3.29). Guarded by blockStream.mu.
	settled bool
	// sealing is whether an ordinary close's seal is in the store right now
	// (nocx-2v80t.3.37). A detach that finds it set leaves the block to that
	// close, which seals it once and says what the seal did; one that finds
	// it clear claims the block (settled) and seals it itself, and a close
	// arriving after that seals nothing. One seal per block either way.
	// Guarded by blockStream.mu.
	sealing bool
	// closingIn is whether this interval's closing screen already reached
	// the artifact. A close that committed its closing rows and then failed
	// to seal must not append them a second time: the retry places them at
	// the artifact's cursor now, where a second copy would be ACCEPTED
	// instead of refused by continuity (nocx-2v80t.3.9).
	closingIn bool
}

type pendingRows struct {
	from  uint64
	lost  uint64
	cause string
	rows  []emulator.Row
}

type pendingEnd struct {
	attempt string
	nonce   string // hex, the completion's own spelling
	endRow  uint64
	closing []emulator.Row
	// incomplete seals the block as a gap: its boundary was settled without
	// its fence, or its delivery was lost (nocx-2v80t.3.29).
	incomplete bool
	// waiting marks an end parked by publishFence's resolution or the
	// still-owed guard (nocx-zg3k3.5.11 Round 9): the artifact's cursor is
	// short of the boundary while the rows source is attached, and the end
	// waits for the deliveries that will advance it. A waiting park spends
	// no attempt — waiting is not failing — and a delivery must not be
	// held behind it: the delivery is what advances the cursor the wait
	// is on.
	waiting bool
}

// parkedBoundary is the boundary an interval end marker has already stated
// while its fence is still on its way: the earliest EndRow among the ends
// parked for their completions. Rows at and above it streamed after that
// boundary and belong to the interval that follows. Derived from the parked
// ends rather than stored, so it cannot drift from them.
func (bs *blockStream) parkedBoundary(sid session.ID) (uint64, bool) {
	var bound uint64
	found := false
	for _, end := range bs.ends[sid] {
		if !found || end.endRow < bound {
			bound, found = end.endRow, true
		}
	}
	return bound, found
}

// mergePendingRows joins two index-ordered deliveries into one, keeping the
// order: the store appends a block's rows in order and refuses a delivery
// that starts anywhere but its own cursor, so the queue a flush reads is
// ordered by the absolute row each delivery begins at.
func mergePendingRows(a, b []pendingRows) []pendingRows {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make([]pendingRows, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].from <= b[j].from {
			merged = append(merged, a[i])
			i++
			continue
		}
		merged = append(merged, b[j])
		j++
	}
	merged = append(merged, a[i:]...)
	return append(merged, b[j:]...)
}

// splitPendingRowsAt separates deliveries at an interval's absolute end row.
// A flush may already have rows from the next interval waiting behind the
// current append; those rows must follow promotion, not be written to the
// block whose close is parked.
func splitPendingRowsAt(deliveries []pendingRows, endRow uint64) (before, after []pendingRows) {
	for _, delivery := range deliveries {
		deliveryEnd := delivery.from + uint64(len(delivery.rows)) // #nosec G115 -- row count arithmetic
		switch {
		case len(delivery.rows) == 0:
			// A loss-only delivery sits at the END of the gap it names, so
			// one AT the end row is a hole inside the interval that end
			// closes — its tail — never the next interval's (nocx-2v80t.3.26).
			if delivery.from <= endRow {
				before = append(before, delivery)
			} else {
				after = append(after, delivery)
			}
		case delivery.from >= endRow:
			after = append(after, delivery)
		case deliveryEnd <= endRow:
			before = append(before, delivery)
		default:
			split := int(endRow - delivery.from) // #nosec G115 -- bounded by len(rows)
			before = append(before, pendingRows{
				from: delivery.from, lost: delivery.lost, rows: delivery.rows[:split],
			})
			after = append(after, pendingRows{
				from: endRow, rows: delivery.rows[split:],
			})
		}
	}
	return before, after
}

func (bs *blockStream) takePendingRows(sid session.ID, attempt string) []pendingRows {
	bs.mu.Lock()
	// Rows of the interval that follows wait behind the close parked for
	// this attempt's end: everything below that end goes out with this
	// batch, the rest stays queued for the block that follows.
	endRow := takeAllPendingRows
	for _, end := range bs.pendingCloses[sid] {
		if end.attempt == attempt {
			endRow = end.endRow
			break
		}
	}
	next := bs.takeForFlushLocked(sid, endRow)
	bs.mu.Unlock()
	return next
}

// blockGrewParams is block.grew's payload (contracts/block.grew.schema.json).
type blockGrewParams struct {
	EntryID string `json:"entryId"`
	From    uint64 `json:"from"`
	Count   uint64 `json:"count"`
}

// blockClosedParams is block.closed's payload
// (contracts/block.closed.schema.json).
type blockClosedParams struct {
	EntryID string `json:"entryId"`
	// Kept says whether the store holds rows for this block. False is a
	// block the keep decision refused, or one no store could open: nothing
	// will ever be readable, and a renderer has nothing to fetch.
	Kept bool `json:"kept"`
}

// AttachBlockRows registers a session's helper rows callbacks as this
// stream's source. Without a source the stream is inert — see the header.
//
// Before the source is exposed, the stream re-binds the session to the
// open block the store still holds (adoptOpenBlock): a session re-adopted
// after a coordinator restart owns a block opened by an authenticated
// start BEFORE the restart, still open in the durable store with its row
// cursor — that is what the no-seal detach preserves — and its rows
// continue it by absolute departed-row index. No lifecycle fact is waited
// for: the re-adopted lane stays Desynchronized until the shell answers
// the snapshot at its post-command prompt, and until then the very fact
// the block stream would wait on is quarantined, so the tail rows would
// land on no block and be dropped (ADR-0076 decision 3).
func (s *WSServer) AttachBlockRows(sid session.ID) {
	found := s.adoptableOpenBlock(sid)
	s.blockStream.attach(sid, nil, s.blockRowsBuffer())
	s.blockStream.adoptOpenBlock(sid, found)
	// The helper's end fact may have arrived before this lane registered
	// (the shell exited while the coordinator was away): the adopted
	// domain's recorded terminal state settles the session now (Round 10).
	// Idempotent — HelperSessionEnded seals only open blocks. Behind an
	// armed end hold (nocx-zg3k3.5.11 Round 4) the settle waits for the
	// replayed window instead: consulting now would read a kernel that has
	// ingested nothing yet, and the exit's own post-hold settle would race
	// the teardown's unregistering.
	if s.settleWhenEndHoldLifts(sid) {
		return
	}
	if s.adoptedDomainTerminal(sid) {
		s.HelperSessionEnded(sid)
	}
}

// AttachBlockRowsWithConfirmation additionally gives deferred rows a way to
// advance the helper's watermark after the bind retry has persisted them.
// It re-binds from the store exactly as AttachBlockRows does.
func (s *WSServer) AttachBlockRowsWithConfirmation(sid session.ID, confirm func(uint64)) {
	found := s.adoptableOpenBlock(sid)
	s.blockStream.attach(sid, confirm, s.blockRowsBuffer())
	s.blockStream.adoptOpenBlock(sid, found)
	// The same boundary consult as AttachBlockRows (Round 10), and the same
	// end-hold deferral (nocx-zg3k3.5.11 Round 4).
	if s.settleWhenEndHoldLifts(sid) {
		return
	}
	if s.adoptedDomainTerminal(sid) {
		s.HelperSessionEnded(sid)
	}
}

// adoptableOpenBlock is the store half of the re-adopt re-bind: the open
// block this session still holds in the store, if any. No store, or a
// failed read, is no block — the stream attaches as ever and rows wait on
// the ordinary lifecycle path.
func (s *WSServer) adoptableOpenBlock(sid session.ID) content.OpenBlockRowsEntry {
	store := s.blockStore()
	if store == nil {
		return content.OpenBlockRowsEntry{}
	}
	// Owner: this stream, at the re-adopting attach, on behalf of the block
	// a previous coordinator process opened. Closing event: the read —
	// nothing is held past it.
	ctx := log.WithLogger(context.Background(), s.log)
	found, err := store.OpenBlockRowsForSession(ctx, string(sid))
	if err != nil {
		s.log.Warn("block rows re-adopt read failed", "session", sid, "error", err)
		return content.OpenBlockRowsEntry{}
	}
	// Probe (nocx-zg3k3.5.3 round 7): which open block — entry, artifact,
	// cursor — the re-adopt re-binds from, or that none survives.
	if found.EntryID != "" {
		log.From(ctx).Debug("block rows re-bind: the store holds the session's open block",
			"session", sid, "entry", found.EntryID, "artifact", found.ArtifactID,
			"cursor", found.NextRow)
	} else {
		log.From(ctx).Debug("block rows re-bind: the store holds no open block",
			"session", sid)
	}
	return found
}

// adoptOpenBlock installs the store's open block into a freshly attached
// stream: the same shape performOpen's install gives a block this process
// opened — attempt, entry (one id by construction), artifact, kept — plus
// the durable cursor, because an artifact this process did not open is
// already holding rows. That cursor is the truth about what the artifact
// holds: a lifecycle open racing this install carries the same artifact at
// rows 0, and every tail delivery would sit behind a store refusal (a
// cursor at 0 turns the first delivery at the real cursor into a
// discontinuity), so the merge lifts rows to the store's answer and never
// lowers it, and the artifact identity the store names is the one kept.
//
// A later open — a NEW command after the re-adopt — may already own the
// stream by the time this lands: the recovered block stays named in
// bs.open so the end its own interval still owes can close it, and current
// keeps the later command's rows. Otherwise the recovered block IS
// current: the session's rows continue it from the artifact's cursor.
func (bs *blockStream) adoptOpenBlock(sid session.ID, found content.OpenBlockRowsEntry) {
	if found.EntryID == "" {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if _, sourced := bs.sources[sid]; !sourced {
		// The attachment this read was made for is gone already.
		return
	}
	if bs.open == nil {
		bs.open = make(map[session.ID]map[string]*openBlock)
	}
	if bs.open[sid] == nil {
		bs.open[sid] = make(map[string]*openBlock)
	}
	if bs.current == nil {
		bs.current = make(map[session.ID]*openBlock)
	}
	b := bs.open[sid][found.EntryID]
	if b == nil {
		b = &openBlock{
			attempt: found.EntryID, entry: found.EntryID,
			artifactID: found.ArtifactID, kept: true,
		}
		bs.open[sid][found.EntryID] = b
	}
	if b.rows < found.NextRow {
		b.rows = found.NextRow
	}
	if found.NextRow > 0 {
		b.floor = found.FirstRow
	}
	if bs.current[sid] == nil {
		bs.current[sid] = b
	}
}

// A LIFECYCLE FRAME THAT FAILS CHANGES NO BLOCK (ADR-0077 decision 9, from
// ADR-0076 decision 2: a block's state changes only on something the helper
// reports, and a store write failing is not that). A transaction that fails
// under a frame is begun again by the store where it failed, so this stream
// never sees that failure: what it decides is what a frame that never failed
// decides, and the store commits exactly that. What reaches this stream is a
// frame that failed every attempt — every store call it still makes answers
// content.ErrLifecycleFrameFailed — and that is no answer about any block:
// the branches that decide on a store error return on it at once
// (frameFailed), and when the frame ends the session's block state is read
// again from the store (rebindIfFrameFails), so whatever the frame's
// projection did in memory before its last attempt failed is gone with the
// rows it wrote.

// frameFailed is a store error that is a lifecycle frame's failure for good.
func frameFailed(err error) bool {
	return errors.Is(err, content.ErrLifecycleFrameFailed)
}

// frameRebind keys one session's re-read in one frame.
type frameRebind struct {
	s   *WSServer
	sid session.ID
}

// rebindIfFrameFails arranges that, when the lifecycle frame ctx carries ends
// without committing, the session's block state is read again from the store
// — once per frame, whichever of its facts touched the session first.
// Outside a frame it does nothing.
func (s *WSServer) rebindIfFrameFails(ctx context.Context, sid session.ID) {
	lifecyclecommit.After(ctx, frameRebind{s: s, sid: sid}, func(committed bool) {
		if !committed {
			s.rebindBlockRowsFromStore(sid)
		}
	})
}

// rebindBlockRowsFromStore re-reads a session's block state from the store,
// the way a coordinator that went away and came back does (ADR-0076): the
// stream forgets what it held for the session and changes no block
// (detachCoordinator), and the open block the store holds is installed again
// at the store's own cursor (adoptOpenBlock). The attachment — its source,
// its helper confirmation, its buffer — is kept.
func (s *WSServer) rebindBlockRowsFromStore(sid session.ID) {
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	confirm, budget := bs.confirmers[sid], bs.budgets[sid]
	bs.mu.Unlock()
	if !sourced {
		return
	}
	found := s.adoptableOpenBlock(sid)
	bs.detachCoordinator(sid)
	bs.attach(sid, confirm, budget)
	bs.adoptOpenBlock(sid, found)
	s.log.Warn("block rows: a lifecycle frame failed every attempt; the session's block state is read again from the store",
		"session", sid, "entry", found.EntryID, "artifact", found.ArtifactID, "cursor", found.NextRow)
}

// blockRowsBuffer is the buffer a session attached now gets.
func (s *WSServer) blockRowsBuffer() int64 {
	n := s.blockRowsBufferBytes.Load()
	if n <= 0 {
		return DefaultBlockRowsBufferBytes
	}
	return max(n, MinBlockRowsBufferBytes)
}

func (bs *blockStream) attach(sid session.ID, confirm func(uint64), budget int64) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.queuedEnds == nil {
		bs.queuedEnds = make(map[session.ID][]pendingEnd)
	}
	if bs.sources == nil {
		bs.sources = make(map[session.ID]struct{})
	}
	if bs.queued == nil {
		bs.queued = make(map[session.ID]string)
	}
	if bs.confirmers == nil {
		bs.confirmers = make(map[session.ID]func(uint64))
	}
	if bs.waiting == nil {
		bs.waiting = make(map[session.ID]string)
	}
	if bs.pending == nil {
		bs.pending = make(map[session.ID][]pendingRows)
	}
	if bs.closedThrough == nil {
		bs.closedThrough = make(map[session.ID]uint64)
	}
	if bs.beyond == nil {
		bs.beyond = make(map[session.ID][]pendingRows)
	}
	if bs.beyondThrough == nil {
		bs.beyondThrough = make(map[session.ID]uint64)
	}
	if bs.flushing == nil {
		bs.flushing = make(map[session.ID]bool)
	}
	if bs.pendingCloses == nil {
		bs.pendingCloses = make(map[session.ID][]pendingEnd)
	}
	if bs.closing == nil {
		bs.closing = make(map[session.ID]bool)
	}
	if bs.closeTries == nil {
		bs.closeTries = make(map[session.ID]map[string]uint8)
	}
	bs.sources[sid] = struct{}{}
	bs.confirmers[sid] = confirm
	if bs.budgets == nil {
		bs.budgets = make(map[session.ID]int64)
	}
	bs.budgets[sid] = budget
	// A new generation every attach, including the first, so an open
	// reserved under a since-detached attachment can never match the one
	// live now (nocx-2v80t.3.51). Stamped from one stream-wide counter and
	// reclaimed at detach (nocx-2v80t.3.53): the counter, not the stamp's
	// survival, is what keeps a re-attach under the same id from accepting
	// an older generation — detach forgets this sid's stamp, and the next
	// attach mints a value no earlier open was reserved under.
	if bs.attachGen == nil {
		bs.attachGen = make(map[session.ID]uint64)
	}
	bs.attachSeq++
	bs.attachGen[sid] = bs.attachSeq
}

// DetachBlockRows ends a session's streaming: every still-open block is
// sealed with whatever arrived — the honest end when the interval's own end
// never will. The composition root calls it where the session's helper
// callbacks are torn down.
// DetachBlockRows ends a session's streaming attachment: the coordinator is
// going away, and per ADR-0076 that changes NO block — the open block, its
// cursor and its open ledger entry stay exactly as they are in the store,
// and the re-adopted stream continues them. Only the helper-reported end of
// the session settles open blocks: HelperSessionEnded.
// adoptedDomainTerminal answers whether any lane bound to this session
// carries a domain the helper already closed or lost — the helper's own
// recorded end, held by the kernel even when the fact arrived before the
// lane registered (nocx-zg3k3.5.3 Round 10).
func (s *WSServer) adoptedDomainTerminal(sid session.ID) bool {
	if s.lifecyclePub == nil {
		return false
	}
	s.lifecycleMu.Lock()
	var lanes []lifecycle.LaneID
	for lane, cur := range s.lifecycleLanes {
		if cur == sid {
			lanes = append(lanes, lane)
		}
	}
	s.lifecycleMu.Unlock()
	for _, lane := range lanes {
		if _, ok := s.lifecyclePub.TerminalDomainOfLane(lane); ok {
			return true
		}
	}
	return false
}

func (s *WSServer) DetachBlockRows(sid session.ID) {
	s.blockStream.detachCoordinator(sid)
}

// HelperSessionEnded settles a session's streaming because THE HELPER
// REPORTED the session's end (ADR-0074 decision 3, as amended by ADR-0076):
// every still-open block is sealed with whatever arrived and said closed —
// the honest end when the interval's own end never will.
func (s *WSServer) HelperSessionEnded(sid session.ID) {
	// Behind an armed end hold the report waits for the replay it follows:
	// settling now would close, "unknown", a command whose own completion
	// the replay is about to deliver (awaitSessionEnd).
	s.awaitSessionEnd(sid)
	// Owner: this stream, on behalf of the session the helper just
	// reported ended. Closing event: HelperSessionEnded's own seals —
	// nothing is held past them (ADR-0076).
	ctx := log.WithLogger(context.Background(), s.log)
	// The session's open entry is resolved BEFORE the sealing detach: the
	// detach seals the block's artifact, and the read below keys on the
	// artifact being open.
	openEntry := ""
	if bsStore := s.blockStore(); bsStore != nil {
		if open, openErr := bsStore.OpenBlockRowsForSession(ctx, string(sid)); openErr == nil && open.EntryID != "" {
			s.log.Debug("helper session end: an open block will be sealed at its stored cursor",
				"session", sid, "entry", open.EntryID)
		}
	}
	if store := s.blockStore(); store != nil {
		if open, err := store.OpenBlockRowsForSession(ctx, string(sid)); err == nil {
			openEntry = open.EntryID
		}
	}
	for _, closed := range s.blockStream.detach(ctx, s.blockStore(), sid) {
		s.notifyBlockSubscriber(ctx, sid, "block.closed", closed)
	}
	// The settle can run before the re-adopting stream has installed the
	// open block (the terminal-state consult at the attach boundary,
	// nocx-zg3k3.5.3 Round 10): the store's own open artifact then seals at
	// its stored cursor here. Idempotent — an artifact the in-memory path
	// just sealed answers sealed and changes nothing.
	if bsStore := s.blockStore(); bsStore != nil {
		if open, openErr := bsStore.OpenBlockRowsForSession(ctx, string(sid)); openErr == nil && open.EntryID != "" {
			if _, sealErr := bsStore.CloseBlockRows(ctx, content.CloseBlockRows{
				EntryID: open.EntryID, ArtifactID: open.ArtifactID,
			}); sealErr != nil {
				log.From(ctx).Warn("helper session end: the store's open block could not be sealed at its stored cursor",
					"session", sid, "entry", open.EntryID, "artifact", open.ArtifactID, "error", sealErr)
			} else {
				s.log.Debug("helper session end: the store's open block sealed at its stored cursor",
					"session", sid, "entry", open.EntryID, "artifact", open.ArtifactID)
			}
		}
	}
	// The helper's end report also closes the session's still-open LEDGER
	// entry (ADR-0074 decision 3 as amended by ADR-0076): the completion
	// fact is never coming — the kernel that would have turned it into a
	// close is gone, and on a re-adopt the new kernel never saw the
	// attempt — so the entry closes unknown with the transport gone,
	// exactly the verdict a live kernel records for a session whose shell
	// vanished. The open entry is the session's own (the entry id IS the
	// attempt id), so the fact rides the ordinary writer.
	if openEntry != "" {
		s.syncLifecycleLedger(ctx, lifecyclepub.Fact{
			Attempt: &lifecyclepub.Attempt{
				ID: openEntry, State: lifecyclepub.AttemptUnknown, Origin: lifecyclepub.OriginShell,
			},
		})
	}
}

// LifecycleRangeLost settles what a lost stretch of the helper's lifecycle
// stream could have settled (ADR-0077). A re-adopt asks for the stream from
// the cursor this machine stored; when the helper's window no longer reaches
// back that far it answers from its base instead, and every frame between —
// possibly the end of the command the session's open block belongs to — is
// gone. That block is not left running on the hope that its end was not in
// the gap: it is sealed as a block whose boundary never arrived whole
// (Incomplete, the store's 'gap' truncation — the same statement a lost
// boundary makes, BlockBoundaryLost), and its entry closes unknown, the
// verdict a kernel records for an attempt whose end it cannot know. The
// helper's own word is what triggers it — its reset names the lost range —
// never the coordinator's absence (ADR-0076 decision 2).
//
// It runs before the re-adopted stream re-binds the session, so there is no
// in-memory block yet: the store's open block is the one to settle.
func (s *WSServer) LifecycleRangeLost(sid session.ID) {
	// Owner: this stream, on behalf of the open block a lost lifecycle
	// range could have settled. Closing event: the seal and the entry's
	// close below — nothing is held past them.
	ctx := log.WithLogger(context.Background(), s.log)
	store := s.blockStore()
	if store == nil {
		return
	}
	open, err := store.OpenBlockRowsForSession(ctx, string(sid))
	if err != nil || open.EntryID == "" {
		log.From(ctx).Info("lifecycle range lost: the session holds no open block to settle",
			"session", sid, "error", err)
		return
	}
	if _, sealErr := store.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: open.EntryID, ArtifactID: open.ArtifactID, Incomplete: true,
	}); sealErr != nil {
		log.From(ctx).Warn("lifecycle range lost: the open block could not be sealed",
			"session", sid, "entry", open.EntryID, "artifact", open.ArtifactID, "error", sealErr)
		return
	}
	log.From(ctx).Info("lifecycle range lost: the open block is sealed incomplete and its entry closes unknown",
		"session", sid, "entry", open.EntryID, "artifact", open.ArtifactID)
	s.syncLifecycleLedger(ctx, lifecyclepub.Fact{
		Attempt: &lifecyclepub.Attempt{
			ID: open.EntryID, State: lifecyclepub.AttemptUnknown, Origin: lifecyclepub.OriginShell,
		},
	})
}

// detach forgets the session and seals what it held, and answers the
// block.closed each still-unended interval is owed: every open block, and
// every completion whose end marker never arrived.
// detachCoordinator forgets the session's streaming state and nothing
// else: the coordinator going away changes no block (ADR-0076). Nothing is
// sealed and nothing is said closed — the open block, its cursor (in the
// artifact's payload), and its open ledger entry survive in the store, and
// a re-adopting coordinator's open resumes them exactly there. Only the
// helper-reported end of the session settles what this forgot:
// HelperSessionEnded runs the old sealing detach over whatever in-memory
// state a same-process session still holds.
func (bs *blockStream) detachCoordinator(sid session.ID) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	delete(bs.closedThrough, sid)
	delete(bs.beyond, sid)
	delete(bs.beyondThrough, sid)
	delete(bs.sources, sid)
	delete(bs.open, sid)
	delete(bs.current, sid)
	delete(bs.queued, sid)
	delete(bs.queuedEnds, sid)
	delete(bs.ends, sid)
	delete(bs.fences, sid)
	delete(bs.confirmers, sid)
	delete(bs.waiting, sid)
	delete(bs.pending, sid)
	delete(bs.flushing, sid)
	delete(bs.opening, sid)
	delete(bs.pendingCloses, sid)
	delete(bs.closing, sid)
	delete(bs.closeTries, sid)
	delete(bs.entered, sid)
	delete(bs.lost, sid)
	delete(bs.reopen, sid)
	delete(bs.closedWhileOpening, sid)
	delete(bs.unrecorded, sid)
	delete(bs.budgets, sid)
	delete(bs.flushingBytes, sid)
	delete(bs.attachGen, sid)
}

// detach is the HELPER-REPORTED settlement of a session's streaming
// (ADR-0074 decision 3 as amended by ADR-0076): every still-open block is
// sealed with whatever arrived and said closed.
func (bs *blockStream) detach(ctx context.Context, store blockOutputStore, sid session.ID) []blockClosedParams {
	bs.mu.Lock()
	opens := bs.open[sid]
	var unsettled []*openBlock
	said := make(map[string]bool)
	for _, b := range opens {
		said[b.entry] = true
		if b.settled || b.sealing {
			// Its own end, or its lost boundary, is sealing it and says so —
			// with what its seal did, since that is the seal the block gets
			// (nocx-2v80t.3.37).
			continue
		}
		// Claimed here, under the lock, so a close still in flight does not
		// say it too; what is SAID is decided below, by what the seal did.
		b.settled = true
		unsettled = append(unsettled, b)
	}
	var unended []blockClosedParams
	for _, attempt := range bs.fences[sid] {
		if attempt != "" && !said[attempt] {
			unended = append(unended, blockClosedParams{EntryID: attempt, Kept: false})
			said[attempt] = true
		}
	}
	delete(bs.closedThrough, sid)
	delete(bs.beyond, sid)
	delete(bs.beyondThrough, sid)
	delete(bs.sources, sid)
	delete(bs.open, sid)
	delete(bs.current, sid)
	delete(bs.queued, sid)
	delete(bs.queuedEnds, sid)
	delete(bs.ends, sid)
	delete(bs.fences, sid)
	delete(bs.confirmers, sid)
	delete(bs.waiting, sid)
	delete(bs.pending, sid)
	delete(bs.flushing, sid)
	delete(bs.opening, sid)
	delete(bs.pendingCloses, sid)
	delete(bs.closing, sid)
	delete(bs.closeTries, sid)
	delete(bs.entered, sid)
	delete(bs.lost, sid)
	delete(bs.reopen, sid)
	delete(bs.closedWhileOpening, sid)
	delete(bs.unrecorded, sid)
	delete(bs.budgets, sid)
	delete(bs.flushingBytes, sid)
	// The generation stamp goes with the rest of the session's state
	// (nocx-2v80t.3.53): attachSeq keeps the next attach's stamp strictly
	// newer, so reclamation costs the staleness check nothing.
	delete(bs.attachGen, sid)
	bs.mu.Unlock()
	owed := make([]blockClosedParams, 0, len(unsettled)+len(unended))
	for _, b := range unsettled {
		// Sealing what arrived: the interval's own end never will come, and
		// the rows already stored are the block's truth. block.closed with
		// kept:true says the block is sealed in history
		// (contracts/block.closed.schema.json), so it says so only when the
		// seal landed; a seal the store refused is said kept:false — nothing
		// final to read — and logged, never claimed (nocx-2v80t.3.32).
		//
		// Owner: the stream itself, on behalf of the detached session.
		// Closing event: DetachBlockRows — the session's rows callbacks are
		// gone and no later fact can name this session.
		owed = append(owed, blockClosedParams{EntryID: b.entry, Kept: sealAtDetach(ctx, store, sid, b)})
	}
	// The session's last chance: nothing after this detach will ever call
	// retryOrphanSeals for it again, so a row that still fails now is a
	// stated loss, not one more retry queued for a next flush or close that
	// is never coming (nocx-2v80t.3.51). Deliberately NOT cleared above,
	// under the earlier lock: this is what reads it.
	bs.retryOrphanSeals(ctx, store, sid, true)
	return append(owed, unended...)
}

// sealAtDetach seals one kept block at the session's detach and answers
// whether the store now holds it sealed.
func sealAtDetach(ctx context.Context, store blockOutputStore, sid session.ID, b *openBlock) bool {
	if !b.kept || store == nil {
		return false
	}
	if _, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: b.entry, ArtifactID: b.artifactID,
	}); err != nil {
		log.From(ctx).Warn("block rows seal refused at detach; the block is said closed but not kept",
			"session", sid, "entry", b.entry, "error", err)
		return false
	}
	return true
}

// BlockRowsArrived delivers one OutputRows delivery from the session's
// helper callback: the rows that left the screen, in order, at the absolute
// index they departed from, with the count the emulator pruned just before
// them and the cause that names which bucket the count belongs to when it
// is not the emulator's own (LostCauseCoordinatorUnavailable, the resend's
// counted absence, nocx-zg3k3.5.3). It answers whether the coordinator
// wants them confirmed written — the helper's "written up to here" mark. Rows that belong to no block (a
// prompt scrolling between commands) and rows of a refused command are
// confirmed and dropped; rows of a kept block are appended first; a store
// failure is the one answer that withholds the confirmation, so the helper's
// mark never claims bytes the store does not hold.
//
// A delivery with no rows and a loss is a LOSS-ONLY delivery
// (nocx-2v80t.3.26): the helper's statement of a hole with nothing after it —
// at the very end of an interval, or where the bridge dropped rows ahead of
// an end marker. It takes the same path as any other delivery, so the loss
// reaches the block's summary and the block's cursor moves to the end of the
// gap, where the closing screen then lands.
func (s *WSServer) BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row, lostCause string) (writtenUpTo uint64, confirm bool) {
	if len(rows) == 0 && lost == 0 {
		return 0, false
	}
	// Owner: this stream, inside one delivery of the helper's rows plane.
	// Closing event: the delivery's return — its appends, and any close it
	// drains, are the last writes it causes.
	ctx := log.WithLogger(context.Background(), s.log)
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	if _, unrecorded := bs.unrecorded[sid]; sourced && unrecorded {
		// Not recorded until the next command (BlockOutputIncomplete): what
		// arrives now is not kept, and the helper's mark must not claim it
		// -- the acknowledgement means STORED, so a refused span stays
		// behind the mark and the resend offers it again (nocx-zg3k3.5.3).
		bs.mu.Unlock()
		return 0, false
	}
	block := bs.current[sid]
	var head []emulator.Row
	var headFrom uint64
	fullyStored := false
	if sourced {
		if closed := bs.closedThrough[sid]; fromRow < closed {
			skip := closed - fromRow
			if skip >= uint64(len(rows)) {
				bs.mu.Unlock()
				return closed, true
			}
			rows = rows[skip:]
			fromRow = closed
			lost = 0
		}
		// Dedup by absolute row index (nocx-zg3k3.5.3): a resent delivery
		// may start behind what this block already committed. block.rows is
		// the artifact's cursor, moved by each append that landed -- the
		// direct one below and the deferred flush's own. The overlap is
		// trimmed here and the new tail appended, so the store never sees a
		// discontinuity and the confirmation names only rows the artifact
		// holds. Rows still queued for the store are NOT part of the bound:
		// they are not durable, and an overlap with them is refused by the
		// store and offered again, by which time the cursor has moved.
		if block != nil && block.kept && fromRow < block.rows {
			// The stored span is [floor, rows): a delivery reaching below
			// the floor is the block's own head, offered again by the
			// resend (nocx-zg3k3.5.3 Round 8). The below-floor part joins
			// the block's held chain; the chain prepends only when it
			// reaches the floor — a prepend must END there, and the
			// resend's batches may land inside the gap (Round 9). Nothing
			// held is ever confirmed.
			if block.floor > 0 && fromRow < block.floor {
				n := block.floor - fromRow
				if n > uint64(len(rows)) { //nolint:gosec // a row count, not a byte count
					n = uint64(len(rows))
				}
				part := rows[:n]
				switch {
				case len(block.held) == 0:
					block.heldFrom = fromRow
					block.held = append([]emulator.Row(nil), part...)
				case fromRow+uint64(len(part)) == block.heldFrom: //nolint:gosec // a row count, not a byte count
					// the part sits directly below the chain: it extends it
					block.held = append(append([]emulator.Row(nil), part...), block.held...)
					block.heldFrom = fromRow
				case fromRow >= block.heldFrom+uint64(len(block.held)): //nolint:gosec // a row count, not a byte count
					// the part continues the chain upward
					block.held = append(block.held, part...)
				default:
					// the part overlaps or precedes the chain with a hole:
					// keep the EARLIER fragment — the replaced one was
					// never confirmed, so the resend offers it again.
					block.heldFrom = fromRow
					block.held = append([]emulator.Row(nil), part...)
				}
				rows = rows[n:]
				fromRow = block.floor
				lost = 0
				if block.heldFrom+uint64(len(block.held)) == block.floor { //nolint:gosec // a row count, not a byte count
					// the chain now reaches the stored span: it joins in
					// one prepend below (after the lock, before anything
					// is confirmed).
					head = block.held
					headFrom = block.heldFrom
				}
			}
			if len(rows) > 0 {
				skip := block.rows - fromRow
				if skip >= uint64(len(rows)) {
					fullyStored = true
				} else {
					rows = rows[skip:]
					fromRow = block.rows
					lost = 0
				}
			} else {
				fullyStored = true
			}
		}
		if fullyStored {
			if len(head) == 0 {
				bs.mu.Unlock()
				if len(block.held) > 0 {
					// the chain is still short of the floor: the held rows
					// are not in the artifact, and this delivery named them
					// — nothing here may be confirmed (Round 9).
					return 0, false
				}
				return block.rows, true
			}
			bs.mu.Unlock()
			if !s.prependBlockHead(sid, block.entry, block.artifactID, headFrom, head) {
				return 0, false
			}
			bs.mu.Lock()
			block.floor = headFrom
			block.held = nil
			bs.mu.Unlock()
			return block.rows, true
		}
		if len(head) > 0 {
			// THIS BATCH COMPLETED THE HEAD AND CARRIES ROWS PAST THE
			// CURSOR (nocx-zg3k3.5.11: sealed at 39 rows of 300 on CI). The
			// resend's batches align with neither the floor nor the cursor,
			// so one batch can hold the chain's last rows, rows the artifact
			// already has, and rows it lacks. head is set only once the
			// chain reaches the floor, so it joins now, in one prepend, and
			// the tail goes on below like any delivery: returning here
			// stored neither, confirmed neither, and the resend never offers
			// a delivered batch again — every later batch then met the store
			// at the old cursor and was refused.
			bs.mu.Unlock()
			if !s.prependBlockHead(sid, block.entry, block.artifactID, headFrom, head) {
				return 0, false
			}
			bs.mu.Lock()
			block.floor = headFrom
			block.held = nil
			head = nil
		}
		// The interval's end marker may have arrived without the completion
		// that names its fence. Its EndRow is the boundary either way, and
		// anything at or above it streamed after the boundary: it is held for
		// the interval that follows rather than appended to the one that is
		// closing over it.
		if block != nil {
			if bound, parked := bs.parkedBoundary(sid); parked {
				if held := bs.beyondThrough[sid]; fromRow < held {
					skip := held - fromRow
					if skip >= uint64(len(rows)) {
						bs.mu.Unlock()
						return 0, false
					}
					rows = rows[skip:]
					fromRow = held
					lost = 0
				}
				if fromRow+uint64(len(rows)) > bound { //nolint:gosec // a row count, not a byte count
					before, after := splitPendingRowsAt([]pendingRows{{from: fromRow, lost: lost, cause: lostCause, rows: rows}}, bound)
					if len(after) == 1 && !s.holdLocked(sid, after[0].from, after[0].rows) {
						bs.mu.Unlock()
						return 0, false
					}
					if len(after) == 1 {
						bs.beyond[sid] = append(bs.beyond[sid], after...)
						held := after[len(after)-1]
						bs.beyondThrough[sid] = held.from + uint64(len(held.rows)) //nolint:gosec // a row count, not a byte count
					}
					if len(before) == 0 {
						// The whole delivery is past the boundary: it is held,
						// so the mark must not claim it is written.
						bs.mu.Unlock()
						return 0, false
					}
					fromRow, lost, rows = before[0].from, before[0].lost, before[0].rows
				}
			}
		}
	}
	waiting := bs.waiting[sid]
	flushing := bs.flushing[sid]
	pending := len(bs.pending[sid]) > 0
	closing := bs.closing[sid]
	retryClose := sourced && len(bs.pendingCloses[sid]) > 0 && !flushing && !closing
	if retryClose {
		bs.closing[sid] = true
		bs.mu.Unlock()
		bs.drainPendingCloses(ctx, s, sid)
		bs.mu.Lock()
		// A delivery is held only behind a close that may SEAL. A WAITING
		// end (nocx-zg3k3.5.11 Round 9: parked by publishFence's resolution
		// or the still-owed guard, no attempt spent) must not hold it — the
		// delivery is what advances the cursor the wait is on, and holding
		// it here would strand the tail behind a wait that only the
		// delivery can end. Recursing instead overflowed the stack: every
		// level re-parked the same end before its delivery could land.
		held := false
		for _, e := range bs.pendingCloses[sid] {
			if !e.waiting {
				held = true
				break
			}
		}
		if held {
			if !s.holdLocked(sid, fromRow, rows) {
				bs.mu.Unlock()
				return 0, false
			}
			bs.pending[sid] = append(bs.pending[sid], pendingRows{
				from: fromRow, lost: lost, cause: lostCause, rows: append([]emulator.Row(nil), rows...),
			})
			bs.mu.Unlock()
			return 0, false
		}
		// ALL remaining ends are WAITING: fall through under the
		// still-held lock — the direct arm below advances the cursor they
		// wait on, and its own completion tail re-drives them.
	}
	if sourced && pending && !flushing && !closing && block != nil && bs.queued[sid] == "" {
		bs.pending[sid] = append(bs.pending[sid], pendingRows{
			from: fromRow, lost: lost, cause: lostCause, rows: append([]emulator.Row(nil), rows...),
		})
		toFlush := bs.takeForFlushLocked(sid, takeAllPendingRows)
		bs.flushing[sid] = true
		confirm := bs.confirmers[sid]
		bs.mu.Unlock()
		bs.flushPendingRows(ctx, s, sid, block, toFlush, confirm)
		return 0, false
	}
	if sourced && (flushing || closing || (waiting != "" && block == nil)) {
		if !s.holdLocked(sid, fromRow, rows) {
			bs.mu.Unlock()
			return 0, false
		}
		if bs.pending == nil {
			bs.pending = make(map[session.ID][]pendingRows)
		}
		bs.pending[sid] = append(bs.pending[sid], pendingRows{
			from: fromRow, lost: lost, cause: lostCause, rows: append([]emulator.Row(nil), rows...),
		})
		bs.mu.Unlock()
		return 0, false
	}
	// THE DIRECT ADMISSION ARMS THE IN-FLIGHT MARK IN THE SAME LOCK HOLD
	// THAT ADMITTED IT (nocx-zg3k3.5.11 Round 5): the store call below runs
	// outside bs.mu, and until it returns, block.rows — the cursor a close
	// snapshots — names the PREVIOUS delivery. A close that resolved in
	// that window (the fence resolving a parked end, or the replayed
	// window's domain closed — both on the lifecycle goroutine, concurrent
	// with this read loop) wrote its closing screen at the stale cursor; on
	// the loaded run the two transactions interleaved, both succeeded, and
	// the sealed body lost them both (2/40, sealed at 256 of 300). A close
	// reads flushing under this same mu, so arming here leaves no window
	// between the admission and the mark; the deferred path has always held
	// the same invariant through takeForFlushLocked. The mark comes down in
	// finishDirectDelivery, on every path the store call can take.
	var store blockOutputStore
	armed := sourced && block != nil && block.kept
	if armed {
		store = s.blockStore()
		armed = store != nil
	}
	if armed {
		if bs.flushingBytes == nil {
			bs.flushingBytes = make(map[session.ID]int64)
		}
		bs.flushing[sid] = true
		bs.flushingBytes[sid] += heldRowsBytes(rows)
	}
	bs.mu.Unlock()
	if !sourced {
		return 0, false
	}
	if len(head) > 0 {
		if !s.prependBlockHead(sid, block.entry, block.artifactID, headFrom, head) {
			return 0, false
		}
		bs.mu.Lock()
		block.floor = headFrom
		block.held = nil
		bs.mu.Unlock()
	}
	writtenUpTo = fromRow + uint64(len(rows)) //nolint:gosec // a row count, not a byte count
	if block == nil || !block.kept {
		// Rows of no block, or of a keep the policy refused: dropped, and
		// NOT confirmed -- the mark means stored (nocx-zg3k3.5.3), and the
		// resend must be able to offer them again for a fresh decision.
		// Never armed above: nothing is in flight.
		return 0, false
	}
	if !armed {
		// No store is wired: nothing to append, nothing in flight.
		return 0, false
	}
	err := store.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: block.entry, ArtifactID: block.artifactID,
		FromRow: fromRow, LostRows: lost, LostCause: lostCause, Rows: rows,
	})
	if err != nil {
		// A store failure is not a refusal: the rows are still wanted, and
		// refusing to confirm is what lets them be offered again.
		log.From(ctx).Warn("block rows append failed", "session", sid, "entry", block.entry,
			"artifact", block.artifactID, "from", fromRow, "error", err)
		s.finishDirectDelivery(ctx, sid, block, rows)
		return 0, false
	}
	// Probe (nocx-zg3k3.5.3 round 7): the direct append's answer — which
	// artifact took the delivery, and what the acknowledgement will claim.
	log.From(ctx).Debug("block rows direct append: stored",
		"session", sid, "entry", block.entry, "artifact", block.artifactID,
		"from", fromRow, "rows", len(rows), "upTo", writtenUpTo)
	// The append committed, so the artifact's cursor is now the exclusive end
	// of this delivery. The close reads it to place its closing screen.
	bs.mu.Lock()
	block.rows = writtenUpTo
	bs.mu.Unlock()
	if len(rows) > 0 {
		// A loss-only delivery stored no row, so nothing grew.
		s.notifyBlockSubscriber(ctx, sid, "block.grew", blockGrewParams{
			EntryID: block.entry, From: fromRow, Count: uint64(len(rows)), //nolint:gosec // a row count, not a byte count
		})
	}
	s.finishDirectDelivery(ctx, sid, block, rows)
	return writtenUpTo, true
}

// finishDirectDelivery is the direct path's completion tail, run on every
// path the store call can take: the in-flight mark the admission armed comes
// down under bs.mu, and whatever parked behind the delivery either flushes
// (the next batch, under the same flushing ownership the deferred path
// holds) or hands the stream to its parked closes. This is what makes the
// close wait: closeBlockRows parks on flushing, and this is where flushing
// comes down.
func (s *WSServer) finishDirectDelivery(ctx context.Context, sid session.ID, block *openBlock, delivered []emulator.Row) {
	bs := s.blockStream
	bs.mu.Lock()
	bs.flushingBytes[sid] -= heldRowsBytes(delivered)
	confirm := bs.confirmers[sid]
	bs.mu.Unlock()
	// takePendingRows takes bs.mu itself, so it runs outside the hold above
	// — the deferred path's own order (flushPendingRows' tail).
	next := bs.takePendingRows(sid, block.attempt)
	if len(next) > 0 {
		bs.flushPendingRows(ctx, s, sid, block, next, confirm)
		return
	}
	bs.mu.Lock()
	bs.flushing[sid] = false
	bs.mu.Unlock()
	bs.drainPendingCloses(ctx, s, sid)
}

// noFenceNonce is the sentinel [Session.SealEnvironmentEntry] sends: a
// confirmed environment change (nocx-2v80t.3.21) has no fence, because the
// shell that would have printed one is no longer the one holding the
// terminal, so there is nothing for BlockIntervalEnded's ordinary
// fence-matching to resolve. An ordinary command's fence is 32
// cryptographically random bytes and is never this — the same reasoning
// internal/lifecycle's own recovery-nonce sentinel already rests on ("the
// read model treats zero as 'no recovery nonce'") — so it is resolved
// directly to whichever attempt is CURRENT rather than matched against one.
var noFenceNonce [32]byte

// BlockIntervalEnded delivers the session's interval end: the end marker's
// rows, the screen at the marker, and the fence the shell minted for this
// command — the same fence the authenticated completion carries. The fence
// is the authentication: an end whose fence the coordinator has not seen
// from the kernel waits (bounded) for it, and one whose fence resolves
// appends the closing rows and seals the block.
//
// noFence says the helper settled the interval without its fence ever being
// sighted (ADR-0074 decision 3): the block is sealed as a gap, which is how
// the renderer comes to say its output may be incomplete (nocx-2v80t.3.29).
// An end whose fence was already settled as lost is dropped (blockStream.lost).
//
// nonce == noFenceNonce is the one exception: an authenticated environment
// entry ends the local interval with no fence at all (nocx-2v80t.3.21), so
// it is resolved to the session's CURRENT block directly rather than parked
// waiting for a fence that will never arrive. With no current block there is
// nothing to seal, and the end is dropped rather than parked — parking it
// would wait forever for a fence noFenceNonce can never resolve.
func (s *WSServer) BlockIntervalEnded(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, noFence bool) {
	// Owner: this stream, inside one delivery of the helper's rows plane.
	// Closing event: the delivery's return — its appends, and any close it
	// drains, are the last writes it causes.
	ctx := log.WithLogger(context.Background(), s.log)
	hexNonce := hex.EncodeToString(nonce[:])
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	if _, unrecorded := bs.unrecorded[sid]; sourced && unrecorded {
		// The first end after an overflow (nocx-2v80t.3.36): the command it
		// closes ran through the overflow, so its block is incomplete, and
		// the rows after it are the next command's — recorded again.
		delete(bs.unrecorded, sid)
		if endRow > bs.closedThrough[sid] {
			bs.closedThrough[sid] = endRow
		}
		noFence = true
	}
	var attempt string
	var resolved bool
	if nonce != noFenceNonce && bs.forgetLostLocked(sid, hexNonce) {
		bs.mu.Unlock()
		return
	}
	if nonce == noFenceNonce {
		// The zero nonce is an environment entry's own end: it ends the
		// CURRENT block, and the attempt runs on under the child, so it must
		// not open a second block.
		if cur := bs.current[sid]; cur != nil {
			attempt, resolved = cur.attempt, true
			bs.markEnteredLocked(sid, attempt)
		}
	} else {
		attempt, resolved = bs.fences[sid][hexNonce]
	}
	if bs.ends == nil {
		bs.ends = make(map[session.ID][]pendingEnd)
	}
	bs.mu.Unlock()
	if !sourced {
		return
	}
	if !resolved {
		if nonce == noFenceNonce {
			// No current block to seal at the entry: nothing this session
			// streamed is open, so there is nothing an environment entry
			// could end. Never parked — a park here would wait forever for a
			// fence this nonce can never carry.
			return
		}
		bs.mu.Lock()
		parked := pendingEnd{nonce: hexNonce, endRow: endRow, closing: closing, incomplete: noFence}
		s.holdEndLocked(sid, &parked)
		ends := append(bs.ends[sid], parked)
		if len(ends) > maxPendingEnds {
			ends = ends[len(ends)-maxPendingEnds:]
			s.log.Warn("block interval ends parked past the bound; the oldest is dropped", "session", sid)
		}
		bs.ends[sid] = ends
		bs.mu.Unlock()
		return
	}
	s.closeBlockRows(ctx, sid, attempt, endRow, closing, hexNonce, noFence)
}

// BlockBoundaryLost settles the block a boundary would have closed, when the
// boundary was accepted here and its delivery to the helper finally failed
// (nocx-2v80t.3.29): the helper refused it, the connection was lost, the
// request could not fit a frame, or the session ended first. The helper can
// never seal that interval, so no end marker will come for it, and the block
// is settled here instead: sealed at what reached the store, with no closing
// screen, stored as a gap, and said closed once.
//
// nonce is the completion's fence, or noFenceNonce for a lost environment
// entry, which ends whichever block is current — the same resolution
// BlockIntervalEnded gives an entry's own end. A fence not yet published is
// remembered, and its block is settled the moment the fence is. Nothing here
// runs for a session with no rows source: a detached session's blocks were
// settled by the detach.
//
// ctx is the caller's: the downlink's own, or the lifecycle frame whose
// completion could not be queued, whose transaction the settle joins.
func (s *WSServer) BlockBoundaryLost(ctx context.Context, sid session.ID, nonce [32]byte) {
	hexNonce := hex.EncodeToString(nonce[:])
	bs := s.blockStream
	s.rebindIfFrameFails(ctx, sid)
	bs.mu.Lock()
	if _, sourced := bs.sources[sid]; !sourced {
		bs.mu.Unlock()
		return
	}
	var block *openBlock
	var attempt string
	if nonce == noFenceNonce {
		block = bs.current[sid]
		if block == nil {
			bs.mu.Unlock()
			return
		}
		attempt = block.attempt
		bs.markEnteredLocked(sid, attempt)
	} else {
		var known bool
		attempt, known = bs.fences[sid][hexNonce]
		if !known {
			// The completion's fact has not published its fence yet:
			// publishFence settles the block when it does.
			bs.rememberLostLocked(sid, hexNonce)
			bs.mu.Unlock()
			return
		}
		bs.rememberLostLocked(sid, hexNonce)
		block = bs.open[sid][attempt]
	}
	var endRow uint64
	if block != nil {
		endRow = block.rows
	}
	bs.mu.Unlock()
	s.closeBlockRows(ctx, sid, attempt, endRow, nil, hexNonce, true)
}

// markEnteredLocked records that an environment entry sealed the attempt's
// block while the attempt runs on (see blockStream.entered).
func (bs *blockStream) markEnteredLocked(sid session.ID, attempt string) {
	if bs.entered == nil {
		bs.entered = make(map[session.ID]map[string]struct{})
	}
	if bs.entered[sid] == nil {
		bs.entered[sid] = make(map[string]struct{})
	}
	bs.entered[sid][attempt] = struct{}{}
}

// markClosedWhileOpeningLocked remembers that attempt was said block.closed
// before its own open ever installed a block for it (nocx-2v80t.3.48). Two
// places an about-to-open attempt can be found — the session's own opening
// attempt right now (bs.opening[sid]), or still queued behind it in
// bs.reopen[sid], not yet even attempted — and the mark applies to either: a
// close cannot tell which one a queued attempt will be by the time its own
// open finally runs, so both are checked, under the SAME lock as the rest of
// the close's own bookkeeping. A no-op for anything that is neither: the far
// more common case of an attempt whose open was never asked for at all (see
// abandonAttempt's own comment), which would otherwise leak a mark nothing
// ever consumes.
func (bs *blockStream) markClosedWhileOpeningLocked(sid session.ID, attempt string) {
	queued := false
	for _, a := range bs.reopen[sid] {
		if a == attempt {
			queued = true
			break
		}
	}
	if bs.opening[sid] != attempt && !queued {
		return
	}
	if bs.closedWhileOpening == nil {
		bs.closedWhileOpening = make(map[session.ID]map[string]struct{})
	}
	if bs.closedWhileOpening[sid] == nil {
		bs.closedWhileOpening[sid] = make(map[string]struct{})
	}
	bs.closedWhileOpening[sid][attempt] = struct{}{}
}

// consumeClosedWhileOpeningLocked answers whether attempt was marked by
// markClosedWhileOpeningLocked, and forgets it either way: consumed once, at
// the one install its own open produces.
func (bs *blockStream) consumeClosedWhileOpeningLocked(sid session.ID, attempt string) bool {
	m := bs.closedWhileOpening[sid]
	if m == nil {
		return false
	}
	_, marked := m[attempt]
	delete(m, attempt)
	if len(m) == 0 {
		delete(bs.closedWhileOpening, sid)
	}
	return marked
}

// recordOrphanSeal remembers a row a discarded open's own cleanup seal
// failed to close (nocx-2v80t.3.51), for retryOrphanSeals to try again.
func (bs *blockStream) recordOrphanSeal(sid session.ID, entry, artifactID string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.orphanSeals == nil {
		bs.orphanSeals = make(map[session.ID][]*orphanSeal)
	}
	bs.orphanSeals[sid] = append(bs.orphanSeals[sid], &orphanSeal{entry: entry, artifactID: artifactID})
}

// retryOrphanSeals attempts, right now, to seal every row this session still
// owes one (nocx-2v80t.3.51): recorded by recordOrphanSeal when a discarded
// open's own cleanup CloseBlockRows failed, at a moment the attempt was
// already forgotten everywhere else in this stream's bookkeeping and the
// renderer already told kept:false — without this the row stands open in
// the store forever, named nowhere. Called at every natural close
// (closeBlockRowsNow) and flush completion (drainPendingCloses), and forced
// at detach: each retry that still fails is kept for the NEXT one, up to
// maxCloseAttempts, matching closeBlockRowsNow's own retry bound and for the
// same reason — a retry that can never succeed must not run forever. Past
// the bound, or when final is true (detach: there is no next flush or close
// ever coming for this session again), it is logged as a permanent, stated
// loss and dropped rather than kept for a retry that will never come.
func (bs *blockStream) retryOrphanSeals(ctx context.Context, store blockOutputStore, sid session.ID, final bool) {
	if store == nil {
		return
	}
	bs.mu.Lock()
	pending := bs.orphanSeals[sid]
	delete(bs.orphanSeals, sid)
	bs.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	var remaining []*orphanSeal
	for i, o := range pending {
		if _, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
			EntryID: o.entry, ArtifactID: o.artifactID,
		}); frameFailed(err) {
			// Not a try: the frame stored nothing, and the seals stay owed.
			remaining = append(remaining, pending[i:]...)
			break
		} else if err != nil {
			o.tries++
			if !final && o.tries < maxCloseAttempts {
				remaining = append(remaining, o)
				continue
			}
			log.From(ctx).Warn("orphaned block row abandoned: its discarded open's cleanup seal kept failing and the store row stands open with nothing left to retry it",
				"session", sid, "entry", o.entry, "artifact", o.artifactID, "attempts", o.tries, "final", final, "error", err)
			continue
		}
	}
	if len(remaining) == 0 {
		return
	}
	bs.mu.Lock()
	if bs.orphanSeals == nil {
		bs.orphanSeals = make(map[session.ID][]*orphanSeal)
	}
	bs.orphanSeals[sid] = append(bs.orphanSeals[sid], remaining...)
	bs.mu.Unlock()
}

// enqueueReopenLocked queues attempt behind whatever is occupying the
// session right now, coalescing a duplicate: an attempt already sitting in
// the queue is not appended again (nocx-2v80t.3.51). The queue is a
// FIFO SET, not a bare slice — three callers (the submit, the shell's
// authenticated start, the ledger.bind retry) can each ask for the SAME
// attempt while it or another is busy, and an uncapped slice would grow one
// entry per ask, all for one eventual open, for as long as the session's
// commands keep the store busy. Bounded instead by the number of DISTINCT
// attempts ever queued at once, which is what the queue exists to hold.
func (bs *blockStream) enqueueReopenLocked(sid session.ID, attempt string) {
	for _, a := range bs.reopen[sid] {
		if a == attempt {
			return
		}
	}
	if bs.reopen == nil {
		bs.reopen = make(map[session.ID][]string)
	}
	bs.reopen[sid] = append(bs.reopen[sid], attempt)
}

// popReopenLocked answers the next attempt queued behind an open in flight,
// and forgets it — the front of the session's FIFO queue, so a later attempt
// (C) never runs ahead of an earlier one (B) still waiting (nocx-2v80t.3.48).
// A primitive: callers that go on to open the popped attempt use
// dequeueNextOpenLocked instead, which reserves it atomically in the same
// lock hold.
func (bs *blockStream) popReopenLocked(sid session.ID) (string, bool) {
	q := bs.reopen[sid]
	if len(q) == 0 {
		return "", false
	}
	next := q[0]
	if len(q) == 1 {
		delete(bs.reopen, sid)
	} else {
		bs.reopen[sid] = q[1:]
	}
	return next, true
}

// queuedOpenKind says what dequeueNextOpenLocked found for the next attempt
// the session's reopen queue owes an open, or that it owed nothing.
type queuedOpenKind int

const (
	queuedOpenNone queuedOpenKind = iota
	// queuedOpenExisting names an attempt whose block is already installed —
	// a duplicate request queued behind one that already succeeded. Safe to
	// hand to openAttemptFor's own entry point, which for an existing block
	// only reconciles current and pending rows; no store call, so no window
	// to protect.
	queuedOpenExisting
	// queuedOpenReserved names an attempt this call has ALREADY reserved —
	// opening[sid] (and waiting[sid], if there is no current) set under the
	// very lock it was dequeued in. The caller must go straight to
	// performOpen, never back through openAttemptFor's own guards, which
	// would see that reservation and wrongly queue the attempt again.
	queuedOpenReserved
)

// dequeueNextOpenLocked advances the session's reopen queue by one runnable
// entry (nocx-2v80t.3.48): FIFO, skipping any entry sealed by an environment
// entry in the meantime (nothing to open — openAttemptFor's own entered
// guard would decide the same), and skipping every entry equal to
// justSettled — the attempt the caller is discarding right now — so a
// duplicate request queued for it is not resurrected the moment this
// discard's own mark is consumed (justSettled is empty from the success and
// openFinished tails, where a self entry is either safe — see
// queuedOpenExisting — or the very retry that must not be dropped).
//
// For the entry it settles on that needs opening, this reserves it — under
// THIS SAME lock, before returning — so there is NO WINDOW between "no
// longer in the queue" and "now the session's opening attempt": a close or
// abandon landing exactly there used to see the attempt in neither
// bs.reopen nor bs.opening and forget it outright, the same defect class as
// the one this bead started from, one handoff earlier (nocx-2v80t.3.48). gen
// is bs.attachGen[sid] at that same reservation, for performOpen's own
// install check (nocx-2v80t.3.51) — meaningless for anything but
// queuedOpenReserved.
func (bs *blockStream) dequeueNextOpenLocked(sid session.ID, justSettled string) (attempt string, kind queuedOpenKind, gen uint64) {
	for {
		next, ok := bs.popReopenLocked(sid)
		if !ok {
			return "", queuedOpenNone, 0
		}
		if next == justSettled {
			continue
		}
		if _, sealed := bs.entered[sid][next]; sealed {
			continue
		}
		if bs.open[sid][next] != nil {
			return next, queuedOpenExisting, 0
		}
		if bs.opening == nil {
			bs.opening = make(map[session.ID]string)
		}
		if bs.waiting == nil {
			bs.waiting = make(map[session.ID]string)
		}
		if bs.current[sid] == nil {
			bs.waiting[sid] = next
		}
		bs.opening[sid] = next
		return next, queuedOpenReserved, bs.attachGen[sid]
	}
}

// continueQueuedOpen carries out dequeueNextOpenLocked's own result, after
// the lock that produced it is released: nothing, the ordinary
// entry point for an attempt already installed, or straight into performOpen
// for one just reserved atomically — never openAttemptFor's own guards
// (nocx-2v80t.3.48; see queuedOpenReserved).
func (bs *blockStream) continueQueuedOpen(ctx context.Context, s *WSServer, sid session.ID, attempt string, kind queuedOpenKind, gen uint64) {
	switch kind {
	case queuedOpenExisting:
		bs.openAttemptFor(ctx, s, sid, attempt)
	case queuedOpenReserved:
		bs.performOpen(ctx, s, sid, attempt, gen)
	case queuedOpenNone:
	}
}

// rememberLostLocked adds a lost fence, the oldest forgotten past the bound.
func (bs *blockStream) rememberLostLocked(sid session.ID, hexNonce string) {
	if bs.isLostLocked(sid, hexNonce) {
		return
	}
	if bs.lost == nil {
		bs.lost = make(map[session.ID][]string)
	}
	lost := append(bs.lost[sid], hexNonce)
	if len(lost) > maxPendingEnds {
		lost = lost[len(lost)-maxPendingEnds:]
	}
	bs.lost[sid] = lost
}

// forgetLostLocked answers whether the fence was lost, and forgets it: a
// lost fence drops at most one end marker.
func (bs *blockStream) forgetLostLocked(sid session.ID, hexNonce string) bool {
	lost := bs.lost[sid]
	for i, f := range lost {
		if f == hexNonce {
			bs.lost[sid] = append(lost[:i:i], lost[i+1:]...)
			if len(bs.lost[sid]) == 0 {
				delete(bs.lost, sid)
			}
			return true
		}
	}
	return false
}

// isLostLocked answers whether the fence was lost, keeping it.
func (bs *blockStream) isLostLocked(sid session.ID, hexNonce string) bool {
	for _, f := range bs.lost[sid] {
		if f == hexNonce {
			return true
		}
	}
	return false
}

// blockClearedParams is block.cleared's payload — see contracts/block.cleared.schema.json.
type blockClearedParams struct {
	// SessionID names the session whose emulator was sighted erased. The
	// notification reaches every pane's dispatcher (one socket, one
	// client), and the live tier's surface must wipe only ITS OWN
	// session's past — a markerless pane beside an integrated one loses
	// nothing when the other clears (nocx-zg3k3.10.4).
	SessionID string `json:"sessionId"`
	// KeepEntryID is the block whose interval the erase happened inside —
	// the command still running, which must never be hidden by its own
	// report of the clear. Null when no interval was open at the sighting:
	// every block the client currently shows is removed.
	KeepEntryID *string `json:"keepEntryId"`
}

// BlockClearBoundary delivers one sighted erase-saved-lines
// (nocx-2v80t.3.17): the store records the cursor an ordinary read applies —
// the record itself is never touched (nocx-zg3k3.10.3's decision) — and the
// attached client is told to remove every block it currently shows except
// the one whose interval the erase happened inside, if one is open.
//
// The client is told ONLY what the store now says (nocx-2v80t.3.26): a live
// removal the durable half did not record would be undone by the next
// reconnect, which reads the same cursor through ledger.query and would show
// every block the removal hid — the two halves disagreeing about what is
// visible, the one thing block.cleared's contract says they never do. So a
// failed write is announced to nobody: the client goes on showing what a
// reload would show, and the failure is logged where the product's own log
// carries it. With no store wired at all there is no read-time half to
// disagree with, and the live removal stands alone.
func (s *WSServer) BlockClearBoundary(sid session.ID) {
	// Owner: this stream, inside the helper's clear-boundary callback.
	// Closing event: the one store write below, nothing held past it — the
	// same shape BlockRowsArrived's own AppendBlockRows call has.
	ctx := log.WithLogger(context.Background(), s.log)
	if store := s.blockStore(); store != nil {
		if _, err := store.RecordClearBoundary(ctx,
			content.RecordClearBoundary{SessionID: string(sid)}); err != nil {
			log.From(ctx).Warn("clear boundary not recorded; the client is not told it happened",
				"session", sid, "error", err)
			return
		}
	}
	bs := s.blockStream
	bs.mu.Lock()
	var keepEntryID *string
	if block := bs.current[sid]; block != nil && block.entry != "" {
		id := block.entry
		keepEntryID = &id
	}
	bs.mu.Unlock()
	s.notifyBlockSubscriber(ctx, sid, "block.cleared", blockClearedParams{SessionID: string(sid), KeepEntryID: keepEntryID})
}

// closeBlockRows appends the closing rows and seals the block an interval
// leaves behind, then says so. A deferred append owns the interval until it
// finishes; the close is parked rather than racing the append.
func (s *WSServer) closeBlockRows(ctx context.Context, sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string, incomplete bool) {
	bs := s.blockStream
	bs.mu.Lock()
	end := pendingEnd{
		attempt: attempt, nonce: hexNonce, endRow: endRow,
		closing: append([]emulator.Row(nil), closing...), incomplete: incomplete,
	}
	current := bs.current[sid]
	if len(bs.pending[sid]) > 0 && !bs.flushing[sid] && !bs.closing[sid] &&
		current != nil && current.attempt == attempt {
		toFlush := bs.takeForFlushLocked(sid, endRow)
		if len(toFlush) > 0 {
			s.holdEndLocked(sid, &end)
			bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
			bs.flushing[sid] = true
			confirm := bs.confirmers[sid]
			bs.mu.Unlock()
			bs.flushPendingRows(ctx, s, sid, current, toFlush, confirm)
			return
		}
		bs.closing[sid] = true
		bs.mu.Unlock()
		s.closeBlockRowsNow(ctx, sid, attempt, endRow, closing, hexNonce, incomplete, false)
		return
	}
	if bs.flushing[sid] || bs.closing[sid] || len(bs.pending[sid]) > 0 {
		s.holdEndLocked(sid, &end)
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
		bs.mu.Unlock()
		return
	}
	if bs.queued[sid] == attempt && current != nil && current.attempt != attempt {
		s.holdEndLocked(sid, &end)
		bs.queuedEnds[sid] = append(bs.queuedEnds[sid], end)
		bs.mu.Unlock()
		return
	}
	bs.closing[sid] = true
	bs.mu.Unlock()
	s.closeBlockRowsNow(ctx, sid, attempt, endRow, closing, hexNonce, incomplete, false)
}

func (s *WSServer) closeBlockRowsNow(ctx context.Context, sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string, incomplete bool, fromWaiting bool) bool {
	bs := s.blockStream
	bs.mu.Lock()
	block := bs.open[sid][attempt]
	current := bs.current[sid]
	queued := bs.queued[sid]
	wasCurrent := current != nil && current.attempt == attempt
	var cursor uint64
	if block != nil {
		cursor = block.rows
	}
	bs.mu.Unlock()
	// store and ctx are named before the closures below, so the retry path
	// and the terminal path can both reach them. The order of the CHECKS that
	// follow is unchanged: a refused block closes without a store.
	//
	// ctx is the caller's: the rows plane's own, or the lifecycle frame
	// whose fact ended this interval — whose transaction the seal joins
	// (ADR-0077).
	store := s.blockStore()
	// Every ordinary close is also a chance to retry a row a PAST discarded
	// open never managed to seal (nocx-2v80t.3.51) — unrelated to this
	// interval's own attempt, so it runs regardless of what this close
	// itself decides below.
	bs.retryOrphanSeals(ctx, store, sid, false)

	promote := func() {
		if wasCurrent && queued != "" && queued != attempt {
			bs.openAttemptFor(ctx, s, sid, queued)
			bs.drainQueuedEnd(ctx, s, sid, queued)
		}
	}
	finish := func() {
		bs.mu.Lock()
		// block==nil means no open ever installed one for this attempt (the
		// ordinary case) — or one is still installing it right now
		// (nocx-2v80t.3.48): mark that so its install discards the result
		// instead of resurrecting what this close just ended.
		if block == nil {
			bs.markClosedWhileOpeningLocked(sid, attempt)
		}
		if wasCurrent {
			delete(bs.current, sid)
		}
		delete(bs.open[sid], attempt)
		delete(bs.fences[sid], hexNonce)
		// A block settled by any other end than its own fence's — the zero
		// nonce, a lost boundary — leaves that fence behind with no end ever
		// coming to take it (nocx-2v80t.3.32); the block's end is its fence's
		// end too.
		for f, a := range bs.fences[sid] {
			if a == attempt {
				delete(bs.fences[sid], f)
			}
		}
		if queued == attempt {
			delete(bs.queued, sid)
		}
		delete(bs.closing, sid)
		// The rows held past this interval's boundary belong to the interval
		// that follows: they join the queue the next block's flush reads, in
		// index order, and the hold's mark goes with them.
		if held := bs.beyond[sid]; len(held) > 0 {
			delete(bs.beyond, sid)
			delete(bs.beyondThrough, sid)
			bs.pending[sid] = mergePendingRows(bs.pending[sid], held)
		}
		ends := bs.pendingCloses[sid]
		kept := ends[:0]
		for _, end := range ends {
			if end.nonce != hexNonce {
				kept = append(kept, end)
			}
		}
		if len(kept) == 0 {
			delete(bs.pendingCloses, sid)
		} else {
			bs.pendingCloses[sid] = kept
		}
		bs.mu.Unlock()
	}
	// claim takes the right to say this block closed. The session's detach
	// can settle a block while this close is still writing it
	// (blockStream.detach), and then the detach has said it: this close
	// finishes its bookkeeping and says nothing a second time
	// (nocx-2v80t.3.29).
	claim := func() bool {
		bs.mu.Lock()
		defer bs.mu.Unlock()
		if block.settled {
			return false
		}
		block.settled = true
		return true
	}
	// beginSeal takes the seal for this close (nocx-2v80t.3.37), or answers
	// false when the session's detach already claimed the block and sealed
	// it: then this close seals nothing and says nothing — the detach said
	// what its own seal did.
	beginSeal := func() bool {
		bs.mu.Lock()
		defer bs.mu.Unlock()
		if block.settled {
			return false
		}
		block.sealing = true
		return true
	}
	// endSeal releases the seal and answers whether the session is still
	// attached: after a detach nothing of it may be recreated, so a failed
	// seal is not retried and is said closed, not kept, at once.
	endSeal := func() (attached bool) {
		bs.mu.Lock()
		defer bs.mu.Unlock()
		block.sealing = false
		_, attached = bs.sources[sid]
		return attached
	}
	// detachedSettle finishes a close whose session was detached under it,
	// saying what its own seal did when it was the one that sealed.
	detachedSettle := func(sealed, said bool) {
		finish()
		if said && claim() {
			s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: sealed})
		}
	}
	// fail records one failed close attempt and answers whether the end is
	// still worth retrying.
	//
	// A retry exists for a real shape: an append that was still in flight
	// when this close read the artifact's cursor, or a store that refused
	// once. It is BOUNDED (maxCloseAttempts), because the alternative is
	// exactly the defect: a close that can never succeed, retried on every
	// later delivery for the life of the session, leaves the block open and
	// the client never told the command ended (nocx-2v80t.3.9). Past the
	// bound the caller takes the terminal decision (abandon).
	fail := func() bool {
		// Keep current/open/fence/queued intact while a retry is still
		// possible. A close error may follow a committed closing-row append,
		// so promotion is unsafe mid-retry; retain the exact end for a later
		// close/row event to retry. store continuity rejects any duplicate
		// closing rows without changing ownership, and openBlock.closingIn is
		// what keeps a retry from appending them twice at the cursor.
		bs.mu.Lock()
		if _, attached := bs.sources[sid]; !attached {
			// Detached: nothing of the session is recreated, and nothing will
			// ever retry this end (nocx-2v80t.3.37).
			bs.mu.Unlock()
			return false
		}
		if bs.closeTries == nil {
			bs.closeTries = make(map[session.ID]map[string]uint8)
		}
		tries := bs.closeTries[sid]
		if tries == nil {
			tries = make(map[string]uint8)
			bs.closeTries[sid] = tries
		}
		tries[hexNonce]++
		spent := tries[hexNonce]
		if spent < maxCloseAttempts {
			retry := pendingEnd{
				attempt: attempt, nonce: hexNonce, endRow: endRow,
				closing: append([]emulator.Row(nil), closing...), incomplete: incomplete,
			}
			s.holdEndLocked(sid, &retry)
			bs.pendingCloses[sid] = append(bs.pendingCloses[sid], retry)
		} else {
			delete(tries, hexNonce)
		}
		delete(bs.closing, sid)
		bs.mu.Unlock()
		return spent < maxCloseAttempts
	}
	// abandon is the TERMINAL decision the bound names: the interval is over,
	// so the block is settled here whatever the store did — sealed as far as
	// the store allows, its closing screen dropped and counted as dropped,
	// the client told it closed, and the queue promoted. Nothing retries it
	// afterwards: an end that kept failing would otherwise hang the command
	// in a running state with no event that could ever end it, which is the
	// user-visible defect this bound exists for. The block's rows are what
	// reached the store, and a later read of history says exactly that.
	abandon := func() {
		sealed := false
		s.log.Warn("block close abandoned at the attempt bound: the block is settled without its closing screen",
			"session", sid, "entry", block.entry, "attempts", maxCloseAttempts,
			"droppedClosingRows", len(closing))
		if !beginSeal() {
			detachedSettle(false, false)
			return
		}
		if store != nil {
			_, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
				EntryID: block.entry, ArtifactID: block.artifactID, Incomplete: incomplete,
			})
			attached := endSeal()
			if frameFailed(err) && attached {
				return
			}
			if !attached {
				detachedSettle(err == nil, true)
				return
			}
			if err != nil {
				// Refused even here: the artifact stands as it is, and the
				// block is settled anyway — a retry that can never succeed is
				// what this path exists to end — but it is not SEALED, so it
				// is not said kept (nocx-2v80t.3.32): block.closed with
				// kept:true is the claim that the block is sealed in history.
				s.log.Warn("block rows seal refused at the attempt bound; the block is said closed but not kept",
					"session", sid, "entry", block.entry, "error", err)
			} else {
				sealed = true
			}
		} else if !endSeal() {
			detachedSettle(false, true)
			return
		}
		bs.mu.Lock()
		if endRow > bs.closedThrough[sid] {
			bs.closedThrough[sid] = endRow
		}
		bs.mu.Unlock()
		finish()
		if claim() {
			s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: sealed})
		}
		promote()
	}
	// EVERY END THE COORDINATOR RESOLVES IS SAID (nocx-2v80t.3.27), kept or
	// not. The renderer finishes a block on block.closed and on nothing else
	// — the render fence it used to wait on was a second owner of this
	// rendezvous (ADR-0066) — and it cannot know that no notification is
	// coming, so a block the store refused, or never opened, would otherwise
	// run forever on screen.
	if block == nil {
		finish()
		if attempt != "" {
			s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: attempt, Kept: false})
		}
		return true
	}
	if !block.kept {
		finish()
		if claim() {
			s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: false})
		}
		promote()
		return true
	}
	if store == nil {
		if !fail() {
			abandon()
		}
		return false
	}
	// ROWS OF THE INTERVAL ARE STILL OWED ON THE ORDERED CHANNEL, but ONLY
	// for a WAITING close (nocx-zg3k3.5.11 Round 9): the fence resolved this
	// end while the walk's remaining batches were still in the FIFO, and
	// sealing now would drop [cursor, endRow) against a settled block
	// (measured: sealed at 196/200 of 277, every absent row sent by the
	// helper and dropped unconfirmed). A WAITING close re-parks here —
	// without spending the attempt bound — until the deliveries advance the
	// cursor to the boundary; a detach (sources gone) or the FIFO reaching
	// the boundary ends the wait. A marker-driven close (fromWaiting false)
	// keeps the designed terminal: the read loop reached the end marker, so
	// every row ahead of it was already delivered or stated, and a short
	// cursor is the honest nocx-2v80t.3.9 seal. The cursor is RE-READ here:
	// the snapshot at the head of this function predates the store work and
	// may be behind an append that committed while it ran.
	bs.mu.Lock()
	live := block.rows
	_, attached := bs.sources[sid]
	bs.mu.Unlock()
	if fromWaiting && live < endRow && attached {
		end := pendingEnd{
			attempt: attempt, nonce: hexNonce, endRow: endRow,
			closing: append([]emulator.Row(nil), closing...), incomplete: incomplete,
			waiting: true,
		}
		bs.mu.Lock()
		s.holdEndLocked(sid, &end)
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
		delete(bs.closing, sid)
		bs.mu.Unlock()
		return false
	}
	// The closing screen's placement, decided from the artifact's own cursor
	// and from whether a previous attempt already committed it.
	bs.mu.Lock()
	closingIn := block.closingIn
	bs.mu.Unlock()
	if len(closing) > 0 && !closingIn {
		// The closing screen goes where the artifact actually is.
		//
		// The index relation the block states is that the artifact holds the
		// interval's rows at [begin, endRow) and its closing screen follows at
		// [endRow, endRow+len(closing)). That relation holds only while every
		// row the interval streamed reached the store, and the producer is not
		// obliged to make it hold — the e2e's end-marker sequence is not
		// monotone — so the close is placed by what the artifact really holds:
		//
		//   cursor == endRow — the relation holds, and this is the ordinary
		//     shape: the closing screen follows the interval's rows.
		//   cursor > endRow — an end marker behind rows already appended.
		//     Appending at endRow would land the close BEHIND the store's own
		//     cursor, which the store refuses by contract
		//     (ErrBlockRowsDiscontinuous) — the shape that used to leave the
		//     block open forever, retried on every later delivery
		//     (nocx-2v80t.3.9). The closing screen follows what is there.
		//   cursor < endRow — rows the interval streamed never reached this
		//     artifact. The closing screen still follows what it holds, and
		//     nothing is claimed for the ones that are absent: the store's
		//     summary measures its drop as (span − lost − stored) over a span
		//     that begins at the block's own first stored row, so a lost count
		//     for rows BEFORE that span is not expressible — subtracting it
		//     underflows the count and a surface renders
		//     `18446744073709552000 rows missing` for a block that lost
		//     nothing of its own (measured on the e2e, nocx-2v80t.3.9). Where
		//     those rows went is a delivery fact — another interval's
		//     artifact, a bridge drop, a floor skip — and the log line below
		//     names the shortfall instead of billing this block for it.
		if cursor != endRow {
			s.log.Warn("block close: the artifact is not at the interval's end row; the closing screen follows what it holds",
				"session", sid, "entry", block.entry, "endRow", endRow, "cursor", cursor)
		}
		if err := store.AppendBlockRows(ctx, content.AppendBlockRows{
			EntryID: block.entry, ArtifactID: block.artifactID,
			FromRow: cursor, Rows: closing,
		}); frameFailed(err) {
			bs.mu.Lock()
			delete(bs.closing, sid)
			bs.mu.Unlock()
			return false
		} else if err != nil {
			s.log.Warn("block closing rows failed", "session", sid, "entry", block.entry,
				"fromRow", cursor, "endRow", endRow, "error", err)
			if !fail() {
				abandon()
			}
			return false
		}
		bs.mu.Lock()
		block.closingIn = true
		block.rows = cursor + uint64(len(closing)) //nolint:gosec // a row count, not a byte count
		bs.mu.Unlock()
		s.notifyBlockSubscriber(ctx, sid, "block.grew", blockGrewParams{
			EntryID: block.entry, From: cursor, Count: uint64(len(closing)), //nolint:gosec // a row count, not a byte count
		})
	}
	if !beginSeal() {
		detachedSettle(false, false)
		return true
	}
	_, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: block.entry, ArtifactID: block.artifactID, Incomplete: incomplete,
	})
	if !endSeal() {
		// The session was detached while this seal was in the store: the
		// detach left the block to it, and this is the one seal it gets.
		detachedSettle(err == nil, true)
		return true
	}
	if frameFailed(err) {
		bs.mu.Lock()
		delete(bs.closing, sid)
		bs.mu.Unlock()
		return false
	}
	if err != nil {
		s.log.Warn("block rows close failed", "session", sid, "entry", block.entry, "error", err)
		if !fail() {
			abandon()
		}
		return false
	}
	s.log.Debug("interval end sealed the block", "session", sid, "entry", block.entry,
		"cursor", block.rows, "endRow", endRow)
	// The block owns its own departed rows: [begin, endRow). Its closing
	// screen is appended after everything the artifact actually holds —
	// at [endRow, endRow+len(closing)) when the interval's rows all reached
	// the store, and at the artifact's own cursor otherwise; the placement
	// rule and what a shortfall means are at the append above — and the
	// RUNTIME owns those rows from here: it holds the screen it emitted and
	// never streams it again, so no delivery for the next interval carries
	// them (rowstream.go). What remains this coordinator's to refuse is a
	// delivery below the closed interval's own boundary: the past arriving
	// again.
	bs.mu.Lock()
	delete(bs.closeTries[sid], hexNonce)
	if endRow > bs.closedThrough[sid] {
		bs.closedThrough[sid] = endRow
	}
	bs.mu.Unlock()
	finish()
	if claim() {
		s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: true})
	}
	promote()
	return true
}

// drainQueuedEnd replays the end that was held until the queued block became
// current. Promotion has already removed the queued marker, so closeBlockRows
// takes the ordinary current-block path.
func (bs *blockStream) drainQueuedEnd(ctx context.Context, s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
	var end pendingEnd
	found := false
	ends := bs.queuedEnds[sid]
	for i, candidate := range ends {
		if candidate.attempt != attempt {
			continue
		}
		end = candidate
		ends = append(ends[:i], ends[i+1:]...)
		found = true
		break
	}
	if len(ends) == 0 {
		delete(bs.queuedEnds, sid)
	} else {
		bs.queuedEnds[sid] = ends
	}
	bs.mu.Unlock()
	if found {
		s.closeBlockRows(ctx, sid, end.attempt, end.endRow, end.closing, end.nonce, end.incomplete)
	}
}

// attemptFact is the production hook: PublishLifecycle hands every
// authenticated attempt fact here. An OPEN fact opens the block (and
// answers the keep decision once per command); a COMPLETED fact publishes
// the fence that resolves any parked interval end; an UNKNOWN fact (the
// attempt's own domain closed, or lost its transport, before a fence ever
// named it) seals the block directly, since no fence is ever coming; every
// fact for a session with no rows source is a no-op.
func (bs *blockStream) attemptFact(ctx context.Context, s *WSServer, f lifecyclepub.Fact) {
	if f.Attempt == nil {
		return
	}
	s.lifecycleMu.Lock()
	sid, ok := s.lifecycleLanes[lifecycle.LaneID(f.Lane)]
	s.lifecycleMu.Unlock()
	if !ok {
		return
	}
	bs.mu.Lock()
	if _, sourced := bs.sources[sid]; !sourced {
		bs.mu.Unlock()
		return
	}
	bs.mu.Unlock()
	s.rebindIfFrameFails(ctx, sid)

	switch f.Attempt.State {
	case lifecyclepub.AttemptOpen:
		bs.openAttemptFor(ctx, s, sid, f.Attempt.ID)
	case lifecyclepub.AttemptCompleted:
		bs.forgetEntered(sid, f.Attempt.ID)
		if f.Attempt.Fence == "" {
			return
		}
		// A completion the kernel reconstructed for a command that ran for
		// the coordinator BEFORE this one (the completion-only replay,
		// ADR-0076) names a synthetic attempt — the shell's frame carries
		// no id. The command's real block is the one the re-adopting
		// stream re-bound from the store: this session's current open
		// block. The fence resolves against THAT attempt, so the resent
		// interval end seals it; the close parks behind any deferred
		// append, so the rows still short of its cursor land first.
		bs.mu.Lock()
		if _, named := bs.open[sid][f.Attempt.ID]; !named {
			if cur := bs.current[sid]; cur != nil && !cur.settled {
				f.Attempt.ID = cur.attempt
			}
		}
		bs.mu.Unlock()
		bs.publishFence(ctx, s, sid, f.Attempt.Fence, f.Attempt.ID)
	case lifecyclepub.AttemptUnknown:
		bs.forgetEntered(sid, f.Attempt.ID)
		bs.abandonAttempt(ctx, s, sid, f.Attempt.ID)
	}
}

// forgetEntered drops an attempt from the entered set once the lifecycle
// settles it: no later fact names it open again.
func (bs *blockStream) forgetEntered(sid session.ID, attempt string) {
	bs.mu.Lock()
	delete(bs.entered[sid], attempt)
	if len(bs.entered[sid]) == 0 {
		delete(bs.entered, sid)
	}
	bs.mu.Unlock()
}

// abandonAttempt seals the block an attempt opened but can never complete
// (nocx-2v80t.3.24): AttemptUnknown means the domain that owned it closed,
// or its transport was lost, before an authenticated fence ever named it —
// exactly what `exit` does to its own remote shell (nocx-mlyu) — so
// BlockIntervalEnded's ordinary fence-matching has nothing left to wait
// for. lifecyclepub's transitionsBelow is the ONLY caller that ever reports
// this transition (PublishAttemptClosed, routed here through
// publishClosedAttemptHistory's own synthetic fact): by the time the lane's
// own fact next derives, the domain is gone and the lane has moved past this
// attempt, so neither PublishLifecycle nor PublishLifecycleProjection ever
// name it again. Without this, the block this attempt opened (when its
// start frame DID reach the kernel — case 1 of the race nocx-mlyu measured,
// and on nocxify-journey.spec.ts against a real sshd the remote `exit`
// reached it in every run measured for nocx-2v80t.3.24) stays "current" forever: every later command's own
// end queues up behind a current that can never close.
//
// A start that never reached the kernel at all opened no block here in the
// first place (bs.open[sid][attempt] is nil), which is the ordinary,
// far-more-common case nocx-mlyu measured — not a bug, and nothing to seal.
func (bs *blockStream) abandonAttempt(ctx context.Context, s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
	block := bs.open[sid][attempt]
	var rows uint64
	if block != nil {
		rows = block.rows
	} else {
		// Its own open may be the one in flight right now (nocx-2v80t.3.48):
		// mark it so that install discards the result instead of installing a
		// block for an attempt already said closed here.
		bs.markClosedWhileOpeningLocked(sid, attempt)
	}
	bs.mu.Unlock()
	if block == nil {
		// Nothing to seal, and still an end: the renderer closes a block on
		// block.closed alone, and it holds one for this attempt whenever its
		// running fact reached the pane (nocx-2v80t.3.30).
		s.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: attempt, Kept: false})
		return
	}
	// No fence: nothing sighted this interval's end, and nothing ever will.
	// Sealed with whatever rows already reached the store and no closing
	// screen — the same shape an abandoned block-close retry settles for
	// (closeBlockRowsNow's own "abandon"), reached directly here because
	// there is no fence for a retry to ever resolve.
	s.closeBlockRows(ctx, sid, attempt, rows, nil, "", false)
}

// openAttemptFor answers the keep decision for one authenticated start the
// caller has already resolved to its session — the submit path's own shape.
func (bs *blockStream) openAttemptFor(ctx context.Context, s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
	if _, sealed := bs.entered[sid][attempt]; sealed {
		// Its block was sealed at an environment entry; the attempt running
		// on under the child is not a new block (see blockStream.entered).
		bs.mu.Unlock()
		return
	}
	waitingOn := bs.waiting[sid]
	busyOnAnother := waitingOn != "" && waitingOn != attempt
	if busyOnAnother || bs.opening[sid] != "" {
		// Not dropped: either this attempt's own open is in flight and may be
		// reading a store that cannot see the row yet (this request may be
		// the bind that lets it), or a DIFFERENT attempt's reservation is
		// still open — its own open still running, or failed and waiting on
		// its own queued retry (waiting stays reserved past a failed open;
		// see the OpenBlockOutput error path below) — and this one cannot
		// become the reservation now either way. It is made again, in the
		// order asked, once whatever it is queued behind finishes — a single
		// slot used to remember only the last of several requests, forgetting
		// the others in between (nocx-2v80t.3.48: with A's open in flight, B
		// then C asking used to leave B forgotten; a different attempt B
		// asked for while A merely held the reservation used to be dropped
		// with nothing recorded at all).
		bs.enqueueReopenLocked(sid, attempt)
		bs.mu.Unlock()
		return
	}
	if existing := bs.open[sid][attempt]; existing != nil {
		if bs.current[sid] == nil {
			bs.current[sid] = existing
			delete(bs.queued, sid)
		}
		if bs.flushing[sid] {
			bs.mu.Unlock()
			return
		}
		pending := bs.takeForFlushLocked(sid, takeAllPendingRows)
		if len(pending) > 0 {
			bs.flushing[sid] = true
		}
		confirm := bs.confirmers[sid]
		// A duplicate request for THIS attempt, already installed, is not
		// the end of the session's queue: whatever was asked for behind it
		// still owes an open, and nothing else advances the queue for this
		// branch (nocx-2v80t.3.51) — the previous fix routed a dequeued
		// duplicate here and returned, stranding everything queued behind it.
		next, kind, gen := bs.dequeueNextOpenLocked(sid, "")
		bs.mu.Unlock()
		if len(pending) > 0 {
			bs.flushPendingRows(ctx, s, sid, existing, pending, confirm)
		}
		bs.continueQueuedOpen(ctx, s, sid, next, kind, gen)
		return
	}
	hasCurrent := bs.current[sid] != nil
	if bs.waiting == nil {
		bs.waiting = make(map[session.ID]string)
	}
	if bs.opening == nil {
		bs.opening = make(map[session.ID]string)
	}
	// Reserve before the store call: rows can arrive while OPEN is blocked.
	// An existing current block owns those rows; only a no-current open uses
	// waiting to hold them for the block being created.
	if !hasCurrent {
		bs.waiting[sid] = attempt
	}
	bs.opening[sid] = attempt
	// The attachment this open is FOR, captured under the same lock as the
	// reservation (nocx-2v80t.3.51): a detach clears sources[sid], but a
	// detach followed by a re-attach under the same session id repopulates
	// it, so presence alone cannot tell a stale attachment from the live
	// one at install time. gen can.
	gen := bs.attachGen[sid]
	bs.mu.Unlock()
	bs.performOpen(ctx, s, sid, attempt, gen)
}

// performOpen does the store call for an attempt already reserved —
// opening[sid] (and waiting[sid], if there was no current) already set,
// either by openAttemptFor's own reservation just above or atomically by
// dequeueNextOpenLocked for one dequeued behind it — and its install or
// discard tail. Split out of openAttemptFor so a dequeued attempt can go
// straight here without passing back through openAttemptFor's own guards,
// which would see the reservation dequeueNextOpenLocked just made and queue
// the attempt again instead of opening it (nocx-2v80t.3.48). gen is the
// attachment this open was reserved under (nocx-2v80t.3.51): install checks
// it against the session's CURRENT attachGen, not merely against
// sources[sid]'s presence, which a detach-then-re-attach would repopulate.
func (bs *blockStream) performOpen(ctx context.Context, s *WSServer, sid session.ID, attempt string, gen uint64) {
	// With no store the block is still TRACKED, unkept — the same shape a
	// refused open takes — so each of its ends says block.closed like any
	// other: the renderer closes a block on that alone, and an untracked
	// one would run on screen forever (nocx-2v80t.3.30).
	openArtifact := ""
	store := s.blockStore()
	if store == nil {
		// Inert without a rows source, as ever: nothing would end it.
		bs.mu.Lock()
		_, sourced := bs.sources[sid]
		bs.mu.Unlock()
		if !sourced {
			bs.openFinished(ctx, s, sid)
			return
		}
	}
	if store != nil {
		v7, mintErr := uuid.NewV7()
		if mintErr != nil {
			s.log.Warn("block rows artifact id mint failed", "session", sid, "entry", attempt, "error", mintErr)
			bs.openFinished(ctx, s, sid)
			return
		}
		// ctx is the caller's: the lifecycle frame whose authenticated
		// start this open answers, whose transaction it joins (ADR-0077).
		opened, err := store.OpenBlockOutput(ctx, content.OpenBlockOutput{
			EntryID: attempt, ArtifactID: v7.String(),
		})
		if frameFailed(err) {
			// The frame failed for good: no answer about this block, and
			// nothing to decide from — the frame's end re-reads the
			// session's block state from the store (rebindIfFrameFails).
			return
		}
		if err != nil {
			if !errors.Is(err, content.ErrNoSuchEntry) {
				s.log.Warn("block rows open failed", "session", sid, "entry", attempt, "error", err)
			}
			// Keep waiting reserved. The ledger.bind retry owns the next open —
			// and when it arrived while this one was in flight, it is made now.
			bs.openFinished(ctx, s, sid)
			return
		}
		openArtifact = opened
	}

	bs.mu.Lock()
	_, attached := bs.sources[sid]
	if !attached || bs.attachGen[sid] != gen {
		// The attachment this open was reserved under is gone: the session
		// detached — or detached AND re-attached under the same id in this
		// store call's window (nocx-2v80t.3.51), which the generation
		// comparison tells apart where sources[sid]'s repopulated presence
		// cannot. Everything now keyed by sid belongs to the attachment
		// that followed — its opening reservation, its waiting, its pending
		// rows, its queue — so this open touches NONE of it: no delete, no
		// dequeue, no queued open (nocx-2v80t.3.53; A's late return used to
		// unreserve B and start C beside it). It only settles the row its
		// own store call created, inline, because no attachment remains
		// whose flush, close or detach would ever revisit an orphan parked
		// here (nocx-2v80t.3.53).
		bs.mu.Unlock()
		bs.settleStaleOpenRow(ctx, s, sid, attempt, openArtifact, store)
		return
	}
	delete(bs.opening, sid)
	hadReservation := bs.waiting[sid] == attempt
	if hadReservation {
		delete(bs.waiting, sid)
	}
	closedEarly := bs.consumeClosedWhileOpeningLocked(sid, attempt)
	if closedEarly {
		// The attempt was already said block.closed — while it was this
		// session's opening attempt, or still queued behind one
		// (markClosedWhileOpeningLocked). Installing now would resurrect a
		// block the renderer was already told is gone. Discarded instead; a
		// row the store did create for it is sealed through the ordinary
		// seal path so it does not stand open in history forever, and rows
		// collected under this attempt's own reservation are dropped with
		// the rest of it — nothing else could have claimed them while this
		// was the only opening attempt.
		var pending []pendingRows
		if hadReservation {
			pending = bs.pending[sid]
			delete(bs.pending, sid)
		}
		confirm := bs.confirmers[sid]
		next, kind, nextGen := bs.dequeueNextOpenLocked(sid, attempt)
		bs.mu.Unlock()
		for _, d := range pending {
			confirmPendingRows(ctx, confirm, d)
		}
		// Owner: this stream, discarding a late open on behalf of the attempt
		// or the session that already ended it.
		// Closing event: this seal — the only store write a discarded open
		// may still make; nothing is held past it.
		if openArtifact != "" && store != nil {
			if _, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
				EntryID: attempt, ArtifactID: openArtifact,
			}); frameFailed(err) {
				return
			} else if err != nil {
				s.log.Warn("block rows seal refused for a late open discarded after its attempt closed or its session detached; retried at the next flush, close or detach",
					"session", sid, "entry", attempt, "error", err)
				bs.recordOrphanSeal(sid, attempt, openArtifact)
			}
		}
		bs.continueQueuedOpen(ctx, s, sid, next, kind, nextGen)
		return
	}
	if bs.open == nil {
		bs.open = make(map[session.ID]map[string]*openBlock)
	}
	if bs.current == nil {
		bs.current = make(map[session.ID]*openBlock)
	}
	if bs.open[sid] == nil {
		bs.open[sid] = make(map[string]*openBlock)
	}
	b := &openBlock{attempt: attempt, entry: attempt, artifactID: openArtifact, kept: openArtifact != ""}
	bs.open[sid][attempt] = b
	hadCurrent := bs.current[sid] != nil
	if !hadCurrent {
		bs.current[sid] = b
	} else {
		bs.queued[sid] = attempt
	}
	var pending []pendingRows
	if !hadCurrent {
		pending = bs.takeForFlushLocked(sid, takeAllPendingRows)
	}
	if len(pending) > 0 {
		bs.flushing[sid] = true
	}
	confirm := bs.confirmers[sid]
	next, kind, nextGen := bs.dequeueNextOpenLocked(sid, "")
	bs.mu.Unlock()
	if len(pending) > 0 {
		bs.flushPendingRows(ctx, s, sid, b, pending, confirm)
	}
	// A later open asked for while this one was in flight is made now rather
	// than lost, in the order asked (nocx-2v80t.3.48). One queued for this
	// SAME attempt (the bind's own retry, arriving after another caller's
	// open already succeeded) is safe to make too: queuedOpenExisting finds
	// the block already installed and only reconciles pending rows and
	// current, never reopening the store.
	bs.continueQueuedOpen(ctx, s, sid, next, kind, nextGen)
}

// settleStaleOpenRow seals the store row a stale open's own call created —
// the one store write an open may still make once its attachment is gone. A
// failure is retried inline up to maxCloseAttempts and then stated as a
// permanent loss: an orphan parked here would be revisited by nothing, since
// the session's own flushes, closes and detach have all already run
// (nocx-2v80t.3.53). The LIVE discard's failed cleanup keeps the ordinary
// orphan path (recordOrphanSeal), which those calls do revisit.
func (bs *blockStream) settleStaleOpenRow(ctx context.Context, s *WSServer, sid session.ID, attempt, openArtifact string, store blockOutputStore) {
	if openArtifact == "" || store == nil {
		return
	}
	// Owner: this stream, settling a late open on behalf of the session that
	// already ended its attachment.
	// Closing event: this seal — the only store write a stale open may still
	// make; nothing is held past it.
	var err error
	for range maxCloseAttempts {
		if _, err = store.CloseBlockRows(ctx, content.CloseBlockRows{
			EntryID: attempt, ArtifactID: openArtifact,
		}); err == nil || frameFailed(err) {
			return
		}
	}
	s.log.Warn("block rows seal refused for a late open discarded after its session detached; retried inline and lost — the store row stands open with nothing left to retry it",
		"session", sid, "entry", attempt, "artifact", openArtifact, "attempts", maxCloseAttempts, "error", err)
}

// openFinished ends an open that did NOT produce a block — this attempt's own
// open is not done, only this ATTEMPT to make it — and makes the open that
// was asked for while it was in flight, if one was: the next one in the
// session's queue (blockStream.reopen), FIFO (nocx-2v80t.3.48). Unlike the
// success and discard tails, a queued entry for this SAME attempt is never
// skipped here (justSettled: ""): this attempt has not opened, so that entry
// is its own retry (the ledger.bind's, most often) and dropping it would
// leave the attempt stuck exactly as nocx-2v80t.3.42 measured.
func (bs *blockStream) openFinished(ctx context.Context, s *WSServer, sid session.ID) {
	bs.mu.Lock()
	delete(bs.opening, sid)
	next, kind, gen := bs.dequeueNextOpenLocked(sid, "")
	bs.mu.Unlock()
	bs.continueQueuedOpen(ctx, s, sid, next, kind, gen)
}

func (bs *blockStream) flushPendingRows(ctx context.Context, s *WSServer, sid session.ID, block *openBlock, pending []pendingRows, confirm func(uint64)) {
	if !block.kept {
		// A refused keep stores nothing, so it confirms nothing (the mark
		// means stored, nocx-zg3k3.5.3): the queued rows were dropped with
		// the block, and the resend offers them again.
		bs.finishPendingRows(sid)
		bs.drainPendingCloses(ctx, s, sid)
		return
	}
	store := s.blockStore()
	if store == nil {
		bs.requeuePendingRows(sid, pending)
		return
	}
	// This batch left bs.pending in the caller before this call — that is
	// what let takeForFlushLocked and requeuePendingRows treat it as one
	// slice instead of two — but it has not landed at the store either, and
	// this call can block for as long as the store is slow. Without counting
	// it somewhere, the bound sees nothing held here and lets rows that keep
	// arriving accumulate a second buffer's worth behind this one
	// (nocx-2v80t.3.49). flushingBytes is that count: EVERY caller of this
	// function extracted this exact batch through takeForFlushLocked, which
	// set it in that same lock hold (nocx-2v80t.3.51) — never here, which
	// would run after the caller had already unlocked with the batch out of
	// bs.pending and counted nowhere in between.
	// Owner: this stream, on behalf of the helper's attached session.
	// Closing event: each successful append or the session detach.
	for i, delivery := range pending {
		if err := store.AppendBlockRows(ctx, content.AppendBlockRows{
			EntryID: block.entry, ArtifactID: block.artifactID,
			FromRow: delivery.from, LostRows: delivery.lost, LostCause: delivery.cause, Rows: delivery.rows,
		}); frameFailed(err) {
			return
		} else if err != nil {
			s.log.Warn("deferred block rows append failed", "session", sid, "entry", block.entry, "error", err)
			bs.requeuePendingRows(sid, pending[i:])
			return
		}
		// The artifact's cursor follows every committed delivery, in the
		// deferred path exactly as in the direct one: the close places its
		// closing screen at that cursor. flushingBytes drops by the same
		// delivery, the instant it stops being "in flight to the store" and
		// becomes rows the store already holds.
		bs.mu.Lock()
		block.rows = delivery.from + uint64(len(delivery.rows)) //nolint:gosec // a row count, not a byte count
		bs.flushingBytes[sid] -= heldRowsBytes(delivery.rows)
		bs.mu.Unlock()
		if len(delivery.rows) > 0 {
			s.notifyBlockSubscriber(ctx, sid, "block.grew", blockGrewParams{
				EntryID: block.entry, From: delivery.from, Count: uint64(len(delivery.rows)), //nolint:gosec // a row count, not a byte count
			})
		}
		confirmPendingRows(ctx, confirm, delivery)
	}
	next := bs.takePendingRows(sid, block.attempt)
	if len(next) == 0 {
		// takePendingRows already set flushingBytes to 0 for this (empty)
		// next batch: every delivery in the one just finished landed, and
		// nothing is left in flight.
		bs.mu.Lock()
		bs.flushing[sid] = false
		bs.mu.Unlock()
		bs.drainPendingCloses(ctx, s, sid)
		return
	}
	bs.flushPendingRows(ctx, s, sid, block, next, confirm)
}

func (bs *blockStream) drainPendingCloses(ctx context.Context, s *WSServer, sid session.ID) {
	for {
		bs.mu.Lock()
		closes := bs.pendingCloses[sid]
		delete(bs.pendingCloses, sid)
		if len(closes) == 0 {
			delete(bs.closing, sid)
			bs.mu.Unlock()
			// A flush that lands with nothing left to close is still a
			// chance to retry a row a past discarded open never managed to
			// seal (nocx-2v80t.3.51).
			bs.retryOrphanSeals(ctx, s.blockStore(), sid, false)
			return
		}
		bs.closing[sid] = true
		bs.mu.Unlock()
		for _, end := range closes {
			bs.mu.Lock()
			if end.waiting {
				if _, open := bs.open[sid][end.attempt]; !open {
					// A WAITING end whose block is gone was already sealed
					// by its end frame's own close (or a detach): the wait
					// is done, and a second close would re-notify a settled
					// block.
					bs.mu.Unlock()
					continue
				}
			}
			current := bs.current[sid]
			queued := bs.queued[sid]
			if queued == end.attempt && current != nil && current.attempt != end.attempt {
				s.holdEndLocked(sid, &end)
				bs.queuedEnds[sid] = append(bs.queuedEnds[sid], end)
				bs.mu.Unlock()
				continue
			}
			bs.mu.Unlock()
			if !s.closeBlockRowsNow(ctx, sid, end.attempt, end.endRow, end.closing, end.nonce, end.incomplete, end.waiting) {
				return
			}
		}
	}
}

// confirmPendingRows tells the helper a delivery is settled — it may forget
// those rows. Inside a lifecycle frame the telling waits for the frame's
// commit and is dropped if it does not commit (ADR-0077 decision 9): rows the
// frame appended are not stored until then, and a helper told otherwise
// never offers them again.
func confirmPendingRows(ctx context.Context, confirm func(uint64), delivery pendingRows) {
	if confirm == nil {
		return
	}
	lifecyclecommit.After(ctx, nil, func(committed bool) {
		if committed {
			confirm(delivery.from + uint64(len(delivery.rows))) //nolint:gosec // a row count, not a byte count
		}
	})
}

func (bs *blockStream) finishPendingRows(sid session.ID) {
	bs.mu.Lock()
	delete(bs.pending, sid)
	bs.flushing[sid] = false
	delete(bs.flushingBytes, sid)
	bs.mu.Unlock()
}

// requeuePendingRows puts a batch flushPendingRows could not finish delivering
// back where heldBytesLocked already counts it — bs.pending — and clears
// flushingBytes for it in the same call: the batch stops being "in flight to
// the store" here, so counting it in both places at once would count it
// twice (nocx-2v80t.3.49).
func (bs *blockStream) requeuePendingRows(sid session.ID, pending []pendingRows) {
	bs.mu.Lock()
	bs.pending[sid] = append(pending, bs.pending[sid]...)
	bs.flushing[sid] = false
	delete(bs.flushingBytes, sid)
	bs.mu.Unlock()
}

// publishFence records a completion's fence for the end marker that has not
// arrived yet, and resolves any end marker that has been waiting for it —
// in either order, one meeting.
func (bs *blockStream) publishFence(ctx context.Context, s *WSServer, sid session.ID, hexNonce, attempt string) {
	bs.mu.Lock()
	if bs.ends == nil {
		bs.ends = make(map[session.ID][]pendingEnd)
	}
	if bs.fences == nil {
		bs.fences = make(map[session.ID]map[string]string)
	}
	if bs.fences[sid] == nil {
		bs.fences[sid] = make(map[string]string)
	}
	bs.fences[sid][hexNonce] = attempt
	var parked []pendingEnd
	var resolved *pendingEnd
	for i, e := range bs.ends[sid] {
		if e.nonce == hexNonce {
			// An INCOMPLETE boundary is a designed terminal (nocx-2v80t.3.29
			// / .3.36): its rows are gone by definition, so it settles now —
			// only a normal end marker waits behind the FIFO's remaining
			// batches (nocx-zg3k3.5.11 Round 9).
			if e.incomplete {
				parked = append(parked, e)
			} else {
				resolved = &e
			}
			bs.ends[sid] = append(bs.ends[sid][:i], bs.ends[sid][i+1:]...)
			break
		}
	}
	// A completion arriving while the stream is not recorded, with no end of
	// its own waiting, is a command whose end the overflow took: its end
	// arrived after the overflow began, if at all. It is settled here, from
	// its own fence, as incomplete — at the row where recording stopped,
	// which every row still held for it precedes (nocx-2v80t.3.36).
	if from, unrecorded := bs.unrecorded[sid]; unrecorded && len(parked) == 0 && !bs.isLostLocked(sid, hexNonce) {
		bs.rememberLostLocked(sid, hexNonce)
		parked = append(parked, pendingEnd{nonce: hexNonce, endRow: from, incomplete: true})
	}
	// A boundary already reported lost before its fence was published here
	// (BlockBoundaryLost) is settled now, as a gap, at what reached the store.
	if len(parked) == 0 && bs.isLostLocked(sid, hexNonce) {
		var endRow uint64
		if block := bs.open[sid][attempt]; block != nil {
			endRow = block.rows
		}
		parked = append(parked, pendingEnd{nonce: hexNonce, endRow: endRow, incomplete: true})
	}
	bs.mu.Unlock()
	for _, e := range parked {
		s.closeBlockRows(ctx, sid, attempt, e.endRow, e.closing, e.nonce, e.incomplete)
	}
	if resolved != nil {
		// THE EITHER-ORDER END MARKER (nocx-zg3k3.5.11 Round 9): this end's
		// own frame was processed before its fence was published — the
		// walk's remaining batches are still in the FIFO behind it, and a
		// close run NOW would seal at the short cursor and drop them
		// (measured: sealed at 196/200 of 277, every absent row sent by
		// the helper and dropped unconfirmed). Park it as a WAITING
		// pendingClose instead: the deliveries re-drive it at the truer
		// cursor, and when the FIFO reaches the end's height the close
		// seals at full height. A WAITING park spends no attempt.
		end := pendingEnd{
			attempt: attempt, nonce: resolved.nonce, endRow: resolved.endRow,
			closing: append([]emulator.Row(nil), resolved.closing...), incomplete: resolved.incomplete,
			waiting: true,
		}
		bs.mu.Lock()
		s.holdEndLocked(sid, &end)
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
		bs.mu.Unlock()
		bs.drainPendingCloses(ctx, s, sid)
	}
}

// notifyBlockSubscriber sends a block notification to the session's one
// attached subscriber — the same fan-out files.changed and git.changed use.
// No subscriber, no send: a client that attaches later reads history.
//
// Inside a lifecycle frame the notification waits for the frame's commit, in
// the frame's order, and is dropped with a frame that is never stored
// (ADR-0077 decision 12): the renderer is not told a block closed that the
// store — and, after the frame's failure, this stream's memory — still holds
// open. Outside a frame it goes at once.
func (s *WSServer) notifyBlockSubscriber(ctx context.Context, sid session.ID, method string, params any) {
	lifecyclecommit.OnCommit(ctx, func() { s.sendBlockNotification(sid, method, params) })
}

func (s *WSServer) sendBlockNotification(sid session.ID, method string, params any) {
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	if err := wconn.TryNotify(method, mustMarshal(params)); err != nil {
		s.log.Debug("write block notification", "session", sid, "method", method, "error", err)
	}
}

// blockStore narrows the content database to the block rows seam. Nil when
// no store is wired — the composition root decides, as it does for every
// other content consumer.
func (s *WSServer) blockStore() blockOutputStore {
	if s.blockRowsStore != nil {
		return s.blockRowsStore
	}
	if s.contentDB == nil {
		return nil
	}
	return s.contentDB.Ledger()
}

// prependBlockHead stores one below-floor delivery — the block's own head,
// departed before its open and offered again by the resend (nocx-zg3k3.5.3
// Round 8) — through the store's contiguous-below-floor path. It answers
// whether the artifact now holds the head: a failure is the one answer that
// withholds the confirmation, so the mark never claims rows the store does
// not hold.
func (s *WSServer) prependBlockHead(sid session.ID, entry, artifact string, from uint64, rows []emulator.Row) bool {
	store := s.blockStore()
	if store == nil {
		return false
	}
	// Owner: this stream, inside the helper's row delivery — the
	// below-floor head's own store write. Closing event: the write — one
	// prepend, nothing held past it (the mark stays behind until the
	// prepend answers, so nothing outlives the context).
	ctx := log.WithLogger(context.Background(), s.log)
	if err := store.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entry, ArtifactID: artifact, FromRow: from, Rows: rows,
	}); err != nil {
		log.From(ctx).Warn("block rows head prepend failed",
			"session", sid, "entry", entry, "artifact", artifact, "from", from, "error", err)
		return false
	}
	log.From(ctx).Debug("block rows head prepend: stored",
		"session", sid, "entry", entry, "artifact", artifact, "from", from, "rows", len(rows))
	return true
}
