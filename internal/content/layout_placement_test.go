package content_test

// Where a tab minted "immediately after another one" lands (nocx-tdiqs).
//
// The defect these tests were written from: workers.spawn minted its
// participant's tab with no position at all, so the row was written at 0 and
// the new tab sorted to the HEAD of the strip — left of every tab, including
// the coordinator's that started it. The renderer's own create writes a
// position (an absolute one it computes), so the store had never needed a
// placement rule; this is the one that closes the gap, and it is the store's
// because the strip's order is: a workspace's open tabs are 0..n-1 in the
// order they are drawn, which ReorderTabs already wrote.
//
// The four properties asserted here are the four a caller can get wrong:
// the seat itself, the neighbours that have to move for it, the tabs that
// must NOT move (every other workspace's), and what is true on disk when the
// write fails half way.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// aStripOfThree seeds one workspace holding tab-1, tab-2 and tab-3 at
// positions 0, 1 and 2 — the dense order ReorderTabs writes, and the one a
// person's strip always has.
func aStripOfThree(t *testing.T, layout content.LayoutRepository) {
	t.Helper()
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	for i, id := range []string{"tab-2", "tab-3"} {
		if _, err := layout.CreateTab(ctx,
			content.Tab{ID: id, WorkspaceID: "ws-1", Position: i + 1, Layout: content.LayoutRow},
			aPane("pane-"+id[4:], id, "/srv"),
		); err != nil {
			t.Fatalf("CreateTab %s: %v", id, err)
		}
	}
}

// seatOf answers where one tab sits in its workspace's strip, which is what a
// strip draws from and what a restart reads back (Tabs' own ORDER BY
// position, id).
func seatOf(t *testing.T, layout content.LayoutRepository, workspaceID, tabID string) int {
	t.Helper()
	for _, tab := range aStrip(t, layout, workspaceID) {
		if tab.ID == tabID {
			return tab.Position
		}
	}
	t.Fatalf("%s is not in %s's strip", tabID, workspaceID)
	return -1
}

func aStrip(t *testing.T, layout content.LayoutRepository, workspaceID string) []content.Tab {
	t.Helper()
	tabs, err := layout.Tabs(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Tabs(%s): %v", workspaceID, err)
	}
	return tabs
}

// Criterion: a tab created after another sits immediately after it, and the
// tabs that were there keep their relative order around it.
func TestATabCreatedAfterAnotherSitsImmediatelyAfterIt(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	if _, err := layout.CreateTabAfter(context.Background(),
		content.Tab{ID: "tab-worker", WorkspaceID: "ws-1", Layout: content.LayoutRow},
		aPane("pane-worker", "tab-worker", "/srv"),
		"tab-2",
	); err != nil {
		t.Fatalf("CreateTabAfter: %v", err)
	}

	got := tabIDs(t, layout, "ws-1")
	want := []string{"tab-1", "tab-2", "tab-worker", "tab-3"}
	if len(got) != len(want) {
		t.Fatalf("the strip holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the strip is %v, want %v", got, want)
		}
	}
}

// Criterion: nothing ends up sharing a seat. The neighbours moved for the new
// tab, and every position in the strip is still held exactly once, 0..n-1 —
// the invariant ReorderTabs writes and the one a stable sort falls back on.
func TestPlacingATabGivesEveryTabOneSeatOfItsOwn(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	if _, err := layout.CreateTabAfter(context.Background(),
		content.Tab{ID: "tab-worker", WorkspaceID: "ws-1", Layout: content.LayoutRow},
		aPane("pane-worker", "tab-worker", "/srv"),
		"tab-1",
	); err != nil {
		t.Fatalf("CreateTabAfter: %v", err)
	}

	strip := aStrip(t, layout, "ws-1")
	seats := map[int]string{}
	for _, tab := range strip {
		if other, taken := seats[tab.Position]; taken {
			t.Fatalf("%s and %s are both at position %d", other, tab.ID, tab.Position)
		}
		seats[tab.Position] = tab.ID
	}
	for i, tab := range strip {
		if tab.Position != i {
			t.Fatalf("the strip's rows are %+v, want positions 0..%d in order", strip, len(strip)-1)
		}
	}
}

// Criterion: tabs in other workspaces keep their positions. A strip is one
// workspace's, and a placement in it must not renumber anybody else's.
func TestPlacingATabLeavesEveryOtherWorkspaceAlone(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	aStripOfThree(t, layout)
	seedWorkspace(t, layout, "ws-2", "tab-other", "pane-other")

	if _, err := layout.CreateTabAfter(ctx,
		content.Tab{ID: "tab-worker", WorkspaceID: "ws-1", Layout: content.LayoutRow},
		aPane("pane-worker", "tab-worker", "/srv"),
		"tab-2",
	); err != nil {
		t.Fatalf("CreateTabAfter: %v", err)
	}

	if got := seatOf(t, layout, "ws-2", "tab-other"); got != 0 {
		t.Fatalf("a tab in another workspace moved to position %d, want 0", got)
	}
	if got := tabIDs(t, layout, "ws-2"); len(got) != 1 || got[0] != "tab-other" {
		t.Fatalf("the other workspace's strip is %v, want [tab-other]", got)
	}
}

// Criterion: a tab with no anchor to sit after still lands somewhere sane.
// "Last" is the answer, and it is the store's rather than the caller's: a
// tab whose position is left at zero is the defect this file exists for.
func TestATabWithNoAnchorGoesLast(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	if _, err := layout.CreateTabAfter(context.Background(),
		content.Tab{ID: "tab-worker", WorkspaceID: "ws-1", Layout: content.LayoutRow},
		aPane("pane-worker", "tab-worker", "/srv"),
		"",
	); err != nil {
		t.Fatalf("CreateTabAfter with no anchor: %v", err)
	}
	if got := seatOf(t, layout, "ws-1", "tab-worker"); got != 3 {
		t.Fatalf("the anchorless tab sits at %d, want 3 (the end of the strip)", got)
	}
}

// An anchor that is not on this strip — an id nobody knows, or another
// workspace's tab — is not an error worth refusing a create over. The tab
// goes last, which is the same answer as no anchor at all.
func TestAnAnchorOutsideTheStripPutsTheNewTabLast(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)
	seedWorkspace(t, layout, "ws-2", "tab-other", "pane-other")

	for _, anchor := range []string{"tab-nobody-made", "tab-other"} {
		t.Run(anchor, func(t *testing.T) {
			id := "tab-" + anchor
			if _, err := layout.CreateTabAfter(context.Background(),
				content.Tab{ID: id, WorkspaceID: "ws-1", Layout: content.LayoutRow},
				aPane("pane-"+anchor, id, "/srv"),
				anchor,
			); err != nil {
				t.Fatalf("CreateTabAfter anchored on %s: %v", anchor, err)
			}
			strip := aStrip(t, layout, "ws-1")
			if strip[len(strip)-1].ID != id {
				t.Fatalf("the strip is %v, want %s last", tabIDs(t, layout, "ws-1"), id)
			}
			if strip[0].ID != "tab-1" || strip[1].ID != "tab-2" || strip[2].ID != "tab-3" {
				t.Fatalf("the tabs already there moved: %v", tabIDs(t, layout, "ws-1"))
			}
		})
	}
}

// Criterion (testing rule 3): when the write fails, the strip is exactly what
// it was. The insert and the renumbering are ONE transaction, so a failure
// anywhere in it leaves no tab, no moved neighbour and nothing at two
// positions — the state a reader finds on disk is the state before the call.
//
// The failure is induced the way a real one arrives: the tab row lands, and
// the pane insert under it is refused because that pane id already means
// something else. What it must not do is leave the tab half-created.
func TestAFailedPlacementLeavesTheStripExactlyAsItWas(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)
	before := aStrip(t, layout, "ws-1")

	// pane-1 already exists, under tab-1: an insert on it is a constraint
	// failure, one statement after the tab row was written.
	_, err := layout.CreateTabAfter(context.Background(),
		content.Tab{ID: "tab-worker", WorkspaceID: "ws-1", Layout: content.LayoutRow},
		aPane("pane-1", "tab-worker", "/srv"),
		"tab-1",
	)
	if err == nil {
		t.Fatal("CreateTabAfter accepted a pane id that already means another pane")
	}

	after := aStrip(t, layout, "ws-1")
	if len(after) != len(before) {
		t.Fatalf("the strip holds %v after a failed placement, want %v", tabIDs(t, layout, "ws-1"), tabIDsOf(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Position != before[i].Position {
			t.Fatalf("row %d is %+v after a failed placement, want %+v", i, after[i], before[i])
		}
	}
	for _, tab := range after {
		if tab.ID == "tab-worker" {
			t.Fatal("a refused placement left its tab row behind")
		}
	}
}

// The read the placement needs: which tab holds this pane. It is the walk
// upstream of PaneCwd's, from the same row, and it answers ErrNoSuchPane
// rather than "" for a pane nobody has — an empty answer would be an id that
// resolves to no tab and reads as a success at the call site.
func TestTabForPaneAnswersTheTabThatHoldsIt(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	got, err := layout.TabForPane(context.Background(), "pane-2")
	if err != nil {
		t.Fatalf("TabForPane: %v", err)
	}
	if got != "tab-2" {
		t.Fatalf("TabForPane answered %q, want tab-2", got)
	}

	if _, err := layout.TabForPane(context.Background(), "pane-nobody-made"); !errors.Is(err, content.ErrNoSuchPane) {
		t.Fatalf("TabForPane for an unknown pane = %v, want ErrNoSuchPane", err)
	}
}

func tabIDsOf(tabs []content.Tab) []string {
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		out = append(out, tab.ID)
	}
	return out
}
