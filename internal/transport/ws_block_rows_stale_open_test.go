package transport

// Codex review of the round-6 fixes (nocx-2v80t.3.53), blockers in
// internal/transport/ws_block_rows.go:
//
//  1. A stale open's return still corrupted the NEXT attachment: its discard
//     tail deleted opening[sid] — by then the later attachment's own
//     reservation — and then dequeued the session's queue, starting a queued
//     attempt beside the one still in flight in the store. Blocks and rows
//     could reorder.
//  2. attachGen grew forever: detach deleted every other per-session map but
//     never attachGen, so every session that ever attached left an entry for
//     the life of the stream.
//  3. An orphan seal raced detach: the late open's cleanup CloseBlockRows
//     failed after detach's own forced retry pass had already run, so the
//     orphan was parked where nothing would ever revisit it — the store row
//     stood open and the map grew.

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// TestAStaleOpensReturnLeavesTheLaterAttachmentAlone is finding 1: A (first
// attachment) blocks in the store; the session detaches and re-attaches; B
// (second attachment) blocks in the store; C queues behind B. When A returns,
// the generation check stops A from INSTALLING — and nothing else. A's
// return must touch nothing that belongs to the later attachment (no
// opening/waiting change, no dequeue): it only seals A's own store row.
// Paired with TestALiveDiscardStillAdvancesTheQueue, the same discard while
// the session is still attached, where the bookkeeping must keep running.
func TestAStaleOpensReturnLeavesTheLaterAttachmentAlone(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	releaseA := make(chan struct{})
	store := &queueingOpenStore{hold: releaseA, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	// Detach and re-attach under the same id while A is still in the store,
	// then let the new attachment open B (held in the store in turn) and ask
	// for C behind it.
	e.ws.DetachBlockRows(sid)
	e.ws.AttachBlockRows(sid)

	releaseB := make(chan struct{})
	store.mu.Lock()
	store.hold = releaseB
	store.mu.Unlock()
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("B's open never reached the store")
	}
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "C")

	bs := e.ws.blockStream
	bs.mu.Lock()
	queued := append([]string(nil), bs.reopen[sid]...)
	bs.mu.Unlock()
	if len(queued) != 1 || queued[0] != "C" {
		t.Fatalf("reopen queue = %v before A returns, want [C]", queued)
	}

	close(releaseA)
	<-doneA

	// A's return must leave the later attachment exactly as it was.
	bs.mu.Lock()
	opening := bs.opening[sid]
	waiting := bs.waiting[sid]
	queuedAfter := append([]string(nil), bs.reopen[sid]...)
	_, hasA := bs.open[sid]["A"]
	current := bs.current[sid]
	bs.mu.Unlock()
	if opening != "B" {
		t.Fatalf("opening[sid] = %q after the stale open returned, want %q — A's discard deleted the later attachment's own reservation", opening, "B")
	}
	if waiting != "B" {
		t.Fatalf("waiting[sid] = %q after the stale open returned, want %q", waiting, "B")
	}
	if len(queuedAfter) != 1 || queuedAfter[0] != "C" {
		t.Fatalf("reopen queue = %v after the stale open returned, want [C] — A's discard dequeued the later attachment's queue", queuedAfter)
	}
	if hasA || current != nil {
		t.Fatalf("the stale open installed into the re-attached session: hasA=%v current=%+v", hasA, current)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("A's row was sealed %d times, want exactly 1 (its own discard seal)", n)
	}

	// The later attachment goes on: B installs when its own store call
	// lands, and C — asked after B — opens after B, never beside it.
	close(releaseB)
	<-doneB

	got := store.orderSoFar()
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("opened %v, want %v — C must follow B, not run beside it", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("opened %v, want %v — C must follow B, not run beside it", got, want)
		}
	}
	bs.mu.Lock()
	current = bs.current[sid]
	queuedNow := bs.queued[sid]
	_, hasB := bs.open[sid]["B"]
	_, hasC := bs.open[sid]["C"]
	bs.mu.Unlock()
	if current == nil || current.attempt != "B" {
		t.Fatalf("current = %+v, want B — the new attachment's first command, not the one asked after it", current)
	}
	if !hasB || !hasC {
		t.Fatalf("open blocks: hasB=%v hasC=%v, want both installed", hasB, hasC)
	}
	if queuedNow != "C" {
		t.Fatalf("queued[sid] = %q, want %q — C stays queued until B's own end", queuedNow, "C")
	}
}

// TestALiveDiscardStillAdvancesTheQueue is the ordinary path the stale-open
// restriction must not break: the same shape — an open in flight, a later
// attempt queued behind it — while the session stays attached. The in-flight
// attempt's own command completes and its interval ends first, so its open's
// return is a LIVE discard (closedEarly), and the queue it advances belongs
// to this same attachment: the queued attempt must still be dequeued and
// opened by the discard, exactly as before.
func TestALiveDiscardStillAdvancesTheQueue(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, db := lateOpenSetup(t)
	release, done := heldReopen(t, e, sid, attempt, store)

	// The first command completes — and the prompt it leaves behind lets a
	// second command ask while the first's open is still in flight: queued.
	fence := lifecycleFence(0x41)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")

	// The first interval ends while its open is still in flight: the close
	// finds only the reservation and marks it.
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)
	closed := awaitBlockClosed(t, e)
	if closed.EntryID != attempt || closed.Kept {
		t.Fatalf("block.closed = %+v, want %q not kept", closed, attempt)
	}

	close(release)
	<-done

	got := store.orderSoFar()
	if len(got) != 2 || got[0] != attempt || got[1] != second {
		t.Fatalf("opened %v, want [%s %s] — the live discard still advances the queue", got, attempt, second)
	}
	bs := e.ws.blockStream
	bs.mu.Lock()
	current := bs.current[sid]
	_, hasAttempt := bs.open[sid][attempt]
	_, hasSecond := bs.open[sid][second]
	bs.mu.Unlock()
	if current == nil || current.attempt != second {
		t.Fatalf("current = %+v, want %s installed", current, second)
	}
	if hasAttempt {
		t.Fatal("the discarded attempt installed a block after its command closed")
	}
	if !hasSecond {
		t.Fatalf("%s was dequeued and opened but never installed", second)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("the discarded attempt's row was sealed %d times, want exactly 1", n)
	}
	assertBlockSealed(t, db, attempt)
}

// TestAttachDetachCyclesRetainNoGenerationState is finding 2: detach deleted
// every other per-session map; attachGen stayed, so every session that ever
// attached left an entry for the life of the stream. Reclamation must not
// cost the staleness check: one monotonic counter for the whole stream,
// stamped per attachment, keeps every later attachment's generation strictly
// newer than any open reserved under an earlier one — across as many
// detach/re-attach cycles as the session takes. Paired inside with the
// ordinary open under the newest stamp, which must still install.
func TestAttachDetachCyclesRetainNoGenerationState(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	bs := e.ws.blockStream

	for range 50 {
		e.ws.AttachBlockRows(sid)
		bs.mu.Lock()
		stamped := bs.attachGen[sid]
		bs.mu.Unlock()
		if stamped == 0 {
			t.Fatal("attach stamped no generation")
		}
		e.ws.DetachBlockRows(sid)
		bs.mu.Lock()
		_, has := bs.attachGen[sid]
		n := len(bs.attachGen)
		bs.mu.Unlock()
		if has || n != 0 {
			t.Fatalf("attachGen kept the session's entry past detach (map holds %d entries) — per-session generation state is never reclaimed", n)
		}
	}

	// Reclamation must not open the door to an older generation: an open
	// reserved under the first attachment, released two full detach/re-attach
	// cycles later, is still stale — and still only seals its own row.
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}
	for range 2 {
		e.ws.DetachBlockRows(sid)
		bs.mu.Lock()
		_, has := bs.attachGen[sid]
		bs.mu.Unlock()
		if has {
			t.Fatal("attachGen entry survived detach")
		}
		e.ws.AttachBlockRows(sid)
	}
	close(release)
	<-doneA

	bs.mu.Lock()
	_, hasA := bs.open[sid]["A"]
	current := bs.current[sid]
	opening := bs.opening[sid]
	stamped := bs.attachGen[sid]
	bs.mu.Unlock()
	if hasA || current != nil || opening != "" {
		t.Fatalf("an open two generations old installed into the newest attachment: hasA=%v current=%+v opening=%q", hasA, current, opening)
	}
	if stamped == 0 {
		t.Fatal("the live attachment lost its own generation stamp")
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("A's row was sealed %d times, want exactly 1 (its own discard seal)", n)
	}

	// Ordinary path, same attachment: a fresh open under the newest stamp
	// installs like any other.
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")
	bs.mu.Lock()
	current = bs.current[sid]
	bs.mu.Unlock()
	if current == nil || current.attempt != "B" {
		t.Fatalf("current = %+v, want B — the reclaimed, restamped attachment still opens normally", current)
	}
}

// TestALateOpensCleanupAfterDetachIsSettledInline is finding 3: the late
// open's cleanup CloseBlockRows failed AFTER DetachBlockRows's own forced
// orphan pass had already run, so the orphan was recorded where nothing
// would ever revisit it and the store row stood open. A cleanup failure once
// the session is detached is settled at once — retried inline to
// maxCloseAttempts, then a stated loss — never parked. Paired with
// TestADiscardedOpensFailedCleanupSealIsRetriedThenSucceeds
// (ws_block_rows_round6_test.go), the LIVE discard whose failed cleanup is
// still revisited by the next flush, close and the detach.
func TestALateOpensCleanupAfterDetachIsSettledInline(t *testing.T) {
	e, _, _, _, sid, attempt, store, db := lateOpenSetup(t)
	store.closeFailures = 1 // the cleanup fails once, then the retry lands
	release, done := heldReopen(t, e, sid, attempt, store)

	// The session detaches while the open is still in flight — past the
	// point where any orphan recorded later would ever be revisited.
	e.ws.DetachBlockRows(sid)

	close(release)
	<-done

	if n := store.closeAttempts(); n != 2 {
		t.Fatalf("cleanup seal attempted %d times, want 2 (the failure, then the inline retry)", n)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("seals landed = %d, want exactly 1 (the inline retry's own success)", n)
	}
	assertBlockSealed(t, db, attempt)
	bs := e.ws.blockStream
	bs.mu.Lock()
	_, parked := bs.orphanSeals[sid]
	bs.mu.Unlock()
	if parked {
		t.Fatal("a detached session's failed cleanup was parked as an orphan nobody will revisit")
	}
}

// TestALateOpensCleanupAfterDetachThatKeepsFailingIsAStatedLoss is the bound
// of the inline settle: a cleanup that keeps failing past maxCloseAttempts
// is stated as a permanent loss and nothing is retained — the same bound
// closeBlockRowsNow and retryOrphanSeals already keep, for the same reason.
func TestALateOpensCleanupAfterDetachThatKeepsFailingIsAStatedLoss(t *testing.T) {
	e, _, _, _, sid, attempt, store, _ := lateOpenSetup(t)
	store.closeFailures = 99 // never succeeds
	release, done := heldReopen(t, e, sid, attempt, store)

	e.ws.DetachBlockRows(sid)

	close(release)
	<-done

	if n := store.closeAttempts(); n != maxCloseAttempts {
		t.Fatalf("cleanup seal attempted %d times, want %d (the inline bound) before settling", n, maxCloseAttempts)
	}
	if n := store.sealCount(); n != 0 {
		t.Fatalf("seals landed = %d, want 0 — every attempt kept failing", n)
	}
	bs := e.ws.blockStream
	bs.mu.Lock()
	_, parked := bs.orphanSeals[sid]
	bs.mu.Unlock()
	if parked {
		t.Fatal("a permanently failing cleanup was parked as an orphan nobody will revisit instead of being stated as a loss")
	}
}
