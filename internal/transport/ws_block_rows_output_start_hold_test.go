package transport

// nocx-4y82c.1.11: a block's artifact begins at the block's OUTPUT start.
// The rows that depart before the shell's ordered output mark — the typed
// command's own echo, and whatever the composer's redraw leaves behind — are
// the renderer's business, never the store's. A recovered interval already
// held them (the retained-window replay); a FRESH block stored them as they
// streamed, so a command that outran its mark carried its own echo twice.
// These tests pin the one rule for both: a block holds what departs before
// its output start, the mark trims it, and a block that closes without ever
// seeing a mark is flushed unchanged — the shell with no shell integration
// keeps exactly the artifact it has today.

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func TestAFreshBlockHoldsItsRowsUntilTheOutputStartMark(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'echoonce'; printf out")

	// The command's own echo departs before the shell's preexec mark, so at
	// the moment it streams its output start is not known yet.
	echo := []emulator.Row{aStreamRow("$ printf 'echo"), aStreamRow("once'; printf out")}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, echo, ""); confirm || up != 0 {
		t.Fatalf("echo rows before the output mark = (%d, %v), want held", up, confirm)
	}
	if rows := streamRows(t, db, attempt); len(rows) != 0 {
		t.Fatalf("artifact holds the echo before the output mark: %+v", rows)
	}

	// The mark is the block's first output row.
	e.ws.BlockOutputStartRow(sid, 2)
	if len(confirmed) == 0 || confirmed[len(confirmed)-1] != 2 {
		t.Fatalf("confirmations after the mark = %v, want the echo settled through row 2", confirmed)
	}

	if up, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("OUT-ONCE")}, ""); !confirm || up != 3 {
		t.Fatalf("first output row ack = (%d, %v), want stored through row 3", up, confirm)
	}
	fence := lifecycleFence(0x81)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 3, []emulator.Row{aStreamRow("$ ")}, false)

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].From != 2 || rows[0].Text != "OUT-ONCE" || rows[1].Text != "$ " {
		t.Fatalf("frozen block rows = %+v, want the output from row 2 and the closing screen, no echo", rows)
	}
	assertSealedAs(t, db, attempt, nil)
}

func TestABlockThatNeverSeesAMarkStoresItsRowsUnchanged(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "echo no-integration")

	// A shell whose integration never emits OSC 133 C: nothing ever names an
	// output start, so the rows still wait when the block closes.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("$ echo no-integration"), aStreamRow("no-integration"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows of a never-marked shell = (%d, %v), want held", up, confirm)
	}
	fence := lifecycleFence(0x82)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 2, []emulator.Row{aStreamRow("$ ")}, false)

	// The artifact is exactly what it was before the hold existed: every
	// departed row, in order, then the closing screen.
	rows := streamRows(t, db, attempt)
	want := []struct {
		from uint64
		text string
	}{{0, "$ echo no-integration"}, {1, "no-integration"}, {2, "$ "}}
	if len(rows) != len(want) {
		t.Fatalf("never-marked block holds %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].From != w.from || rows[i].Text != w.text {
			t.Fatalf("row %d = (%d, %q), want (%d, %q)", i, rows[i].From, rows[i].Text, w.from, w.text)
		}
	}
	if len(confirmed) == 0 || confirmed[len(confirmed)-1] != 2 {
		t.Fatalf("confirmations after the close = %v, want the two rows settled", confirmed)
	}
	assertSealedAs(t, db, attempt, nil)
}

func TestTheOutputStartHoldIsBounded(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "yes")

	// A block may wait for its mark with `maxHeldBeforeOutputStartRows` rows
	// and not one more: past the bound the hold is released and every row is
	// stored, unchanged, as it would have been before the hold existed.
	held := make([]emulator.Row, maxHeldBeforeOutputStartRows)
	for i := range held {
		held[i] = aStreamRow("held")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, held, ""); confirm || up != 0 {
		t.Fatalf("rows up to the bound = (%d, %v), want held", up, confirm)
	}
	if rows := streamRows(t, db, attempt); len(rows) != 0 {
		t.Fatalf("artifact holds %d rows up to the bound, want none held there", len(rows))
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows, 0, []emulator.Row{aStreamRow("overflowing")}, ""); !confirm || up != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("row past the bound ack = (%d, %v), want stored through %d", up, confirm, maxHeldBeforeOutputStartRows+1)
	}
	rows := streamRows(t, db, attempt)
	if len(rows) != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("artifact holds %d rows after the bound, want %d", len(rows), maxHeldBeforeOutputStartRows+1)
	}
	if rows[len(rows)-1].Text != "overflowing" || rows[len(rows)-1].From != maxHeldBeforeOutputStartRows {
		t.Fatalf("last stored row = (%d, %q), want the row that crossed the bound", rows[len(rows)-1].From, rows[len(rows)-1].Text)
	}

	// The block still knows its start is unknown: a late mark is the block's
	// own output start and only trims what arrives after the bound.
	e.ws.BlockOutputStartRow(sid, maxHeldBeforeOutputStartRows+1)
	if up, confirm := e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows+1, 0, []emulator.Row{aStreamRow("after")}, ""); !confirm || up != maxHeldBeforeOutputStartRows+2 {
		t.Fatalf("row after the late mark ack = (%d, %v)", up, confirm)
	}
	fence := lifecycleFence(0x83)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, maxHeldBeforeOutputStartRows+2, []emulator.Row{aStreamRow("$ ")}, false)
	assertSealedAs(t, db, attempt, nil)
}
