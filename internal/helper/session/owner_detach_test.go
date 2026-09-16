package session

// Regression tests for nocx-q502e: a detached writer's abandoned goroutine is
// never stopped (owner_ssh.go's own doc), only forgotten, and that forgetting
// was incomplete in two ways.
//
// First, the interval performDetach opens by nilling o.inFlight does not
// close until run() itself returns — but nothing kept the abandoned writer's
// EVENTUAL send on o.writeRes from being read by run()'s own select in that
// interval, and completeWrite, told nothing about detach, dereferenced the
// o.inFlight performDetach had already cleared. TestAStaleWriteOutcome...
// forces exactly that ordering: a write parked mid-Write, detached out from
// under it, and only THEN released, while run() is still looping.
//
// Second, performDetach cleared writerBusy as though the writer were free
// again, so advance() went on to hand the next pending item to a writer
// nobody would ever hear back from — run()'s own goroutine blocking forever
// on the unbuffered o.writeReq. TestAPendingItemAfterDetach... forces that
// ordering: a second item already queued behind the stuck write before
// detach ever fires.
//
// Both drive triggerDetach directly (the same signal owner.stop's forced
// deadline path sends) against the package's own fake Process
// (ownerFakeProcess, owner_test.go) rather than a real ssh.Channel stuck on a
// zero-sized remote window (ssh_owner_test.go, nocx_local_ssh-tagged, needs a
// live network) — a write this owner cannot interrupt and gives up on is the
// fact both bugs turn on, and the fake already produces exactly that,
// deterministically, through blockNextWrite/release rather than any timing.

import (
	"errors"
	"testing"
	"time"
)

// TestAStaleWriteOutcomeAfterDetachIsIgnoredNotAttributed is nocx-q502e's
// panic reproduction. Invariant, stated as the interval AGENTS.md's testing
// rule 3 asks for: from the moment performDetach clears o.inFlight until
// run() returns, no write outcome arriving on o.writeRes is attributed to
// any item — not the one detach already resolved, and not whatever the
// writer is handed next. Before the fix, an outcome arriving inside that
// interval crashed the owner's goroutine on a nil pointer instead.
func TestAStaleWriteOutcomeAfterDetachIsIgnoredNotAttributed(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	entered, release := proc.blockNextWrite()

	stuckDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("stuck")})
	if err != nil {
		t.Fatalf("submit the write that will get stuck: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the write never entered proc.Write")
	}

	// The write is now blocked inside proc.Write — the shape a real SSH
	// write stuck behind a peer holding its window at zero takes
	// (owner_ssh.go). Detach it the way stop's forced deadline path does,
	// without ever hearing back from the writer.
	owner.triggerDetach()

	select {
	case res := <-stuckDone:
		if !errors.Is(res.Err, errDeliveryUnknown) {
			t.Fatalf("the detached in-flight item should resolve delivery_unknown, got: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach never resolved the in-flight item")
	}

	// Now let the abandoned write actually return, while run() is STILL
	// looping — nothing has stopped it. Its outcome lands on o.writeRes and
	// run()'s own select still has a case reading it (owner.go's run). Before
	// the fix, completeWrite read fi := o.inFlight (nil, performDetach having
	// already cleared it) and then dereferenced fi.item, panicking run()'s
	// own goroutine with nobody to recover it.
	release()

	// The observable proof run() survived that read, rather than inferring
	// it from the mere absence of a crash: drive one more item through the
	// same owner and see it actually resolve.
	afterDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("after")})
	if err != nil {
		t.Fatalf("submit after detach: %v", err)
	}
	select {
	case res := <-afterDone:
		if res.Err == nil {
			t.Fatalf("expected the post-detach item to resolve with a definite error, got: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not answer after the stale outcome arrived — it is gone (nocx-q502e)")
	}
}

// TestAPendingItemAfterDetachResolvesInsteadOfHanging is nocx-q502e's second
// half: an item still in o.pending when the writer is detached must never be
// handed to it — that writer will never report back to anyone again — and
// must instead resolve with a definite answer. Invariant as an interval: from
// the detach until run() returns, no payload is ever offered to the writer,
// for any item, old or new.
//
// Before the fix, performDetach cleared writerBusy as though the writer were
// merely free again, so advance() dispatched the pending item straight into
// writeStart's unconditional `o.writeReq <- payload` — unbuffered, and by now
// received by nobody, since the one writer goroutine this owner will ever
// have is itself still blocked inside the abandoned write. That send blocks
// run()'s own goroutine forever: the pending item's done channel never
// receives anything, which is what this test actually waits on rather than
// inferring a hang from a duration.
func TestAPendingItemAfterDetachResolvesInsteadOfHanging(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	entered, release := proc.blockNextWrite()
	// Released last (t.Cleanup runs LIFO, so this fires before the stop
	// above) — not because the fix needs it (a detached writer's own
	// eventual return no longer matters to anything), but so the fake's
	// still-blocked Write does not leak a goroutine, and stop can actually
	// close the process, past this test.
	t.Cleanup(release)

	stuckDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("stuck")})
	if err != nil {
		t.Fatalf("submit the write that will get stuck: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the write never entered proc.Write")
	}

	// Queued behind the stuck write: writerBusy is true, so this can only
	// ever reach o.pending, never the writer, until something frees the
	// writer up — which, in this test, only a detach ever does, because the
	// stuck write's own gate is never released.
	pendingDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("pending")})
	if err != nil {
		t.Fatalf("submit the pending item: %v", err)
	}

	owner.triggerDetach()

	select {
	case res := <-stuckDone:
		if !errors.Is(res.Err, errDeliveryUnknown) {
			t.Fatalf("the detached in-flight item should resolve delivery_unknown, got: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach never resolved the in-flight item")
	}

	select {
	case res := <-pendingDone:
		if res.Err == nil {
			t.Fatalf("expected the pending item to resolve with a definite error rather than executing, got: %+v", res)
		}
		if errors.Is(res.Err, errDeliveryUnknown) {
			t.Fatalf("the pending item was never handed to the writer — delivery_unknown misrepresents it as having been, got: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() deadlocked handing the pending item to the detached writer (nocx-q502e)")
	}
}
