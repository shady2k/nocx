package capability_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

// CONTENT KEEPS TWO OWNERS AND NEITHER IS THE OTHER'S (integration, migration
// stages 6 and 7). Stage 6 ordered content per resource; stage 7 gave every
// writer of the store set the shared barrier that backup.restore excludes them
// with. The transport composes SharedBarrier(content) with the content FIFO,
// so this asserts what that composition buys and what it must not lose: a
// restore excludes a content writer, a quarantined content store refuses one,
// and two content writers on different keys still run together — which taking
// Shared(StoreContent) instead, with its capacity-one store key, would not
// allow.
func TestSharedBarrierExcludesWithRestoreAndLeavesOrderToTheCaller(t *testing.T) {
	stores := newStoreGuard(8)
	order := control.NewResourceOrder("content-order", control.DefaultOrderLimit,
		"store:content", "collection:workspace-layout")
	lane := control.NewSemaphore("test-lane", 4)
	admission := func() control.Admission {
		return control.NewComposite(stores.SharedBarrier(capability.StoreContent), order)
	}
	db := newFakeContentDB()
	resources := func(turn time.Duration, keys ...string) context.Context {
		return control.WithOrder(context.Background(), control.OrderRequest{Keys: keys, Turn: turn})
	}

	// A restore holds the set exclusively; a content writer composed of the
	// barrier and the FIFO must wait for it, not slip past on the FIFO alone.
	restore, rej := stores.Exclusive(catalog.OpBackupRestore.Declaration()).TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("the restore was refused: %+v", rej)
	}
	content := capability.NewContentOperation(admission(), lane, db)
	ran := make(chan struct{})
	go func() {
		_ = content.Run(resources(2*time.Second, "store:content"), func(context.Context, capability.ContentService) error {
			close(ran)
			return nil
		})
	}()
	select {
	case <-ran:
		t.Fatal("a content write ran while the restore held the store set")
	case <-time.After(150 * time.Millisecond):
	}
	restore.Release()
	waittest.WaitFor(t, "the content write to run once the restore released", func() bool {
		select {
		case <-ran:
			return true
		default:
			return false
		}
	})

	// Two different keys of one content domain still run together: the store
	// key Shared(StoreContent) would have taken is not in this plan.
	layout := capability.NewLayoutOperation(admission(), lane, db)
	held, rej := order.TryAcquire(resources(time.Second, "store:content"))
	if rej != nil {
		t.Fatalf("hold the content store key: %+v", rej)
	}
	defer held.Release()
	layoutRan := make(chan struct{})
	go func() {
		_ = layout.Run(resources(2*time.Second, "collection:workspace-layout"), func(context.Context, capability.LayoutService) error {
			close(layoutRan)
			return nil
		})
	}()
	select {
	case <-layoutRan:
	case <-time.After(2 * time.Second):
		t.Fatal("a layout write waited on the content store key the plan replaced")
	}

	// And the quarantine travels with the barrier half, so a content method
	// is refused by name rather than running against a store nobody trusts.
	stores.Quarantine([]string{capability.StoreContent}, "the rollback failed")
	ranAfter := false
	err := content.Run(resources(time.Second, "store:content"), func(context.Context, capability.ContentService) error {
		ranAfter = true
		return nil
	})
	if ranAfter {
		t.Fatal("a content write ran against a quarantined store")
	}
	var unavailable *capability.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("quarantined content = %v, want *capability.UnavailableError", err)
	}
}
