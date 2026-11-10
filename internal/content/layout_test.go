package content_test

// Acceptance tests for the durable layout chain (nocx-isoph.1, design
// .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §3, §4.5, §5,
// §7): workspace → tab → pane become rows the backend owns, through the
// public seam content.Open → ContentDB.Layout() → LayoutRepository.
//
// Half of these tests assert an ABSENCE, which is the point of the bead as
// much as the presence: a tab must have no column for the activity
// indicator, the attention indicator or the label (§4.5), a pane no way to
// nest inside a pane (§5), and a workspace no way to nest inside a workspace
// (§3). A field invented here is a field the whole epic inherits, so the
// column sets are asserted EXACTLY rather than by membership — the schema
// equivalent of additionalProperties:false, and the only form that can fail
// on a column nobody meant to add.
//
// These tests are the only callers of this write path until nocx-isoph.2
// puts the create/move methods on the wire; layout.go's header says so in
// the same words.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lineage"
)

// ── fixtures ─────────────────────────────────────────────────────────────

func newLayout(t *testing.T) (content.ContentDB, content.LayoutRepository) {
	t.Helper()
	db, _ := newTestStore(t)
	return db, db.Layout()
}

func newLayoutAt(t *testing.T) (content.ContentDB, content.LayoutRepository, string) {
	t.Helper()
	db, dir := newTestStore(t)
	return db, db.Layout(), filepath.Join(dir, "content.db")
}

func str(s string) *string { return &s }
func i64(v int64) *int64   { return &v }

// rawRows runs one query against the encrypted file the way Open does and
// returns every row as a slice of strings — enough for PRAGMA table_info and
// PRAGMA foreign_key_list, which is the only way to assert what the schema
// does NOT have.
func rawRows(t *testing.T, path, query string, cols ...int) [][]string {
	t.Helper()
	keyHex := hex.EncodeToString(testKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("raw query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		t.Fatalf("raw columns: %v", err)
	}
	var out [][]string
	for rows.Next() {
		cells := make([]any, len(names))
		holders := make([]*string, len(names))
		for i := range holders {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("raw scan: %v", err)
		}
		row := make([]string, 0, len(cols))
		for _, c := range cols {
			if holders[c] == nil {
				row = append(row, "")
				continue
			}
			row = append(row, *holders[c])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("raw rows: %v", err)
	}
	return out
}

// columnsOf is the table's column names in declaration order.
func columnsOf(t *testing.T, path, table string) []string {
	t.Helper()
	var names []string
	for _, r := range rawRows(t, path, "PRAGMA table_info("+table+")", 1) {
		names = append(names, r[0])
	}
	return names
}

// foreignKeysOf is (referenced table, from column, to column, on delete) for
// every foreign key the table declares.
func foreignKeysOf(t *testing.T, path, table string) [][]string {
	t.Helper()
	// PRAGMA foreign_key_list columns: id, seq, table, from, to, on_update,
	// on_delete, match.
	return rawRows(t, path, "PRAGMA foreign_key_list("+table+")", 2, 3, 4, 6)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── presence: every stored field survives a write and a read ─────────────

// The round trip, and it is a round trip through a REOPENED file: a value
// that only exists in the writer's memory is not stored, and this is the
// assertion that tells the two apart.
func TestLayoutStoresAndReadsBackEveryField(t *testing.T) {
	db, layout, path := newLayoutAt(t)
	ctx := context.Background()

	ws := content.Workspace{ID: "ws-1", Name: "release", Position: 2}
	root := content.Tab{
		ID:          "tab-root",
		WorkspaceID: "ws-1",
		Position:    0,
		Layout:      content.LayoutRow,
	}
	// The whole chain is minted in one call: a workspace is created together
	// with its first tab and that tab's first pane (nocx-isoph.3).
	if _, err := layout.CreateWorkspace(ctx, ws, root, content.Pane{
		ID: "pane-root", TabID: "tab-root", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1,
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	child := content.Tab{
		ID:          "tab-child",
		WorkspaceID: "ws-1",
		ParentID:    str("tab-root"),
		Name:        str("deploy"),
		Colour:      str("#ff8800"),
		Position:    1,
		Pinned:      true,
		Layout:      content.LayoutColumn,
		SeenAt:      i64(1_700_000_000_000),
	}
	local := content.Pane{
		ID:        "pane-local",
		TabID:     "tab-child",
		Cwd:       "/srv/api",
		Kind:      content.PaneLocal,
		SizeShare: 0.25,
	}
	remote := content.Pane{
		ID:        "pane-ssh",
		TabID:     "tab-child",
		Cwd:       "/var/log",
		Kind:      content.PaneSSH,
		Endpoint:  str("deploy@srv-01:22"),
		SizeShare: 0.75,
	}
	if _, err := layout.CreateTab(ctx, child, local); err != nil {
		t.Fatalf("CreateTab child: %v", err)
	}
	// The second pane of a tab is a split, and that is the one creation
	// that adds a member to a container that already exists.
	if _, err := layout.CreatePane(ctx, remote); err != nil {
		t.Fatalf("CreatePane %s: %v", remote.ID, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	back := again.Layout()

	spaces, err := back.Workspaces(ctx)
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(spaces) != 1 || spaces[0] != ws {
		t.Fatalf("Workspaces = %+v, want exactly %+v", spaces, ws)
	}

	tabs, err := back.Tabs(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(tabs) != 2 {
		t.Fatalf("Tabs = %d rows, want 2", len(tabs))
	}
	// Ordered by position: the strip's order is stored, not incidental.
	if tabs[0].ID != "tab-root" || tabs[1].ID != "tab-child" {
		t.Fatalf("Tabs order = %s, %s — want position order", tabs[0].ID, tabs[1].ID)
	}
	assertTabEqual(t, tabs[0], root)
	assertTabEqual(t, tabs[1], child)

	panes, err := back.Panes(ctx, "tab-child")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("Panes = %d rows, want 2", len(panes))
	}
	byID := map[string]content.Pane{}
	for _, p := range panes {
		byID[p.ID] = p
	}
	assertPaneEqual(t, byID["pane-local"], local)
	assertPaneEqual(t, byID["pane-ssh"], remote)
}

func assertTabEqual(t *testing.T, got, want content.Tab) {
	t.Helper()
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID ||
		got.Position != want.Position || got.Pinned != want.Pinned ||
		got.Layout != want.Layout {
		t.Fatalf("tab %s: got %+v, want %+v", want.ID, got, want)
	}
	assertStrPtr(t, "parent", got.ParentID, want.ParentID)
	assertStrPtr(t, "name", got.Name, want.Name)
	assertStrPtr(t, "colour", got.Colour, want.Colour)
	switch {
	case (got.SeenAt == nil) != (want.SeenAt == nil):
		t.Fatalf("tab %s seenAt: got %v, want %v", want.ID, got.SeenAt, want.SeenAt)
	case got.SeenAt != nil && *got.SeenAt != *want.SeenAt:
		t.Fatalf("tab %s seenAt: got %d, want %d", want.ID, *got.SeenAt, *want.SeenAt)
	}
}

func assertPaneEqual(t *testing.T, got, want content.Pane) {
	t.Helper()
	if got.ID != want.ID || got.TabID != want.TabID || got.Cwd != want.Cwd ||
		got.Kind != want.Kind || got.SizeShare != want.SizeShare {
		t.Fatalf("pane %s: got %+v, want %+v", want.ID, got, want)
	}
	assertStrPtr(t, "endpoint", got.Endpoint, want.Endpoint)
}

func assertStrPtr(t *testing.T, what string, got, want *string) {
	t.Helper()
	switch {
	case (got == nil) != (want == nil):
		t.Fatalf("%s: got %v, want %v", what, got, want)
	case got != nil && *got != *want:
		t.Fatalf("%s: got %q, want %q", what, *got, *want)
	}
}

// ── absence: what a tab must NOT store (§4.5) ────────────────────────────

// The activity indicator, the attention indicator and the label are COMPUTED
// from the tab's panes and must have no column. Attention ARRIVES AT A PANE —
// a command failed, a worker asked a question — so a column on the tab too
// would give one fact two owners, and they diverge the first time a pane is
// dragged elsewhere. The label is derived for the same reason: a tab created
// by a drag was never named by anybody, so its panes' titles are its label.
//
// What IS the tab's own is "I have seen this", which duplicates nothing, so
// seen_at is present and asserted above.
func TestTabStoresNoActivityNoAttentionAndNoLabel(t *testing.T) {
	db, _, path := newLayoutAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := columnsOf(t, path, "tabs")
	// Exactly this, in this order. A membership check would pass a schema
	// that had quietly grown `unseen` next to `seen_at`.
	// digest is the create key's content binding (§7, nocx-isoph.2), not a
	// property of the tab: it is what tells a RETRY of a create from an id
	// reused for something else, and it never crosses the wire.
	// closed_at is not a property of the tab either — it is which SET the row
	// is in, the window or the store (nocx-l21ib.4) — and it is a column for
	// exactly that reason: the alternative is a second table holding the same
	// identity, which is two owners of one row.
	want := []string{
		"id", "workspace_id", "parent_id", "name", "colour",
		"position", "pinned", "layout", "seen_at", "closed_at", "digest",
	}
	if !equalStrings(got, want) {
		t.Fatalf("tabs columns = %v, want exactly %v", got, want)
	}
	// Named individually as well, because this is the assertion the bead is
	// about and a reader of a failure should see the word that broke it.
	for _, forbidden := range []string{
		"activity", "has_activity", "active",
		"attention", "needs_attention", "alert",
		"label", "title", "unseen", "unread",
	} {
		for _, c := range got {
			if c == forbidden {
				t.Fatalf("tabs has column %q — it is computed from the tab's panes (§4.5), never stored", forbidden)
			}
		}
	}
}

func TestWorkspaceAndPaneStoreExactlyTheirFields(t *testing.T) {
	db, _, path := newLayoutAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, want := columnsOf(t, path, "workspaces"),
		[]string{"id", "name", "colour", "position", "created_at", "payload", "digest"}; !equalStrings(got, want) {
		t.Fatalf("workspaces columns = %v, want exactly %v", got, want)
	}
	if got, want := columnsOf(t, path, "panes"),
		[]string{"id", "tab_id", "cwd", "kind", "endpoint", "size_share", "closed_at", "digest"}; !equalStrings(got, want) {
		t.Fatalf("panes columns = %v, want exactly %v", got, want)
	}
}

// Panes do not nest (§5), and the schema makes a pane whose parent is a pane
// UNREPRESENTABLE rather than merely unused: a pane's only foreign key is its
// tab, so there is nowhere to write one.
func TestPanesCannotNest(t *testing.T) {
	db, _, path := newLayoutAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fks := foreignKeysOf(t, path, "panes")
	if len(fks) != 1 {
		t.Fatalf("panes foreign keys = %v, want exactly one (its tab)", fks)
	}
	if fks[0][0] != "tabs" || fks[0][1] != "tab_id" {
		t.Fatalf("panes foreign key = %v, want tab_id → tabs", fks[0])
	}
	for _, c := range columnsOf(t, path, "panes") {
		if strings.Contains(c, "pane") || c == "parent_id" {
			t.Fatalf("panes has column %q — a pane may not name another pane (§5)", c)
		}
	}
}

// A workspace is flat, never nested (§3): nothing on the row can name another
// workspace, so depth cannot be expressed at all. Depth comes from lineage,
// which lives on the tab.
func TestWorkspacesCannotNest(t *testing.T) {
	db, _, path := newLayoutAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fks := foreignKeysOf(t, path, "workspaces"); len(fks) != 0 {
		t.Fatalf("workspaces foreign keys = %v, want none", fks)
	}
	for _, c := range columnsOf(t, path, "workspaces") {
		if strings.Contains(c, "workspace") || c == "parent_id" {
			t.Fatalf("workspaces has column %q — a workspace may not name a workspace (§3)", c)
		}
	}
}

// ── the two edges are two columns, and only one of them is lineage ───────

// parent_id is lineage: provenance, immutable, never set by hand (§4.2). The
// display grouping is a separate SYMMETRIC relation set by dragging, and it
// has no column here on purpose — it arrives with drag (nocx-8m2x6). This
// test is what fails if somebody folds it onto parent_id, which is the
// failure AGENTS.md names.
func TestTabLineageEdgeIsAPointerToATabAndSurvivesNothingElse(t *testing.T) {
	db, _, path := newLayoutAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fks := foreignKeysOf(t, path, "tabs")
	if len(fks) != 2 {
		t.Fatalf("tabs foreign keys = %v, want exactly two (its workspace and its lineage parent)", fks)
	}
	byFrom := map[string][]string{}
	for _, fk := range fks {
		byFrom[fk[1]] = fk
	}
	// BOTH of the tab's edges are ON DELETE SET NULL, and workspace_id became
	// the second one with nocx-l21ib.4: a workspace is deleted while its
	// closed tabs are kept, so a cascade would take them — and their panes,
	// and every block anchored to those panes — which is what the marking
	// exists to prevent.
	if got := byFrom["workspace_id"]; got == nil || got[0] != "workspaces" || got[3] != "SET NULL" {
		t.Fatalf("tabs.workspace_id = %v, want → workspaces ON DELETE SET NULL", got)
	}
	if got := byFrom["parent_id"]; got == nil || got[0] != "tabs" || got[3] != "SET NULL" {
		t.Fatalf("tabs.parent_id = %v, want → tabs ON DELETE SET NULL", got)
	}
}

// ── the delete behaviours, each stated and each tested ───────────────────

// A tab belongs to its workspace: the workspace going takes its tabs, and
// their panes with them. §4.1's rule — a tab exists while it holds a pane —
// runs the other way and is a repository concern (nocx-isoph.3); this is the
// schema half.
func TestDeletingAWorkspaceTakesItsTabsAndTheirPanes(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedChain(t, layout)

	if err := layout.DeleteWorkspace(ctx, "ws-1", aReplacement()); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if tabs, err := layout.Tabs(ctx, "ws-1"); err != nil || len(tabs) != 0 {
		t.Fatalf("Tabs after the workspace went = %v (err %v), want none", tabs, err)
	}
	if panes, err := layout.Panes(ctx, "tab-1"); err != nil || len(panes) != 0 {
		t.Fatalf("Panes after the workspace went = %v (err %v), want none", panes, err)
	}
}

// A pane leaves the window with its tab. A CHILD tab does not — it is an
// independent tab that merely records where it came from — and since
// nocx-l21ib.4 it KEEPS the lineage edge it had: the parent's row is still
// there, marked closed, so "provenance lost" is no longer the honest state.
// It was the honest one while the parent was deleted, and the null it left
// was the cost of that delete rather than a property of closing a tab.
func TestClosingATabTakesItsPanesAndLeavesItsChildrensLineageIntact(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedChain(t, layout)

	if err := layout.DeleteTab(ctx, "tab-1", aReplacement()); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}
	if panes, err := layout.Panes(ctx, "tab-1"); err != nil || len(panes) != 0 {
		t.Fatalf("Panes after the tab left the window = %v (err %v), want none", panes, err)
	}
	tabs, err := layout.Tabs(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "tab-2" {
		t.Fatalf("tabs after the parent left = %+v, want the child alone", tabs)
	}
	if tabs[0].ParentID == nil || *tabs[0].ParentID != "tab-1" {
		t.Fatalf("child lineage = %v, want tab-1 — the parent's row outlives its closing", tabs[0].ParentID)
	}
}

// Deleting a pane leaves its tab and its siblings alone: a pane is the
// durable identity, and nothing about the tab is derived from a row count in
// the schema.
func TestDeletingAPaneLeavesItsTabAndSiblings(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedChain(t, layout)
	if _, err := layout.CreatePane(ctx, content.Pane{
		ID: "pane-2", TabID: "tab-1", Cwd: "/", Kind: content.PaneLocal, SizeShare: 0.5,
	}); err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	if err := layout.DeletePane(ctx, "pane-1", aReplacement()); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}
	panes, err := layout.Panes(ctx, "tab-1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 1 || panes[0].ID != "pane-2" {
		t.Fatalf("panes = %+v, want the sibling alone", panes)
	}
	if tabs, err := layout.Tabs(ctx, "ws-1"); err != nil || len(tabs) != 2 {
		t.Fatalf("tabs = %+v (err %v), want both still present", tabs, err)
	}
}

// seedChain writes ws-1 → tab-1 → pane-1, plus tab-2 whose lineage parent is
// tab-1 and whose own first pane is pane-1b. Every tab here holds a pane
// because a tab that holds none cannot exist (nocx-isoph.3).
func seedChain(t *testing.T, layout content.LayoutRepository) {
	t.Helper()
	ctx := context.Background()
	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-1", Name: "work"},
		content.Tab{ID: "tab-1", WorkspaceID: "ws-1", Position: 0, Layout: content.LayoutRow},
		content.Pane{ID: "pane-1", TabID: "tab-1", Cwd: "/srv", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, err := layout.CreateTab(ctx,
		content.Tab{ID: "tab-2", WorkspaceID: "ws-1", ParentID: str("tab-1"), Position: 1, Layout: content.LayoutRow},
		content.Pane{ID: "pane-1b", TabID: "tab-2", Cwd: "/srv", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateTab tab-2: %v", err)
	}
}

// ── the lineage edge is admitted by internal/lineage, not by a second rule ──

func TestTabLineageRefusesSelfCycleAndDepth(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "ws-1", "tab-seed", "pane-seed")
	tab := func(id string, parent *string) content.Tab {
		return content.Tab{ID: id, WorkspaceID: "ws-1", ParentID: parent, Layout: content.LayoutRow}
	}
	// Every tab is minted with a pane, so the first pane's id follows the
	// tab's; what this test is about is the lineage edge, not the member.
	pane := func(tabID string) content.Pane {
		return content.Pane{ID: "pane-" + tabID, TabID: tabID, Cwd: "/", Kind: content.PaneLocal, SizeShare: 1}
	}
	if _, err := layout.CreateTab(ctx, tab("self", str("self")), pane("self")); !errors.Is(err, lineage.ErrSelf) {
		t.Fatalf("self-parent: err = %v, want lineage.ErrSelf", err)
	}
	// A parent no row carries is refused rather than written and left
	// dangling — the store is the only thing that can answer that question,
	// and it answers it before the insert.
	if _, err := layout.CreateTab(ctx, tab("orphan", str("nobody")), pane("orphan")); err == nil {
		t.Fatalf("unknown parent: err = nil, want a refusal")
	}
	// The depth bound, both ends. n0 is a root; a child of n(MaxDepth-1) has
	// exactly MaxDepth ancestors and is the last one accepted.
	name := func(i int) string { return fmt.Sprintf("n%d", i) }
	if _, err := layout.CreateTab(ctx, tab(name(0), nil), pane(name(0))); err != nil {
		t.Fatalf("CreateTab root: %v", err)
	}
	for i := 1; i <= lineage.MaxDepth; i++ {
		if _, err := layout.CreateTab(ctx, tab(name(i), str(name(i-1))), pane(name(i))); err != nil {
			t.Fatalf("CreateTab within the bound at %d: %v", i, err)
		}
	}
	_, err := layout.CreateTab(ctx, tab(name(lineage.MaxDepth+1), str(name(lineage.MaxDepth))), pane(name(lineage.MaxDepth+1)))
	if !errors.Is(err, lineage.ErrTooDeep) {
		t.Fatalf("one past the bound: err = %v, want lineage.ErrTooDeep", err)
	}
	// A cycle cannot be reached through the seam at all — parent is written
	// once and never updated — so the refusal is asserted where it can be
	// expressed: an id that is already an ancestor is the child's own id,
	// which is the self case above. What this asserts is that no row was left
	// behind by any of the refusals.
	tabs, err := layout.Tabs(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	// The seeded tab the workspace was minted around, plus n0…nMaxDepth.
	if len(tabs) != lineage.MaxDepth+2 {
		t.Fatalf("tabs = %d, want %d — a refusal left a row behind", len(tabs), lineage.MaxDepth+2)
	}
}

// The closed enums say no: a pane is local or ssh and nothing else (§5), and
// a tab's layout is a row or a column. Reached through the seam, because a
// Go-typed caller can express the wrong value even though the type is named.
func TestClosedEnumsAreRefused(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedChain(t, layout)
	if _, err := layout.CreatePane(ctx, content.Pane{
		ID: "pane-bad", TabID: "tab-1", Cwd: "/", Kind: content.PaneKind("container"), SizeShare: 1,
	}); err == nil {
		t.Fatalf("pane kind 'container': err = nil, want a refusal — a pane is local or ssh")
	}
	if _, err := layout.CreateTab(ctx, content.Tab{
		ID: "tab-bad", WorkspaceID: "ws-1", Layout: content.TabLayout("grid"),
	}, content.Pane{ID: "pane-bad-tab", TabID: "tab-bad", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1}); err == nil {
		t.Fatalf("tab layout 'grid': err = nil, want a refusal — no asymmetric layouts (§5)")
	}
}

// An id that is already taken FAILS; it never overwrites (§7 consequence 2).
// The id is untrusted input and knowing one confers nothing.
func TestCreateRefusesAnIDThatIsAlreadyTaken(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedChain(t, layout)
	if _, err := layout.CreateWorkspace(ctx, content.Workspace{ID: "ws-1", Name: "other"},
		aTab("tab-other", "ws-1"), aPane("pane-other", "tab-other", "/")); err == nil {
		t.Fatalf("duplicate workspace id: err = nil, want a refusal")
	}
	if _, err := layout.CreateTab(ctx, content.Tab{
		ID: "tab-1", WorkspaceID: "ws-1", Layout: content.LayoutRow,
	}, aPane("pane-dup", "tab-1", "/")); err == nil {
		t.Fatalf("duplicate tab id: err = nil, want a refusal")
	}
	if _, err := layout.CreatePane(ctx, content.Pane{
		ID: "pane-1", TabID: "tab-1", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1,
	}); err == nil {
		t.Fatalf("duplicate pane id: err = nil, want a refusal")
	}
	// And the original is untouched.
	if spaces, err := layout.Workspaces(ctx); err != nil || len(spaces) != 1 || spaces[0].Name != "work" {
		t.Fatalf("workspaces = %+v (err %v), want the original name", spaces, err)
	}
}

// A tab whose workspace does not exist is refused: the chain has no orphan
// rung.
func TestATabWithoutAWorkspaceAndAPaneWithoutATabAreRefused(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	if _, err := layout.CreateTab(ctx, content.Tab{
		ID: "tab-1", WorkspaceID: "ws-nobody", Layout: content.LayoutRow,
	}, aPane("pane-1", "tab-1", "/")); err == nil {
		t.Fatalf("tab under an absent workspace: err = nil, want a refusal")
	}
	if _, err := layout.CreatePane(ctx, content.Pane{
		ID: "pane-1", TabID: "tab-nobody", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1,
	}); err == nil {
		t.Fatalf("pane under an absent tab: err = nil, want a refusal")
	}
}

// Every method on a closed store answers ErrClosed rather than panicking or
// hanging: the failure path for the one external dependency this seam has.
func TestLayoutOnAClosedStore(t *testing.T) {
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
			_, err := layout.CreateWorkspace(ctx, content.Workspace{ID: "w", Name: "n"},
				aTab("t", "w"), aPane("p", "t", "/"))
			return err
		}},
		{"DeleteWorkspace", func() error { return layout.DeleteWorkspace(ctx, "w", aReplacement()) }},
		{"CreateTab", func() error {
			_, err := layout.CreateTab(ctx, content.Tab{ID: "t", WorkspaceID: "w", Layout: content.LayoutRow},
				aPane("p", "t", "/"))
			return err
		}},
		{"DeleteTab", func() error { return layout.DeleteTab(ctx, "t", aReplacement()) }},
		{"CreatePane", func() error {
			_, err := layout.CreatePane(ctx, content.Pane{ID: "p", TabID: "t", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1})
			return err
		}},
		{"DeletePane", func() error { return layout.DeletePane(ctx, "p", aReplacement()) }},
		{"MovePane", func() error {
			_, err := layout.MovePane(ctx, "p", "t")
			return err
		}},
		{"RenameWorkspace", func() error {
			_, err := layout.RenameWorkspace(ctx, "w", "n")
			return err
		}},
		{"ReorderWorkspaces", func() error {
			_, err := layout.ReorderWorkspaces(ctx, []string{"w"})
			return err
		}},
		{"RenameTab", func() error {
			_, err := layout.RenameTab(ctx, "t", nil)
			return err
		}},
		{"RecolourTab", func() error {
			_, err := layout.RecolourTab(ctx, "t", nil)
			return err
		}},
		{"PinTab", func() error {
			_, err := layout.PinTab(ctx, "t", true)
			return err
		}},
		{"ReorderTabs", func() error {
			_, err := layout.ReorderTabs(ctx, "w", []string{"t"})
			return err
		}},
		{"WorkspaceForPane", func() error {
			_, err := layout.WorkspaceForPane(ctx, "p")
			return err
		}},
		{"Workspaces", func() error { _, err := layout.Workspaces(ctx); return err }},
		{"Tabs", func() error { _, err := layout.Tabs(ctx, "w"); return err }},
		{"Panes", func() error { _, err := layout.Panes(ctx, "t"); return err }},
	} {
		if err := c.call(); err == nil {
			t.Fatalf("%s on a closed store: err = nil, want a refusal", c.name)
		}
	}
}
