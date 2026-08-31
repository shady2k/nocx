package content

// A REAL PROCESS, KILLED BETWEEN THE STAMP AND THE COMMIT, AGAINST A HOT WAL
// (nocx-lmb6v.2).
//
// This is the one end of the interval no in-process test reaches. The ladder's
// own suite ends the span two ways — a step that returns an error, and a
// transaction that is rolled back — and both are the DATABASE/SQL layer
// unwinding an open transaction politely, on a connection that is still there
// to unwind it. Neither is what a crash is. A crash leaves the transaction
// with nobody to roll it back: what ends the span is SQLite's recovery on the
// NEXT open, reading a write-ahead log whose last frames have no commit record,
// and that mechanism is not exercised at all by a `tx.Rollback()`.
//
// So the process here is a genuine second process, stopped with SIGKILL, which
// is the one signal a program cannot handle, defer through, or flush on the way
// out of. Nothing in the child runs after it: no `defer tx.Rollback()`, no
// `Close`, no checkpoint.
//
// AND THE WAL IS NOT INCIDENTAL. A live content.db is a small main file and a
// large hot `-wal` (measured in nocx-7qunp; the fixture here is ~200 KB of
// schema against megabytes of log). That shape is the whole difficulty: at the
// moment of the kill the log holds COMMITTED frames that must survive and
// UNCOMMITTED frames that must not, interleaved in one file, and "the old
// database is intact" is a claim about both halves at once. A test that killed
// a process over a checkpointed database would assert nothing about the file a
// user actually has.
//
// WHY THE KILL LANDS WHERE IT DOES, AND HOW THE TEST KNOWS. applyStep runs four
// things in one transaction: the edge's DDL, the foreign-key check, the stamp,
// and the COMMIT. The forbidden file — the rows of 14 under the number 15 —
// exists only in the window between the third and the fourth. The step this
// test injects therefore runs the REAL edge's DDL and then writes the REAL
// stamp, `PRAGMA user_version=15`, on applyStep's own transaction, which puts
// the transaction in precisely the state applyStep would hand to Commit; then
// it announces itself and blocks. The announcement is the observable the parent
// waits on — a line on the child's stdout, written only after that stamp
// returned — so the kill cannot land early, and the parent additionally
// requires that the child died BY SIGNAL rather than by returning, so it cannot
// land late either. No duration is waited on anywhere (AGENTS.md).
//
// This uses the fault-injection seam exactly as it was left: `apply` is a field
// and the ladder is a parameter. No build tag, no hook, no test-only export in
// production code.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/shady2k/nocx/internal/log"
)

// The child is this same test binary, re-executed. These name the handoff.
const (
	killChildEnv  = "NOCX_TEST_MIGRATION_KILL_CHILD"
	killChildPath = "NOCX_TEST_MIGRATION_KILL_DB"
	// The child prints this, and nothing else, once the stamp is written and
	// the transaction is one COMMIT away from producing schema 15.
	killChildReady = "STAMPED-AND-UNCOMMITTED"
	// Enough log to make the point that the file is a small database plus a
	// large hot WAL, without making the test slow: 1500 rows of 3 KiB.
	killChildBulkRows = 1500
	killChildBulkSize = 3072
)

// TestAMigrationKilledBetweenTheStampAndTheCommitLeavesTheOldDatabase is the
// parent half. It builds a schema 14 database, hands it to a child process that
// will be killed mid-edge, and then asks the file the two questions the
// protocol answers: what version does it claim to be, and are the rows of that
// version all still in it.
func TestAMigrationKilledBetweenTheStampAndTheCommitLeavesTheOldDatabase(t *testing.T) {
	if os.Getenv(killChildEnv) != "" {
		t.Skip("this process is the child; the child's work is in the helper test")
	}
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema14Database(t, path)

	//nolint:gosec // os.Args[0] is this very test binary and both arguments are constants
	child := exec.Command(os.Args[0], "-test.run=^TestKillChildStampsAndBlocks$", "-test.v")
	child.Env = append(os.Environ(),
		killChildEnv+"=1",
		killChildPath+"="+path,
	)
	// The child blocks on a read of this pipe, which the parent never writes
	// to and never closes. Blocking in a syscall rather than on a channel
	// keeps Go's deadlock detector out of it, and it is a wait on an event
	// that will not arrive rather than on a duration.
	hold, err := child.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = hold.Close() }()
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	child.Stderr = os.Stderr
	if startErr := child.Start(); startErr != nil {
		t.Fatalf("start the child: %v", startErr)
	}
	// However this test ends, the child does not outlive it.
	defer func() { _ = child.Process.Kill() }()

	// THE OBSERVABLE. Read until the child says the stamp is written and
	// uncommitted. A scan that ends without that line means the child died on
	// its way there, and the test must fail rather than kill nothing and
	// assert on a database no migration ever touched.
	scanner := bufio.NewScanner(out)
	ready := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), killChildReady) {
			ready = true
			break
		}
	}
	if !ready {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("the child never reached the state between the stamp and the commit (scanner error: %v)", scanner.Err())
	}

	// The WAL is hot RIGHT NOW, with the child still holding its uncommitted
	// transaction open. Measured before the kill, because this is the claim
	// the whole test rests on: what follows happens to a small database with
	// a large log beside it, not to a tidy checkpointed file.
	walBytes := sizeOf(t, path+"-wal")
	mainBytes := sizeOf(t, path)
	t.Logf("at the moment of the kill: %s = %d bytes, %s-wal = %d bytes", filepath.Base(path), mainBytes, filepath.Base(path), walBytes)
	if walBytes <= mainBytes {
		t.Fatalf("the -wal is %d bytes against a %d byte database: the log is not hot, so this test is not exercising the shape a live content.db has", walBytes, mainBytes)
	}
	if walBytes < 1<<20 {
		t.Fatalf("the -wal is only %d bytes; the case being tested is megabytes of hot log", walBytes)
	}

	if killErr := child.Process.Kill(); killErr != nil {
		t.Fatalf("SIGKILL the child: %v", killErr)
	}
	waitErr := child.Wait()
	// THE SECOND OBSERVABLE: it died from the signal, not from returning. A
	// child that had finished the migration and exited cleanly would make
	// every assertion below pass for the wrong reason.
	var exitErr *exec.ExitError
	if !asExitError(waitErr, &exitErr) {
		t.Fatalf("the child ended with %v, want the SIGKILL — nothing was interrupted", waitErr)
	}
	status, ok := exitErr.Sys().(interface{ Signaled() bool })
	if !ok || !status.Signaled() {
		t.Fatalf("the child exited rather than being signalled (%v): the migration was not interrupted mid-edge", exitErr)
	}

	// ── THE OPENING STATE OF THE SPAN, AFTER THE KILL ──────────────────────
	//
	// The span opened at 14 and its COMMIT never returned, so the file still
	// answers 14 and still holds exactly the rows of 14. Reading it is what
	// runs SQLite's recovery over that hot log: the committed frames come
	// back, the uncommitted ones do not, and no test has to arrange either.
	if got := rawUserVersion(t, path); got != 14 {
		t.Fatalf("user_version = %d after the kill, want 14 — a process killed between the stamp and the commit left a file claiming a version its rows are not in", got)
	}
	assertTheRowsOfSchema14(t, path)
	if n := rawCount(t, path, `SELECT count(*) FROM workspaces`); n != killChildBulkRows+1 {
		t.Fatalf("workspaces holds %d rows after the kill, want %d — recovery dropped rows that were COMMITTED before the migration began", n, killChildBulkRows+1)
	}
	if scopeErr := rawTry(t, path, aContentScope); scopeErr == nil {
		t.Fatal("a 'content' scope inserted after the kill — the edge's table rebuild survived a transaction that never committed, which is the half-migrated file the protocol forbids")
	}

	// ── AND THE PAIRED POSITIVE: THE NEXT START COMPLETES IT ───────────────
	//
	// A killed edge is an edge that has not run yet, so the next ordinary
	// start migrates the file the same way it would have the first time. This
	// is the half that makes the test about recovery rather than about
	// refusal: a build that answered the crash by refusing the file forever
	// would satisfy every assertion above.
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open after the kill: %v — the next start must complete the migration or refuse, and a file at 14 is one it can migrate", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d after the restart, want %d", got, schemaVersion)
	}
	assertTheRowsOfSchema14(t, path)
	if n := rawCount(t, path, `SELECT count(*) FROM workspaces`); n != killChildBulkRows+1 {
		t.Fatalf("workspaces holds %d rows after the restart, want %d", n, killChildBulkRows+1)
	}
	if err := rawTry(t, path, aContentScope); err != nil {
		t.Fatalf("a 'content' scope is still refused after the restart: %v — the edge did not complete", err)
	}
	if _, err := db.Ledger().RecordCompleted(context.Background(), aRecordedCommand("echo after the crash")); err != nil {
		t.Fatalf("RecordCompleted after the restart: %v — the recovered database opens but does not work", err)
	}
}

// TestKillChildStampsAndBlocks is the CHILD, and it is not a test: it is the
// other half of the one above, in a process that can be killed. Under an
// ordinary `go test` run it skips, because the environment that names its
// database is absent.
//
// It does three things in order. It makes the log hot, by switching the file
// into WAL with automatic checkpointing off and committing several megabytes —
// rows that belong to schema 14 and must therefore survive everything that
// follows. It starts the real migration through the real `migrateSchema`, with
// a ladder whose single step runs the real edge and then writes the real stamp.
// And then it announces the state and stops, holding the transaction open,
// waiting to be killed.
func TestKillChildStampsAndBlocks(t *testing.T) {
	if os.Getenv(killChildEnv) == "" {
		t.Skip("not the child process")
	}
	path := os.Getenv(killChildPath)
	if path == "" {
		t.Fatalf("%s is not set", killChildPath)
	}
	ctx := context.Background()

	keyHex := hex.EncodeToString(schemaTestKey())
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("child: open: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("child: conn: %v", err)
	}

	// WAL on, and NO automatic checkpoint: what is committed from here stays
	// in the log rather than being folded back into the database, which is
	// what makes the file the small-main-plus-hot-log shape a live one has.
	if _, walErr := conn.ExecContext(ctx, "PRAGMA journal_mode=WAL"); walErr != nil {
		t.Fatalf("child: journal_mode=WAL: %v", walErr)
	}
	if _, ckErr := conn.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); ckErr != nil {
		t.Fatalf("child: wal_autocheckpoint=0: %v", ckErr)
	}
	filler := strings.Repeat("x", killChildBulkSize)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("child: begin the bulk: %v", err)
	}
	for i := range killChildBulkRows {
		if _, bulkErr := tx.ExecContext(ctx,
			`INSERT INTO workspaces (id, name, created_at, payload) VALUES (?, ?, 0, ?)`,
			fmt.Sprintf("bulk-%05d", i), "bulk", filler); bulkErr != nil {
			t.Fatalf("child: bulk insert %d: %v", i, bulkErr)
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatalf("child: commit the bulk: %v", commitErr)
	}

	killer := []migrationStep{{
		from: 14, to: 15,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			if edgeErr := migrateGrantScopeKinds14to15(ctx, tx); edgeErr != nil {
				return edgeErr
			}
			// THE STAMP, exactly as applyStep writes it, on applyStep's own
			// transaction. From here the transaction holds every page the
			// COMMIT would make durable, and nothing has been committed.
			if _, stampErr := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", 15)); stampErr != nil {
				return fmt.Errorf("child: stamp: %w", stampErr)
			}
			_, _ = fmt.Fprintln(os.Stdout, killChildReady)
			_ = os.Stdout.Sync()
			// Park, holding the transaction open, until the parent kills the
			// process. The read never returns: the parent holds the write end
			// of this pipe and never writes to it or closes it.
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			return fmt.Errorf("child: the parent was supposed to kill this process")
		},
	}}
	err = migrateSchema(ctx, conn, killer, log.NewSlogAdapter(nil))
	t.Fatalf("child: migrateSchema returned (%v) — the process should have been killed inside the edge", err)
}

// sizeOf is the size of a file that is expected to exist; a missing -wal is a
// failure of the test's own premise, not a skip.
func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v — the file this test is about is not there", path, err)
	}
	return info.Size()
}

// asExitError is errors.As without importing errors for one call site.
func asExitError(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
