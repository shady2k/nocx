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
	// current is the block the session's newest authenticated start opened:
	// the one rows arriving now belong to (the rows and the ends share one
	// ordered channel, so stream order is the attribution).
	current map[session.ID]*openBlock
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
	if bs.sources == nil {
		bs.sources = make(map[session.ID]struct{})
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
	if sourced && pending && !flushing && waiting == "" && !closing && block != nil {
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
	if sourced && (flushing || waiting != "" || pending || closing) {
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
	if bs.flushing[sid] || bs.closing[sid] || len(bs.pending[sid]) > 0 {
		bs.pendingCloses[sid] = append(bs.pendingCloses[sid], pendingEnd{
			attempt: attempt, nonce: hexNonce, endRow: endRow,
			closing: append([]emulator.Row(nil), closing...),
		})
		bs.mu.Unlock()
		return
	}
	bs.mu.Unlock()
	s.closeBlockRowsNow(sid, attempt, endRow, closing, hexNonce)
}

func (s *WSServer) closeBlockRowsNow(sid session.ID, attempt string, endRow uint64, closing []emulator.Row, hexNonce string) {
	bs := s.blockStream
	bs.mu.Lock()
	block := bs.open[sid][attempt]
	current := bs.current[sid]
	if current != nil && current.attempt == attempt {
		delete(bs.current, sid)
	}
	delete(bs.open[sid], attempt)
	delete(bs.fences[sid], hexNonce)
	bs.mu.Unlock()
	if block == nil {
		return
	}
	if !block.kept {
		return
	}
	store := s.blockStore()
	if store == nil {
		return
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
		} else {
			s.notifyBlockSubscriber(sid, "block.grew", blockGrewParams{
				EntryID: block.entry, From: endRow, Count: uint64(len(closing)), //nolint:gosec // a row count, not a byte count
			})
		}
	}
	if _, err := store.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: block.entry, ArtifactID: block.artifactID,
	}); err != nil {
		s.log.Warn("block rows close failed", "session", sid, "entry", block.entry, "error", err)
		return
	}
	s.notifyBlockSubscriber(sid, "block.closed", blockClosedParams{EntryID: block.entry})
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
	if bs.waiting == nil {
		bs.waiting = make(map[session.ID]string)
	}
	if bs.opening == nil {
		bs.opening = make(map[session.ID]bool)
	}
	// Reserve before the store call: rows can arrive while OPEN is blocked,
	// and a second OPEN fact must not start a competing attempt.
	bs.waiting[sid] = attempt
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
	bs.current[sid] = b
	delete(bs.opening, sid)
	if bs.waiting[sid] == attempt {
		delete(bs.waiting, sid)
	}
	pending := bs.pending[sid]
	delete(bs.pending, sid)
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
	bs.mu.Lock()
	next := bs.pending[sid]
	delete(bs.pending, sid)
	if len(next) == 0 {
		bs.flushing[sid] = false
		bs.mu.Unlock()
		bs.drainPendingCloses(s, sid)
		return
	}
	bs.mu.Unlock()
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
			s.closeBlockRowsNow(sid, end.attempt, end.endRow, end.closing, end.nonce)
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
	if s.contentDB == nil {
		return nil
	}
	return s.contentDB.Ledger()
}
