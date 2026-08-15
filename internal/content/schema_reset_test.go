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
	if _, err := db.CommandHistory().Add(context.Background(), CommandRecord{
		Command: "echo new", Cwd: "/srv", Host: "", Status: StatusSuccess,
	}); err != nil {
		t.Fatalf("Add after rebuild: %v", err)
	}
	page, err := db.CommandHistory().Query(context.Background(), ScopeEverywhere, "", "", 50, nil, "")
	if err != nil {
		t.Fatalf("Query after rebuild: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Command != "echo new" {
		t.Fatalf("entries = %+v, want only the row written after the rebuild", page.Entries)
	}
	// The old row is gone by design: it belongs to a shape this build cannot
	// read, and keeping it would need the migration this project does not
	// carry.
	for _, e := range page.Entries {
		if e.Command == "echo old" {
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
	if _, err := first.CommandHistory().Add(context.Background(), CommandRecord{
		Command: "echo keep", Cwd: "/srv", Host: "", Status: StatusSuccess,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i := range 2 {
		again := openStore(t, path)
		page, err := again.CommandHistory().Query(context.Background(), ScopeEverywhere, "", "", 50, nil, "")
		if err != nil {
			t.Fatalf("Query on reopen %d: %v", i, err)
		}
		if len(page.Entries) != 1 || page.Entries[0].Command != "echo keep" {
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

// The rebuild is one transaction: a DROP that fails midway rolls the file
// back whole. The file below carries a foreign key the drop order does not
// expect — entries references artifacts, so the DROP of artifacts (fourth
// in the order, after grant_scopes, artifact_chunks and authority_grants
// have gone) fails under foreign_keys=ON. The old code left those earlier
// drops committed, the file half-destroyed, and logged the pre-read count
// as if it had been discarded. The assertion is on what the file holds
// afterwards, not on the code.
func TestRebuildFailureMidwayLeavesTheOldFileWhole(t *testing.T) {
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
	warned := 0
	recording := &captureLogger{warn: func(string, ...any) { warned++ }}
	if _, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: recording,
	}); err == nil {
		t.Fatal("Open over the FK-crossed file succeeded — the rebuild did not fail where the review said it would")
	}
	// Wholly old: every table and row the file held is still there, and the
	// version stamp was not moved.
	want := []string{"artifact_chunks", "artifacts", "authority_grants", "command_history", "entries", "grant_scopes"}
	if got := rawTableNames(t, path); !slices.Equal(want, got) {
		t.Fatalf("tables after the failed rebuild = %v, want %v — the file is half-destroyed", got, want)
	}
	if n := rawRowCount(t, path, "command_history"); n != 2 {
		t.Fatalf("command_history rows = %d, want 2", n)
	}
	if n := rawRowCount(t, path, "entries"); n != 1 {
		t.Fatalf("entries rows = %d, want 1", n)
	}
	if got := rawUserVersion(t, path); got != 1 {
		t.Fatalf("user_version = %d, want 1 — the failed rebuild stamped the file", got)
	}
	// Nothing was discarded, so no discard claim was made.
	if warned != 0 {
		t.Fatalf("the failed rebuild logged %d warnings — a count was reported that no commit discarded", warned)
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
