package content_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// testBudget is a valid two-number budget for tests: retention below the
// physical ceiling, hysteresis inside (0,1).
var testBudget = content.Budget{
	RetentionBytes:   1 << 30,
	DiskCeilingBytes: 2 << 30,
	CompactionFloor:  0.8,
}

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func newTestStore(t *testing.T) (content.ContentDB, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	}
	ctx := context.Background()
	db, err := content.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

// The file, its WAL and its SHM are 0600 inside a 0700 directory, carry no
// SQLite header and no plaintext of a row we wrote, and reopen with the key.
func TestOpenCreatesEncryptedStoreWithAtRestPosture(t *testing.T) {
	db, dir := newTestStore(t)
	ctx := context.Background()
	if _, addErr := db.Ledger().RecordCompleted(ctx, aCompletedCommand("canary-51e21c88-command")); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	fi, statDirErr := os.Stat(dir)
	if statDirErr != nil {
		t.Fatalf("stat dir: %v", statDirErr)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", fi.Mode().Perm())
	}

	// The WAL and SHM exist only while the store is live — a clean close
	// checkpoints and removes them — so check all three mid-session.
	for _, name := range []string{"content.db", "content.db-wal", "content.db-shm"} {
		path := filepath.Join(dir, name)
		data, readErr := os.ReadFile(path) //nolint:gosec // test reading files the store created
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", name, statErr)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, fi.Mode().Perm())
		}
		if len(data) >= 15 && string(data[:15]) == "SQLite format 3" {
			t.Errorf("%s has a plaintext SQLite header", name)
		}
		if strings.Contains(string(data), "canary-51e21c88-command") {
			t.Errorf("%s contains plaintext of a written row", name)
		}
	}

	// After a clean close, the surviving file is only content.db, still
	// 0600 and still encrypted.
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	mainData, readErr := os.ReadFile(filepath.Join(dir, "content.db")) //nolint:gosec // test reading a store-created file
	if readErr != nil {
		t.Fatalf("read content.db after close: %v", readErr)
	}
	if strings.Contains(string(mainData), "canary-51e21c88-command") {
		t.Error("content.db contains plaintext after close")
	}
	for _, name := range []string{"content.db-wal", "content.db-shm"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s still exists after a clean close (want checkpointed away)", name)
		}
	}

	// Reopen with the key: the record is there.
	db2, reopenErr := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	defer func() { _ = db2.Close() }()
	recs, listErr := db2.Ledger().ListEntries(ctx, 10)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(recs) != 1 || recs[0].Intent != "canary-51e21c88-command" {
		t.Fatalf("reopened store read %+v, want the marker record", recs)
	}
}

// The author a command was submitted under is DURABLE — it is the entry's own
// kind (design §3.1, nocx-iadtt/nocx-e5vsc), so a restart must read back which
// of the two authors ran each line. Written against the reopened store rather
// than the live one: an author kept only in memory would satisfy every
// in-process assertion and lose the fact on the next launch, which is the one
// state this feature exists for.
func TestHistoryRecordAuthorSurvivesRestartInLedger(t *testing.T) {
	db, dir := newTestStore(t)
	ctx := context.Background()

	for _, rec := range []content.CompletedCommand{
		{
			Client: "test-client", Env: content.Environment{ID: "local", Kind: content.EnvLocal},
			Cwd: "/srv/api", Intent: "agent-cmd", Status: content.EntrySuccess,
			Source: content.SourceAssistant,
		},
		{
			Client: "test-client", Env: content.Environment{ID: "local", Kind: content.EnvLocal},
			Cwd: "/srv/api", Intent: "shell-cmd", Status: content.EntrySuccess,
			Source: content.SourceUser,
		},
	} {
		if _, err := db.Ledger().RecordCompleted(ctx, rec); err != nil {
			t.Fatalf("RecordCompleted %q: %v", rec.Intent, err)
		}
	}

	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	db2, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()

	entries, err := db2.Ledger().ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries after reopen: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after reopen = %+v, want 2 durable rows", entries)
	}
	if entries[0].Kind != content.EntryShell || entries[0].Intent != "shell-cmd" || entries[0].Source != content.SourceUser {
		t.Fatalf("newest ledger entry = %+v, want shell-cmd under the user source", entries[0])
	}
	if entries[1].Kind != content.EntryShell || entries[1].Intent != "agent-cmd" || entries[1].Source != content.SourceAssistant {
		t.Fatalf("older ledger entry = %+v, want agent-cmd under the assistant source", entries[1])
	}
}

// A source outside the two command-bearing subjects is refused rather than
// written: `action` and `text` are kinds, never subjects, and an unknown
// source would meet the CHECK constraint halfway through the transaction
// instead of at the seam that can name the vocabulary.
func TestRecordCompleted_RefusesASourceThatIsNotACommandSubject(t *testing.T) {
	db, _ := newTestStore(t)
	ctx := context.Background()
	for _, src := range []content.Source{"", "system", "action", "robot"} {
		rec := aCompletedCommand("x")
		rec.Source = src
		if _, err := db.Ledger().RecordCompleted(ctx, rec); err == nil {
			t.Fatalf("source %q was accepted; want a refusal naming user or assistant", src)
		}
	}
}

// The paired success (criterion 7 — for every refusal there is an ordinary
// row that succeeds): each of the two sources records, and the row carries
// exactly the source the caller named. There is no third value: "nobody
// said who submitted this" is refused above, never quietly written.
func TestRecordCompleted_RecordsTheSourceItWasGiven(t *testing.T) {
	db, _ := newTestStore(t)
	ctx := context.Background()
	for _, src := range []content.Source{content.SourceUser, content.SourceAssistant} {
		rec := aCompletedCommand(string(src))
		rec.Source = src
		if _, err := db.Ledger().RecordCompleted(ctx, rec); err != nil {
			t.Fatalf("RecordCompleted(source=%q): %v", src, err)
		}
	}
	entries, err := db.Ledger().ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two sourced rows", entries)
	}
	// Newest first: the assistant's row, then the person's.
	if entries[0].Source != content.SourceAssistant || entries[1].Source != content.SourceUser {
		t.Fatalf("sources = %q/%q, want the assistant's then the person's", entries[0].Source, entries[1].Source)
	}
}

// A wrong key fails at Open, leaves the file byte-identical, and creates no
// second, unencrypted file.
func TestWrongKeyFailsCleanly(t *testing.T) {
	db, dir := newTestStore(t)
	ctx := context.Background()
	if _, err := db.Ledger().RecordCompleted(ctx, aCompletedCommand("wrongkey-marker")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	path := filepath.Join(dir, "content.db")
	before, err := os.ReadFile(path) //nolint:gosec // test comparing bytes before/after a wrong-key open
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	entriesBefore := dirEntries(t, dir)

	wrong := make([]byte, 32)
	for i := range wrong {
		wrong[i] = 0xff
	}
	_, err = content.Open(ctx, content.Config{
		Path:   path,
		Key:    wrong,
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open with the wrong key succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "not a database") {
		t.Errorf("wrong-key error = %q, want a 'not a database' class error", err)
	}

	after, err := os.ReadFile(path) //nolint:gosec // test comparing bytes before/after a wrong-key open
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Error("wrong-key open modified the database file")
	}
	if strings.Contains(string(after), "wrongkey-marker") {
		t.Error("database file contains plaintext after a wrong-key open")
	}
	// No new files (no second, unencrypted database, no stray journal).
	entriesAfter := dirEntries(t, dir)
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("wrong-key open created new files: before %v, after %v", entriesBefore, entriesAfter)
	}

	// The right key still works.
	db2, err := content.Open(ctx, content.Config{
		Path:   path,
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen with right key after wrong-key attempt: %v", err)
	}
	defer func() { _ = db2.Close() }()
	recs, err := db2.Ledger().ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// ── text filter (nocx-ms7v) and coverage ────────────────────────────────

// ── concurrency: one writer, many readers, no lost rows ──────────────────

func TestConcurrentReadersWithOneWriter(t *testing.T) {
	db, _ := newTestStore(t)
	ctx := context.Background()
	hist := db.Ledger()

	const total = 1000
	var wg sync.WaitGroup
	errCh := make(chan error, 16)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range total {
			if _, err := hist.RecordCompleted(ctx, aCompletedCommand(fmt.Sprintf("cmd-%d", i))); err != nil {
				errCh <- fmt.Errorf("writer: %w", err)
				return
			}
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				page, err := hist.QueryEntries(ctx, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
				if err != nil {
					errCh <- fmt.Errorf("reader: %w", err)
					return
				}
				_, _ = page, page.Exhausted
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	recs, err := hist.ListEntries(ctx, total+1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != total {
		t.Fatalf("got %d rows, want %d (no rows lost)", len(recs), total)
	}
}

// ── error paths the caller must be able to act on ────────────────────────

// Disk full: a per-process file-size cap makes the next write fail with an
// actionable error instead of a panic, and the store stays usable after the
// condition clears.
func TestDiskFullProducesActionableError(t *testing.T) {
	db, _ := newTestStore(t)
	ctx := context.Background()
	hist := db.Ledger()

	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	original := lim
	// Without this, the first oversized write delivers SIGXFSZ, whose default
	// action terminates the process — the very panic we are proving absent.
	signal.Ignore(syscall.SIGXFSZ)
	lim.Cur = 64 * 1024
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatalf("setrlimit: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original) })

	big := strings.Repeat("x", 2<<20) // 2 MiB row, far over the cap
	if _, err := hist.RecordCompleted(ctx, aCompletedCommand(big)); err == nil {
		t.Fatal("oversized write succeeded, want a disk-full-class error")
	}

	// The store is intact: after the limit is lifted, small writes work and
	// the failed write left nothing behind.
	_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
	if _, err := hist.RecordCompleted(ctx, aCompletedCommand("after-full")); err != nil {
		t.Fatalf("Add after the condition cleared: %v", err)
	}
	recs, err := hist.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 || recs[0].Intent != "after-full" {
		t.Fatalf("records after disk-full = %+v, want exactly the one clean row", recs)
	}
}

// A directory that is not writable yields an error, not a panic. The store
// enforces its own 0700 posture on the directory at Open and keeps its WAL
// file descriptors open mid-session, so the observable boundary is Open:
// a path whose parent cannot be a directory must fail cleanly.
func TestUnwritableDirectoryProducesError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(blocker, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if err == nil {
		t.Fatal("Open under a regular file succeeded, want an error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open returned a timeout, want the filesystem error: %v", err)
	}

	// A genuinely read-only location (the store re-asserts 0700 on
	// directories it owns, so an own-parent chmod cannot produce a
	// permission error; an unwritable filesystem can).
	t.Run("read-only filesystem", func(t *testing.T) {
		if _, err := os.Stat("/proc"); err != nil {
			t.Skip("/proc not available")
		}
		_, err := content.Open(context.Background(), content.Config{
			Path:   "/proc/nocx-contentdb-test/content.db",
			Key:    testKey(),
			Budget: testBudget,
			Logger: log.NewSlogAdapter(nil),
		})
		if err == nil {
			t.Fatal("Open on a read-only filesystem succeeded, want an error")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Open returned a timeout, want the filesystem error: %v", err)
		}
	})
}

// ── lifecycle ────────────────────────────────────────────────────────────

func TestAddAfterCloseReturnsErrClosed(t *testing.T) {
	db, _ := newTestStore(t)
	ctx := context.Background()
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	_, err := db.Ledger().RecordCompleted(ctx, aCompletedCommand("late"))
	if !errors.Is(err, content.ErrClosed) {
		t.Fatalf("Add after Close = %v, want ErrClosed", err)
	}
	if secondCloseErr := db.Close(); secondCloseErr != nil {
		t.Fatalf("second Close = %v, want nil (idempotent)", secondCloseErr)
	}
}

// auto_vacuum is decided at creation (nocx-rtg0.11): INCREMENTAL at open and
// still INCREMENTAL after a reopen.
func TestAutoVacuumDecidedAtCreation(t *testing.T) {
	db, dir := newTestStore(t)
	ctx := context.Background()
	if _, err := db.Ledger().RecordCompleted(ctx, aCompletedCommand("av")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	// The test opens its own keyed connection to read the pragma the way a
	// raw caller would (the store does not expose PRAGMA access).
	av := readAutoVacuum(t, filepath.Join(dir, "content.db"))
	if av != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", av)
	}
}

// readAutoVacuum opens a keyed connection the way a raw caller would and
// reads PRAGMA auto_vacuum. The store does not expose PRAGMA access; the
// test needs a raw view to assert the creation-time decision.
func readAutoVacuum(t *testing.T, path string) int {
	t.Helper()
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + keyHex(t) + "'"); err != nil {
			return err
		}
		return c.Exec("PRAGMA busy_timeout=5000")
	})
	if err != nil {
		t.Fatalf("open keyed conn: %v", err)
	}
	defer func() { _ = db.Close() }()
	var av int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&av); err != nil {
		t.Fatalf("auto_vacuum: %v", err)
	}
	return av
}

func keyHex(t *testing.T) string {
	t.Helper()
	k := testKey()
	var b strings.Builder
	for _, c := range k {
		fmt.Fprintf(&b, "%02x", c)
	}
	return b.String()
}

// ── History policy behaviour (settings wired to the store) ───────────────

// Keep-history-off: a command runs and no row appears — through the store,
// not the toggle's own state. The decision applies live: toggling back on
// records again without a restart.
func TestAddHonorsDisabledHistory(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetEnabled(false)

	dir := t.TempDir()
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Policy: policy,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	hist := db.Ledger()

	// A command runs while history is off: no row appears, no error.
	if _, addErr := hist.RecordCompleted(context.Background(), aCompletedCommand("off-1")); addErr != nil {
		t.Fatalf("Add while disabled returned an error: %v", addErr)
	}
	recs, err := hist.ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("history disabled but %d rows appeared", len(recs))
	}

	// Live toggle: enabled again, the next command is recorded.
	policy.SetEnabled(true)
	if _, addErr := hist.RecordCompleted(context.Background(), aCompletedCommand("on-1")); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}
	recs, err = hist.ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 || recs[0].Intent != "on-1" {
		t.Fatalf("after re-enable rows = %+v, want exactly the new command", recs)
	}
}

// ── atomic private-content restore (the export restore operation's seam) ─
