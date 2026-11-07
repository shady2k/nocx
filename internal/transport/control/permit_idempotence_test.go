package control

import (
	"context"
	"testing"
	"time"
)

// A double Release must not hand back capacity the permit never held. Before
// Permit became idempotent this failed with two slots free while one permit
// was still out — a semaphore of 2 admitting 3. The bound is the whole point
// of this package, so the way it can be silently exceeded gets a test.
func TestDoubleReleaseDoesNotOverAdmit(t *testing.T) {
	sem := NewWaitingSemaphore("probe", 2, 10*time.Millisecond)

	x, rej := sem.TryAcquire(t.Context())
	if rej != nil {
		t.Fatal("first acquire refused")
	}
	_, rej = sem.TryAcquire(t.Context())
	if rej != nil {
		t.Fatal("second acquire refused")
	}
	// Both permits are out; capacity 2 is exhausted. A third acquire waits its
	// bound out and is told the wait ended.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, r := sem.TryAcquire(ctx); r == nil {
		t.Fatal("third acquire should not have been granted while both permits are held")
	}

	x.Release()
	x.Release() // one holder releases twice; the second must be a no-op

	// One permit is still held, so exactly ONE acquisition may be granted.
	if _, r := sem.TryAcquire(t.Context()); r != nil {
		t.Fatalf("a slot was not free after the single release: %+v", r)
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel2()
	if _, r := sem.TryAcquire(ctx2); r == nil {
		t.Fatal("capacity corrupted: two slots free while one permit is still held")
	}
}
