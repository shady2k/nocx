package content

// A database written by an older schema is REBUILT, not half-opened
// (nocx-rtg0.17). Internal rather than external because the reproduction has
// to reach the encrypted file the way Open does — the keyed URI and the
// driver — to put the file into the exact state the owner hit: the previous
// shape of command_history, and a user_version that predates the stamp.

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
