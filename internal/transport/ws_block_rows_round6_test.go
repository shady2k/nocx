package transport

// Stage review round 6 (codex, on 34c764562), findings 1, 2, 3 and 4, all in
// internal/transport/ws_block_rows.go (nocx-2v80t.3.51):
//
//  1. A late open crosses detach and re-attach: the install check asked only
//     whether sources[sid] existed, which a re-attach under the same session
//     id repopulates, so a stale open installed into the NEW attachment.
//  2. A queued duplicate of the attempt that just succeeded stranded the
//     rest: dequeuing it took openAttemptFor's "existing" branch, which never
//     advanced the queue, so anything queued behind it waited forever.
//  3. The reopen queue was an uncapped slice: every request, duplicates
//     included, appended while a store open was blocked.
//  4. A discarded late open whose cleanup CloseBlockRows failed was logged
//     and forgotten: the store row stood open with no owner.

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// TestALateOpenAcrossDetachAndReattachInstallsIntoNeitherAttachment is
// finding 1: attachGen is bumped by every attach (including the first), and
// an open's own reservation captures it; performOpen compares that capture
// against the CURRENT attachGen at install, not merely against sources[sid]'s
// presence — presence alone cannot tell a re-attach from the original
// attachment, since DetachBlockRows followed by AttachBlockRows under the
// SAME session id repopulates sources[sid].
func TestALateOpenAcrossDetachAndReattachInstallsIntoNeitherAttachment(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, db := lateOpenSetup(t)
	release, done := heldReopen(t, e, sid, attempt, store)

	// The session detaches, then re-attaches under the same id, while the
	// retried open is still in the store. The detach changes no block
	// (ADR-0076) — it owes no block.closed — and the completed attempt's
	// discarded block installs into neither attachment.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, lifecycleFence(0x99))))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	e.ws.HelperSessionEnded(sid)
	e.ws.AttachBlockRows(sid)

	// The open's store call now succeeds — and installs into neither
	// attachment: the completed attempt's block was discarded.
	close(release)
	<-done

	bs := e.ws.blockStream
	bs.mu.Lock()
	_, hasBlock := bs.open[sid][attempt]
	current := bs.current[sid]
	bs.mu.Unlock()
	if hasBlock || current != nil {
		t.Fatalf("the late open installed into the re-attached session: hasBlock=%v current=%+v", hasBlock, current)
	}
	assertBlockSealed(t, db, attempt)

	// ADR-0076: the detach said nothing, so the completed attempt's block
	// is settled by the establishment reconcile (the shell's post-command
	// prompt answers the snapshot) — its close arrives here, not kept (the
	// completed attempt's block was discarded).
	got := awaitBlockClosed(t, e)
	if got.EntryID != attempt || got.Kept {
		t.Fatalf("establishment's block.closed = %+v, want %q not kept", got, attempt)
	}

	// Paired: the re-attached session is not broken by the discarded open —
	// a fresh command opens and closes normally under it.
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	fence := lifecycleFence(0x21)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)
	closed := awaitBlockClosed(t, e)
	if closed.EntryID != second || !closed.Kept {
		t.Fatalf("block.closed = %+v, want %q kept — the new attachment stays authoritative", closed, second)
	}
	assertBlockSealed(t, db, second)
}

// TestADuplicateQueuedAttemptDoesNotStrandTheQueue is finding 2: A's open is
// in flight; A is asked for again (queued behind itself — the ledger.bind's
// own redundant retry, most often) and B queues behind that duplicate. Once
// A's own open succeeds, dequeuing the duplicate "A" must not stop at
// openAttemptFor's "existing" branch: it has to keep draining the queue, or
// B waits forever. Paired with TestThreeAttemptsQueuedBehindOneOpenAllOpenOnceEachInOrder
// (nocx-2v80t.3.48), the ordinary case with no duplicate ahead of B.
func TestADuplicateQueuedAttemptDoesNotStrandTheQueue(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	// A duplicate request for A itself while it is still opening, then B
	// queued behind that duplicate.
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")

	close(release)
	<-done

	got := store.orderSoFar()
	want := []string{"A", "B"}
	if len(got) != len(want) {
		t.Fatalf("opened %v, want %v — A once, then B, not stranded behind the duplicate", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("opened %v, want %v — A once, then B, not stranded behind the duplicate", got, want)
		}
	}
}

// TestManyDuplicateRequestsCoalesceInTheQueue is finding 3: the queue is
// bounded by the number of DISTINCT attempts ever queued at once, not by how
// many times any one of them was asked for. Paired with
// TestOneAttemptQueuedBehindOneOpenStillOpens (nocx-2v80t.3.48), the
// ordinary single request.
func TestManyDuplicateRequestsCoalesceInTheQueue(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	for range 50 {
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")
	}

	bs := e.ws.blockStream
	bs.mu.Lock()
	n := len(bs.reopen[sid])
	bs.mu.Unlock()
	if n != 1 {
		t.Fatalf("reopen queue length = %d after 50 duplicate requests for B, want 1 (coalesced, bounded by distinct attempts)", n)
	}

	close(release)
	<-done

	got := store.orderSoFar()
	want := []string{"A", "B"}
	if len(got) != len(want) || got[0] != "A" || got[1] != "B" {
		t.Fatalf("opened %v, want %v — B exactly once despite 50 requests", got, want)
	}
}

// TestADiscardedOpensFailedCleanupSealIsRetriedThenSucceeds is finding 4,
// the retry half of the DONE WHEN's "retried ... or recorded as a stated
// loss": the discard's own cleanup CloseBlockRows fails once, so the row is
// remembered (recordOrphanSeal) rather than forgotten; DetachBlockRows's own
// forced pass (retryOrphanSeals) retries it, the store now answers, and it
// seals for real. Chose retry-then-loss over a bare stated loss because this
// codebase already retries a close a bounded number of times before
// settling for "not kept" (closeBlockRowsNow's own maxCloseAttempts) — the
// same shape, applied to a row this stream no longer tracks any other way.
func TestADiscardedOpensFailedCleanupSealIsRetriedThenSucceeds(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, db := lateOpenSetup(t)
	store.closeFailures = 1 // the discard's own cleanup fails once, then works
	release, done := heldReopen(t, e, sid, attempt, store)

	fence := lifecycleFence(0x31)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)
	closed := awaitBlockClosed(t, e)
	if closed.EntryID != attempt || closed.Kept {
		t.Fatalf("block.closed = %+v, want %q not kept", closed, attempt)
	}

	close(release)
	<-done

	if n := store.closeAttempts(); n != 1 {
		t.Fatalf("the discard's own cleanup seal was attempted %d times before detach, want 1 (the failing one)", n)
	}

	bs := e.ws.blockStream
	bs.mu.Lock()
	_, stillOwed := bs.orphanSeals[sid]
	bs.mu.Unlock()
	if !stillOwed {
		t.Fatal("the failed cleanup seal was not remembered for a retry")
	}

	// The next close, or detach, is the retry (nocx-2v80t.3.51). Detach is
	// forced and unambiguous either way.
	e.ws.HelperSessionEnded(sid)
	if n := store.closeAttempts(); n != 2 {
		t.Fatalf("close attempts after detach = %d, want 2 (the failure, then the retry)", n)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("seals landed = %d, want exactly 1 (the retry's own success)", n)
	}
	assertBlockSealed(t, db, attempt)

	bs.mu.Lock()
	_, stillOwedAfterDetach := bs.orphanSeals[sid]
	bs.mu.Unlock()
	if stillOwedAfterDetach {
		t.Fatal("the orphan seal was not cleared once it landed")
	}
}

// Paired: a cleanup seal that keeps failing past the bound is a stated,
// permanent loss at detach — dropped rather than kept for a retry that will
// never come, since nothing calls retryOrphanSeals for this session again
// once it is gone.
func TestADiscardedOpensFailedCleanupSealBecomesAStatedLossAtDetach(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, _ := lateOpenSetup(t)
	store.closeFailures = 99 // never succeeds
	release, done := heldReopen(t, e, sid, attempt, store)

	fence := lifecycleFence(0x32)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)
	awaitBlockClosed(t, e)

	close(release)
	<-done

	e.ws.HelperSessionEnded(sid)

	if n := store.sealCount(); n != 0 {
		t.Fatalf("seals landed = %d, want 0 — every attempt kept failing", n)
	}
	bs := e.ws.blockStream
	bs.mu.Lock()
	_, stillOwed := bs.orphanSeals[sid]
	bs.mu.Unlock()
	if stillOwed {
		t.Fatal("a row past detach's own forced retry must be dropped as a stated loss, not kept for a retry that will never come")
	}
}
