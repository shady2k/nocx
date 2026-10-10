package transport

// ADR-0076: the coordinator going away changes no block; only what the
// helper reports does.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// ADR-0076: the coordinator going away changes no block. DetachBlockRows
// forgets the streaming attachment and nothing else: the open block, its
// cursor, and its open ledger entry survive in the store, the re-attached
// stream continues them by absolute departed-row index, and the block ends
// only when the helper reports its end.
func TestDetachBlockRows_ACoordinatorDetachChangesNoBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	if err := db.Ledger().CreateSession(context.Background(), content.Session{ID: sid, WorkspaceID: "ws-lifecycle"}); err != nil {
		t.Fatalf("create persisted session for rebind: %v", err)
	}
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make restart")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("kept-0"), aStreamRow("kept-1")}, ""); !confirm || written != 2 {
		t.Fatalf("first ack = (%d, %v), want rows 0 and 1 stored", written, confirm)
	}

	e.ws.DetachBlockRows(session.ID(sid))

	// The artifact is still open, its cursor still at 2: the detach changed
	// nothing in the store.
	art := blockArtifact(t, db, attempt)
	if art.State != content.ArtifactOpen {
		t.Fatalf("artifact state = %q after the coordinator detach, want open", art.State)
	}
	var payload struct {
		NextRow uint64 `json:"nextRow"`
	}
	if err := json.Unmarshal([]byte(art.Payload), &payload); err != nil {
		t.Fatalf("decode the payload: %v (raw %s)", err, art.Payload)
	}
	if payload.NextRow != 2 {
		t.Fatalf("cursor = %d after the coordinator detach, want 2", payload.NextRow)
	}

	// The stream re-attaches (the coordinator came back). The attach alone
	// re-binds the open block from the store — session id → open entry →
	// its cursor — and the re-adopt's attempt fact, when it arrives,
	// finds the block already installed.
	e.ws.AttachBlockRows(session.ID(sid))
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, session.ID(sid), attempt)
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{aStreamRow("kept-2"), aStreamRow("kept-3")}, ""); !confirm || written != 4 {
		t.Fatalf("continued ack = (%d, %v), want rows 2 and 3 appended to the open block", written, confirm)
	}

	// The helper reports the interval's end: only now does the block seal,
	// with every row it was given across the restart.
	fence := lifecycleFence(0x37)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 4, nil, false)

	kept := streamRows(t, db, attempt)
	if len(kept) != 4 {
		t.Fatalf("the sealed block holds %d rows, want all four across the restart", len(kept))
	}
	for i, want := range []string{"kept-0", "kept-1", "kept-2", "kept-3"} {
		if kept[i].Text != want {
			t.Fatalf("row %d = %q, want %q", i, kept[i].Text, want)
		}
	}
}

// The paired half: when the HELPER reports the session's end, the open
// block settles (ADR-0074 decision 3 as amended by ADR-0076) — sealed,
// said closed, and inert to later rows.
func TestHelperSessionEnded_SealsTheOpenBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make exit")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("partial")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.HelperSessionEnded(session.ID(sid))

	art := blockArtifact(t, db, attempt)
	if art.State != content.ArtifactSealed {
		t.Fatalf("artifact state = %q after the helper reported the session's end, want sealed", art.State)
	}
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 5, 0, []emulator.Row{aStreamRow("late")}, ""); confirm {
		t.Fatal("a session the helper reported ended still answered rows")
	}
}

// The re-adopt re-bind (ADR-0076 decision 3): the attach ALONE re-binds
// the stream to the open entry the store still holds — session id → open
// entry → its cursor — without waiting for a lifecycle fact. A re-adopted
// lane stays Desynchronized until the shell answers the snapshot at its
// post-command prompt, and until then no attempt fact can reach the block
// stream: without the re-bind the tail rows are rows of no block and are
// dropped. The resent overlap is deduped by the cursor, and the
// interval's own end — the helper resends the ends the drop took — closes
// the same block.
func TestAttachBlockRows_AReAdoptingStreamContinuesTheOpenBlockFromTheStore(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	// The sessions row production's own open path writes; the read the
	// re-bind makes joins the entry's session through it.
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make restart")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("kept-0"), aStreamRow("kept-1")}, ""); !confirm || written != 2 {
		t.Fatalf("first ack = (%d, %v), want rows 0 and 1 stored", written, confirm)
	}

	e.ws.DetachBlockRows(session.ID(sid))

	// The coordinator comes back. No attempt fact is ingested here — the
	// lane is Desynchronized and its lifecycle events quarantined — yet
	// the attach alone must continue the open block from the store.
	e.ws.AttachBlockRows(session.ID(sid))

	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{aStreamRow("kept-2"), aStreamRow("kept-3")}, ""); !confirm || written != 4 {
		t.Fatalf("continued ack = (%d, %v), want the tail appended to the rebound block", written, confirm)
	}
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("kept-1"), aStreamRow("kept-2")}, ""); !confirm || written != 4 {
		t.Fatalf("overlap ack = (%d, %v), want the resent prefix deduped at the cursor", written, confirm)
	}

	fence := lifecycleFence(0x3b)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 4, nil, false)

	kept := streamRows(t, db, attempt)
	if len(kept) != 4 {
		t.Fatalf("the sealed block holds %d rows, want all four across the restart", len(kept))
	}
	for i, want := range []string{"kept-0", "kept-1", "kept-2", "kept-3"} {
		if kept[i].Text != want {
			t.Fatalf("row %d = %q, want %q", i, kept[i].Text, want)
		}
	}
	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactSealed {
		t.Fatalf("artifact state = %q, want sealed by the resent end", art.State)
	}
}

// The race the re-bind loses nothing to: a lifecycle open for the same
// attempt can land between the attach and the re-bind's store read,
// installing the same open artifact at rows 0 — the artifact this
// coordinator process did not open already holds rows, and a cursor at 0
// turns the first tail delivery into a store refusal. The merge lifts the
// installed block to the durable cursor and keeps the artifact identity.
func TestTheRebindReconcilesAnEarlierInstalledBlockToTheDurableCursor(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.AttachBlockRows(sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make race")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("r0"), aStreamRow("r1")}, ""); !confirm {
		t.Fatal("the streamed rows were not confirmed")
	}
	art := blockArtifact(t, db, attempt)
	e.ws.DetachBlockRows(sid)

	e.ws.AttachBlockRows(sid)
	// A lifecycle open won the race: the same entry, the same (idempotent)
	// artifact, installed at rows 0 — what performOpen gives a block this
	// process opened.
	e.ws.blockStream.mu.Lock()
	raced := &openBlock{attempt: attempt, entry: attempt, artifactID: art.ID, kept: true}
	e.ws.blockStream.open[sid] = map[string]*openBlock{attempt: raced}
	e.ws.blockStream.current[sid] = raced
	e.ws.blockStream.mu.Unlock()

	// The re-bind's store read lands last: same entry, the durable cursor.
	e.ws.blockStream.adoptOpenBlock(sid, content.OpenBlockRowsEntry{
		EntryID: attempt, ArtifactID: art.ID, NextRow: 2,
	})

	e.ws.blockStream.mu.Lock()
	if raced.rows != 2 {
		t.Fatalf("rebound cursor = %d, want the durable 2", raced.rows)
	}
	if raced.artifactID != art.ID {
		t.Fatalf("artifact id = %q, want the open artifact's own %q kept", raced.artifactID, art.ID)
	}
	e.ws.blockStream.mu.Unlock()

	if written, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("r2")}, ""); !confirm || written != 3 {
		t.Fatalf("tail ack = (%d, %v), want the tail appended past the durable cursor", written, confirm)
	}
}

// A NEW command started after the re-adopt owns the stream: the recovered
// block stays named in bs.open — the end its own interval still owes must
// still be able to close it — and never takes current from the later
// command's block.
func TestTheRebindDoesNotStealCurrentFromALaterBlock(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.AttachBlockRows(sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make first")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("f0")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	art := blockArtifact(t, db, attempt)
	e.ws.DetachBlockRows(sid)

	e.ws.AttachBlockRows(sid)
	e.ws.blockStream.mu.Lock()
	later := &openBlock{attempt: "later", entry: "later", artifactID: "later-art", kept: true}
	e.ws.blockStream.open[sid] = map[string]*openBlock{"later": later}
	e.ws.blockStream.current[sid] = later
	e.ws.blockStream.mu.Unlock()

	e.ws.blockStream.adoptOpenBlock(sid, content.OpenBlockRowsEntry{
		EntryID: attempt, ArtifactID: art.ID, NextRow: 1,
	})

	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if e.ws.blockStream.current[sid] != later {
		t.Fatal("the rebind took current from the later command's block")
	}
	recovered := e.ws.blockStream.open[sid][attempt]
	if recovered == nil {
		t.Fatal("the recovered block is not named in bs.open: its own end could never close it")
	}
	if recovered.rows != 1 {
		t.Fatalf("recovered cursor = %d, want the durable 1", recovered.rows)
	}
}
