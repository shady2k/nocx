package transport

// The resend's coordinator half (nocx-zg3k3.5.3): the helper's mark is the
// position the next resend starts from, so the acknowledgement means
// STORED — rows an artifact holds — and never "seen and refused". A
// delivery the store already holds (a resent overlap) is trimmed to the
// block's cursor, not refused: dedup by absolute row index is the
// coordinator's half of the owner's decision.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// A refused keep is not a stored row: the mark must stay behind it so the
// resend offers the rows again and the coordinator re-decides with the
// policy then in force.
func TestBlockRowsArrived_ARefusedKeepIsNotConfirmed(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := newLedgerStoreWithPolicy(t, policy)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make secret")

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("classified")}, "")
	if confirm || written != 0 {
		t.Fatalf("ack = (%d, %v), want nothing confirmed: the mark must not claim rows the store refused", written, confirm)
	}
	if body := blockRowsBody(t, db, attempt); body != "" {
		t.Fatalf("a refused command stored %q — not even a first chunk may be written", body)
	}
}

// An unrecorded stream (the helper's buffer overflowed) is not stored, so
// it is not confirmed either: the helper offers those rows again.
func TestBlockRowsArrived_UnrecordedStreamIsNotConfirmed(t *testing.T) {
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	startsACommand(t, e, pub, lane, h, 2, "make flood")
	e.ws.BlockOutputIncomplete(session.ID(sid), 5)

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 5, 0, []emulator.Row{aStreamRow("dropped")}, "")
	if confirm || written != 0 {
		t.Fatalf("ack = (%d, %v), want nothing confirmed while the stream is unrecorded", written, confirm)
	}
}

// Rows that belong to no block are dropped, and — the change the resend
// rides on — not confirmed: nothing about them is stored.
func TestBlockRowsArrived_NoAttemptRowsAreNotConfirmed(t *testing.T) {
	e, _, _, _, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("prompt noise")}, "")
	if confirm || written != 0 {
		t.Fatalf("ack = (%d, %v), want nothing confirmed for rows of no block", written, confirm)
	}
}

// A resent delivery whose start is behind the block's cursor is deduped by
// its absolute row index: the overlap is trimmed, the new tail appended,
// and the store never sees a discontinuity.
func TestBlockRowsArrived_AResentDeliveryIsTrimmedToTheCursor(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("one"), aStreamRow("two")}, ""); !confirm || written != 2 {
		t.Fatalf("first ack = (%d, %v), want rows 0 and 1 stored", written, confirm)
	}

	// The same rows offered again, one row further: index 1 is already in
	// the artifact, index 2 is new.
	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("two"), aStreamRow("three")}, "")
	if !confirm || written != 3 {
		t.Fatalf("resent ack = (%d, %v), want the new row stored and confirmed through 3", written, confirm)
	}
	kept := streamRows(t, db, attempt)
	if len(kept) != 3 {
		t.Fatalf("stored rows = %+v, want exactly three — the overlap stored once", kept)
	}
	for i, want := range []string{"one", "two", "three"} {
		if kept[i].Text != want {
			t.Fatalf("stored row %d = %q, want %q", i, kept[i].Text, want)
		}
	}
}

// The paired ordinary case: rows below a sealed interval's boundary are in
// the sealed block — stored — so a resent delivery below the seal is
// confirmed the way it always was.
func TestBlockRowsArrived_RowsBelowASealedIntervalStayConfirmed(t *testing.T) {
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "make first")
	fence := lifecycleFence(0x31)
	e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("early")}, "")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("first screen")}, false)

	// The second command opens the next block; the resend offers rows the
	// sealed first interval already owns.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	startsACommand(t, e, pub, lane, h, 5, "make second")
	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("early")}, "")
	if !confirm {
		t.Fatalf("ack = (%d, %v), want rows below the seal confirmed: the sealed block stores them", written, confirm)
	}
}

// The resend's gap reaches the store with its cause attached: the sealed
// payload can then say how many rows went missing because the coordinator
// was away (nocx-zg3k3.5.3) — a count the emulator's struck feeds never
// add to.
func TestBlockRowsArrived_AnUnavailableGapReachesThePayload(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make output")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("one")}, ""); !confirm || written != 1 {
		t.Fatalf("first ack = (%d, %v), want row 0 stored", written, confirm)
	}
	// The resend's gap: rows [1, 3) pruned while the coordinator was away.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 3, 2, []emulator.Row{aStreamRow("two")}, proto.LostCauseCoordinatorUnavailable); !confirm || written != 4 {
		t.Fatalf("gap ack = (%d, %v), want the gap named and row 3 stored", written, confirm)
	}

	fence := lifecycleFence(0x33)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 4, nil, false)

	var payload struct {
		UnavailableRows uint64 `json:"unavailableRows"`
		LostRows        uint64 `json:"lostRows"`
	}
	if err := json.Unmarshal([]byte(blockRowsPayloadJSON(t, db, attempt)), &payload); err != nil {
		t.Fatalf("decode the sealed payload: %v", err)
	}
	if payload.UnavailableRows != 2 {
		t.Fatalf("unavailableRows = %d, want 2: the absence's own count", payload.UnavailableRows)
	}
	if payload.LostRows != 2 {
		t.Fatalf("lostRows = %d, want 2", payload.LostRows)
	}
}

// blockRowsPayloadJSON reads a block's sealed payload sidecar the way the
// close left it.
func blockRowsPayloadJSON(t *testing.T, db content.ContentDB, entryID string) string {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType != content.MediaBlockRows {
				continue
			}
			art, err := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
			if err != nil {
				t.Fatalf("Artifact: %v", err)
			}
			return art.Payload
		}
	}
	t.Fatal("no block rows artifact")
	return ""
}
