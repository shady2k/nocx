package transport

// A FAILED ATTEMPT AT A LIFECYCLE FRAME CHANGES NO BLOCK STATE (ADR-0077
// decision 9, from ADR-0076 decision 2: a block's state changes only on
// something the helper reports, and a store write failing is not that).
// The frame's writes and the coordinator's in-memory block state describe
// one block, so after a frame the two agree: a write that failed and then
// succeeded on the frame's next attempt leaves the block exactly as a frame
// that never failed would — open in memory and in the store, at the same row
// cursor — and it then receives its rows and seals on the helper's end like
// any other. A frame that fails on every attempt leaves the block exactly as
// it was before the frame, in memory and in the store, and the next
// coordinator applies the frame once.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// holdSwitch is the store's frame timer, driven by the test: a transaction's
// hold bound fires when the test says so — at a chosen store call, or as it
// is armed — and every pause between attempts fires at once.
type holdSwitch struct {
	mu            sync.Mutex
	armed         func()
	expireAtBirth bool
}

func (h *holdSwitch) timer(d time.Duration, fire func()) func() bool {
	if d != content.LifecycleFrameMaxHold {
		go fire()
		return func() bool { return false }
	}
	h.mu.Lock()
	atBirth := h.expireAtBirth
	if !atBirth {
		h.armed = fire
	}
	h.mu.Unlock()
	if atBirth {
		fire()
		return func() bool { return false }
	}
	return func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.armed = nil
		return true
	}
}

// expireNow fires the bound of the transaction that holds the store now.
func (h *holdSwitch) expireNow() {
	h.mu.Lock()
	fire := h.armed
	h.armed = nil
	h.mu.Unlock()
	if fire != nil {
		fire()
	}
}

func (h *holdSwitch) setExpireAtBirth(on bool) {
	h.mu.Lock()
	h.expireAtBirth = on
	h.mu.Unlock()
}

// expiringOpen is the store with one fault: the frame's transaction is rolled
// back by its hold bound just as the block's open reaches the store, so the
// open's write fails on the frame's first attempt.
type expiringOpen struct {
	blockOutputStore
	holds *holdSwitch
	once  sync.Once
}

func (s *expiringOpen) OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error) {
	s.once.Do(s.holds.expireNow)
	return s.blockOutputStore.OpenBlockOutput(ctx, in)
}

func newSwitchedLedgerStoreAt(t *testing.T, path string, holds *holdSwitch) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i) // newLedgerStoreAt's key, so either opens the file
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:       path,
		Key:        key,
		Budget:     content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger:     log.NewSlogAdapter(nil),
		FrameTimer: holds.timer,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// blockState is one block as the coordinator holds it in memory, and as the
// store holds it. Everything a failed attempt may not change.
type blockState struct {
	Entry, Artifact string
	Rows            uint64
	Current         bool
	Kept            bool
	// Settling is every in-memory mark a close leaves while it waits or
	// retries: parked closes, a close in flight, a recorded fence.
	PendingCloses, Fences int
	Closing               bool
}

func memoryBlock(ws *WSServer, sid session.ID, entry string) (blockState, bool) {
	bs := ws.blockStream
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b := bs.open[sid][entry]
	if b == nil {
		return blockState{}, false
	}
	return blockState{
		Entry: b.entry, Artifact: b.artifactID, Rows: b.rows, Kept: b.kept,
		Current:       bs.current[sid] == b,
		PendingCloses: len(bs.pendingCloses[sid]), Fences: len(bs.fences[sid]), Closing: bs.closing[sid],
	}, true
}

func storedBlock(t *testing.T, db content.ContentDB, sid string) content.OpenBlockRowsEntry {
	t.Helper()
	open, err := db.Ledger().OpenBlockRowsForSession(context.Background(), sid)
	if err != nil {
		t.Fatalf("OpenBlockRowsForSession: %v", err)
	}
	return open
}

// applyFrame applies one frame the way the coordinator does: the kernel's
// ingest inside the store's own frame, the cursor at offset.
func applyFrame(db content.ContentDB, sid string, offset uint64, ingest func(ctx context.Context) error) (ingestErr, frameErr error) {
	frameErr = db.Ledger().ApplyLifecycleFrame(context.Background(), sid, offset, func(ctx context.Context) error {
		ingestErr = ingest(ctx)
		return nil
	})
	return ingestErr, frameErr
}

func TestAWriteThatFailsOnceLeavesTheBlockAsIfItHadNotAndItSealsNormally(t *testing.T) {
	holds := &holdSwitch{}
	db := newSwitchedLedgerStoreAt(t, filepath.Join(t.TempDir(), "content.db"), holds)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	zero := uint64(0)
	if err := db.Ledger().CreateSession(context.Background(), content.Session{
		ID: sid, WorkspaceID: "ws-lifecycle", LifecycleApplied: &zero,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	e.ws.blockRowsStore = &expiringOpen{blockOutputStore: db.Ledger(), holds: holds}
	e.ws.AttachBlockRows(session.ID(sid))

	r := repeatFrames{lane: lane, h: h}
	ingest := func(env lifecycle.Envelope) func(ctx context.Context) error {
		return func(ctx context.Context) error { return pub.Ingest(ctx, "T", env) }
	}
	if ierr, ferr := applyFrame(db, sid, 100, ingest(r.prompt(2))); ierr != nil || ferr != nil {
		t.Fatalf("the prompt frame: ingest %v, frame %v", ierr, ferr)
	}
	// The start frame: the block's open fails on the frame's first attempt
	// and succeeds on its second.
	if ierr, ferr := applyFrame(db, sid, 220, ingest(r.start(3, 0, "make build"))); ierr != nil || ferr != nil {
		t.Fatalf("the start frame, whose open failed once: ingest %v, frame %v — want it applied", ierr, ferr)
	}
	const entry = "s-dom-repeat-0"
	stored := storedBlock(t, db, sid)
	mem, inMemory := memoryBlock(e.ws, session.ID(sid), entry)
	if stored.EntryID != entry || !inMemory || mem.Artifact != stored.ArtifactID || mem.Rows != stored.NextRow || !mem.Current || !mem.Kept {
		t.Fatalf("after the retried start: in memory %+v (present %v), in the store %+v — want the same open block, current and kept, at the same row cursor",
			mem, inMemory, stored)
	}

	// The block then lives like any other: its rows land, and the helper's
	// end seals it with all of them.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0,
		[]emulator.Row{aStreamRow("row-0"), aStreamRow("row-1"), aStreamRow("row-2")}, ""); !confirm || written != 3 {
		t.Fatalf("rows ack = (%d, %v), want rows 0..2 stored", written, confirm)
	}
	if ierr, ferr := applyFrame(db, sid, 340, ingest(r.complete(4, 0))); ierr != nil || ferr != nil {
		t.Fatalf("the complete frame: ingest %v, frame %v", ierr, ferr)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), lifecycleFence(0x50), 3, nil, false)
	if art := blockArtifact(t, db, entry); art.State != content.ArtifactSealed {
		t.Fatalf("the block's artifact is %q after the helper's end, want sealed", art.State)
	}
	kept := streamRows(t, db, entry)
	if len(kept) != 3 || kept[0].Text != "row-0" || kept[2].Text != "row-2" {
		t.Fatalf("the sealed block holds %+v, want rows 0..2", kept)
	}
	if _, still := memoryBlock(e.ws, session.ID(sid), entry); still {
		t.Fatal("the sealed block is still open in memory")
	}
	assertOneOfEach(t, db)
}

func TestAFrameThatFailsEveryAttemptChangesNoBlockAndTheNextCoordinatorAppliesItOnce(t *testing.T) {
	// The helper's end marker may reach the coordinator before the
	// completion frame or after it; when it came first, the failing frame's
	// fence resolves it and the block's seal runs inside the frame.
	for _, endFirst := range []bool{false, true} {
		name := "the end marker after the frame"
		if endFirst {
			name = "the end marker before the frame"
		}
		t.Run(name, func(t *testing.T) { frameThatFailsEveryAttempt(t, endFirst) })
	}
}

func frameThatFailsEveryAttempt(t *testing.T, endFirst bool) {
	holds := &holdSwitch{}
	db := newSwitchedLedgerStoreAt(t, filepath.Join(t.TempDir(), "content.db"), holds)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	zero := uint64(0)
	if err := db.Ledger().CreateSession(context.Background(), content.Session{
		ID: sid, WorkspaceID: "ws-lifecycle", LifecycleApplied: &zero,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	e.ws.AttachBlockRows(session.ID(sid))

	r := repeatFrames{lane: lane, h: h}
	ingest := func(pub interface {
		Ingest(context.Context, lifecycle.TransportID, lifecycle.Envelope) error
	}, tID lifecycle.TransportID, env lifecycle.Envelope,
	) func(ctx context.Context) error {
		return func(ctx context.Context) error { return pub.Ingest(ctx, tID, env) }
	}
	if ierr, ferr := applyFrame(db, sid, 100, ingest(pub, "T", r.prompt(2))); ierr != nil || ferr != nil {
		t.Fatalf("the prompt frame: ingest %v, frame %v", ierr, ferr)
	}
	if ierr, ferr := applyFrame(db, sid, 220, ingest(pub, "T", r.start(3, 0, "make build"))); ierr != nil || ferr != nil {
		t.Fatalf("the start frame: ingest %v, frame %v", ierr, ferr)
	}
	const entry = "s-dom-repeat-0"
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0,
		[]emulator.Row{aStreamRow("row-0"), aStreamRow("row-1"), aStreamRow("row-2")}, ""); !confirm || written != 3 {
		t.Fatalf("rows ack = (%d, %v), want rows 0..2 stored", written, confirm)
	}
	if endFirst {
		e.ws.BlockIntervalEnded(session.ID(sid), lifecycleFence(0x50), 3, nil, false)
	}
	memBefore, _ := memoryBlock(e.ws, session.ID(sid), entry)
	storedBefore := storedBlock(t, db, sid)
	shapeBefore := ledgerShape(t, db)

	// The complete frame: every transaction it begins is rolled back by its
	// bound as it begins, so all four attempts fail.
	holds.setExpireAtBirth(true)
	_, ferr := applyFrame(db, sid, 340, ingest(pub, "T", r.complete(4, 0)))
	var failed *content.FrameFailedError
	if !errors.As(ferr, &failed) || failed.Attempts != 4 {
		t.Fatalf("the complete frame = %v, want a FrameFailedError after 4 attempts", ferr)
	}
	holds.setExpireAtBirth(false)

	memAfter, inMemory := memoryBlock(e.ws, session.ID(sid), entry)
	if !inMemory || memAfter != memBefore {
		t.Fatalf("the failed frame changed the block in memory:\n before %+v\n after  %+v (present %v)", memBefore, memAfter, inMemory)
	}
	if storedAfter := storedBlock(t, db, sid); storedAfter != storedBefore {
		t.Fatalf("the failed frame changed the block in the store:\n before %+v\n after  %+v", storedBefore, storedAfter)
	}
	if shapeAfter := ledgerShape(t, db); shapeAfter != shapeBefore {
		t.Fatalf("the failed frame changed the ledger:\n before %s\n after  %s", shapeBefore, shapeAfter)
	}

	// The next coordinator re-adopts the session from the store and applies
	// the same frame: once.
	fresh := newFreshCoordinator(t, db, sid, lane, h)
	ierr, ferr := applyFrame(db, sid, 340, ingest(fresh.pub, "T2", r.complete(4, 0)))
	if ierr != nil || ferr != nil {
		t.Fatalf("the next coordinator's complete frame: ingest %v, frame %v", ierr, ferr)
	}
	fresh.ws.BlockIntervalEnded(session.ID(sid), lifecycleFence(0x50), 3, nil, false)
	if art := blockArtifact(t, db, entry); art.State != content.ArtifactSealed {
		t.Fatalf("the block's artifact is %q after the next coordinator applied the frame and the helper's end, want sealed", art.State)
	}
	if kept := streamRows(t, db, entry); len(kept) != 3 {
		t.Fatalf("the sealed block holds %d rows, want 3", len(kept))
	}
	row, err := db.Ledger().Entry(context.Background(), entry)
	if err != nil || row == nil || row.Phase != content.PhaseClosed || row.Status != content.EntrySuccess {
		t.Fatalf("the entry after the next coordinator = %+v, %v — want closed, success", row, err)
	}
	assertOneOfEach(t, db)
}

// drainInbox takes every control-plane frame the inbox has retained, and
// answers their notification methods.
func drainInbox(conn *websocket.Conn) []string {
	var methods []string
	b := inboxOf(conn)
	for {
		msg, ok := b.take(func([]byte) bool { return true })
		if !ok {
			return methods
		}
		if f, decoded := decodeFrame(msg); decoded && f.ID == nil {
			methods = append(methods, f.Method)
		}
	}
}

// renderedUntilSentinel sends a sentinel down the session's subscriber and
// answers every notification the renderer received before it, in order: the
// subscriber's queue is FIFO, so whatever was sent before the sentinel has
// arrived when it has.
func renderedUntilSentinel(t *testing.T, e *lifecycleTestEnv, sid string, n int) []string {
	t.Helper()
	marker := fmt.Sprintf("test.sentinel.%d", n)
	e.ws.notifyBlockSubscriber(context.Background(), session.ID(sid), marker, struct{}{})
	if _, err := awaitFrame(e.conn, time.Now().Add(10*time.Second), isNotification(marker)); err != nil {
		t.Fatalf("the sentinel never arrived: %v", err)
	}
	return drainInbox(e.conn)
}

// A FRAME THAT IS NEVER STORED TELLS THE RENDERER NOTHING (codex review of
// the run, findings 4 and 5; ADR-0077 decision 12). The completion's frame
// runs its whole projection — the entry closes, the fence resolves the end
// marker that came first, the block seals — and then its cursor cannot be
// written, on every attempt. Nothing of it is stored, the block is open
// again in memory, and the renderer, which would otherwise have been told
// the command finished and its block closed, was told nothing.
func TestAFrameThatIsNeverStoredTellsTheRendererNothing(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	// The binding records no cursor, so every frame's last write fails.
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))
	r := repeatFrames{lane: lane, h: h}
	mustLifecycleIngest(t, pub, "T", r.prompt(2))
	mustLifecycleIngest(t, pub, "T", r.start(3, 0, "make build"))
	const entry = "s-dom-repeat-0"
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0,
		[]emulator.Row{aStreamRow("row-0"), aStreamRow("row-1"), aStreamRow("row-2")}, ""); !confirm || written != 3 {
		t.Fatalf("rows ack = (%d, %v), want rows 0..2 stored", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), lifecycleFence(0x50), 3, nil, false)
	_ = renderedUntilSentinel(t, e, sid, 1)

	ferr := db.Ledger().ApplyLifecycleFrame(context.Background(), sid, 340, func(ctx context.Context) error {
		return pub.Ingest(ctx, "T", r.complete(4, 0))
	})
	if !errors.Is(ferr, content.ErrLifecycleCursorMissing) {
		t.Fatalf("the complete frame = %v, want it failed at its cursor", ferr)
	}
	for _, m := range renderedUntilSentinel(t, e, sid, 2) {
		switch m {
		case "block.closed", "block.grew", "lifecycle.changed", "history.recorded":
			t.Fatalf("the renderer was sent %s for a frame that was never stored", m)
		}
	}
	if art := blockArtifact(t, db, entry); art.State != content.ArtifactOpen {
		t.Fatalf("the block's artifact is %q after the frame failed, want open", art.State)
	}
	if mem, ok := memoryBlock(e.ws, session.ID(sid), entry); !ok || !mem.Current {
		t.Fatalf("the block in memory after the frame failed: %+v (present %v), want open and current", mem, ok)
	}
}
