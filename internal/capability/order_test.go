package capability

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/transport/control"
)

// stubDB is the content store these proofs never read: the callbacks assert
// admission, not storage.
func stubDB() content.ContentDB { return content.NewStub(log.NewSlogAdapter(nil)) }

// These are the stage-6 proofs at the layer where a mutation lives: a domain's
// operations order on the resources of the invocation, a cancelled queued
// mutation never starts, and holding one resource does not hold another.

func orderCtx(turn time.Duration, keys ...string) context.Context {
	return control.WithOrder(context.Background(), control.OrderRequest{Keys: keys, Turn: turn})
}

func TestOrderedOperationsOrderOnTheirOwnResourceOnly(t *testing.T) {
	order := control.NewResourceOrder("content-order", control.DefaultOrderLimit,
		"store:content", "collection:workspace-layout")
	lane := control.NewSemaphore("test-lane", 4)

	// A content write is in flight on the store.
	held, rej := order.TryAcquire(orderCtx(time.Second, "store:content"))
	if rej != nil {
		t.Fatalf("hold the store: %+v", rej)
	}
	defer held.Release()

	// A layout write is a different resource of the same domain: it runs
	// while the store is held. Under the content gate this was the whole
	// domain, so it waited.
	layout := NewLayoutOperation(order, lane, stubDB())
	layoutRan := make(chan struct{})
	go func() {
		err := layout.Run(orderCtx(time.Second, "collection:workspace-layout"), func(context.Context, LayoutService) error {
			close(layoutRan)
			return nil
		})
		if err != nil {
			t.Errorf("layout write beside a held store: %v", err)
		}
	}()
	select {
	case <-layoutRan:
	case <-time.After(5 * time.Second):
		t.Fatal("a layout write waited for the content store: the domain is still one resource")
	}

	// A second content write does wait, and its turn bounds the wait.
	content := NewContentOperation(order, lane, stubDB())
	err := content.Run(orderCtx(50*time.Millisecond, "store:content"), func(context.Context, ContentService) error {
		t.Error("the second content write ran while the store was held")
		return nil
	})
	if err == nil || !IsRefused(err) {
		t.Fatalf("a content write past its turn = %v, want a refusal", err)
	}
}

func TestACancelledQueuedMutationNeverStarts(t *testing.T) {
	order := control.NewResourceOrder("content-order", control.DefaultOrderLimit, "store:content")
	lane := control.NewSemaphore("test-lane", 4)
	ledger := NewLedgerOperation(order, lane, stubDB())

	held, rej := order.TryAcquire(orderCtx(time.Second, "store:content"))
	if rej != nil {
		t.Fatalf("hold the store: %+v", rej)
	}

	ran := make(chan struct{})
	ctx, cancel := context.WithCancel(orderCtx(time.Second, "store:content"))
	settled := make(chan error, 1)
	go func() {
		settled <- ledger.Run(ctx, func(context.Context, LedgerService) error {
			close(ran)
			return nil
		})
	}()
	// Cancel while it is queued, then release the chain in front of it.
	cancel()
	held.Release()

	select {
	case err := <-settled:
		if err == nil {
			t.Fatal("a cancelled queued mutation's Run returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled queued mutation never settled")
	}
	select {
	case <-ran:
		t.Fatal("a cancelled queued mutation started")
	default:
	}
}

func TestAnOrderedOperationMarksItsCallbackAndRefusesNesting(t *testing.T) {
	order := control.NewResourceOrder("content-order", control.DefaultOrderLimit, "store:content")
	lane := control.NewSemaphore("test-lane", 4)
	ledger := NewLedgerOperation(order, lane, stubDB())

	err := ledger.Run(orderCtx(time.Second, "store:content"), func(ctx context.Context, _ LedgerService) error {
		if !control.OrderHeld(ctx) {
			t.Error("the callback does not know it holds an ordered resource")
		}
		defer func() {
			if recover() == nil {
				t.Error("a nested ordered acquisition did not panic")
			}
		}()
		// The callback's own context: this is the nesting the rule names —
		// a second ordered acquisition made while holding one.
		return ledger.Run(ctx, func(context.Context, LedgerService) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("outer run: %v", err)
	}
	// The outer permit was released by the panic's unwinding: the store is
	// free again.
	p, rej := order.TryAcquire(orderCtx(50*time.Millisecond, "store:content"))
	if rej != nil {
		t.Fatalf("the store was left held: %+v", rej)
	}
	p.Release()
}
