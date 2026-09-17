package app

// A worker's tab opens immediately after the tab that started it (nocx-tdiqs).
//
// WHAT THE OWNER SAW (2026-09-11, again 2026-09-17): a coordinator running in
// tab k calls workers.spawn, and the participant's new tab appears at the
// HEAD of the strip — left of every tab, the coordinator's included. The
// cause was one missing field: workerSpawner mints content.Tab{ID,
// WorkspaceID, Layout} with no Position, so the row was written at 0, and a
// strip draws in position order.
//
// WHERE THE SEAT IS DECIDED. Not here: content.CreateTabAfter places the tab
// and renumbers the strip it lands in, in the same transaction, because
// "immediately after" is a statement about the strip's order and the strip's
// order is the store's (ReorderTabs writes the same 0..n-1). What this file
// asserts through Spawn — and through the REAL content store, so these are
// the rows a restart reads back — is that the spawner asks for that placement
// at all, with the right anchor, and that it degrades in the one case where
// there is no anchor on the strip the tab is going into.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// placeTabs seeds one workspace's strip: each tab at the position its seat in
// the argument implies, each with one pane named after it.
//
// A NAMED WORKSPACE COMES WITH ITS FIRST TAB (CreateWorkspace is the only way
// one can be minted), and the default is the exception that proves it — it
// has no row until a tab needs it.
func placeTabs(t *testing.T, layout content.LayoutRepository, workspaceID string, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for seat, id := range ids {
		tab := content.Tab{ID: id, WorkspaceID: workspaceID, Position: seat, Layout: content.LayoutRow}
		pane := content.Pane{ID: paneOf(id), TabID: id, Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1}
		var err error
		if seat == 0 && workspaceID != content.DefaultWorkspaceID {
			_, err = layout.CreateWorkspace(ctx, content.Workspace{ID: workspaceID, Name: workspaceID}, tab, pane)
		} else {
			_, err = layout.CreateTab(ctx, tab, pane)
		}
		if err != nil {
			t.Fatalf("seed %s in %s: %v", id, workspaceID, err)
		}
	}
}

func paneOf(tabID string) string { return "pane-" + tabID }

// stripOf is the read a restart performs: one workspace's open tabs in
// position order, which is Tabs' own ORDER BY.
func stripOf(t *testing.T, layout content.LayoutRepository, workspaceID string) []content.Tab {
	t.Helper()
	tabs, err := layout.Tabs(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Tabs(%s): %v", workspaceID, err)
	}
	return tabs
}

func idsOf(t *testing.T, tabs []content.Tab) []string {
	t.Helper()
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		out = append(out, tab.ID)
	}
	return out
}

// openCoordinator opens a real session standing in for a coordinator's own
// pane — the walk Spawn performs (session → pane → tab) then runs for real
// rather than against an id the test handed it.
func (w *workerStand) openCoordinator(t *testing.T, paneID string) session.ID {
	t.Helper()
	sess, err := w.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: paneID,
	})
	if err != nil {
		t.Fatalf("open the coordinator's session: %v", err)
	}
	return sess.ID()
}

// paneOfSession answers which pane the participant's own session opened in —
// the fact that ties one seat in the strip to this spawn rather than to a
// neighbour the test happened to name.
func (w *workerStand) paneOfSession(t *testing.T, sid string) string {
	t.Helper()
	sess, err := w.reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("the participant's session is not in the registry: %v", err)
	}
	return sess.PaneID()
}

// registerFrom runs one registration the way workers.spawn does — through the
// record — from a CHOSEN coordinator session, which is the one thing the
// shared helper pins to a name no session in the registry answers to.
//
// The enrolment is supplied as soon as the participant's own session appears,
// waited on as a state and never as a duration: a registration that needed a
// sleep to be observed would be one whose ordering is not actually
// guaranteed.
func (w *workerStand) registerFrom(t *testing.T, group workers.ID, coordinator session.ID) workers.Participant {
	t.Helper()
	ctx := context.Background()
	w.ensureWorker(t, group, string(coordinator))

	before := map[string]bool{}
	for _, s := range w.reg.List() {
		before[string(s.ID())] = true
	}

	type outcome struct {
		p   workers.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		reg, err := w.record.Register(ctx, workers.RegisterRequest{
			Group: group, CoordinatorSession: string(coordinator),
			Role: workers.RoleWorker, Command: "claude",
		})
		done <- outcome{reg.Participant, err}
	}()

	var sid session.ID
	waittest.WaitFor(t, "the participant's own session to exist", func() bool {
		for _, s := range w.reg.List() {
			if !before[string(s.ID())] {
				sid = s.ID()
				return true
			}
		}
		return false
	})
	w.lanes.register(lifecycle.LaneID("lane-"+string(group)), string(sid))
	w.enrol.enrolled(sid, "lane-"+string(group))

	got := <-done
	if got.err != nil {
		t.Fatalf("register: %v", got.err)
	}
	return got.p
}

// Criterion: after a spawn from a coordinator in tab k, the stored order is
// [..., k, worker, k+1, ...] — asserted against the content store, which is
// what a restart reads back.
func TestAWorkersTabOpensImmediatelyAfterItsCoordinatorsTab(t *testing.T) {
	stand := newWorkerStand(t)
	layout := stand.db.Layout()
	// Production places a participant's tab in the default workspace
	// (app.go's workerSpawner.workspace), so the coordinator's tab is in the
	// same strip as the one about to be minted.
	placeTabs(t, layout, content.DefaultWorkspaceID, "tab-1", "tab-2", "tab-3")

	coordinator := stand.openCoordinator(t, paneOf("tab-2"))
	participant := stand.registerFrom(t, "worker-placement", coordinator)

	stored := stripOf(t, layout, content.DefaultWorkspaceID)
	if len(stored) != 4 {
		t.Fatalf("the strip holds %v, want four tabs", idsOf(t, stored))
	}
	if stored[0].ID != "tab-1" || stored[1].ID != "tab-2" || stored[3].ID != "tab-3" {
		t.Fatalf("the strip is %v, want the participant's tab between tab-2 and tab-3", idsOf(t, stored))
	}
	// AND THE SEAT IS THE PARTICIPANT'S TAB, not a neighbour's: the tab that
	// holds the pane this participant's own session opened in is the one that
	// landed at seat 2.
	held, err := layout.TabForPane(context.Background(), stand.paneOfSession(t, participant.Liveness.SessionID))
	if err != nil {
		t.Fatalf("which tab holds the participant's pane: %v", err)
	}
	if stored[2].ID != held {
		t.Fatalf("the tab at seat 2 is %s, but the participant's pane is in %s", stored[2].ID, held)
	}
	// Every tab still has one seat of its own, 0..n-1 — the order a strip
	// draws in and the one ReorderTabs writes.
	for seat, tab := range stored {
		if tab.Position != seat {
			t.Fatalf("the strip's rows are %+v, want positions 0..3", stored)
		}
	}
}

// Criterion: tabs in other workspaces keep their positions. A strip is one
// workspace's and a placement in it renumbers nobody else's.
func TestAWorkersTabLeavesEveryOtherWorkspaceAlone(t *testing.T) {
	stand := newWorkerStand(t)
	layout := stand.db.Layout()
	placeTabs(t, layout, content.DefaultWorkspaceID, "tab-1", "tab-2", "tab-3")
	placeTabs(t, layout, "ws-elsewhere", "tab-x", "tab-y")
	before := stripOf(t, layout, "ws-elsewhere")

	coordinator := stand.openCoordinator(t, paneOf("tab-2"))
	stand.registerFrom(t, "worker-placement", coordinator)

	after := stripOf(t, layout, "ws-elsewhere")
	if len(after) != len(before) {
		t.Fatalf("another workspace's strip is %v, want %v", idsOf(t, after), idsOf(t, before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Position != before[i].Position {
			t.Fatalf("row %d is %+v, want %+v — a placement moved another workspace",
				i, after[i], before[i])
		}
	}
}

// A coordinator whose tab is in ANOTHER workspace names no seat on the strip
// the participant's tab is going into, and the spawn still succeeds: the tab
// goes last rather than taking a seat that belongs to somebody else — and
// never the head, which is where the defect this file was written from left
// it.
func TestAWorkersTabGoesLastWhenItsCoordinatorIsElsewhere(t *testing.T) {
	stand := newWorkerStand(t)
	layout := stand.db.Layout()
	placeTabs(t, layout, content.DefaultWorkspaceID, "tab-1", "tab-2")
	placeTabs(t, layout, "ws-elsewhere", "tab-x")

	coordinator := stand.openCoordinator(t, paneOf("tab-x"))
	stand.registerFrom(t, "worker-placement", coordinator)

	stored := stripOf(t, layout, content.DefaultWorkspaceID)
	if len(stored) != 3 {
		t.Fatalf("the strip is %v, want three tabs", idsOf(t, stored))
	}
	if stored[0].ID != "tab-1" || stored[1].ID != "tab-2" {
		t.Fatalf("the strip is %v, want the existing order kept", idsOf(t, stored))
	}
	if stored[2].ID == "tab-1" || stored[2].ID == "tab-2" {
		t.Fatalf("no participant's tab is in the strip at all: %v", idsOf(t, stored))
	}
}

// Criterion (testing rule 3): when the layout refuses the create, the spawn
// fails with an error naming it, no session is opened and nothing is
// compensated — there is nothing to compensate, because the tab was never
// minted. What is NOT true after this: a tab at any position, a session
// holding a pane nobody recorded, or a deletion aimed at a tab that does not
// exist.
func TestARefusedTabPlacementFailsTheSpawnBeforeAnySessionExists(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.tabs.createErr = errors.New("the content store is closed")
	coordinator := openCoordinatorPane(t, stand, "coord-pane")
	stand.tabs.tabOf = map[string]string{"coord-pane": "coord-tab"}

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant:        "p-refused",
		Group:              "worker-1",
		CoordinatorSession: string(coordinator),
		Command:            "run-agent",
	})
	if err == nil {
		t.Fatal("Spawn succeeded although the layout refused the participant's tab")
	}
	if !strings.Contains(err.Error(), "minting the participant's tab") {
		t.Fatalf("error %q does not name the tab that could not be minted", err)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		t.Fatalf("a session was opened for a tab that was never recorded: %s", sid)
	}
	created, deleted := stand.tabs.snapshot()
	if len(created) != 0 || len(deleted) != 0 {
		t.Fatalf("created=%v deleted=%v, want nothing minted and nothing compensated", created, deleted)
	}
	if anchors := stand.tabs.anchorsAsked(); len(anchors) != 1 || anchors[0] != "coord-tab" {
		t.Fatalf("the placement asked for anchor %v, want the coordinator's tab once", anchors)
	}
}
