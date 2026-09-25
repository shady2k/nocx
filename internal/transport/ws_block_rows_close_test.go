package transport

// A block must become SEALED, and the client must be told it closed, whatever
// the producer's end marker claims (nocx-2v80t.3.9). The end marker's own row
// count is the producer's claim about where the interval ended; the artifact's
// cursor is what the coordinator really holds. When the two disagree — the e2e's
// shape, where an end marker arrives behind rows the interval already streamed —
// the old close appended its closing screen at the marker's row, landed behind
// the store's cursor, was refused by continuity, and the block stayed open with
// the close retried on every later delivery: `transcript-0007 never froze`, the
// command stuck running for the client forever.
//
// Two things are asserted here, and they are the two halves of the fix: a close
// that cannot sit at the interval's index settles at the artifact's own cursor
// and seals, and a close that keeps failing reaches a terminal outcome instead
// of being retried for the life of the session.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// A producer whose end marker runs BEHIND rows the interval already streamed:
// the closing screen belongs after what the artifact holds, and the block seals
// with exactly the rows it received.
func TestBlockIntervalEnded_SealsWhenTheEndRowIsBehindTheArtifact(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf many")
	fence := lifecycleFence(0x97)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	// Twenty rows reach the artifact; the interval's end marker then names row
	// ten — the producer's count is behind what it already sent.
	rows := make([]emulator.Row, 0, 20)
	for i := range 20 {
		rows = append(rows, aStreamRow(fmt.Sprintf("row-%02d", i)))
	}
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, rows); !confirm || written != 20 {
		t.Fatalf("rows ack = (%d, %v), want the exclusive end 20 confirmed", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 10, []emulator.Row{aStreamRow("closing")}, false)

	// The block seals: this is the state the client's `cmd-block-running` waits
	// on, and the e2e's whole failure.
	assertBlockSealed(t, db, attempt)

	// The rows it holds are exactly what it received, in the order it received
	// them: the twenty streamed rows, then the closing screen at the cursor the
	// artifact actually had.
	kept := streamRows(t, db, attempt)
	if len(kept) != 21 {
		t.Fatalf("the sealed block holds %d rows, want the 20 streamed plus the closing screen: %+v", len(kept), kept)
	}
	for i := range 20 {
		if want := fmt.Sprintf("row-%02d", i); kept[i].Text != want {
			t.Fatalf("stored row %d = %q, want %q", i, kept[i].Text, want)
		}
	}
	if kept[20].Text != "closing" {
		t.Fatalf("the closing screen is at %d/%q, want it after the rows the artifact holds", kept[20].From, kept[20].Text)
	}

	// The client was told the block closed — the notification the renderer
	// subscribes to (block.grew/block.closed) and the moment a running command
	// stops looking unfinished.
	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}

	// And the decision is TERMINAL: a later delivery (a replay below the closed
	// boundary) and a repeated end marker neither retry the close nor add a
	// second closing screen. Before the fix the first of these was the retry
	// that could never succeed.
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("row-00")}); !confirm {
		t.Fatal("a replay below the closed boundary was not confirmed")
	}
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 10, []emulator.Row{aStreamRow("closing")}, false)
	if again := streamRows(t, db, attempt); len(again) != 21 {
		t.Fatalf("a later delivery changed the sealed block: %d rows (%+v)", len(again), again)
	}
	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	open := e.ws.blockStream.open[session.ID(sid)][attempt]
	e.ws.blockStream.mu.Unlock()
	if parked != 0 || open != nil {
		t.Fatalf("the close left state behind: parked ends=%d open block=%+v", parked, open)
	}
}

// The other disagreement: the interval's end marker runs AHEAD of whatever
// reached the artifact — rows the interval streamed that this block never
// received (the e2e's transcript-0007, whose artifact held only its closing
// screen). The block must still seal, hold exactly what it did receive, and
// claim NOTHING for the absent rows: a lost count for rows before the
// artifact's own first stored row is outside the span the store's summary
// measures over, and subtracting it there underflows into the renderer's
// `18446744073709552000 rows missing`.
func TestBlockIntervalEnded_SealsWithoutClaimingRowsItNeverReceived(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf short")
	fence := lifecycleFence(0x99)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	// Ten rows reach the artifact; the end marker names row forty.
	rows := make([]emulator.Row, 0, 10)
	for i := range 10 {
		rows = append(rows, aStreamRow(fmt.Sprintf("delivered-%02d", i)))
	}
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, rows); !confirm || written != 10 {
		t.Fatalf("rows ack = (%d, %v), want the exclusive end 10 confirmed", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 40, []emulator.Row{aStreamRow("closing")}, false)

	assertBlockSealed(t, db, attempt)
	kept := streamRows(t, db, attempt)
	if len(kept) != 11 {
		t.Fatalf("the sealed block holds %d rows, want the 10 it received plus the closing screen: %+v", len(kept), kept)
	}
	for i := range 10 {
		if want := fmt.Sprintf("delivered-%02d", i); kept[i].Text != want {
			t.Fatalf("stored row %d = %q, want %q", i, kept[i].Text, want)
		}
	}
	if kept[10].Text != "closing" || kept[10].From != 10 {
		t.Fatalf("the closing screen is %+v, want it after the rows the artifact holds (from 10)", kept[10])
	}

	// The account it keeps: no rows dropped by a cap, no rows lost — nothing
	// is billed to a block that received everything it was handed.
	art, err := db.Ledger().Artifact(context.Background(), rowsArtifactID(t, db, attempt))
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	var account struct {
		DroppedRows uint64 `json:"droppedRows"`
		LostRows    uint64 `json:"lostRows"`
	}
	if err := json.Unmarshal([]byte(art.Payload), &account); err != nil {
		t.Fatalf("parse the block's account %q: %v", art.Payload, err)
	}
	if account.DroppedRows != 0 || account.LostRows != 0 {
		t.Fatalf("the block's account is dropped=%d lost=%d, want 0/0: rows it never received are not its loss to claim",
			account.DroppedRows, account.LostRows)
	}
}

// A store that keeps refusing the seal: the close must reach its terminal
// outcome instead of being retried on every later delivery forever. The block
// is settled, the client is told, and no end is left parked.
func TestBlockRowsCloseAbandonsAtTheAttemptBound(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	failing := &closeFailureBlockStore{ledger: db.Ledger(), fail: true}
	e.ws.blockRowsStore = failing
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf stuck")
	fence := lifecycleFence(0x98)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("running")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	// The first attempt is the close itself; every later delivery retries the
	// end while it is still parked. maxCloseAttempts bounds that.
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")}, false)
	for i := range maxCloseAttempts {
		from := uint64(i + 1) //nolint:gosec // a row index, never negative
		e.ws.BlockRowsArrived(session.ID(sid), from, 0, []emulator.Row{aStreamRow(fmt.Sprintf("later-%d", i))})
	}

	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	current := e.ws.blockStream.current[session.ID(sid)]
	open := e.ws.blockStream.open[session.ID(sid)][attempt]
	tries := len(e.ws.blockStream.closeTries[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if parked != 0 || current != nil || open != nil || tries != 0 {
		t.Fatalf("after the attempt bound: parked=%d current=%+v open=%+v tries=%d; want the close settled and nothing left to retry",
			parked, current, open, tries)
	}

	// The client is told the command's block ended, so the surface cannot sit
	// in a running state with no event left that could end it.
	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber after the bound: %v", err)
	}
}
