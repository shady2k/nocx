package content_test

// A clear boundary (nocx-2v80t.3.17): the record never deletes
// (nocx-zg3k3.10.3's decision), so these assert the CURSOR — recorded once,
// applied by an ordinary pane-scoped read — rather than any mutation of the
// entries it bounds. Every entry this file records stays retrievable by id
// throughout, which is the paired half of "the earlier block is not shown,
// and it is still in the store" the bead's acceptance criterion asks for.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// finishOne carries entryID from Submit through StartExecution and
// FinishExecution to a closed, successful entry — the shape a real command's
// completion leaves, and the one RecordClearBoundary's own boundary excludes
// the still-running command by relying on (see ledger_clear_boundary.go's
// header comment).
func finishOne(t *testing.T, led content.LedgerRepository, entryID string) {
	t.Helper()
	ctx := context.Background()
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution(%s): %v", entryID, err)
	}
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1, TerminationReason: content.TermCompleted, Status: content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution(%s): %v", entryID, err)
	}
}

// submitInPane submits one entry anchored to paneID and sessionID, closed or
// still running depending on the caller — Submit alone leaves it open.
func submitInPane(t *testing.T, led content.LedgerRepository, id, paneID, sessionID, intent string) string {
	t.Helper()
	if _, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr(paneID), SessionID: strPtr(sessionID),
		Cwd: "/repo", Kind: content.EntryShell, Intent: intent,
	}); err != nil {
		t.Fatalf("Submit(%s): %v", id, err)
	}
	return id
}

// TestRecordClearBoundary_ExcludesTheStillRunningCommand is the boundary this
// file's header argues for: the erase is sighted INSIDE the `clear` command's
// own interval, and that command's entry already exists (Submit opened it)
// by the time the erase runs. A boundary naming the pane's newest ingest_seq
// outright would hide the very command reporting the clear; scoping to
// phase='closed' excludes it structurally, with no id comparison at all.
func TestRecordClearBoundary_ExcludesTheStillRunningCommand(t *testing.T) {
	ctx := context.Background()
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")
	const sessionID = "sess-1"
	if err := led.CreateSession(ctx, content.Session{ID: sessionID, WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	older := submitInPane(t, led, "00000000-0000-7000-8000-00000000c001", "pane-1", sessionID, "ls")
	finishOne(t, led, older)
	newer := submitInPane(t, led, "00000000-0000-7000-8000-00000000c002", "pane-1", sessionID, "pwd")
	finishOne(t, led, newer)
	// `clear` itself: submitted, running, NOT yet finished — the entry the
	// boundary must not bound.
	running := submitInPane(t, led, "00000000-0000-7000-8000-00000000c003", "pane-1", sessionID, "clear")

	newerEntry, err := led.Entry(ctx, newer)
	if err != nil || newerEntry == nil {
		t.Fatalf("Entry(%s): %+v, %v", newer, newerEntry, err)
	}

	rec, err := led.RecordClearBoundary(ctx, content.RecordClearBoundary{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RecordClearBoundary: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("RecordClearBoundary returned no id")
	}
	if rec.PaneID == nil || *rec.PaneID != "pane-1" {
		t.Fatalf("PaneID = %v, want pane-1", rec.PaneID)
	}
	if rec.IngestSeq != newerEntry.IngestSeq {
		t.Fatalf("IngestSeq = %d, want the newest CLOSED entry's %d (excluding the running `clear`)",
			rec.IngestSeq, newerEntry.IngestSeq)
	}

	page := entriesForPane(t, led, "pane-1")
	got := map[string]bool{}
	for _, e := range page.Entries {
		got[e.ID] = true
	}
	if got[older] || got[newer] {
		t.Fatalf("QueryEntries(pane-1) = %+v, want %s and %s hidden by the boundary", page.Entries, older, newer)
	}
	if !got[running] {
		t.Fatalf("QueryEntries(pane-1) = %+v, want the still-running `clear` visible: the boundary must not hide the command that produced it", page.Entries)
	}

	// The record never deletes (nocx-zg3k3.10.3): both hidden entries are
	// still in the store, by id, exactly as they were.
	for _, id := range []string{older, newer} {
		e, err := led.Entry(ctx, id)
		if err != nil || e == nil {
			t.Fatalf("Entry(%s) after the boundary = %+v, %v, want it still readable", id, e, err)
		}
	}

	// A command run AFTER the boundary is not hidden by it.
	finishOne(t, led, running)
	after := submitInPane(t, led, "00000000-0000-7000-8000-00000000c004", "pane-1", sessionID, "date")
	finishOne(t, led, after)
	page2 := entriesForPane(t, led, "pane-1")
	got2 := map[string]bool{}
	for _, e := range page2.Entries {
		got2[e.ID] = true
	}
	if !got2[running] || !got2[after] {
		t.Fatalf("QueryEntries(pane-1) after more commands = %+v, want %s and %s visible", page2.Entries, running, after)
	}
	if got2[older] || got2[newer] {
		t.Fatalf("QueryEntries(pane-1) after more commands = %+v, want the boundary still standing over %s and %s",
			page2.Entries, older, newer)
	}
}

// TestRecordClearBoundary_NoPaneIsProvenanceOnly: a session with no entry at
// all (an erase before anything ever ran in it) resolves no pane. The
// boundary is still recorded — every sighting is a real event — with nothing
// yet to apply it against, and recording it must not fail the session.
func TestRecordClearBoundary_NoPaneIsProvenanceOnly(t *testing.T) {
	ctx := context.Background()
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	if err := led.CreateSession(ctx, content.Session{ID: "sess-empty", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rec, err := led.RecordClearBoundary(ctx, content.RecordClearBoundary{SessionID: "sess-empty"})
	if err != nil {
		t.Fatalf("RecordClearBoundary: %v", err)
	}
	if rec.PaneID != nil {
		t.Fatalf("PaneID = %v, want nil: this session never anchored a pane", rec.PaneID)
	}
	if rec.IngestSeq != 0 {
		t.Fatalf("IngestSeq = %d, want 0: nothing sealed to bound", rec.IngestSeq)
	}
}

// TestRecordClearBoundary_RequiresASessionID: the required-field refusal
// every write path in this package gives, so a caller wiring this up wrong
// gets an error rather than a boundary nobody can ever apply.
func TestRecordClearBoundary_RequiresASessionID(t *testing.T) {
	_, led := newLedger(t)
	if _, err := led.RecordClearBoundary(context.Background(), content.RecordClearBoundary{}); err == nil {
		t.Fatal("RecordClearBoundary with no session id: want an error, got nil")
	}
}
