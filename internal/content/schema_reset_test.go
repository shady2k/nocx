package content

// What a schema DIFFERENCE does to a file, from both directions.
//
// It was a rebuild when this file was written (nocx-rtg0.17) and it is a
// migration or a refusal now (nocx-lmb6v.1); what has not changed is why the
// tests are internal rather than external. The reproductions have to reach
// the encrypted file the way Open does — the keyed URI and the driver — to
// put it into a state no public API can produce: a shape from a released
// build, and a user_version that says so.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

func schemaTestKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// rawExec runs statements against the encrypted file the way Open does,
// without going through Open — the only way to fabricate an out-of-date file.
func rawExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("raw exec %q: %v", s, err)
		}
	}
}

func rawUserVersion(t *testing.T, path string) int {
	t.Helper()
	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

func openStore(t *testing.T, path string) ContentDB {
	t.Helper()
	db, err := Open(context.Background(), Config{
		Path:   path,
		Key:    schemaTestKey(),
		Budget: Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// WHAT USED TO BE HERE, AND WHERE ITS PROMISE WENT (nocx-lmb6v.1).
//
// This file was written when a schema difference in either direction meant a
// REBUILD: every table this build owns dropped in one transaction and the
// discarded row count announced to the user. Four tests guarded that
// behaviour and none of them survives, because the behaviour is deliberately
// gone — a database the ladder has no step for is now REFUSED, and nothing
// drops a table for a version difference any more.
//
//   - "rebuilds a database written by an older schema" and "discards a file
//     whose foreign keys cross the drop order": both fixtures are files from
//     before the ladder's first rung. Their promise is now the opposite one
//     and is asserted in schema_migrate_test.go —
//     TestADatabaseBelowTheLadderIsRefusedWithEveryRowIntact keeps the same
//     command_history fixture and requires the rows to still be there.
//   - "refuses a file with tables it does not know": the unknown-table gate
//     was a membership test in front of the demolition — it existed to stop
//     the rebuild dropping something unaccounted for. With no demolition
//     there is nothing for it to gate, and a file below the chain is refused
//     for its version rather than for its table names.
//   - "drops the layout chain including self-referencing tabs" and "discards
//     a file holding a nested entry": both existed because DROP TABLE fires
//     ON DELETE actions, and entries.parent_id is a self-reference that no
//     drop order could satisfy. Nothing is dropped now, so the hazard is
//     unreachable; the two fixtures are kept below and assert the opposite —
//     that the rows come through the upgrade.

// The other side of the interval: a file this build wrote is opened again and
// again without losing anything. A reset that fires when it should not is the
// same defect wearing the opposite sign.
func TestReopeningACurrentDatabaseKeepsItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")

	first := openStore(t, path)
	if _, err := first.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo keep")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i := range 2 {
		again := openStore(t, path)
		page, err := again.Ledger().QueryEntries(context.Background(), LedgerQuery{Scope: ScopeEverywhere, Limit: 50})
		if err != nil {
			t.Fatalf("Query on reopen %d: %v", i, err)
		}
		if len(page.Entries) != 1 || page.Entries[0].Intent != "echo keep" {
			t.Fatalf("reopen %d: entries = %+v, want the row to survive", i, page.Entries)
		}
		if err := again.Close(); err != nil {
			t.Fatalf("Close on reopen %d: %v", i, err)
		}
	}
}

// A brand-new file is a creation, not a reset — nothing is announced as data
// loss, and the stamp is written so the next open is a no-op.
func TestFreshDatabaseIsStampedAndNotReportedAsAReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db := openStore(t, path)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
}

// ── nocx-lmb6v.1: an upgrade carries the rows across ──────────────────────

// rawTableNames lists the user tables on the raw file — the read-back
// assertion for "the migrated file holds the shape this build creates".
func rawTableNames(t *testing.T, path string) []string {
	t.Helper()
	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("raw list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("raw scan table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("raw list tables: %v", err)
	}
	return names
}

// rawRowCount counts rows on the raw file, bypassing Open.
func rawRowCount(t *testing.T, path, table string) int {
	t.Helper()
	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil { //nolint:gosec // constant table names from the test
		t.Fatalf("raw count %s: %v", table, err)
	}
	return n
}

// THE UPGRADE A USER ACTUALLY MEETS: the tabs and panes they had open are
// still there afterwards.
//
// The fixture is the layout chain that used to prove the DEMOLITION was
// survivable — tabs.parent_id references tabs, so the implicit DELETE FROM
// behind DROP TABLE met the table's own rows on the way out — and it is kept
// because it is the shape most likely to break a table rebuild too. What it
// asserts is now the opposite: the workspace, both tabs and the pane come
// through the version change, which before this bead was the one thing a
// schema bump guaranteed they would not do.
func TestAnUpgradeKeepsTheTabsAndPanesTheUserHadOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db := openStore(t, path)
	ctx := context.Background()
	layout := db.Layout()
	if _, err := layout.CreateWorkspace(ctx, Workspace{ID: "ws-1", Name: "work"},
		Tab{ID: "tab-1", WorkspaceID: "ws-1", Layout: LayoutRow},
		Pane{ID: "pane-0", TabID: "tab-1", Cwd: "/", Kind: PaneLocal, SizeShare: 1}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	parent := "tab-1"
	if _, err := layout.CreateTab(ctx, Tab{ID: "tab-2", WorkspaceID: "ws-1", ParentID: &parent, Layout: LayoutRow},
		Pane{ID: "pane-1", TabID: "tab-2", Cwd: "/", Kind: PaneLocal, SizeShare: 1}); err != nil {
		t.Fatalf("CreateTab child: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restamp it as the version below: the next Open must MIGRATE it.
	rawExec(t, path, "PRAGMA user_version=14")

	again := openStore(t, path)
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the migration did not complete", got, schemaVersion)
	}
	spaces, err := again.Layout().Workspaces(ctx)
	if err != nil {
		t.Fatalf("Workspaces after the migration: %v", err)
	}
	if len(spaces) != 1 || spaces[0].ID != "ws-1" {
		t.Fatalf("workspaces after the migration = %+v, want the one the user had", spaces)
	}
	tabs, err := again.Layout().Tabs(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Tabs after the migration: %v", err)
	}
	if len(tabs) != 2 {
		t.Fatalf("tabs after the migration = %+v, want both", tabs)
	}
	panes, err := again.Layout().Panes(ctx, "tab-1")
	if err != nil {
		t.Fatalf("Panes after the migration: %v", err)
	}
	if len(panes) != 1 || panes[0].ID != "pane-0" {
		t.Fatalf("panes after the migration = %+v, want the one in tab-1", panes)
	}
}

// A file holding a NESTED entry comes through with its tree intact.
//
// This fixture is from the nocx-dev stand (2026-08-30), where it was the
// reproduction for a rebuild that could not finish: entries.parent_id is a
// SELF-reference with ON DELETE SET NULL, so the implicit delete inside
// `DROP TABLE entries` nulled every child's parent_id and the table's own
// `CHECK (parent_id IS NOT NULL OR pos IS NULL)` then refused the row that
// still held a seat. Open failed, the app fell back to the content stub, and
// every user with one nested block was pinned to the old schema for good.
//
// Nothing drops now, so that hazard is gone by construction — and the file is
// kept as a fixture for the stronger claim: both rows, and the parent link
// between them, are still on disk afterwards.
//
// The tables around them are the frozen schema 14 script rather than the two
// hand-written columns this fixture used to carry. That mattered not at all
// while the answer was a demolition — every table went — and it matters now:
// a file STAMPED 14 whose entries table is not the shape 14 had is a file no
// build ever wrote, and what happens to one is a question about corruption,
// not about migration.
func TestAFileHoldingANestedEntryIsMigratedWithItsTreeIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(
		t, path,
		theSchema14Script(t),
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent, phase, status, submitted_at)
			VALUES ('root', 1, 'c', 'd', 'env', NULL, NULL, '/', 'shell', 'user', 'x', 'closed', 'success', 0)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent, phase, status, submitted_at)
			VALUES ('child', 2, 'c', 'd', 'env', 'root', 0, '/', 'text', 'user', '', 'closed', 'success', 0)`,
		`UPDATE ledger_sequence SET next = 2 WHERE id = 1`,
		`PRAGMA user_version=14`,
	)
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a file with a nested entry: %v — the migration refused a file it can carry, so the app runs on the stub", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the file was not migrated", got, schemaVersion)
	}
	if n := rawRowCount(t, path, "entries"); n != 2 {
		t.Fatalf("entries rows = %d after the migration, want both — the tree was not carried across", n)
	}
	if n := rawRowCount(t, path, "entries WHERE id = 'child' AND parent_id = 'root'"); n != 1 {
		t.Fatal("the child no longer names its parent — the tree came across broken")
	}
	// And the tables this build owns and the fixture never had are there,
	// created by schemaV1 straight after the walk: a migration that left the
	// file short of a table opens perfectly and fails every statement.
	names := rawTableNames(t, path)
	for _, want := range []string{"grant_scopes", "panes", "tabs", "workspaces"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tables after the migration = %v, want %q among them", names, want)
		}
	}
}

// ── nocx-7qunp: a file written by a NEWER schema is refused, not rebuilt ──

// rawConn hands back a connection onto the encrypted file, opened the way
// Open opens it and with none of the pragmas Open sets afterwards. It exists
// so migrateSchema can be exercised DIRECTLY: `journal_mode=WAL`
// rewrites the header before Open ever reaches the reset, so a byte-identity
// assertion is only meaningful against the function that makes the decision.
func rawConn(t *testing.T, path string) (*sql.Conn, func()) {
	t.Helper()
	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("raw conn: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		_ = db.Close()
	}
}

// fileFingerprint is "not one byte modified", made checkable: the content
// itself, plus the two pieces of metadata a write moves.
type fileFingerprint struct {
	size int64
	mod  time.Time
	sum  string
}

func fingerprint(t *testing.T, path string) fileFingerprint {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	body, err := os.ReadFile(path) // #nosec G304 — path is the t.TempDir file this test wrote.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return fileFingerprint{size: info.Size(), mod: info.ModTime(), sum: hex.EncodeToString(sum[:])}
}

// aFileFromANewerSchema writes the reproduction: every table name is one this
// build owns, and `entries` carries a column no build of ours has ever heard
// of. That is the whole defect — a newer schema that adds or changes COLUMNS
// inside FAMILIAR tables walks past the unknown-table gate, which only ever
// looks at names.
func aFileFromANewerSchema(t *testing.T, path string) {
	t.Helper()
	rawExec(
		t, path,
		`CREATE TABLE entries (
			id TEXT PRIMARY KEY, ingest_seq INTEGER NOT NULL UNIQUE,
			client TEXT NOT NULL, digest TEXT NOT NULL, environment_id TEXT NOT NULL,
			cwd TEXT NOT NULL, kind TEXT NOT NULL, intent TEXT NOT NULL,
			phase TEXT NOT NULL, status TEXT NOT NULL, submitted_at INTEGER NOT NULL,
			a_column_from_the_future TEXT
		) STRICT`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, intent, phase, status, submitted_at, a_column_from_the_future)
			VALUES ('e1', 1, 'c', 'd', 'env', '/', 'shell', 'echo tomorrow', 'closed', 'success', 0, 'x')`,
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL) STRICT`,
		`INSERT INTO workspaces (id, name) VALUES ('ws-1', 'work')`,
		fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1),
	)
}

// The bug, stated as an assertion: a file stamped ABOVE this build is refused
// and NOT ONE BYTE of it is modified.
//
// Before the direction check, `onDisk == schemaVersion -> return` sent every
// inequality into the rebuild, and the unknown-table gate below it could not
// catch this file because it owns every name in it. The older binary
// therefore DESTROYED rows written by the newer one — tolerable while the
// file belongs to one machine and one build, and not tolerable at all once
// it is shared, which is what the tier-2 remote server makes true.
//
// Asserted against migrateSchema rather than through Open, because Open's
// own prologue (auto_vacuum, journal_mode=WAL) rewrites the header before the
// decision is reached: byte identity is a property of the refusal, not of the
// caller around it.
func TestMigrateRefusesADatabaseWrittenByANewerSchemaWithoutTouchingAByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aFileFromANewerSchema(t, path)

	before := fingerprint(t, path)

	conn, done := rawConn(t, path)
	defer done()
	warned := 0
	err := migrateSchema(
		context.Background(),
		conn,
		schemaLadder,
		&captureLogger{warn: func(string, ...any) { warned++ }},
	)
	if err == nil {
		t.Fatal("migrateSchema accepted a file written by a newer schema — the rows it holds were rebuilt away")
	}
	// The refusal names both versions and what to do about it, because this
	// string is what the person reads: the composition root hands Open's
	// error to history.status as its detail, and the Settings notice prints
	// it after "The history database could not be opened".
	for _, want := range []string{
		fmt.Sprintf("%d", schemaVersion+1),
		fmt.Sprintf("%d", schemaVersion),
		"update",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q — the refusal must say which version is needed", err, want)
		}
	}

	after := fingerprint(t, path)
	if before != after {
		t.Fatalf("the file changed under a refused open:\n before = %+v\n after  = %+v", before, after)
	}
	if warned != 0 {
		t.Fatalf("the refusal warned %d times — nothing was discarded", warned)
	}
	// And the stamp is still the newer one: a refusal that downgraded the
	// stamp would let the very next open rebuild the file.
	if got := rawUserVersion(t, path); got != schemaVersion+1 {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion+1)
	}
}

// The same file through the real Open: the store refuses to hand itself out,
// and the ROWS survive.
//
// Byte identity is deliberately not claimed here and cannot be — Open sets
// auto_vacuum and journal_mode before it reaches the reset, and WAL rewrites
// the header. What the product promises is the thing that matters to a
// person: an older build opening a newer file loses nothing.
func TestOpenRefusesADatabaseWrittenByANewerSchemaAndKeepsItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aFileFromANewerSchema(t, path)

	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open over a file from a newer schema succeeded — the older build rebuilt a database it does not understand")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", schemaVersion+1)) {
		t.Fatalf("error = %v, want it to name the schema the file was written by", err)
	}
	want := []string{"entries", "workspaces"}
	if got := rawTableNames(t, path); !slices.Equal(want, got) {
		t.Fatalf("tables after the refusal = %v, want %v — the newer file was rebuilt", got, want)
	}
	if n := rawRowCount(t, path, "entries"); n != 1 {
		t.Fatalf("entries rows = %d, want 1 — the newer schema's rows were discarded", n)
	}
	if got := rawUserVersion(t, path); got != schemaVersion+1 {
		t.Fatalf("user_version = %d, want %d — the refusal restamped the file", got, schemaVersion+1)
	}
}

// aHotWalFileFromANewerSchema is the SHARED-FILE version of the fixture
// above, and the one that matters (nocx-7qunp, follow-up): a content.db as a
// newer nocx actually leaves it. Our store runs in WAL, so a live database is
// a small main file plus a large `-wal` holding most of the content — copy
// one, sync one, or kill the process, and that is the pair the older binary
// finds. The `-shm` is deliberately NOT copied: rebuilding it from the `-wal`
// is what makes this a genuine hot-WAL recovery rather than a resumed one.
//
// It returns the path and the number of rows written.
func aHotWalFileFromANewerSchema(t *testing.T, rows int) (string, int) {
	t.Helper()
	live := filepath.Join(t.TempDir(), "content.db")
	conn, done := rawConn(t, live)
	ctx := context.Background()
	var jm string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&jm); err != nil {
		t.Fatalf("journal_mode=WAL: %v", err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal — the fixture is not the shared-file case", jm)
	}
	stmts := []string{
		`CREATE TABLE entries (
			id TEXT PRIMARY KEY, ingest_seq INTEGER NOT NULL UNIQUE,
			client TEXT NOT NULL, digest TEXT NOT NULL, environment_id TEXT NOT NULL,
			cwd TEXT NOT NULL, kind TEXT NOT NULL, intent TEXT NOT NULL,
			phase TEXT NOT NULL, status TEXT NOT NULL, submitted_at INTEGER NOT NULL,
			a_column_from_the_future TEXT
		) STRICT`,
	}
	for i := range rows {
		stmts = append(stmts, fmt.Sprintf(
			`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, intent, phase, status, submitted_at, a_column_from_the_future)
				VALUES ('e%d', %d, 'c', 'd', 'env', '/', 'shell', 'echo %d', 'closed', 'success', 0, '%s')`,
			i, i, i, strings.Repeat("x", 512)))
	}
	stmts = append(stmts, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1))
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			t.Fatalf("hot-wal fixture %q: %v", s, err)
		}
	}

	// Copy the pair while the writer still holds it — before any checkpoint.
	shared := filepath.Join(t.TempDir(), "content.db")
	mainSize := copyFile(t, live, shared)
	walSize := copyFile(t, live+"-wal", shared+"-wal")
	done()

	// The fixture is only the case it claims to be if the rows really are in
	// the WAL. Asserted, not assumed: a checkpoint between the writes and the
	// copy would quietly turn this into the ordinary test above.
	if walSize <= mainSize {
		t.Fatalf("wal=%d main=%d — the content is not in the WAL, so this is not the shared-file case", walSize, mainSize)
	}
	t.Logf("shared content.db: main=%d bytes, hot -wal=%d bytes", mainSize, walSize)
	return shared, rows
}

func copyFile(t *testing.T, from, to string) int64 {
	t.Helper()
	body, err := os.ReadFile(from) // #nosec G304 — both paths are t.TempDir files this test made.
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	if err := os.WriteFile(to, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", to, err)
	}
	return int64(len(body))
}

// The shared-database case, end to end: a newer nocx's content.db arrives as
// a main file plus a hot `-wal`, an older binary opens it, and EVERY ROW
// SURVIVES.
//
// This is the case the bead was really about — the one that arrives once a
// content.db is shared between two machines or two versions — and it is not
// the same test as the one above, because WAL mode changes what "untouched"
// can even mean. Measured while writing this: opening such a file and reading
// its stamp modifies the main file not at all, but CLOSING the handle
// checkpoints the WAL into it (4 KB → 272 KB here) and deletes the `-wal`.
// That happens with no journal_mode pragma of ours involved, because WAL is a
// property of the FILE, not of the connection — so no reordering inside Open
// can avoid it, and "not one byte modified" is unreachable through any path
// that opens the database at all.
//
// What IS reachable, and what a person actually needs, is asserted here: the
// checkpoint is schema-blind page copying, so the rows a newer nocx wrote are
// all still there afterwards, still stamped with its schema, and still
// readable by it. The refusal costs a running feature, never a data set.
func TestOpenRefusesANewerDatabaseArrivingInWalWithoutLosingARow(t *testing.T) {
	path, rows := aHotWalFileFromANewerSchema(t, 200)

	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open over a shared newer database succeeded — an older binary rebuilt a file it does not understand")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", schemaVersion+1)) {
		t.Fatalf("error = %v, want it to name the schema the file was written by", err)
	}
	if n := rawRowCount(t, path, "entries"); n != rows {
		t.Fatalf("entries rows = %d, want %d — rows written by the newer nocx were lost", n, rows)
	}
	if got := rawUserVersion(t, path); got != schemaVersion+1 {
		t.Fatalf("user_version = %d, want %d — the older binary restamped a file it refused", got, schemaVersion+1)
	}
	// And the column this build has never heard of is still there: the file
	// is intact as a NEWER database, not merely non-empty.
	if got := rawTableNames(t, path); !slices.Equal([]string{"entries"}, got) {
		t.Fatalf("tables = %v, want [entries]", got)
	}
}
