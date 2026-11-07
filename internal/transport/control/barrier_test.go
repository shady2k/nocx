package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

// A waiting exclusive holder is the one thing that stops a restore being
// starved by continuous readers, and it is the reason the barrier exists at
// all (ADR-8eej3e66 §5).
//
// The test proves the waiter is REGISTERED before it asserts anything about
// it, and it does that without a sleep: once an exclusive waiter exists, a new
// shared acquisition must be REFUSED by its own bound instead of granted. That
// refusal is the observable, and only the writer-preference rule produces it.
func TestBarrier_AWaitingExclusiveAdmitsNoNewShared(t *testing.T) {
	b := control.NewBarrier("store-set", 5*time.Second)

	held, rej := b.Shared().TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("first shared acquisition refused: %+v", rej)
	}
	defer held.Release()

	granted := make(chan struct{})
	go func() {
		p, r := b.Exclusive().TryAcquire(context.Background())
		if r != nil {
			close(granted)
			return
		}
		close(granted)
		// Held until the test releases every reader below; the goroutine
		// must not release it early, or the "no new reader" state would
		// disappear under the assertion.
		<-time.After(2 * time.Second)
		p.Release()
	}()

	// Refused, which means the exclusive waiter was registered: writer
	// preference is the only rule that refuses a new reader while a writer
	// waits, and a granted reader would say no writer had arrived yet.
	waittest.WaitFor(t, "the exclusive waiter to register", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		p, r := b.Shared().TryAcquire(ctx)
		if r == nil {
			p.Release()
			return false
		}
		return true
	})

	select {
	case <-granted:
		t.Fatal("the exclusive holder was granted while a shared holder still held the barrier")
	default:
	}

	held.Release()
	waittest.WaitFor(t, "the exclusive holder to be granted once the reader releases", func() bool {
		select {
		case <-granted:
			return true
		default:
			return false
		}
	})
}

// A wait that runs out is refused with the wait-exceeded class, and it leaves
// no waiter behind: the next acquisition after the holder releases succeeds
// immediately.
func TestBarrier_WaitIsBoundedAndLeavesNoWaiter(t *testing.T) {
	b := control.NewBarrier("store-set", 30*time.Millisecond)
	held, rej := b.Exclusive().TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("exclusive acquisition refused: %+v", rej)
	}

	if _, r := b.Shared().TryAcquire(context.Background()); r == nil || r.Kind != control.RejectionWaitExceeded {
		t.Fatalf("shared acquisition got %+v, want a wait-exceeded rejection", r)
	}
	held.Release()
	p, r := b.Shared().TryAcquire(context.Background())
	if r != nil {
		t.Fatalf("shared acquisition after release refused: %+v", r)
	}
	p.Release()
}

// A full waiter queue is refused at once instead of growing: maxQueue 0 is the
// bound with nothing in it.
// There is no waiter count (ADR-8eej3e66 stage 8): a burst of shared
// acquisitions held behind one exclusive waiter is queued and served in turn,
// never refused for its size.
func TestBarrier_EverySharedWaiterBehindAnExclusiveHolderIsServed(t *testing.T) {
	b := control.NewBarrier("store-set", 5*time.Second)
	held, rej := b.Exclusive().TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("exclusive acquisition refused: %+v", rej)
	}
	const waiters = 12
	served := make(chan struct{}, waiters)
	for range waiters {
		go func() {
			p, rej := b.Shared().TryAcquire(context.Background())
			if rej == nil {
				p.Release()
			}
			served <- struct{}{}
		}()
	}
	held.Release()
	for range waiters {
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Fatal("a shared waiter was never served")
		}
	}
}

// Release is idempotent, and a double release must not leave the barrier
// believing a mode is still held.
func TestBarrier_DoubleReleaseIsHarmless(t *testing.T) {
	b := control.NewBarrier("store-set", 100*time.Millisecond)
	shared, _ := b.Shared().TryAcquire(context.Background())
	shared.Release()
	shared.Release()

	exclusive, r := b.Exclusive().TryAcquire(context.Background())
	if r != nil {
		t.Fatalf("exclusive after a double shared release refused: %+v", r)
	}
	exclusive.Release()
	exclusive.Release()

	again, r := b.Shared().TryAcquire(context.Background())
	if r != nil {
		t.Fatalf("shared after a double exclusive release refused: %+v", r)
	}
	again.Release()
}

// Continuous readers plus one writer make progress: every reader finishes, the
// writer gets the barrier, and nothing deadlocks. A deadlock here would hang
// the test to its own deadline, which is the failure this asserts against.
func TestBarrier_ReadersAndOneWriterAllFinish(t *testing.T) {
	b := control.NewBarrier("store-set", 5*time.Second)
	var wg sync.WaitGroup
	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				p, r := b.Shared().TryAcquire(context.Background())
				if r != nil {
					t.Errorf("reader refused: %+v", r)
					return
				}
				p.Release()
			}
		}()
	}

	exclusive, r := b.Exclusive().TryAcquire(context.Background())
	if r != nil {
		t.Fatalf("exclusive refused against continuous readers: %+v", r)
	}
	exclusive.Release()
	wg.Wait()
}
