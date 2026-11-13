package content

// THE 23 → 24 EDGE: the skill library's audit storage is retired
// (ADR-anntnxdj §7).
//
// Built from testdata/schema_v23.sql — the released shape, not the current
// schema with its stamp lowered — because a fixture the current code produced
// would agree with the migration by construction.

import (
	"context"
	"path/filepath"
	"testing"
)

// aDatabaseAtSchema23 is a released-shape file carrying a row in the table
// this rung removes, so "the table is gone" cannot pass by the fixture having
// had nothing in it.
func aDatabaseAtSchema23(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		releasedSchema(t, 23),
		`INSERT INTO workspaces (id, name, created_at) VALUES ('workspace-23', 'preserved workspace', 2300)`,
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-23', 'workspace-23', 'preserved tab')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-23', 'tab-23', '/srv/twenty-three', 'local')`,
		`INSERT INTO skill_checks (name, provenance, verdict, report, role, endpoint, model, digest, checked_at)
			VALUES ('the-check', 'installed', 'clear', 'the report', 'auditing', 'local', 'the-model', 'the-digest', 2300)`,
		`PRAGMA user_version=23`,
	)
}

// THE HEADLINE: a released schema 23 database opens, lands on the current
// schema, keeps every row it had, and no longer has the table the skill
// library's audit wrote.
func TestAReleasedSchema23DatabaseDropsTheSkillChecksTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aDatabaseAtSchema23(t, path)

	db := openStore(t, path)
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("schema stamp = %d, want the current %d", got, schemaVersion)
	}
	if aTableExists(t, path, "skill_checks") {
		t.Fatal("skill_checks survived the 23→24 rung — the skill library's audit storage is still in the file")
	}

	// The rows the file carried before the rung, whole.
	if got := rawString(t, path, `SELECT name FROM tabs WHERE id = 'tab-23'`); got != "preserved tab" {
		t.Errorf("tab row after migration = %q", got)
	}
	if got := rawString(t, path, `SELECT id || ':' || cwd FROM panes`); got != "pane-23:/srv/twenty-three" {
		t.Errorf("pane row after migration = %q", got)
	}

	// And the store works on a file that got here by migration: a row written
	// through a repository reads back.
	if _, err := db.Layout().CreateTab(context.Background(),
		Tab{ID: "tab-after", WorkspaceID: "workspace-23", Layout: LayoutRow},
		Pane{ID: "pane-after", TabID: "tab-after", Cwd: "/srv/after", Kind: PaneLocal, SizeShare: 1}); err != nil {
		t.Fatalf("CreateTab on a migrated row: %v", err)
	}

	// A fresh file and this migrated one are the same shape.
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
