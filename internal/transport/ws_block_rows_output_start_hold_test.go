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
	"time"

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

// ── The three review findings on the hold (nocx-4y82c.1.11) ────────────────
//
// Each is a data-loss sequence: output the coordinator had already received
// never reached the artifact. They are fixed test-first, under -race.

// holdReleaseProbe is how long the session-end test lets the end reach its
// seal before it concludes the release is holding it. It is a probe, not an
// assertion: the test's claim is the artifact's content, and this only decides
// when the store gate opens.
const holdReleaseProbe = 2 * time.Second

// A session end must not seal between a mark taking the held rows out of the
// hold and the release delivering them: the rows are in neither place, and a
// seal that runs there closes the artifact short and forgets them.
func TestASessionEndWaitsForAnInFlightHoldRelease(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	store := newBlockingAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf output")

	// Two rows wait in the hold. The mark names row zero as the block's
	// output start, so the release delivers both.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("$ printf output"), aStreamRow("output"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the mark = (%d, %v), want held", up, confirm)
	}

	// The mark starts the release, which parks in the store.
	markDone := make(chan struct{})
	go func() {
		defer close(markDone)
		e.ws.BlockOutputStartRow(sid, 0)
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the release never reached the store")
	}

	// The helper reports the session's end while the release is in the store.
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		e.ws.HelperSessionEnded(sid)
	}()

	// Wait for the end to do the thing the finding is about — reach its seal —
	// rather than for a duration: with the defect it seals now, with the fix
	// it waits for the release and reaches no seal until the gate below opens.
	// The assertion is the artifact; this only decides when the gate opens.
	select {
	case <-store.sealed:
	case <-endDone:
	case <-time.After(holdReleaseProbe):
	}

	// The release finishes; the end must then seal over its rows.
	close(store.release)
	select {
	case <-markDone:
	case <-time.After(wantWithin):
		t.Fatal("the release did not finish")
	}
	select {
	case <-endDone:
	case <-time.After(wantWithin):
		t.Fatal("the session end did not finish")
	}

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "$ printf output" || rows[1].Text != "output" {
		t.Fatalf("artifact holds %+v, want the released prefix the session end sealed over", rows)
	}
	assertBlockSealed(t, db, attempt)
}

// A helper overflow ends the block incomplete from that row on. The rows the
// coordinator received BEFORE the overflow were stored before this hold
// existed and must stay stored: the release is not a row arriving after the
// marker, so it must not be answered by the unrecorded gate.
func TestAHeldPrefixSurvivesAHelperOverflow(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")

	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("kept-1"), aStreamRow("kept-2"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the overflow = (%d, %v), want held", up, confirm)
	}
	// The helper's buffer overflows at row 2: everything from there is lost,
	// and the two rows received before it are not.
	e.ws.BlockOutputIncomplete(sid, 2)
	fence := lifecycleFence(0x86)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "kept-1" || rows[1].Text != "kept-2" {
		t.Fatalf("artifact holds %+v, want the two rows received before the overflow", rows)
	}
	assertSealedAs(t, db, attempt, gap)
}

// The bound is stated where rows ENTER the hold, so the rows that accumulate
// while an open is in flight are bounded too: 300 of them cannot wait for a
// mark that may never come.
func TestTheHoldBoundCoversRowsThatArriveDuringTheOpen(t *testing.T) {
	e, _, _, _, sid, attempt, store, db := lateOpenSetup(t)
	e.ws.BlockOutputStartPlaneAttached(sid)
	release, done := heldReopen(t, e, sid, attempt, store)

	rows := make([]emulator.Row, maxHeldBeforeOutputStartRows+44)
	for i := range rows {
		rows[i] = aStreamRow("during-open")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, rows, ""); confirm || up != 0 {
		t.Fatalf("rows during the open = (%d, %v), want them held pending the block", up, confirm)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("the open did not finish")
	}

	stored := streamRows(t, db, attempt)
	if len(stored) != len(rows) {
		t.Fatalf("artifact holds %d rows after the open, want all %d: the bound is enforced where rows enter the hold", len(stored), len(rows))
	}
	e.ws.blockStream.mu.Lock()
	held := len(e.ws.blockStream.current[sid].preOutputStart)
	armed := e.ws.blockStream.current[sid].holdBeforeOutputStart
	e.ws.blockStream.mu.Unlock()
	if held != 0 || armed {
		t.Fatalf("hold after the open = %d rows, armed %v, want the bound to have released it", held, armed)
	}
}
