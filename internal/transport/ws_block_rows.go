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
	"sync"
	"unsafe"

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
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
	opening  map[session.ID]bool
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
}

// DefaultBlockRowsBufferBytes is the coordinator's buffer when nothing is
// configured (nocx-2v80t.3.36): sized like the helper's, since it holds what
// the helper's buffer sends.
const DefaultBlockRowsBufferBytes int64 = 20 << 20

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

// heldBytesLocked is what the session's buffer holds now: the rows waiting
// for the store and the rows held past a parked boundary, and the closing
// screens of ends waiting behind them. Derived from what is held, so it
// cannot drift from it.
func (bs *blockStream) heldBytesLocked(sid session.ID) int64 {
	var n int64
	for _, set := range [][]pendingRows{bs.pending[sid], bs.beyond[sid]} {
		for _, d := range set {
			n += heldRowsBytes(d.rows)
		}
	}
	for _, e := range bs.pendingCloses[sid] {
		n += heldRowsBytes(e.closing)
	}
	return n
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
func (s *WSServer) BlockOutputIncomplete(sid session.ID, fromRow uint64) {
	bs := s.blockStream
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if _, sourced := bs.sources[sid]; !sourced {
		return
	}
	bs.markUnrecordedLocked(sid, fromRow)
	s.log.Warn("helper row buffer overflowed: the block in flight ends incomplete",
		"session", sid, "fromRow", fromRow)
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
	from uint64
	lost uint64
	rows []emulator.Row
}

type pendingEnd struct {
	attempt string
	nonce   string // hex, the completion's own spelling
	endRow  uint64
	closing []emulator.Row
	// incomplete seals the block as a gap: its boundary was settled without
	// its fence, or its delivery was lost (nocx-2v80t.3.29).
	incomplete bool
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
	next := bs.pending[sid]
	delete(bs.pending, sid)
	var endRow uint64
	hasEnd := false
	for _, end := range bs.pendingCloses[sid] {
		if end.attempt == attempt {
			endRow, hasEnd = end.endRow, true
			break
		}
	}
	if hasEnd {
		var remaining []pendingRows
		next, remaining = splitPendingRowsAt(next, endRow)
		if len(remaining) > 0 {
			bs.pending[sid] = remaining
		}
	}
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
func (s *WSServer) AttachBlockRows(sid session.ID) {
	s.blockStream.attach(sid, nil, s.blockRowsBuffer())
}

// AttachBlockRowsWithConfirmation additionally gives deferred rows a way to
// advance the helper's watermark after the bind retry has persisted them.
func (s *WSServer) AttachBlockRowsWithConfirmation(sid session.ID, confirm func(uint64)) {
	s.blockStream.attach(sid, confirm, s.blockRowsBuffer())
}

// blockRowsBuffer is the buffer a session attached now gets.
func (s *WSServer) blockRowsBuffer() int64 {
	if n := s.blockRowsBufferBytes.Load(); n > 0 {
		return n
	}
	return DefaultBlockRowsBufferBytes
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
}

// DetachBlockRows ends a session's streaming: every still-open block is
// sealed with whatever arrived — the honest end when the interval's own end
// never will. The composition root calls it where the session's helper
// callbacks are torn down.
func (s *WSServer) DetachBlockRows(sid session.ID) {
	// The session's end is the event that settles every interval still open
	// on it (ADR-0074 decision 3), and nothing after it can send block.closed
	// — so it is said here, for each, or the renderer, which finishes a block
	// on that notification alone, shows it running forever (nocx-2v80t.3.27).
	//
	// Owner: this stream, on behalf of the detached session.
	// Closing event: the detach's own seals — nothing is held past them.
	ctx := log.WithLogger(context.Background(), s.log)
	for _, closed := range s.blockStream.detach(ctx, s.blockStore(), sid) {
		s.notifyBlockSubscriber(sid, "block.closed", closed)
	}
}

// detach forgets the session and seals what it held, and answers the
// block.closed each still-unended interval is owed: every open block, and
// every completion whose end marker never arrived.
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
	delete(bs.unrecorded, sid)
	delete(bs.budgets, sid)
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
// them. It answers whether the coordinator wants them confirmed written —
// the helper's "written up to here" mark. Rows that belong to no block (a
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
func (s *WSServer) BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row) (writtenUpTo uint64, confirm bool) {
	if len(rows) == 0 && lost == 0 {
		return 0, false
	}
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	if _, unrecorded := bs.unrecorded[sid]; sourced && unrecorded {
		// Not recorded until the next command (BlockOutputIncomplete): what
		// arrives now is confirmed, so nothing waits on it, and not kept.
		bs.mu.Unlock()
		return fromRow + uint64(len(rows)), true //nolint:gosec // a row count, not a byte count
	}
	block := bs.current[sid]
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
					before, after := splitPendingRowsAt([]pendingRows{{from: fromRow, lost: lost, rows: rows}}, bound)
					if len(after) == 1 && !s.holdLocked(sid, after[0].from, after[0].rows) {
						bs.mu.Unlock()
						return fromRow + uint64(len(rows)), true //nolint:gosec // a row count, not a byte count
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
		bs.drainPendingCloses(s, sid)
		bs.mu.Lock()
		if len(bs.pendingCloses[sid]) > 0 {
			if !s.holdLocked(sid, fromRow, rows) {
				bs.mu.Unlock()
				return fromRow + uint64(len(rows)), true //nolint:gosec // a row count, not a byte count
			}
			bs.pending[sid] = append(bs.pending[sid], pendingRows{
				from: fromRow, lost: lost, rows: append([]emulator.Row(nil), rows...),
			})
			bs.mu.Unlock()
			return 0, false
		}
		bs.mu.Unlock()
		return s.BlockRowsArrived(sid, fromRow, lost, rows)
	}
	if sourced && pending && !flushing && !closing && block != nil && bs.queued[sid] == "" {
		bs.pending[sid] = append(bs.pending[sid], pendingRows{
			from: fromRow, lost: lost, rows: append([]emulator.Row(nil), rows...),
		})
		toFlush := bs.pending[sid]
		delete(bs.pending, sid)
		bs.flushing[sid] = true
		confirm := bs.confirmers[sid]
		bs.mu.Unlock()
		bs.flushPendingRows(s, sid, block, toFlush, confirm)
		return 0, false
	}
	if sourced && (flushing || closing || (waiting != "" && block == nil)) {
		if !s.holdLocked(sid, fromRow, rows) {
			bs.mu.Unlock()
			return fromRow + uint64(len(rows)), true //nolint:gosec // a row count, not a byte count
		}
		if bs.pending == nil {
			bs.pending = make(map[session.ID][]pendingRows)
		}
		bs.pending[sid] = append(bs.pending[sid], pendingRows{
			from: fromRow, lost: lost, rows: append([]emulator.Row(nil), rows...),
		})
		bs.mu.Unlock()
		return 0, false
	}
	bs.mu.Unlock()
	if !sourced {
		return 0, false
	}
	writtenUpTo = fromRow + uint64(len(rows)) //nolint:gosec // a row count, not a byte count
	if block == nil || !block.kept {
		return writtenUpTo, true
	}
	store := s.blockStore()
	if store == nil {
		return 0, false
	}
	// Owner: this stream, inside the helper's row delivery. Closing event:
	// the append — one store write per delivery, nothing held past it.
	err := store.AppendBlockRows(context.Background(), content.AppendBlockRows{
		EntryID: block.entry, ArtifactID: block.artifactID,
		FromRow: fromRow, LostRows: lost, Rows: rows,
	})
	if err != nil {
		// A store failure is not a refusal: the rows are still wanted, and
		// refusing to confirm is what lets them be offered again.
		s.log.Warn("block rows append failed", "session", sid, "entry", block.entry, "error", err)
		return 0, false
	}
	// The append committed, so the artifact's cursor is now the exclusive end
	// of this delivery. The close reads it to place its closing screen.
	bs.mu.Lock()
	block.rows = writtenUpTo
	bs.mu.Unlock()
	if len(rows) > 0 {
		// A loss-only delivery stored no row, so nothing grew.
		s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
			EntryID: block.entry, From: fromRow, Count: uint64(len(rows)), //nolint:gosec // a row count, not a byte count
		})
	}
	return writtenUpTo, true
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
		ends := append(bs.ends[sid], pendingEnd{nonce: hexNonce, endRow: endRow, closing: closing, incomplete: noFence})
		if len(ends) > maxPendingEnds {
			ends = ends[len(ends)-maxPendingEnds:]
			s.log.Warn("block interval ends parked past the bound; the oldest is dropped", "session", sid)
		}
		bs.ends[sid] = ends
		bs.mu.Unlock()
		return
	}
	s.closeBlockRows(sid, attempt, endRow, closing, hexNonce, noFence)
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
func (s *WSServer) BlockBoundaryLost(sid session.ID, nonce [32]byte) {
	hexNonce := hex.EncodeToString(nonce[:])
	bs := s.blockStream
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
	s.closeBlockRows(sid, attempt, endRow, nil, hexNonce, true)
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
	s.notifyBlockSubscriber(sid, "block.cleared", blockClearedParams{KeepEntryID: keepEntryID})
}

// closeBlockRows appends the closing rows and seals the block an interval
// leaves behind, then says so. A deferred append owns the interval until it
// finishes; the close is parked rather than racing the append.
func (s *WSServer) closeBlockRows(sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string, incomplete bool) {
	bs := s.blockStream
	bs.mu.Lock()
	end := pendingEnd{
		attempt: attempt, nonce: hexNonce, endRow: endRow,
		closing: append([]emulator.Row(nil), closing...), incomplete: incomplete,
	}
	current := bs.current[sid]
	if len(bs.pending[sid]) > 0 && !bs.flushing[sid] && !bs.closing[sid] &&
		current != nil && current.attempt == attempt {
		toFlush, remaining := splitPendingRowsAt(bs.pending[sid], endRow)
		delete(bs.pending, sid)
		if len(remaining) > 0 {
			bs.pending[sid] = remaining
		}
		if len(toFlush) > 0 {
			bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
			bs.flushing[sid] = true
			confirm := bs.confirmers[sid]
			bs.mu.Unlock()
			bs.flushPendingRows(s, sid, current, toFlush, confirm)
			return
		}
		bs.closing[sid] = true
		bs.mu.Unlock()
		s.closeBlockRowsNow(sid, attempt, endRow, closing, hexNonce, incomplete)
		return
	}
	if bs.flushing[sid] || bs.closing[sid] || len(bs.pending[sid]) > 0 {
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], end)
		bs.mu.Unlock()
		return
	}
	if bs.queued[sid] == attempt && current != nil && current.attempt != attempt {
		bs.queuedEnds[sid] = append(bs.queuedEnds[sid], end)
		bs.mu.Unlock()
		return
	}
	bs.closing[sid] = true
	bs.mu.Unlock()
	s.closeBlockRowsNow(sid, attempt, endRow, closing, hexNonce, incomplete)
}

func (s *WSServer) closeBlockRowsNow(sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string, incomplete bool) bool {
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
	// Owner: this stream, inside the interval's authenticated end.
	// Closing event: the seal — the closing append and the close are the
	// last writes this interval can cause.
	store := s.blockStore()
	ctx := context.Background()

	promote := func() {
		if wasCurrent && queued != "" && queued != attempt {
			bs.openAttemptFor(s, sid, queued)
			bs.drainQueuedEnd(s, sid, queued)
		}
	}
	finish := func() {
		bs.mu.Lock()
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
			s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: sealed})
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
			bs.pendingCloses[sid] = append(bs.pendingCloses[sid], pendingEnd{
				attempt: attempt, nonce: hexNonce, endRow: endRow,
				closing: append([]emulator.Row(nil), closing...), incomplete: incomplete,
			})
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
			s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: sealed})
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
			s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: attempt, Kept: false})
		}
		return true
	}
	if !block.kept {
		finish()
		if claim() {
			s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: false})
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
		}); err != nil {
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
		s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
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
	if err != nil {
		s.log.Warn("block rows close failed", "session", sid, "entry", block.entry, "error", err)
		if !fail() {
			abandon()
		}
		return false
	}
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
		s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry, Kept: true})
	}
	promote()
	return true
}

// drainQueuedEnd replays the end that was held until the queued block became
// current. Promotion has already removed the queued marker, so closeBlockRows
// takes the ordinary current-block path.
func (bs *blockStream) drainQueuedEnd(s *WSServer, sid session.ID, attempt string) {
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
		s.closeBlockRows(sid, end.attempt, end.endRow, end.closing, end.nonce, end.incomplete)
	}
}

// attemptFact is the production hook: PublishLifecycle hands every
// authenticated attempt fact here. An OPEN fact opens the block (and
// answers the keep decision once per command); a COMPLETED fact publishes
// the fence that resolves any parked interval end; an UNKNOWN fact (the
// attempt's own domain closed, or lost its transport, before a fence ever
// named it) seals the block directly, since no fence is ever coming; every
// fact for a session with no rows source is a no-op.
func (bs *blockStream) attemptFact(s *WSServer, f lifecyclepub.Fact) {
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

	switch f.Attempt.State {
	case lifecyclepub.AttemptOpen:
		bs.openAttemptFor(s, sid, f.Attempt.ID)
	case lifecyclepub.AttemptCompleted:
		bs.forgetEntered(sid, f.Attempt.ID)
		if f.Attempt.Fence == "" {
			return
		}
		bs.publishFence(s, sid, f.Attempt.Fence, f.Attempt.ID)
	case lifecyclepub.AttemptUnknown:
		bs.forgetEntered(sid, f.Attempt.ID)
		bs.abandonAttempt(s, sid, f.Attempt.ID)
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
func (bs *blockStream) abandonAttempt(s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
	block := bs.open[sid][attempt]
	var rows uint64
	if block != nil {
		rows = block.rows
	}
	bs.mu.Unlock()
	if block == nil {
		// Nothing to seal, and still an end: the renderer closes a block on
		// block.closed alone, and it holds one for this attempt whenever its
		// running fact reached the pane (nocx-2v80t.3.30).
		s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: attempt, Kept: false})
		return
	}
	// No fence: nothing sighted this interval's end, and nothing ever will.
	// Sealed with whatever rows already reached the store and no closing
	// screen — the same shape an abandoned block-close retry settles for
	// (closeBlockRowsNow's own "abandon"), reached directly here because
	// there is no fence for a retry to ever resolve.
	s.closeBlockRows(sid, attempt, rows, nil, "", false)
}

// openAttemptFor answers the keep decision for one authenticated start the
// caller has already resolved to its session — the submit path's own shape.
func (bs *blockStream) openAttemptFor(s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
	if _, sealed := bs.entered[sid][attempt]; sealed {
		// Its block was sealed at an environment entry; the attempt running
		// on under the child is not a new block (see blockStream.entered).
		bs.mu.Unlock()
		return
	}
	if waiting := bs.waiting[sid]; waiting != "" && waiting != attempt {
		bs.mu.Unlock()
		return
	}
	if bs.opening[sid] {
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
		pending := bs.pending[sid]
		delete(bs.pending, sid)
		confirm := bs.confirmers[sid]
		if len(pending) > 0 {
			bs.flushing[sid] = true
		}
		bs.mu.Unlock()
		if len(pending) > 0 {
			bs.flushPendingRows(s, sid, existing, pending, confirm)
		}
		return
	}
	hasCurrent := bs.current[sid] != nil
	if bs.waiting == nil {
		bs.waiting = make(map[session.ID]string)
	}
	if bs.opening == nil {
		bs.opening = make(map[session.ID]bool)
	}
	// Reserve before the store call: rows can arrive while OPEN is blocked.
	// An existing current block owns those rows; only a no-current open uses
	// waiting to hold them for the block being created.
	if !hasCurrent {
		bs.waiting[sid] = attempt
	}
	bs.opening[sid] = true
	bs.mu.Unlock()

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
			bs.clearOpening(sid)
			return
		}
	}
	if store != nil {
		v7, mintErr := uuid.NewV7()
		if mintErr != nil {
			s.log.Warn("block rows artifact id mint failed", "session", sid, "entry", attempt, "error", mintErr)
			bs.clearOpening(sid)
			return
		}
		// Owner: this stream, at the command's authenticated start.
		// Closing event: the open — one store write that decides keep or
		// refuse; nothing is held past it.
		opened, err := store.OpenBlockOutput(context.Background(), content.OpenBlockOutput{
			EntryID: attempt, ArtifactID: v7.String(),
		})
		if err != nil {
			if !errors.Is(err, content.ErrNoSuchEntry) {
				s.log.Warn("block rows open failed", "session", sid, "entry", attempt, "error", err)
			}
			bs.clearOpening(sid)
			// Keep waiting reserved. The ledger.bind retry owns the next open.
			return
		}
		openArtifact = opened
	}

	bs.mu.Lock()
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
	delete(bs.opening, sid)
	if bs.waiting[sid] == attempt {
		delete(bs.waiting, sid)
	}
	var pending []pendingRows
	if !hadCurrent {
		pending = bs.pending[sid]
		delete(bs.pending, sid)
	}
	confirm := bs.confirmers[sid]
	if len(pending) > 0 {
		bs.flushing[sid] = true
	}
	bs.mu.Unlock()
	if len(pending) > 0 {
		bs.flushPendingRows(s, sid, b, pending, confirm)
	}
}

func (bs *blockStream) clearOpening(sid session.ID) {
	bs.mu.Lock()
	delete(bs.opening, sid)
	bs.mu.Unlock()
}

func (bs *blockStream) flushPendingRows(s *WSServer, sid session.ID, block *openBlock, pending []pendingRows, confirm func(uint64)) {
	if !block.kept {
		for _, delivery := range pending {
			confirmPendingRows(confirm, delivery)
		}
		bs.finishPendingRows(sid)
		bs.drainPendingCloses(s, sid)
		return
	}
	store := s.blockStore()
	if store == nil {
		bs.requeuePendingRows(sid, pending)
		return
	}
	// Owner: this stream, on behalf of the helper's attached session.
	// Closing event: each successful append or the session detach.
	for i, delivery := range pending {
		if err := store.AppendBlockRows(context.Background(), content.AppendBlockRows{
			EntryID: block.entry, ArtifactID: block.artifactID,
			FromRow: delivery.from, LostRows: delivery.lost, Rows: delivery.rows,
		}); err != nil {
			s.log.Warn("deferred block rows append failed", "session", sid, "entry", block.entry, "error", err)
			bs.requeuePendingRows(sid, pending[i:])
			return
		}
		// The artifact's cursor follows every committed delivery, in the
		// deferred path exactly as in the direct one: the close places its
		// closing screen at that cursor.
		bs.mu.Lock()
		block.rows = delivery.from + uint64(len(delivery.rows)) //nolint:gosec // a row count, not a byte count
		bs.mu.Unlock()
		if len(delivery.rows) > 0 {
			s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
				EntryID: block.entry, From: delivery.from, Count: uint64(len(delivery.rows)), //nolint:gosec // a row count, not a byte count
			})
		}
		confirmPendingRows(confirm, delivery)
	}
	next := bs.takePendingRows(sid, block.attempt)
	if len(next) == 0 {
		bs.mu.Lock()
		bs.flushing[sid] = false
		bs.mu.Unlock()
		bs.drainPendingCloses(s, sid)
		return
	}
	bs.flushPendingRows(s, sid, block, next, confirm)
}

func (bs *blockStream) drainPendingCloses(s *WSServer, sid session.ID) {
	for {
		bs.mu.Lock()
		closes := bs.pendingCloses[sid]
		delete(bs.pendingCloses, sid)
		if len(closes) == 0 {
			delete(bs.closing, sid)
			bs.mu.Unlock()
			return
		}
		bs.closing[sid] = true
		bs.mu.Unlock()
		for _, end := range closes {
			bs.mu.Lock()
			current := bs.current[sid]
			queued := bs.queued[sid]
			if queued == end.attempt && current != nil && current.attempt != end.attempt {
				bs.queuedEnds[sid] = append(bs.queuedEnds[sid], end)
				bs.mu.Unlock()
				continue
			}
			bs.mu.Unlock()
			if !s.closeBlockRowsNow(sid, end.attempt, end.endRow, end.closing, end.nonce, end.incomplete) {
				return
			}
		}
	}
}

func confirmPendingRows(confirm func(uint64), delivery pendingRows) {
	if confirm != nil {
		confirm(delivery.from + uint64(len(delivery.rows))) //nolint:gosec // a row count, not a byte count
	}
}

func (bs *blockStream) finishPendingRows(sid session.ID) {
	bs.mu.Lock()
	delete(bs.pending, sid)
	bs.flushing[sid] = false
	bs.mu.Unlock()
}

func (bs *blockStream) requeuePendingRows(sid session.ID, pending []pendingRows) {
	bs.mu.Lock()
	bs.pending[sid] = append(pending, bs.pending[sid]...)
	bs.flushing[sid] = false
	bs.mu.Unlock()
}

// publishFence records a completion's fence for the end marker that has not
// arrived yet, and resolves any end marker that has been waiting for it —
// in either order, one meeting.
func (bs *blockStream) publishFence(s *WSServer, sid session.ID, hexNonce, attempt string) {
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
	for i, e := range bs.ends[sid] {
		if e.nonce == hexNonce {
			parked = append(parked, e)
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
		s.closeBlockRows(sid, attempt, e.endRow, e.closing, e.nonce, e.incomplete)
	}
}

// notifyBlockSubscriber sends a block notification to the session's one
// attached subscriber — the same fan-out files.changed and git.changed use.
// No subscriber, no send: a client that attaches later reads history.
func (s *WSServer) notifyBlockSubscriber(sid session.ID, method string, params any) {
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
