package content_test

// A tab or a pane that leaves the window is MARKED CLOSED, never deleted
// (nocx-l21ib.4). The two facts this file exists to keep apart:
//
//	the WINDOW SET   the rows with closed_at IS NULL — what the strip draws
//	the STORE        every row ever written, closed ones included
//
// Before this, an ordinary Cmd-W issued a DELETE, and entries.pane_id is
// ON DELETE SET NULL — so every close permanently unhooked that pane's blocks
// from the pane they ran in. The assertions below are therefore mostly about
// what is STILL there after a close, which is the half a test written against
// the old behaviour could not have.
//
// A workspace is still DELETED once its last open tab has gone; that is the
// one row in the chain that does not survive, and its closed tabs outlive it
// with a null workspace_id — the same "provenance lost" shape tabs.parent_id
// has always had.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// aBlockIn records one entry anchored to a pane — the block a close must not
// unhook. It goes through the ledger seam, so the anchor is written the way
// the product writes it.
func aBlockIn(t *testing.T, led content.LedgerRepository, id, paneID string) {
	t.Helper()
	if _, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr(paneID),
		Cwd:    "/repo", Kind: content.EntryShell, Intent: "make ci",
	}); err != nil {
		t.Fatalf("Submit %s: %v", id, err)
	}
}

// The whole bug in one test: close a tab, and the blocks its pane printed are
// still findable BY THAT PANE — across a restart, which is the interval a
// restore actually spans. The closed tab's workspace is deleted here too (it
// held no other open tab), so this also says the delete no longer cascades
// through the tab to the pane and out to the block's anchor.
func TestAClosedTabKeepsItsPanesAndTheBlocksAnchoredToThem(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	layout := db.Layout()
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	// A second chain, so this close is not also the application's last tab —
	// the replacement is a different rule with its own tests.
	aPaneUnder(t, db, "ws-2", "tab-2", "pane-2")
	envReady(t, led, "local")
	const block = "00000000-0000-7000-8000-0000000c1001"
	aBlockIn(t, led, block, "pane-1")

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })

	page := entriesForPane(t, again.Ledger(), "pane-1")
	if len(page.Entries) != 1 || page.Entries[0].ID != block {
		t.Fatalf("blocks of the closed tab's pane = %+v, want the one entry %s — the close unhooked them",
			page.Entries, block)
	}
	// And the anchor is read off the row itself, not merely inferred from a
	// query that filtered on it: the column is what the delete used to null.
	entry, err := again.Ledger().Entry(ctx, block)
	if err != nil || entry == nil {
		t.Fatalf("Entry after the close: %+v, %v", entry, err)
	}
	if entry.PaneID == nil || *entry.PaneID != "pane-1" {
		t.Fatalf("paneId of the surviving block = %v, want pane-1 — entries.pane_id was nulled by the close",
			entry.PaneID)
	}
}

// The other half of the same fact, one rung down: closing a PANE (the last
// one in its tab, so the tab dissolves with it) leaves its blocks anchored.
func TestAClosedPaneKeepsTheBlocksAnchoredToIt(t *testing.T) {
	ctx := context.Background()
	db, led, _ := newLedgerAt(t)
	layout := db.Layout()
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	aPaneUnder(t, db, "ws-2", "tab-2", "pane-2")
	envReady(t, led, "local")
	const block = "00000000-0000-7000-8000-0000000c2001"
	aBlockIn(t, led, block, "pane-1")

	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}

	if page := entriesForPane(t, led, "pane-1"); len(page.Entries) != 1 {
		t.Fatalf("blocks of the closed pane = %+v, want the one entry still anchored to it", page.Entries)
	}
	entry, err := led.Entry(ctx, block)
	if err != nil || entry == nil || entry.PaneID == nil || *entry.PaneID != "pane-1" {
		t.Fatalf("the block after its pane closed = %+v (err %v), want one still naming pane-1", entry, err)
	}
}

// A closed tab is not in the window set: neither the workspace's tab list nor
// the snapshot the renderer draws itself from carries it. That read IS the
// window — a row the store keeps and this read returns would be a tab the
// strip draws after the user closed it.
func TestAClosedTabIsAbsentFromTheWindowRead(t *testing.T) {
	ctx := context.Background()
	_, layout := newLayout(t)
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-2", "/srv")); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-2" {
		t.Fatalf("tabs in the window = %v, want tab-2 alone", got)
	}
	if panes, err := layout.Panes(ctx, "tab-1"); err != nil || len(panes) != 0 {
		t.Fatalf("panes of the closed tab = %+v (err %v), want none in the window", panes, err)
	}
	snap, err := layout.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, tab := range snap.Tabs {
		if tab.ID == "tab-1" {
			t.Fatalf("snapshot carries the closed tab-1: %+v", snap.Tabs)
		}
	}
	for _, pane := range snap.Panes {
		if pane.ID == "pane-1" {
			t.Fatalf("snapshot carries the closed pane-1: %+v", snap.Panes)
		}
	}
}

// A reorder names the tabs the user can see, so the membership it is checked
// against must be the window set too. With closed rows counted, the first
// reorder after any close is refused as "not a permutation" — the whole strip
// stops being draggable the moment somebody closes a tab.
func TestAReorderAfterACloseNamesOnlyTheOpenTabs(t *testing.T) {
	ctx := context.Background()
	_, layout := newLayout(t)
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-2", "/srv")); err != nil {
		t.Fatalf("CreateTab tab-2: %v", err)
	}
	if _, err := layout.CreateTab(ctx, aTab("tab-3", "ws-1"), aPane("pane-3", "tab-3", "/srv")); err != nil {
		t.Fatalf("CreateTab tab-3: %v", err)
	}
	if err := layout.DeleteTab(ctx, "tab-2", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if _, err := layout.ReorderTabs(ctx, "ws-1", []string{"tab-3", "tab-1"}); err != nil {
		t.Fatalf("ReorderTabs over the open tabs: %v", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 2 || got[0] != "tab-3" || got[1] != "tab-1" {
		t.Fatalf("tabs after the reorder = %v, want tab-3 then tab-1", got)
	}
}

// A pane is not dragged into a tab that is no longer in the window, and a
// closed pane is not dragged anywhere: both are rows nothing on screen can
// pick up, so the move is refused rather than half-applied against them.
func TestAMoveNamesOnlyRowsThatAreInTheWindow(t *testing.T) {
	ctx := context.Background()
	_, layout := newLayout(t)
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-2", "/srv")); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if _, err := layout.CreatePane(ctx, aPane("pane-2b", "tab-2", "/srv")); err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}

	// tab-1 dissolved with its last pane; pane-1 is closed. Neither may be
	// named by a move.
	if _, err := layout.MovePane(ctx, "pane-1", "tab-2"); err == nil {
		t.Fatal("MovePane of a closed pane: err = nil, want a refusal")
	}
	if _, err := layout.MovePane(ctx, "pane-2", "tab-1"); err == nil {
		t.Fatal("MovePane into a closed tab: err = nil, want a refusal")
	}
}

// A pane is not added to a tab that has left the window either. The refusal
// is the one the store already gave when the row was deleted outright, kept
// across the change: an in-flight split for a tab the user has just closed
// must not land an open pane under a closed tab.
func TestASplitIsRefusedForATabThatHasLeftTheWindow(t *testing.T) {
	ctx := context.Background()
	_, layout := newLayout(t)
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")
	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if _, err := layout.CreatePane(ctx, aPane("pane-late", "tab-1", "/srv")); err == nil {
		t.Fatal("CreatePane under a closed tab: err = nil, want a refusal")
	}
}

// Decoration is a window act — the user renames, recolours or pins a tab they
// are looking at — so a tab that has left is refused, which is the answer the
// renderer already got when a close deleted the row. It also has to be: a
// closed tab may have outlived its workspace, and the wire says a tab's
// workspaceId is never empty, so answering with one would put a shape on the
// socket that the contract forbids.
func TestDecoratingATabThatHasLeftTheWindowIsRefused(t *testing.T) {
	ctx := context.Background()
	_, layout := newLayout(t)
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")
	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if _, err := layout.RenameTab(ctx, "tab-1", str("late")); !errors.Is(err, content.ErrNoSuchTab) {
		t.Fatalf("RenameTab on a closed tab: err = %v, want ErrNoSuchTab", err)
	}
	if _, err := layout.RecolourTab(ctx, "tab-1", str("#ff0000")); !errors.Is(err, content.ErrNoSuchTab) {
		t.Fatalf("RecolourTab on a closed tab: err = %v, want ErrNoSuchTab", err)
	}
	if _, err := layout.PinTab(ctx, "tab-1", true); !errors.Is(err, content.ErrNoSuchTab) {
		t.Fatalf("PinTab on a closed tab: err = %v, want ErrNoSuchTab", err)
	}
}

// ClearWindow is the CLEAN START (restore.onStartup off): everything the last
// session left is marked closed in one act, so what the chain holds as OPEN
// is always the last session. Before it, the leftovers were merely hidden by
// the renderer, and every clean start piled another session's rows on the
// ones before it — turning restore back on reopened the session before last.
func TestClearWindowClosesEveryLeftoverAndKeepsWhatComesAfter(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	layout := db.Layout()
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	aPaneUnder(t, db, "ws-2", "tab-2", "pane-2")
	envReady(t, led, "local")
	const block = "00000000-0000-7000-8000-0000000c3001"
	aBlockIn(t, led, block, "pane-1")

	if err := layout.ClearWindow(ctx); err != nil {
		t.Fatalf("ClearWindow: %v", err)
	}

	snap, snapErr := layout.Snapshot(ctx)
	if snapErr != nil {
		t.Fatalf("Snapshot: %v", snapErr)
	}
	if len(snap.Tabs) != 0 || len(snap.Panes) != 0 {
		t.Fatalf("window after a clean start = %d tabs, %d panes, want none", len(snap.Tabs), len(snap.Panes))
	}
	// The blocks are the point: a clean start is a decision about what the
	// window opens on, never an instruction to forget.
	if page := entriesForPane(t, led, "pane-1"); len(page.Entries) != 1 {
		t.Fatalf("blocks of a swept pane = %+v, want the one entry still anchored", page.Entries)
	}

	// The session that follows is the one the chain now holds, and a restart
	// reads back exactly it.
	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-3", Name: "clean"},
		aTab("tab-3", "ws-3"), aPane("pane-3", "tab-3", "/srv")); err != nil {
		t.Fatalf("CreateWorkspace after the sweep: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, reopenErr := reopenStore(t, path)
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	t.Cleanup(func() { _ = again.Close() })
	back, backErr := again.Layout().Snapshot(ctx)
	if backErr != nil {
		t.Fatalf("Snapshot after reopen: %v", backErr)
	}
	if len(back.Tabs) != 1 || back.Tabs[0].ID != "tab-3" {
		t.Fatalf("tabs after the restart = %+v, want tab-3 alone — the leftovers came back", back.Tabs)
	}
}

func TestSandboxGrantRollbackAllowsRetryAfterLaunchFailure(t *testing.T) {
	ctx := t.Context()
	db, _, _ := newLedgerAt(t)
	layout := db.Layout()

	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-sandbox-retry", Name: "sandbox retry"},
		aTab("tab-sandbox-retry", "ws-sandbox-retry"),
		aPane("pane-sandbox-retry", "tab-sandbox-retry", "/workspace")); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	grant := content.SandboxGrant{
		PaneID: "pane-sandbox-retry", Version: 1, IssuedAt: 42,
		Workspace: "/workspace", Payload: `{}`,
	}
	if err := layout.InsertSandboxGrant(ctx, grant); err != nil {
		t.Fatalf("InsertSandboxGrant: %v", err)
	}
	if err := layout.InsertSandboxGrant(ctx, grant); !errors.Is(err, content.ErrSandboxGrantExists) {
		t.Fatalf("duplicate InsertSandboxGrant error = %v, want ErrSandboxGrantExists", err)
	}
	if err := layout.RemoveSandboxGrant(ctx, grant.PaneID); err != nil {
		t.Fatalf("RemoveSandboxGrant: %v", err)
	}
	if granted, err := layout.SandboxGrantExists(ctx, grant.PaneID); err != nil || granted {
		t.Fatalf("SandboxGrantExists after rollback = %v, %v; want false, nil", granted, err)
	}
	if err := layout.InsertSandboxGrant(ctx, grant); err != nil {
		t.Fatalf("retry InsertSandboxGrant: %v", err)
	}
}

// A granted pane is recorded so its blocks and workspace provenance have a
// durable anchor while it is alive, but its authority cannot be silently
// re-issued in the next backend incarnation.
func TestCloseSandboxPanesLeavesOnlyRestorablePanesInTheWindow(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	layout := db.Layout()

	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-sandbox", Name: "sandbox"},
		aTab("tab-sandbox", "ws-sandbox"),
		aPane("pane-sandbox", "tab-sandbox", "/workspace")); err != nil {
		t.Fatalf("CreateWorkspace sandbox: %v", err)
	}
	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-ordinary", Name: "ordinary"},
		aTab("tab-ordinary", "ws-ordinary"),
		aPane("pane-ordinary", "tab-ordinary", "/repo")); err != nil {
		t.Fatalf("CreateWorkspace ordinary: %v", err)
	}
	if err := layout.InsertSandboxGrant(ctx, content.SandboxGrant{
		PaneID: "pane-sandbox", Version: 1, IssuedAt: 42,
		Workspace: "/workspace", Payload: `{"writableRoots":["/workspace"]}`,
	}); err != nil {
		t.Fatalf("InsertSandboxGrant: %v", err)
	}
	granted, queryErr := layout.SandboxGrantExists(ctx, "pane-sandbox")
	if queryErr != nil || !granted {
		t.Fatalf("SandboxGrantExists(sandbox) = %v, %v; want true, nil", granted, queryErr)
	}
	granted, queryErr = layout.SandboxGrantExists(ctx, "pane-ordinary")
	if queryErr != nil || granted {
		t.Fatalf("SandboxGrantExists(ordinary) = %v, %v; want false, nil", granted, queryErr)
	}

	envReady(t, led, "local")
	const block = "00000000-0000-7000-8000-0000000c4001"
	aBlockIn(t, led, block, "pane-sandbox")

	if closeErr := layout.CloseSandboxPanes(ctx); closeErr != nil {
		t.Fatalf("CloseSandboxPanes: %v", closeErr)
	}
	snap, snapshotErr := layout.Snapshot(ctx)
	if snapshotErr != nil {
		t.Fatalf("Snapshot: %v", snapshotErr)
	}
	if len(snap.Panes) != 1 || snap.Panes[0].ID != "pane-ordinary" {
		t.Fatalf("window panes = %+v, want pane-ordinary alone", snap.Panes)
	}
	if _, closedErr := layout.SandboxGrantExists(ctx, "pane-sandbox"); !errors.Is(closedErr, content.ErrNoSuchPane) {
		t.Fatalf("SandboxGrantExists(closed sandbox) error = %v, want ErrNoSuchPane", closedErr)
	}
	if page := entriesForPane(t, led, "pane-sandbox"); len(page.Entries) != 1 {
		t.Fatalf("sandbox blocks after sweep = %+v, want the anchored entry", page.Entries)
	}

	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	again, reopenErr := reopenStore(t, path)
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	t.Cleanup(func() { _ = again.Close() })
	back, snapshotErr := again.Layout().Snapshot(ctx)
	if snapshotErr != nil {
		t.Fatalf("Snapshot after reopen: %v", snapshotErr)
	}
	if len(back.Panes) != 1 || back.Panes[0].ID != "pane-ordinary" {
		t.Fatalf("panes after restart = %+v, want pane-ordinary alone", back.Panes)
	}
}
