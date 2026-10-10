package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// THE EXIT CARRY WAITS FOR THE REPLAY TOO (nocx-zg3k3.5.11). A command
// finishes while the coordinator is away and the shell then exits; the
// returning coordinator is owed the command's completion from the helper's
// lifecycle window (ADR-0076 decision 4, ADR-0077), and it is also told the
// session ended — by the helper's exit record, which rides the pane's output
// and reaches HelperSessionEnded straight from the attachment's end, without
// passing through monitorExit. Measured on the shell-exit acceptance: that
// report sealed the block and closed its entry "unknown" a millisecond before
// the replayed completion was applied, so a command that succeeded read as
// one nobody knows the end of. The end hold already keeps monitorExit and the
// boundary consults behind the replay; the helper's session-end report waits
// behind it the same way, and the block settles with the command's own
// status.
func TestTheHelpersSessionEndWaitsBehindTheReplayItFollows(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "make release")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("built")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	// The re-adopt arms the hold: the window still owes its replay.
	drained := make(chan struct{})
	e.ws.HoldSessionEndFor(session.ID(sid), drained)

	// The helper's exit record arrives now, ahead of the replay.
	ended := make(chan struct{})
	go func() {
		e.ws.HelperSessionEnded(session.ID(sid))
		close(ended)
	}()
	settledEarly := false
	waittest.WaitFor(t, "the helper's session-end report either settled or parked behind the hold", func() bool {
		select {
		case <-ended:
			settledEarly = true
			return true
		default:
		}
		hold := e.ws.waitSessionEnd(session.ID(sid))
		return hold != nil && hold.waiting.Load() > 0
	})
	if settledEarly {
		row, _ := db.Ledger().Entry(context.Background(), attempt)
		status := content.EntryStatus("<unreadable>")
		if row != nil {
			status = row.Status
		}
		t.Fatalf("the helper's session end settled the block (entry %q) while the replay it follows was still owed", status)
	}

	// The replay: the command's completion, spoken while nobody was attached.
	fence := lifecycleFence(0x41)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	close(drained)
	<-ended

	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil || row == nil {
		t.Fatalf("Entry(%s) = %v, %v", attempt, row, err)
	}
	if row.Status != content.EntrySuccess {
		t.Fatalf("entry status = %q, want success: the replayed completion is the command's own end", row.Status)
	}
	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactSealed {
		t.Fatalf("the block is %q after the session's end, want sealed", art.State)
	}
}
