package content

// A database written by an older schema is REBUILT, not half-opened
// (nocx-rtg0.17). Internal rather than external because the reproduction has
// to reach the encrypted file the way Open does — the keyed URI and the
// driver — to put the file into the exact state the owner hit: the previous
// shape of command_history, and a user_version that predates the stamp.

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

// The owner's exact failure: a file whose command_history predates two added
// columns. Before the reset it opened perfectly and then failed every INSERT
// and every SELECT with "no such column", so the store reported itself
// healthy while recording nothing.
func TestOpenRebuildsADatabaseWrittenByAnOlderSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")

	// A file in the shape that shipped before masking: no masked_count, no
	// masked_kinds, and the user_version of a build that never stamped one.
	rawExec(
		t, path,
		`CREATE TABLE command_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL, cwd TEXT NOT NULL, host TEXT NOT NULL,
			status TEXT NOT NULL, exit_code INTEGER,
			started_at INTEGER, ended_at INTEGER,
			trusted INTEGER NOT NULL DEFAULT 0
		) STRICT`,
		`INSERT INTO command_history (command, cwd, host, status) VALUES ('echo old', '/srv', '', 'success')`,
		`PRAGMA user_version=0`,
	)

	db := openStore(t, path)

	// The store WORKS — this is the assertion that used to fail, and it fails
	// on a write as well as on a read, so both are exercised.
	if _, err := db.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo new")); err != nil {
		t.Fatalf("Add after rebuild: %v", err)
	}
	page, err := db.Ledger().QueryEntries(context.Background(), LedgerQuery{Scope: ScopeEverywhere, Limit: 50})
	if err != nil {
		t.Fatalf("Query after rebuild: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Intent != "echo new" {
		t.Fatalf("entries = %+v, want only the row written after the rebuild", page.Entries)
	}
	// The old row is gone by design: it belongs to a shape this build cannot
	// read, and keeping it would need the migration this project does not
	// carry.
	for _, e := range page.Entries {
		if e.Intent == "echo old" {
			t.Fatal("a row from the discarded schema survived the rebuild")
		}
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — an unstamped file rebuilds on every open", got, schemaVersion)
	}
}

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

// ── nocx-rtg0.17: the rebuild is all-or-nothing ───────────────────────────

// rawTableNames lists the user tables on the raw file — the read-back
// assertion for "wholly the old schema" after a failed rebuild.
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
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("raw count %s: %v", table, err)
	}
	return n
}

// A foreign key the drop order does not expect is no longer an outcome at
// all. The file below is the shape that used to prove the rebuild's
// atomicity: entries references artifacts, and artifacts is dropped fourth,
// while entries still holds a row pointing at it — which under
// foreign_keys=ON aborted the DROP and, before nocx-rtg0.17, left the file
// half-destroyed with the pre-read count logged as if it had been discarded.
//
// The rebuild now suspends foreign keys for the demolition, because a
// self-referencing ON DELETE SET NULL made ordering unable to satisfy them at
// all (see TestRebuildDiscardsAFileHoldingANestedEntry). So this file is
// DISCARDED, wholly and in one transaction — the honest answer for a file
// made entirely of tables this build owns. What is asserted is the same
// thing as before: the file afterwards is whole, not half of each schema.
func TestRebuildDiscardsAFileWhoseForeignKeysCrossTheDropOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(
		t, path,
		`CREATE TABLE command_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL, cwd TEXT NOT NULL, host TEXT NOT NULL,
			status TEXT NOT NULL, exit_code INTEGER,
			started_at INTEGER, ended_at INTEGER,
			trusted INTEGER NOT NULL DEFAULT 0
		) STRICT`,
		`INSERT INTO command_history (command, cwd, host, status) VALUES ('echo old-1', '/', '', 'success')`,
		`INSERT INTO command_history (command, cwd, host, status) VALUES ('echo old-2', '/', '', 'success')`,
		`CREATE TABLE authority_grants (
			id INTEGER PRIMARY KEY, execution_id INTEGER NOT NULL UNIQUE,
			version INTEGER NOT NULL, issued_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL, policy TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE grant_scopes (
			grant_id INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
			resource_kind TEXT NOT NULL, resource_id TEXT NOT NULL,
			PRIMARY KEY (grant_id, resource_kind, resource_id)
		) STRICT`,
		`CREATE TABLE artifacts (
			id TEXT PRIMARY KEY, execution_id INTEGER NOT NULL, media_type TEXT NOT NULL
		) STRICT`,
		`INSERT INTO artifacts (id, execution_id, media_type) VALUES ('a1', 1, 'text/plain')`,
		`CREATE TABLE artifact_chunks (
			artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY (artifact_id, seq)
		) STRICT`,
		`CREATE TABLE entries (
			id TEXT PRIMARY KEY, ingest_seq INTEGER NOT NULL UNIQUE,
			client TEXT NOT NULL, digest TEXT NOT NULL, environment_id TEXT NOT NULL,
			cwd TEXT NOT NULL, kind TEXT NOT NULL, intent TEXT NOT NULL,
			phase TEXT NOT NULL, status TEXT NOT NULL, submitted_at INTEGER NOT NULL,
			artifact_id TEXT REFERENCES artifacts(id)
		) STRICT`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, intent, phase, status, submitted_at, artifact_id)
			VALUES ('e1', 1, 'c', 'd', 'env', '/', 'shell', 'x', 'closed', 'success', 0, 'a1')`,
		`PRAGMA user_version=1`,
	)
	discarded := -1
	recording := &captureLogger{warn: func(string, ...any) { discarded++ }}
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: recording,
	})
	if err != nil {
		t.Fatalf("Open over the FK-crossed file: %v — a file made only of tables this build owns must be rebuilt", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Wholly new: not one table of the old shape survives, and the stamp
	// moved. `command_history` is the old file's table and this build creates
	// none, so its absence is the check that the demolition finished.
	if got := rawTableNames(t, path); slices.Contains(got, "command_history") {
		t.Fatalf("tables after the rebuild = %v — command_history survived, so the file is half of each schema", got)
	}
	if n := rawRowCount(t, path, "entries"); n != 0 {
		t.Fatalf("entries rows = %d, want 0 — the old rows were not discarded", n)
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the rebuild did not stamp the file", got, schemaVersion)
	}
	// Three rows went (two command_history, one entries), and the discard was
	// announced: a commit that loses history says so.
	if discarded < 0 {
		t.Fatal("the rebuild discarded the file without warning that history was lost")
	}
}

// A file with a table this build does not know about is refused, not
// dropped: its content is unaccounted for, and dropping it would hand the
// outcome to the foreign-key check — the half-destroyed file the rebuild
// exists to prevent. The file below carries exactly the review's shape: a
// future table carrying a foreign key into one of ours.
func TestRebuildRefusesAFileWithTablesItDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(
		t, path,
		`CREATE TABLE command_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL, cwd TEXT NOT NULL, host TEXT NOT NULL,
			status TEXT NOT NULL, exit_code INTEGER,
			started_at INTEGER, ended_at INTEGER,
			trusted INTEGER NOT NULL DEFAULT 0
		) STRICT`,
		`INSERT INTO command_history (command, cwd, host, status) VALUES ('echo old-1', '/', '', 'success')`,
		`CREATE TABLE future_thing (
			id INTEGER PRIMARY KEY, entry_id TEXT REFERENCES entries(id)
		) STRICT`,
		`PRAGMA user_version=1`,
	)
	warned := 0
	recording := &captureLogger{warn: func(string, ...any) { warned++ }}
	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: recording,
	})
	if err == nil {
		t.Fatal("Open over a file with an unknown table succeeded — the unknown table would have been dropped")
	}
	if !strings.Contains(err.Error(), "future_thing") || !strings.Contains(err.Error(), "newer schema") {
		t.Fatalf("error = %v, want it to name the unknown table and the newer schema", err)
	}
	// Untouched: the refusal happens before any DROP.
	want := []string{"command_history", "future_thing"}
	if got := rawTableNames(t, path); !slices.Equal(want, got) {
		t.Fatalf("tables after the refusal = %v, want %v", got, want)
	}
	if n := rawRowCount(t, path, "command_history"); n != 1 {
		t.Fatalf("command_history rows = %d, want 1", n)
	}
	if got := rawUserVersion(t, path); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
	if warned != 0 {
		t.Fatalf("the refusal logged %d warnings — nothing was discarded", warned)
	}
}

// The rebuild survives the layout chain (nocx-isoph.1), with the shape that
// could plausibly have broken it: tabs.parent_id references tabs, so the
// implicit DELETE FROM behind DROP TABLE meets the table's own rows on the way
// out. A DROP that failed there would leave the file half destroyed — the
// exact state TestRebuildFailureMidwayLeavesTheOldFileWhole exists to
// prevent — so it is exercised rather than reasoned about. (The cascades
// happen to make the order of panes and tabs within rebuildDropOrder
// immaterial; they are listed children-first anyway, because that is the
// property the list claims and the next table added may not cascade.)
//
// The discard itself is deliberate and accepted: nocx is greenfield, no
// migration is written, and the warning stays loud because "your history was
// discarded" is a fact the user is entitled to.
func TestRebuildDropsTheLayoutChainIncludingSelfReferencingTabs(t *testing.T) {
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

	// Stamp the file as an earlier schema: the next Open must rebuild it.
	rawExec(t, path, "PRAGMA user_version=4")

	again := openStore(t, path)
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the rebuild did not complete", got, schemaVersion)
	}
	spaces, err := again.Layout().Workspaces(ctx)
	if err != nil {
		t.Fatalf("Workspaces after the rebuild: %v", err)
	}
	if len(spaces) != 0 {
		t.Fatalf("workspaces after the rebuild = %+v, want none — the file was discarded", spaces)
	}
	// And the tables are back, empty rather than missing: a rebuild that
	// dropped without recreating opens perfectly and fails every statement.
	if _, err := again.Layout().Tabs(ctx, "ws-1"); err != nil {
		t.Fatalf("Tabs after the rebuild: %v", err)
	}
	if _, err := again.Layout().Panes(ctx, "tab-1"); err != nil {
		t.Fatalf("Panes after the rebuild: %v", err)
	}
}

// A file holding a NESTED entry is rebuilt, not refused (nocx-dev stand,
// 2026-08-30). entries.parent_id is a SELF-reference with ON DELETE SET
// NULL, so under foreign_keys=ON the implicit delete inside `DROP TABLE
// entries` nulls every child's parent_id — and the table's own
// `CHECK (parent_id IS NOT NULL OR pos IS NULL)` then refuses the row that
// still holds a seat. Children-first ordering cannot help: a self-referencing
// table is its own child, so there is no order that drops it after its
// children. The rebuild aborts, Open fails, the app falls back to the content
// stub, and layout.read answers ErrNotImplemented — which the renderer shows
// as "Tabs are not being remembered — the layout store is unavailable". Every
// user with one nested block is permanently pinned to the old schema.
func TestRebuildDiscardsAFileHoldingANestedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	rawExec(
		t, path,
		`CREATE TABLE entries (
			id TEXT PRIMARY KEY, ingest_seq INTEGER NOT NULL UNIQUE,
			client TEXT NOT NULL, digest TEXT NOT NULL, environment_id TEXT NOT NULL,
			parent_id TEXT REFERENCES entries(id) ON DELETE SET NULL,
			pos INTEGER,
			cwd TEXT NOT NULL, kind TEXT NOT NULL, intent TEXT NOT NULL,
			phase TEXT NOT NULL, status TEXT NOT NULL, submitted_at INTEGER NOT NULL,
			UNIQUE (parent_id, pos),
			CHECK (parent_id IS NOT NULL OR pos IS NULL)
		) STRICT`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, intent, phase, status, submitted_at)
			VALUES ('root', 1, 'c', 'd', 'env', NULL, NULL, '/', 'shell', 'x', 'closed', 'success', 0)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, intent, phase, status, submitted_at)
			VALUES ('child', 2, 'c', 'd', 'env', 'root', 0, '/', 'text', '', 'closed', 'success', 0)`,
		`PRAGMA user_version=14`,
	)
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a file with a nested entry: %v — the rebuild refused a file it owns, so the app runs on the stub", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d — the file was not rebuilt", got, schemaVersion)
	}
}

// ── nocx-7qunp: a file written by a NEWER schema is refused, not rebuilt ──

// rawConn hands back a connection onto the encrypted file, opened the way
// Open opens it and with none of the pragmas Open sets afterwards. It exists
// so resetIfSchemaChanged can be exercised DIRECTLY: `journal_mode=WAL`
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
// Asserted against resetIfSchemaChanged rather than through Open, because
// Open's own prologue (auto_vacuum, journal_mode=WAL) rewrites the header
// before the decision is reached: byte identity is a property of the refusal,
// not of the caller around it.
func TestResetRefusesADatabaseWrittenByANewerSchemaWithoutTouchingAByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aFileFromANewerSchema(t, path)

	before := fingerprint(t, path)

	conn, done := rawConn(t, path)
	defer done()
	warned := 0
	discards := 0
	err := resetIfSchemaChanged(
		context.Background(),
		conn,
		&captureLogger{warn: func(string, ...any) { warned++ }},
		func(int) { discards++ },
	)
	if err == nil {
		t.Fatal("resetIfSchemaChanged accepted a file written by a newer schema — the rows it holds were rebuilt away")
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
	if warned != 0 || discards != 0 {
		t.Fatalf("the refusal warned %d times and announced %d discards — nothing was discarded", warned, discards)
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
