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

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
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
}

// maxPendingEnds bounds the interval ends parked while their completion is
// in flight. One per outstanding command is already generous; more means the
// lifecycle channel stopped delivering completions, and an unbounded park
// would hide exactly that.
const maxPendingEnds = 8

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
}

type openBlock struct {
	attempt    string
	entry      string
	artifactID string
	kept       bool
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
}

// splitPendingRowsAt separates deliveries at an interval's absolute end row.
// A flush may already have rows from the next interval waiting behind the
// current append; those rows must follow promotion, not be written to the
// block whose close is parked.
func splitPendingRowsAt(deliveries []pendingRows, endRow uint64) (before, after []pendingRows) {
	for _, delivery := range deliveries {
		deliveryEnd := delivery.from + uint64(len(delivery.rows)) // #nosec G115 -- row count arithmetic
		switch {
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
}

// AttachBlockRows registers a session's helper rows callbacks as this
// stream's source. Without a source the stream is inert — see the header.
func (s *WSServer) AttachBlockRows(sid session.ID) {
	s.blockStream.attach(sid, nil)
}

// AttachBlockRowsWithConfirmation additionally gives deferred rows a way to
// advance the helper's watermark after the bind retry has persisted them.
func (s *WSServer) AttachBlockRowsWithConfirmation(sid session.ID, confirm func(uint64)) {
	s.blockStream.attach(sid, confirm)
}

func (bs *blockStream) attach(sid session.ID, confirm func(uint64)) {
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
	if bs.flushing == nil {
		bs.flushing = make(map[session.ID]bool)
	}
	if bs.pendingCloses == nil {
		bs.pendingCloses = make(map[session.ID][]pendingEnd)
	}
	if bs.closing == nil {
		bs.closing = make(map[session.ID]bool)
	}
	bs.sources[sid] = struct{}{}
	bs.confirmers[sid] = confirm
}

// DetachBlockRows ends a session's streaming: every still-open block is
// sealed with whatever arrived — the honest end when the interval's own end
// never will. The composition root calls it where the session's helper
// callbacks are torn down.
func (s *WSServer) DetachBlockRows(sid session.ID) {
	s.blockStream.detach(s.blockStore(), sid)
}

func (bs *blockStream) detach(store blockOutputStore, sid session.ID) {
	bs.mu.Lock()
	opens := bs.open[sid]
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
	bs.mu.Unlock()
	for _, b := range opens {
		if !b.kept {
			continue
		}
		// Sealing what arrived is best-effort at teardown: the interval's
		// own end never will come, the rows already stored are durable
		// either way, and a close that fails here loses nothing that was
		// not already lost.
		//
		// Owner: the stream itself, on behalf of the detached session.
		// Closing event: DetachBlockRows — the session's rows callbacks are
		// gone and no later fact can name this session.
		_, _ = store.CloseBlockRows(context.Background(), content.CloseBlockRows{
			EntryID: b.entry, ArtifactID: b.artifactID,
		})
	}
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
func (s *WSServer) BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row) (writtenUpTo uint64, confirm bool) {
	if len(rows) == 0 {
		return 0, false
	}
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	block := bs.current[sid]
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
	last := fromRow + uint64(len(rows)) - 1 //nolint:gosec // a row count, not a byte count
	if block == nil || !block.kept {
		return last, true
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
	s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
		EntryID: block.entry, From: fromRow, Count: uint64(len(rows)), //nolint:gosec // a row count, not a byte count
	})
	return last, true
}

// BlockIntervalEnded delivers the session's interval end: the end marker's
// rows, the screen at the marker, and the fence the shell minted for this
// command — the same fence the authenticated completion carries. The fence
// is the authentication: an end whose fence the coordinator has not seen
// from the kernel waits (bounded) for it, and one whose fence resolves
// appends the closing rows and seals the block.
func (s *WSServer) BlockIntervalEnded(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row) {
	hexNonce := hex.EncodeToString(nonce[:])
	bs := s.blockStream
	bs.mu.Lock()
	_, sourced := bs.sources[sid]
	attempt, resolved := bs.fences[sid][hexNonce]
	if bs.ends == nil {
		bs.ends = make(map[session.ID][]pendingEnd)
	}
	bs.mu.Unlock()
	if !sourced {
		return
	}
	if !resolved {
		bs.mu.Lock()
		ends := append(bs.ends[sid], pendingEnd{nonce: hexNonce, endRow: endRow, closing: closing})
		if len(ends) > maxPendingEnds {
			ends = ends[len(ends)-maxPendingEnds:]
			s.log.Warn("block interval ends parked past the bound; the oldest is dropped", "session", sid)
		}
		bs.ends[sid] = ends
		bs.mu.Unlock()
		return
	}
	s.closeBlockRows(sid, attempt, endRow, closing, hexNonce)
}

// closeBlockRows appends the closing rows and seals the block an interval
// leaves behind, then says so. A deferred append owns the interval until it
// finishes; the close is parked rather than racing the append.
func (s *WSServer) closeBlockRows(sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string) {
	bs := s.blockStream
	bs.mu.Lock()
	end := pendingEnd{
		attempt: attempt, nonce: hexNonce, endRow: endRow,
		closing: append([]emulator.Row(nil), closing...),
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
		s.closeBlockRowsNow(sid, attempt, endRow, closing, hexNonce)
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
	s.closeBlockRowsNow(sid, attempt, endRow, closing, hexNonce)
}

func (s *WSServer) closeBlockRowsNow(sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string) bool {
	bs := s.blockStream
	bs.mu.Lock()
	block := bs.open[sid][attempt]
	current := bs.current[sid]
	queued := bs.queued[sid]
	wasCurrent := current != nil && current.attempt == attempt
	bs.mu.Unlock()

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
		if queued == attempt {
			delete(bs.queued, sid)
		}
		delete(bs.closing, sid)
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
	fail := func() {
		// Keep current/open/fence/queued intact. A close error may follow a
		// committed closing-row append, so promotion is unsafe; retain the
		// exact end for a later close/row event to retry. Store continuity
		// rejects any duplicate closing rows without changing ownership.
		bs.mu.Lock()
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], pendingEnd{
			attempt: attempt, nonce: hexNonce, endRow: endRow,
			closing: append([]emulator.Row(nil), closing...),
		})
		delete(bs.closing, sid)
		bs.mu.Unlock()
	}
	if block == nil {
		finish()
		return true
	}
	if !block.kept {
		finish()
		promote()
		return true
	}
	store := s.blockStore()
	if store == nil {
		fail()
		return false
	}
	// Owner: this stream, inside the interval's authenticated end.
	// Closing event: the seal — the closing append and the close are the
	// last writes this interval can cause.
	ctx := context.Background()
	if len(closing) > 0 {
		if err := store.AppendBlockRows(ctx, content.AppendBlockRows{
			EntryID: block.entry, ArtifactID: block.artifactID,
			FromRow: endRow, Rows: closing,
		}); err != nil {
			s.log.Warn("block closing rows failed", "session", sid, "entry", block.entry, "error", err)
			fail()
			return false
		}
		s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
			EntryID: block.entry, From: endRow, Count: uint64(len(closing)), //nolint:gosec // a row count, not a byte count
		})
	}
	if _, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: block.entry, ArtifactID: block.artifactID,
	}); err != nil {
		s.log.Warn("block rows close failed", "session", sid, "entry", block.entry, "error", err)
		fail()
		return false
	}
	finish()
	s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry})
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
		s.closeBlockRows(sid, end.attempt, end.endRow, end.closing, end.nonce)
	}
}

// attemptFact is the production hook: PublishLifecycle hands every
// authenticated attempt fact here. An OPEN fact opens the block (and
// answers the keep decision once per command); a COMPLETED fact publishes
// the fence that resolves any parked interval end; every fact for a session
// with no rows source is a no-op.
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
		if f.Attempt.Fence == "" {
			return
		}
		bs.publishFence(s, sid, f.Attempt.Fence, f.Attempt.ID)
	}
}

// openAttemptFor answers the keep decision for one authenticated start the
// caller has already resolved to its session — the submit path's own shape.
func (bs *blockStream) openAttemptFor(s *WSServer, sid session.ID, attempt string) {
	bs.mu.Lock()
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

	store := s.blockStore()
	if store == nil {
		bs.clearOpening(sid)
		return
	}
	v7, mintErr := uuid.NewV7()
	if mintErr != nil {
		s.log.Warn("block rows artifact id mint failed", "session", sid, "entry", attempt, "error", mintErr)
		bs.clearOpening(sid)
		return
	}
	artifactID := v7.String()
	// Owner: this stream, at the command's authenticated start.
	// Closing event: the open — one store write that decides keep or
	// refuse; nothing is held past it.
	openArtifact, err := store.OpenBlockOutput(context.Background(), content.OpenBlockOutput{
		EntryID: attempt, ArtifactID: artifactID,
	})
	if err != nil {
		if !errors.Is(err, content.ErrNoSuchEntry) {
			s.log.Warn("block rows open failed", "session", sid, "entry", attempt, "error", err)
		}
		bs.clearOpening(sid)
		// Keep waiting reserved. The ledger.bind retry owns the next open.
		return
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
		s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
			EntryID: block.entry, From: delivery.from, Count: uint64(len(delivery.rows)), //nolint:gosec // a row count, not a byte count
		})
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
			if !s.closeBlockRowsNow(sid, end.attempt, end.endRow, end.closing, end.nonce) {
				return
			}
		}
	}
}

func confirmPendingRows(confirm func(uint64), delivery pendingRows) {
	if confirm != nil {
		confirm(delivery.from + uint64(len(delivery.rows)) - 1) //nolint:gosec // a row count, not a byte count
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
	bs.mu.Unlock()
	for _, e := range parked {
		s.closeBlockRows(sid, attempt, e.endRow, e.closing, e.nonce)
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
