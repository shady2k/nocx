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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// maxOpenConns bounds the pool: one connection serves the writer, the
	// rest serve concurrent readers. WAL lets readers run alongside the
	// single writer without blocking.
	maxOpenConns = 16
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
	// sweep removes rows older than cutoff (age retention). A field so the
	// failure path is testable: the default runs the DELETE; tests inject a
	// failing one to prove Add stays nil (the sweep is best-effort by
	// design — the INSERT is already durable, so a sweep failure must not
	// make Add fail or a retry would duplicate the command).
	sweep func(ctx context.Context, cutoff int64) error

	// writeCh serializes every mutation (design §5.3: one writer goroutine,
	// short transactions). It is NEVER closed: Close signals via stop, so a
	// racing Add can select on stop instead of sending into a closed channel.
	writeCh chan writeReq
	stop    chan struct{}
	closed  atomic.Bool
	closeMu sync.Once
	wg      sync.WaitGroup
}

// writeOp is one kind of mutation on the serialized write path.
type writeOp int

const (
	opAdd writeOp = iota
	opRewrite
	opRestore
	opRun // one ledger mutation, executed as fn on the writer goroutine
)

// writeReq is one mutation on the serialized write path. The writer answers
// on done with the outcome: the assigned row id (opAdd) and any error.
type writeReq struct {
	ctx     context.Context
	op      writeOp
	fn      func(ctx context.Context) error // opRun: the ledger mutation
	record  CommandRecord                   // opAdd
	rew     rewriteRequest                  // opRewrite
	restore restoreRequest                  // opRestore
	done    chan writeOutcome
}

// rewriteRequest is the opRewrite payload: address the row by its stable
// id, replace the redaction segment at span with reference, drop the
// segment from the row's redactions.
type rewriteRequest struct {
	id        int64
	span      Redaction
	reference string
}

// restoreRequest is the opRestore payload: one private-content block to
// apply atomically.
type restoreRequest struct {
	conversations []Conversation
	history       []CommandRecord
}

// writeOutcome is the writer's answer to one writeReq.
type writeOutcome struct {
	id  int64
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
	req := writeReq{ctx: ctx, op: opRun, fn: fn, done: make(chan writeOutcome, 1)}
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
	select {
	case out := <-req.done:
		// Matches the other write paths: the at-rest posture (0600 on every
		// database file) is re-asserted after any outcome — a failed
		// transaction is a no-op for chmod, and a committed one must not be
		// skipped because of an error that followed it.
		enforceFileModes(s.path)
		return out.err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
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
		if err := resetIfSchemaChanged(ctx, createConn, cfg.Logger); err != nil {
			return err
		}
		if _, err := createConn.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("content: schema: %w", err)
		}
		// Startup reconciliation (spec §4.3): every entry that never reached
		// 'closed' — a crash, a force-quit, a session that died — is closed
		// as status='unknown' through the entries_open partial index. Must
		// run before the store is handed out: after this, no open row
		// survives a restart.
		if err := closeOpenEntries(ctx, createConn, cfg.Logger); err != nil {
			return err
		}
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
		log:    cfg.Logger,
		cfg:    cfg,
		db:     db,
		keyHex: keyHex,
		path:   cfg.Path,
		policy: cfg.Policy,
	}
	s.sweep = func(ctx context.Context, cutoff int64) error {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM command_history WHERE ended_at IS NOT NULL AND ended_at < ?`, cutoff)
		return err
	}
	s.writeCh = make(chan writeReq)
	s.stop = make(chan struct{})
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

// closeOpenEntries is the startup sweep of design §5 (one sweep, both
// lifecycles), run once per Open on the creation connection, ONE
// transaction so the two tables can never disagree:
//
//   - entries: every open entry closes. An agent-kind entry (a question)
//     whose run was non-terminal says `interrupted` — the block says so and
//     the user asks again (design §4.2) — every other kind closes as
//     `unknown`: the reconstruction UI shows the gap, never a fabricated
//     outcome. Frame entries are already closed at ingest and never reach
//     this update.
//   - executions: every non-terminal agent run (state IS NOT NULL and not
//     in the terminal set) becomes `interrupted`, with the termination
//     reason and an end time — an interrupted run has an end. Executions
//     without a state (frame captures, future shell runs) are not runs and
//     are untouched.
//
// Both updates run in one transaction because the interval they guard has
// one closing event: a restart that interrupted the run also closed the
// entry that asked. A crash between the two leaves the pair split — one
// half of "this ask was interrupted" — and the next start repairs it, but
// a reader between the two would see an inconsistency that never existed
// in any running process.
func closeOpenEntries(ctx context.Context, conn *sql.Conn, logger log.Logger) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: startup sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE entries SET phase = 'closed', status =
		   CASE WHEN kind = 'agent' THEN 'interrupted' ELSE 'unknown' END
		 WHERE phase != 'closed'`)
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
		logger.Info("content: startup sweep closed open entries", "closed", n)
	}
	return nil
}

// schemaVersion stamps the shape below into the file's user_version. Bump it
// in the same commit as any change to schemaV1 — that is the whole protocol.
//
// We write no migrations (greenfield), and `CREATE TABLE IF NOT EXISTS` is a
// no-op against a table that already exists, so before this check an added
// column produced a database that opened perfectly and then failed every
// INSERT and every SELECT with "no such column". The store went on reporting
// itself healthy while recording nothing; recall quietly fell back to the
// session, which is the only reason it was noticeable at all. A silent
// half-broken store is worse than no store, so the file is rebuilt instead —
// and it says so, because "your history was discarded" is a fact the user is
// entitled to rather than something to infer from an empty panel.
const schemaVersion = 4

// rebuildDropOrder is the complete set of user tables this build owns,
// children first so a parent DROP never meets a surviving child under
// foreign_keys=ON. It is also the membership gate in resetIfSchemaChanged: a
// file whose user tables are all in this set was written by an earlier
// schema of THIS store and is discarded deliberately; one containing any
// other table is refused.
var rebuildDropOrder = []string{
	"grant_scopes", "artifact_chunks", "authority_grants", "artifacts",
	"edges", "executions", "environment_observations", "entries",
	"sessions", "environments", "workspaces", "ledger_sequence",
	"command_history",
}

// resetIfSchemaChanged rebuilds the file when it was written by a different
// schema. Rows are lost by design: they belong to a shape this build cannot
// read, and inventing a migration to keep them is the backwards compatibility
// this project deliberately does not carry.
//
// The rebuild is all-or-nothing (nocx-rtg0.17): every DROP and the
// discarded-row count share ONE transaction, so a crash, a cancellation or a
// failed DROP midway leaves the file wholly old or wholly new, and the
// warning is logged only after the commit, with the count that commit
// actually discarded. SQLite DDL is transactional, so this costs nothing.
func resetIfSchemaChanged(ctx context.Context, conn *sql.Conn, logger log.Logger) error {
	var onDisk int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&onDisk); err != nil {
		return fmt.Errorf("content: read schema version: %w", err)
	}
	if onDisk == schemaVersion {
		return nil
	}
	// A fresh file is version 0 with no tables — that is a creation, not a
	// reset, and must not be announced as data loss. Any user table (the
	// interim command_history on a pre-v1 file, a v1 table on a future one)
	// means the file belongs to a different schema and is rebuilt.
	names, err := conn.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("content: probe schema: %w", err)
	}
	var tables []string
	for names.Next() {
		var name string
		if scanErr := names.Scan(&name); scanErr != nil {
			_ = names.Close()
			return fmt.Errorf("content: probe schema: %w", scanErr)
		}
		tables = append(tables, name)
	}
	if iterErr := names.Err(); iterErr != nil {
		return fmt.Errorf("content: probe schema: %w", iterErr)
	}
	if closeErr := names.Close(); closeErr != nil {
		return fmt.Errorf("content: probe schema: %w", closeErr)
	}
	if len(tables) == 0 {
		return nil
	}
	// A table this build does not know about is refused, deliberately: its
	// content is unaccounted for, so discarding it is not the "history
	// discarded" the rebuild promises — it is data this build cannot name.
	// Dropping it would also hand the outcome to the foreign-key check,
	// which is exactly the half-destroyed file this function exists to
	// prevent. A file that reaches here with an unknown table was written
	// by a newer schema (or is not a ContentDB file at all).
	for _, name := range tables {
		known := false
		for _, t := range rebuildDropOrder {
			if t == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("content: rebuild refused: table %q is not part of schema %d — the file was written by a newer schema (or is not a ContentDB file); update nocx rather than discard it",
				name, schemaVersion)
		}
	}
	// Count inside the transaction, before any DROP: the number is the only
	// measure of what the user lost, and the count that is logged is the
	// one this commit discards. A count that fails (the interim table is
	// absent on a v1 file) is not a reason to abandon the rebuild — report
	// it as unknown and carry on.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rowsDiscarded := -1
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM command_history").Scan(&rowsDiscarded); err != nil {
		rowsDiscarded = -1
	}
	for _, t := range rebuildDropOrder {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
			return fmt.Errorf("content: rebuild for schema %d: %w", schemaVersion, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: commit rebuild: %w", err)
	}
	if logger != nil {
		logger.Warn("content: history discarded — the database was written by an older schema",
			"was", onDisk, "now", schemaVersion, "rowsDiscarded", rowsDiscarded)
	}
	return nil
}

// schemaV1 is schema v1 of the one authoritative ledger (nocx-rtg0.2),
// design §5.2 as amended by ADR-0019 and ADR-0020: the interim
// command_history table plus the v1 tables. command_history stays the live
// path until nocx-rtg0.3 cuts the wire over to ledger.*; nothing writes both
// (ADR-0019 §4). The engine posture is fixed here: STRICT, auto_vacuum, WAL.
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
CREATE TABLE IF NOT EXISTS command_history (
  id           INTEGER PRIMARY KEY AUTOINCREMENT, -- backend seq; the only total order
  command      TEXT    NOT NULL,
  cwd          TEXT    NOT NULL,
  host         TEXT    NOT NULL,
  status       TEXT    NOT NULL,
  exit_code    INTEGER,
  started_at   INTEGER,
  ended_at     INTEGER,
  trusted      INTEGER NOT NULL DEFAULT 0,
  masked_count INTEGER NOT NULL DEFAULT 0,
  masked_kinds TEXT    NOT NULL DEFAULT '[]',
  redactions   TEXT    NOT NULL DEFAULT '[]'
) STRICT;
CREATE INDEX IF NOT EXISTS command_history_by_scope ON command_history (cwd, host, id DESC);
CREATE INDEX IF NOT EXISTS command_history_by_host  ON command_history (host, id DESC);
CREATE INDEX IF NOT EXISTS command_history_by_ended ON command_history (ended_at);

CREATE TABLE IF NOT EXISTS workspaces (
  id           TEXT PRIMARY KEY,           -- client-minted UUIDv7
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,           -- backend wall clock, display only
  payload      TEXT NOT NULL DEFAULT '{}'  -- sparse extension only
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
  session_id      TEXT REFERENCES sessions(id) ON DELETE SET NULL,
  cwd             TEXT NOT NULL,
  kind            TEXT NOT NULL CHECK (kind IN ('shell','agent','action')),
  intent          TEXT NOT NULL,
  phase           TEXT NOT NULL CHECK (phase IN ('open','bound','closed')),
  status          TEXT NOT NULL CHECK (status IN ('pending','running','success','failure','interrupted','unknown')),
  conversation_id TEXT,
  submitted_at    INTEGER NOT NULL,        -- backend wall clock, display only
  started_at      INTEGER,                 -- frontend monotonic clock — durations only
  ended_at        INTEGER,
  duration_ms     INTEGER,
  sensitivity     TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal','sensitive')),
  reviewed_at     INTEGER,
  -- capture_key is the renderer's idempotency key for a FRAME capture
  -- (nocx-f4s5): the backend mints the frame entry's id, so the untrusted
  -- key gets its own column, unique where present — a replay of the same
  -- capture returns the original frame id, and two captures can never
  -- share a key. NULL for every non-frame entry.
  capture_key     TEXT,
  payload         TEXT NOT NULL DEFAULT '{}' -- kind payload, sparse extension only
) STRICT;

CREATE TABLE IF NOT EXISTS edges (
  from_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  to_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  rel     TEXT NOT NULL CHECK (rel IN ('rerun-of','supersedes','caused-by','cites','in-span','references')),
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
                      ('completed','failed','timeout','transport-gone','user-killed','agent-declined','interrupted')),
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
  policy       TEXT NOT NULL CHECK (policy IN ('ask-every-time','ask-on-mutate','autonomous')),
  payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS grant_scopes (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool')),
  resource_id   TEXT NOT NULL,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT;

CREATE TABLE IF NOT EXISTS artifacts (
  id              TEXT PRIMARY KEY,        -- client-minted UUIDv7
  execution_id    INTEGER NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
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
CREATE INDEX IF NOT EXISTS entries_by_env        ON entries(environment_id, cwd, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_by_status     ON entries(status, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_open          ON entries(phase) WHERE phase != 'closed';
CREATE INDEX IF NOT EXISTS entries_by_session    ON entries(session_id);
CREATE INDEX IF NOT EXISTS edges_by_to           ON edges(to_id);
CREATE INDEX IF NOT EXISTS executions_by_entry   ON executions(entry_id, attempt);
CREATE INDEX IF NOT EXISTS artifacts_by_execution ON artifacts(execution_id);
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
	switch req.op {
	case opAdd:
		id, err := s.doAdd(req.ctx, req.record)
		req.done <- writeOutcome{id: id, err: err}
	case opRewrite:
		req.done <- writeOutcome{err: s.doRewrite(req.ctx, req.rew)}
	case opRestore:
		req.done <- writeOutcome{err: s.doRestore(req.ctx, req.restore)}
	case opRun:
		req.done <- writeOutcome{err: req.fn(req.ctx)}
	}
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

func (s *sqliteContent) doAdd(ctx context.Context, r CommandRecord) (int64, error) {
	id, err := insertRecord(ctx, s.db, r)
	if err != nil {
		return 0, err
	}
	enforceFileModes(s.path)

	// Age-based retention, run in the same writer turn: completed commands
	// older than the limit are removed from nocx. Deletion is a short
	// autocommit transaction and uses the ended_at index; a crash between
	// the insert and the sweep only delays the sweep.
	if days := s.policy.RetentionDays(); days > 0 {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		if sweepErr := s.sweep(ctx, cutoff); sweepErr != nil {
			s.log.Warn("retention sweep failed", "error", sweepErr)
		}
	}
	return id, nil
}

// insertRecord executes one command-history INSERT through the given
// executor — the pool (single-row path) or a restore transaction. Shared so
// the two write paths cannot drift on the row shape.
func insertRecord(ctx context.Context, ex execer, r CommandRecord) (int64, error) {
	kinds := r.MaskedKinds
	if kinds == nil {
		kinds = []string{}
	}
	kindsJSON, err := json.Marshal(kinds)
	if err != nil {
		return 0, err
	}
	redactions := r.Redactions
	if redactions == nil {
		redactions = []Redaction{}
	}
	redactionsJSON, err := json.Marshal(redactions)
	if err != nil {
		return 0, err
	}
	// One INSERT, one autocommit transaction: short, atomic, replay-safe.
	res, err := ex.ExecContext(ctx, `INSERT INTO command_history
		(command, cwd, host, status, exit_code, started_at, ended_at, trusted, masked_count, masked_kinds, redactions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Command, r.Cwd, r.Host, string(r.Status), r.ExitCode, r.StartedAt, r.EndedAt, r.Trusted, r.MaskedCount, string(kindsJSON), string(redactionsJSON))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// doRestore applies one private-content block in a single transaction:
// either every history row is durable or none is. A caller that restored
// rows one by one could be interrupted between rows, and a partial restore
// cannot be unwound through the repository surface — the store owns the
// atomicity (the export restore operation relies on it).
//
// Conversations are stubbed until agent mode (design §5.1): a block that
// carries them is refused, exactly as ConversationRepository.Save refuses.
func (s *sqliteContent) doRestore(ctx context.Context, r restoreRequest) error {
	if len(r.conversations) > 0 {
		return ErrNotImplemented
	}
	if len(r.history) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("restore private content: %w", err)
	}
	for _, rec := range r.history {
		if _, err := insertRecord(ctx, tx, rec); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("restore private content: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("restore private content: %w", err)
	}
	enforceFileModes(s.path)

	// Retention at the batch level: restored rows older than the limit are
	// removed, matching the per-write path. Best-effort, as in doAdd.
	if days := s.policy.RetentionDays(); days > 0 {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		if sweepErr := s.sweep(ctx, cutoff); sweepErr != nil {
			s.log.Warn("retention sweep failed", "error", sweepErr)
		}
	}
	return nil
}

// execer is the ExecContext surface shared by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Add serializes the insert through the single writer goroutine and returns
// the backend-assigned row id — the row's stable identity, which a later
// RewriteRedaction addresses. The record's ID field is informational.
func (s *sqliteContent) Add(ctx context.Context, record CommandRecord) (int64, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	// Keep-history-off: a command runs and no row appears. Decided before the
	// writer is invoked, so nothing is serialized for a record nobody wants.
	if !s.policy.Enabled() {
		return 0, nil
	}
	req := writeReq{ctx: ctx, op: opAdd, record: record, done: make(chan writeOutcome, 1)}
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.stop:
		return 0, ErrClosed
	}
	select {
	case out := <-req.done:
		return out.id, out.err
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.stop:
		return 0, ErrClosed
	}
}

// RestorePrivate applies one private-content block atomically through the
// single writer. The caller's context governs the transaction: a
// cancellation before the writer accepts the request does nothing, and a
// cancellation INSIDE the transaction aborts it (the insert path observes
// ctx). A cancellation AFTER the transaction committed must not surface as
// an error — the restore is committed, and reporting failure would send the
// export restore operation into a rollback that splits the stores — so once
// the writer accepts the request the caller waits for its outcome, which is
// authoritative. The writer drains its queue on Close, so an accepted
// request is always answered.
func (s *sqliteContent) RestorePrivate(ctx context.Context, conversations []Conversation, history []CommandRecord) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if len(conversations) > 0 {
		// The SQLite backing has no conversation table yet; the stub is
		// the honest surface (agent mode, design §5.1). Refuse rather
		// than drop.
		return ErrNotImplemented
	}
	if !s.policy.Enabled() {
		// History off: the single-row path's Add stores nothing and
		// succeeds, and the restore matches it exactly.
		return nil
	}
	req := writeReq{
		ctx:     ctx,
		op:      opRestore,
		restore: restoreRequest{history: history},
		done:    make(chan writeOutcome, 1),
	}
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
	// Accepted: the writer owns the outcome. Deliberately no ctx.Done or
	// stop case here — either could win after the transaction committed and
	// report failure for a committed restore (see the doc comment).
	out := <-req.done
	return out.err
}

// RewriteRedaction replaces the redaction segment at span in the row's
// stored command with reference, dropping the segment from the row's
// redactions. Read-modify-write happens inside one writer turn, so no
// concurrent mutation can interleave. Idempotent for a span already holding
// the same reference.
func (s *sqliteContent) RewriteRedaction(ctx context.Context, id int64, span Redaction, reference string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	req := writeReq{
		ctx:  ctx,
		op:   opRewrite,
		rew:  rewriteRequest{id: id, span: span, reference: reference},
		done: make(chan writeOutcome, 1),
	}
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
	select {
	case out := <-req.done:
		return out.err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return ErrClosed
	}
}

func (s *sqliteContent) doRewrite(ctx context.Context, rr rewriteRequest) error {
	var command string
	var redactionsJSON string
	err := s.db.QueryRowContext(
		ctx,
		"SELECT command, redactions FROM command_history WHERE id = ?", rr.id,
	).Scan(&command, &redactionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var redactions []Redaction
	if uerr := json.Unmarshal([]byte(redactionsJSON), &redactions); uerr != nil {
		return uerr
	}
	// The span is byte offsets into the stored command. A span that no
	// longer fits means the row changed shape underneath this caller —
	// refuse rather than corrupt.
	if rr.span.Start < 0 || rr.span.End > len(command) || rr.span.Start > rr.span.End {
		return fmt.Errorf("content: redaction span [%d:%d] out of range for row %d", rr.span.Start, rr.span.End, rr.id)
	}
	// Idempotency: the span must be one of the row's CURRENT redactions.
	// A retried save (a lost response) re-sends the span it captured at
	// record time; the first attempt already removed it, so the retry is a
	// no-op instead of replacing text at stale offsets.
	matched := false
	kept := make([]Redaction, 0, len(redactions))
	for _, r := range redactions {
		if r.Start == rr.span.Start && r.End == rr.span.End && r.Kind == rr.span.Kind {
			matched = true
			continue
		}
		kept = append(kept, r)
	}
	if !matched {
		return nil
	}
	newCommand := command[:rr.span.Start] + rr.reference + command[rr.span.End:]
	keptJSON, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(
		ctx,
		"UPDATE command_history SET command = ?, redactions = ? WHERE id = ?",
		newCommand, string(keptJSON), rr.id,
	); err != nil {
		return err
	}
	enforceFileModes(s.path)
	return nil
}

const recordCols = "id, command, cwd, host, status, exit_code, started_at, ended_at, trusted, masked_count, masked_kinds, redactions"

func scanRecord(row interface{ Scan(...any) error }) (CommandRecord, error) {
	var r CommandRecord
	var kindsJSON string
	var redactionsJSON string
	err := row.Scan(&r.ID, &r.Command, &r.Cwd, &r.Host, &r.Status, &r.ExitCode, &r.StartedAt, &r.EndedAt, &r.Trusted, &r.MaskedCount, &kindsJSON, &redactionsJSON)
	if err != nil {
		return CommandRecord{}, err
	}
	if err := json.Unmarshal([]byte(kindsJSON), &r.MaskedKinds); err != nil {
		return CommandRecord{}, err
	}
	if err := json.Unmarshal([]byte(redactionsJSON), &r.Redactions); err != nil {
		return CommandRecord{}, err
	}
	return r, nil
}

// List returns the limit newest records, newest first.
func (s *sqliteContent) List(ctx context.Context, limit int) ([]CommandRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+recordCols+
		" FROM command_history ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CommandRecord
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetByID returns one record, or (nil, nil) when no row carries that id.
func (s *sqliteContent) GetByID(ctx context.Context, id int64) (*CommandRecord, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+recordCols+" FROM command_history WHERE id = ?", id)
	r, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// FindByPrefix returns the limit newest records whose command starts with
// prefix. LIKE wildcards in the prefix are escaped: a prefix containing % or
// _ matches them literally.
func (s *sqliteContent) FindByPrefix(ctx context.Context, prefix string, limit int) ([]CommandRecord, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	rows, err := s.db.QueryContext(ctx, "SELECT "+recordCols+
		" FROM command_history WHERE command LIKE ? ESCAPE '\\' ORDER BY id DESC LIMIT ?",
		escaped+"%", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CommandRecord
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Query returns one page of history for the recall-ladder rung, newest first
// (contracts/history.query.schema.json). The directory rung is the exact
// (cwd, host) pair — the overlay's own rung semantics, design §10.6. The page
// and the store-wide row count are read in one read transaction so HasRows
// cannot race a concurrent write.
//
// text is the search filter (nocx-ms7v): a case-insensitive substring over
// command, applied WITHIN the rung — the server never silently widens. Empty
// means no filter. There is deliberately no FTS: a substring match cannot use
// an index, and at command-history sizes a full scan of the rung is cheap —
// measured 100k rows, filter hit, ~260 µs per query (dev machine, WAL warm),
// so the overlay's per-keystroke queries are nowhere near a frame budget.
// FTS arrives with output search, whose indexing unit is still an open
// decision.
//
// Coverage is the store-wide MIN(ended_at) — how far back retention lets this
// answer see, independent of the rung and the filter. It is read in the same
// transaction so the horizon and the page cannot disagree about the store's
// state.
func (s *sqliteContent) Query(ctx context.Context, scope Scope, cwd, host string, limit int, before *int64, text string) (HistoryPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return HistoryPage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	cond, args := scopeWhere(scope, cwd, host)
	if before != nil {
		if cond == "" {
			cond = " WHERE id < ?"
		} else {
			cond += " AND id < ?"
		}
		args = append(args, *before)
	}
	// The filter is a parameterized case-folded substring predicate, not
	// LIKE: instr() has no wildcard grammar, so a search for "100%_done"
	// matches that literal command and nothing else. lower(?) is bound once;
	// lower(command) is computed per row (no index — the measurement above).
	if text != "" {
		if cond == "" {
			cond = " WHERE instr(lower(command), lower(?)) > 0"
		} else {
			cond += " AND instr(lower(command), lower(?)) > 0"
		}
		args = append(args, text)
	}
	// Fetch limit+1: one extra row proves the rung is not exhausted.
	// cond and recordCols are package constants — never user input.
	rows, err := tx.QueryContext(ctx, "SELECT "+recordCols+ //nolint:gosec // constant fragments
		" FROM command_history"+cond+" ORDER BY id DESC LIMIT ?",
		append(args, limit+1)...)
	if err != nil {
		return HistoryPage{}, err
	}
	entries := []CommandRecord{}
	extra := false
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			_ = rows.Close()
			return HistoryPage{}, err
		}
		if len(entries) == limit {
			extra = true
			break
		}
		entries = append(entries, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return HistoryPage{}, err
	}

	var total int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM command_history").Scan(&total); err != nil {
		return HistoryPage{}, err
	}
	// MIN ignores NULL ended_at (running entries), so a store full of
	// running rows reports no horizon rather than a misleading one.
	var coverage *int64
	if err := tx.QueryRowContext(ctx, "SELECT MIN(ended_at) FROM command_history").Scan(&coverage); err != nil {
		return HistoryPage{}, err
	}
	if err := tx.Commit(); err != nil {
		return HistoryPage{}, err
	}
	return HistoryPage{Entries: entries, Exhausted: !extra, HasRows: total > 0, Coverage: coverage}, nil
}

func scopeWhere(scope Scope, cwd, host string) (string, []any) {
	switch scope {
	case ScopeDirectory:
		return " WHERE cwd = ? AND host = ?", []any{cwd, host}
	case ScopeHost:
		return " WHERE host = ?", []any{host}
	default:
		return "", nil
	}
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

func (s *sqliteContent) CommandHistory() CommandHistoryRepository {
	return s
}

// Ledger returns the schema-v1 repository (ledger.go). Until nocx-rtg0.3
// wires the ledger.* wire methods to this surface, its only callers are
// tests — the v1 write path has no production caller yet (stated loudly in
// the task report: the same shape shipped once before as a silent dead path).
func (s *sqliteContent) Ledger() LedgerRepository {
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
