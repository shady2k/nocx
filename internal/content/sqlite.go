package content

// The SQLite backing for ContentDB (nocx-rtg0.1), built on
// github.com/ncruces/go-sqlite3 v0.35.2 with the adiantum encryption VFS
// (ADR-0018, amended 2026-08-01).
//
// Posture, each from the design or the ADR:
//
//   - One content.db, WAL, foreign_keys=ON, auto_vacuum=INCREMENTAL decided
//     at creation (nocx-rtg0.11: it cannot be changed afterwards without a
//     full vacuum), 0600 file inside a 0700 directory, excluded from any
//     diagnostic bundle.
//   - One writer goroutine with short transactions (§5.3): every mutation
//     goes through a single serialized channel; no handler opens its own
//     write transaction. Concurrent readers are served by the pool directly.
//   - The key is a parameter (ADR-0018 §3, nocx-rtg0.9): the keychain is
//     never called from this package. The key must be 32 bytes.
//   - Every file-creating path goes through the keyed VFS (the canary rule):
//     omitting vfs=adiantum and the key silently defeats encryption — the
//     SQLite backup API, ATTACH and VACUUM INTO write through whatever VFS
//     the destination URI selects.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/shady2k/nocx/internal/log"
)

// Config is the construction parameter set for the SQLite backing.
type Config struct {
	// Path is the content.db file path. Its parent directory is created
	// with 0700 if missing.
	Path string
	// Key is the 32-byte ContentDB key (ADR-0018 §3). A parameter, never
	// fetched here.
	Key []byte
	// Budget is the two-number storage budget (nocx-rtg0.11); a zero budget
	// is refused at Open.
	Budget Budget
	// Policy is the live History policy (keep/enable, retention, output).
	// When nil, the default policy applies (history kept, no age limit).
	Policy *Policy
	// Logger receives operational logging. When nil, the default slog
	// adapter is used.
	Logger log.Logger
}

const (
	// maxOpenConns is ONE, and the reason is the cipher rather than SQLite
	// (ADR-0043, nocx-4p3l2). This store is encrypted through a VFS that
	// enciphers whole 4096-byte blocks. A WAL frame is 24+page_size, so
	// frames never align to those blocks: appending one rewrites the block
	// holding the tail of the frame before it — a frame a reader on another
	// connection is entitled to be reading. SQLite's own locking makes that
	// safe, because it promises only that the reader's FRAMES do not change;
	// it never promised the surrounding BYTES would not. A wide-block cipher
	// makes that difference fatal, because a torn read garbles the whole
	// block rather than the bytes that moved, and the reader is told the
	// database image is malformed.
	//
	// So the pool is the exclusion. One connection means SQLite never has
	// two file handles on this database at once, and the race is unreachable
	// rather than unlikely. TestConcurrentReadersWithOneWriter is red at 16
	// and green at 1.
	//
	// MEASURED, AND THE COST IS REAL. On the profile the test below runs —
	// 5000 RecordCompleted calls against sixteen goroutines running real
	// ledger queries flat out — sixteen connections take ~10s and one takes
	// ~34s. That 3.4x is the honest number; a synthetic probe with light
	// SELECTs said 1.8x and was flattering itself. What makes it acceptable
	// is that no part of the product reads like that profile: its readers
	// are recall search and block restore, which a person asks for.
	//
	// The alternative that keeps the pool is a rollback journal, and it is
	// worse on BOTH axes: ~50-63s on that same profile, and 3.89 ms per
	// commit against WAL's 0.03. That second number is the disqualifying
	// one, because AppendChunk commits once per streamed assistant delta.
	//
	// THE PRICE PAID ELSEWHERE: a read that holds an open cursor while going
	// back to the pool now deadlocks against itself instead of borrowing a
	// spare connection. Every such read drains first — see executionsFor and
	// artifactsFor, and TestEntryDoesNotDeadlockOnASingleConnection, which
	// exists because a deadlock otherwise reports itself as a ten-minute
	// silence.
	maxOpenConns = 1
	// busyTimeoutMs is the lock-wait budget for cross-process writers
	// (multi-process safety, ADR-0018 amendment).
	busyTimeoutMs = 5000
)

// sqliteContent implements ContentDB.
type sqliteContent struct {
	log log.Logger
	cfg Config

	db     *sql.DB
	keyHex string
	path   string
	policy *Policy

	// writeCh serializes every mutation (design §5.3: one writer goroutine,
	// short transactions). It is NEVER closed: Close signals via stop, so a
	// racing Add can select on stop instead of sending into a closed channel.
	writeCh chan writeReq
	stop    chan struct{}
	closed  atomic.Bool
	closeMu sync.Once
	wg      sync.WaitGroup

	// pending is the set of sessions a PREVIOUS incarnation left behind,
	// read once at Open and emptied by verdicts (reconcile.go). It is in
	// memory and not on the rows because it is exact without a stamp: Open
	// is the first thing this incarnation does, so what it finds is what it
	// inherited, and what is written afterwards is its own.
	pendingMu sync.Mutex
	pending   map[string]*pendingSession
}

// writeReq is one mutation on the serialized write path: a function the
// writer goroutine runs, and the channel it answers on.
//
// IT USED TO BE A TAGGED UNION of four kinds — an interim-table insert, that
// table's redaction rewrite, a private-content restore, and the ledger's
// catch-all. Three of them died with command_history (nocx-rtg0.19), and what
// is left is the one shape that was always general: the caller brings the
// mutation, the writer decides only WHEN it runs. There is nothing here for a
// fifth kind to be added to, which is the point.
type writeReq struct {
	ctx  context.Context
	fn   func(ctx context.Context) error
	done chan writeOutcome
}

// writeOutcome is the writer's answer to one writeReq.
//
// It used to carry a row id as well, because one of the four mutations was an
// INSERT whose autoincrement key the caller needed. That mutation went with
// command_history (nocx-rtg0.19); the ledger's ids are minted inside its own
// transaction and come back through the call, so the only thing left to
// answer with is whether it worked.
type writeOutcome struct {
	err error
}

var _ ContentDB = (*sqliteContent)(nil)

// run serializes one ledger mutation through the single writer goroutine
// (design §5.3). fn executes ON the writer goroutine, so its transactions
// serialize with every other mutation; it must use the pool directly and
// never call back into run or any other writer-serialized method — that
// would deadlock the writer against itself.
func (s *sqliteContent) run(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	req := writeReq{ctx: ctx, fn: fn, done: make(chan writeOutcome, 1)}
	// Before the handoff the request is still the caller's, so a cancel or a
	// Close may abandon it: nothing has run and nothing is owed.
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
	// After it, the request is the writer's and the answer is the only way
	// out — which is why this select has no cancellation branches to match
	// the one above. The moment the send lands, fn is running on the writer
	// goroutine; returning here would abandon a mutation still executing, on
	// both axes at once. It would race every caller that assigns into the
	// variable it returns, the write happening on the writer goroutine and
	// the read on this one with nothing ordering them. And it would report a
	// committed transaction as a cancel: fn runs to its Commit regardless,
	// so the ledger keeps the row while the caller is told it does not
	// exist.
	//
	// Waiting is safe on both axes because the writer never drops an
	// accepted request: see process, whose contract this is, and writer's
	// drain loop, which answers everything already queued before it exits on
	// stop. And it costs a cancelled caller nothing extra — fn is handed
	// req.ctx, so cancellation still ends the work promptly rather than
	// making this wait out a full transaction.
	out := <-req.done
	// Matches the other write paths: the at-rest posture (0600 on every
	// database file) is re-asserted after any outcome — a failed transaction
	// is a no-op for chmod, and a committed one must not be skipped because
	// of an error that followed it.
	enforceFileModes(s.path)
	return out.err
}

// Open creates or opens the encrypted ContentDB at cfg.Path. A wrong key
// fails here, cleanly, before the store is handed out: the first real
// statement touches page 1 and SQLite answers "file is not a database".
func Open(ctx context.Context, cfg Config) (ContentDB, error) {
	if err := cfg.Budget.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Key) != 32 {
		return nil, fmt.Errorf("content: key must be 32 bytes, got %d", len(cfg.Key))
	}
	if cfg.Path == "" {
		return nil, errors.New("content: empty path")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.NewSlogAdapter(nil)
	}
	if cfg.Policy == nil {
		cfg.Policy = NewPolicy()
	}

	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("content: create %s: %w", dir, err)
	}
	// 0700 on the directory, always — not just at creation. G302's "0600 or
	// less" is for files; a database directory must be traversable.
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // directory, not file
		return nil, fmt.Errorf("content: chmod %s: %w", dir, err)
	}

	keyHex := hex.EncodeToString(cfg.Key)
	db, err := driver.Open("file:"+cfg.Path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		// Key first: every pragma below and every statement must come after
		// the codec key is installed (canary rule, ADR-0018 amendment).
		if err := c.Exec("PRAGMA hexkey='" + keyHex + "'"); err != nil {
			return fmt.Errorf("content: key: %w", err)
		}
		if err := c.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMs)); err != nil {
			return err
		}
		if err := c.Exec("PRAGMA foreign_keys=ON"); err != nil {
			return err
		}
		if err := c.Exec("PRAGMA temp_store=memory"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("content: open %s: %w", cfg.Path, err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)

	// Creation-time pragmas and schema run on one connection, in order, so
	// auto_vacuum is decided before the first table exists (nocx-rtg0.11: it
	// cannot be changed afterwards without a full vacuum).
	createConn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	var pending map[string]*pendingSession
	var carryErr error
	creationErr := func() error {
		if _, err := createConn.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return fmt.Errorf("content: auto_vacuum: %w", err)
		}
		if _, err := createConn.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return fmt.Errorf("content: journal_mode: %w", err)
		}
		// The wrong-key probe: the first page access refuses a key that does
		// not fit the file. This is what makes Open fail cleanly instead of
		// handing out a store whose every statement errors.
		if _, err := createConn.ExecContext(ctx, "SELECT count(*) FROM sqlite_master"); err != nil {
			return fmt.Errorf("content: open %s: %w (wrong key or corrupt file)", cfg.Path, err)
		}
		// The ladder, and it runs BEFORE schemaV1 (schema_migrate.go): a
		// database behind this build is walked forward one explicit edge
		// at a time, one ahead of it is refused, and one whose version the
		// chain cannot reach is refused too. Nothing here drops a table.
		//
		// The order is load-bearing in both directions. A step edits the
		// shape the PREVIOUS version left, so it has to run before the
		// current shape is asserted over the top of it; and schemaV1 is
		// `IF NOT EXISTS` throughout, so what a step deliberately does not
		// write — a table or an index that is simply new — arrives on the
		// line below at no cost.
		if err := migrateSchema(ctx, createConn, schemaLadder, cfg.Logger); err != nil {
			return err
		}
		if _, err := createConn.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("content: schema: %w", err)
		}
		if err := migrateAPIRuns(ctx, createConn); err != nil {
			return err
		}
		// Startup reconciliation, and its scope is now half of what it was
		// (nocx-k6p18.5; reconcile.go carries the argument). An entry that
		// never reached 'closed' AND NAMES NO SESSION belonged to a pipe
		// whose provenance had already ended, so nothing could ever report
		// it live and closing it here is still right. An entry that names a
		// session is not judged here at all: its session may be running on a
		// host that outlived this coordinator, and only whoever can ask that
		// host may say.
		if err := closeUnanchoredEntries(ctx, createConn, cfg.Logger); err != nil {
			return err
		}
		// The other half is no longer a sweep. `sessions` rows and the
		// recordings beside them SURVIVE Open; they are carried over as the
		// pending set and judged later by whoever could reach the host
		// (reconcile.go). Open cannot ask — asking needs a carrier, the
		// carrier may need the vault, and the vault needs this store — so
		// Open judges nothing.
		pending, carryErr = carryOver(ctx, createConn)
		if carryErr != nil {
			return carryErr
		}
		// The stamp for a file the ladder did not walk: a CREATION. Every
		// other path arrives here already stamped — an unchanged file was
		// current before Open touched it, and a migrated one was stamped
		// by the edge that carried it, inside that edge's own transaction
		// (schema_migrate.go). This line is therefore a no-op on both, and
		// it must stay one: a stamp out here that could move a file the
		// ladder declined to move is exactly the half-migrated state the
		// interval forbids.
		if _, err := createConn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return fmt.Errorf("content: stamp schema version: %w", err)
		}
		return nil
	}()
	_ = createConn.Close()
	if creationErr != nil {
		_ = db.Close()
		return nil, creationErr
	}

	enforceFileModes(cfg.Path)

	s := &sqliteContent{
		log:     cfg.Logger,
		cfg:     cfg,
		db:      db,
		keyHex:  keyHex,
		path:    cfg.Path,
		policy:  cfg.Policy,
		pending: pending,
	}
	s.writeCh = make(chan writeReq)
	s.stop = make(chan struct{})
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

// closeUnanchoredEntries is what is LEFT of the startup sweep of design §5
// (one sweep, both lifecycles) once a session can outlive the coordinator
// (nocx-k6p18.5). It runs once per Open on the creation connection, in ONE
// transaction so the two tables can never disagree:
//
//   - entries: every open entry NOTHING COULD STILL BE RUNNING closes — one
//     that names no session, and every question. An agent-kind entry (a
//     question) whose run was non-terminal says `interrupted` — the block says
//     so and the user asks again (design §4.2) — every other kind closes as
//     `unknown`: the reconstruction UI shows the gap, never a fabricated
//     outcome. Frame entries are already closed at ingest and never reach this
//     update.
//   - executions: every non-terminal agent run becomes `interrupted`, with the
//     termination reason and an end time — an interrupted run has an end.
//     Executions without a state (frame captures, future shell runs) are not
//     runs and are untouched.
//
// WHY THE WHERE CLAUSE GREW TWO TERMS, and they are the whole of what this
// bead changed here. The question each asks is the same one: could anything,
// anywhere, still be running this?
//
//   - `session_id IS NULL` — provenance is "which pipe it ran in, null once
//     that pipe is gone", so an open entry with a NULL one is an entry whose
//     pipe was already gone before this Open. No inventory owns it and nothing
//     can report it live, so leaving it open would show a running command that
//     no process anywhere is running.
//   - `kind = 'ask'` — a question is answered by the ASSISTANT, which lives in
//     THIS process and dies with it (design §4.2: on start every non-terminal
//     run becomes interrupted, the block says so, and the user asks again).
//     Nothing survives a replacement to go on answering it, whatever host its
//     session was on. A shell command is the opposite: its process belongs to
//     the session, and since nocx-k6p18.3 the session belongs to the helper.
//
// What is left open is therefore exactly the shape this bead exists for: a
// SHELL entry that still names a session. Closing one is what would declare a
// running command finished; it waits for its session's verdict (reconcile.go).
//
// The executions half is NOT narrowed, for the same reason `ask` is closed
// here: an execution with a state is an agent run (frame captures and shell
// runs carry none), and an agent run is this process's own work. Delivering
// the real exit status to a shell entry left open is a different problem with
// a different authority, and it is nocx-k6p18.6.
//
// Both updates run in one transaction because the interval they guard has
// one closing event: a restart that interrupted the run also closed the
// entry that asked. A crash between the two leaves the pair split — one
// half of "this ask was interrupted" — and the next start repairs it, but
// a reader between the two would see an inconsistency that never existed
// in any running process.
func closeUnanchoredEntries(ctx context.Context, conn *sql.Conn, logger log.Logger) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE entries SET phase = 'closed', status =
		   CASE WHEN kind = 'ask' THEN 'interrupted' ELSE 'unknown' END
		 WHERE phase != 'closed' AND (session_id IS NULL OR kind = 'ask')`)
	if err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE executions SET state = 'interrupted', termination_reason = 'interrupted', ended_at = ?
		 WHERE state IS NOT NULL AND state NOT IN ('completed','cancelled','failed','interrupted')`,
		time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	if n > 0 && logger != nil {
		logger.Info("content: startup sweep closed open entries with no session", "closed", n)
	}
	return nil
}

// schemaVersion stamps the shape below into the file's user_version. Bump it
// in the same commit as any change to schemaV1 AND add the rung that carries
// an existing database across the edge (schema_migrate.go) — the three move
// together or a build creates one shape and migrates to another.
//
// It exists because `CREATE TABLE IF NOT EXISTS` is a no-op against a table
// that already exists, so an added column once produced a database that
// opened perfectly and then failed every INSERT and every SELECT with "no
// such column". The store went on reporting itself healthy while recording
// nothing, and recall quietly fell back to the session, which is the only
// reason it was noticeable at all.
//
// The answer to that mismatch used to be a rebuild — every table dropped and
// the count of discarded rows announced to the user. It is a MIGRATION now
// (nocx-lmb6v.1): the ladder in schema_migrate.go walks one explicit step per
// edge, and a database it has no step for is refused rather than emptied.
const schemaVersion = 15

// schemaV1 is schema v1 of the one authoritative ledger (nocx-rtg0.2),
// design §5.2 as amended by ADR-0019 and ADR-0020. It used to carry an
// interim `command_history` table beside these, holding a command and no
// output at all; nocx-rtg0.19 deleted it and the ledger is the only place a
// command lives. Nothing writes two tables (ADR-0019 §4) because there is no
// longer a second one. The engine posture is fixed here: STRICT, auto_vacuum,
// WAL.
//
// entries' two clocks, because the column comments have room for the answer
// and not the reason (nocx-rtg0.23). started_at is the RENDERER's wall clock
// at submit — the same Date.now reading history.record already takes and
// floor-checks — because a start that renders as "3 days ago" after a restart
// can only be a wall clock; the monotonic reading design §3.2 first specified
// here is precisely the nocx-rtg0.16 defect, where a presentation clock in a
// field the store judged by deleted every row microseconds after it landed.
// ended_at is the BACKEND's own wall clock at the close, on ADR-0019's rule
// that what the store judges by, the store must own. duration_ms is the
// renderer's measurement and is never the difference of the two: two clocks,
// deliberately, and a duration is asked of the one that measured it.
//
// A TURN is the case where that last sentence names the backend (nocx-hoeq3).
// The renderer cannot measure an assistant turn — it does not open it and it
// does not close it — so the clock that measured it is this process's, at
// both ends: the run's started_at at submit and the terminalizer's own
// time.Now at the close. FinishAgentRun subtracts THOSE, which is asking one
// clock and not differencing two. Nothing else can answer how long the
// assistant took, and a turn nobody times comes off the wire as null — which
// the header draws as no duration chip at all, never as 0ms.
//
// The six open review questions, decided conservatively:
//
//  1. CHECK constraints on the closed enums — YES, on every one. The reason
//     to have a schema is that it says no (STRICT is type, CHECK is value).
//  2. Crash-safe ingest_seq — a one-row ledger_sequence counter, incremented
//     with RETURNING in the SAME transaction as the entry insert. Commit
//     order is the counter's order; a crash rolls both back together, and
//     the UNIQUE constraint is the backstop against any other writer.
//  3. derived_from ON DELETE SET NULL — derived text is durable by default
//     (§3.5 rule 3) and must survive eviction of the raw capture it came
//     from; the link going null is the honest "provenance lost" state.
//     RESTRICT would make raw-VT eviction impossible while any derived text
//     exists, which is always.
//  4. Pinning vs eviction — pinned=1 exempts an artifact from background
//     eviction (a capsule whose content can be evicted underneath it is a
//     broken promise, §3.5); an explicit DeleteEntry still cascades. A pin
//     protects against the background, never against the user. Eviction
//     itself lands with retention, not in this task.
//  5. Edge cascades — ON DELETE CASCADE, the design's own choice. An edge
//     whose endpoint is gone is meaningless; the incident graph is a query
//     over surviving entries, and a separate edge-retention horizon would be
//     a policy nobody asked for.
//  6. byte_len is LOGICAL content bytes (the sum of chunk bodies,
//     maintained by AppendChunk). It deliberately excludes FTS (none yet),
//     B-tree overhead, WAL and free pages: the retention budget is logical
//     retained content (§5.4), and physical disk use is the separate
//     Budget.DiskCeiling number.
const schemaV1 = `
-- The layout chain (nocx-isoph.1, tabs-panes-and-blocks §3): workspace → tab
-- → pane. A workspace is FLAT — it has no column naming another workspace, so
-- nesting is unrepresentable rather than merely unused; depth comes from
-- lineage, which lives on the tab.
--
-- EVERY ID HERE IS MINTED BY THE FRONTEND AND IS UNTRUSTED (§7), so all three
-- tables carry a digest: the store's own hash of what the create asked for,
-- bound to the id (nocx-isoph.2). It is what tells a RETRY of a create — the
-- socket dropped and the answer was lost, which is why AD-9 exists — from an
-- id being reused for something else. The first replays and returns the row
-- that is already there; the second is refused. The client never sends it and
-- never sees it: a value the client could supply would bind nothing.
--
-- The default value is for the rows the BACKEND mints — the fallback
-- workspace the ledger ensures for a session nobody recorded, which no
-- frontend create ever asked for and which therefore matches no digest.
CREATE TABLE IF NOT EXISTS workspaces (
  id           TEXT PRIMARY KEY,           -- client-minted UUIDv7
  name         TEXT NOT NULL,
  colour       TEXT,                       -- NULL: the default workspace, and
                                           -- any row the backend minted
  position     INTEGER NOT NULL DEFAULT 0, -- switcher order
  created_at   INTEGER NOT NULL,           -- backend wall clock, display only
  payload      TEXT NOT NULL DEFAULT '{}', -- sparse extension only
  digest       TEXT NOT NULL DEFAULT ''    -- the create key's content binding
) STRICT;

-- A tab is the strip entry and what the user decorates (§4.5). What is here
-- is what the tab OWNS; the activity indicator, the attention indicator and
-- the label are computed from its panes and are deliberately absent — a
-- column for any of them would give one fact two owners, and they diverge the
-- first time a pane is dragged elsewhere.
--
-- parent_id is the LINEAGE edge and only that (§4.2): who spawned whom,
-- provenance, immutable, never set by hand, admitted by internal/lineage. The
-- display grouping — "A, B and C are shown together" — is the tab's other
-- edge; it is symmetric, has no host and therefore no row (§4.3), and it
-- arrives with drag (nocx-8m2x6). It must never be folded onto this column.
--
-- ON DELETE SET NULL on parent_id, matching artifacts.derived_from and for
-- the same reason: the link going null is the honest "provenance lost" state.
-- CASCADE would delete an independent tab the user still has open; RESTRICT
-- would make a tab that ever spawned another undeletable, and §4.4 removes
-- tabs automatically the moment their last pane leaves.
--
-- closed_at IS THE WINDOW (nocx-l21ib.4). NULL means the tab is in the
-- window; a timestamp means it left. A tab is never deleted, because
-- entries.pane_id is ON DELETE SET NULL and panes.tab_id is ON DELETE
-- CASCADE, so deleting one tab permanently unhooked every block its panes
-- had printed — an ordinary Cmd-W forgot a session's work. Every read that
-- feeds the window filters closed_at IS NULL, and that read IS the window
-- set.
--
-- workspace_id is therefore NULLABLE with ON DELETE SET NULL, which
-- workspaces being the ONE row still deleted forces: under the previous
-- CASCADE, deleting a workspace took its closed tabs and then their panes,
-- which is exactly what the marking exists to prevent. Same shape and same
-- reason as parent_id above — the link going null is the honest "the
-- container this row remembers is gone" state. The invariant that replaces
-- the NOT NULL is the CHECK at the foot of the table: an OPEN tab is always
-- in a workspace; a CLOSED tab may have outlived its own.
CREATE TABLE IF NOT EXISTS tabs (
  id           TEXT PRIMARY KEY,           -- client-minted UUIDv7: UNTRUSTED
  workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
  parent_id    TEXT REFERENCES tabs(id) ON DELETE SET NULL
               CHECK (parent_id IS NULL OR parent_id != id), -- no self-parent
  name         TEXT,                       -- NULL: nobody named it (§4.5)
  colour       TEXT,                       -- NULL: never decorated
  position     INTEGER NOT NULL DEFAULT 0, -- strip order
  pinned       INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0,1)),
  layout       TEXT NOT NULL DEFAULT 'row' CHECK (layout IN ('row','column')),
  seen_at      INTEGER,                    -- the seen-mark; NULL = never seen
  closed_at    INTEGER,                    -- NULL: in the window
  digest       TEXT NOT NULL DEFAULT '',   -- the create key's content binding
  CHECK (closed_at IS NOT NULL OR workspace_id IS NOT NULL)
) STRICT;

-- A pane is the DURABLE IDENTITY (§5): it outlives its shell, its tab and the
-- application, and its blocks are found by its id after a restart.
--
-- PANES DO NOT NEST. tab_id is the pane's only edge, so a pane whose parent is
-- a pane cannot be written down at all. The cost is stated rather than hidden:
-- no asymmetric layouts, ever, until §5's decision is revisited deliberately.
--
-- size_share is the MEMBER's property; the direction is the SET's and lives on
-- the tab. That split is why the tab needed a row and the display group did
-- not.
--
-- closed_at is the tab's column one rung down and means the same thing: NULL
-- is "in the window", and a pane that leaves is marked rather than deleted so
-- the blocks anchored to it (entries.pane_id) keep their anchor. tab_id keeps
-- its CASCADE and it is now unreachable — a tab row is never deleted either —
-- which is why the mark has to be written on BOTH tables in one transaction
-- rather than left to the foreign key.
CREATE TABLE IF NOT EXISTS panes (
  id         TEXT PRIMARY KEY,             -- client-minted UUIDv7: UNTRUSTED
  tab_id     TEXT NOT NULL REFERENCES tabs(id) ON DELETE CASCADE,
  cwd        TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('local','ssh')),
  endpoint   TEXT,                         -- canonical user@host:port; NULL local
  size_share REAL NOT NULL DEFAULT 1.0 CHECK (size_share > 0),
  closed_at  INTEGER,                      -- NULL: in the window
  digest     TEXT NOT NULL DEFAULT ''      -- the create key's content binding
) STRICT;

CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,           -- server-authoritative (AD-7)
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS environments (
  id          TEXT PRIMARY KEY,            -- derived from facets, never from a session
  kind        TEXT NOT NULL CHECK (kind IN ('local','ssh','container','unknown')),
  endpoint    TEXT,                        -- canonical user@host:port; NULL for local
  profile_id  TEXT,
  first_seen  INTEGER NOT NULL,            -- backend wall clock
  payload     TEXT NOT NULL DEFAULT '{}'   -- identity facets (sparse extension)
) STRICT;

CREATE TABLE IF NOT EXISTS environment_observations (
  id             INTEGER PRIMARY KEY,      -- row identity an execution pins
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  version        INTEGER NOT NULL,         -- per-environment ascending
  observed_at    INTEGER NOT NULL,         -- backend wall clock
  confidence     TEXT NOT NULL DEFAULT '{}', -- JSON per-facet: asserted|derived|unknown
  criticality    TEXT NOT NULL CHECK (criticality IN ('routine','sensitive','critical')),
  payload        TEXT NOT NULL DEFAULT '{}', -- facet values: branch, containerId, privilege, …
  UNIQUE (environment_id, version)
) STRICT;

CREATE TABLE IF NOT EXISTS entries (
  id              TEXT PRIMARY KEY,        -- client-minted UUIDv7: UNTRUSTED idempotency key
  ingest_seq      INTEGER NOT NULL UNIQUE, -- backend monotonic; commit order, NOT causality
  client          TEXT NOT NULL,           -- binds the idempotency key to a client
  digest          TEXT NOT NULL,           -- payload digest binding the idempotency key
  environment_id  TEXT NOT NULL REFERENCES environments(id),
  -- THE TWO EDGES (design §6.1), and neither does the other's work.
  --
  -- pane_id is the ANCHOR: durable, frontend-minted, and what makes restore
  -- possible. A user works in a tab, so they expect to see what they did
  -- there — the session is a fact ABOUT a block, not its home. Nullable and
  -- ON DELETE SET NULL for the same reason session_id is: a closed pane is
  -- not restored, its blocks stay in recall (which is scoped by environment
  -- and directory, never by pane), and nothing is left pointing at a row
  -- that is gone. It was ABSENT until nocx-rtg0.28, which is why every
  -- command recorded before that produced a block nothing could re-attach.
  --
  -- session_id is PROVENANCE: which pipe it ran in, null once that pipe is
  -- gone. A session dies with the backend (D5), and Open is what makes that
  -- true of the rows as well — see dropDeadSessions.
  pane_id         TEXT REFERENCES panes(id) ON DELETE SET NULL,
  session_id      TEXT REFERENCES sessions(id) ON DELETE SET NULL,
  -- THE TREE (ADR-0040, amending ADR-0039). Everything drawn in the
  -- scrollback is an entry, and entries form ONE ordered tree: parent_id is
  -- containment and pos orders siblings. NULL parent is a top-level block,
  -- whose order stays ingest_seq — the design's total order (ADR-0019 §2) is
  -- unchanged and the tree does not replace it.
  --
  -- This is a COLUMN and no longer an edge. It was a caused-by row in
  -- edges carrying {pos, at}, and an edge cannot say "one parent" — the
  -- table would take a second one and the reader would have to pick. The
  -- database says it now. edges keeps the relations that genuinely are not
  -- a tree (rerun-of, supersedes, cites, in-span, references).
  --
  -- ON DELETE SET NULL, not CASCADE, for the reason pane_id and session_id
  -- above give in as many words: the container this row remembers is gone,
  -- the block is not, and it must not be left pointing at a row that is not
  -- there. A tool call whose turn was evicted is still a command that ran.
  --
  -- UNIQUE (parent_id, pos) is the seat, and it is the database's job
  -- rather than a writer's: two children at one position is a drawing order
  -- with two answers, and the store refuses it instead of picking. SQLite
  -- counts NULLs as distinct in a unique index, so every top-level block
  -- (NULL, NULL) coexists — the constraint binds siblings only.
  parent_id       TEXT REFERENCES entries(id) ON DELETE SET NULL,
  pos             INTEGER,
  cwd             TEXT NOT NULL,
  -- text is one run of assistant prose (ADR-0040): a thing that was
  -- PRINTED, not attempted. Its shape is declared by the CHECK at the foot
  -- of the table rather than left to convention, because the objection to
  -- prose living in a table built around intent → attempt → outcome is real.
  -- Left implicit it becomes "for text this column is NULL and that one does
  -- not apply", which is how a table rots.
  kind            TEXT NOT NULL CHECK (kind IN ('shell','ask','action','text','frame')),
  -- source is the IMMEDIATE subject that submitted the content or the
  -- intent this entry represents. Initiation is NOT transitive: the
  -- assistant was set going by a person, and the command the assistant ran
  -- was submitted by the assistant — if initiation chained, every row in
  -- the tree would be 'user' and the column would say nothing. Approval
  -- does not change it: a call the assistant proposed stays 'assistant'
  -- after a person allows it, because the person authorised somebody
  -- else's intent, they did not submit it. (No backticks in here: this DDL
  -- is a Go raw string literal, and one would end it.)
  source          TEXT NOT NULL CHECK (source IN ('user','assistant')),
  intent          TEXT NOT NULL,
  phase           TEXT NOT NULL CHECK (phase IN ('open','bound','closed')),
  status          TEXT NOT NULL CHECK (status IN ('pending','running','success','failure','interrupted','unknown')),
  submitted_at    INTEGER NOT NULL,        -- backend wall clock, display only
  -- The terminal facts, written by FinishExecution — see the header note on
  -- the two clocks (nocx-rtg0.23).
  started_at      INTEGER,                 -- renderer wall clock at submit
  ended_at        INTEGER,                 -- backend wall clock at the close
  duration_ms     INTEGER,                 -- measured by whoever ran the clock
  sensitivity     TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal','sensitive')),
  -- capture_key is the renderer's idempotency key for a FRAME capture
  -- (nocx-f4s5): the backend mints the frame entry's id, so the untrusted
  -- key gets its own column, unique where present — a replay of the same
  -- capture returns the original frame id, and two captures can never
  -- share a key. NULL for every non-frame entry.
  capture_key     TEXT,
  payload         TEXT NOT NULL DEFAULT '{}', -- kind payload, sparse extension only
  UNIQUE (parent_id, pos),
  -- The seat is the database's. SQLite counts NULLs as distinct in a unique
  -- index, so UNIQUE(parent_id, pos) constrains SIBLINGS only — a top-level
  -- block (parent_id NULL, pos NULL) never collides with another root. But
  -- it also does NOT constrain a root that holds a seat: SQLite counts
  -- (NULL, n) as distinct from every other row, so a root with a pos slips
  -- past the unique index and claims a seat nothing is ordered by (top-level
  -- order is ingest_seq, and roots hold no seat). That is a drawing order
  -- with a dead seat — the store refuses it.
  CHECK (parent_id IS NOT NULL OR pos IS NULL),
  -- The text shape, stated once and enforced by the engine: a run of prose
  -- sits INSIDE a block (parent_id, pos), says nothing about an intent
  -- (intent = ''), and has no execution to wait for or judge — it was
  -- printed, so it is born closed and successful. Every clause is refused
  -- separately; a row that satisfies four of the five is not a text block.
  CHECK (kind <> 'text' OR (
           parent_id IS NOT NULL AND pos IS NOT NULL AND
           intent = '' AND phase = 'closed' AND status = 'success'))
) STRICT;

-- What is left here is what is genuinely NOT a tree (ADR-0040). caused-by
-- is retired with its {pos, at} payload: containment is entries.parent_id
-- now, and the database guarantees the one parent an edge never could. These
-- five are relations between blocks that each already have a home.
CREATE TABLE IF NOT EXISTS edges (
  from_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  to_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  rel     TEXT NOT NULL CHECK (rel IN ('rerun-of','supersedes','cites','in-span','references')),
  -- payload is the edge's sparse extension: for a references edge it is
  -- the region JSON (design §5 — references carry region coordinates).
  payload TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (from_id, to_id, rel)
) STRICT;

CREATE TABLE IF NOT EXISTS executions (
  id                  INTEGER PRIMARY KEY,
  entry_id            TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  lane                TEXT,                -- agent lane; NULL for a human's shell
  attempt             INTEGER NOT NULL DEFAULT 1,
  environment_obs_id  INTEGER NOT NULL REFERENCES environment_observations(id),
  lease_deadline      INTEGER,             -- wall clock, renewable, bounded ceiling
  inactivity_deadline INTEGER,             -- silence is a different failure from slowness
  interactivity       TEXT NOT NULL DEFAULT 'none'
                      CHECK (interactivity IN ('none','stdin','tty','awaiting-takeover')),
  process_group       TEXT,
  started_at          INTEGER,
  ended_at            INTEGER,
  termination_reason  TEXT CHECK (termination_reason IN
                      ('completed','failed','timeout','transport-gone','user-killed','agent-declined','interrupted','inactivity','output-budget')),
  executor            TEXT,                -- executor identity
  -- state is the ASSISTANT RUN state the renderer draws (design §7):
  -- prepared | streaming | awaiting_approval | completed | cancelled |
  -- failed | interrupted. NULL on executions that are not agent runs (a
  -- frame capture), so the startup sweep — every non-terminal run becomes
  -- interrupted — never touches them.
  state               TEXT CHECK (state IN
                      ('prepared','streaming','awaiting_approval','completed','cancelled','failed','interrupted')),
  payload             TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS authority_grants (
  id           INTEGER PRIMARY KEY,
  execution_id INTEGER NOT NULL UNIQUE REFERENCES executions(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  issued_at    INTEGER NOT NULL,           -- backend wall clock
  expires_at   INTEGER NOT NULL,           -- expiring: a grant is not a toggle
  -- policy is the decision MATRIX as JSON (ADR-0020 §7 as amended
  -- 2026-08-16); the CHECK replaced the old preset enum, and the column
  -- stays SQLite's discipline in a weaker form: a grant whose policy is
  -- not even JSON cannot be recorded.
  policy       TEXT NOT NULL CHECK (json_valid(policy)),
  payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS grant_scopes (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool','content','workspace')),
  resource_id   TEXT NOT NULL,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT;

CREATE TABLE IF NOT EXISTS grant_effects (
  grant_id INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  effect   TEXT NOT NULL CHECK (effect IN
            ('observe','mutate-reversible','mutate-destructive','privilege-change',
             'disclose','cross-boundary','delegate')),
  PRIMARY KEY (grant_id, effect)
) STRICT;

-- AN ARTIFACT BELONGS TO ITS BLOCK (ADR-0040). entry_id is the OWNER: it is
-- what a body is a body OF, and it is NOT NULL because a body with no block
-- is nothing a reader could ever draw. It cascades, so DeleteEntry still
-- takes the bodies with it and eviction still frees what it accounts for.
--
-- execution_id is now PROVENANCE and nullable: WHICH ATTEMPT produced this
-- body, when there was an attempt. A text block has a body and no attempt —
-- it was printed, not run — so the column is honestly empty there rather
-- than pointing at an execution invented to hold it. ON DELETE SET NULL for
-- the reason derived_from below and entries.pane_id above both give: the
-- link going null is the honest "provenance lost" state, and the body's own
-- home is entry_id.
--
-- This does NOT collapse the executions table into entries. An execution is an
-- ATTEMPT and there are several per entry by design (ADR-0020 decision 4:
-- an approved retry is attempt 2 of the same intent, never a new intent) —
-- which attempt printed a body is exactly what this column still answers.
CREATE TABLE IF NOT EXISTS artifacts (
  id              TEXT PRIMARY KEY,        -- client-minted UUIDv7
  entry_id        TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  execution_id    INTEGER REFERENCES executions(id) ON DELETE SET NULL,
  media_type      TEXT NOT NULL CHECK (media_type IN
                  ('application/vt','text/plain','text/markdown','application/json')),
  derived_from    TEXT REFERENCES artifacts(id) ON DELETE SET NULL,
  state           TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','sealed')),
  byte_len        INTEGER NOT NULL DEFAULT 0, -- logical content bytes (question 6)
  pinned          INTEGER NOT NULL DEFAULT 0, -- eviction-exempt (question 4)
  truncated       TEXT CHECK (truncated IN ('cap','gap','suppressed')),
  capture_method  TEXT NOT NULL DEFAULT 'none'
                  CHECK (capture_method IN ('terminal-cells','raw-output','serialized-html','none')),
  capture_version INTEGER NOT NULL DEFAULT 1,
  terminal_cols   INTEGER,
  terminal_rows   INTEGER,
  stream          TEXT CHECK (stream IN ('stdout','stderr','combined')),
  byte_offset     INTEGER,                 -- capture provenance: stream position
  byte_end        INTEGER,
  encoding        TEXT NOT NULL DEFAULT 'utf-8',
  gaps            TEXT NOT NULL DEFAULT '[]', -- JSON [{start,end,reason}]
  payload         TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS artifact_chunks (
  artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  body        BLOB NOT NULL,               -- append-only; never one BLOB
  PRIMARY KEY (artifact_id, seq)
) STRICT;

CREATE TABLE IF NOT EXISTS ledger_sequence (
  id   INTEGER PRIMARY KEY CHECK (id = 1), -- exactly one row
  next INTEGER NOT NULL
) STRICT;
INSERT INTO ledger_sequence (id, next) VALUES (1, 0)
  ON CONFLICT(id) DO NOTHING;  -- schemaV1 re-runs on every open; the seed must be idempotent.
                               -- next=0: the first Submit increments to ingest_seq 1.

-- The retention watermark (nocx-rtg0.12, design §5.4): what eviction removed
-- and how far this store's knowledge is now incomplete. It exists because
-- coverage CANNOT be computed from the rows that remain — once eviction has
-- deleted them there is nothing left to count, and MIN(ended_at) over the
-- survivors reports a partial store as a whole one. Written in the SAME
-- transaction as the deletion it accounts for; see retention.go for why this
-- is one accumulating row rather than a journal of passes.
-- The backend's own recording of what a session printed (nocx-22k1c.1,
-- session_output.go). It is NOT the ledger's artifacts and must not be
-- folded into them: an artifact hangs on an ENTRY — a block a person ran —
-- and these bytes belong to a PIPE, with no entry to hang on precisely
-- because nothing was attached to record one. One table answering two
-- identities acquires a second writer, which is the argument api_runs
-- already makes for its own pair.
--
-- Geometry-free on purpose (AD-6): a stream has no width, so there is no
-- size, no grid and no VT here, and no byte is interpreted. byte_offset is
-- the coordinate the replay ring already keys on, which is what lets a
-- recording be checked against what a client received by offset.
CREATE TABLE IF NOT EXISTS session_output (
  session_id   TEXT PRIMARY KEY,        -- transport's session id (AD-7, server-authoritative)
  first_offset INTEGER NOT NULL,        -- stream offset of the first byte ever recorded
  next_offset  INTEGER NOT NULL,        -- offset one past the last byte accepted; also "produced"
  byte_len     INTEGER NOT NULL DEFAULT 0, -- what is currently KEPT, across every chunk
  head_end     INTEGER NOT NULL,        -- offset one past the reserved head; head chunks end at or before it
  truncated    TEXT CHECK (truncated IN ('cap','gap','suppressed')),
  -- JSON [{start,end,reason}]. The cap's own entries are RECOMPUTED from the
  -- chunks on every write and never accumulated, so they cannot drift from
  -- them; the rest are ranges nobody was there to record (nocx-k6p18.2),
  -- which nothing can recompute -- the chunk layout of "the cap took these"
  -- and "nobody ever offered these" is identical -- so those are read back
  -- and carried forward. See session_output_sqlite.go's header.
  gaps         TEXT NOT NULL DEFAULT '[]',
  started_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS session_output_chunks (
  session_id  TEXT NOT NULL REFERENCES session_output(session_id) ON DELETE CASCADE,
  byte_offset INTEGER NOT NULL,          -- stream offset of body[0]
  body        BLOB NOT NULL,
  -- head = 1 marks a chunk the cap may never take. The head holds the
  -- invocation and its first diagnostics; the tail holds the errors. Same
  -- half-and-half split capture-client.ts applies to a frozen block, so the
  -- two halves of one product cut a body the same way.
  head        INTEGER NOT NULL DEFAULT 0 CHECK (head IN (0, 1)),
  PRIMARY KEY (session_id, byte_offset)
) STRICT;

CREATE TABLE IF NOT EXISTS retention_watermark (
  id              INTEGER PRIMARY KEY CHECK (id = 1), -- exactly one row
  evicted_count   INTEGER NOT NULL DEFAULT 0, -- entries EVER evicted; monotonic
  horizon         INTEGER,                    -- newest instant removed; complete only after it
  last_evicted_at INTEGER                     -- wall clock of the last pass that removed something
) STRICT;
INSERT INTO retention_watermark (id, evicted_count, horizon, last_evicted_at)
  VALUES (1, 0, NULL, NULL)
  ON CONFLICT(id) DO NOTHING;  -- idempotent for the same reason as the sequence seed above.
-- The layout chain is read by parent: a workspace's tabs in strip order, a
-- tab's panes. tabs_by_parent is what keeps ON DELETE SET NULL — and the
-- lineage walk — from scanning the strip.
CREATE INDEX IF NOT EXISTS tabs_by_workspace     ON tabs(workspace_id, position);
CREATE INDEX IF NOT EXISTS tabs_by_parent        ON tabs(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS panes_by_tab          ON panes(tab_id);
CREATE INDEX IF NOT EXISTS entries_by_env        ON entries(environment_id, cwd, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_by_status     ON entries(status, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_open          ON entries(phase) WHERE phase != 'closed';
CREATE INDEX IF NOT EXISTS entries_by_session    ON entries(session_id);
-- Restore reads one pane's blocks, newest first, and that is the whole
-- access pattern the anchor exists for (design §8).
CREATE INDEX IF NOT EXISTS entries_by_pane       ON entries(pane_id, ingest_seq DESC) WHERE pane_id IS NOT NULL;
-- A block's children are read by parent in pos order, and there is
-- deliberately no index here for it: UNIQUE (parent_id, pos) on the table
-- already IS that index. A second one over the same two columns would cost
-- every insert and answer nothing the first cannot.
CREATE INDEX IF NOT EXISTS edges_by_to           ON edges(to_id);
CREATE INDEX IF NOT EXISTS executions_by_entry   ON executions(entry_id, attempt);
-- A block's bodies are read by the block: the owning column is what the
-- restore and the detail read reach for. artifacts_by_execution stays for
-- the provenance question ("what did THIS attempt print") and is partial,
-- because a text block's row has nothing to say to it.
CREATE INDEX IF NOT EXISTS artifacts_by_entry     ON artifacts(entry_id);
CREATE INDEX IF NOT EXISTS artifacts_by_execution ON artifacts(execution_id) WHERE execution_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observations_by_env   ON environment_observations(environment_id, version DESC);
-- The frame idempotency replay check is an index lookup, never a scan: one
-- capture_key per frame (nocx-f4s5).
CREATE UNIQUE INDEX IF NOT EXISTS entries_capture_key ON entries(capture_key) WHERE capture_key IS NOT NULL;
`

// keyedURI is the ONE file-creating path (canary rule): every file this
// package creates must be created through the adiantum VFS with the key in
// the URI. Omitting either silently defeats encryption — the SQLite backup
// API, ATTACH and VACUUM INTO write through whatever VFS the destination URI
// selects, and a destination opened without a key is either refused or
// encrypted with a throwaway random key (verified by the canary test).
func keyedURI(path, keyHex string) string {
	return "file:" + path + "?vfs=adiantum&hexkey=" + keyHex
}

// enforceFileModes keeps the at-rest posture (design §5.5, ADR-0018 §4):
// 0600 on every database file inside the 0700 directory. WAL and SHM files
// are created lazily by SQLite, so this runs after every successful write as
// well as at Open.
func enforceFileModes(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o600)
		}
	}
}

// ── writer goroutine (design §5.3) ───────────────────────────────────────

// process executes one accepted mutation and answers its caller. A request
// that reached process is owed an answer, whatever happens next: a caller
// waiting for its outcome must never hang, and a committed outcome must
// never be replaced by a proxy error.
func (s *sqliteContent) process(req writeReq) {
	req.done <- writeOutcome{err: req.fn(req.ctx)}
}

func (s *sqliteContent) writer() {
	defer s.wg.Done()
	for {
		select {
		case req := <-s.writeCh:
			s.process(req)
		case <-s.stop:
			// Answer everything already queued before exiting (see
			// process): a request accepted before Close must learn its
			// outcome, not hang. The final default leaves only the
			// microscopic race of a send landing after this drain, which
			// no ordering in the app can produce — Close is teardown.
			for {
				select {
				case req := <-s.writeCh:
					s.process(req)
				default:
					// One blocking peek: a sender that won the send race
					// against the drain above still gets its answer.
					select {
					case req := <-s.writeCh:
						s.process(req)
					default:
						return
					}
				}
			}
		}
	}
}

// execer is the ExecContext surface shared by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ── backup (the canary-safe copy path) ───────────────────────────────────

// Backup writes a consistent encrypted snapshot to destPath via the SQLite
// online backup API. The destination URI carries vfs=adiantum and the key:
// the backup API opens the destination itself, so there is no PRAGMA window
// to key it afterwards (verified by the canary test).
func (s *sqliteContent) Backup(ctx context.Context, destPath string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(driverConn any) error {
		dc, ok := driverConn.(driver.Conn)
		if !ok {
			return fmt.Errorf("content: unexpected driver connection %T", driverConn)
		}
		return dc.Raw().Backup("main", keyedURI(destPath, s.keyHex))
	})
	return err
}

// ── ContentDB surface ────────────────────────────────────────────────────

// Conversations stays stubbed until agent mode (design §5.1).
func (s *sqliteContent) Conversations() ConversationRepository {
	return &convStub{log: s.log}
}

// The ledger repository IS the store. It used to be a wrapper type
// (`ledgerRepo`), and the reason was real while it lasted: two repositories
// disagreed about one method, because rewriting a redaction addressed
// command_history by its autoincrement rowid and an entry by its
// client-minted UUIDv7, and one Go type cannot carry both signatures under
// one name. nocx-rtg0.19 deleted the other repository, so the disagreement
// has no second party and the wrapper has nothing left to separate.
//
// It is removed rather than left standing, because a shim whose reason is
// gone is the scaffolding that outlives its purpose — and the next reader
// would have to reconstruct a boundary that no longer exists to explain why
// it is there.
var _ LedgerRepository = (*sqliteContent)(nil)

// Ledger returns the schema-v1 repository (ledger.go). Its wire callers are
// ledger.open / ledger.bind / ledger.close and the agent ask transaction;
// ledger.go's header keeps the by-hand list of what is still test-reachable
// only, because `deadcode` cannot tell the two apart in this package.
func (s *sqliteContent) Ledger() LedgerRepository {
	return s
}

// Layout returns the workspace → tab → pane repository (layout.go). No
// production caller yet — nocx-isoph.2 puts it on the wire — and layout.go's
// header is where that statement is kept current, for the same reason
// ledger.go's is: `deadcode` cannot tell a wired write path from an unwired
// one in this package.
func (s *sqliteContent) Layout() LayoutRepository {
	return s
}

// SessionOutput returns the session-output recorder (session_output.go). Its
// production caller is the transport's per-session recorder, which is the
// replay ring's consumer while no client is attached — so unlike Layout this
// one is wired, and pumpToRing is where.
func (s *sqliteContent) SessionOutput() SessionOutputRepository {
	return s
}

// Close stops the writer goroutine and closes the pool. Idempotent; later
// operations return ErrClosed.
func (s *sqliteContent) Close() error {
	var err error
	s.closeMu.Do(func() {
		s.closed.Store(true)
		close(s.stop)
		s.wg.Wait()
		err = s.db.Close()
		enforceFileModes(s.path)
	})
	return err
}
