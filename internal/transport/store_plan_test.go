package transport

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// The root hands a config call ONE plan over both stores it writes, not the
// config plan plus the vault plan. The two per-store plans share the
// vault-reset barrier, and a plan that takes a shared barrier twice deadlocks
// against a queued reset — capability's
// TestAPlanForTwoStoresTakesEachBarrierOnce is where that is proved.
//
// What is asserted here is the wiring that cannot be read off a type: the
// admission the config operation is built from covers BOTH stores, which is
// what the pair was for, and a vault-only plan would silently stop excluding
// a reset from a config write.
func TestTheConfigPlanCoversBothStores(t *testing.T) {
	guard := capability.NewStoreGuard(catalog.Declarations(), time.Second)
	server := &WSServer{storeGuard: guard}
	plan := server.domainGates().configVault

	permit, rej := plan.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("a free store set was refused: %+v", rej)
	}
	permit.Release()

	guard.Quarantine([]string{capability.StoreVault}, "a restore could not be finished")
	_, rej = plan.TryAcquire(context.Background())
	if rej == nil || rej.Kind != control.RejectionUnavailable {
		t.Fatalf("the config plan was not refused over a quarantined vault: %+v", rej)
	}
}
