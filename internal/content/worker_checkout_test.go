package content

// The worker_checkouts repository and the 18→19 rung (nocx-xn63t.1.4).
//
// The repository is the durable HALF of the checkout list, never a second
// list: everything here is asserted as annotation behaviour (put, touch
// forward, list by key, drop by name) and never as "the checkouts that
// exist", which is git's answer to give. The rung is purely additive, so the
// migration test asserts the ordinary things an add-table edge must do — an
// 18 file opens, the table arrives, the rows the file already had survive —
// and, once, the second-open discipline the 17→18 file's own header records
// as a live defect class before it.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// aWorkerCheckout is the ordinary row: every string distinctive, so a copy
// that transposed columns is visible rather than merely suspected.
func aWorkerCheckout() WorkerCheckout {
	return WorkerCheckout{
		RepoKey:    "nocx-1a2b3c4d",
		Path:       "/data/nocx/worktrees/nocx-1a2b3c4d/feat-one",
		Branch:     "feat/one",
		Base:       "4f2a1c9b",
		Name:       "worker-1",
		Task:       "read AGENTS.md and report",
		CreatedAt:  1700,
		LastUsedAt: 1700,
	}
}

func openWorkerCheckoutStore(t *testing.T) ContentDB {
	t.Helper()
	path := t.TempDir() + "/content.db"
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Criterion: a row that went in comes back whole, and only under its own
// repository's key.
func TestAWorkerCheckoutRowRoundTrips(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	want := aWorkerCheckout()
	if err := repo.Put(context.Background(), want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := repo.List(context.Background(), want.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("list = %+v, want exactly %+v", got, want)
	}
}

// Criterion: Touch moves last_used forward — and never back, because the
// sweep will judge a checkout's age by this stamp and a clock that moved
// backward would age a checkout somebody is using right now.
func TestTouchMovesLastUsedForwardAndNeverBackward(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	co := aWorkerCheckout()
	if err := repo.Put(context.Background(), co); err != nil {
		t.Fatalf("put: %v", err)
	}
	ctx := context.Background()
	if err := repo.Touch(ctx, co.Path, 2000); err != nil {
		t.Fatalf("touch forward: %v", err)
	}
	if err := repo.Touch(ctx, co.Path, 1500); err != nil {
		t.Fatalf("touch backward: %v", err)
	}
	got, err := repo.List(ctx, co.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].LastUsedAt != 2000 {
		t.Fatalf("last_used = %+v, want the forward value 2000", got)
	}
}

// Criterion: a Touch for a path no row carries changes nothing and fails
// nothing — most panes nocx opens are not in recorded checkouts.
func TestTouchForAnUnknownPathChangesNothing(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	co := aWorkerCheckout()
	if err := repo.Put(context.Background(), co); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := repo.Touch(context.Background(), "/home/dev/somewhere-else", 9999); err != nil {
		t.Fatalf("touch an unknown path: %v", err)
	}
	got, err := repo.List(context.Background(), co.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].LastUsedAt != co.LastUsedAt {
		t.Fatalf("rows = %+v, want the row untouched", got)
	}
}

// Criterion: Put again for the same (repo key, path) rewrites the row — a
// spawn that re-created a checkout at the same path after the old one left
// is a new fact, not a conflict.
func TestPutRewritesAnExistingRowForTheSameKeyAndPath(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	ctx := context.Background()
	first := aWorkerCheckout()
	if err := repo.Put(ctx, first); err != nil {
		t.Fatalf("put: %v", err)
	}
	second := first
	second.Branch, second.Name, second.Task, second.LastUsedAt = "feat/two", "worker-2", "another task", 2500
	if err := repo.Put(ctx, second); err != nil {
		t.Fatalf("put again: %v", err)
	}
	got, err := repo.List(ctx, first.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != second {
		t.Fatalf("list = %+v, want the rewritten row %+v", got, second)
	}
}

// Criterion: List answers one repository's rows; All answers every
// repository's. The pane-open note knows a cwd and not a key, which is what
// All is for.
func TestListAnswersOneRepositoryAndAllAnswersEveryRepository(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	ctx := context.Background()
	first := aWorkerCheckout()
	second := aWorkerCheckout()
	second.RepoKey, second.Path, second.Branch = "other-5e6f7a8b", "/data/nocx/worktrees/other-5e6f7a8b/fix-two", "fix/two"
	for _, co := range []WorkerCheckout{first, second} {
		if err := repo.Put(ctx, co); err != nil {
			t.Fatalf("put %s: %v", co.Path, err)
		}
	}
	got, err := repo.List(ctx, first.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Path != first.Path {
		t.Fatalf("list = %+v, want only %s", got, first.Path)
	}
	all, err := repo.All(ctx)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d rows, want 2", len(all))
	}
}

// Criterion: Delete drops the named rows of one repository and is
// idempotent — the reader that noticed a checkout left git's list drops its
// row, and a second notice must not fail.
func TestDeleteDropsTheNamedRowsAndIsIdempotent(t *testing.T) {
	db := openWorkerCheckoutStore(t)
	repo := db.WorkerCheckouts()
	ctx := context.Background()
	first := aWorkerCheckout()
	second := aWorkerCheckout()
	second.Path, second.Branch = first.Path+"-sibling", "feat/one-b"
	for _, co := range []WorkerCheckout{first, second} {
		if err := repo.Put(ctx, co); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := repo.Delete(ctx, first.RepoKey, []string{second.Path, "/already/gone"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.Delete(ctx, first.RepoKey, []string{second.Path}); err != nil {
		t.Fatalf("delete again: %v", err)
	}
	got, err := repo.List(ctx, first.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Path != first.Path {
		t.Fatalf("rows = %+v, want only %s left", got, first.Path)
	}
}

// ── the 18→19 rung ────────────────────────────────────────────────────────

// aReleasedSchema18Database writes the file a shipped schema 18 build would
// have: the frozen released DDL, one row in the table this build's own
// working tree most recently touched (so "the rows the file had survive" is
// about real data), and the stamp. It is built from testdata/schema_v18.sql
// rather than from the current schemaV1 — the discipline
// schema_migrate_released_test.go states and this rung keeps.
func aReleasedSchema18Database(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		releasedSchema(t, 18),
		`INSERT INTO workspaces (id, name, created_at) VALUES ('ws-eighteen', 'the workspace from before the checkout record', 1800)`,
		`INSERT INTO tabs (id, workspace_id, name) VALUES ('tab-eighteen', 'ws-eighteen', 'the tab the user named')`,
		`INSERT INTO panes (id, tab_id, cwd, kind) VALUES ('pane-eighteen', 'tab-eighteen', '/srv/eighteen', 'local')`,
		`INSERT INTO skill_checks (name, provenance, verdict, report, role, endpoint, model, digest, checked_at)
			VALUES ('the-check', 'the-provenance', 'pass', 'the-report', 'the-role', 'the-endpoint', 'the-model', 'the-digest', 1800)`,
		`PRAGMA user_version=18`,
	)
}

// THE HEADLINE: a real schema 18 database opens, lands on 19, the rows it
// already had are untouched, and the new table is there to write.
func TestAReleasedSchema18DatabaseMigratesAndGainsWorkerCheckouts(t *testing.T) {
	path := t.TempDir() + "/content.db"
	aReleasedSchema18Database(t, path)

	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a released schema 18 database: %v — a version behind must migrate, not refuse", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	// The row the file carried before the rung, whole.
	if got := rawRow(t, path, `SELECT provenance || '|' || verdict FROM skill_checks WHERE name = 'the-check'`); got != "the-provenance|pass" {
		t.Fatalf("skill_checks row after the rung = %q, want it intact", got)
	}
	// And the rung's own table takes a row and gives it back — through the
	// repository, which is the only door anything else uses.
	db, reopenErr := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if reopenErr != nil {
		t.Fatalf("reopen the migrated database: %v", reopenErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := db.WorkerCheckouts()
	want := aWorkerCheckout()
	if putErr := repo.Put(context.Background(), want); putErr != nil {
		t.Fatalf("put into the migrated table: %v", putErr)
	}
	got, err := repo.List(context.Background(), want.RepoKey)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("list = %+v, want %+v", got, want)
	}
}

// AND THE UPGRADED FILE OPENS AGAIN — the second-open assertion the 17→18
// file's header records as a defect class, asserted here because an
// add-table edge has nothing to rebuild and the assertion is cheap.
func TestAnUpgradedSchema18DatabaseOpensAgain(t *testing.T) {
	path := t.TempDir() + "/content.db"
	aReleasedSchema18Database(t, path)
	for i := range 2 {
		db, err := Open(context.Background(), Config{
			Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
		})
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %d: %v", i+1, err)
		}
	}
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
}

// A NEWER FILE IS STILL REFUSED, at the new version.
func TestADatabaseStampedAboveSchema19IsStillRefused(t *testing.T) {
	path := t.TempDir() + "/content.db"
	aReleasedSchema18Database(t, path)
	rawExec(t, path, fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion+1))
	_, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open over a database stamped above this build's schema was refused, and it was not")
	}
}

// The stub repository is honest about having no store: reads answer empty,
// Touch changes nothing, and nothing errors the caller into inventing a
// second answer.
func TestTheStubWorkerCheckoutRepositoryAnswersNothingAndFailsNothing(t *testing.T) {
	repo := NewStub(log.NewSlogAdapter(nil)).WorkerCheckouts()
	ctx := context.Background()
	if err := repo.Put(ctx, aWorkerCheckout()); err == nil {
		t.Fatal("stub Put answered nil, want ErrNotImplemented")
	}
	if err := repo.Touch(ctx, aWorkerCheckout().Path, time.Now().UnixMilli()); err != nil {
		t.Fatalf("stub Touch: %v", err)
	}
	if rows, err := repo.List(ctx, "nocx-1a2b3c4d"); err != nil || len(rows) != 0 {
		t.Fatalf("stub List = %v, %v; want empty, nil", rows, err)
	}
	if rows, err := repo.All(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("stub All = %v, %v; want empty, nil", rows, err)
	}
	if err := repo.Delete(ctx, "nocx-1a2b3c4d", []string{aWorkerCheckout().Path}); err != nil {
		t.Fatalf("stub Delete: %v", err)
	}
}
