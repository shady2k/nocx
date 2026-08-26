package content

// The SQLite implementation of LedgerRepository — schema v1 of the one
// authoritative ledger (nocx-rtg0.2), per ADR-0019, ADR-0020 and design
// §5.2. Every mutation goes through the single writer goroutine (run in
// sqlite.go — design §5.3); every read goes through the pool directly.
//
// The entry lifecycle — Submit, StartExecution, FinishExecution — is driven
// in production by the ledger.* control methods (nocx-rtg0.3,
// internal/transport/ws_ledger.go); the ask transaction drives CaptureFrame,
// SubmitAgentAsk, TransitionRun and FinishAgentRun. The methods with no
// production caller yet are named in ledger.go's header, which is where that
// list is kept — deadcode cannot answer the question for this package, since
// RTA calls every method here reflection-reachable.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The v1 methods hang off *sqliteContent and are promoted into ledgerRepo,
// which adds the one method whose key is the ledger's own (sqlite.go).

// ── identity and narrative scope ─────────────────────────────────────────

// CreateWorkspace moved to layout_sqlite.go with nocx-isoph.1: the workspace
// is the head of the layout chain the backend now owns, and one table with two
// repository owners is the defect that design exists to avoid. What the ledger
// keeps is the reference — sessions.workspace_id — and the fallback default
// row ensureSessionRecorded writes for a session nobody has recorded yet.

func (s *sqliteContent) CreateSession(ctx context.Context, sess Session) error {
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO sessions (id, workspace_id, started_at) VALUES (?, ?, ?)`,
			sess.ID, sess.WorkspaceID, time.Now().UnixMilli())
		return err
	})
}

// DeleteSession removes a restore key. Entries keep their rows: the ON
// DELETE SET NULL on entries.session_id is the ADR-0019 §5 rule — an entry
// outlives its session.
func (s *sqliteContent) DeleteSession(ctx context.Context, id string) error {
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return err
	})
}

// EnsureEnvironment records durable identity. ON CONFLICT(id) DO NOTHING:
// identity is derived from the facets, so a changed identity is a new id,
// never an UPDATE of this one — and unlike INSERT OR IGNORE, a CHECK
// violation (an unknown kind) still errors instead of silently vanishing.
func (s *sqliteContent) EnsureEnvironment(ctx context.Context, env Environment) error {
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO environments (id, kind, endpoint, profile_id, first_seen, payload)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			env.ID, string(env.Kind), env.Endpoint, env.ProfileID, time.Now().UnixMilli(), env.Payload)
		return err
	})
}

// RecordObservation appends one versioned observation. Version ascends per
// environment; the row id returned is what an execution pins.
func (s *sqliteContent) RecordObservation(ctx context.Context, obs Observation) (int64, error) {
	var id int64
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		var version int
		if rowErr := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) + 1 FROM environment_observations WHERE environment_id = ?`,
			obs.EnvironmentID).Scan(&version); rowErr != nil {
			return rowErr
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO environment_observations
			(environment_id, version, observed_at, confidence, criticality, payload)
			VALUES (?, ?, ?, ?, ?, ?)`,
			obs.EnvironmentID, version, time.Now().UnixMilli(), obs.Confidence,
			string(obs.Criticality), obs.Payload)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return id, err
}

// ── entries ──────────────────────────────────────────────────────────────

// Submit accepts an intent as an open, pending entry. The client-minted id
// is an UNTRUSTED idempotency key: it is bound to the client identity and a
// digest of the submitted content, so a replay of the same id returns the
// original row (Replayed) and a replay with different content is refused
// (ErrIDConflict) — otherwise a replay aliases two different intents.
//
// ingest_seq is assigned from the one-row ledger_sequence counter in the
// SAME transaction as the insert (open question 2, decided): commit order IS
// counter order, a crash rolls both back together, and the UNIQUE constraint
// is the backstop. Two entries submitted in the same millisecond get
// distinct, ordered sequences — wall time is not a key.
func (s *sqliteContent) Submit(ctx context.Context, in SubmitEntry) (SubmitResult, error) {
	if in.ID == "" {
		return SubmitResult{}, errors.New("content: submit: entry id is required")
	}
	if in.Client == "" {
		return SubmitResult{}, errors.New("content: submit: client is required — it binds the idempotency key")
	}
	if in.Sensitivity == "" {
		in.Sensitivity = SensitivityNormal
	}
	// Empty Source is the ordinary shell path — every caller before the
	// source split named no source, and a person at the keyboard is what
	// those rows are. Normalized HERE, before the digest below, so a
	// legacy empty-source replay hashes identically to the normalized row
	// it created: replay of the same id must not see a different digest
	// and refuse the very row it wrote. The assistant's callers name
	// SourceAssistant explicitly; nothing in this store guesses it.
	if in.Source == "" {
		in.Source = SourceUser
	}
	digest := entryDigest(in)
	var out SubmitResult
	err := s.run(ctx, func(ctx context.Context) error {
		// BEGIN IMMEDIATE (the ncruces driver maps LevelSerializable to it):
		// the write lock is taken at BEGIN, not at the first write. With a
		// deferred BEGIN, two processes — each with its own writer goroutine
		// — can both read the same snapshot, and the loser's upgrade then
		// fails with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not
		// repair (nocx-rtg0.18). Taking the lock up front makes the second
		// writer wait, bounded by busy_timeout, and read a fresh snapshot:
		// both submits land, in commit order.
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var seq, submittedAt int64
		var haveClient, haveDigest string
		err = tx.QueryRowContext(ctx,
			`SELECT ingest_seq, submitted_at, client, digest FROM entries WHERE id = ?`, in.ID,
		).Scan(&seq, &submittedAt, &haveClient, &haveDigest)
		switch {
		case err == nil:
			if haveClient != in.Client || haveDigest != digest {
				return ErrIDConflict
			}
			out = SubmitResult{ID: in.ID, IngestSeq: seq, SubmittedAt: submittedAt, Replayed: true}
			return tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		var next int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`,
		).Scan(&next); err != nil {
			return fmt.Errorf("content: assign ingest_seq: %w", err)
		}
		// The anchor is RESOLVED before the write, not left to the foreign
		// key — the same rule the layout chain follows when it inserts a tab
		// (layout_sqlite.go): the FK would refuse a dangling pane_id anyway,
		// but a driver's constraint text does not say WHICH reference was
		// missing, and entries has three. ErrNoSuchPane is the answer this
		// repository already gives to "that pane does not exist"; a second
		// name for it would be a second owner of one fact.
		if in.PaneID != nil {
			if _, err := paneByID(ctx, tx, *in.PaneID); err != nil {
				return err
			}
		}
		submittedAt = time.Now().UnixMilli()
		// A `text` block is BORN CLOSED (ADR-0040). Every other kind opens a
		// lifecycle the driver will bind and close; prose has none — it was
		// printed, so there is nothing to wait for and nothing to judge. The
		// schema's CHECK says exactly this, and the two are read together
		// deliberately: the enum's mirror is the CHECK (this file's header),
		// so the one writer of the column obeys it here rather than handing
		// the constraint a row it will refuse.
		phase, status := PhaseOpen, EntryPending
		if in.Kind == EntryText {
			phase, status = PhaseClosed, EntrySuccess
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, session_id, parent_id, pos,
			 cwd, kind, source, intent, phase, status, submitted_at, started_at, ended_at,
			 duration_ms, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, next, in.Client, digest, in.EnvironmentID, in.PaneID, in.SessionID,
			in.ParentID, in.Pos, in.Cwd, string(in.Kind), string(in.Source), in.Intent,
			string(phase), string(status), submittedAt, in.StartedAt, in.EndedAt, in.DurationMs,
			string(in.Sensitivity), in.Payload,
		); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		out = SubmitResult{ID: in.ID, IngestSeq: next, SubmittedAt: submittedAt}
		// Age retention, in the same writer turn and after the entry is
		// durable — the placement the command-history sweep already uses
		// (doAdd). It runs here rather than on a timer because this is the
		// one moment the ledger is known to have grown, and it is
		// best-effort: the submit above has committed, so an eviction
		// failure must not turn it into an error the caller would retry.
		s.evictOnWrite(ctx)
		return nil
	})
	return out, err
}

// entryDigest is the content binding of the idempotency key. The store
// derives it from the submitted content (the client never sends it — that
// would be forgeable), so replay of the same id cannot alias a different
// intent. The struct field order is fixed, so the hash is deterministic.
func entryDigest(in SubmitEntry) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(struct {
		Client, EnvironmentID, Cwd, Intent, Payload string
		Kind, Source, Sensitivity                   string
		PaneID, SessionID, ParentID                 *string
		Pos                                         *int
	}{
		Client: in.Client, EnvironmentID: in.EnvironmentID, Cwd: in.Cwd, Intent: in.Intent,
		Payload: in.Payload, Kind: string(in.Kind), Source: string(in.Source), Sensitivity: string(in.Sensitivity),
		PaneID: in.PaneID, SessionID: in.SessionID, ParentID: in.ParentID, Pos: in.Pos,
	})
	return hex.EncodeToString(h.Sum(nil))
}

// ── the environment an entry ran in (nocx-rtg0.25) ───────────────────────

// environmentColumns is the environment half of every entry read, LEFT
// JOINed as `env`. It is a const so the two reads below cannot drift into
// two shapes, and so the join stays part of the entry query rather than
// something a caller adds per row: nocx-rtg0.20's ledger.query is built on
// ListEntries, and one lookup per row is how a page of history becomes a
// page of queries.
//
// LEFT, not INNER: entries.environment_id has a foreign key, so a missing
// environment cannot happen through this seam — but an INNER join would
// answer a vanished environment by dropping the entry, which is the one
// answer worse than "unknown".
const environmentColumns = `env.id, env.kind, env.endpoint, env.profile_id, env.payload`

// environmentJoin resolves the environment for the entry table aliased `e`.
const environmentJoin = `LEFT JOIN environments env ON env.id = e.environment_id`

// environmentScan holds the joined columns. Every one is nullable here
// because the join itself can miss, so the whole record is nil-or-present:
// the read never fills in a default, which for `endpoint` would be the
// empty string — a real value meaning the local machine.
type environmentScan struct {
	id        sql.NullString
	kind      sql.NullString
	endpoint  *string
	profileID *string
	payload   sql.NullString
}

// dest is the scan target list, in environmentColumns order.
func (r *environmentScan) dest() []any {
	return []any{&r.id, &r.kind, &r.endpoint, &r.profileID, &r.payload}
}

// value is the resolved environment, or nil when no environment row carries
// the entry's environment_id.
func (r *environmentScan) value() *Environment {
	if !r.id.Valid {
		return nil
	}
	return &Environment{
		ID:        r.id.String,
		Kind:      EnvironmentKind(r.kind.String),
		Endpoint:  r.endpoint,
		ProfileID: r.profileID,
		Payload:   r.payload.String,
	}
}

// Entry is the recall read: the entry, its environment, its executions, each
// execution's pinned observation and grant, and its artifacts (metadata only
// — the recall read never hauls chunk bodies; Artifact fetches those).
func (s *sqliteContent) Entry(ctx context.Context, id string) (*LedgerEntry, error) {
	e := &LedgerEntry{}
	var env environmentScan
	dest := []any{
		&e.ID, &e.IngestSeq, &e.Client, &e.Digest, &e.EnvironmentID, &e.PaneID, &e.SessionID,
		&e.ParentID, &e.Pos, &e.Cwd,
		&e.Kind, &e.Source, &e.Intent, &e.Phase, &e.Status, &e.SubmittedAt,
		&e.StartedAt, &e.EndedAt, &e.DurationMs, &e.Sensitivity, &e.Payload, &e.ProseEvicted,
	}
	// ProseEvicted is asked of the RUN and answered ONCE, whatever the run
	// was cut into: EXISTS over the receipts retention left on the bodies of
	// this block's `text` children (retention.go). It is a subquery rather
	// than a stored column because there is one stored fact — the receipt on
	// the body — and a second copy of it on the turn would be a second
	// answer to drift from the first.
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.ingest_seq, e.client, e.digest,
		e.environment_id, e.pane_id, e.session_id, e.parent_id, e.pos, e.cwd, e.kind, e.source,
		e.intent, e.phase, e.status, e.submitted_at, e.started_at, e.ended_at, e.duration_ms,
		e.sensitivity, e.payload,
		EXISTS (SELECT 1 FROM entries c JOIN artifacts a ON a.entry_id = c.id
		         WHERE c.parent_id = e.id AND c.kind = 'text'
		           AND `+bodyEvictedExpr("a")+`), `+environmentColumns+`
		FROM entries e `+environmentJoin+` WHERE e.id = ?`, id).
		Scan(append(dest, env.dest()...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Environment = env.value()
	execs, err := s.executionsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Executions = execs
	own, err := s.ownArtifactsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Artifacts = own
	return e, nil
}

// ownArtifactsFor reads the bodies this ENTRY owns and no execution produced
// (ADR-0040). The `execution_id IS NULL` is what keeps it disjoint from
// artifactsFor: a command's output belongs to the attempt that produced it
// and is listed there, so no body appears in both lists.
func (s *sqliteContent) ownArtifactsFor(ctx context.Context, entryID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM artifacts WHERE entry_id = ? AND execution_id IS NULL ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The row loop is CLOSED before the per-id reads: artifactByID goes back
	// to the pool, and holding an open cursor across it is how a single-
	// connection store deadlocks against itself.
	out := make([]Artifact, 0, len(ids))
	for _, id := range ids {
		a, err := s.artifactByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// ── the recall read (nocx-rtg0.20) ───────────────────────────────────────

// entryPageColumns is the timeline row: the entry's own facts plus the
// environment half of the join. One const, so ListEntries and QueryEntries
// cannot drift into two row shapes.
const entryPageColumns = `e.id, e.ingest_seq, e.environment_id, e.cwd, e.kind, e.source, e.intent,
	e.phase, e.status, e.submitted_at, e.started_at, e.ended_at, e.duration_ms, e.payload, ` +
	environmentColumns

// rowQuerier is the read seam both the pool and a transaction satisfy, so
// the page statement is written once and runs in either — ListEntries reads
// straight off the pool, QueryEntries reads inside the transaction that also
// carries HasRows and the horizon.
//
// QueryRowContext is on it for the same reason: the layout chain's single-row
// reads (layout_sqlite.go) run standalone and inside the transaction that
// wrote the row. One seam for "something rows can be read through" is the
// point of the type; a second interface with one method would be a second
// name for one concept.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// entryPage runs the ONE page statement: the filter, the order and the join,
// in a single query however many rows and however many hosts it spans. cond
// is built by ledgerWhere out of package constants — never out of user text.
func entryPage(ctx context.Context, q rowQuerier, cond string, args []any, limit int) ([]LedgerEntrySummary, error) {
	rows, err := q.QueryContext(ctx, //nolint:gosec // constant fragments; every value is bound
		`SELECT `+entryPageColumns+` FROM entries e `+environmentJoin+cond+
			` ORDER BY e.ingest_seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []LedgerEntrySummary{}
	for rows.Next() {
		var e LedgerEntrySummary
		var env environmentScan
		dest := []any{
			&e.ID, &e.IngestSeq, &e.EnvironmentID, &e.Cwd, &e.Kind, &e.Source,
			&e.Intent, &e.Phase, &e.Status, &e.SubmittedAt,
			&e.StartedAt, &e.EndedAt, &e.DurationMs, &e.Payload,
		}
		if err := rows.Scan(append(dest, env.dest()...)...); err != nil {
			return nil, err
		}
		e.Environment = env.value()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ledgerWhere is the rung and the filters, in SQL. It is the counterpart of
// scopeWhere on the interim table (sqlite.go) and obeys the same rule: the
// server answers from the rung it was asked for and never silently widens.
// The rung's coordinates are the environment identity and the directory —
// which is what entries_by_env(environment_id, cwd, ingest_seq DESC) is for.
func ledgerWhere(q LedgerQuery) (string, []any) {
	var conds []string
	var args []any
	switch q.Scope {
	case ScopeDirectory:
		conds = append(conds, "e.environment_id = ?", "e.cwd = ?")
		args = append(args, q.EnvironmentID, q.Cwd)
	case ScopeHost:
		conds = append(conds, "e.environment_id = ?")
		args = append(args, q.EnvironmentID)
	case ScopeEverywhere:
		// No rung filter. environmentId and cwd are the rung's own
		// coordinates, so they are not applied here — the rung is echoed
		// back to the caller, which is how "everywhere" stays visible.
	}
	if q.Kind != "" {
		conds = append(conds, "e.kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.Status != "" {
		conds = append(conds, "e.status = ?")
		args = append(args, string(q.Status))
	}
	// One pane's blocks (design §8): the restore read, served by
	// entries_by_pane. It composes with the rung rather than replacing it,
	// so "this pane, on this host" is one query and not two semantics.
	if q.PaneID != "" {
		conds = append(conds, "e.pane_id = ?")
		args = append(args, q.PaneID)
	}
	// The search box, and it is the SAME predicate the interim path answers
	// (sqlite.go's Query, nocx-ms7v) — one matching semantics for one product
	// object, extended to the ledger's column rather than reinvented beside
	// it. instr() over lower(), not LIKE: there is no wildcard grammar, so a
	// search for "100%_done" matches that literal intent and nothing else.
	// lower(?) is bound once; lower(e.intent) is computed per row, since no
	// index can serve a substring anyway. Empty is no filter, the state an
	// absent field arrives as.
	if q.Text != "" {
		conds = append(conds, "instr(lower(e.intent), lower(?)) > 0")
		args = append(args, q.Text)
	}
	if q.Since != nil {
		conds = append(conds, "e.submitted_at >= ?")
		args = append(args, *q.Since)
	}
	if q.Before != nil {
		conds = append(conds, "e.ingest_seq < ?")
		args = append(args, *q.Before)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// validateLedgerQuery refuses what the store cannot answer honestly. Every
// one of these would otherwise come back as an empty page, and an empty page
// is the answer most likely to be believed: "nothing ever failed on this
// host" is indistinguishable from "you misspelled the status".
func validateLedgerQuery(q LedgerQuery) error {
	switch q.Scope {
	case ScopeDirectory, ScopeHost:
		if q.EnvironmentID == "" {
			return fmt.Errorf("content: query: scope %q needs an environment id — a rung with no coordinates matches nothing", q.Scope)
		}
	case ScopeEverywhere:
	default:
		return fmt.Errorf("content: query: unknown scope %q", q.Scope)
	}
	switch q.Kind {
	case "", EntryShell, EntryAsk, EntryAction, EntryFrame, EntryText:
	default:
		return fmt.Errorf("content: query: unknown kind %q", q.Kind)
	}
	switch q.Status {
	case "", EntryPending, EntryRunning, EntrySuccess, EntryFailure, EntryInterrupted, EntryUnknown:
	default:
		return fmt.Errorf("content: query: unknown status %q", q.Status)
	}
	if q.Limit < 1 || q.Limit > MaxLedgerPageLimit {
		return fmt.Errorf("content: query: limit %d is outside [1, %d]", q.Limit, MaxLedgerPageLimit)
	}
	if q.BeforeID != "" && q.Before != nil {
		return fmt.Errorf("content: query: before and beforeId are two answers to where the page starts; send one")
	}
	if q.Before != nil && *q.Before < 1 {
		return fmt.Errorf("content: query: before %d is not an ingest_seq", *q.Before)
	}
	if q.Since != nil && *q.Since < 0 {
		return fmt.Errorf("content: query: since %d is not a wall clock", *q.Since)
	}
	return nil
}

// QueryEntries is the recall read and the only ordering implementation
// (design §6.2). One page of the rung asked for, newest first by ingest_seq
// — the backend-assigned total order, so two entries submitted in the same
// millisecond still have one — plus the three facts that keep the answer
// honest.
//
// The page, HasRows and the horizon are read in ONE read transaction, for
// the reason the interim Query states and this one inherits: a HasRows taken
// after the page could report a store that emptied or filled in between, and
// a horizon that disagrees with its page is worse than no horizon.
//
// limit+1 rows are fetched and the extra one is dropped: the row that is not
// returned is what proves the rung is not exhausted, and it costs one row
// rather than a second count over the same predicate.
func (s *sqliteContent) QueryEntries(ctx context.Context, q LedgerQuery) (LedgerPage, error) {
	if err := validateLedgerQuery(q); err != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, err
	}
	defer func() { _ = tx.Rollback() }()

	// The cursor, resolved INSIDE the read transaction so the page and the
	// position it starts from cannot disagree about what the store holds
	// (nocx-rtg0.19). One primary-key lookup; the ordering is still
	// ingest_seq and only ingest_seq.
	if q.BeforeID != "" {
		var seq int64
		switch cursorErr := tx.QueryRowContext(ctx,
			`SELECT ingest_seq FROM entries WHERE id = ?`, q.BeforeID).Scan(&seq); {
		case errors.Is(cursorErr, sql.ErrNoRows):
			return LedgerPage{Entries: []LedgerEntrySummary{}},
				fmt.Errorf("%w: cursor %q", ErrNotFound, q.BeforeID)
		case cursorErr != nil:
			return LedgerPage{Entries: []LedgerEntrySummary{}}, cursorErr
		}
		q.Before = &seq
	}

	cond, args := ledgerWhere(q)
	entries, err := entryPage(ctx, tx, cond, args, q.Limit+1)
	if err != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, err
	}
	exhausted := len(entries) <= q.Limit
	if !exhausted {
		entries = entries[:q.Limit]
	}

	// HasRows is EXISTS rather than a count: the question is whether the
	// ledger has anything to answer from, and a count would read every row
	// to answer a yes/no.
	var hasRows bool
	if existsErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entries)`).Scan(&hasRows); existsErr != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, existsErr
	}
	// Coverage has TWO sources, and which one is honest depends on whether
	// this store has ever evicted (§5.4, nocx-rtg0.12).
	//
	// Once it has, the watermark owns the answer: the rows that would have
	// carried the horizon are deleted, so no query over the survivors can
	// recover it. MIN(ended_at) would then report the oldest row eviction
	// happened to leave — and would report NO horizon at all for a store
	// evicted empty, which reads as "nothing was ever here". That is the
	// failure this field exists to prevent.
	//
	// Until it has, the surviving rows ARE the whole store, so the oldest
	// one is the honest horizon — and it is the better answer, because it is
	// exact where the watermark would still be null.
	//
	// Read in the same transaction as the page: a horizon that disagrees
	// with its page is worse than no horizon. MIN ignores NULL ended_at
	// (entries still running), so a ledger holding nothing but live commands
	// reports no horizon rather than a misleading one. No rung and no filter
	// appear on either path: retention is store-wide, so the horizon it
	// leaves is store-wide too.
	wm, err := s.watermark(ctx, tx)
	if err != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, err
	}
	coverage := wm.Horizon
	if wm.EvictedCount == 0 {
		if minErr := tx.QueryRowContext(ctx, `SELECT MIN(ended_at) FROM entries`).Scan(&coverage); minErr != nil {
			return LedgerPage{Entries: []LedgerEntrySummary{}}, minErr
		}
	}
	if err := tx.Commit(); err != nil {
		return LedgerPage{Entries: []LedgerEntrySummary{}}, err
	}
	return LedgerPage{Entries: entries, Exhausted: exhausted, HasRows: hasRows, Coverage: coverage}, nil
}

// ListEntries returns the limit newest entries, newest first, ordered by
// ingest_seq — commit order, never by wall clock (two entries in the same
// millisecond still have an order). Each row carries its environment, joined
// in this one statement: a page costs one query whatever its length and
// however many hosts it spans.
//
// It runs QueryEntries' page statement with no rung and no filter, so the
// ordering has ONE implementation; it stops there because the timeline read
// has no honesty facts to state — a caller that needs the horizon or wants
// to know whether the store holds anything asks the query for them.
func (s *sqliteContent) ListEntries(ctx context.Context, limit int) ([]LedgerEntrySummary, error) {
	return entryPage(ctx, s.db, "", nil, limit)
}

// RewriteRedaction turns one masked span on a ledger row into a vault
// reference: the intent gets the reference, the receipt in entries.payload
// loses the segment. Read-modify-write runs inside ONE writer turn (s.run),
// so no other mutation can interleave between the read and the update.
//
// The entry's digest is deliberately NOT recomputed. It binds the untrusted
// client id to the content as SUBMITTED, and a replay from the renderer's
// outbox re-sends the original command, which masks back to the original
// intent — recomputing the digest here would turn every such replay into
// ErrIDConflict. A rewrite is a later event on a recorded row, not a second
// submission of one intent (the same reasoning FinishExecution's header
// gives for keeping the close off Submit).
func (s *sqliteContent) RewriteRedaction(ctx context.Context, entryID string, span Redaction, reference string) error {
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var intent, payload string
		err = tx.QueryRowContext(ctx,
			`SELECT intent, payload FROM entries WHERE id = ?`, entryID).Scan(&intent, &payload)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		masking, err := EntryMaskingOf(payload)
		if err != nil {
			return err
		}
		newIntent, kept, matched, err := applyRedactionRewrite(
			intent, masking.Redactions, span, reference, entryID)
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		masking.Redactions = kept
		newPayload, err := WithEntryMasking(payload, masking)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET intent = ?, payload = ? WHERE id = ?`,
			newIntent, newPayload, entryID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// DeleteEntry removes an entry; its edges, executions, artifacts, chunks and
// grant cascade (open question 5, decided: an edge whose endpoint is gone is
// meaningless). Idempotent for a missing id. A pin protects against
// background eviction, never against this explicit deletion.
func (s *sqliteContent) DeleteEntry(ctx context.Context, id string) error {
	return s.run(ctx, func(ctx context.Context) error {
		// THE SEAT GOES WITH THE PARENT, exactly as eviction does
		// (retention.go): ON DELETE SET NULL nulls the children's
		// parent_id, and the schema's CHECK (parent_id IS NOT NULL OR
		// pos IS NULL) would then refuse surviving children that still
		// hold a seat. Detach them first, in the same transaction.
		if _, err := s.db.ExecContext(ctx, `UPDATE entries SET pos = NULL WHERE parent_id = ?`, id); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE id = ?`, id)
		return err
	})
}

// ── executions ───────────────────────────────────────────────────────────

// StartExecution begins one run of an entry (ADR-0020 §4: a rerun, a retry,
// a takeover and an infrastructure failure are executions of the same entry,
// never new intents). The store pins the environment observation current at
// THIS moment — a later observation never moves it, so an old execution is
// never reinterpreted with today's facts. Fails when the entry's environment
// has no observation yet: an unpinned execution would be exactly the
// reinterpretation the amendment forbids.
//
// Grant, when non-nil, is recorded on the run: versioned, expiring,
// immutable once execution starts (no update path exists). The workspace
// minted it; this table is the receipt, not the enforcement object.
func (s *sqliteContent) StartExecution(ctx context.Context, in StartExecution) (int64, error) {
	if in.Interactivity == "" {
		in.Interactivity = InteractivityNone
	}
	if in.Attempt <= 0 {
		in.Attempt = 1
	}
	var id int64
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var envID string
		err = tx.QueryRowContext(ctx,
			`SELECT environment_id FROM entries WHERE id = ?`, in.EntryID).Scan(&envID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: start execution: no such entry %q", in.EntryID)
		}
		if err != nil {
			return err
		}
		var obsID int64
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM environment_observations WHERE environment_id = ? ORDER BY version DESC LIMIT 1`,
			envID).Scan(&obsID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: start execution: no observation recorded for the entry's environment — nothing to pin")
		}
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO executions
			(entry_id, lane, attempt, environment_obs_id, lease_deadline, inactivity_deadline,
			 interactivity, process_group, started_at, executor)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.EntryID, in.Lane, in.Attempt, obsID, in.LeaseDeadline, in.InactivityDeadline,
			string(in.Interactivity), in.ProcessGroup, time.Now().UnixMilli(), in.Executor)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if in.Grant != nil {
			// The policy column holds the decision MATRIX as JSON (ADR-0020
			// §7 as amended 2026-08-16): the recorded grant's rows are what
			// the decision was, so "what was this allowed to do" is a query
			// over the record, never a reconstruction. A grant whose
			// policy the store cannot reproduce is not recorded — writing
			// a run whose authority cannot be answered later is the hole
			// ADR-0020 decision 5 exists to close.
			policyJSON, err := json.Marshal(in.Grant.Policy)
			if err != nil {
				return fmt.Errorf("authority grant policy: %w", err)
			}
			g, err := tx.ExecContext(ctx, `INSERT INTO authority_grants
				(execution_id, version, issued_at, expires_at, policy) VALUES (?, ?, ?, ?, ?)`,
				id, in.Grant.Version, time.Now().UnixMilli(), in.Grant.ExpiresAt,
				string(policyJSON))
			if err != nil {
				return err
			}
			grantID, err := g.LastInsertId()
			if err != nil {
				return err
			}
			for _, sc := range in.Grant.Scopes {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO grant_scopes (grant_id, resource_kind, resource_id) VALUES (?, ?, ?)`,
					grantID, string(sc.Kind), sc.ID); err != nil {
					return err
				}
			}
			for _, e := range in.Grant.Effects {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO grant_effects (grant_id, effect) VALUES (?, ?)`,
					grantID, string(e)); err != nil {
					return err
				}
			}
		}
		// The entry is bound while a run is live (phase owned by the driver;
		// the store records the transition).
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET phase = 'bound' WHERE id = ?`, in.EntryID); err != nil {
			return err
		}
		return tx.Commit()
	})
	return id, err
}

// FinishExecution closes the run with its termination reason (the five
// outcomes one status plus exit code cannot separate — ADR-0020 §4) and
// closes the entry with its final status AND its terminal facts: the start
// if this is what learns it, the end, the measured duration and the kind
// payload. For an agent run (state IS NOT NULL) it also maps the termination
// to the terminal run state the renderer draws — completed | cancelled |
// failed | interrupted — in the SAME update, so the run is never reported
// terminal in one vocabulary and not the other. Executions without a state
// (a frame capture) are untouched.
//
// The entry's facts are written HERE, inside the run's own transaction,
// rather than through Submit (nocx-rtg0.23). Two reasons, and the second is
// the one that decided it. Submit is write-once: its id is an idempotency
// key bound to a digest of the submitted content, so a later fact routed
// through it would change the digest and make every replay of the original
// open an ErrIDConflict. And a close is one commit or none: a crash between
// the run's end and the entry's payload would leave a closed entry with no
// exit code, which is exactly the state a reader cannot tell from "the
// command produced none".
//
// COALESCE is the "fills what is missing, overwrites nothing" rule in SQL:
// started_at keeps a start the row already knew and duration_ms keeps what it
// holds when the close carries none. ended_at is assigned outright because a
// close always knows when it ended.
//
// payload is MERGED rather than assigned (json_patch, RFC 7396), because two
// writers own different keys of that one column (nocx-rtg0.24): the close
// writes the kind arm, and the open wrote the redaction receipt that a
// capture save then rewrites. Assigning here would have the close erase the
// receipt — or, if the close carried its own copy, resurrect a span a save
// had already settled, which is precisely the stale-offset replacement the
// rewrite's idempotency rule exists to prevent. A NULL payload still touches
// nothing at all.
func (s *sqliteContent) FinishExecution(ctx context.Context, executionID int64, end FinishExecution) error {
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		var entryID string
		err = tx.QueryRowContext(ctx,
			`SELECT entry_id FROM executions WHERE id = ?`, executionID).Scan(&entryID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: finish execution: no such execution %d", executionID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE executions SET ended_at = ?, termination_reason = ?,
			   state = CASE WHEN state IS NOT NULL THEN ? ELSE state END
			 WHERE id = ?`,
			end.EndedAt, string(end.TerminationReason),
			string(runStateForTermination(end.TerminationReason)), executionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET phase = 'closed', status = ?,
			   started_at = COALESCE(started_at, ?),
			   ended_at = ?,
			   duration_ms = COALESCE(?, duration_ms),
			   payload = CASE WHEN ? IS NULL THEN payload ELSE json_patch(payload, ?) END
			 WHERE id = ?`,
			string(end.Status), end.StartedAt, end.EndedAt, end.DurationMs,
			end.Payload, end.Payload, entryID); err != nil {
			return err
		}
		// An ASK run seals the body it was streaming into, in the same
		// transaction — any terminalizer keeps the ledger consistent, not
		// just FinishAgentRun. An ordinary command has no agent run and
		// this is a no-op.
		if err := sealTurnBody(ctx, tx, entryID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// runStateForTermination maps an execution's termination reason to the
// terminal run state on the wire (design §7). The mapping is owned HERE so
// the model half never invents a second one: the user asked for it
// (user-killed → cancelled), the backend was interrupted (interrupted),
// the model finished (completed), and everything else failed — the model,
// the transport, the timeout or the policy.
func runStateForTermination(r TerminationReason) RunState {
	switch r {
	case TermCompleted:
		return RunCompleted
	case TermUserKilled:
		return RunCancelled
	case TermInterrupted:
		return RunInterrupted
	default:
		return RunFailed
	}
}

// ── artifacts ────────────────────────────────────────────────────────────

// AppendArtifact creates one artifact of a BLOCK with its capture provenance
// (ADR-0019 §6: a capture records how it was taken — method and version,
// terminal dimensions, stream position, encoding, gaps). Content arrives via
// AppendChunk: an artifact is never one BLOB.
//
// The entry is required and the execution is not (ADR-0040): a body belongs
// to the block it is a body of, and which ATTEMPT produced it is a second,
// weaker fact that a `text` block simply does not have.
func (s *sqliteContent) AppendArtifact(ctx context.Context, in AppendArtifact) (string, error) {
	if in.ID == "" {
		return "", errors.New("content: append artifact: id is required")
	}
	if in.EntryID == "" {
		return "", errors.New("content: append artifact: entry id is required — an artifact belongs to its block")
	}
	err := s.run(ctx, func(ctx context.Context) error {
		return insertArtifact(ctx, s.db, in)
	})
	return in.ID, err
}

// insertArtifact and appendChunkAt take the `execer` sqlite.go already
// declares — the surface *sql.DB and *sql.Tx share — so the artifact and
// chunk statements are written ONCE and run either way: AppendArtifact on the
// store's own connection, CaptureOutput inside a transaction, with no second
// copy of the column list to drift from the first.
//
// insertArtifact is THE artifact insert. Its defaults live here rather than at
// a caller, so an artifact written by any path has the same shape.
func insertArtifact(ctx context.Context, q execer, in AppendArtifact) error {
	if in.CaptureMethod == "" {
		in.CaptureMethod = CaptureNone
	}
	if in.Encoding == "" {
		in.Encoding = "utf-8"
	}
	gaps := "[]"
	if len(in.Gaps) > 0 {
		b, err := json.Marshal(in.Gaps)
		if err != nil {
			return err
		}
		gaps = string(b)
	}
	var stream *string
	if in.Stream != nil {
		v := string(*in.Stream)
		stream = &v
	}
	var truncated *string
	if in.Truncated != nil {
		v := string(*in.Truncated)
		truncated = &v
	}
	payload := in.Payload
	if payload == "" {
		payload = "{}"
	}
	_, err := q.ExecContext(ctx, `INSERT INTO artifacts
		(id, entry_id, execution_id, media_type, derived_from, pinned, truncated, capture_method,
		 capture_version, terminal_cols, terminal_rows, stream, byte_offset, byte_end,
		 encoding, gaps, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.EntryID, in.ExecutionID, string(in.MediaType), in.DerivedFrom, in.Pinned, truncated,
		string(in.CaptureMethod), in.CaptureVersion, in.TerminalCols, in.TerminalRows,
		stream, in.ByteOffset, in.ByteEnd, in.Encoding, gaps, payload)
	return err
}

// appendChunkAt is THE chunk insert, and the idempotency point of the whole
// capture path: (artifact_id, seq) is the table's key, so a replayed chunk
// inserts nothing, and byte_len moves only when a row actually appeared.
func appendChunkAt(ctx context.Context, q execer, artifactID string, seq int, body []byte) error {
	res, err := q.ExecContext(ctx,
		`INSERT INTO artifact_chunks (artifact_id, seq, body) VALUES (?, ?, ?)
		 ON CONFLICT (artifact_id, seq) DO NOTHING`,
		artifactID, seq, body)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err = q.ExecContext(ctx,
		`UPDATE artifacts SET byte_len = byte_len + ? WHERE id = ?`, len(body), artifactID)
	return err
}

// CaptureOutput records one body of a frozen block (nocx-2f0f, design §4):
// the artifact if it is not there yet, then the chunk at its seq, in ONE
// transaction against the entry's own execution.
//
// The two refusals-that-are-not-errors are decided before the transaction
// opens, so nothing is written for a body nobody wants: output retention off
// is the user's setting, and a sensitive entry is the store's own rule about
// what a command's text says about its output.
func (s *sqliteContent) CaptureOutput(ctx context.Context, in CaptureOutput) (bool, error) {
	if in.EntryID == "" || in.ArtifactID == "" {
		return false, errors.New("content: capture: entry id and artifact id are required")
	}
	if in.Seq < 1 {
		return false, errors.New("content: capture: seq starts at 1")
	}
	if !s.policy.OutputEnabled() {
		return false, nil
	}
	stored := false
	err := s.run(ctx, func(ctx context.Context) error {
		// BEGIN IMMEDIATE for the reason Submit and RecordCompleted state:
		// the write lock is taken at BEGIN rather than at the first write, so
		// a second writer waits instead of failing an upgrade (nocx-rtg0.18).
		tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if txErr != nil {
			return txErr
		}
		defer func() { _ = tx.Rollback() }()

		var sensitivity string
		if err := tx.QueryRowContext(ctx,
			`SELECT sensitivity FROM entries WHERE id = ?`, in.EntryID).Scan(&sensitivity); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSuchEntry
			}
			return err
		}
		if Sensitivity(sensitivity) == SensitivitySensitive {
			return nil
		}

		// The entry's own execution — the one RecordCompleted wrote in the
		// same transaction as the entry. Ordered by attempt so a re-run's
		// output lands on the run that produced it rather than on the first.
		//
		// Its PINNED observation comes with it, because criticality is read
		// from what was true when the command ran rather than from the
		// environment's latest: marking a host critical afterwards changes
		// what is kept from then on, and cannot rewrite what a past run was
		// allowed to keep.
		var execID int64
		var criticality string
		if err := tx.QueryRowContext(ctx,
			`SELECT e.id, o.criticality
			   FROM executions e
			   JOIN environment_observations o ON o.id = e.environment_obs_id
			  WHERE e.entry_id = ?
			  ORDER BY e.attempt DESC LIMIT 1`,
			in.EntryID).Scan(&execID, &criticality); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSuchEntry
			}
			return err
		}
		// A critical environment contributes intent and metadata only
		// (design §7.4). The command is still recorded: criticality decides
		// what is kept ABOUT a command, never whether it happened.
		if Criticality(criticality) == CriticalityCritical {
			return nil
		}

		var existingMedia, existingEntry sql.NullString
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT media_type, entry_id FROM artifacts WHERE id = ?`,
			in.ArtifactID).Scan(&existingMedia, &existingEntry)
		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			if insertErr := insertArtifact(ctx, tx, AppendArtifact{
				EntryID: in.EntryID, ExecutionID: &execID,
				ID: in.ArtifactID, MediaType: in.MediaType,
				DerivedFrom: in.DerivedFrom, Truncated: in.Truncated,
				CaptureMethod: in.CaptureMethod, CaptureVersion: in.CaptureVersion,
				TerminalCols: in.TerminalCols, TerminalRows: in.TerminalRows,
			}); insertErr != nil {
				return insertErr
			}
		case lookupErr != nil:
			return lookupErr
		default:
			// A replay must find the artifact it wrote. Anything else under
			// the same id is a different object, and this store never
			// overwrites one id with another object (§7). The identity
			// checked is the artifact's OWNER (ADR-0040) — the execution
			// beside it is provenance, and a body cannot change which block
			// it is a body of whatever attempt wrote it.
			if existingMedia.String != string(in.MediaType) ||
				existingEntry.String != in.EntryID {
				return ErrIDConflict
			}
		}
		// The ceiling is read INSIDE the transaction, against what the
		// artifact already holds: a caller splitting a body into legal chunks
		// must not be able to assemble an illegal artifact out of them.
		var held int64
		if sizeErr := tx.QueryRowContext(ctx,
			`SELECT byte_len FROM artifacts WHERE id = ?`, in.ArtifactID).Scan(&held); sizeErr != nil {
			return sizeErr
		}
		if held+int64(len(in.Body)) > MaxArtifactBytes {
			return ErrArtifactTooLarge
		}
		if chunkErr := appendChunkAt(ctx, tx, in.ArtifactID, in.Seq, in.Body); chunkErr != nil {
			return chunkErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
		stored = true
		return nil
	})
	return stored, err
}

// AppendChunk appends one chunk to an artifact and maintains its byte_len —
// logical content bytes, the retention budget's unit (open question 6,
// decided: deliberately excludes FTS, B-tree overhead, WAL and free pages;
// physical disk use is the separate Budget.DiskCeiling number).
func (s *sqliteContent) AppendChunk(ctx context.Context, artifactID string, seq int, body []byte) error {
	if seq < 1 {
		return errors.New("content: append chunk: seq starts at 1")
	}
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := appendChunkAt(ctx, tx, artifactID, seq, body); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// Artifact returns one artifact with its chunk bodies, or nil when no
// artifact carries id.
func (s *sqliteContent) Artifact(ctx context.Context, id string) (*Artifact, error) {
	a, err := s.artifactByID(ctx, id)
	if err != nil || a == nil {
		return a, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM artifact_chunks WHERE artifact_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		a.Chunks = append(a.Chunks, body)
	}
	return a, rows.Err()
}

// artifactByID reads one artifact row plus its chunk count, without bodies.
func (s *sqliteContent) artifactByID(ctx context.Context, id string) (*Artifact, error) {
	var a Artifact
	var gapsJSON string
	var stream, truncated sql.NullString
	// The eviction receipt is read here, next to byte_len, because the two
	// are only meaningful together: zero bytes is what a body retention took
	// and a capture that held nothing BOTH look like, and this is the column
	// that tells them apart.
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.entry_id, a.execution_id, a.media_type,
		a.derived_from,
		a.state, a.byte_len, (SELECT count(*) FROM artifact_chunks c WHERE c.artifact_id = a.id),
		a.pinned, a.truncated, a.capture_method, a.capture_version, a.terminal_cols,
		a.terminal_rows, a.stream, a.byte_offset, a.byte_end, a.encoding, a.gaps, a.payload,
		`+bodyEvictedExpr("a")+`
		FROM artifacts a WHERE a.id = ?`, id).Scan(
		&a.ID, &a.EntryID, &a.ExecutionID, &a.MediaType, &a.DerivedFrom, &a.State, &a.ByteLen,
		&a.ChunkCount, &a.Pinned, &truncated, &a.CaptureMethod, &a.CaptureVersion,
		&a.TerminalCols, &a.TerminalRows, &stream, &a.ByteOffset, &a.ByteEnd, &a.Encoding,
		&gapsJSON, &a.Payload, &a.Evicted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if stream.Valid {
		v := Stream(stream.String)
		a.Stream = &v
	}
	if truncated.Valid {
		v := Truncation(truncated.String)
		a.Truncated = &v
	}
	if err := json.Unmarshal([]byte(gapsJSON), &a.Gaps); err != nil {
		return nil, err
	}
	return &a, nil
}

// ── edges ────────────────────────────────────────────────────────────────
func (s *sqliteContent) AddEdge(ctx context.Context, e Edge) error {
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO edges (from_id, to_id, rel, payload) VALUES (?, ?, ?, ?)`,
			e.From, e.To, string(e.Rel), e.Payload)
		return err
	})
}

// AddCause seats an existing entry as the next child of the block that
// caused it, and returns the seat it took.
//
// It was one `caused-by` edge; ADR-0040 made containment a column, so this
// is now an UPDATE of the child's own row. What did not change is why read
// and write are one transaction: the seat is derived from what is already
// stored — the highest pos any child of this parent holds — so an in-memory
// counter is not merely unnecessary but wrong. The approval resume builds a
// fresh pipeline for a turn that already has children, and a counter that
// started at zero there would stack the resumed call on top of the calls
// before it. Reading the maximum in a separate statement would have the same
// fault against a concurrent writer, one transaction narrower.
//
// Both ids are resolved HERE rather than left to the foreign key, the rule
// Submit follows for its pane: the FK would refuse a parent that is not
// there, but its constraint text does not say which reference was missing,
// and an UPDATE that matches no row does not fail at all — it silently
// changes nothing, which is the one answer worse than an error.
func (s *sqliteContent) AddCause(ctx context.Context, turnID, causedID string) (int, error) {
	pos := 0
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var parent sql.NullString
		var seat sql.NullInt64
		err = tx.QueryRowContext(ctx,
			`SELECT parent_id, pos FROM entries WHERE id = ?`, causedID).Scan(&parent, &seat)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: seat %s under %s: %w", causedID, turnID, ErrNoSuchEntry)
		}
		if err != nil {
			return err
		}
		// The replay: this child is already under this parent, so it keeps
		// the seat it took the first time and nothing is written.
		if parent.Valid && parent.String == turnID {
			if !seat.Valid {
				return fmt.Errorf("content: %s is a child of %s with no position — the tree is inconsistent", causedID, turnID)
			}
			pos = int(seat.Int64)
			return tx.Commit()
		}
		// ONE PARENT, which an edge could never say. A block already drawn
		// inside something else is refused rather than moved: re-parenting
		// is a deliberate act, not a side effect of a second caller
		// recording the same cause against a different turn.
		if parent.Valid {
			return fmt.Errorf("content: %s is already a child of %s and cannot also be seated under %s",
				causedID, parent.String, turnID)
		}

		var exists string
		err = tx.QueryRowContext(ctx, `SELECT id FROM entries WHERE id = ?`, turnID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: seat %s under %s: %w", causedID, turnID, ErrNoSuchEntry)
		}
		if err != nil {
			return err
		}

		var highest sql.NullInt64
		if err = tx.QueryRowContext(ctx,
			`SELECT MAX(pos) FROM entries WHERE parent_id = ?`, turnID).Scan(&highest); err != nil {
			return err
		}
		if highest.Valid {
			pos = int(highest.Int64) + 1
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE entries SET parent_id = ?, pos = ? WHERE id = ?`,
			turnID, pos, causedID); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return 0, err
	}
	return pos, nil
}

// Caused returns a block's CHILDREN in pos order, each resolved into what a
// reader draws it with: the child's kind and intent, and — for an action
// entry — the effect the gate decided and the resource the call named, read
// back off that row's own payload.
//
// ONE statement over the child rows: the alternative is handing the caller a
// list of ids to resolve itself, which is the same arrangement owned twice
// (AD-8). Ordering is by pos and then by id, so a page is stable even if a
// seat were ever duplicated by a hand-written row — which the table's
// UNIQUE (parent_id, pos) is there to make impossible through this seam.
func (s *sqliteContent) Caused(ctx context.Context, entryID string) ([]CausedEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT n.id, COALESCE(n.pos, 0), n.kind, n.source, n.intent, n.payload
		 FROM entries n
		 WHERE n.parent_id = ?
		 ORDER BY n.pos, n.id`,
		entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]CausedEntry, 0, 4)
	for rows.Next() {
		var c CausedEntry
		var payload string
		if err := rows.Scan(&c.EntryID, &c.Position, &c.Kind, &c.Source, &c.Intent, &payload); err != nil {
			return nil, err
		}
		// The tool facts belong to an ACTION row and are read from nowhere
		// else: a shell command a turn ran has no effect class and names no
		// resource, and inventing one for it would be the reader guessing.
		if c.Kind == EntryAction {
			var facts ActionFacts
			if err := json.Unmarshal([]byte(payload), &facts); err != nil {
				return nil, fmt.Errorf("content: action %s: payload: %w", c.EntryID, err)
			}
			c.Effect = facts.Effect
			c.Args = facts.Args
			c.Resource = facts.Resource
			c.OpensBlock = facts.OpensBlock
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Edges returns every edge touching entryID, in either direction.
func (s *sqliteContent) Edges(ctx context.Context, entryID string) ([]Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_id, to_id, rel, payload FROM edges WHERE from_id = ? OR to_id = ? ORDER BY rel`,
		entryID, entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Rel, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── read helpers ─────────────────────────────────────────────────────────

// executionsFor loads one entry's executions with their pinned observation,
// grant and artifact metadata. N+1 by design: Entry is a recall-shaped read
// of one entry, never a page scan.
func (s *sqliteContent) executionsFor(ctx context.Context, entryID string) ([]Execution, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, entry_id, lane, attempt, environment_obs_id,
		lease_deadline, inactivity_deadline, interactivity, process_group, started_at, ended_at,
		termination_reason, executor, state, payload
		FROM executions WHERE entry_id = ? ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Execution
	for rows.Next() {
		var ex Execution
		var obsID int64
		var termination, state sql.NullString
		if err := rows.Scan(&ex.ID, &ex.EntryID, &ex.Lane, &ex.Attempt, &obsID,
			&ex.LeaseDeadline, &ex.InactivityDeadline, &ex.Interactivity, &ex.ProcessGroup,
			&ex.StartedAt, &ex.EndedAt, &termination, &ex.Executor, &state, &ex.Payload); err != nil {
			return nil, err
		}
		if termination.Valid {
			v := TerminationReason(termination.String)
			ex.TerminationReason = &v
		}
		if state.Valid {
			v := RunState(state.String)
			ex.State = &v
		}
		obs, err := s.observationByID(ctx, obsID)
		if err != nil {
			return nil, err
		}
		ex.Observation = *obs
		grant, err := s.grantFor(ctx, ex.ID)
		if err != nil {
			return nil, err
		}
		ex.Grant = grant
		arts, err := s.artifactsFor(ctx, ex.ID)
		if err != nil {
			return nil, err
		}
		ex.Artifacts = arts
		out = append(out, ex)
	}
	return out, rows.Err()
}

func (s *sqliteContent) observationByID(ctx context.Context, id int64) (*Observation, error) {
	var o Observation
	err := s.db.QueryRowContext(ctx, `SELECT id, environment_id, version, observed_at,
		confidence, criticality, payload FROM environment_observations WHERE id = ?`, id).Scan(
		&o.ID, &o.EnvironmentID, &o.Version, &o.ObservedAt, &o.Confidence, &o.Criticality,
		&o.Payload)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// grantFor returns the grant recorded on one execution, or nil when the run
// carried none.
func (s *sqliteContent) grantFor(ctx context.Context, executionID int64) (*Grant, error) {
	var g Grant
	var grantID int64
	var policyJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id, version, expires_at, policy
		FROM authority_grants WHERE execution_id = ?`, executionID).Scan(
		&grantID, &g.Version, &g.ExpiresAt, &policyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A stored policy that no longer parses — an older build's shape, a
	// hand-edited row — degrades to the zero matrix, which decides ask:
	// the recorded authority is never re-read as "permitted" because it
	// cannot be read at all.
	if parsed, perr := ParseEffectPolicy([]byte(policyJSON)); perr != nil {
		g.Policy = EffectPolicy{}
	} else {
		g.Policy = parsed
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT resource_kind, resource_id FROM grant_scopes WHERE grant_id = ? ORDER BY resource_kind, resource_id`,
		grantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sc GrantScope
		if scanErr := rows.Scan(&sc.Kind, &sc.ID); scanErr != nil {
			return nil, scanErr
		}
		g.Scopes = append(g.Scopes, sc)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	erows, err := s.db.QueryContext(ctx,
		`SELECT effect FROM grant_effects WHERE grant_id = ? ORDER BY effect`,
		grantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = erows.Close() }()
	for erows.Next() {
		var e Effect
		if escanErr := erows.Scan(&e); escanErr != nil {
			return nil, escanErr
		}
		g.Effects = append(g.Effects, e)
	}
	return &g, erows.Err()
}

// artifactsFor loads the artifacts one execution PRODUCED, metadata only (no
// bodies). It reads the provenance column, deliberately: this hangs the
// bodies off the attempt that wrote them inside LedgerEntry, which is what
// makes a re-run's output distinguishable from the first run's. The block's
// own bodies — including a `text` block's, which no attempt produced — are
// reached by entry_id.
func (s *sqliteContent) artifactsFor(ctx context.Context, executionID int64) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artifacts
		WHERE execution_id = ? ORDER BY id`, executionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Artifact
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		a, err := s.artifactByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}
