package content

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// persistentEnginePosture is the file-level SQLite state that can outlive the
// connection that created it. Connection-scoped settings such as
// busy_timeout, foreign_keys, and temp_store are deliberately not included.
type persistentEnginePosture struct {
	autoVacuum  int
	pageSize    int
	encoding    string
	journalMode string
}

// TestFreshAndUpgradedDatabasesHaveTheSamePersistentEnginePosture audits the
// four per-file settings most likely to make a fresh database differ from an
// upgraded one:
//
//   - auto_vacuum can differ permanently after the first table. SQLite notices
//     it for page reclamation, but the store does not inspect the value after
//     creation, so a mismatch would be silent and unsafe for the intended
//     posture. The released fixture must set it before its first table.
//   - page_size can differ permanently when selected before the first table.
//     SQLite notices it in page layout and WAL frame sizing; this build leaves
//     the default untouched, so both populations must be 4096.
//   - encoding can differ permanently when selected before the first table.
//     SQLite notices it while decoding text; this build leaves the default
//     UTF-8 encoding untouched, so both populations must be UTF-8.
//   - journal_mode is file state, but Open normalises it to WAL on every open.
//     SQLite notices it for transaction recovery, while the normalisation makes
//     the post-open value safe for both populations.
//
// This is separate from schemaDifference: that comparator intentionally checks
// schema shape, not engine posture, and must not fail because a fixture omitted
// a creation-time pragma.
func TestFreshAndUpgradedDatabasesHaveTheSamePersistentEnginePosture(t *testing.T) {
	releasedPath := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema14Database(t, releasedPath)
	released := readPersistentEnginePosture(t, releasedPath)
	wantReleased := persistentEnginePosture{
		autoVacuum:  2,
		pageSize:    4096,
		encoding:    "UTF-8",
		journalMode: "wal",
	}
	if released != wantReleased {
		t.Fatalf("released schema 14 fixture posture = %+v, want %+v", released, wantReleased)
	}

	fresh := readPersistentEnginePosture(t, aFreshDatabase(t))
	upgraded := readPersistentEnginePosture(t, anUpgradedDatabase(t))

	if fresh != upgraded {
		t.Fatalf("persistent engine posture differs: fresh=%+v upgraded=%+v", fresh, upgraded)
	}
	if fresh.autoVacuum != 2 {
		t.Fatalf("fresh auto_vacuum = %d, want 2 (INCREMENTAL)", fresh.autoVacuum)
	}
	if fresh.pageSize != 4096 {
		t.Fatalf("fresh page_size = %d, want 4096", fresh.pageSize)
	}
	if fresh.encoding != "UTF-8" {
		t.Fatalf("fresh encoding = %q, want UTF-8", fresh.encoding)
	}
	if !strings.EqualFold(fresh.journalMode, "wal") {
		t.Fatalf("fresh journal_mode = %q, want wal", fresh.journalMode)
	}
}

func readPersistentEnginePosture(t *testing.T, path string) persistentEnginePosture {
	t.Helper()
	conn, done := rawConn(t, path)
	defer done()
	ctx := context.Background()

	var posture persistentEnginePosture
	if err := conn.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&posture.autoVacuum); err != nil {
		t.Fatalf("auto_vacuum: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&posture.pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA encoding").Scan(&posture.encoding); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&posture.journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	return posture
}
