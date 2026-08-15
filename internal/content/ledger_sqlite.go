package content

// The SQLite implementation of LedgerRepository — schema v1 of the one
// authoritative ledger (nocx-rtg0.2), per ADR-0019, ADR-0020 and design
// §5.2. Every mutation goes through the single writer goroutine (run in
// sqlite.go — design §5.3); every read goes through the pool directly.
//
// The v1 write path has NO PRODUCTION CALLER until nocx-rtg0.3 cuts the wire
// over to ledger.* — only tests exercise these methods today. Stated loudly
// because the same shape shipped once before (a reachable read path hid an
// unreachable write path in the same package).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var _ LedgerRepository = (*sqliteContent)(nil)

// ── identity and narrative scope ─────────────────────────────────────────

func (s *sqliteContent) CreateWorkspace(ctx context.Context, ws Workspace) error {
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`,
			ws.ID, ws.Name, time.Now().UnixMilli())
		return err
	})
}

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
		submittedAt = time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, session_id, cwd, kind, intent,
			 phase, status, conversation_id, submitted_at, started_at, ended_at, duration_ms,
			 sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', 'pending', ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, next, in.Client, digest, in.EnvironmentID, in.SessionID, in.Cwd,
			string(in.Kind), in.Intent, in.ConversationID, submittedAt, in.StartedAt,
			in.EndedAt, in.DurationMs, string(in.Sensitivity), in.Payload,
		); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		out = SubmitResult{ID: in.ID, IngestSeq: next, SubmittedAt: submittedAt}
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
		Kind, Sensitivity                           string
		SessionID, ConversationID                   *string
	}{
		Client: in.Client, EnvironmentID: in.EnvironmentID, Cwd: in.Cwd, Intent: in.Intent,
		Payload: in.Payload, Kind: string(in.Kind), Sensitivity: string(in.Sensitivity),
		SessionID: in.SessionID, ConversationID: in.ConversationID,
	})
	return hex.EncodeToString(h.Sum(nil))
}

// Entry is the recall read: the entry, its executions, each execution's
// pinned observation and grant, and its artifacts (metadata only — the
// recall read never hauls chunk bodies; Artifact fetches those).
func (s *sqliteContent) Entry(ctx context.Context, id string) (*LedgerEntry, error) {
	e := &LedgerEntry{}
	err := s.db.QueryRowContext(ctx, `SELECT id, ingest_seq, client, digest, environment_id,
		session_id, cwd, kind, intent, phase, status, conversation_id, submitted_at,
		started_at, ended_at, duration_ms, sensitivity, reviewed_at, payload
		FROM entries WHERE id = ?`, id).Scan(
		&e.ID, &e.IngestSeq, &e.Client, &e.Digest, &e.EnvironmentID, &e.SessionID, &e.Cwd,
		&e.Kind, &e.Intent, &e.Phase, &e.Status, &e.ConversationID, &e.SubmittedAt,
		&e.StartedAt, &e.EndedAt, &e.DurationMs, &e.Sensitivity, &e.ReviewedAt, &e.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	execs, err := s.executionsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Executions = execs
	return e, nil
}

// ListEntries returns the limit newest entries, newest first, ordered by
// ingest_seq — commit order, never by wall clock (two entries in the same
// millisecond still have an order).
func (s *sqliteContent) ListEntries(ctx context.Context, limit int) ([]LedgerEntrySummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ingest_seq, environment_id, cwd, kind,
		intent, phase, status, submitted_at
		FROM entries ORDER BY ingest_seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LedgerEntrySummary
	for rows.Next() {
		var e LedgerEntrySummary
		if err := rows.Scan(&e.ID, &e.IngestSeq, &e.EnvironmentID, &e.Cwd, &e.Kind,
			&e.Intent, &e.Phase, &e.Status, &e.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEntry removes an entry; its edges, executions, artifacts, chunks and
// grant cascade (open question 5, decided: an edge whose endpoint is gone is
// meaningless). Idempotent for a missing id. A pin protects against
// background eviction, never against this explicit deletion.
func (s *sqliteContent) DeleteEntry(ctx context.Context, id string) error {
	return s.run(ctx, func(ctx context.Context) error {
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
			g, err := tx.ExecContext(ctx, `INSERT INTO authority_grants
				(execution_id, version, issued_at, expires_at, policy) VALUES (?, ?, ?, ?, ?)`,
				id, in.Grant.Version, time.Now().UnixMilli(), in.Grant.ExpiresAt,
				string(in.Grant.Policy))
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
// closes the entry with its final status. For an agent run (state IS NOT
// NULL) it also maps the termination to the terminal run state the renderer
// draws — completed | cancelled | failed | interrupted — in the SAME
// update, so the run is never reported terminal in one vocabulary and not
// the other. Executions without a state (a frame capture) are untouched.
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
			`UPDATE entries SET phase = 'closed', status = ? WHERE id = ?`,
			string(end.Status), entryID); err != nil {
			return err
		}
		// An ASK run (an entry with a caused-by answer) closes its answer
		// entry, seals its artifact and ends its container execution in the
		// same transaction — any terminalizer keeps the ledger consistent,
		// not just FinishAgentRun. A non-ask run has no answer entry and
		// this is a no-op.
		if err := closeAnswerFor(ctx, tx, entryID, end.Status, end.EndedAt, end.TerminationReason); err != nil {
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

// AppendArtifact creates one artifact of an execution with its capture
// provenance (ADR-0019 §6: a capture records how it was taken — method and
// version, terminal dimensions, stream position, encoding, gaps). Content
// arrives via AppendChunk: an artifact is never one BLOB.
func (s *sqliteContent) AppendArtifact(ctx context.Context, in AppendArtifact) (string, error) {
	if in.ID == "" {
		return "", errors.New("content: append artifact: id is required")
	}
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
			return "", err
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
	err := s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts
			(id, execution_id, media_type, derived_from, pinned, truncated, capture_method,
			 capture_version, terminal_cols, terminal_rows, stream, byte_offset, byte_end,
			 encoding, gaps, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, in.ExecutionID, string(in.MediaType), in.DerivedFrom, in.Pinned, truncated,
			string(in.CaptureMethod), in.CaptureVersion, in.TerminalCols, in.TerminalRows,
			stream, in.ByteOffset, in.ByteEnd, in.Encoding, gaps, in.Payload)
		return err
	})
	return in.ID, err
}

// AppendChunk appends one chunk to an artifact and maintains its byte_len —
// logical content bytes, the retention budget's unit (open question 6,
// decided: deliberately excludes FTS, B-tree overhead, WAL and free pages;
// physical disk use is the separate Budget.DiskCeiling number).
func (s *sqliteContent) AppendChunk(ctx context.Context, artifactID string, body []byte) error {
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		var next int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM artifact_chunks WHERE artifact_id = ?`,
			artifactID).Scan(&next); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO artifact_chunks (artifact_id, seq, body) VALUES (?, ?, ?)`,
			artifactID, next, body); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET byte_len = byte_len + ? WHERE id = ?`, len(body), artifactID); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.execution_id, a.media_type, a.derived_from,
		a.state, a.byte_len, (SELECT count(*) FROM artifact_chunks c WHERE c.artifact_id = a.id),
		a.pinned, a.truncated, a.capture_method, a.capture_version, a.terminal_cols,
		a.terminal_rows, a.stream, a.byte_offset, a.byte_end, a.encoding, a.gaps, a.payload
		FROM artifacts a WHERE a.id = ?`, id).Scan(
		&a.ID, &a.ExecutionID, &a.MediaType, &a.DerivedFrom, &a.State, &a.ByteLen,
		&a.ChunkCount, &a.Pinned, &truncated, &a.CaptureMethod, &a.CaptureVersion,
		&a.TerminalCols, &a.TerminalRows, &stream, &a.ByteOffset, &a.ByteEnd, &a.Encoding,
		&gapsJSON, &a.Payload)
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
	err := s.db.QueryRowContext(ctx, `SELECT id, version, expires_at, policy
		FROM authority_grants WHERE execution_id = ?`, executionID).Scan(
		&grantID, &g.Version, &g.ExpiresAt, &g.Policy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
		if err := rows.Scan(&sc.Kind, &sc.ID); err != nil {
			return nil, err
		}
		g.Scopes = append(g.Scopes, sc)
	}
	return &g, rows.Err()
}

// artifactsFor loads one execution's artifacts, metadata only (no bodies).
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
