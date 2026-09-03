package content

// THE API-RUN TABLES ARE INSIDE THE LADDER (nocx-lmb6v.5), AND ONE COUNTER
// ANSWERS "WHAT SHAPE IS THIS FILE IN" (nocx-lmb6v.3).
//
// Until schema 16, `Open`'s schema sequence was THREE steps and only two of
// them were the protocol: the ladder walk, `schemaV1`, and then a third that
// created the `api_run*` tables and versioned them through a private
// `api_run_schema` table — outside `user_version`, outside `schemaLadder`,
// and outside every refusal the ladder makes. Roughly a quarter of the tables
// in the file were therefore governed by nothing this epic built.
//
// These are the assertions that pin the fold, and the first is the hole: a
// file whose api-run tables were written by a build ahead of this one used to
// be WALKED — the ladder migrated it and stamped it, and only then did the
// third step notice and refuse. A refusal that has already modified the file
// is not the refusal the protocol names.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// ── the fixture: a database from the two-counter era ──────────────────────

// theTwoCounterEraAPIRunScript is the frozen DDL those builds ran. It lives in
// testdata rather than in this file for the reason every released fixture
// does: it must not be regenerated from code that no longer writes it.
func theTwoCounterEraAPIRunScript(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "api_runs_two_counter_era.sql"))
	if err != nil {
		t.Fatalf("the frozen two-counter-era api-run DDL is missing: %v", err)
	}
	return string(body)
}

// aTwoCounterEraDatabase writes what a real released build left on disk: a
// released schema 14 ledger AND the api-run tables that build's third step
// created beside it, with a run in them and the private counter seeded.
//
// `apiRunCounter` is the whole point of the parameter. 1 is what every
// released build wrote. Anything above it is the file this build must refuse
// — and it is representable at all precisely because the two counters were
// independent, which is the defect: a file could be stamped at any base
// version with api-run tables at any version of their own.
func aTwoCounterEraDatabase(t *testing.T, path string, apiRunCounter int) {
	t.Helper()
	aReleasedSchema14Database(t, path)
	rawExec(
		t, path,
		`INSERT INTO api_runs
			(id, collection_path, request_rel_path, method, url, outcome,
			 request_spans, metadata, started_at, ended_at, logical_bytes)
			VALUES (1, 'collections/the-one-that-must-survive', 'requests/ping.http',
			        'GET', 'https://example.invalid/ping', 'answered', '[]', '{}', 1400, 1401, 9)`,
		`INSERT INTO api_run_artifacts (id, run_id, kind, byte_len, chunk_count) VALUES (1, 1, 'request', 9, 1)`,
		`INSERT INTO api_run_artifact_chunks (artifact_id, seq, body) VALUES (1, 1, CAST('GET /ping' AS BLOB))`,
		fmt.Sprintf(`UPDATE api_run_schema SET version = %d WHERE id = 1`, apiRunCounter),
		`PRAGMA user_version=14`,
	)
}

func aTableExists(t *testing.T, path, name string) bool {
	t.Helper()
	return rawCount(t, path,
		fmt.Sprintf("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='%s'", name)) == 1
}

// ── the hole ──────────────────────────────────────────────────────────────

// A FILE WHOSE API-RUN TABLES ARE AHEAD OF THIS BUILD IS REFUSED BEFORE
// ANYTHING IS WALKED, AND NOT ONE BYTE OF IT MOVES.
//
// This is the epic's own third row — `onDisk > current → REFUSE, without
// modifying a byte` — applied to the tables it never covered. The old
// sequence refused this file, but only after the ladder had carried it across
// 14→15 and committed the stamp: the decision was made by the wrong owner,
// two steps too late, on a file that had already been rewritten.
//
// Asserted against `migrateSchema` rather than through `Open` for the same
// reason TestMigrateRefusesADatabaseWrittenByANewerSchemaWithoutTouchingAByte
// is: `Open`'s prologue puts the file into WAL before any decision is
// reached, so byte identity is a property of the refusal itself and not of
// the caller around it.
func TestAFileWhoseAPIRunTablesAreAheadOfThisBuildIsRefusedBeforeTheLadderWalks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aTwoCounterEraDatabase(t, path, 2)
	before := fingerprint(t, path)

	conn, done := rawConn(t, path)
	err := migrateSchema(context.Background(), conn, schemaLadder, log.NewSlogAdapter(nil))
	done()

	if err == nil {
		t.Fatal("migrateSchema accepted a file whose api-run tables were written by a newer build — " +
			"the api_run* tables are a quarter of this database and the protocol's refusal has to cover them too")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Fatalf("the refusal reads %q; it must name the api-run tables so the person reading it knows which part of the file is ahead", err.Error())
	}
	if !strings.Contains(err.Error(), "no rows were discarded") {
		t.Fatalf("the refusal reads %q; every refusal in this ladder promises the rows are still there", err.Error())
	}

	after := fingerprint(t, path)
	if after.size != before.size || after.sum != before.sum {
		t.Fatalf("the file changed across the refusal: %d bytes / %s became %d bytes / %s — "+
			"the ladder walked a file it was going to refuse, which is the whole defect",
			before.size, before.sum, after.size, after.sum)
	}
}

// AND THE PRODUCT REFUSES IT TOO, with the file still answering the version
// it arrived with. The test above pins the decision; this one pins what a
// person actually meets, because `Open` is the only seam anybody reaches.
func TestOpenRefusesADatabaseWhoseAPIRunTablesAreAheadOfThisBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aTwoCounterEraDatabase(t, path, 2)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		_ = db.Close()
		t.Fatal("Open handed out a store over a database whose api-run tables this build cannot read")
	}
	if got := rawUserVersion(t, path); got != 14 {
		t.Fatalf("user_version = %d after the refusal, want the 14 the file arrived with — "+
			"a refused file must not be carried anywhere", got)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM api_runs`); n != 1 {
		t.Fatalf("api_runs holds %d rows after the refusal, want the 1 it arrived with", n)
	}
	assertTheRowsOfSchema14(t, path)
}

// ── one counter ───────────────────────────────────────────────────────────

// ONE PLACE ANSWERS WHAT SHAPE THIS FILE IS IN.
//
// A fresh database carries the api-run tables and carries NO second version
// counter: `user_version` is the whole answer. Two counters for one question
// is the duplicate-owner shape AGENTS.md spends a section on — they agree
// everywhere anybody looks, and on the day they disagree nothing says which
// one is right.
func TestOneVersionCounterAnswersWhatShapeTheFileIsIn(t *testing.T) {
	path := aFreshDatabase(t)

	if aTableExists(t, path, "api_run_schema") {
		t.Fatal("a fresh database still carries api_run_schema — a second version counter answering the same question as user_version")
	}
	for _, table := range []string{"api_runs", "api_run_artifacts", "api_run_artifact_chunks"} {
		if !aTableExists(t, path, table) {
			t.Fatalf("a fresh database has no %s table — folding the api-run tables into the ladder must not lose them", table)
		}
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
}

// ── the rung ──────────────────────────────────────────────────────────────

// A DATABASE FROM THE TWO-COUNTER ERA WALKS FORWARD AND KEEPS ITS API RUNS.
//
// The retirement is an ordinary rung and is judged the way every other rung
// is: the rows are still there afterwards, and they still say what they said.
// Without this the refusal above would be satisfied by a build that simply
// refused every database with api-run tables in it.
func TestADatabaseFromTheTwoCounterEraKeepsItsAPIRunsAcrossTheRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aTwoCounterEraDatabase(t, path, 1)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a two-counter-era database: %v — the ordinary released shape must migrate, not refuse", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if aTableExists(t, path, "api_run_schema") {
		t.Fatal("api_run_schema survived the walk — the second counter is retired by a rung, not left behind as a table nothing owns")
	}
	assertTheRowsOfSchema14(t, path)

	// Read back through the seam a person reaches, not off the raw file: a
	// run whose row survives but whose artifact chunks do not is a run the
	// API panel shows empty.
	run, err := db.APIRuns().Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("the api run recorded before the upgrade cannot be read afterwards: %v", err)
	}
	if run.CollectionPath != "collections/the-one-that-must-survive" {
		t.Fatalf("the api run's collection is %q after the walk", run.CollectionPath)
	}
	if run.Request.Text != "GET /ping" {
		t.Fatalf("the api run's request text is %q after the walk, want %q — the artifact chunks did not come through",
			run.Request.Text, "GET /ping")
	}
}

// ── and the parity test sees them ─────────────────────────────────────────

// THE API-RUN TABLES ARE IN THE FRESH-VERSUS-MIGRATED COMPARISON, and the
// comparison is not vacuous for them.
//
// nocx-lmb6v.7's parity test starts from testdata/schema_v14.sql, which has no
// api-run tables at all, so after the fold BOTH of its databases get them from
// `schemaV1` and the comparison cannot see a divergence in them even in
// principle. A real released 14 database DID have them — its build's third
// step created them — and `schemaV1` is `IF NOT EXISTS` throughout, so it
// cannot repair a table that is already there. This is therefore the
// population the comparison has to cover: an upgraded file whose api-run
// tables came from the two-counter era, against a fresh one whose came from
// `schemaV1`.
func TestTheUpgradedAPIRunTablesHaveTheSameShapeAsAFreshInstalls(t *testing.T) {
	fresh := aFreshDatabase(t)

	upgraded := filepath.Join(t.TempDir(), "content.db")
	aTwoCounterEraDatabase(t, upgraded, 1)
	db, err := Open(context.Background(), Config{
		Path: upgraded, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a two-counter-era database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the migrated database: %v", err)
	}

	if diff := schemaDifference(t, fresh, upgraded); diff != "" {
		t.Fatalf("a database upgraded from the two-counter era has a different shape from a fresh one:\n%s\n\n"+
			"The api-run tables an old build created are already there when `schemaV1` runs, and `IF NOT EXISTS` "+
			"declines to repair them. Whatever shape that build left is what an upgraded user keeps, forever, "+
			"unless a rung changes it.", diff)
	}
}
