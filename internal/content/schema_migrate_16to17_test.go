package content

// THE 16→17 RUNG: executions.termination_reason learns `answer-revoked`
// (nocx-4yjwk.7).
//
// The rung is a TABLE REBUILD, which is where rows go missing, so what is
// asserted here is not "the migration ran" but "the executions a person had
// are still there, whole, with the reasons they were closed under". Every
// value below is distinctive rather than plausible, for the reason
// aReleasedSchema14Database gives: a copy that pairs the right number of rows
// with the wrong values in them satisfies any count you could assert.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// aReleasedSchema16Database writes the file a shipped schema 16 build would
// have: the frozen released DDL, executions in the table the rung REBUILDS —
// one per reason 16 knew, plus a live one with no reason at all — and the
// stamp.
//
// It is built from testdata/schema_v16.sql rather than from the current
// schemaV1, which is the whole point: a fixture the current code produced
// would test the migration against the migration's own idea of the past.
func aReleasedSchema16Database(t *testing.T, path string) {
	t.Helper()
	statements := []string{
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		releasedSchema(t, 16),
		`INSERT INTO workspaces (id, name, created_at) VALUES ('ws-sixteen', 'the workspace from before the widening', 1600)`,
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-sixteen', 'ws-sixteen', 'the tab the user named')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-sixteen', 'tab-sixteen', '/srv/sixteen', 'local')`,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env-sixteen', 'local', 1600)`,
		`INSERT INTO environment_observations (id, environment_id, version, observed_at, criticality)
			VALUES (1, 'env-sixteen', 1, 1600, 'routine')`,
		`INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at)
			VALUES ('entry-sixteen', 1, 'client-sixteen', 'digest-entry', 'env-sixteen', 'pane-sixteen',
			        '/srv/sixteen', 'shell', 'user', 'echo the command that must survive the widening', 'closed', 'success', 1600)`,
	}
	// One execution per reason schema 16 admitted, each with a distinctive
	// executor string, so a copy that transposed the columns is visible.
	for i, reason := range reasonsSchema16Knew {
		statements = append(statements, fmt.Sprintf(
			`INSERT INTO executions (id, entry_id, environment_obs_id, attempt, started_at, ended_at,
				termination_reason, executor, state, payload)
			 VALUES (%d, 'entry-sixteen', 1, %d, %d, %d, '%s', 'executor-for-%s', 'completed', '{"kept":"%s"}')`,
			i+1, i+1, 1600+i, 1700+i, reason, reason, reason))
	}
	statements = append(statements,
		// A run still in flight when the upgrade happens: no reason at all,
		// which is the row a NOT NULL or a botched default would destroy.
		`INSERT INTO executions (id, entry_id, environment_obs_id, attempt, started_at, state, payload)
			VALUES (99, 'entry-sixteen', 1, 99, 1690, 'streaming', '{"kept":"in-flight"}')`,
		// A dependent that must still point at its execution afterwards. The
		// rung drops and recreates the table the foreign key names, so this is
		// the row `PRAGMA foreign_key_check` is asked about inside the step.
		`INSERT INTO authority_grants (id, execution_id, version, issued_at, expires_at, policy)
			VALUES (1, 1, 1, 1600, 1660000, '{"kept":"yes"}')`,
		`INSERT INTO artifacts (id, entry_id, execution_id, media_type)
			VALUES ('artifact-sixteen', 'entry-sixteen', 2, 'text/plain')`,
		`UPDATE ledger_sequence SET next = 1 WHERE id = 1`,
		`PRAGMA user_version=16`,
	)
	rawExec(t, path, statements...)
}

// reasonsSchema16Knew is the CHECK's vocabulary at 16, frozen. It is written
// out rather than read from the Go constants deliberately: the constants now
// include the tenth value, and a fixture that followed them would be a schema
// 17 database wearing a 16 stamp.
var reasonsSchema16Knew = []string{
	"completed", "failed", "timeout", "transport-gone", "user-killed",
	"agent-declined", "interrupted", "inactivity", "output-budget",
}

// THE HEADLINE: a real schema 16 database with executions in it comes across
// the rung with every row intact, and the widened CHECK then accepts the
// reason 16 could not store.
func TestAReleasedSchema16DatabaseMigratesAndKeepsEveryExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema16Database(t, path)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a released schema 16 database: %v — a version behind must migrate, not refuse", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}

	// EVERY EXECUTION, AS A WHOLE TUPLE. A rebuild that copies the right
	// number of rows with the columns transposed passes every count.
	for i, reason := range reasonsSchema16Knew {
		id := i + 1
		got := rawRow(t, path, fmt.Sprintf(
			`SELECT attempt || '|' || started_at || '|' || ended_at || '|' ||
			        termination_reason || '|' || executor || '|' || state || '|' || payload
			 FROM executions WHERE id = %d`, id))
		want := fmt.Sprintf(`%d|%d|%d|%s|executor-for-%s|completed|{"kept":"%s"}`,
			id, 1600+i, 1700+i, reason, reason, reason)
		if got != want {
			t.Fatalf("execution %d after the rung = %q, want %q — the rebuild did not copy the row it was given", id, got, want)
		}
	}
	// THE IN-FLIGHT RUN, AT BOTH ENDS OF THE OPEN. Across the RUNG it keeps
	// its empty reason — a rebuild that invented a default for the column
	// would put a reason on a run nothing had decided about. It is the STARTUP
	// SWEEP, afterwards, that closes it as interrupted, and that is a
	// different owner doing a different job: the rung carries what is there,
	// the sweep ends what the last process left open.
	if got := rawRow(t, path, `SELECT ifnull(termination_reason, 'NULL') || '|' || state FROM executions WHERE id = 99`); got != "interrupted|interrupted" {
		t.Fatalf("the in-flight execution after Open = %q, want %q — the startup sweep closes what the last process left running", got, "interrupted|interrupted")
	}
	assertTheRungItselfLeavesAnUnfinishedRunUntouched(t)
	if n := rawCount(t, path, `SELECT count(*) FROM executions`); n != len(reasonsSchema16Knew)+1 {
		t.Fatalf("executions holds %d rows after the rung, want %d", n, len(reasonsSchema16Knew)+1)
	}
	// The dependents still name their executions: the rung drops the table
	// their foreign keys point at.
	if n := rawCount(t, path,
		`SELECT count(*) FROM authority_grants g JOIN executions e ON e.id = g.execution_id`); n != 1 {
		t.Fatalf("the grant no longer joins to its execution (%d rows) — the rebuild broke the reference", n)
	}
	if n := rawCount(t, path,
		`SELECT count(*) FROM artifacts a JOIN executions e ON e.id = a.execution_id`); n != 1 {
		t.Fatalf("the artifact no longer joins to its execution (%d rows) — the rebuild broke the reference", n)
	}

	// AND THE WIDENING ACTUALLY HAPPENED, which is the only reason the rung
	// exists: the value schema 16 refused now inserts.
	if err := rawTry(t, path,
		`INSERT INTO executions (entry_id, environment_obs_id, termination_reason)
		 VALUES ('entry-sixteen', 1, 'answer-revoked')`); err != nil {
		t.Fatalf("`answer-revoked` is still refused after the migration: %v — the rung did not widen the CHECK", err)
	}
	// And it is a WIDENING, not a removal: the CHECK still says no.
	if err := rawTry(t, path,
		`INSERT INTO executions (entry_id, environment_obs_id, termination_reason)
		 VALUES ('entry-sixteen', 1, 'answer-unrevoked')`); err == nil {
		t.Fatal("the rebuilt executions table accepted a reason no Go constant names — the rung replaced the CHECK with nothing")
	}
}

// AND THE UPGRADED FILE OPENS AGAIN.
//
// This is the assertion the shape guard was missing, and its absence was a
// live defect rather than a gap (nocx-4yjwk.7): `validateOnDiskSchemaShapeFor`
// digested the stored DDL VERBATIM, and a rebuild re-emits the rebuilt table's
// name quoted, so a database that had successfully migrated was refused on its
// NEXT open with "stamp and contents disagree" — telling a person to update to
// the build they were already running. Every rung that widens a CHECK rebuilds
// a table, so every upgraded database was one restart away from it.
//
// It is asserted over a SECOND close and open rather than one, because the
// first open is the one that migrates and it is the second that reads the
// stamp it wrote.
func TestAnUpgradedDatabaseOpensAgainAfterTheMigrationThatWroteIt(t *testing.T) {
	for _, from := range []int{14, 16} {
		t.Run(fmt.Sprintf("from schema %d", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.db")
			if from == 14 {
				aReleasedSchema14Database(t, path)
			} else {
				aReleasedSchema16Database(t, path)
			}
			first, err := Open(context.Background(), Config{
				Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
			})
			if err != nil {
				t.Fatalf("the migrating open failed: %v", err)
			}
			if closeErr := first.Close(); closeErr != nil {
				t.Fatalf("close the migrated database: %v", closeErr)
			}

			second, err := Open(context.Background(), Config{
				Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
			})
			if err != nil {
				t.Fatalf("the SECOND open of a database this build itself migrated was refused: %v\n\n"+
					"The migration worked and the file is stamped %d. Refusing it now tells a person to update to the "+
					"build they are running and takes their history away on an ordinary restart. A rebuilt table's DDL "+
					"differs from schemaV1's only in typography (SQLite re-emits the name quoted after RENAME), which is "+
					"why the shape digest normalises before it hashes — see schema_shape_normalise.go.", err, schemaVersion)
			}
			t.Cleanup(func() { _ = second.Close() })

			// And it is genuinely usable, not merely openable.
			if _, err := second.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo after the restart")); err != nil {
				t.Fatalf("RecordCompleted on the reopened database: %v", err)
			}
		})
	}
}

// A NEWER FILE IS STILL REFUSED, at the new version. The direction rule is
// what stops an older build destroying rows it cannot read, and a schema bump
// is exactly the moment to check it still holds rather than to assume it.
func TestADatabaseStampedAboveSchema17IsStillRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema16Database(t, path)
	rawExec(t, path, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1))

	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatalf("Open accepted a database stamped %d while this build creates %d", schemaVersion+1, schemaVersion)
	}
	for _, want := range []string{
		fmt.Sprintf("schema %d", schemaVersion+1),
		"no rows were discarded",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal reads %q, want it to contain %q — it is what a person reads in Settings", err, want)
		}
	}
	if got := rawUserVersion(t, path); got != schemaVersion+1 {
		t.Fatalf("user_version = %d after the refusal, want %d — a refusal that restamps invites the next open to migrate a shape it never checked",
			got, schemaVersion+1)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM executions`); n != len(reasonsSchema16Knew)+1 {
		t.Fatalf("executions holds %d rows after the refusal, want %d — the refusal destroyed data", n, len(reasonsSchema16Knew)+1)
	}
}

// rawRow answers a single-string query on the encrypted file without going
// through Open, so a whole tuple can be compared as one value.
func rawRow(t *testing.T, path, query string) string {
	t.Helper()
	conn, done := rawConn(t, path)
	defer done()
	var got string
	if err := conn.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

// assertTheRungItselfLeavesAnUnfinishedRunUntouched walks the ladder WITHOUT
// the rest of Open, so the row is read between the rung and the startup sweep
// — the one window in which "the rebuild invented a reason" and "the sweep
// wrote one" are distinguishable at all.
func assertTheRungItselfLeavesAnUnfinishedRunUntouched(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema16Database(t, path)
	conn, done := rawConn(t, path)
	err := migrateSchema(context.Background(), conn, schemaLadder, log.NewSlogAdapter(nil))
	done()
	if err != nil {
		t.Fatalf("walk the ladder over a schema 16 database: %v", err)
	}
	if got := rawRow(t, path, `SELECT ifnull(termination_reason, 'NULL') || '|' || ifnull(state, 'NULL') FROM executions WHERE id = 99`); got != "NULL|streaming" {
		t.Fatalf("the unfinished execution after the rung alone = %q, want %q — the rebuild must carry an empty reason across as empty, not fill it in", got, "NULL|streaming")
	}
}
