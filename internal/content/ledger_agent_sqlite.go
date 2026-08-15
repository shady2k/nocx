package content

// The ask transaction (nocx-f4s5): agent.captureFrame ingests a frame
// first and mints the backend frame id; agent.ask records the frame
// reference, the question and a PENDING run in ONE atomic create. Design
// §5, §7; ADR-0019 (one ledger), AD-7 (ids are backend-minted).
//
// The ordering rule that makes recovery honest: an attempt is written down
// BEFORE the tool is invoked, never after. Here that rule is the whole
// transaction — the run row exists in state prepared from before
// agent.ask returns until the run terminalizes (FinishExecution or the
// startup sweep), and the two writes (question + run) are one commit, so
// no reader can ever observe a question without its run or a run without
// its question.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// artifactChunkSize bounds one artifact_chunks row: the frame body arrives
// in bounded, ordered, idempotent chunks — an artifact is never one BLOB.
const artifactChunkSize = 64 << 10

// mintID is the backend's id mint (AD-7 — ids are server-authoritative):
// 32 hex chars from crypto/rand, the same shape as session.NewID. The
// frame id and its artifact id are minted here, never supplied by the
// renderer.
func mintID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("content: mint id: " + err.Error()) // rand.Read never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

// frameDigest is the content binding of the capture idempotency key. The
// store derives it from the frame content (the client never sends it — that
// would be forgeable), so a replay of the same capture id cannot alias a
// different frame. Struct field order is fixed, so the hash is
// deterministic across calls and restarts.
func frameDigest(in CaptureFrame) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(struct {
		Client, Cwd, Source string
		EnvironmentID       string
		SessionID           *string
		Rows                []FrameRow
		Cursor              *FrameCursor
		Identity            *FrameIdentity
		Range               *FrameRange
		SerializerVersion   *int
	}{
		Client: in.Client, Cwd: in.Cwd, Source: string(in.Source),
		EnvironmentID: in.Env.ID, SessionID: in.SessionID,
		Rows: in.Rows, Cursor: in.Cursor, Identity: in.Identity,
		Range: in.Range, SerializerVersion: in.SerializerVersion,
	})
	return hex.EncodeToString(h.Sum(nil))
}

// askDigest is the content binding of the ask idempotency key: question
// text + the references in order. Same rule as frameDigest — derived by
// the store, never sent by the client.
func askDigest(in AgentAsk) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(struct {
		Client, Cwd, Question string
		EnvironmentID         string
		SessionID             *string
		References            []AgentReference
	}{
		Client: in.Client, Cwd: in.Cwd, Question: in.Question,
		EnvironmentID: in.Env.ID, SessionID: in.SessionID,
		References: in.References,
	})
	return hex.EncodeToString(h.Sum(nil))
}

// DefaultWorkspaceID is the workspace every SESSION NOT ALREADY RECORDED is
// recorded under until the ledger cutover (nocx-rtg0.3) gives sessions
// their real restore-key lifecycle. This is a stated dependency, not a
// hidden one: the product has no workspace concept yet (no registry, no
// grant minting — ADR-0020 §5), the ledger schema demands a workspace
// owner, and the ask transaction must be able to record a frame or a
// question for ANY live tab. A session that is ALREADY in the ledger is
// NEVER re-parented or marked — the ensure is ON CONFLICT DO NOTHING, so a
// session the cutover (or anything else) recorded under its real workspace
// keeps it, marker-free.
//
// Synthetic children carry captureEnsuredSessionMarker in sessions.payload,
// so the cutover can tell them from genuinely assigned rows. The cutover's
// migration order is load-bearing: UPDATE the marked sessions to their real
// workspace FIRST, and only then DROP the default row — sessions.workspace_id
// is ON DELETE CASCADE, so dropping first would delete every synthetic
// session, which is the exact failure the marker exists to prevent. Re-
// parenting is a plain FK update; session identities are the PK and never
// move.
const DefaultWorkspaceID = "workspace:default"

// captureEnsuredSessionMarker is written into the payload of a session row
// THIS ensure created (the fallback workspace's child). A session recorded
// by anyone else — the cutover, a real workspace lifecycle — never carries
// it, and the ensure never stamps it onto an existing row.
const captureEnsuredSessionMarker = `{"ensure":"agent-capture"}`

// ensureLedgerContext makes the environment, its observation, the default
// workspace and the session row exist, and returns the observation the
// transaction's execution pins. Inlined here, not called through the
// repository methods, because this runs inside the writer goroutine's
// transaction and a nested s.run would deadlock the writer against itself.
// Only the FIRST observation is ever recorded: the environment facets
// derived from a session (kind, endpoint) are static, so one empty snapshot
// is the honest "nothing observed changed"; a later observation would be a
// lie about having re-observed. The workspace and session rows are ensured
// idempotently for the same reason: recording a capture must never fail
// because the tab's restore key was not (yet) written by the cutover, and
// an existing session row is never touched — its workspace is preserved.
func ensureLedgerContext(ctx context.Context, tx *sql.Tx, env Environment, sessionID *string) (int64, error) {
	if env.ID == "" {
		return 0, errors.New("content: environment id is required")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO environments (id, kind, endpoint, profile_id, first_seen, payload)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		env.ID, string(env.Kind), env.Endpoint, env.ProfileID, time.Now().UnixMilli(), env.Payload); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, created_at) VALUES (?, 'default', ?)
		 ON CONFLICT(id) DO NOTHING`,
		DefaultWorkspaceID, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	if sessionID != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (id, workspace_id, started_at, payload) VALUES (?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			*sessionID, DefaultWorkspaceID, time.Now().UnixMilli(),
			captureEnsuredSessionMarker); err != nil {
			return 0, err
		}
	}
	var obsID int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM environment_observations WHERE environment_id = ? ORDER BY version DESC LIMIT 1`,
		env.ID).Scan(&obsID)
	switch {
	case err == nil:
		return obsID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO environment_observations (environment_id, version, observed_at, confidence, criticality, payload)
		 VALUES (?, 1, ?, '{}', 'routine', '{}') RETURNING id`,
		env.ID, time.Now().UnixMilli()).Scan(&obsID); err != nil {
		return 0, err
	}
	return obsID, nil
}

// ── agent.captureFrame ────────────────────────────────────────────────────

// CaptureFrame ingests one renderer-minted frame and returns the
// backend-minted frame id. One transaction: replay check, environment
// ensure, the frame entry (kind=agent, intent=frame-capture — a fact,
// closed at capture time), its execution (NOT a run: state NULL, complete
// at ingest) and the frame artifact: cells-derived text with capture
// provenance, chunked and sealed. Nothing is observable in a partial
// state — a crash rolls the whole capture back.
func (s *sqliteContent) CaptureFrame(ctx context.Context, in CaptureFrame) (CaptureFrameResult, error) {
	if in.CaptureID == "" {
		return CaptureFrameResult{}, errors.New("content: capture frame: capture id is required")
	}
	if in.Client == "" {
		return CaptureFrameResult{}, errors.New("content: capture frame: client is required — it binds the idempotency key")
	}
	switch in.Source {
	case FrameLive:
		if in.Identity == nil {
			return CaptureFrameResult{}, errors.New("content: capture frame: live frame requires the capture identity")
		}
		if in.Range == nil {
			return CaptureFrameResult{}, errors.New("content: capture frame: live frame requires the buffer row range")
		}
	case FrameFrozen:
		if in.SerializerVersion == nil {
			return CaptureFrameResult{}, errors.New("content: capture frame: frozen frame requires the serializer version")
		}
	default:
		return CaptureFrameResult{}, fmt.Errorf("content: capture frame: unknown source %q", in.Source)
	}
	if in.Cursor == nil && in.Source == FrameLive {
		return CaptureFrameResult{}, errors.New("content: capture frame: live frame requires a cursor")
	}
	if in.Cursor != nil && in.Source == FrameFrozen {
		return CaptureFrameResult{}, errors.New("content: capture frame: a frozen frame has no cursor")
	}
	digest := frameDigest(in)
	var out CaptureFrameResult
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		// Replay: the same (capture id, client, content) returns the
		// original backend-minted id and creates nothing new — otherwise a
		// lost response orphans a duplicate frame on every retry.
		var existing string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM entries WHERE capture_key = ? AND client = ? AND digest = ?`,
			in.CaptureID, in.Client, digest).Scan(&existing)
		switch {
		case err == nil:
			out = CaptureFrameResult{FrameID: existing, Replayed: true}
			return tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		// The key is already bound to DIFFERENT content: a replay that
		// would alias two captures is refused (the UNIQUE index below would
		// surface it as a raw constraint error; the semantic error is
		// ErrIDConflict, exactly like Submit).
		var bound string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM entries WHERE capture_key = ?`, in.CaptureID).Scan(&bound)
		switch {
		case err == nil:
			return ErrIDConflict
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		obsID, err := ensureLedgerContext(ctx, tx, in.Env, in.SessionID)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		frameID := mintID()
		artifactID := mintID()

		var seq int64
		if seqErr := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`).Scan(&seq); seqErr != nil {
			return fmt.Errorf("content: capture frame: assign ingest_seq: %w", seqErr)
		}
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, session_id, cwd, kind, intent,
			 phase, status, submitted_at, capture_key, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'agent', ?, 'closed', 'success', ?, ?, '{}')`,
			frameID, seq, in.Client, digest, in.Env.ID, in.SessionID, in.Cwd,
			FrameIntent, now, in.CaptureID); insertErr != nil {
			return insertErr
		}

		// The frame's execution is a capture record, not a run: state NULL
		// (the startup sweep only touches runs), complete at ingest.
		res, err := tx.ExecContext(ctx, `INSERT INTO executions
			(entry_id, attempt, environment_obs_id, interactivity, started_at, ended_at, termination_reason)
			VALUES (?, 1, ?, 'none', ?, ?, 'completed')`,
			frameID, obsID, now, now)
		if err != nil {
			return err
		}
		execID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		text := []byte(frameText(in))
		cols, rows := frameGeometry(in)
		payload, err := json.Marshal(frameProvenance(in))
		if err != nil {
			return err
		}
		mediaType, method, version := frameArtifactIdentity(in)
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
			(id, execution_id, media_type, state, byte_len, capture_method, capture_version,
			 terminal_cols, terminal_rows, encoding, gaps, payload)
			VALUES (?, ?, ?, 'sealed', ?, ?, ?, ?, ?, 'utf-8', '[]', ?)`,
			artifactID, execID, string(mediaType), len(text), string(method), version,
			cols, rows, string(payload)); err != nil {
			return err
		}
		for i, chunk := range chunkBytes(text) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO artifact_chunks (artifact_id, seq, body) VALUES (?, ?, ?)`,
				artifactID, i, chunk); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		out = CaptureFrameResult{FrameID: frameID}
		return nil
	})
	return out, err
}

// frameText derives the frame's durable text from its rows: live cells'
// chars joined per row, frozen text rows as-is, rows joined by '\n'. The
// cells' ATTRIBUTES are not retained — the durable record is text with
// provenance (ADR-0019 §6); the renderer keeps the full cells for drawing,
// and the model half reads text, masked by the one masking owner
// (nocx-a21v).
func frameText(in CaptureFrame) string {
	lines := make([]string, 0, len(in.Rows))
	for _, r := range in.Rows {
		if r.Kind == "text" {
			lines = append(lines, r.Text)
			continue
		}
		var b strings.Builder
		for _, c := range r.Cells {
			b.WriteString(c.Char)
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

func frameGeometry(in CaptureFrame) (cols, rows *int) {
	if in.Source == FrameLive && in.Identity != nil {
		return &in.Identity.Cols, &in.Identity.Rows
	}
	return nil, nil
}

// frameProvenance is the artifact payload: everything the frame IS beyond
// its text — the capture identity (ADR-0029's comparison vocabulary), the
// live row range, the cursor, the source-specific facts, and the row/col
// counts the ask's region validation is checked against. The wire frame id
// → region check reads this.
func frameProvenance(in CaptureFrame) map[string]any {
	prov := map[string]any{
		"rowCount": len(in.Rows),
		"cwd":      in.Cwd,
	}
	if in.Source == FrameLive {
		cols, rows := frameGeometry(in)
		prov["cols"] = cols
		prov["rows"] = rows
		prov["identity"] = in.Identity
		prov["range"] = in.Range
		prov["scrollbackCapLines"] = 10000
		prov["cursor"] = in.Cursor
	} else {
		prov["serializerVersion"] = in.SerializerVersion
		prov["transforms"] = "wrapped lines joined; leading/trailing blanks dropped"
		prov["closed"] = true
	}
	return prov
}

// frameArtifactIdentity maps the capture source to the artifact's
// provenance columns: a live frame is terminal cells (version 1 — the
// renderer's mint), a frozen frame is serialized block HTML at the
// SERIALIZER_VERSION that produced it. The two are never substituted.
func frameArtifactIdentity(in CaptureFrame) (MediaType, CaptureMethod, int) {
	if in.Source == FrameLive {
		return MediaVT, CaptureTerminalCells, 1
	}
	return MediaText, CaptureSerializedHTML, *in.SerializerVersion
}

func chunkBytes(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	var out [][]byte
	for len(b) > artifactChunkSize {
		out = append(out, b[:artifactChunkSize])
		b = b[artifactChunkSize:]
	}
	return append(out, b)
}

// ── agent.ask ─────────────────────────────────────────────────────────────

// SubmitAgentAsk records ONE ask transaction atomically: the question entry
// (kind=agent, open/pending), its pending run (the backend-minted execution
// row, state prepared — the model is never called here) and the references
// edges, each carrying its region. Every reference is validated INSIDE the
// transaction: unknown id, non-frame id, a frame from another session or an
// out-of-bounds region refuses the WHOLE transaction — the interval "the
// run record exists from before the ask returns until the run terminalizes"
// has no one-sided states.
func (s *sqliteContent) SubmitAgentAsk(ctx context.Context, in AgentAsk) (AgentAskResult, error) {
	if in.ID == "" {
		return AgentAskResult{}, errors.New("content: submit agent ask: ask id is required")
	}
	if in.Client == "" {
		return AgentAskResult{}, errors.New("content: submit agent ask: client is required — it binds the idempotency key")
	}
	if strings.TrimSpace(in.Question) == "" {
		return AgentAskResult{}, errors.New("content: submit agent ask: question is required")
	}
	digest := askDigest(in)
	var out AgentAskResult
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		// Replay: the same (ask id, client, content) returns the ORIGINAL
		// run id — the bead's "a retry duplicates both" is the defect this
		// exists to prevent. Same id, different content: ErrIDConflict.
		var haveSeq int64
		var haveClient, haveDigest string
		err = tx.QueryRowContext(ctx,
			`SELECT ingest_seq, client, digest FROM entries WHERE id = ?`, in.ID,
		).Scan(&haveSeq, &haveClient, &haveDigest)
		switch {
		case err == nil:
			if haveClient != in.Client || haveDigest != digest {
				return ErrIDConflict
			}
			var runID int64
			if runErr := tx.QueryRowContext(ctx,
				`SELECT id FROM executions WHERE entry_id = ? AND lane = 'agent' ORDER BY attempt LIMIT 1`,
				in.ID).Scan(&runID); runErr != nil {
				if errors.Is(runErr, sql.ErrNoRows) {
					return fmt.Errorf("content: submit agent ask: replayed ask %q has no run", in.ID)
				}
				return runErr
			}
			// A replay returns the ORIGINAL answer identity too — the
			// renderer's lost response must not orphan the answer block.
			var answerID, artifactID string
			if answerErr := tx.QueryRowContext(ctx,
				`SELECT from_id FROM edges WHERE to_id = ? AND rel = 'caused-by'`,
				in.ID).Scan(&answerID); answerErr != nil {
				return fmt.Errorf("content: submit agent ask: replayed ask %q has no answer entry", in.ID)
			}
			if artifactErr := tx.QueryRowContext(ctx,
				`SELECT a.id FROM artifacts a JOIN executions e ON a.execution_id = e.id
				  WHERE e.entry_id = ? ORDER BY a.id LIMIT 1`, answerID).Scan(&artifactID); artifactErr != nil {
				return fmt.Errorf("content: submit agent ask: replayed ask %q has no answer artifact", in.ID)
			}
			out = AgentAskResult{
				RunID: runID, QuestionID: in.ID, AnswerEntryID: answerID,
				AnswerArtifactID: artifactID, IngestSeq: haveSeq, Replayed: true,
			}
			return tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		obsID, err := ensureLedgerContext(ctx, tx, in.Env, in.SessionID)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()

		var seq int64
		if seqErr := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`).Scan(&seq); seqErr != nil {
			return fmt.Errorf("content: submit agent ask: assign ingest_seq: %w", seqErr)
		}
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, session_id, cwd, kind, intent,
			 phase, status, submitted_at, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'agent', ?, 'open', 'pending', ?, 'normal', '{}')`,
			in.ID, seq, in.Client, digest, in.Env.ID, in.SessionID, in.Cwd,
			in.Question, now); insertErr != nil {
			return insertErr
		}

		// The pending run: the model has not been called, and this row is
		// the durable proof. From this commit until the run terminalizes
		// (FinishAgentRun, or the startup sweep → interrupted), the run
		// record EXISTS — the first end of the interval is this INSERT in
		// this transaction, the closing end is the terminal state. The
		// payload pins the endpoint+model the run will use, as they were at
		// the time (design §5).
		factsJSON, err := json.Marshal(in.Facts)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO executions
			(entry_id, lane, attempt, environment_obs_id, interactivity, started_at, state, payload)
			VALUES (?, 'agent', 1, ?, 'none', ?, 'prepared', ?)`,
			in.ID, obsID, now, string(factsJSON))
		if err != nil {
			return err
		}
		runID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		for _, ref := range in.References {
			if refErr := validateFrameReference(ctx, tx, in, ref); refErr != nil {
				return refErr
			}
			regionJSON, regionErr := json.Marshal(ref.Region)
			if regionErr != nil {
				return regionErr
			}
			if _, edgeErr := tx.ExecContext(ctx,
				`INSERT INTO edges (from_id, to_id, rel, payload) VALUES (?, ?, 'references', ?)`,
				in.ID, ref.FrameID, string(regionJSON)); edgeErr != nil {
				return edgeErr
			}
		}

		// The answer entry: the model's streamed reply lands here — an
		// entry in the flow, joined to the question by a caused-by edge
		// (design §5: the answer is an entry, never a string held in a map
		// that dies with the process). Its identity exists from before the
		// first delta until the run terminalizes — the same span as the
		// run: question, run, references, answer entry and artifact commit
		// together or none does.
		answerID := mintID()
		artifactID := mintID()
		// The answer gets its own ingest_seq — commit order is the
		// counter's order (ADR-0019 §2), and a second entry means a second
		// counter increment, never seq+1 (which the next ask would collide
		// with).
		var answerSeq int64
		if answerSeqErr := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`).Scan(&answerSeq); answerSeqErr != nil {
			return fmt.Errorf("content: submit agent ask: assign answer ingest_seq: %w", answerSeqErr)
		}
		// The entry first — its execution and artifact reference it (FK).
		if _, answerInsertErr := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, session_id, cwd, kind, intent,
			 phase, status, conversation_id, submitted_at, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'agent', ?, 'open', 'pending', ?, ?, 'normal', '{}')`,
			answerID, answerSeq, in.Client, digest, in.Env.ID, in.SessionID, in.Cwd,
			AnswerIntent, in.ID, now); answerInsertErr != nil {
			return answerInsertErr
		}
		answerExec, err := tx.ExecContext(ctx, `INSERT INTO executions
			(entry_id, attempt, environment_obs_id, interactivity, started_at)
			VALUES (?, 1, ?, 'none', ?)`,
			answerID, obsID, now)
		if err != nil {
			return err
		}
		answerExecID, err := answerExec.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
			(id, execution_id, media_type, state, byte_len, capture_method, capture_version,
			 encoding, gaps, payload)
			VALUES (?, ?, 'text/plain', 'open', 0, 'none', 1, 'utf-8', '[]', '{}')`,
			artifactID, answerExecID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (from_id, to_id, rel, payload) VALUES (?, ?, 'caused-by', '{}')`,
			answerID, in.ID); err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		out = AgentAskResult{
			RunID: runID, QuestionID: in.ID, AnswerEntryID: answerID,
			AnswerArtifactID: artifactID, IngestSeq: seq,
		}
		return nil
	})
	return out, err
}

// TransitionRun moves the assistant run to a NON-TERMINAL state. This slice
// knows exactly one such move — prepared → streaming — and it is the gate
// deltas may not pass before: a delta persisted before the streaming
// transition commits would be a delta outside the run's non-terminal span.
func (s *sqliteContent) TransitionRun(ctx context.Context, runID int64, to RunState) error {
	if to.IsTerminal() {
		return fmt.Errorf("content: transition run: %s is terminal — use FinishAgentRun", to)
	}
	return s.run(ctx, func(ctx context.Context) error {
		var current sql.NullString
		err := s.db.QueryRowContext(ctx,
			`SELECT state FROM executions WHERE id = ?`, runID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchRun
		}
		if err != nil {
			return err
		}
		if !current.Valid {
			return fmt.Errorf("content: transition run: execution %d is not an agent run", runID)
		}
		cur := RunState(current.String)
		if cur.IsTerminal() {
			return fmt.Errorf("content: transition run: run %d is already terminal (%s)", runID, cur)
		}
		if cur == to {
			return nil // idempotent: the driver may retry after a lost commit
		}
		if !(cur == RunPrepared && to == RunStreaming) {
			return fmt.Errorf("content: transition run: illegal move %s → %s", cur, to)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE executions SET state = ? WHERE id = ?`, string(to), runID); err != nil {
			return err
		}
		return nil
	})
}

// FinishAgentRun closes the run and its entries in ONE transaction: the
// run's terminal state, end and termination reason; the question entry; the
// answer entry (found via its caused-by edge); and the answer artifact
// (sealed) with the answer execution's end. A run is never reported
// terminal in the run vocabulary while its entries still say otherwise —
// both lifecycles close together, or neither does.
func (s *sqliteContent) FinishAgentRun(ctx context.Context, runID int64, in FinishAgentRun) error {
	if !in.State.IsTerminal() {
		return fmt.Errorf("content: finish agent run: %s is not terminal", in.State)
	}
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var entryID, payload string
		err = tx.QueryRowContext(ctx,
			`SELECT entry_id, payload FROM executions WHERE id = ?`, runID).Scan(&entryID, &payload)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchRun
		}
		if err != nil {
			return err
		}
		if in.Error != "" {
			var m map[string]any
			if json.Unmarshal([]byte(payload), &m) != nil || m == nil {
				m = map[string]any{}
			}
			m["error"] = in.Error
			b, err := json.Marshal(m)
			if err != nil {
				return err
			}
			payload = string(b)
		}
		status := EntryFailure
		if in.State == RunCompleted {
			status = EntrySuccess
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE executions SET state = ?, ended_at = ?, termination_reason = ?, payload = ? WHERE id = ?`,
			string(in.State), in.EndedAt, string(in.TerminationReason), payload, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET phase = 'closed', status = ? WHERE id = ?`, string(status), entryID); err != nil {
			return err
		}
		// The answer entry and its container close with the run.
		if err := closeAnswerFor(ctx, tx, entryID, status, in.EndedAt, in.TerminationReason); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// closeAnswerFor closes the answer entry joined to questionEntryID by its
// caused-by edge, seals its artifact and ends its container execution — the
// terminalizers' shared step, so EVERY path that closes an ask run closes
// the answer too (a run is never terminal while its answer entry still says
// pending). No answer entry (a non-ask run) is a no-op.
func closeAnswerFor(ctx context.Context, tx *sql.Tx, questionEntryID string, status EntryStatus, endedAt int64, reason TerminationReason) error {
	var answerID string
	err := tx.QueryRowContext(ctx,
		`SELECT from_id FROM edges WHERE to_id = ? AND rel = 'caused-by'`, questionEntryID).Scan(&answerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, closeErr := tx.ExecContext(ctx,
		`UPDATE entries SET phase = 'closed', status = ? WHERE id = ?`, string(status), answerID); closeErr != nil {
		return closeErr
	}
	if _, sealErr := tx.ExecContext(ctx,
		`UPDATE artifacts SET state = 'sealed'
		  WHERE execution_id IN (SELECT id FROM executions WHERE entry_id = ?)`, answerID); sealErr != nil {
		return sealErr
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE executions SET ended_at = ?, termination_reason = ?
		  WHERE entry_id = ? AND state IS NULL`, endedAt, string(reason), answerID)
	return err
}

// validateFrameReference checks one reference against the STORED frame:
// the id names a frame (kind=agent, intent=frame-capture), it belongs to
// the asking session — "an ask naming a frame from another session is
// rejected" (design §5) — and the region lies inside the frame's own rows
// and columns. Any failure refuses the whole ask transaction.
func validateFrameReference(ctx context.Context, tx *sql.Tx, in AgentAsk, ref AgentReference) error {
	var kind, intent string
	var sessionID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT kind, intent, session_id FROM entries WHERE id = ?`, ref.FrameID).
		Scan(&kind, &intent, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFrameNotFound
	}
	if err != nil {
		return err
	}
	if kind != string(EntryAgent) || intent != FrameIntent {
		return ErrNotAFrame
	}
	if !sessionID.Valid || in.SessionID == nil || sessionID.String != *in.SessionID {
		return ErrFrameSessionMismatch
	}

	var payload string
	err = tx.QueryRowContext(ctx,
		`SELECT a.payload FROM artifacts a
		   JOIN executions e ON a.execution_id = e.id
		  WHERE e.entry_id = ? ORDER BY a.id LIMIT 1`, ref.FrameID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFrameNotFound
	}
	if err != nil {
		return err
	}
	if err := validateRegion([]byte(payload), ref.Region); err != nil {
		return err
	}
	return nil
}

// validateRegion bounds a region against the frame's stored provenance:
// rows within [0, rowCount), columns within [0, cols) when the frame has a
// geometry, and no column span on a frozen frame (a row range is
// full-width there). Out-of-bounds is refused, never truncated or
// silently re-scoped.
func validateRegion(provenance []byte, r FrameRegion) error {
	var p struct {
		RowCount int  `json:"rowCount"`
		Cols     *int `json:"cols"`
	}
	if err := json.Unmarshal(provenance, &p); err != nil {
		return err
	}
	if r.RowStart < 0 || r.RowEnd <= r.RowStart || r.RowEnd > p.RowCount {
		return ErrRegionOutOfBounds
	}
	if p.Cols == nil {
		if r.ColStart != nil || r.ColEnd != nil {
			return ErrRegionOutOfBounds
		}
		return nil
	}
	if r.ColStart == nil && r.ColEnd == nil {
		return nil // a row range is legal on a live frame too (full width)
	}
	if r.ColStart == nil || r.ColEnd == nil ||
		*r.ColStart < 0 || *r.ColEnd <= *r.ColStart || *r.ColEnd > *p.Cols {
		return ErrRegionOutOfBounds
	}
	return nil
}

// ── run state reads ───────────────────────────────────────────────────────

// RunState returns the durable assistant run state of one execution — the
// state a reconnecting renderer reads (design §7; it never infers liveness
// from notifications having stopped). Nil when the execution is not an
// agent run.
func (s *sqliteContent) RunState(ctx context.Context, executionID int64) (*RunState, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state FROM executions WHERE id = ?`, executionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchRun
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	v := RunState(raw.String)
	return &v, nil
}
