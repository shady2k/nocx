package control

import (
	"context"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func awaitKeyedTask(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func newTestKeyedOrdered(t *testing.T, name string, capacity int) *keyedOrderedSubmission {
	t.Helper()
	sub, ok := NewKeyedOrderedSubmission(name, capacity).(*keyedOrderedSubmission)
	if !ok {
		t.Fatal("unexpected keyed ordered submission implementation")
	}
	return sub
}

func TestKeyedOrderedSubmissionIsolatesKeysAndPreservesFIFO(t *testing.T) {
	sub := newTestKeyedOrdered(t, "sessions", 4)
	t.Cleanup(sub.Shutdown)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	order := make(chan int, 2)
	if rej := sub.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) {
		close(started)
		<-release
	}}); rej != nil {
		t.Fatalf("submit A1: %+v", rej)
	}
	awaitKeyedTask(t, started, "A1 to start")
	for _, n := range []int{2, 3} {
		if rej := sub.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) { order <- n }}); rej != nil {
			t.Fatalf("submit A%d: %+v", n, rej)
		}
	}
	independent := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), Task{Key: "B", Run: func(context.Context) { close(independent) }}); rej != nil {
		t.Fatalf("submit B: %+v", rej)
	}
	awaitKeyedTask(t, independent, "B while A1 is blocked")
	select {
	case n := <-order:
		t.Fatalf("A%d overtook blocked A1", n)
	default:
	}
	// Shutdown fences admission without discarding either queued A task.
	sub.Shutdown()
	select {
	case <-sub.done:
		t.Fatal("shutdown completed with A1 still blocked")
	default:
	}
	close(release)
	released = true
	awaitKeyedTask(t, sub.done, "all keys to drain")
	for _, want := range []int{2, 3} {
		if got := <-order; got != want {
			t.Fatalf("A task order = %d, want %d", got, want)
		}
	}
}

func TestKeyedOrderedSubmissionDrainsInOrderAndReclaimsKeys(t *testing.T) {
	sub := newTestKeyedOrdered(t, "sessions", 3)
	t.Cleanup(sub.Shutdown)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	started := make(chan struct{})
	order := make(chan int, 2)
	if rej := sub.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) {
		close(started)
		<-release
	}}); rej != nil {
		t.Fatalf("submit A1: %+v", rej)
	}
	awaitKeyedTask(t, started, "A1")
	for _, n := range []int{2, 3} {
		if rej := sub.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) { order <- n }}); rej != nil {
			t.Fatalf("submit A%d: %+v", n, rej)
		}
	}
	sub.Shutdown()
	if rej := sub.TrySubmit(context.Background(), Task{Key: "B", Run: func(context.Context) { t.Error("rejected task ran") }}); rej == nil {
		t.Fatal("admitted work after shutdown")
	}
	close(release)
	released = true
	awaitKeyedTask(t, sub.done, "admitted tasks to drain")
	for _, want := range []int{2, 3} {
		if got := <-order; got != want {
			t.Fatalf("A task order = %d, want %d", got, want)
		}
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.admitted != 0 || sub.keys != nil {
		t.Fatalf("drained submission retained state: admitted=%d keys=%v", sub.admitted, sub.keys)
	}
}

func TestKeyedOrderedSubmissionBoundsRunningAndQueuedGlobally(t *testing.T) {
	sub := newTestKeyedOrdered(t, "sessions", 3)
	t.Cleanup(sub.Shutdown)
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 2)
	for _, key := range []string{"A", "B"} {
		if rej := sub.TrySubmit(context.Background(), Task{Key: key, Run: func(context.Context) {
			started <- struct{}{}
			<-release
		}}); rej != nil {
			t.Fatalf("submit %s: %+v", key, rej)
		}
	}
	awaitKeyedTask(t, started, "first running key")
	awaitKeyedTask(t, started, "second running key")
	if rej := sub.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) {}}); rej != nil {
		t.Fatalf("submit queued A: %+v", rej)
	}
	refused := make(chan *Rejection, 1)
	go func() {
		refused <- sub.TrySubmit(context.Background(), Task{Key: "C", Run: func(context.Context) { t.Error("refused task ran") }})
	}()
	select {
	case rej := <-refused:
		if rej == nil || rej.Reason != "capacity exhausted" || rej.Scope != "sessions" {
			t.Fatalf("capacity refusal = %+v", rej)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capacity admission blocked")
	}
}

func TestKeyedOrderedSubmissionRejectsEmptyKeysWithoutAllocatingState(t *testing.T) {
	sub := newTestKeyedOrdered(t, "sessions", 2)
	if rej := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) { t.Error("empty key ran") }}); rej == nil {
		t.Fatal("empty key was admitted")
	}
	if sub.keys != nil || sub.admitted != 0 {
		t.Fatal("empty key allocated scheduling state")
	}
	sub.Shutdown()
	awaitKeyedTask(t, sub.done, "empty submission shutdown")
	sub.Shutdown()
	zero := newTestKeyedOrdered(t, "zero", 0)
	if rej := zero.TrySubmit(context.Background(), Task{Key: "A", Run: func(context.Context) { t.Error("zero capacity ran") }}); rej == nil {
		t.Fatal("zero-capacity submission admitted a task")
	}
	zero.Shutdown()
	awaitKeyedTask(t, zero.done, "zero-capacity submission shutdown")
}

func TestKeyedOrderedSubmissionPreservesCancelledTaskContext(t *testing.T) {
	sub := newTestKeyedOrdered(t, "sessions", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observed := make(chan error, 1)
	if rej := sub.TrySubmit(ctx, Task{Key: "A", Run: func(taskCtx context.Context) { observed <- taskCtx.Err() }}); rej != nil {
		t.Fatalf("submit: %+v", rej)
	}
	sub.Shutdown()
	awaitKeyedTask(t, sub.done, "cancelled task")
	if err := <-observed; err != context.Canceled {
		t.Fatalf("task context error = %v, want canceled", err)
	}
}

func TestKeyedOrderedSubmissionReclaimsIdleKeysBeforeShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sub := newTestKeyedOrdered(t, "sessions", 1)
		defer sub.Shutdown()
		for i := range 64 {
			ran := false
			if rej := sub.TrySubmit(context.Background(), Task{Key: strconv.Itoa(i), Run: func(context.Context) {
				ran = true
			}}); rej != nil {
				t.Fatalf("submit key %d after previous key drained: %+v", i, rej)
			}
			synctest.Wait()
			if !ran {
				t.Fatalf("key %d never ran", i)
			}
			if sub.admitted != 0 || sub.keys != nil {
				t.Fatalf("idle key %d retained scheduler state: admitted=%d keys=%v", i, sub.admitted, sub.keys)
			}
		}
	})
}
