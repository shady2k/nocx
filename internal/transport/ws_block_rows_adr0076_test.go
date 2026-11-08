package transport

// ADR-0076: the coordinator going away changes no block; only what the
// helper reports does.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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
	marks := attachConfirmedBlockRows(t, e.ws, session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make restart")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("kept-0"), aStreamRow("kept-1")}, ""); confirm || written != 0 {
		t.Fatalf("first admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 2)
	awaitBlockRowsIdle(t, e.ws, session.ID(sid))

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
	marks = attachConfirmedBlockRows(t, e.ws, session.ID(sid))
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, session.ID(sid), attempt)
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{aStreamRow("kept-2"), aStreamRow("kept-3")}, ""); confirm || written != 0 {
		t.Fatalf("continued admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 4)
	awaitBlockRowsIdle(t, e.ws, session.ID(sid))

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
	marks := attachConfirmedBlockRows(t, e.ws, session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make exit")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("partial")}, ""); confirm || written != 0 {
		t.Fatalf("row admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 1)
	awaitBlockRowsIdle(t, e.ws, session.ID(sid))

	e.ws.HelperSessionEnded(session.ID(sid))

	art := blockArtifact(t, db, attempt)
	if art.State != content.ArtifactSealed {
		t.Fatalf("artifact state = %q after the helper reported the session's end, want sealed", art.State)
	}
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 5, 0, []emulator.Row{aStreamRow("late")}, ""); confirm {
		t.Fatal("a session the helper reported ended still answered rows")
	}
}

// Helper exit follows ordinary rows, but its settlement already has a
// per-pane owner goroutine (bindBlockEndToSession). That owner must retain
// the in-flight append and admitted tail until storage finishes.
func TestHelperSessionEnded_DrainsAdmittedRowsBeforeSealing(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	other := session.ID(e.openSession(t, 301))
	screen := []byte(`{"revision":78,"state":{"screen":"other pane survives terminal drain"}}`)
	store := newBlockingAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	marks := attachConfirmedBlockRows(t, e.ws, session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "make exit with tail")

	type admission struct {
		up      uint64
		confirm bool
	}
	admitted := make(chan admission, 2)
	continueReader := make(chan struct{})
	readerDone := make(chan struct{})
	endDone := make(chan struct{})
	var releaseOnce, continueOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.release) }) }
	resumeReader := func() { continueOnce.Do(func() { close(continueReader) }) }
	t.Cleanup(func() {
		// Release both gates even when a callback incorrectly blocks or an
		// assertion fails; fixture shutdown must never inherit our store gate.
		release()
		resumeReader()
		select {
		case <-readerDone:
		case <-time.After(wantWithin):
			t.Error("helper callbacks did not finish after releasing storage")
		}
		select {
		case <-endDone:
		case <-time.After(wantWithin):
			t.Error("terminal owner did not finish after releasing storage")
		}
	})
	go func() {
		defer close(readerDone)
		up, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first")}, "")
		admitted <- admission{up: up, confirm: confirm}
		<-continueReader
		up, confirm = e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("second")}, "")
		admitted <- admission{up: up, confirm: confirm}
		go func() {
			defer close(endDone)
			e.ws.HelperSessionEnded(session.ID(sid))
		}()
		e.ws.PublishScreenFrame(other, 78, screen)
	}()

	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("first admitted row never reached the store")
	}
	select {
	case got := <-admitted:
		if got.confirm || got.up != 0 {
			t.Fatalf("first admission = (%d, %v), want deferred confirmation", got.up, got.confirm)
		}
	case <-time.After(wantWithin):
		t.Fatal("first row callback waited for storage")
	}

	// The first append is inside the store, not merely scheduled. Admit the
	// second row and report exit without any durability or idle wait.
	resumeReader()
	select {
	case got := <-admitted:
		if got.confirm || got.up != 0 {
			t.Fatalf("second admission = (%d, %v), want deferred confirmation", got.up, got.confirm)
		}
	case <-time.After(wantWithin):
		t.Fatal("second row callback waited for storage")
	}
	select {
	case <-readerDone:
	case <-time.After(wantWithin):
		t.Fatal("row reader did not hand exit to its separate terminal owner")
	}
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), func(msg []byte) bool {
		frame, decodeErr := DecodeFrame(msg)
		return decodeErr == nil && frame.MsgType == MsgTypeMetadata && string(frame.Payload) == string(screen)
	}); err != nil {
		t.Fatalf("another pane's screen waited behind terminal settlement: %v", err)
	}
	// Observe that terminal handling has entered its ownership interval:
	// the old implementation removes the source immediately, whereas a
	// draining terminal owner registers a waiter on the held flush. Neither
	// observation waits for the row drain to become idle.
	deadline := time.NewTimer(wantWithin)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		bs := e.ws.blockStream
		bs.mu.Lock()
		_, sourced := bs.sources[session.ID(sid)]
		waiting := bs.flushDone[session.ID(sid)].done != nil
		bs.mu.Unlock()
		if !sourced || waiting {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("terminal owner never observed the held row drain")
		}
	}
	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactOpen {
		t.Errorf("artifact state = %q with accepted rows still blocked in storage, want open", art.State)
	}
	select {
	case up := <-marks:
		t.Errorf("confirmed row %d before storage accepted either row", up)
	default:
	}

	release()
	closed := awaitBlockClosed(t, e)
	if closed.EntryID != attempt || !closed.Kept {
		t.Fatalf("block.closed = %+v, want entry %q kept after its admitted tail drains", closed, attempt)
	}
	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactSealed {
		t.Fatalf("artifact state = %q after block.closed, want sealed", art.State)
	}
	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].From != 0 || rows[0].Text != "first" || rows[1].From != 1 || rows[1].Text != "second" {
		t.Fatalf("sealed rows = %+v, want both admitted rows in order", rows)
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
	marks := attachConfirmedBlockRows(t, e.ws, session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make restart")
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("kept-0"), aStreamRow("kept-1")}, ""); confirm || written != 0 {
		t.Fatalf("first admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 2)
	awaitBlockRowsIdle(t, e.ws, session.ID(sid))

	e.ws.DetachBlockRows(session.ID(sid))

	// The coordinator comes back. No attempt fact is ingested here — the
	// lane is Desynchronized and its lifecycle events quarantined — yet
	// the attach alone must continue the open block from the store.
	marks = attachConfirmedBlockRows(t, e.ws, session.ID(sid))

	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{aStreamRow("kept-2"), aStreamRow("kept-3")}, ""); confirm || written != 0 {
		t.Fatalf("continued admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 4)
	awaitBlockRowsIdle(t, e.ws, session.ID(sid))
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
	marks := attachConfirmedBlockRows(t, e.ws, sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make race")
	if written, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("r0"), aStreamRow("r1")}, ""); confirm || written != 0 {
		t.Fatalf("row admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 2)
	awaitBlockRowsIdle(t, e.ws, sid)
	art := blockArtifact(t, db, attempt)
	e.ws.DetachBlockRows(sid)

	marks = attachConfirmedBlockRows(t, e.ws, sid)
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

	if written, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("r2")}, ""); confirm || written != 0 {
		t.Fatalf("tail admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 3)
}

// A NEW command started after the re-adopt owns the stream: the recovered
// block stays named in bs.open — the end its own interval still owes must
// still be able to close it — and never takes current from the later
// command's block.
func TestTheRebindDoesNotStealCurrentFromALaterBlock(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	marks := attachConfirmedBlockRows(t, e.ws, sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make first")
	if written, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("f0")}, ""); confirm || written != 0 {
		t.Fatalf("row admission = (%d, %v), want deferred confirmation", written, confirm)
	}
	awaitConfirmedBlockRows(t, marks, 1)
	awaitBlockRowsIdle(t, e.ws, sid)
	art := blockArtifact(t, db, attempt)
	e.ws.DetachBlockRows(sid)

	if err := e.ws.AttachBlockRows(t.Context(), sid, nil); err != nil {
		t.Fatalf("attach block rows: %v", err)
	}
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
