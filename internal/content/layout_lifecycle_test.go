package content_test

// The container lifecycle (nocx-isoph.3), design
// .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §4.1 and §4.4,
// and §4.1/§4.2/§4.4 of the 2026-08-15 workspaces UX design.
//
// Two rules, one shape:
//
//	a workspace exists only while it holds at least one tab
//	a tab exists only while it holds at least one pane
//
// Neither needs lifecycle code and neither gets a sweep: the container's row
// goes in the SAME TRANSACTION that removes its last member, and creation is
// always creation-with-content, so there is never a moment when an empty
// container exists. That is what makes the rule cheap rather than a garbage
// collector — the empty state is unreachable, not merely tidied up.
//
// The transactional half of the rule is asserted in
// layout_lifecycle_internal_test.go, which can fail the second statement of
// the transaction deterministically; this file is the seam's behaviour.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

// ── fixtures ─────────────────────────────────────────────────────────────

func aTab(id, workspaceID string) content.Tab {
	return content.Tab{ID: id, WorkspaceID: workspaceID, Layout: content.LayoutRow}
}

func aPane(id, tabID, cwd string) content.Pane {
	return content.Pane{ID: id, TabID: tabID, Cwd: cwd, Kind: content.PaneLocal, SizeShare: 1}
}

// aReplacement is the identity a caller pre-mints for the tab that appears
// when the last one in the application closes. The ids are the frontend's
// (§7), so they are a parameter rather than something the backend invents.
func aReplacement() content.Replacement {
	return content.Replacement{TabID: "tab-replacement", PaneID: "pane-replacement", Cwd: "/home/user"}
}

// seedWorkspace mints a whole chain in one call, the only way one can be
// minted: workspace, its first tab, that tab's first pane.
func seedWorkspace(t *testing.T, layout content.LayoutRepository, wsID, tabID, paneID string) {
	t.Helper()
	if _, err := layout.CreateWorkspace(context.Background(),
		content.Workspace{ID: wsID, Name: wsID},
		aTab(tabID, wsID),
		aPane(paneID, tabID, "/srv"),
	); err != nil {
		t.Fatalf("CreateWorkspace %s: %v", wsID, err)
	}
}

func tabIDs(t *testing.T, layout content.LayoutRepository, workspaceID string) []string {
	t.Helper()
	tabs, err := layout.Tabs(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Tabs(%s): %v", workspaceID, err)
	}
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		out = append(out, tab.ID)
	}
	return out
}

func workspaceIDs(t *testing.T, layout content.LayoutRepository) []string {
	t.Helper()
	spaces, err := layout.Workspaces(context.Background())
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	out := make([]string, 0, len(spaces))
	for _, ws := range spaces {
		out = append(out, ws.ID)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ── creation is always creation-with-content ─────────────────────────────

// An empty workspace was proposed and rejected by the owner: a workspace with
// no tabs has no meaning. Refusing the call is what removes the empty state —
// and with it the "open a tab somewhere it does not belong, then move it out"
// path — rather than leaving a window in which one exists.
func TestAWorkspaceCannotBeCreatedWithoutAFirstTab(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()

	_, err := layout.CreateWorkspace(ctx, content.Workspace{ID: "ws-1", Name: "release"},
		content.Tab{}, aPane("pane-1", "tab-1", "/srv"))
	if !errors.Is(err, content.ErrNoFirstTab) {
		t.Fatalf("CreateWorkspace with no first tab: err = %v, want content.ErrNoFirstTab", err)
	}
	// And nothing was written: a refusal that leaves the row behind is the
	// empty workspace by another route.
	if got := workspaceIDs(t, layout); len(got) != 0 {
		t.Fatalf("workspaces after the refusal = %v, want none", got)
	}
}

// The same rule one rung down: a tab exists only while it holds a pane, so a
// tab is minted with its first pane. §4.4's "dragging a pane out mints a tab
// for it" is exactly this call — the pane is what the tab is minted around.
func TestATabCannotBeCreatedWithoutAFirstPane(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	_, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), content.Pane{})
	if !errors.Is(err, content.ErrNoFirstPane) {
		t.Fatalf("CreateTab with no first pane: err = %v, want content.ErrNoFirstPane", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-1" {
		t.Fatalf("tabs after the refusal = %v, want the seeded one alone", got)
	}
}

// The whole chain in one call, and it is one transaction: after it there is a
// workspace, a tab in it and a pane in that tab, and at no point was any of
// them alone.
func TestCreatingAWorkspaceMintsItsFirstTabAndThatTabsFirstPane(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()

	ws := content.Workspace{ID: "ws-1", Name: "release", Position: 3}
	tab := content.Tab{ID: "tab-1", Name: str("deploy"), Colour: str("#ff8800"), Layout: content.LayoutColumn}
	pane := content.Pane{
		ID: "pane-1", Cwd: "/srv/api", Kind: content.PaneSSH,
		Endpoint: str("deploy@srv-01:22"), SizeShare: 1,
	}
	// The tab's workspace and the pane's tab are left EMPTY on purpose: the
	// containers are what this call is creating, so naming them again is a
	// second place for the answer to be wrong.
	if _, err := layout.CreateWorkspace(ctx, ws, tab, pane); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != "ws-1" {
		t.Fatalf("workspaces = %v, want ws-1 alone", got)
	}
	tabs, err := layout.Tabs(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "tab-1" || tabs[0].WorkspaceID != "ws-1" {
		t.Fatalf("tabs = %+v, want tab-1 in ws-1", tabs)
	}
	panes, err := layout.Panes(ctx, "tab-1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 1 || panes[0].ID != "pane-1" || panes[0].TabID != "tab-1" {
		t.Fatalf("panes = %+v, want pane-1 in tab-1", panes)
	}
	if panes[0].Cwd != "/srv/api" || panes[0].Kind != content.PaneSSH {
		t.Fatalf("first pane = %+v, want the caller's cwd and kind", panes[0])
	}
}

// A first tab that names a DIFFERENT workspace is refused rather than
// silently re-parented: the call says which workspace it is creating, and two
// answers to that question is the shape this whole design spends its length
// avoiding.
func TestCreationRefusesAMemberThatNamesAnotherContainer(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()

	_, err := layout.CreateWorkspace(ctx, content.Workspace{ID: "ws-1", Name: "release"},
		aTab("tab-1", "ws-other"), aPane("pane-1", "tab-1", "/srv"))
	if !errors.Is(err, content.ErrMismatchedContainer) {
		t.Fatalf("first tab naming another workspace: err = %v, want content.ErrMismatchedContainer", err)
	}
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	_, err = layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-other", "/srv"))
	if !errors.Is(err, content.ErrMismatchedContainer) {
		t.Fatalf("first pane naming another tab: err = %v, want content.ErrMismatchedContainer", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 {
		t.Fatalf("tabs after the refusal = %v, want the seeded one alone", got)
	}
}

// ── dissolution: the container goes with its last member ─────────────────

func TestRemovingTheLastPaneRemovesItsTab(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-2", "/var")); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}

	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-2" {
		t.Fatalf("tabs = %v, want tab-2 alone — the tab goes with its last pane", got)
	}
	// The workspace still holds a tab, so it stays.
	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != "ws-1" {
		t.Fatalf("workspaces = %v, want ws-1 — it still holds a tab", got)
	}
}

// A tab with a sibling pane keeps its row: the rule is "no members left", not
// "a member left".
func TestRemovingAPaneWithASiblingLeavesTheTab(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreatePane(ctx, aPane("pane-2", "tab-1", "/var")); err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-1" {
		t.Fatalf("tabs = %v, want tab-1 still standing", got)
	}
	panes, err := layout.Panes(ctx, "tab-1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 1 || panes[0].ID != "pane-2" {
		t.Fatalf("panes = %+v, want the sibling alone", panes)
	}
}

func TestRemovingTheLastTabRemovesItsWorkspace(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	// A second workspace, so the application is not left empty and the
	// replacement rule is not what this test is measuring.
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}
	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != "ws-2" {
		t.Fatalf("workspaces = %v, want ws-2 alone — the workspace goes with its last tab", got)
	}
}

// The whole chain unwinds from one pane: the pane was the tab's last and the
// tab was the workspace's last, so all three go in one transaction.
func TestRemovingTheLastPaneOfTheLastTabTakesTheWorkspaceToo(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")

	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}
	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != "ws-2" {
		t.Fatalf("workspaces = %v, want ws-2 alone", got)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 0 {
		t.Fatalf("tabs in the dissolved workspace = %v, want none", got)
	}
}

// ── the strip stays dense when a tab leaves it ───────────────────────────
//
// A workspace's open tabs are 0..n-1 in the order they are drawn. ReorderTabs
// writes that, and CreateTabAfter's seatTabAfter renumbers the strip around an
// INSERT; a close has to close the gap for the same reason, because every
// reader of the strip — Tabs' own ORDER BY position, id — sorts rather than
// indexes, and a hole would still read back in order while no longer being
// dense. The order is what a person sees; the density is the state the store
// promised (nocx-xn63t.4.14).
//
// Two doors reach a tab leaving the strip, and both are asserted: DeleteTab,
// and the dissolution DeletePane and MovePane trigger when the last pane of a
// tab goes.

// Criterion: closing a tab in the middle of a strip leaves the rest dense —
// the neighbours move up by one and the order is what it was.
func TestClosingATabRenumbersTheStripBehindIt(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	if err := layout.DeleteTab(context.Background(), "tab-2", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	strip := aStrip(t, layout, "ws-1")
	if got := tabIDsOf(strip); len(got) != 2 || got[0] != "tab-1" || got[1] != "tab-3" {
		t.Fatalf("the strip is %v, want [tab-1 tab-3] — the order is what it was", got)
	}
	for seat, tab := range strip {
		if tab.Position != seat {
			t.Fatalf("tab %s sits at position %d, want %d — the strip is not dense after the close",
				tab.ID, tab.Position, seat)
		}
	}
}

// The same property through the OTHER door: closing a tab's last pane
// dissolves the tab, and the strip it was in closes the gap just the same. One
// invariant, one implementation, and both ways in are asserted because they are
// two different call sites.
func TestDissolvingATabByClosingItsLastPaneRenumbersTheStrip(t *testing.T) {
	_, layout := newLayout(t)
	aStripOfThree(t, layout)

	if err := layout.DeletePane(context.Background(), "pane-2", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}

	strip := aStrip(t, layout, "ws-1")
	if got := tabIDsOf(strip); len(got) != 2 || got[0] != "tab-1" || got[1] != "tab-3" {
		t.Fatalf("the strip is %v, want [tab-1 tab-3]", got)
	}
	for seat, tab := range strip {
		if tab.Position != seat {
			t.Fatalf("tab %s sits at position %d, want %d — the strip is not dense after the close",
				tab.ID, tab.Position, seat)
		}
	}
}

// A close renumbers the strip it happened in and nothing else: another
// workspace's tabs keep the seats they had.
func TestClosingATabLeavesEveryOtherStripsSeatsAlone(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	aStripOfThree(t, layout)
	seedWorkspace(t, layout, "ws-2", "tab-other", "pane-other")

	if err := layout.DeleteTab(ctx, "tab-2", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if got := seatOf(t, layout, "ws-2", "tab-other"); got != 0 {
		t.Fatalf("a tab in another workspace moved to position %d, want 0", got)
	}
}

// The last tab in a workspace leaves nothing to renumber — and the workspace
// itself is dissolved, so the row carrying its positions is gone. Asserted
// because "close the gap" must not reach past the strip it is closing.
func TestClosingTheLastTabOfAStripHasNoGapLeftToClose(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if got := tabIDs(t, layout, "ws-1"); len(got) != 0 {
		t.Fatalf("tabs in the dissolved workspace = %v, want none", got)
	}
	strip := aStrip(t, layout, "ws-2")
	if len(strip) != 1 || strip[0].Position != 0 {
		t.Fatalf("ws-2's strip = %+v, want tab-2 alone at position 0", strip)
	}
}

// ── the replacement tab, and the workspace it belongs to ─────────────────

// Closing the last tab in the APPLICATION yields a fresh one, and it is in
// the DEFAULT workspace — never in the one being closed, or closing a
// workspace resurrects what it just deleted. Asserted by reading the new
// tab's workspace_id, not by looking at a strip.
func TestClosingTheLastTabInTheApplicationYieldsATabInTheDefaultWorkspace(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != content.DefaultWorkspaceID {
		t.Fatalf("workspaces = %v, want the default alone — ws-1 was dissolved and the replacement went home", got)
	}
	tabs, err := layout.Tabs(ctx, content.DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("Tabs(default): %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "tab-replacement" {
		t.Fatalf("tabs in the default workspace = %+v, want the replacement", tabs)
	}
	if tabs[0].WorkspaceID != content.DefaultWorkspaceID {
		t.Fatalf("replacement tab workspace_id = %q, want %q", tabs[0].WorkspaceID, content.DefaultWorkspaceID)
	}
	// And it arrived with content, like every other container: the
	// replacement is a tab holding a pane, never an empty strip entry.
	panes, err := layout.Panes(ctx, "tab-replacement")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 1 || panes[0].ID != "pane-replacement" || panes[0].Cwd != "/home/user" {
		t.Fatalf("replacement panes = %+v, want one pane in the caller's cwd", panes)
	}
}

// The same rule reached from the other two doors: the last pane, and the
// workspace itself. All three run through one implementation, and this is
// what says so.
func TestTheReplacementArrivesWhicheverDoorEmptiedTheApplication(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(content.LayoutRepository) error
	}{
		{"the last pane", func(l content.LayoutRepository) error {
			return l.DeletePane(context.Background(), "pane-1", aReplacement())
		}},
		{"the last tab", func(l content.LayoutRepository) error {
			return l.DeleteTab(context.Background(), "tab-1", aReplacement())
		}},
		{"the workspace", func(l content.LayoutRepository) error {
			return l.DeleteWorkspace(context.Background(), "ws-1", aReplacement())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, layout := newLayout(t)
			seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
			if err := tc.close(layout); err != nil {
				t.Fatalf("close: %v", err)
			}
			tabs, err := layout.Tabs(context.Background(), content.DefaultWorkspaceID)
			if err != nil {
				t.Fatalf("Tabs(default): %v", err)
			}
			if len(tabs) != 1 || tabs[0].ID != "tab-replacement" {
				t.Fatalf("tabs in the default workspace = %+v, want the replacement", tabs)
			}
			// By its workspace_id, and not merely by which list it came back
			// in: closing a WORKSPACE that held every tab must not put the
			// replacement in the workspace it just deleted, or the close
			// resurrects what it removed (nocx-isoph.6's dialog is the ask in
			// front of exactly this act).
			if tabs[0].WorkspaceID != content.DefaultWorkspaceID {
				t.Fatalf("replacement tab workspace_id = %q, want %q",
					tabs[0].WorkspaceID, content.DefaultWorkspaceID)
			}
			if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != content.DefaultWorkspaceID {
				t.Fatalf("workspaces = %v, want the default alone — ws-1 is gone", got)
			}
		})
	}
}

// FAIL CLOSED: a close that would empty the application and carries no
// identity for the replacement removes NOTHING. The alternative is an
// application with no tab at all, which is a state no surface can render and
// no user asked for — and minting an id in the backend would make a durable
// pane id that did not come from the frontend (§7).
func TestAClosePastTheLastTabWithoutAReplacementIdentityIsRefused(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	err := layout.DeleteTab(ctx, "tab-1", content.Replacement{})
	if !errors.Is(err, content.ErrNoReplacement) {
		t.Fatalf("closing the last tab with no replacement: err = %v, want content.ErrNoReplacement", err)
	}
	if got := workspaceIDs(t, layout); len(got) != 1 || got[0] != "ws-1" {
		t.Fatalf("workspaces = %v, want ws-1 untouched", got)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-1" {
		t.Fatalf("tabs = %v, want tab-1 untouched — the refusal removed nothing", got)
	}
	if panes, perr := layout.Panes(ctx, "tab-1"); perr != nil || len(panes) != 1 {
		t.Fatalf("panes = %+v (err %v), want pane-1 untouched", panes, perr)
	}
}

// The DEFAULT workspace is the one exemption from dissolution, and it is not
// a special case for its own sake: it is where the replacement tab goes, it
// never renders, and the ledger records every session nobody named a
// workspace for under it (sessions.workspace_id ON DELETE CASCADE). Deleting
// its row when its last tab left would take those session records with it and
// then need it back in the next statement.
func TestTheDefaultWorkspaceSurvivesItsLastTab(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, content.DefaultWorkspaceID, "tab-default", "pane-default")
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	if err := layout.DeleteTab(ctx, "tab-default", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}
	if got := workspaceIDs(t, layout); !contains(got, content.DefaultWorkspaceID) {
		t.Fatalf("workspaces = %v, want the default still present", got)
	}
	if got := tabIDs(t, layout, content.DefaultWorkspaceID); len(got) != 0 {
		t.Fatalf("tabs in the default workspace = %v, want none — only its row survives", got)
	}
	// And it cannot be closed by hand either: it never renders, so nothing
	// can offer the affordance, and a call that tried would take the
	// fallback sessions with it.
	if err := layout.DeleteWorkspace(ctx, content.DefaultWorkspaceID, aReplacement()); !errors.Is(err, content.ErrDefaultWorkspace) {
		t.Fatalf("DeleteWorkspace(default): err = %v, want content.ErrDefaultWorkspace", err)
	}
}

// ── the move: only a reference changes ───────────────────────────────────

// THE assertion this design exists for. Dragging a pane into another tab
// changes its tab_id and nothing else: its identity, its cwd, its blocks and
// its LIVE PIPE are untouched, because only a reference moved. "A new pane
// with the same contents" is precisely the failure the swap of the words tab
// and pane (nocx-ehkvy) was made to prevent, so the session is asserted to be
// the SAME incarnation — id, instance and epoch — and to still carry bytes
// after the move, not merely to exist.
func TestAPaneMovedBetweenTabsKeepsItsIdentityItsCwdAndItsLiveSession(t *testing.T) {
	db, layout, path := newLayoutAt(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	if _, err := layout.CreateTab(ctx, aTab("tab-2", "ws-1"), aPane("pane-2", "tab-2", "/var")); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}

	// A live session. The pipe is a loopback rather than a real pty: what is
	// asserted is that the session the backend holds is the same one
	// afterwards, and an OS pty would add a dependency this question does
	// not have.
	//
	// It carries no workspace of its own (nocx-isoph.2): the workspace is
	// the TAB's column and the backend resolves pane → tab → workspace, so
	// the only durable link between this session and a workspace is the
	// ledger record written below.
	reg := session.New(log.NewSlogAdapter(nil), &loopbackFactory{})
	sess, openErr := reg.Open(ctx, session.Config{
		Kind: session.KindLocal,
		Cwd:  "/srv",
		Cols: 80, Rows: 24,
	})
	if openErr != nil {
		t.Fatalf("session Open: %v", openErr)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })
	out := make(chan []byte, 8)
	if err := sess.StartOutput(ctx, func(data []byte) error {
		buf := make([]byte, len(data))
		copy(buf, data)
		out <- buf
		return nil
	}); err != nil {
		t.Fatalf("StartOutput: %v", err)
	}
	// The durable half of the attachment: the ledger's record of this
	// session, under the workspace the pane is in. A lifecycle that took the
	// workspace down would take it with it (ON DELETE CASCADE), which is the
	// concrete way a move could lose a live session's record.
	if err := db.Ledger().CreateSession(ctx, content.Session{ID: string(sess.ID()), WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	before := sess.Identity()

	if _, err := layout.MovePane(ctx, "pane-1", "tab-2"); err != nil {
		t.Fatalf("MovePane: %v", err)
	}

	// The pane: same id, same cwd, same everything but its tab.
	panes, err := layout.Panes(ctx, "tab-2")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	var moved *content.Pane
	for i := range panes {
		if panes[i].ID == "pane-1" {
			moved = &panes[i]
		}
	}
	if moved == nil {
		t.Fatalf("panes in tab-2 = %+v, want pane-1 among them", panes)
	}
	if moved.Cwd != "/srv" || moved.Kind != content.PaneLocal || moved.TabID != "tab-2" {
		t.Fatalf("moved pane = %+v, want the same pane under tab-2", *moved)
	}
	// The tab it left held nothing else, so it went in the same transaction.
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-2" {
		t.Fatalf("tabs = %v, want tab-2 alone", got)
	}

	// The session: the SAME incarnation, still registered, still alive, and
	// still passing bytes. Anything that closed and reopened it would fail
	// at the identity, and anything that dropped the pipe would fail at the
	// echo.
	live, err := reg.Get(sess.ID())
	if err != nil {
		t.Fatalf("registry.Get after the move: %v", err)
	}
	if !before.SameIncarnation(sess.ID(), live) {
		t.Fatalf("session identity after the move = %+v, want %+v — this is a NEW session, not a moved pane",
			live.Identity(), before)
	}
	if got := live.Liveness().Liveness; got != session.LivenessAlive {
		t.Fatalf("liveness after the move = %q, want alive", got)
	}
	if _, err := live.Write([]byte("echo still-here\n")); err != nil {
		t.Fatalf("write to the moved pane's session: %v", err)
	}
	select {
	case got := <-out:
		if string(got) != "echo still-here\n" {
			t.Fatalf("output after the move = %q, want the bytes written", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no output after the move: the pane's live pipe did not survive")
	}

	// And its durable record survived: nothing in the move touched the
	// session's row, so its blocks still resolve. Read through the keyed VFS
	// because the ledger seam writes restore keys and never reads them back.
	var recorded []string
	for _, row := range rawRows(t, path, `SELECT id, workspace_id FROM sessions`, 0, 1) {
		recorded = append(recorded, row[0])
	}
	if !contains(recorded, string(sess.ID())) {
		t.Fatalf("sessions = %v, want the moved pane's session still recorded", recorded)
	}
}

// A move to the tab the pane is already in is a no-op, not a dissolution: the
// naive implementation removes the "empty" source tab it is still in.
func TestMovingAPaneIntoItsOwnTabChangesNothing(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	if _, err := layout.MovePane(ctx, "pane-1", "tab-1"); err != nil {
		t.Fatalf("MovePane onto itself: %v", err)
	}
	if got := tabIDs(t, layout, "ws-1"); len(got) != 1 || got[0] != "tab-1" {
		t.Fatalf("tabs = %v, want tab-1 still there", got)
	}
	if panes, err := layout.Panes(ctx, "tab-1"); err != nil || len(panes) != 1 {
		t.Fatalf("panes = %+v (err %v), want pane-1 still there exactly once", panes, err)
	}
}

// A move naming a pane or a tab that does not exist FAILS rather than
// half-applying: the requirement inherited from §4.4 of the workspaces design
// is that a partial move must leave the pane in exactly one place.
func TestAMoveNamingSomethingThatDoesNotExistIsRefused(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")

	if _, err := layout.MovePane(ctx, "pane-nobody", "tab-1"); !errors.Is(err, content.ErrNoSuchPane) {
		t.Fatalf("moving an absent pane: err = %v, want content.ErrNoSuchPane", err)
	}
	if _, err := layout.MovePane(ctx, "pane-1", "tab-nobody"); !errors.Is(err, content.ErrNoSuchTab) {
		t.Fatalf("moving into an absent tab: err = %v, want content.ErrNoSuchTab", err)
	}
	if panes, err := layout.Panes(ctx, "tab-1"); err != nil || len(panes) != 1 || panes[0].ID != "pane-1" {
		t.Fatalf("panes = %+v (err %v), want pane-1 exactly where it was", panes, err)
	}
}

// Whether a pane can be dragged BETWEEN WORKSPACES is open (§12 q. 5 of the
// tabs/panes design, §4.4 of the workspaces one): the atomicity model for a
// subtree move is undesigned and a partial move must fail closed. Until it
// exists the store refuses the move whole. It is a refusal with a reason, not
// a permanent rule — the alternative available today is worse than refusing:
// the source workspace would dissolve under a live session whose record
// hangs off it, and the pane would be in a workspace its session is not in.
func TestMovingAPaneIntoAnotherWorkspaceIsRefusedUntilTheAtomicityModelExists(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-1", "pane-1")
	seedWorkspace(t, layout, "ws-2", "tab-2", "pane-2")

	_, err := layout.MovePane(ctx, "pane-1", "tab-2")
	if !errors.Is(err, content.ErrCrossWorkspaceMove) {
		t.Fatalf("cross-workspace move: err = %v, want content.ErrCrossWorkspaceMove", err)
	}
	// Neither in two places nor in none.
	src, err := layout.Panes(ctx, "tab-1")
	if err != nil || len(src) != 1 || src[0].ID != "pane-1" {
		t.Fatalf("source panes = %+v (err %v), want pane-1 still there", src, err)
	}
	dst, err := layout.Panes(ctx, "tab-2")
	if err != nil || len(dst) != 1 || dst[0].ID != "pane-2" {
		t.Fatalf("destination panes = %+v (err %v), want only its own pane", dst, err)
	}
	if got := workspaceIDs(t, layout); len(got) != 2 {
		t.Fatalf("workspaces = %v, want both — a refused move dissolves nothing", got)
	}
}

// ── the failure path for the one external dependency ─────────────────────

func TestLifecycleOnAClosedStore(t *testing.T) {
	db, layout := newLayout(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"CreateWorkspace", func() error {
			_, err := layout.CreateWorkspace(ctx, content.Workspace{ID: "w", Name: "n"}, aTab("t", "w"), aPane("p", "t", "/"))
			return err
		}},
		{"CreateTab", func() error {
			_, err := layout.CreateTab(ctx, aTab("t", "w"), aPane("p", "t", "/"))
			return err
		}},
		{"DeletePane", func() error { return layout.DeletePane(ctx, "p", aReplacement()) }},
		{"DeleteTab", func() error { return layout.DeleteTab(ctx, "t", aReplacement()) }},
		{"DeleteWorkspace", func() error { return layout.DeleteWorkspace(ctx, "w", aReplacement()) }},
		{"MovePane", func() error {
			_, err := layout.MovePane(ctx, "p", "t")
			return err
		}},
	} {
		if err := c.call(); err == nil {
			t.Fatalf("%s on a closed store: err = nil, want a refusal", c.name)
		}
	}
}

// ── a pipe that stays open ───────────────────────────────────────────────

// loopbackPTY is a pty whose output is whatever was written to it. pty.Stub
// answers EOF on the first read, which ends the session immediately — the
// opposite of what this test needs, since the question is whether a LIVE
// session survives a move.
type loopbackPTY struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	done chan struct{}
	once sync.Once
}

func newLoopbackPTY() *loopbackPTY {
	r, w := io.Pipe()
	return &loopbackPTY{r: r, w: w, done: make(chan struct{})}
}

func (p *loopbackPTY) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *loopbackPTY) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *loopbackPTY) Close() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.w.Close()
		_ = p.r.Close()
	})
	return nil
}
func (p *loopbackPTY) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (p *loopbackPTY) Done() <-chan struct{}                             { return p.done }

type loopbackFactory struct{}

func (f *loopbackFactory) NewPTY(_ context.Context, _ pty.Config) (pty.Pty, error) {
	return newLoopbackPTY(), nil
}
