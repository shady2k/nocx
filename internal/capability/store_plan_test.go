package capability

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ONE CALL TAKES EACH STORE RESOURCE ONCE (ADR-8eej3e66 §5: a phase acquires
// its complete set together, and no acquisition is nested inside a held
// scope).
//
// A config call writes the config document and resolves vault row handles, so
// it takes the plan for both stores. Built as TWO per-store plans it takes the
// vault-reset barrier twice — once inside each plan — and the second
// acquisition is not a no-op: a reset queued between them closes the barrier
// to new shared holders (writer preference), while the reset itself waits for
// the first acquisition this call is still holding. One config call deadlocks
// against one reset until its turn ends.
func TestAPlanForTwoStoresTakesEachBarrierOnce(t *testing.T) {
	guard := NewStoreGuard(catalog.Declarations(), time.Second)
	ctx := control.WithHolder(context.Background(), control.Holder{Method: "profiles.create", Request: "1/open#1"})

	// The plan both stores are read through, as the composition root asks for
	// it: one acquisition of the complete set.
	permit, rej := guard.Shared(StoreConfig, StoreVault).TryAcquire(ctx)
	if rej != nil {
		t.Fatalf("a free store set was refused: %+v", rej)
	}
	defer permit.Release()

	checked := map[string]bool{}
	for _, store := range []string{StoreConfig, StoreVault} {
		for _, name := range guard.BarriersOf(store) {
			if checked[name] {
				continue
			}
			checked[name] = true
			if got := heldBy(guard.barriers[name].Holders(), "1/open#1"); got != 1 {
				t.Errorf("barrier %s taken %d times by one call, want 1", name, got)
			}
		}
	}
}

// heldBy counts the permits of one request in a holder snapshot.
func heldBy(holders []control.Holder, request string) int {
	n := 0
	for _, h := range holders {
		if h.Request == request {
			n++
		}
	}
	return n
}
