package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const schema21EntryRows = "entry-21-early:oldest completed command:1000|entry-21-later:later completed command:2000|entry-21-unended:unended historical command:NULL"

func theSchema21Script(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "schema_v21.sql"))
	if err != nil {
		t.Fatalf("read the schema 21 fixture: %v", err)
	}
	return string(body)
}

// aDatabaseAtSchema21 is a released-shape file, not the current schema with
// its stamp lowered: its DDL comes only from the frozen v21 fixture.
func aDatabaseAtSchema21(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		theSchema21Script(t),
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env-21', 'local', 100)`,
		`INSERT INTO workspaces (id, name, created_at) VALUES ('workspace-21', 'preserved workspace', 100)`,
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-21', 'workspace-21', 'preserved tab')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-21', 'tab-21', '/srv/old', 'local')`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at, ended_at)
			VALUES ('entry-21-early', 1, 'client-21', 'digest-early', 'env-21', 'pane-21', '/srv/old', 'shell', 'user', 'oldest completed command', 'closed', 'success', 900, 1000)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at, ended_at)
			VALUES ('entry-21-later', 2, 'client-21', 'digest-later', 'env-21', 'pane-21', '/srv/old', 'shell', 'user', 'later completed command', 'closed', 'success', 1900, 2000)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at, ended_at)
			VALUES ('entry-21-unended', 3, 'client-21', 'digest-unended', 'env-21', 'pane-21', '/srv/old', 'shell', 'user', 'unended historical command', 'closed', 'unknown', 2900, NULL)`,
		`INSERT INTO clear_boundaries (id, pane_id, ingest_seq, kind, created_at) VALUES ('boundary-21', 'pane-21', 1, 'clear', 3000)`,
		`UPDATE ledger_sequence SET next = 4 WHERE id = 1`,
		`PRAGMA user_version=21`,
	)
}

func rawString(t *testing.T, path, query string) string {
	t.Helper()
	conn, done := rawConn(t, path)
	defer done()
	var got string
	if err := conn.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

func assertSchema21Contents(t *testing.T, path string) {
	t.Helper()
	for _, table := range []string{"environments", "workspaces", "tabs", "panes", "clear_boundaries"} {
		if got := rawCount(t, path, "SELECT count(*) FROM "+table); got != 1 {
			t.Errorf("%s rows = %d, want 1", table, got)
		}
	}
	if got := rawCount(t, path, "SELECT count(*) FROM entries"); got != 3 {
		t.Errorf("entries rows = %d, want 3", got)
	}
	got := rawString(t, path, `SELECT group_concat(value, '|') FROM (
		SELECT id || ':' || intent || ':' || COALESCE(CAST(ended_at AS TEXT), 'NULL') AS value
		FROM entries ORDER BY ingest_seq)`)
	if got != schema21EntryRows {
		t.Errorf("entry contents = %q, want %q", got, schema21EntryRows)
	}
	if got := rawString(t, path, `SELECT id || ':' || name FROM workspaces`); got != "workspace-21:preserved workspace" {
		t.Errorf("workspace content = %q", got)
	}
	if got := rawString(t, path, `SELECT id || ':' || kind FROM panes`); got != "pane-21:local" {
		t.Errorf("pane content = %q", got)
	}
}

func assertSchema22Coverage(t *testing.T, db ContentDB) {
	t.Helper()
	page, err := db.Ledger().QueryEntries(context.Background(), LedgerQuery{Scope: ScopeEverywhere, Limit: 10})
	if err != nil {
		t.Fatalf("query entries after migration: %v", err)
	}
	if !page.HasRows || len(page.Entries) != 3 {
		t.Fatalf("page rows = %d (HasRows=%v), want all 3", len(page.Entries), page.HasRows)
	}
	if page.Coverage == nil || *page.Coverage != 1000 {
		t.Fatalf("coverage = %v, want oldest ended_at 1000 (NULL is ignored)", page.Coverage)
	}
	ids := make(map[string]bool, len(page.Entries))
	for _, entry := range page.Entries {
		ids[entry.ID] = true
	}
	for _, id := range []string{"entry-21-early", "entry-21-later", "entry-21-unended"} {
		if !ids[id] {
			t.Errorf("migrated page is missing %q", id)
		}
	}
}

func assertSchema22CatalogMatchesFreshDDL(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	wantDigest, wantObjects, err := shapeOfDDL(ctx, schemaV1)
	if err != nil {
		t.Fatalf("compute fresh schema 22 shape: %v", err)
	}
	conn, done := rawConn(t, path)
	defer done()
	objects, err := sqliteSchemaObjects(ctx, conn)
	if err != nil {
		t.Fatalf("read migrated schema 22 shape: %v", err)
	}
	if got := schemaObjectsDigest(objects); got != wantDigest {
		t.Errorf("migrated schema digest = %s, fresh schema digest = %s", got, wantDigest)
	}
	if got := schemaObjectNames(objects); !reflect.DeepEqual(got, wantObjects) {
		t.Errorf("migrated schema objects differ from fresh DDL: got %v, want %v", got, wantObjects)
	}
}

func TestSchema21IndexMigrationPreservesDataCoverageAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aDatabaseAtSchema21(t, path)
	assertSchema21Contents(t, path)

	first := openStore(t, path)
	assertSchema22Coverage(t, first)
	if got := rawUserVersion(t, path); got != 22 {
		t.Fatalf("schema stamp = %d, want 22", got)
	}
	if got := rawCount(t, path, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='entries_by_ended_at'`); got != 1 {
		t.Fatalf("entries_by_ended_at count = %d, want 1", got)
	}
	assertSchema22CatalogMatchesFreshDDL(t, path)
	assertSchema21Contents(t, path)
	if err := first.Close(); err != nil {
		t.Fatalf("close first migrated store: %v", err)
	}

	second := openStore(t, path)
	assertSchema22Coverage(t, second)
	if got := rawUserVersion(t, path); got != 22 {
		t.Fatalf("schema stamp after reopen = %d, want 22", got)
	}
	assertSchema21Contents(t, path)
}

func TestSchema21IndexMigrationRollsBackFailureAndCancellationThenRetries(t *testing.T) {
	for _, mode := range []string{"failure-after-ddl", "cancel-after-ddl"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.db")
			aDatabaseAtSchema21(t, path)
			conn, done := rawConn(t, path)
			defer done()

			ctx := context.Background()
			wantErr := error(nil)
			step := migrationStep{from: 21, to: 22, apply: func(ctx context.Context, tx *sql.Tx) error {
				if err := migrateAddEntriesByEndedAt21to22(ctx, tx); err != nil {
					return err
				}
				if mode == "failure-after-ddl" {
					wantErr = errors.New("injected failure after index DDL")
					return wantErr
				}
				return nil
			}}
			if mode == "cancel-after-ddl" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				step.apply = func(ctx context.Context, tx *sql.Tx) error {
					if err := migrateAddEntriesByEndedAt21to22(ctx, tx); err != nil {
						return err
					}
					cancel()
					return nil
				}
				wantErr = context.Canceled
			}

			err := applyStep(ctx, conn, step)
			if !errors.Is(err, wantErr) {
				t.Fatalf("applyStep error = %v, want %v", err, wantErr)
			}
			if got := rawUserVersion(t, path); got != 21 {
				t.Errorf("schema stamp after rollback = %d, want 21", got)
			}
			if got := rawCount(t, path, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='entries_by_ended_at'`); got != 0 {
				t.Errorf("entries_by_ended_at count after rollback = %d, want 0", got)
			}
			assertSchema21Contents(t, path)

			// Ordinary Open retries the same edge and commits its index and stamp.
			retry := openStore(t, path)
			assertSchema22Coverage(t, retry)
			if got := rawUserVersion(t, path); got != 22 {
				t.Errorf("schema stamp after retry = %d, want 22", got)
			}
			if got := rawCount(t, path, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='entries_by_ended_at'`); got != 1 {
				t.Errorf("entries_by_ended_at count after retry = %d, want 1", got)
			}
			assertSchema21Contents(t, path)
		})
	}
}

func TestMalformedSchema21AndFutureSchemaRefuseWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stamp int
		setup string
	}{
		{name: "malformed-21", stamp: 21, setup: `DROP INDEX clear_boundaries_by_pane`},
		{name: "future-23", stamp: 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.db")
			aDatabaseAtSchema21(t, path)
			if tc.setup != "" {
				rawExec(t, path, tc.setup)
			}
			if tc.stamp != 21 {
				rawExec(t, path, fmt.Sprintf("PRAGMA user_version=%d", tc.stamp))
			}
			_, err := Open(context.Background(), Config{
				Path: path, Key: schemaTestKey(),
				Budget: Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
			})
			if err == nil {
				t.Fatal("Open accepted a schema shape this build must refuse")
			}
			if got := rawUserVersion(t, path); got != tc.stamp {
				t.Errorf("refusal changed schema stamp to %d, want %d", got, tc.stamp)
			}
			if got := rawCount(t, path, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='entries_by_ended_at'`); got != 0 {
				t.Errorf("refusal created entries_by_ended_at: count %d", got)
			}
			assertSchema21Contents(t, path)
		})
	}
}
