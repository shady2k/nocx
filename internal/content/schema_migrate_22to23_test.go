package content

// THE 22 → 23 EDGE: the sidebar panel becomes the tab's (nocx-ouru5.1.18).
//
// Built from testdata/schema_v22.sql — the released shape, not the current
// schema with its stamp lowered — because a fixture the current code produced
// would agree with the migration by construction.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func theSchema22Script(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "schema_v22.sql"))
	if err != nil {
		t.Fatalf("read the schema 22 fixture: %v", err)
	}
	return string(body)
}

// aDatabaseAtSchema22 is a released-shape file: its DDL comes only from the
// frozen v22 fixture, and its rows are distinctive enough to notice a copy
// that kept the count and lost the values.
func aDatabaseAtSchema22(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		theSchema22Script(t),
		`INSERT INTO workspaces (id, name, created_at) VALUES ('workspace-22', 'preserved workspace', 100)`,
		`INSERT INTO tabs (id, workspace_id, name, pinned, layout, seen_at) VALUES ('tab-22', 'workspace-22', 'preserved tab', 1, 'column', 1234)`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-22', 'tab-22', '/srv/old', 'local')`,
		`PRAGMA user_version=22`,
	)
}

func TestSchema22PanelMigrationAddsTheColumnsAndKeepsTheRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aDatabaseAtSchema22(t, path)

	db := openStore(t, path)
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("schema stamp = %d, want the current %d", got, schemaVersion)
	}

	// The columns exist, and the row that predates them gets the declared
	// defaults: no panel, rail expanded. NULL would be a third state the
	// schema does not have.
	if got := rawString(t, path, `SELECT panel_view || ':' || panel_collapsed FROM tabs WHERE id = 'tab-22'`); got != ":0" {
		t.Errorf("migrated panel = %q, want %q", got, ":0")
	}
	// Everything else about the row is untouched, values included.
	if got := rawString(t, path, `SELECT name || ':' || pinned || ':' || layout || ':' || seen_at FROM tabs WHERE id = 'tab-22'`); got != "preserved tab:1:column:1234" {
		t.Errorf("tab row after migration = %q", got)
	}
	if got := rawString(t, path, `SELECT id || ':' || cwd FROM panes`); got != "pane-22:/srv/old" {
		t.Errorf("pane row after migration = %q", got)
	}

	// The repository reads the new fields like any other: the migration and
	// the code that writes them agree about the row.
	tabs, err := db.Layout().Tabs(context.Background(), "workspace-22")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "tab-22" {
		t.Fatalf("Tabs = %+v, want the migrated tab", tabs)
	}
	if tabs[0].PanelView != "" || tabs[0].PanelCollapsed {
		t.Fatalf("migrated tab panel = %q/%v, want empty/expanded", tabs[0].PanelView, tabs[0].PanelCollapsed)
	}
	// And writing one is an ordinary update on the migrated row.
	if _, err = db.Layout().SetTabPanel(context.Background(), "tab-22", "ports", true); err != nil {
		t.Fatalf("SetTabPanel on a migrated row: %v", err)
	}

	// A fresh file and this migrated one are the same shape — the columns
	// arrived in the same order with the same declarations.
	ctx := context.Background()
	wantDigest, _, err := shapeOfDDL(ctx, schemaV1)
	if err != nil {
		t.Fatalf("fresh shape: %v", err)
	}
	conn, done := rawConn(t, path)
	defer done()
	objects, err := sqliteSchemaObjects(ctx, conn)
	if err != nil {
		t.Fatalf("migrated shape: %v", err)
	}
	if got := schemaObjectsDigest(objects); got != wantDigest {
		t.Errorf("migrated schema digest = %s, fresh = %s", got, wantDigest)
	}
}
