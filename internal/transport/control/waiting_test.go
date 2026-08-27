package control

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// The waiting admission is the gate behind the domain-conflict fix: a
// conflicting operation WAITS (bounded) for the gate instead of refusing
// instantly, so a sequential client whose previous response left the permit
// held for a moment is never told the control plane is busy. These tests
// pin the two bounds — the wait timeout and the queue depth — and the
// acquire-after-release handoff.

// releaseGate holds a waiting admission's single permit until released,
// standing in for an in-flight conflicting operation.
type releaseGate struct {
	a       Admission
	permit  Permit
	release chan struct{}
	once    sync.Once
}

// holdBusy acquires the sole permit of a capacity-1 waiting admission and
// returns a releaseGate the test releases when the conflict should end.
func holdBusy(t *testing.T, a Admission) *releaseGate {
	t.Helper()
	p, rej := a.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("initial acquire refused: %+v", rej)
	}
	return &releaseGate{a: a, permit: p, release: make(chan struct{})}
}

func (g *releaseGate) free() {
	g.once.Do(func() {
		g.permit.Release()
		close(g.release)
	})
}

// waitersOf reads the admission's waiter accounting (in-package test).
func waitersOf(t *testing.T, a Admission, want int, what string) {
	t.Helper()
	ws, ok := a.(*waitingSemaphore)
	if !ok {
		t.Fatalf("admission is %T, want *waitingSemaphore", a)
	}
	// One accessor for both the condition and the failure text, so the
	// detail closure never takes a lock the caller is already holding.
	waiters := func() int {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.waiters
	}
	waittest.WaitForTimeoutDetail(t, what, 2*time.Second,
		func() string { return fmt.Sprintf("waiter count = %d, want %d", waiters(), want) },
		func() bool { return waiters() == want })
}

// TestWaitingAdmission_AwaitsConflictInsteadOfRefusing is the heart of the
// fix: a request that conflicts with an in-flight operation is NOT refused;
// it waits, and runs once the gate frees. Against the old non-blocking
// semaphore this test fails immediately (the second acquire refuses).
func TestWaitingAdmission_AwaitsConflictInsteadOfRefusing(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, time.Second)
	busy := holdBusy(t, a)
	defer busy.free()

	acquired := make(chan struct{}, 1)
	go func() {
		p, rej := a.TryAcquire(context.Background())
		if rej != nil {
			acquired <- struct{}{}
			return
		}
		p.Release()
		acquired <- struct{}{}
	}()

	// The waiter must not complete while the gate is held...
	select {
	case <-acquired:
		t.Fatal("conflicting acquire completed while the gate was held: it must wait")
	case <-time.After(50 * time.Millisecond):
	}

	// ...and completes promptly once the gate frees.
	busy.free()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("conflicting acquire never completed after the gate freed")
	}
}

// TestWaitingAdmission_WaitBoundIsExhausted pins the wait bound: a request
// that cannot get the gate within the timeout is refused, and the refusal
// is a Rejection (the wire maps it to -32004).
func TestWaitingAdmission_WaitBoundIsExhausted(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, 100*time.Millisecond)
	busy := holdBusy(t, a)
	defer busy.free()

	start := time.Now()
	_, rej := a.TryAcquire(context.Background())
	if rej == nil {
		t.Fatal("waiting acquire must be refused after the wait bound")
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("refusal came after %v, before the wait bound elapsed", elapsed)
	}
}

// TestWaitingAdmission_QueueDepthBoundIsInstant pins the queue-depth bound:
// once maxQueue callers are waiting, the next conflicting caller is refused
// instantly, never queued.
func TestWaitingAdmission_QueueDepthBoundIsInstant(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 2, time.Second)
	busy := holdBusy(t, a)
	defer busy.free()

	// Fill the waiter bound (2 waiters), each waiting on the held gate.
	// The waiter accounting is the registration barrier: poll it directly
	// (in-package test) so the third acquire below races nothing.
	for range 2 {
		go func() {
			_, _ = a.TryAcquire(context.Background())
		}()
	}
	waitersOf(t, a, 2, "fill the queue")

	// The third conflicting caller must be refused instantly.
	start := time.Now()
	if _, rej := a.TryAcquire(context.Background()); rej == nil {
		t.Fatal("acquire beyond the queue depth must be refused")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("queue-depth refusal took %v; it must be instant", elapsed)
	}

	// Releasing the gate lets the queued waiters through one by one.
	busy.free()
	waitersOf(t, a, 0, "drain the queue")
}

// TestWaitingAdmission_CancelledWaitIsRefused pins context cancellation:
// a waiter whose context dies leaves the queue without stranding a token.
func TestWaitingAdmission_CancelledWaitIsRefused(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, time.Second)
	busy := holdBusy(t, a)
	defer busy.free()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, rej := a.TryAcquire(ctx); rej == nil {
			t.Error("cancelled wait must be refused")
		}
	}()
	waitersOf(t, a, 1, "register the waiter")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter never returned")
	}
	waitersOf(t, a, 0, "unregister the cancelled waiter")

	// The cancellation must not leak capacity: a fresh acquire succeeds.
	busy.free()
	p, rej := a.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("acquire after cancellation failed: %+v", rej)
	}
	p.Release()
}

// TestWaitingAdmission_TimedOutWaiterLeavesNoStrandedToken: a waiter that
// times out must not hold a phantom claim on the token, so the next release
// hands the token to a genuinely waiting caller.
func TestWaitingAdmission_TimedOutWaiterLeavesNoStrandedToken(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, 50*time.Millisecond)
	busy := holdBusy(t, a)

	// One waiter times out while the gate is still held.
	if _, rej := a.TryAcquire(context.Background()); rej == nil {
		t.Fatal("expected the timed-out refusal")
	}
	busy.free()

	// The gate is free again and a fresh acquire must succeed immediately.
	p, rej := a.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("gate did not recover after a timed-out waiter: %+v", rej)
	}
	p.Release()
}

// TestWaitingAdmission_ZeroTimeoutRefusesWhenBusy pins the degenerate
// configuration: a zero wait timeout is the old non-blocking behavior — a
// busy gate refuses instantly, a free gate admits.
func TestWaitingAdmission_ZeroTimeoutRefusesWhenBusy(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, 0)
	p, rej := a.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("free gate refused: %+v", rej)
	}
	if _, rej := a.TryAcquire(context.Background()); rej == nil {
		t.Fatal("busy gate with zero timeout must refuse instantly")
	}
	p.Release()
	if _, rej := a.TryAcquire(context.Background()); rej != nil {
		t.Fatalf("gate did not recover after release: %+v", rej)
	}
}

// TestWaitingAdmission_DoubleReleaseDoesNotOverAdmit keeps the permit
// contract: a double release must not hand back a token a held permit owns.
func TestWaitingAdmission_DoubleReleaseDoesNotOverAdmit(t *testing.T) {
	a := NewWaitingSemaphore("config", 1, 8, time.Second)
	p, rej := a.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("acquire refused: %+v", rej)
	}
	if _, rej := a.TryAcquire(context.Background()); rej == nil {
		t.Fatal("second acquire should fail while the permit is held")
	}
	p.Release()
	p.Release() // must be a no-op, not a second token
	if _, rej := a.TryAcquire(context.Background()); rej != nil {
		t.Fatalf("gate did not recover after release: %+v", rej)
	}
}
