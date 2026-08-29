package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestOutputRing_CancellableWaitForData verifies that waitForData returns
// when ctx is cancelled without needing data or ring closure (DEFECT 5 fix).
func TestOutputRing_CancellableWaitForData(t *testing.T) {
	ring := newOutputRing()

	// Call waitForData at offset 10 (no data written yet) with a
	// cancellable context. Cancel it after a short delay, verify it
	// returns promptly.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		ring.waitForData(ctx, 10)
		close(done)
	}()

	// No observable exposes that the waiter has parked; this pacing gives the
	// goroutine a chance to enter waitForData before cancellation.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// passed
	case <-time.After(wantWithin):
		t.Fatal("waitForData did not return after ctx cancellation")
	}
}

// TestOutputRing_WaitForDataAlreadyCancelled verifies that waitForData
// returns immediately if the context is already cancelled on entry.
func TestOutputRing_WaitForDataAlreadyCancelled(t *testing.T) {
	ring := newOutputRing()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		ring.waitForData(ctx, 10)
		close(done)
	}()

	select {
	case <-done:
		// passed
	case <-time.After(wantWithin):
		t.Fatal("waitForData did not return immediately with cancelled ctx")
	}
}

// TestOutputRing_WaitForDataClosedRing verifies that a closed ring returns
// true even with ctx not cancelled.
func TestOutputRing_WaitForDataClosedRing(t *testing.T) {
	ring := newOutputRing()
	ring.close()

	ctx := context.Background()
	closed := ring.waitForData(ctx, 0)
	if !closed {
		t.Fatal("expected closed=true for closed ring")
	}
}

// TestOutputRing_WakeBroadcasts verifies that calling wake broadcasts to
// cond-waiters.
func TestOutputRing_WakeBroadcasts(t *testing.T) {
	ring := newOutputRing()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		ring.waitForData(ctx, 10)
		close(done)
	}()

	// No observable exposes that the waiter has parked; this pacing gives the
	// goroutine a chance to enter waitForData before wake.
	time.Sleep(20 * time.Millisecond)

	// Calling wake from outside should unblock the waiter so it can
	// re-check its conditions (including ctx cancellation).
	ring.wake()

	// No observable exposes whether wake was consumed before cancellation; this
	// pacing gives the waiter a chance to re-check after wake.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// passed
	case <-time.After(wantWithin):
		t.Fatal("waitForData did not return after wake + cancel")
	}
}

// ── The recorder as the ring's consumer (nocx-22k1c.1) ───────────────────
//
// The stall these tests exist for: write blocks when the ring is full and
// nothing has been acked (AD-10 — throttle the source, never drop), and the
// acks come from an attached client. A session that outlives its window has
// no client, so nothing acked and the session froze after RingCapacity bytes.
//
// The invariant, both ends named: FROM the moment the last client detaches
// UNTIL one attaches, the ring's consumer is the recorder, and the source is
// never throttled while the recorder is keeping up.

// recordEverything is a stand-in recorder: it consumes the ring as fast as it
// is written and advances the PERSISTENCE cursor, which is what the real one
// does between its store writes. The returned func stops it and waits — one
// call, so a test cannot leave the goroutine running past its own end.
func recordEverything(t *testing.T, ring *outputRing) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pos uint64
		for {
			data, _, _ := ring.snapshot(pos)
			if len(data) == 0 {
				if ring.waitForData(ctx, pos) || ctx.Err() != nil {
					return
				}
				continue
			}
			pos += uint64(len(data))
			if err := ring.recordTo(pos); err != nil {
				t.Errorf("recordTo(%d): %v", pos, err)
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			// The recorder may be parked in write's own wait rather than in
			// waitForData; a wake is what makes the cancel observable.
			ring.wake()
			<-done
		})
	}
}

// THE STALL, and the point of the bead: a detached session produces more than
// the ring can hold and keeps running.
func TestOutputRing_DetachedSessionKeepsWritingPastCapacity(t *testing.T) {
	ring := newOutputRing()
	stopRecorder := recordEverything(t, ring)
	defer stopRecorder()

	// Four times the ring, in the chunks the output pump produces.
	const total = 4 * RingCapacity
	chunk := make([]byte, 32*1024)
	written := make(chan uint64, 1)
	go func() {
		var n uint64
		for n < total {
			if err := ring.write(chunk); err != nil {
				t.Errorf("write at %d: %v", n, err)
				break
			}
			n += uint64(len(chunk))
		}
		written <- n
	}()

	select {
	case n := <-written:
		if n < total {
			t.Fatalf("wrote %d of %d bytes", n, total)
		}
	case <-time.After(wantWithin):
		t.Fatalf("a detached session stalled after %d bytes; the recorder is not freeing the ring",
			ring.writtenLocked())
	}
	// Still alive and still writing at the end — the acceptance says the
	// process keeps going, not merely that it got there.
	if err := ring.write([]byte("after")); err != nil {
		t.Fatalf("write after the burst: %v", err)
	}
}

// The other end of the interval: with a client attached, the recorder does
// NOT free the ring. AD-10 is unchanged for an attached session — the source
// throttles until the client acks, because the client is still owed those
// bytes.
func TestOutputRing_AttachedRingIsNotFreedByTheRecorder(t *testing.T) {
	ring := newOutputRing()
	ring.setAttached(true)

	chunk := make([]byte, 32*1024)
	for filled := 0; filled < RingCapacity; filled += len(chunk) {
		if err := ring.write(chunk); err != nil {
			t.Fatalf("filling the ring: %v", err)
		}
	}
	// The recorder has it all durably; the client has acked nothing.
	if err := ring.recordTo(ring.writtenLocked()); err != nil {
		t.Fatalf("recordTo: %v", err)
	}

	blocked := make(chan struct{})
	go func() {
		_ = ring.write(chunk)
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("the recorder freed the ring while a client was attached; those bytes are still owed to it")
	case <-time.After(100 * time.Millisecond):
	}

	// And the ack — the client's, the only thing that may free it here —
	// releases the writer.
	if err := ring.ack(ring.writtenLocked()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(wantWithin):
		t.Fatal("an ack did not release the writer")
	}
}

// Detaching is what hands the ring to the recorder, and it does so on a
// writer that is ALREADY blocked: the window closes while the shell is
// mid-flood, which is exactly when the stall used to begin.
func TestOutputRing_DetachingReleasesABlockedWriter(t *testing.T) {
	ring := newOutputRing()
	ring.setAttached(true)

	chunk := make([]byte, 32*1024)
	for filled := 0; filled < RingCapacity; filled += len(chunk) {
		if err := ring.write(chunk); err != nil {
			t.Fatalf("filling the ring: %v", err)
		}
	}
	if err := ring.recordTo(ring.writtenLocked()); err != nil {
		t.Fatalf("recordTo: %v", err)
	}

	blocked := make(chan struct{})
	go func() {
		_ = ring.write(chunk)
		close(blocked)
	}()
	// Give the writer a chance to park; nothing observable exposes it, the
	// same pacing the wait tests above use.
	time.Sleep(20 * time.Millisecond)

	ring.setAttached(false)
	select {
	case <-blocked:
	case <-time.After(wantWithin):
		t.Fatal("the last client detached and the writer stayed blocked")
	}
}

// A PERSISTENCE CURSOR IS NOT AN ACKNOWLEDGEMENT. The recorder having the
// bytes says nothing about the frontend having them, and the ring must never
// let one stand in for the other — a client that reconnects would be told it
// already holds bytes it never saw.
func TestOutputRing_RecordCursorIsNotAnAck(t *testing.T) {
	ring := newOutputRing()
	body := []byte("0123456789")
	if err := ring.write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ring.recordTo(uint64(len(body))); err != nil {
		t.Fatalf("recordTo: %v", err)
	}

	// The bytes are still replayable from zero: the ring has room, so
	// nothing was reclaimed, and the client is owed every one of them.
	data, from, needsReset := ring.snapshot(0)
	if needsReset || from != 0 || string(data) != string(body) {
		t.Fatalf("snapshot(0) = (%q, %d, reset=%v); the recorder's cursor consumed the client's replay",
			data, from, needsReset)
	}
	// And the ack cursor has not moved: a client acking from zero is a fresh
	// ack, not a step backwards.
	if err := ring.ack(4); err != nil {
		t.Fatalf("ack(4) after recordTo(10): %v", err)
	}
}

// recordTo is validated exactly as ack is, and for the same reason: a cursor
// that ran ahead of what was produced would free bytes nobody has.
func TestOutputRing_RecordToRejectsImpossibleOffsets(t *testing.T) {
	ring := newOutputRing()
	if err := ring.write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ring.recordTo(11); err == nil {
		t.Error("recordTo past written was accepted")
	}
	if err := ring.recordTo(6); err != nil {
		t.Fatalf("recordTo(6): %v", err)
	}
	if err := ring.recordTo(3); err == nil {
		t.Error("recordTo behind the cursor was accepted")
	}
}

// Reclaiming is what the ring does under PRESSURE and never eagerly: while
// there is room, a detached client's replay survives untouched. Eager
// reclamation would turn every brief disconnect into a reset.
func TestOutputRing_DetachedReplaySurvivesWhileThereIsRoom(t *testing.T) {
	ring := newOutputRing()
	body := make([]byte, 32*1024)
	for i := range body {
		body[i] = byte(i)
	}
	if err := ring.write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ring.recordTo(uint64(len(body))); err != nil {
		t.Fatalf("recordTo: %v", err)
	}

	data, from, needsReset := ring.snapshot(0)
	if needsReset {
		t.Fatal("a detached client was reset while the ring had room to spare")
	}
	if from != 0 || len(data) != len(body) {
		t.Fatalf("snapshot(0) = (%d bytes, from %d), want the whole %d", len(data), from, len(body))
	}
}

// Once the ring HAS reclaimed, a client reattaching at an offset that is gone
// is told to reset — and the pump that resumes for it is not wedged by the
// credit window, whose in-flight measure must count from what the ring can
// still deliver rather than from a cursor no client will ever move.
func TestOutputRing_ReattachAfterReclaimIsNotWedgedByCredit(t *testing.T) {
	ring := newOutputRing()
	stopRecorder := recordEverything(t, ring)

	chunk := make([]byte, 32*1024)
	for n := 0; n < 3*RingCapacity; n += len(chunk) {
		if err := ring.write(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	stopRecorder()

	if _, _, needsReset := ring.snapshot(0); !needsReset {
		t.Fatal("an offset the ring reclaimed was answered as a resume")
	}
	w := ring.writtenLocked()
	ring.setAttached(true)

	// The pump for the reattached client starts at `written` — nothing is in
	// flight to it, so credit must be open immediately.
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), wantWithin)
		defer cancel()
		ring.waitForCredit(ctx, w, w, CreditLimit)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("the reattached pump parked on a credit window nothing can reopen")
	}
}
