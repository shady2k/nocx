package transport

import (
	"context"
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

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// passed
	case <-time.After(2 * time.Second):
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
	case <-time.After(2 * time.Second):
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

	time.Sleep(20 * time.Millisecond)

	// Calling wake from outside should unblock the waiter so it can
	// re-check its conditions (including ctx cancellation).
	ring.wake()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// passed
	case <-time.After(2 * time.Second):
		t.Fatal("waitForData did not return after wake + cancel")
	}
}
