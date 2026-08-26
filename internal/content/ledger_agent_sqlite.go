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

	"github.com/shady2k/nocx/internal/workspace"
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
		Subject             Source
		EnvironmentID       string
		SessionID           *string
		Rows                []FrameRow
		Cursor              *FrameCursor
		Identity            *FrameIdentity
		Range               *FrameRange
		SerializerVersion   *int
	}{
		Client: in.Client, Cwd: in.Cwd, Source: string(in.Source),
		Subject: in.Subject, EnvironmentID: in.Env.ID, SessionID: in.SessionID,
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
// It is DERIVED from workspace.Default rather than restated: one id with
// two declarations is two owners of one fact, and nocx-49d4 — the bug
// this constant belongs to — says in as many words that nothing mints a
// second permanent home under another id. internal/workspace owns the
// domain concept; the store looks at it.
const DefaultWorkspaceID = string(workspace.Default)

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
	// Empty Subject is the ask gesture — the renderer's captureFrame call,
	// which is a person selecting blocks and asking. The readScreen pull is
	// a different producer that stamps SourceAssistant when it ever
	// persists; until then the honest default for a capture naming no
	// subject is the person's. Defaulted HERE, before frameDigest, so a
	// legacy empty-subject replay hashes identically to the normalized
	// frame it created (the same rule Submit follows for Source).
	if in.Subject == "" {
		in.Subject = SourceUser
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
			(id, ingest_seq, client, digest, environment_id, session_id, cwd, kind, source, intent,
			 phase, status, submitted_at, capture_key, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'frame', ?, ?, 'closed', 'success', ?, ?, '{}')`,
			frameID, seq, in.Client, digest, in.Env.ID, in.SessionID, in.Cwd,
			string(in.Subject), FrameIntent, now, in.CaptureID); insertErr != nil {
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
		// The frame's body belongs to the frame BLOCK (ADR-0040); the
		// capture execution beside it says which attempt took it.
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
			(id, entry_id, execution_id, media_type, state, byte_len, capture_method, capture_version,
			 terminal_cols, terminal_rows, encoding, gaps, payload)
			VALUES (?, ?, ?, ?, 'sealed', ?, ?, ?, ?, ?, 'utf-8', '[]', ?)`,
			artifactID, frameID, execID, string(mediaType), len(text), string(method), version,
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

// SubmitAgentAsk records ONE ask transaction atomically: the TURN
// (kind=agent, open/pending — the question is its intent, anchored to the
// pane it was asked in), its pending run (the backend-minted execution row,
// state prepared — the model is never called here) with the open artifact
// the answer will be streamed into, and the references edges, each carrying
// its region. Every reference is validated INSIDE the
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
			// A replay returns the ORIGINAL run and turn, and nothing else
			// to return: the answer's body is not one artifact any more, it
			// is whatever `text` children the run has opened so far
			// (ADR-0040). A replay that re-drove the stream would open its
			// prose blocks after the ones already there, which is the same
			// answer twice and is a defect of the driver, not of this
			// result shape.
			out = AgentAskResult{
				RunID: runID, EntryID: in.ID, IngestSeq: haveSeq, Replayed: true,
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
		// The turn: the question is the entry's INTENT, which is what the
		// block's header renders, and pane_id is the anchor that makes it
		// restorable at all (nocx-4em1z). session_id beside it is
		// provenance and is swept on the next Open.
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, session_id, cwd, kind, source, intent,
			 phase, status, submitted_at, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ask', 'user', ?, 'open', 'pending', ?, 'normal', '{}')`,
			in.ID, seq, in.Client, digest, in.Env.ID, in.PaneID, in.SessionID, in.Cwd,
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

		// NO ANSWER ARTIFACT. The ask used to open one text/plain artifact
		// on the run here, and every delta of the whole answer appended to
		// it — which made the STORED unit the whole answer while the DRAWN
		// unit was a run of prose between two calls, and something had to
		// translate between them. That translation was the anchor ADR-0040
		// deletes.
		//
		// The turn's body is its children now: the run opens a `text` block
		// on the first delta after a call (OpenProse) and seals it when the
		// next call arrives, so the boundary is a row rather than an offset.
		// A turn that never streamed a word therefore carries no body at
		// all, which is the honest shape — an empty artifact was a claim
		// that something was printed.
		//
		// The prose blocks stay ARTIFACTS WITH PROVENANCE rather than
		// strings in a column, which is the part of design §5 that outlives
		// the shape it was written in (ADR-0019 §6); text/plain and never
		// application/vt, because a turn has no terminal body and never
		// will, and that stored fact is what a restored block picks its
		// grammar from (prose wraps, a grid must not).
		if err := tx.Commit(); err != nil {
			return err
		}
		out = AgentAskResult{RunID: runID, EntryID: in.ID, IngestSeq: seq}
		return nil
	})
	return out, err
}

// TransitionRun moves the assistant run to a NON-TERMINAL state. The machine
// (nocx-z9hj4): prepared → streaming (the ask starts — the gate deltas may
// not pass before: a delta persisted before the streaming transition commits
// would be a delta outside the run's non-terminal span), streaming →
// awaiting_approval (the policy or the egress gate suspended the run before
// the provider was reached), and awaiting_approval → streaming (the person
// answered and the run streams again). Terminal moves go through
// FinishAgentRun; a move not on the machine is refused, never silently
// applied.
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
		legal := (cur == RunPrepared && to == RunStreaming) ||
			(cur == RunStreaming && to == RunAwaitingApproval) ||
			(cur == RunAwaitingApproval && to == RunStreaming)
		if !legal {
			return fmt.Errorf("content: transition run: illegal move %s → %s", cur, to)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE executions SET state = ? WHERE id = ?`, string(to), runID); err != nil {
			return err
		}
		return nil
	})
}

// FinishAgentRun closes the run and its turn in ONE transaction: the run's
// terminal state, end and termination reason; the turn's entry, with the
// interval it took; and the answer body (sealed). A run is never reported
// terminal in the run vocabulary while its entry still says otherwise — both
// lifecycles close together, or neither does.
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
		var runStartedAt sql.NullInt64
		err = tx.QueryRowContext(ctx,
			`SELECT entry_id, payload, started_at FROM executions WHERE id = ?`, runID).
			Scan(&entryID, &payload, &runStartedAt)
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
		// The turn's terminal facts, and its duration with them (nocx-hoeq3).
		// A shell command's duration is the RENDERER's measurement because
		// the renderer is what timed it; nobody times a turn but this
		// process, which opened the run at submit and is closing it now. So
		// the two ends here are one clock, and subtracting them is asking
		// that clock rather than differencing two (see the schema note).
		//
		// A run with no start — nothing writes one, but the column is
		// nullable and a row can be older than the code reading it — leaves
		// both the start and the duration untouched. Null is "we do not
		// know", which is a fact the header can draw honestly; a zero would
		// be the claim that the assistant answered instantly.
		var startedAt, durationMs *int64
		if runStartedAt.Valid {
			started := runStartedAt.Int64
			startedAt = &started
			if d := in.EndedAt - started; d >= 0 {
				durationMs = &d
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET phase = 'closed', status = ?,
			   started_at = COALESCE(started_at, ?),
			   ended_at = ?,
			   duration_ms = COALESCE(?, duration_ms)
			 WHERE id = ?`,
			string(status), startedAt, in.EndedAt, durationMs, entryID); err != nil {
			return err
		}
		// The body seals with the run: a terminal run never leaves an
		// artifact open for deltas that can no longer arrive.
		if err := sealTurnBody(ctx, tx, entryID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// sealTurnBody seals an ASK turn's answer body — which since ADR-0040 is
// every `text` child the run wrote, not one artifact on the run. It is the
// terminalizers' shared step, so EVERY path that closes an ask run closes its
// prose too: a run is never reported terminal while a block it streamed into
// still says open.
//
// The tool-call boundary seals a block as it goes (SealProse), so on the
// ordinary path this reaches only the LAST run of prose — the one no call
// ever followed. It is written as a set rather than as "the last one"
// deliberately: a run that failed between opening a block and sealing it
// leaves an open artifact this must still close, and asking for the whole set
// costs one statement and cannot miss one.
//
// The entry itself is closed by the caller, in the same transaction — that
// is the whole of what "close the turn" now means, because the turn is one
// entry (nocx-4em1z). An entry with no prose children — an ordinary command,
// or a turn that failed before it said anything — matches nothing and this is
// a no-op, which is what keeps the capture path's own artifact out of it.
func sealTurnBody(ctx context.Context, tx *sql.Tx, entryID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE artifacts SET state = 'sealed'
		  WHERE entry_id IN (SELECT id FROM entries WHERE parent_id = ? AND kind = 'text')`,
		entryID)
	return err
}

// proseDigest is the content binding of a prose block's row. entries.digest
// exists to bind an UNTRUSTED client-minted id to what it was submitted with;
// a prose block has neither — its id is minted here from crypto/rand — so the
// honest value is what identifies the row in the tree: the turn it belongs to
// and the seat it took. Deterministic, so a row can be recognised rather than
// merely stored.
func proseDigest(turnID string, pos int) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "prose\x00%s\x00%d", turnID, pos)
	return hex.EncodeToString(h.Sum(nil))
}

// OpenProse opens one run of assistant prose under a turn (ADR-0040): the
// `text` child and its body, in ONE transaction.
//
// Why the two writes cannot be separate calls: a `text` entry with no
// artifact is a block that will draw as an empty paragraph if the run dies
// between them, and an artifact with no entry has no place in the order at
// all. They are one fact — "there is a run of prose here, and this is where
// its text goes" — so they are one commit.
//
// The seat is taken INSIDE the transaction as MAX(pos)+1 under the parent,
// which is AddCause's rule and deliberately the same one: two writers reach
// entries.pos, and a second definition of "the next free seat" is a second
// answer that would agree until the day it did not. UNIQUE (parent_id, pos)
// is the backstop under both.
//
// The block carries the turn's environment and cwd — the columns are NOT NULL
// and its prose was written in the turn's context — and deliberately NO
// pane_id and NO session_id. A prose block is drawn INSIDE its parent and
// never at the top level, and pane_id is exactly what the pane-scoped page
// reads (ledgerWhere): giving prose an anchor would put every paragraph of
// every answer into the restore as a block of its own, and into the model's
// own blocks.list beside them.
//
// It DOES carry the run that printed it, in the payload (ProseFacts). The run
// is resolved in the same transaction and must be an agent-lane execution of
// this turn: a run id from another turn would seat prose here and attribute
// it there, which is the one way this column could make a reader worse off
// than no column at all.
func (s *sqliteContent) OpenProse(ctx context.Context, turnID string, runID int64) (ProseBlock, error) {
	if turnID == "" {
		return ProseBlock{}, errors.New("content: open prose: turn id is required — prose is somebody's child")
	}
	if runID <= 0 {
		return ProseBlock{}, errors.New("content: open prose: run id is required — prose belongs to the run that printed it")
	}
	facts, factsErr := json.Marshal(ProseFacts{RunID: runID})
	if factsErr != nil {
		return ProseBlock{}, factsErr
	}
	var out ProseBlock
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		// The parent is RESOLVED, not left to the foreign key: the FK would
		// refuse a dangling parent_id anyway, but a driver's constraint text
		// does not say which of the entry's references was missing, and
		// ErrNoSuchEntry is the answer this repository already gives to "that
		// entry does not exist" (AddCause).
		var client, envID, cwd string
		err = tx.QueryRowContext(ctx,
			`SELECT client, environment_id, cwd FROM entries WHERE id = ?`, turnID).
			Scan(&client, &envID, &cwd)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: open prose under %s: %w", turnID, ErrNoSuchEntry)
		}
		if err != nil {
			return err
		}

		// The run must be THIS turn's, and an agent-lane one: the prose it
		// prints is assembled into this turn's message, so a run belonging
		// to another block would put one turn's sentences into another's
		// context. Refused by name rather than by a foreign key, which would
		// accept any execution in the table.
		var lane sql.NullString
		err = tx.QueryRowContext(ctx,
			`SELECT lane FROM executions WHERE id = ? AND entry_id = ?`, runID, turnID).Scan(&lane)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("content: open prose under %s: run %d is not a run of that turn: %w",
				turnID, runID, ErrNoSuchRun)
		}
		if err != nil {
			return err
		}
		if lane.String != agentLane {
			return fmt.Errorf("content: open prose under %s: execution %d is not an agent run: %w",
				turnID, runID, ErrNoSuchRun)
		}

		var highest sql.NullInt64
		if err = tx.QueryRowContext(ctx,
			`SELECT MAX(pos) FROM entries WHERE parent_id = ?`, turnID).Scan(&highest); err != nil {
			return err
		}
		pos := 0
		if highest.Valid {
			pos = int(highest.Int64) + 1
		}

		var seq int64
		if seqErr := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`).Scan(&seq); seqErr != nil {
			return fmt.Errorf("content: open prose: assign ingest_seq: %w", seqErr)
		}

		entryID := mintID()
		// Born closed and successful, naming no intent: printing text can
		// neither run nor fail, and the intent was the question. The CHECK on
		// entries says exactly this, and the row is written to satisfy it
		// rather than to be refused by it.
		if _, err = tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent,
			 phase, status, submitted_at, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'text', 'assistant', '', 'closed', 'success', ?, 'normal', ?)`,
			entryID, seq, client, proseDigest(turnID, pos), envID, turnID, pos, cwd,
			time.Now().UnixMilli(), string(facts)); err != nil {
			return err
		}

		// The body: owned by the BLOCK, with no execution — a run of prose
		// was printed, not attempted, so there is no attempt to name as its
		// provenance (ADR-0040 decision 3). Open until the boundary arrives.
		artifactID := mintID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts
			(id, entry_id, execution_id, media_type, state, byte_len, capture_method, capture_version,
			 encoding, gaps, payload)
			VALUES (?, ?, NULL, 'text/plain', 'open', 0, 'none', 1, 'utf-8', '[]', '{}')`,
			artifactID, entryID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		out = ProseBlock{EntryID: entryID, ArtifactID: artifactID}
		return nil
	})
	return out, err
}

// SealProse seals one prose block's body. The caller's fact is "nothing more
// will be appended here", and it is stated by id rather than by hunting for
// "the open one": the transport holds the block it opened, so a search would
// be a second answer to a question the caller already has.
//
// A block with no body matches nothing and that is not an error — the fact
// being recorded is about the block, and it is true whether or not a byte
// ever arrived.
func (s *sqliteContent) SealProse(ctx context.Context, entryID string) error {
	if entryID == "" {
		return errors.New("content: seal prose: entry id is required")
	}
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`UPDATE artifacts SET state = 'sealed' WHERE entry_id = ?`, entryID)
		return err
	})
}

// validateFrameReference checks one reference against the STORED frame:
// the id names a frame (kind=frame — the discriminated column, never an
// intent comparison), it belongs to the asking session — "an ask naming a
// frame from another session is rejected" (design §5) — and the region
// lies inside the frame's own rows and columns. Any failure refuses the
// whole ask transaction.
func validateFrameReference(ctx context.Context, tx *sql.Tx, in AgentAsk, ref AgentReference) error {
	var kind string
	var sessionID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT kind, session_id FROM entries WHERE id = ?`, ref.FrameID).
		Scan(&kind, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFrameNotFound
	}
	if err != nil {
		return err
	}
	if kind != string(EntryFrame) {
		return ErrNotAFrame
	}
	if !sessionID.Valid || in.SessionID == nil || sessionID.String != *in.SessionID {
		return ErrFrameSessionMismatch
	}

	var payload string
	err = tx.QueryRowContext(ctx,
		`SELECT a.payload FROM artifacts a WHERE a.entry_id = ? ORDER BY a.id LIMIT 1`,
		ref.FrameID).Scan(&payload)
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
