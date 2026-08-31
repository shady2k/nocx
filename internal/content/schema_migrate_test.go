package content

// The migration ladder (nocx-lmb6v.1). These are the tests the ladder was
// developed against, not its acceptance suite — that is nocx-lmb6v.2, written
// from the criteria by somebody who did not write this code (AGENTS.md rule
// 4). What is here is the smallest set that could have driven the design:
// one real edge walked end to end, one edge killed in the middle, and one
// database the ladder cannot reach.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// aDatabaseAtSchema14 fabricates a file a real build of nocx wrote: the frozen
// testdata/schema_v14.sql, the rows the edge has to carry across, and the
// stamp. It is the fixture the whole ladder is judged on, so it deliberately
// fills the table the edge REBUILDS (grant_scopes) as well as tables it does
// not touch — a migration that keeps the rows it rewrites while losing the
// ones it ignores is the defect this bead exists to make impossible.
func aDatabaseAtSchema14(t *testing.T, path string) {
	t.Helper()
	now := time.Now().UnixMilli()
	rawExec(
		t, path,
		theSchema14Script(t),
		fmt.Sprintf(`INSERT INTO workspaces (id, name, created_at) VALUES ('ws-1', 'work', %d)`, now),
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-1', 'ws-1', 'the tab from before the upgrade')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-1', 'tab-1', '/srv', 'local')`,
		fmt.Sprintf(`INSERT INTO environments (id, kind, first_seen) VALUES ('env-1', 'local', %d)`, now),
		fmt.Sprintf(`INSERT INTO environment_observations (id, environment_id, version, observed_at, criticality)
			VALUES (1, 'env-1', 1, %d, 'routine')`, now),
		fmt.Sprintf(`INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at)
			VALUES ('e-14', 1, 'c', 'd', 'env-1', 'pane-1', '/srv', 'shell', 'user', 'echo fourteen', 'closed', 'success', %d)`, now),
		`INSERT INTO executions (id, entry_id, environment_obs_id) VALUES (1, 'e-14', 1)`,
		fmt.Sprintf(`INSERT INTO authority_grants (id, execution_id, version, issued_at, expires_at, policy)
			VALUES (1, 1, 1, %d, %d, '{}')`, now, now+60_000),
		`INSERT INTO grant_scopes (grant_id, resource_kind, resource_id) VALUES (1, 'session', 'sess-1')`,
		`INSERT INTO grant_scopes (grant_id, resource_kind, resource_id) VALUES (1, 'path', '/srv')`,
		// The counter a real schema 14 database would be carrying: one entry
		// was recorded, so the next ingest_seq is 2. A fixture that seeds
		// rows without the counter behind them is a file no build wrote.
		`UPDATE ledger_sequence SET next = 1 WHERE id = 1`,
		`PRAGMA user_version=14`,
	)
}

// theSchema14Script is the frozen fixture: the DDL a build stamping 14 ran.
func theSchema14Script(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "schema_v14.sql"))
	if err != nil {
		t.Fatalf("read the schema 14 fixture: %v", err)
	}
	return string(body)
}

// rawCount answers a single-value query on the encrypted file without going
// through Open, so "the rows are still there" can be asked of a database the
// store has REFUSED to hand out.
func rawCount(t *testing.T, path, query string) int {
	t.Helper()
	conn, done := rawConn(t, path)
	defer done()
	var n int
	if err := conn.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// rawTry runs one statement and hands back its error, which is how the SHAPE
// of a table is asserted: the widened resource-kind CHECK is not a column
// anyone can read, it is a statement that either succeeds or does not.
func rawTry(t *testing.T, path, statement string) error {
	t.Helper()
	conn, done := rawConn(t, path)
	defer done()
	_, err := conn.ExecContext(context.Background(), statement)
	return err
}

const aContentScope = `INSERT INTO grant_scopes (grant_id, resource_kind, resource_id) VALUES (1, 'content', 'note/1')`

// THE HEADLINE: a database one version behind opens, and every row is still
// there afterwards.
//
// It used to be dropped and rebuilt, which was correct while content.db was
// local and disposable and stopped being correct the moment a remote server
// owns the only copy (nocx-lmb6v). The edge exercised here is the real one —
// 14 to 15 widened the grant_scopes resource-kind CHECK, which SQLite cannot
// ALTER, so the step rebuilds the table and copies the rows; the assertion
// that the widening actually happened is that a scope this build understands
// and 14 did not now inserts.
func TestADatabaseOneVersionBehindIsMigratedAndKeepsEveryRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aDatabaseAtSchema14(t, path)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a schema 14 database: %v — a version behind must migrate, not refuse", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	page, err := db.Ledger().QueryEntries(context.Background(), LedgerQuery{Scope: ScopeEverywhere, Limit: 50})
	if err != nil {
		t.Fatalf("QueryEntries after the migration: %v", err)
	}
	found := false
	for _, e := range page.Entries {
		if e.Intent == "echo fourteen" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the command recorded under schema 14 is gone: %+v", page.Entries)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM grant_scopes`); n != 2 {
		t.Fatalf("grant_scopes holds %d rows after the migration, want 2 — the table rebuild lost rows", n)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM tabs`); n != 1 {
		t.Fatalf("tabs holds %d rows after the migration, want 1", n)
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the file was not stamped for the schema it now holds", got, schemaVersion)
	}
	if err := rawTry(t, path, aContentScope); err != nil {
		t.Fatalf("a 'content' scope is still refused after the migration: %v — the edge did not widen the CHECK", err)
	}
	// And the store WORKS on the migrated shape, which is the half a
	// version stamp cannot prove (nocx-rtg0.17: a file that opened
	// perfectly and then failed every INSERT is what bought the stamp).
	if _, err := db.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo fifteen")); err != nil {
		t.Fatalf("RecordCompleted after the migration: %v", err)
	}
}

// THE CRASH-SAFETY INTERVAL, stated as a test: from before a step's first
// write until user_version names the new edge, either the whole edge is
// applied or none of it is.
//
// The failure is injected into the REAL edge rather than a synthetic one —
// the step's own DDL runs, and then the step reports failure, which is
// exactly the shape of a statement that fails half way through an edge. What
// the file must show afterwards is the OPENING state: stamp 14, the rows, and
// a grant_scopes that still refuses the kind the edge was widening it to
// accept. Then the next start finishes the job, because a rolled-back edge is
// simply an edge that has not run yet.
func TestAnEdgeThatFailsPartWayLeavesTheDatabaseWhollyAtTheVersionItStarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aDatabaseAtSchema14(t, path)

	boom := errors.New("killed in the middle of the edge")
	ladder := []migrationStep{{
		from: 14, to: 15,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := migrateGrantScopeKinds14to15(ctx, tx); err != nil {
				return err
			}
			return boom
		},
	}}

	conn, done := rawConn(t, path)
	err := migrateSchema(context.Background(), conn, ladder, log.NewSlogAdapter(nil))
	done()
	if !errors.Is(err, boom) {
		t.Fatalf("migrateSchema returned %v, want the injected failure — a step that fails must fail the migration", err)
	}

	if got := rawUserVersion(t, path); got != 14 {
		t.Fatalf("user_version = %d, want 14 — the stamp outran its own edge", got)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM grant_scopes`); n != 2 {
		t.Fatalf("grant_scopes holds %d rows after the failed edge, want 2", n)
	}
	if scopeErr := rawTry(t, path, aContentScope); scopeErr == nil {
		t.Fatal("a 'content' scope inserted after the edge FAILED — half of the edge survived its own rollback")
	}

	// The next start completes it. Same file, no fixture repair, no manual
	// step: the ladder simply finds a database at 14 again.
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open after a failed migration: %v — the next start must complete it or refuse, and refusing a file it can migrate is neither", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d after the retry, want %d", got, schemaVersion)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM grant_scopes`); n != 2 {
		t.Fatalf("grant_scopes holds %d rows after the retry, want 2", n)
	}
}

// THE MECHANISM THE INTERVAL RESTS ON, asserted on its own: `PRAGMA
// user_version` is an ordinary write inside the transaction that issues it,
// so it rolls back with everything else.
//
// The test above cannot show this. Its injected failure happens BEFORE the
// stamp is written, so what it proves is that the edge's DDL rolls back —
// which leaves the interesting half unasserted: a crash between the stamp and
// the COMMIT. If the stamp were to escape its transaction, that crash would
// produce exactly the file the protocol forbids, holding the rows of one
// version under the number of the next, and every test in this package would
// still be green. So it is asserted here directly, against the driver and the
// encrypting VFS this store actually uses rather than against SQLite's
// documentation.
func TestAVersionStampRollsBackWithItsOwnTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(t, path, `CREATE TABLE probe (id INTEGER PRIMARY KEY) STRICT`, `PRAGMA user_version=14`)

	conn, done := rawConn(t, path)
	defer done()
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version=15"); err != nil {
		t.Fatalf("stamp inside the transaction: %v", err)
	}
	// Visible to the writer that made it — otherwise the assertion below
	// would pass for the wrong reason, on a pragma that never took effect.
	var inside int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&inside); err != nil {
		t.Fatalf("read the stamp inside the transaction: %v", err)
	}
	if inside != 15 {
		t.Fatalf("user_version inside the transaction = %d, want 15 — the pragma did nothing", inside)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if got := rawUserVersion(t, path); got != 14 {
		t.Fatalf("user_version = %d after the rollback, want 14 — the stamp outlives its transaction, so a crash between the stamp and the commit leaves a half-migrated file", got)
	}
}

// A database BELOW the ladder's first rung is refused, and nothing in it is
// touched. This is the case the old code rebuilt: it is the only remaining
// answer that is neither "migrate" nor "open", and it must never become
// "drop".
func TestADatabaseBelowTheLadderIsRefusedWithEveryRowIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(
		t, path,
		`CREATE TABLE command_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL, cwd TEXT NOT NULL, host TEXT NOT NULL,
			status TEXT NOT NULL) STRICT`,
		`INSERT INTO command_history (command, cwd, host, status) VALUES ('echo ancient', '/', '', 'success')`,
		`PRAGMA user_version=1`,
	)

	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open accepted a database the ladder has no step for")
	}
	for _, want := range []string{"1", fmt.Sprintf("%d", schemaVersion), "no rows were discarded"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q — the refusal is what a person reads in Settings", err, want)
		}
	}
	if n := rawCount(t, path, `SELECT count(*) FROM command_history`); n != 1 {
		t.Fatalf("command_history holds %d rows after the refusal, want 1 — the refusal destroyed data", n)
	}
	if got := rawUserVersion(t, path); got != 1 {
		t.Fatalf("user_version = %d after the refusal, want 1 — a refusal that restamps invites the next open to migrate a shape it never checked", got)
	}
}

// The ladder is a CHAIN, and a gap in it is a defect that must be found by a
// test rather than by a user whose database stops half way. Contiguous,
// ascending, one version per step, ending exactly at the version this build
// creates from scratch.
func TestTheLadderIsAContiguousChainEndingAtTheCurrentSchema(t *testing.T) {
	if err := validateLadder(schemaLadder); err != nil {
		t.Fatalf("the shipped ladder is malformed: %v", err)
	}
	if len(schemaLadder) == 0 {
		t.Fatal("the ladder has no steps — a migration engine with no edge is a feature nobody can reach")
	}
	if last := schemaLadder[len(schemaLadder)-1].to; last != schemaVersion {
		t.Fatalf("the ladder ends at %d and this build creates %d", last, schemaVersion)
	}

	// And the validation is not vacuous: a gap is reported.
	gapped := []migrationStep{
		{from: 1, to: 2, apply: func(context.Context, *sql.Tx) error { return nil }},
		{from: 3, to: 4, apply: func(context.Context, *sql.Tx) error { return nil }},
	}
	if err := validateLadder(gapped); err == nil {
		t.Fatal("validateLadder accepted a ladder with a hole between 2 and 3")
	}
}
