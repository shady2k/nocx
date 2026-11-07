package control

import (
	"context"
	"testing"
	"time"
)

// TestResponseEnqueuedBeforeReleaseIsNotRefused drives the defect window
// DIRECTLY, deterministically: the handler enqueues its response inside the
// task, and the permit is released a moment later — so a sequential client
// that answers the response and sends its next conflicting request can land
// in that window. The test holds the window open: the first task signals
// that its response is on the wire and then blocks, still holding the
// conflict gate (exactly the task tail of the real wiring). The second
// submission must WAIT, never be refused.
//
// The wiring under test mirrors the transport's: the submission hands the task
// over and the TASK acquires the operation's composite — conflict gates
// (waiting) — inside its Run, on the task goroutine. Against the old
// non-blocking gates this test fails deterministically: the second task's
// composite acquire refuses instantly.
func TestResponseEnqueuedBeforeReleaseIsNotRefused(t *testing.T) {
	conflict := NewWaitingSemaphore("config", 1, time.Second)
	sub := AsyncSubmission{}

	responseSent := make(chan struct{})
	releaseFirst := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), Task{Run: func(ctx context.Context) {
		// The operation's composite: conflict gates before the execution
		// lane (canonical order), acquired on the task goroutine.
		p, rej := conflict.TryAcquire(ctx)
		if rej != nil {
			return
		}
		defer p.Release()
		// The handler's callback ran: the response is enqueued...
		close(responseSent)
		// ...and the permit is STILL held — the defect window.
		<-releaseFirst
	}}); rej != nil {
		t.Fatalf("first submit refused: %+v", rej)
	}
	select {
	case <-responseSent:
	case <-time.After(time.Second):
		t.Fatal("first task never reached the response enqueue")
	}

	// The client received the response and sends its next conflicting
	// request. It must not be refused at the queue...
	ran := make(chan struct{}, 1)
	if rej := sub.TrySubmit(context.Background(), Task{Run: func(ctx context.Context) {
		p, rej := conflict.TryAcquire(ctx)
		if rej == nil {
			defer p.Release()
		}
		ran <- struct{}{}
	}}); rej != nil {
		t.Fatalf("second submit refused at the queue: %+v", rej)
	}

	// ...and it must not have RUN while the first task still holds the
	// gate: it waits on the conflict gate, never refuses.
	select {
	case <-ran:
		t.Fatal("second task completed while the conflict gate was held: it was refused, not queued")
	case <-time.After(50 * time.Millisecond):
	}

	// The first task finishes: the gate frees and the second task runs.
	close(releaseFirst)
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("second task never ran after the gate freed")
	}
}
