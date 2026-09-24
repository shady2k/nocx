package content

// Block rows — the SQLite half of a streamed block's output
// (nocx-2v80t.3.7). The WHY of the stored form is in ledger.go (the types)
// and block_rows_encode.go (the vocabulary); this file is the mechanism, and
// it deliberately walks the two paths this package already built:
//
// CaptureOutput's decision block answers the ONE keep decision — output
// retention off, a sensitive entry, a critical environment, decided at the
// command's authenticated start so nothing that may not be kept is ever
// written, not even a first chunk.
//
// session_output's cap answers the bound: the head is RESERVED at the first
// append and never moves (a head that moved with the knob would orphan bytes
// already marked head or promote bytes the cap already dropped), the oldest
// droppable chunk goes when the block would exceed the cap, and exceeding
// the cap is what the cap is for and never fails the write. The differences
// are forced by the artifacts table having neither a head_end column nor a
// per-chunk flag: the reservation and the cursor ride the artifact's payload
// (one writer, one transaction, so it cannot drift from the chunks), and a
// chunk is head by arithmetic — its end offset at or before head_end —
// derived from the same seq-ordered scan that drives eviction.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// blockRowsChunkBytes bounds one chunk and is the eviction granularity: the
// cap is enforced by dropping whole chunks, cut at line boundaries so a
// stored line is never split across the head boundary or an eviction. The
// 16 KiB chunk keeps transactions bounded without making row encoding depend
// on the cap.
const blockRowsChunkBytes = 16 << 10

// blockRowsPayload is the rows artifact's payload sidecar. While the block
// is open it is the writer's own cursor and reservation; at close it is
// rewritten to the summary a reader sees. It is a sidecar and never a second
// copy of anything the chunks hold: dropped rows are DERIVED at close from
// the chunks that are actually there, so the count cannot drift from them.
type blockRowsPayload struct {
	// HeadEnd is one past the reserved head's last byte, fixed at the first
	// append. Nil until then.
	HeadEnd *int64 `json:"headEnd,omitempty"`
	// NextRow is the absolute index the NEXT delivery must start at.
	NextRow uint64 `json:"nextRow,omitempty"`
	// LostRows is what the emulator pruned before the coordinator could
	// read it, carried here because no cap chose it.
	LostRows uint64 `json:"lostRows,omitempty"`
	// DroppedRows is written once, at close, derived from the chunks.
	DroppedRows uint64 `json:"droppedRows,omitempty"`
	// Appended is the total bytes ever appended, BEFORE any eviction. It is
	// the stream offset the next delivery begins at, and it is what makes
	// the eviction walk honest after a previous eviction: the surviving
	// chunks' cumulative lengths no longer equal their stream offsets once
	// the middle is gone, and a cursor derived from them alone would exempt
	// tail chunks that were never head.
	Appended int64 `json:"appended,omitempty"`
}

func (s *sqliteContent) OpenBlockOutput(ctx context.Context, in OpenBlockOutput) (string, error) {
	if in.EntryID == "" || in.ArtifactID == "" {
		return "", errors.New("content: block rows: entry id and artifact id are required")
	}
	// The gate, before the transaction opens — the same shape CaptureOutput
	// decides its refusals in. Nothing is written for a block nobody wants,
	// and the caller is told it was refused rather than that it failed.
	if !s.policy.OutputEnabled() {
		return "", nil
	}
	opened := ""
	err := s.run(ctx, func(ctx context.Context) error {
		// BEGIN IMMEDIATE for the reason Submit and CaptureOutput state:
		// the write lock is taken at BEGIN rather than at the first write.
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

		// The entry's own execution — the run the authenticated start
		// bound — with its PINNED observation, because criticality is read
		// from what was true when the command ran (CaptureOutput's comment
		// carries the full argument).
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
		if Criticality(criticality) == CriticalityCritical {
			return nil
		}

		// The caller's id first: untrusted, exactly CaptureOutput's
		// idempotency key. A replay of an open whose ack was lost must find
		// the block it wrote, and the same id naming a different entry's
		// block is a conflict, never an overwrite.
		var heldID, heldEntry string
		byIDErr := tx.QueryRowContext(ctx,
			`SELECT id, entry_id FROM artifacts WHERE id = ? AND media_type = ?`,
			in.ArtifactID, string(MediaBlockRows)).Scan(&heldID, &heldEntry)
		switch {
		case byIDErr == nil:
			if heldEntry != in.EntryID {
				return fmt.Errorf("content: block rows: artifact %s belongs to entry %s: %w",
					in.ArtifactID, heldEntry, ErrIDConflict)
			}
			opened = heldID
			return tx.Commit()
		case !errors.Is(byIDErr, sql.ErrNoRows):
			return byIDErr
		}
		// An app-originated start's open fact arrives twice — the submit
		// handler emits it before its row insert is durable and again after
		// — so an open block on this run is the idempotent answer, whatever
		// id the replay carries.
		var existingID string
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT id FROM artifacts
			  WHERE execution_id = ? AND media_type = ? AND state = ?`,
			execID, string(MediaBlockRows), string(ArtifactOpen)).Scan(&existingID)
		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			if insertErr := insertArtifact(ctx, tx, AppendArtifact{
				EntryID:       in.EntryID,
				ExecutionID:   &execID,
				ID:            in.ArtifactID,
				MediaType:     MediaBlockRows,
				CaptureMethod: CaptureTerminalCells,
			}); insertErr != nil {
				return insertErr
			}
			opened = in.ArtifactID
		case lookupErr != nil:
			return lookupErr
		default:
			opened = existingID
		}
		return tx.Commit()
	})
	return opened, err
}

func (s *sqliteContent) AppendBlockRows(ctx context.Context, in AppendBlockRows) error {
	if in.EntryID == "" || in.ArtifactID == "" {
		return errors.New("content: block rows: entry id and artifact id are required")
	}
	if len(in.Rows) == 0 {
		// An empty delivery is not an event: the seam never sends one, and
		// recording nothing under a row index that advanced would put a
		// silent hole in the block.
		return errors.New("content: block rows: an append carries at least one row")
	}
	capBytes := int64(s.policy.OutputCapBytes())
	return s.run(ctx, func(ctx context.Context) error {
		// BEGIN IMMEDIATE — CaptureOutput's reason, again.
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var known int
		if knownErr := tx.QueryRowContext(ctx,
			`SELECT 1 FROM entries WHERE id = ?`, in.EntryID).Scan(&known); knownErr != nil {
			if errors.Is(knownErr, sql.ErrNoRows) {
				return ErrNoSuchEntry
			}
			return knownErr
		}
		state, err := openBlockRowsForAppend(ctx, tx, in)
		if err != nil {
			return err
		}

		// Continuity, both ends: a replay from behind the cursor is the
		// past arriving again; a jump must name the rows that went missing.
		// The FIRST append takes any FromRow — the session's absolute row
		// counter includes rows that departed before this command started,
		// which belonged to no block.
		if state.hasCursor {
			switch {
			case in.FromRow < state.payload.NextRow:
				return fmt.Errorf("%w: block %s is at row %d, append starts at %d",
					ErrBlockRowsDiscontinuous, in.ArtifactID, state.payload.NextRow, in.FromRow)
			case in.FromRow > state.payload.NextRow && in.LostRows != in.FromRow-state.payload.NextRow:
				return fmt.Errorf("%w: block %s is at row %d, append starts at %d claiming %d lost",
					ErrBlockRowsDiscontinuous, in.ArtifactID, state.payload.NextRow, in.FromRow, in.LostRows)
			}
		}

		if state.payload.HeadEnd == nil {
			// The reservation is FIXED here and never moves; see the header.
			head := capBytes / 2
			state.payload.HeadEnd = &head
		}

		// Encode, then cut into chunk rows at line boundaries — every line
		// whole inside one chunk, so eviction and the head boundary take
		// whole lines and a stored row is never split. The head boundary is
		// a cut like any other: a chunk that straddled head_end would have
		// to be half-taken or wholly exempt at eviction, and wholly exempt
		// would let one oversized chunk carry the whole cap. The offset is
		// where this delivery begins, which is the artifact's own length.
		offset := state.payload.Appended
		headEnd := int64(^uint64(0) >> 1) // no reservation yet: no head cut
		if state.payload.HeadEnd != nil {
			headEnd = *state.payload.HeadEnd
		}
		var chunks [][]byte
		current := make([]byte, 0, blockRowsChunkBytes)
		// at is the stream offset the OPEN chunk starts at; it moves only
		// when a chunk is closed, and a closed chunk moves it by the whole
		// chunk — not by the line, or every line after the first would
		// still look like it sits at the head boundary.
		at := offset
		for i, row := range in.Rows {
			line, encErr := encodeBlockRowsLine(in.FromRow+uint64(i), row) //nolint:gosec // row counts, not byte counts
			if encErr != nil {
				return encErr
			}
			cut := len(current) > 0 &&
				(len(current)+len(line) > blockRowsChunkBytes ||
					(at < headEnd && at+int64(len(current))+int64(len(line)) > headEnd)) //nolint:gosec // a chunk is bounded well below the ceiling
			if cut {
				chunks = append(chunks, current)
				at += int64(len(current)) //nolint:gosec // a chunk is bounded well below the ceiling
				current = make([]byte, 0, blockRowsChunkBytes)
			}
			current = append(current, line...)
		}
		chunks = append(chunks, current)

		var seq int
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(max(seq), 0) FROM artifact_chunks WHERE artifact_id = ?`,
			in.ArtifactID).Scan(&seq); err != nil {
			return err
		}
		var appended int64
		for _, body := range chunks {
			seq++
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO artifact_chunks (artifact_id, seq, body) VALUES (?, ?, ?)`,
				in.ArtifactID, seq, body); err != nil {
				return err
			}
			appended += int64(len(body)) //nolint:gosec // a chunk is bounded well below the ceiling
		}

		state.payload.NextRow = in.FromRow + uint64(len(in.Rows)) //nolint:gosec // row counts, not byte counts
		state.payload.LostRows += in.LostRows
		state.payload.Appended += appended

		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET byte_len = byte_len + ?, payload = ? WHERE id = ?`,
			appended, state.payload.json(), in.ArtifactID); err != nil {
			return err
		}

		// The cap, against what the artifact holds now: the caller splitting
		// a stream into legal deliveries must not assemble an unbounded
		// block out of them. Exceeding is what the cap is for.
		if err := evictBlockRowsToCap(ctx, tx, in.ArtifactID, capBytes); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *sqliteContent) CloseBlockRows(ctx context.Context, in CloseBlockRows) (BlockRowsSummary, error) {
	if in.EntryID == "" || in.ArtifactID == "" {
		return BlockRowsSummary{}, errors.New("content: block rows: entry id and artifact id are required")
	}
	var summary BlockRowsSummary
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		state, err := blockRowsForClose(ctx, tx, in)
		if err != nil {
			return err
		}
		if state.sealed {
			summary = state.summary
			return tx.Commit()
		}

		// The cap's count is DERIVED from the chunks that are actually
		// there, on the write where it becomes permanent — never
		// accumulated, so it cannot drift from them. The block spans
		// [firstFrom, nextRow), the rows the emulator pruned are subtracted
		// first (a jump the caller accounted for is loss, not eviction),
		// and what the chunks hold is what survived the cap.
		dropped, err := deriveBlockRowsDropped(ctx, tx, in.ArtifactID, state.payload.NextRow, state.payload.LostRows)
		if err != nil {
			return err
		}
		summary = BlockRowsSummary{DroppedRows: dropped, LostRows: state.payload.LostRows}

		final := blockRowsPayload{DroppedRows: dropped, LostRows: state.payload.LostRows}
		var truncated any
		if dropped > 0 {
			t := TruncCap
			truncated = string(t)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET state = ?, truncated = COALESCE(?, truncated), payload = ? WHERE id = ?`,
			string(ArtifactSealed), truncated, final.json(), in.ArtifactID); err != nil {
			return err
		}
		return tx.Commit()
	})
	return summary, err
}

// blockRowsState is what an append or a close reads the block as.
type blockRowsState struct {
	payload blockRowsPayload
	// hasCursor is true once the payload carries a NextRow — the first
	// append of a block has no cursor yet, and its FromRow is the block's
	// own beginning.
	hasCursor bool
	sealed    bool
	summary   BlockRowsSummary
}

// json is the payload's stored form. The error json.Marshal declares for
// four integers cannot happen, and a branch no test could reach is worse
// than none.
func (p blockRowsPayload) json() string {
	raw, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func decodeBlockRowsPayload(raw string) blockRowsPayload {
	var p blockRowsPayload
	if raw == "" {
		return p
	}
	// A payload that is not valid JSON is an empty one: the column defaults
	// to '{}' and nothing else writes here.
	_ = json.Unmarshal([]byte(raw), &p)
	return p
}

// blockRowsArtifactFor resolves the block rows artifact an append or a close
// names: by id, verified against the entry. Every id is untrusted, so an id
// that names another entry's block is a conflict and an id that names a
// sealed block has nowhere to put anything.
func blockRowsArtifactFor(ctx context.Context, tx *sql.Tx, entryID, artifactID string) (blockRowsState, error) {
	var state blockRowsState
	var entryIDHeld, stateHeld, payload string
	err := tx.QueryRowContext(ctx,
		`SELECT entry_id, state, payload FROM artifacts WHERE id = ? AND media_type = ?`,
		artifactID, string(MediaBlockRows)).Scan(&entryIDHeld, &stateHeld, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return state, fmt.Errorf("%w: %s", ErrBlockNotOpen, artifactID)
	}
	if err != nil {
		return state, err
	}
	if entryIDHeld != entryID {
		return state, fmt.Errorf("content: block rows: artifact %s belongs to entry %s: %w",
			artifactID, entryIDHeld, ErrIDConflict)
	}
	state.payload = decodeBlockRowsPayload(payload)
	state.hasCursor = state.payload.NextRow > 0
	state.sealed = stateHeld == string(ArtifactSealed)
	if state.sealed {
		state.summary = BlockRowsSummary{DroppedRows: state.payload.DroppedRows, LostRows: state.payload.LostRows}
	}
	return state, nil
}

// The two thin wrappers below exist because the resolve differs: an append
// refuses a sealed block, a close answers it idempotently.
func openBlockRowsForAppend(ctx context.Context, tx *sql.Tx, in AppendBlockRows) (blockRowsState, error) {
	state, err := blockRowsArtifactFor(ctx, tx, in.EntryID, in.ArtifactID)
	if err != nil {
		return state, err
	}
	if state.sealed {
		return state, fmt.Errorf("%w: %s is sealed", ErrBlockNotOpen, in.ArtifactID)
	}
	return state, nil
}

func blockRowsForClose(ctx context.Context, tx *sql.Tx, in CloseBlockRows) (blockRowsState, error) {
	return blockRowsArtifactFor(ctx, tx, in.EntryID, in.ArtifactID)
}

// evictBlockRowsToCap drops the oldest droppable chunks until the artifact
// fits. A chunk is droppable when it starts at or after the head boundary —
// derived from the seq-ordered scan, never stored, so it cannot disagree
// with the bytes. Eviction never fails the write and never touches the head.
func evictBlockRowsToCap(ctx context.Context, tx *sql.Tx, artifactID string, capBytes int64) error {
	for {
		var held int64
		if err := tx.QueryRowContext(ctx,
			`SELECT byte_len FROM artifacts WHERE id = ?`, artifactID).Scan(&held); err != nil {
			return err
		}
		if held <= capBytes {
			return nil
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT seq, length(body) FROM artifact_chunks WHERE artifact_id = ? ORDER BY seq`,
			artifactID)
		if err != nil {
			return err
		}
		// Head and tail are ROLES OF SURVIVING SEQ ORDER, not absolute
		// offsets — which is what makes the walk honest after any deletion
		// pattern: the head is a leading run of chunks (cut at the head
		// boundary when they were written, never taken, so it stays a
		// prefix), the tail is the newest half-cap of bytes, and the cap
		// owns only what sits between them. The oldest middle chunk is the
		// victim, which is what makes the bound "head and tail together".
		var payloadRaw string
		if err := tx.QueryRowContext(ctx,
			`SELECT payload FROM artifacts WHERE id = ?`, artifactID).Scan(&payloadRaw); err != nil {
			_ = rows.Close()
			return err
		}
		payload := decodeBlockRowsPayload(payloadRaw)
		headEnd := int64(-1)
		if payload.HeadEnd != nil {
			headEnd = *payload.HeadEnd
		}
		type chunkRow struct {
			seq  int
			size int64
		}
		var survivors []chunkRow
		for rows.Next() {
			var c chunkRow
			if err := rows.Scan(&c.seq, &c.size); err != nil {
				_ = rows.Close()
				return err
			}
			survivors = append(survivors, c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		// The tail: the newest chunks totalling at least half the cap.
		tailFrom := len(survivors)
		var tailBytes int64
		for tailFrom-1 >= 0 && tailBytes < capBytes/2 {
			tailFrom--
			tailBytes += survivors[tailFrom].size
		}
		// The head: leading chunks while they still sit inside the
		// reservation. A store without a reservation yet has no head.
		headTo := 0
		var headCursor int64
		for headTo < len(survivors) && headEnd >= 0 && headCursor < headEnd {
			headCursor += survivors[headTo].size
			headTo++
		}
		// The oldest chunk that is neither head nor tail.
		var victim int
		var victimBytes int64
		if headTo < tailFrom {
			victim = survivors[headTo].seq
			victimBytes = survivors[headTo].size
		}
		if victim == 0 {
			// Nothing droppable and still over the cap: the protected ends
			// alone exceed it. The ends win — they are what the reservation
			// and the reserve are.
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM artifact_chunks WHERE artifact_id = ? AND seq = ?`,
			artifactID, victim); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET byte_len = byte_len - ? WHERE id = ?`,
			victimBytes, artifactID); err != nil {
			return err
		}
	}
}

// deriveBlockRowsDropped counts the rows the cap took: the block spans
// [firstFrom, nextRow), the chunks hold what survived, and the difference
// minus what the emulator lost before the coordinator could read it is the
// drop — recomputed from the bytes on the one write that seals it. Loss and
// eviction are different facts, and a block that reports its loss as a cap
// verdict sends a user to raise a limit that took nothing.
func deriveBlockRowsDropped(ctx context.Context, tx *sql.Tx, artifactID string, nextRow, lost uint64) (uint64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT body FROM artifact_chunks WHERE artifact_id = ? ORDER BY seq`, artifactID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var stored uint64
	var firstLine []byte
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return 0, err
		}
		stored += uint64(bytes.Count(body, []byte{'\n'})) //nolint:gosec // line counts, not byte counts
		if firstLine == nil {
			if at := bytes.IndexByte(body, '\n'); at >= 0 {
				firstLine = body[:at]
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if firstLine == nil {
		// Nothing was ever appended: the block is empty, not dropped.
		return 0, nil
	}
	var line blockRowsLine
	if err := json.Unmarshal(firstLine, &line); err != nil {
		return 0, fmt.Errorf("content: block rows: read the first stored line of %s: %w", artifactID, err)
	}
	span := nextRow - line.From
	if lost > span {
		lost = span // defensive: loss beyond the block is not this block's
	}
	expected := span - lost
	if stored > expected {
		// The chunks hold MORE than this block's own span-minus-loss
		// arithmetic expects — a bridge drop this block's deliveries never
		// named, or a closing screen the coordinator placed at the
		// artifact's own cursor rather than at the interval's endRow
		// because the interval's rows never fully reached it
		// (ws_block_rows.go's closeBlockRowsNow, "cursor < endRow"). The
		// unsigned subtraction below would otherwise underflow and report
		// roughly 2^64 rows missing for a block that in fact dropped
		// nothing of its own (measured on the e2e, nocx-2v80t.3.9,
		// stage review nocx-2v80t.3.15 finding 10) — so a shortfall this
		// block's own numbers cannot account for is reported as zero
		// rather than as a lie.
		return 0, nil
	}
	return expected - stored, nil
}
