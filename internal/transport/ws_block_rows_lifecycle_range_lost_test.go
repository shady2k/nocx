package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/session"
)

// ADR-0077: the returning coordinator asked for the lifecycle stream from its
// stored cursor, and the helper answered from its window's base instead — the
// frames between are gone, and the end of the command the session's open block
// belongs to may have been among them. That block is not left running: it is
// sealed as one whose boundary never arrived whole, and its entry closes
// unknown. It runs before the re-adopted stream re-binds the session, so the
// store's open block is what it settles.
func TestLifecycleRangeLost_SettlesTheOpenBlockTheLostRangeCouldHaveEnded(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make long")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("before-the-gap")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	// The coordinator goes away: the block stays open (ADR-0076).
	e.ws.DetachBlockRows(session.ID(sid))

	e.ws.LifecycleRangeLost(session.ID(sid))

	gap := content.TruncGap
	assertSealedAs(t, db, attempt, &gap)
	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil || row == nil {
		t.Fatalf("Entry(%s) = %v, %v", attempt, row, err)
	}
	if row.Status != content.EntryUnknown {
		t.Fatalf("entry status = %q after its lifecycle range was lost, want unknown: the block was left running", row.Status)
	}
	if kept := streamRows(t, db, attempt); len(kept) != 1 || kept[0].Text != "before-the-gap" {
		t.Fatalf("the settled block holds %+v, want the one row it was given", kept)
	}
}
