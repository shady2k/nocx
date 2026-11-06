package control

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// orderCtx is one invocation's context: the canonical keys it orders on, the
// turn it may wait, and the holder identity the dispatcher would bind.
func orderCtx(keys []string, turn time.Duration) context.Context {
	ctx := WithOrder(context.Background(), OrderRequest{Keys: keys, Turn: turn})
	return WithHolder(ctx, Holder{Method: "session.resize", Request: "1/resize#1"})
}

func newTestOrder(t *testing.T, limit int, prefixes ...string) *ResourceOrder {
	t.Helper()
	return NewResourceOrder("test", limit, prefixes...)
}

func mustAcquire(t *testing.T, order *ResourceOrder, ctx context.Context) Permit {
	t.Helper()
	p, rej := order.TryAcquire(ctx)
	if rej != nil {
		t.Fatalf("acquire %v: %+v", ctx, rej)
	}
	return p
}

func TestResourceOrderLetsADifferentResourceThrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		order := newTestOrder(t, 8, "session:")
		first := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
		ran := make(chan struct{})
		go func() {
			p, rej := order.TryAcquire(orderCtx([]string{"session:a"}, time.Second))
			if rej != nil {
				t.Errorf("same-key waiter refused: %+v", rej)
				return
			}
			p.Release()
			close(ran)
		}()
		synctest.Wait()
		select {
		case <-ran:
			t.Fatal("session:a started while session:a was held")
		default:
		}
		// A different session is free: no domain is held.
		mustAcquire(t, order, orderCtx([]string{"session:b"}, 25*time.Millisecond)).Release()
		first.Release()
		<-ran
	})
}

func TestResourceOrderHoldsAKeySetTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		order := newTestOrder(t, 8, "session:", "store:")
		first := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))

		setStarted := make(chan struct{})
		go func() {
			p, rej := order.TryAcquire(orderCtx([]string{"session:a", "store:content"}, time.Second))
			if rej != nil {
				t.Errorf("key set refused: %+v", rej)
				return
			}
			close(setStarted)
			p.Release()
		}()
		synctest.Wait()

		// A store-only waiter queued after the set is behind it: the set
		// acquires both keys at once, so the store is never held by anyone
		// else in between.
		storeStarted := make(chan struct{})
		go func() {
			p, rej := order.TryAcquire(orderCtx([]string{"store:content"}, time.Second))
			if rej != nil {
				t.Errorf("store waiter refused: %+v", rej)
				return
			}
			close(storeStarted)
			p.Release()
		}()
		synctest.Wait()
		select {
		case <-setStarted:
			t.Fatal("the key set started while session:a was held")
		case <-storeStarted:
			t.Fatal("the store waiter overtook the queued key set")
		default:
		}
		first.Release()
		<-setStarted
		<-storeStarted
	})
}

func TestResourceOrderDropsACancelledWaiterBeforeItStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		order := newTestOrder(t, 8, "session:")
		held := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))

		ctx, cancel := context.WithCancel(orderCtx([]string{"session:a"}, time.Second))
		type outcome struct {
			permit Permit
			rej    *Rejection
		}
		settled := make(chan outcome, 1)
		go func() {
			p, rej := order.TryAcquire(ctx)
			settled <- outcome{p, rej}
		}()
		synctest.Wait()
		cancel()
		got := <-settled
		if got.permit != nil {
			got.permit.Release()
			t.Fatal("a cancelled waiter acquired the key")
		}
		if got.rej == nil {
			t.Fatal("a cancelled waiter settled without a rejection")
		}
		held.Release()

		// The next waiter is not left behind the dropped one.
		p := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
		p.Release()
		order.mu.Lock()
		defer order.mu.Unlock()
		if len(order.queues) != 0 || order.admitted != 0 {
			t.Fatalf("order kept state after every release: queues=%d admitted=%d", len(order.queues), order.admitted)
		}
	})
}

func TestResourceOrderTurnBoundsTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		order := newTestOrder(t, 8, "session:")
		held := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
		defer held.Release()
		_, rej := order.TryAcquire(orderCtx([]string{"session:a"}, 25*time.Millisecond))
		if rej == nil {
			t.Fatal("a waiter past its turn acquired the key")
		}
		if !strings.Contains(rej.Reason, "wait exceeded") {
			t.Fatalf("rejection reason = %q", rej.Reason)
		}
		if len(rej.Holders) != 1 || rej.Holders[0].Method != "session.resize" {
			t.Fatalf("holder = %+v", rej.Holders)
		}
	})
}

func TestResourceOrderStartAgainstCancelSettlesExactlyOnce(t *testing.T) {
	// Both orders of start and cancel. Whichever wins, the invocation either
	// ran (permit returned) or did not (rejection), never both, and the key
	// is free afterwards.
	for i := 0; i < 200; i++ {
		order := newTestOrder(t, 8, "session:")
		held := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
		ctx, cancel := context.WithCancel(orderCtx([]string{"session:a"}, time.Second))
		settled := make(chan bool, 1)
		var ran sync.WaitGroup
		ran.Add(1)
		go func() {
			defer ran.Done()
			p, rej := order.TryAcquire(ctx)
			if rej != nil {
				settled <- false
				return
			}
			settled <- true
			p.Release()
		}()
		held.Release()
		cancel()
		<-settled
		ran.Wait()
		// Nothing is held: the key can be taken at once.
		p := mustAcquire(t, order, orderCtx([]string{"session:a"}, 50*time.Millisecond))
		p.Release()
	}
}

func TestResourceOrderRefusesNestedAcquisition(t *testing.T) {
	order := newTestOrder(t, 8, "session:")
	permit := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
	defer permit.Release()
	ctx := WithOrderHeld(orderCtx([]string{"session:b"}, time.Second))
	defer func() {
		if recover() == nil {
			t.Fatal("nested acquisition did not panic")
		}
	}()
	_, _ = order.TryAcquire(ctx)
}

func TestResourceOrderBoundsAdmission(t *testing.T) {
	order := NewResourceOrder("test", 1, "session:")
	held := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
	defer held.Release()
	_, rej := order.TryAcquire(orderCtx([]string{"session:b"}, 20*time.Millisecond))
	if rej == nil || !strings.Contains(rej.Reason, "resource order full") {
		t.Fatalf("admission past the limit = %+v", rej)
	}
}

func TestHoldsOrderFindsTheOrderInAComposite(t *testing.T) {
	order := newTestOrder(t, 8, "session:")
	if !HoldsOrder(NewComposite(order, NewSemaphore("lane", 1))) {
		t.Fatal("HoldsOrder did not see the order inside a composite")
	}
	if HoldsOrder(NewComposite(NewSemaphore("lane", 1))) {
		t.Fatal("HoldsOrder saw an order in a plain composite")
	}
}

func TestResourceOrderRepeatedTimeoutsGrowNothing(t *testing.T) {
	// The constraint table's "repeated timeouts cannot grow resources", for
	// this admission: ten thousand calls that each expire on the same held
	// resource leave no waiter, no queue and no goroutine behind.
	synctest.Test(t, func(t *testing.T) {
		before := runtime.NumGoroutine()
		order := newTestOrder(t, 8, "session:")
		held := mustAcquire(t, order, orderCtx([]string{"session:a"}, time.Second))
		for i := 0; i < 10000; i++ {
			_, rej := order.TryAcquire(orderCtx([]string{"session:a"}, 10*time.Millisecond))
			if rej == nil {
				t.Fatalf("call %d acquired a held key", i)
			}
		}
		order.mu.Lock()
		queues, admitted := len(order.queues), order.admitted
		order.mu.Unlock()
		if queues != 1 || admitted != 1 {
			t.Fatalf("after 10,000 expiries: queues=%d admitted=%d, want the one held key", queues, admitted)
		}
		held.Release()
		order.mu.Lock()
		queues, admitted = len(order.queues), order.admitted
		order.mu.Unlock()
		if queues != 0 || admitted != 0 {
			t.Fatalf("after release: queues=%d admitted=%d", queues, admitted)
		}
		synctest.Wait()
		if after := runtime.NumGoroutine(); after > before {
			t.Fatalf("goroutines grew from %d to %d", before, after)
		}
	})
}
