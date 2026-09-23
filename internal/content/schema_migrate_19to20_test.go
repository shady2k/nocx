package content

// THE 19→20 RUNG: artifacts.media_type learns `application/x-nocx-rows`
// (nocx-2v80t.3.7) — the stored form of a streamed block's output.
//
// The rung is a TABLE REBUILD of artifacts, and artifacts is the one table
// whose rebuild is self-referential (derived_from REFERENCES artifacts(id)):
// a real capture holds a derived text artifact pointing at the vt artifact
// beside it, so the fixture carries exactly that pair. What is asserted is
// not "the migration ran" but "the artifacts a person had are still there,
// whole, with the provenance they were captured under" — every value is
// distinctive rather than plausible, for the reason the 17→18 test states.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func aReleasedSchema19Database(t *testing.T, path string) {
	t.Helper()
	statements := []string{
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		releasedSchema(t, 19),
		`INSERT INTO workspaces (id, name, created_at) VALUES ('ws-nineteen', 'the workspace from before the widening', 1900)`,
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-nineteen', 'ws-nineteen', 'the tab the user named')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-nineteen', 'tab-nineteen', '/srv/nineteen', 'local')`,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env-nineteen', 'local', 1900)`,
		`INSERT INTO environment_observations (id, environment_id, version, observed_at, criticality)
			VALUES (1, 'env-nineteen', 1, 1900, 'routine')`,
		`INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, cwd, kind, source, intent, phase, status, submitted_at)
			VALUES ('entry-nineteen', 1, 'client-nineteen', 'digest-entry', 'env-nineteen', 'pane-nineteen',
			        '/srv/nineteen', 'shell', 'user', 'echo the captures that must survive the widening', 'closed', 'success', 1900)`,
		`INSERT INTO executions (id, entry_id, environment_obs_id, attempt, started_at, ended_at,
			termination_reason, state, payload)
			VALUES (1, 'entry-nineteen', 1, 1, 1900, 1950, 'completed', 'completed', '{"kept":"run"}')`,
		// The vt artifact first, then the derived text that points AT it —
		// the self-reference the rebuild has to carry across a DROP of the
		// very table both rows live in.
		`INSERT INTO artifacts (id, entry_id, execution_id, media_type, state, byte_len, truncated,
			capture_method, capture_version, terminal_cols, terminal_rows, encoding, gaps, payload)
			VALUES ('art-nineteen-vt', 'entry-nineteen', 1, 'application/vt', 'sealed', 11, 'cap',
			        'terminal-cells', 1, 80, 24, 'utf-8', '[]', '{"kept":"vt"}')`,
		`INSERT INTO artifacts (id, entry_id, execution_id, media_type, derived_from, state, byte_len,
			capture_method, capture_version, encoding, gaps, payload)
			VALUES ('art-nineteen-text', 'entry-nineteen', 1, 'text/plain', 'art-nineteen-vt', 'sealed', 5,
			        'terminal-cells', 1, 'utf-8', '[{"start":3,"end":9,"reason":"cap"}]', '{"kept":"text"}')`,
		`INSERT INTO artifact_chunks (artifact_id, seq, body)
			VALUES ('art-nineteen-vt', 1, CAST('vt bytes 01' AS BLOB)),
			       ('art-nineteen-text', 1, CAST('text1' AS BLOB))`,
		`UPDATE ledger_sequence SET next = 1 WHERE id = 1`,
		`PRAGMA user_version=19`,
	}
	rawExec(t, path, statements...)
}

// THE HEADLINE: a real schema 19 database with captures in it comes across
// the rung with every artifact intact — the derived_from that pointed across
// the DROP included — and the widened CHECK then accepts the media type 19
// could not store.
func TestAReleasedSchema19DatabaseMigratesAndKeepsEveryArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema19Database(t, path)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a released schema 19 database: %v — a version behind must migrate, not refuse", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}

	// EVERY ARTIFACT, AS A WHOLE TUPLE. A rebuild that copies the right
	// number of rows with the columns transposed passes every count.
	for _, tc := range []struct{ id, want string }{
		{"art-nineteen-vt", `application/vt||sealed|11|cap|terminal-cells|1|80|24|[]|{"kept":"vt"}`},
		{"art-nineteen-text", `text/plain|art-nineteen-vt|sealed|5||terminal-cells|1|||[{"start":3,"end":9,"reason":"cap"}]|{"kept":"text"}`},
	} {
		got := rawRow(t, path, fmt.Sprintf(
			`SELECT media_type || '|' || coalesce(derived_from, '') || '|' || state || '|' ||
			        byte_len || '|' || coalesce(truncated, '') || '|' || capture_method || '|' ||
			        capture_version || '|' || coalesce(terminal_cols, '') || '|' ||
			        coalesce(terminal_rows, '') || '|' || gaps || '|' || payload
			 FROM artifacts WHERE id = '%s'`, tc.id))
		if got != tc.want {
			t.Fatalf("artifact %s after the rung = %q, want %q — the rebuild did not copy the row it was given", tc.id, got, tc.want)
		}
	}
	if got := rawRow(t, path,
		`SELECT body FROM artifact_chunks WHERE artifact_id = 'art-nineteen-vt' AND seq = 1`); got != "vt bytes 01" {
		t.Fatalf("vt chunk after the rung = %q, want the body it held", got)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM artifact_chunks`); n != 2 {
		t.Fatalf("artifact_chunks holds %d rows after the rung, want 2", n)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name IN ('artifacts_by_entry', 'artifacts_by_execution')`); n != 2 {
		t.Fatalf("sqlite_master holds %d artifact indexes after the rung, want 2 — the indexes the rebuild took must come back", n)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Fatalf("foreign_key_check reports %d violations after the rung — the rebuild committed a schema that does not hold together", n)
	}

	// THE WIDENED CHECK. The point of the rung: the media type a schema 19
	// build refuses is storable now, on the migrated table.
	rawExec(t, path, `INSERT INTO artifacts (id, entry_id, media_type)
		VALUES ('art-nineteen-rows', 'entry-nineteen', 'application/x-nocx-rows')`)
}

// A database this build itself migrated opens again and is genuinely usable —
// the rebuilt table's DDL differs from schemaV1's only in typography, which
// the shape digest normalises before hashing.
func TestAnUpgradedSchema19DatabaseOpensAgainAfterItsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema19Database(t, path)

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
		t.Fatalf("the SECOND open of a database this build itself migrated was refused: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if _, err := second.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo after the restart")); err != nil {
		t.Fatalf("RecordCompleted on the reopened database: %v", err)
	}
}

// A database stamped ABOVE this build is still refused, and a refusal
// destroys nothing — the direction rule has to survive a schema bump rather
// than be assumed past it.
func TestADatabaseStampedAboveSchema20IsStillRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema19Database(t, path)
	rawExec(t, path, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1))

	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatalf("Open accepted a database stamped %d while this build creates %d", schemaVersion+1, schemaVersion)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("schema %d", schemaVersion+1)) {
		t.Fatalf("the refusal reads %q, want it to name the version it refused", err)
	}
	if got := rawUserVersion(t, path); got != schemaVersion+1 {
		t.Fatalf("user_version = %d after the refusal, want %d — a refusal that restamps invites the next open to migrate a shape it never checked",
			got, schemaVersion+1)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM artifacts`); n != 2 {
		t.Fatalf("artifacts holds %d rows after the refusal, want 2 — the refusal destroyed data", n)
	}
}
