package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// orderedSubmission preserves submission order while running off the
// caller's goroutine — the property resize's coalescing lane depends on
// (ws_session_ops_test.go). These tests pin FIFO, the capacity bound, and
// per-task context propagation.

func TestOrderedSubmission_RunsInSubmissionOrder(t *testing.T) {
	sub := NewOrderedSubmission("session")
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
	sub := NewOrderedSubmission("session")
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

// TestOrderedSubmission_NeverRefusesWhileOpen is the stage-8 guarantee the
// capacity test stood for: a queue longer than any fixed depth is accepted and
// drains in order. What bounds how much a client may have outstanding is the
// credit window, not the submission (ADR-8eej3e66 §7).
func TestOrderedSubmission_NeverRefusesWhileOpen(t *testing.T) {
	sub := NewOrderedSubmission("session")
	ctx := context.Background()
	release := make(chan struct{})
	running := make(chan struct{})
	go func() {
		<-running
	}()

	const queued = 64
	var ran int64
	done := make(chan struct{})
	for i := range queued {
		if rej := sub.TrySubmit(ctx, Task{Run: func(context.Context) {
			if atomic.AddInt64(&ran, 1) == queued {
				close(done)
			}
		}}); rej != nil {
			t.Fatalf("submit %d refused: %+v", i, rej)
		}
	}
	close(release)
	waittest.WaitFor(t, "every queued task ran", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

func TestOrderedSubmission_PropagatesTaskContext(t *testing.T) {
	sub := NewOrderedSubmission("session")
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

func TestOrderedSubmission_ShutdownRejectsNewWorkAndWaitsForAdmitted(t *testing.T) {
	sub, ok := NewOrderedSubmission("session").(*orderedSubmission)
	if !ok {
		t.Fatal("NewOrderedSubmission returned an unexpected implementation")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if rejection := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) {
		close(started)
		<-release
	}}); rejection != nil {
		t.Fatalf("TrySubmit: %v", rejection)
	}
	<-started

	sub.Shutdown()
	if rejection := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) {}}); rejection == nil {
		t.Fatal("TrySubmit succeeded after Shutdown")
	}
	waited := make(chan struct{})
	go func() {
		<-sub.done
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned while admitted work was running")
	default:
	}
	close(release)
	<-waited
}

// A queued task whose start gate says no never runs. This is the one-step
// start-against-cancel decision: cancellation does not have to beat the
// worker to the queue, it has to beat the gate.
func TestOrderedSubmissionNeverStartsAWithdrawnTask(t *testing.T) {
	sub, ok := NewOrderedSubmission("ordered-start-gate").(*orderedSubmission)
	if !ok {
		t.Fatal("unexpected ordered submission implementation")
	}
	t.Cleanup(sub.Shutdown)
	block := make(chan struct{})
	started := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) {
		close(started)
		<-block
	}}); rej != nil {
		t.Fatalf("first submit: %+v", rej)
	}
	<-started
	ran := make(chan struct{}, 1)
	if rej := sub.TrySubmit(context.Background(), Task{
		Start: func() bool { return false },
		Run:   func(context.Context) { ran <- struct{}{} },
	}); rej != nil {
		t.Fatalf("second submit: %+v", rej)
	}
	close(block)
	sub.Shutdown()
	select {
	case <-ran:
		t.Fatal("a withdrawn queued task ran")
	case <-time.After(100 * time.Millisecond):
	}
}
