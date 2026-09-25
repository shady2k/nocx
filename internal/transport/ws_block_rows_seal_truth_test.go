package transport

// block.closed says a block is sealed in history, and `kept` says the store
// holds it (contracts/block.closed.schema.json). A seal the store refused is
// neither, so the notification must not say kept:true for it
// (nocx-2v80t.3.32) — at the session's detach, and at the close path's own
// attempt bound. Each refusal is paired with the seal succeeding.

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func detachWithSeal(t *testing.T, sealFails bool) (blockClosedParams, string, content.ContentDB) {
	t.Helper()
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	e.ws.blockRowsStore = &closeFailureBlockStore{ledger: db.Ledger(), fail: sealFails}
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("partial")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.DetachBlockRows(session.ID(sid))
	return awaitBlockClosed(t, e), attempt, db
}

func TestADetachWhoseSealFailedDoesNotSayTheBlockIsKept(t *testing.T) {
	got, attempt, db := detachWithSeal(t, true)
	if got.EntryID != attempt || got.Kept {
		t.Fatalf("block.closed = %+v, want entry %q with kept:false — the store refused the seal", got, attempt)
	}
	if art := blockArtifact(t, db, attempt); art.State == content.ArtifactSealed {
		t.Fatal("the refusing store sealed the block anyway; the test proves nothing")
	}
}

func TestADetachWhoseSealLandedSaysTheBlockIsKept(t *testing.T) {
	got, attempt, db := detachWithSeal(t, false)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want entry %q kept", got, attempt)
	}
	assertBlockSealed(t, db, attempt)
}

// abandonAtTheBound drives one close to maxCloseAttempts against a store that
// refuses its closing append, and — when sealFails — its seal too.
func abandonAtTheBound(t *testing.T, sealFails bool) (blockClosedParams, string) {
	t.Helper()
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	e.ws.blockRowsStore = &closeFailureBlockStore{ledger: db.Ledger(), appendFail: true, fail: sealFails}
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf stuck")
	fence := lifecycleFence(0x9a)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	e.ws.BlockIntervalEnded(session.ID(sid), fence, 0, []emulator.Row{aStreamRow("final screen")}, false)
	for i := range maxCloseAttempts {
		e.ws.BlockRowsArrived(session.ID(sid), uint64(i), 0, []emulator.Row{aStreamRow(fmt.Sprintf("later-%d", i))}) //nolint:gosec // a row index
	}
	return awaitBlockClosed(t, e), attempt
}

func TestABlockAbandonedWithItsSealRefusedIsNotSaidKept(t *testing.T) {
	got, attempt := abandonAtTheBound(t, true)
	if got.EntryID != attempt || got.Kept {
		t.Fatalf("block.closed = %+v, want entry %q with kept:false — the store refused the seal", got, attempt)
	}
}

func TestABlockAbandonedWithItsSealLandedIsSaidKept(t *testing.T) {
	got, attempt := abandonAtTheBound(t, false)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want entry %q kept", got, attempt)
	}
}
