package control

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/testwait"
)

// orderedSubmission preserves submission order while running off the
// caller's goroutine — the property resize's coalescing lane depends on
// (ws_session_ops_test.go). These tests pin FIFO, the capacity bound, and
// per-task context propagation.

func TestOrderedSubmission_RunsInSubmissionOrder(t *testing.T) {
	sub := NewOrderedSubmission("session", 8)
	ctx := context.Background()
	var mu sync.Mutex
	var ran []int
	done := make(chan struct{})
	for i := range 5 {
		i := i
		if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
			mu.Lock()
			ran = append(ran, i)
			mu.Unlock()
			if i == 4 {
				close(done)
			}
		}}); rej != nil {
			t.Fatalf("submit %d refused: %+v", i, rej)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tasks never all ran")
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range ran {
		if ran[i] != i {
			t.Fatalf("execution order = %v, want 0..4 in submission order", ran)
		}
	}
}

func TestOrderedSubmission_SecondTaskWaitsForFirst(t *testing.T) {
	sub := NewOrderedSubmission("session", 4)
	ctx := context.Background()
	firstStarted := make(chan struct{})
	firstDone := make(chan struct{})
	secondRan := make(chan struct{})

	if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
		close(firstStarted)
		<-firstDone
	}}); rej != nil {
		t.Fatal("first submit refused")
	}
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first task never started")
	}
	if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
		close(secondRan)
	}}); rej != nil {
		t.Fatal("second submit refused")
	}
	select {
	case <-secondRan:
		t.Fatal("second task ran while the first was still in flight — order violated")
	case <-time.After(200 * time.Millisecond):
	}
	close(firstDone)
	select {
	case <-secondRan:
	case <-time.After(5 * time.Second):
		t.Fatal("second task never ran after the first completed")
	}
}

func TestOrderedSubmission_RefusesWhenFull(t *testing.T) {
	sub := NewOrderedSubmission("session", 2)
	ctx := context.Background()
	release := make(chan struct{})
	running := make(chan struct{})
	var n int64

	// OCCUPY THE WORKER FIRST, then fill the queue. Submitting exactly
	// `capacity` tasks does not fill it: the worker starts on the first
	// submit and dequeues immediately, so one of those tasks is in flight
	// rather than queued and a slot is free. The next submit was then
	// admitted and the refusal never came — instantly, with no timeout to
	// hint at why. It held on an idle machine, where the worker had not got
	// round to dequeuing yet, and failed under load (nocx-8b47).
	//
	// Once this task is inside Run and blocked on release, the single worker
	// is occupied and nothing drains the channel, so the buffer's occupancy
	// is fully determined by what is submitted after this point.
	if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
		atomic.AddInt64(&n, 1)
		close(running)
		<-release
	}}); rej != nil {
		t.Fatalf("first submit refused: %+v", rej)
	}
	<-running

	// Now fill the queue itself — capacity tasks, none of which can be
	// dequeued while the worker is blocked.
	for i := range 2 {
		if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
			atomic.AddInt64(&n, 1)
		}}); rej != nil {
			t.Fatalf("queued submit %d refused while the queue had room: %+v", i, rej)
		}
	}

	// One more must be refused, never queued.
	rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {}})
	if rej == nil {
		t.Fatal("a submit past capacity must be refused when the queue is full")
	}
	if rej.Scope != "session" {
		t.Fatalf("rejection scope = %q, want the submission's name", rej.Scope)
	}
	close(release)
	// All THREE admitted tasks complete — the one that occupied the worker
	// and the two that were queued behind it. The refused one never ran.
	testwait.WaitForTimeoutDetail(t, "all admitted tasks to complete", 5*time.Second,
		func() string { return fmt.Sprintf("%d of 3 ran", atomic.LoadInt64(&n)) },
		func() bool { return atomic.LoadInt64(&n) >= 3 })
}

func TestOrderedSubmission_PropagatesTaskContext(t *testing.T) {
	sub := NewOrderedSubmission("session", 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observed := make(chan bool, 1)
	if rej := sub.TrySubmit(ctx, Task{Run: func(taskCtx context.Context) {
		select {
		case <-taskCtx.Done():
			observed <- true
		default:
			observed <- false
		}
	}}); rej != nil {
		t.Fatal("submit refused")
	}
	select {
	case cancelled := <-observed:
		if !cancelled {
			t.Fatal("task did not observe the cancelled submit context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task never ran")
	}
}
