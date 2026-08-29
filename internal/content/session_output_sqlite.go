package content

// The SQLite half of session output recording (nocx-22k1c.1). The WHY is in
// session_output.go; this file is the mechanism.
//
// ── The cap, and why it drops what it drops ───────────────────────────────
//
// The bound is the user's own per-command cap (Policy.OutputCapBytes,
// settings.HistoryOutputCapKB), and its declared semantics are "head and tail
// together, middle dropped" — errors live in the tail and the invocation in
// the head. capBody in capture-client.ts cuts a frozen block half and half,
// and this cuts a live stream the same way, so the two halves of one product
// do not disagree about what a cap means.
//
// A live stream cannot be cut the way a finished body can: nothing knows how
// long it will be. So the head is RESERVED at the first append — head_end is
// fixed there and never moves — and everything after it is droppable. When
// the recording would exceed the cap the OLDEST droppable chunk goes, then
// the next, until it fits. Exceeding the cap is what the cap is for and never
// fails the write.
//
// head_end is fixed at the first append rather than recomputed from the live
// setting, deliberately. A head that moved with the knob would either orphan
// bytes already marked head (the cap shrinks) or promote bytes the cap has
// already dropped (it grows), and the second is a recording that claims a
// head it does not hold.
//
// ── Idempotency ──────────────────────────────────────────────────────────
//
// (session_id, byte_offset) is the chunk table's key and a re-sent run
// inserts nothing. That is what lets the recorder retry an append whose error
// it could not classify: the alternative is a cursor it dare not advance,
// which is the stall this whole feature exists to remove.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// sessionOutputChunkBytes bounds one chunk row. It is the eviction
// granularity as well as the row size: the cap is enforced by dropping whole
// chunks, so a recording can sit up to one chunk under its cap. 16 KiB puts
// the default 256 KiB cap at sixteen rows and the error under 7%, which is a
// better trade than either a row per PTY read (an eviction step of 32 KiB) or
// a row per kilobyte (256 rows and 256 inserts per session).
const sessionOutputChunkBytes = 16 << 10

var _ SessionOutputRepository = (*sqliteContent)(nil)

// Stance answers from the live policy, never from a value read at open: the
// History settings apply without a restart (app.go's registry notifier), and
// this is what the product's "a detached session is not being recorded"
// sentence is derived from.
func (s *sqliteContent) Stance() SessionOutputStance {
	switch {
	case !s.policy.Enabled():
		return SessionOutputHistoryOff
	case !s.policy.OutputEnabled():
		return SessionOutputRetentionOff
	default:
		return SessionOutputKept
	}
}

// Append records one run of bytes at its stream offset.
func (s *sqliteContent) Append(ctx context.Context, in SessionOutputAppend) (SessionOutputResult, error) {
	if in.SessionID == "" {
		return SessionOutputResult{}, errors.New("content: session output: session id is required")
	}
	// Decided before the transaction opens, exactly as CaptureOutput decides
	// its two refusals: nothing is written for a body nobody wants, and the
	// caller is told it was refused rather than that it failed.
	if s.Stance() != SessionOutputKept {
		return SessionOutputResult{}, nil
	}
	capBytes := int64(s.policy.OutputCapBytes())

	var out SessionOutputResult
	err := s.run(ctx, func(ctx context.Context) error {
		var runErr error
		out, runErr = s.appendSessionOutput(ctx, in, capBytes)
		return runErr
	})
	if err != nil {
		return SessionOutputResult{}, err
	}
	return out, nil
}

// sessionOutputRow is the recording's own row, read at the top of an append.
type sessionOutputRow struct {
	first   int64
	next    int64
	byteLen int64
	headEnd int64
}

// appendSessionOutput runs ON the writer goroutine (see run's contract) and
// must never call back into run.
func (s *sqliteContent) appendSessionOutput(ctx context.Context, in SessionOutputAppend, capBytes int64) (SessionOutputResult, error) {
	// BEGIN IMMEDIATE for the reason Submit and CaptureOutput both state: the
	// write lock is taken at BEGIN rather than at the first write, so a
	// second writer waits instead of failing an upgrade.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SessionOutputResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	// Offsets are byte counts of a stream one process produced; they cannot
	// reach the int64 ceiling in any run this program survives.
	offset := int64(in.Offset) //nolint:gosec
	row, err := loadOrCreateSessionOutput(ctx, tx, in.SessionID, offset, capBytes, now)
	if err != nil {
		return SessionOutputResult{}, err
	}

	end := offset + int64(len(in.Body))
	switch {
	case len(in.Body) == 0:
		// A zero-length read is a non-event, not a degrade. The row exists
		// now, which is what makes "this session produced nothing" a
		// recording rather than an absence.
		if commitErr := tx.Commit(); commitErr != nil {
			return SessionOutputResult{}, commitErr
		}
		return SessionOutputResult{Kept: true}, nil
	case offset < row.next && end == row.next:
		// The run that ended at the cursor, sent again — a retry after an
		// error the caller could not classify. Every chunk of it is already
		// there under the same key.
		if commitErr := tx.Commit(); commitErr != nil {
			return SessionOutputResult{}, commitErr
		}
		return SessionOutputResult{Kept: true}, nil
	case offset != row.next:
		return SessionOutputResult{}, fmt.Errorf(
			"%w: session %s is at %d, append starts at %d",
			ErrSessionOutputDiscontinuous, in.SessionID, row.next, offset)
	}

	if insertErr := insertSessionOutputChunks(ctx, tx, in.SessionID, offset, end, row.headEnd, in.Body); insertErr != nil {
		return SessionOutputResult{}, insertErr
	}
	row.byteLen += int64(len(in.Body))
	row.next = end

	dropped, err := evictSessionOutput(ctx, tx, in.SessionID, &row, capBytes)
	if err != nil {
		return SessionOutputResult{}, err
	}

	gaps, truncated, err := sessionOutputHole(ctx, tx, in.SessionID, row)
	if err != nil {
		return SessionOutputResult{}, err
	}
	if _, updateErr := tx.ExecContext(ctx,
		`UPDATE session_output
		    SET next_offset = ?, byte_len = ?, truncated = ?, gaps = ?, updated_at = ?
		  WHERE session_id = ?`,
		row.next, row.byteLen, truncated, gaps, now, in.SessionID); updateErr != nil {
		return SessionOutputResult{}, fmt.Errorf("content: session output: update recording: %w", updateErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return SessionOutputResult{}, commitErr
	}
	return SessionOutputResult{Kept: true, Dropped: uint64(dropped)}, nil //nolint:gosec // dropped ≥ 0
}

// loadOrCreateSessionOutput reads the recording's row, creating it on the
// first append. The head is reserved HERE and nowhere else — see the header.
func loadOrCreateSessionOutput(ctx context.Context, tx *sql.Tx, sessionID string, offset, capBytes, now int64) (sessionOutputRow, error) {
	var row sessionOutputRow
	err := tx.QueryRowContext(ctx,
		`SELECT first_offset, next_offset, byte_len, head_end FROM session_output WHERE session_id = ?`,
		sessionID).Scan(&row.first, &row.next, &row.byteLen, &row.headEnd)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("content: session output: read recording: %w", err)
	}
	row = sessionOutputRow{first: offset, next: offset, headEnd: offset + capBytes/2}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session_output
		    (session_id, first_offset, next_offset, byte_len, head_end, truncated, gaps, started_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, NULL, '[]', ?, ?)`,
		sessionID, row.first, row.next, row.headEnd, now, now); err != nil {
		return row, fmt.Errorf("content: session output: open recording: %w", err)
	}
	return row, nil
}

// insertSessionOutputChunks splits the body into rows, cutting at the head
// boundary so a chunk is wholly inside the head or wholly outside it —
// eviction takes whole chunks, and a chunk that straddled the boundary would
// have to be half-taken or wholly exempt, both of which are wrong.
func insertSessionOutputChunks(ctx context.Context, tx *sql.Tx, sessionID string, offset, end, headEnd int64, body []byte) error {
	base := offset
	for at := offset; at < end; {
		stop := at + sessionOutputChunkBytes
		if stop > end {
			stop = end
		}
		if at < headEnd && stop > headEnd {
			stop = headEnd
		}
		head := 0
		if at < headEnd {
			head = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_output_chunks (session_id, byte_offset, body, head)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (session_id, byte_offset) DO NOTHING`,
			sessionID, at, body[at-base:stop-base], head); err != nil {
			return fmt.Errorf("content: session output: append chunk at %d: %w", at, err)
		}
		at = stop
	}
	return nil
}

// evictSessionOutput drops the oldest droppable chunks until the recording
// fits its cap, and returns what that cost. It stops when only head chunks
// are left: the head is half the cap, so a recording of the head alone is
// always inside it, and a store that took the head to satisfy a bound would
// have thrown away the invocation to keep a progress bar.
func evictSessionOutput(ctx context.Context, tx *sql.Tx, sessionID string, row *sessionOutputRow, capBytes int64) (int64, error) {
	var dropped int64
	for row.byteLen > capBytes {
		var at, n int64
		err := tx.QueryRowContext(ctx,
			`SELECT byte_offset, length(body) FROM session_output_chunks
			  WHERE session_id = ? AND head = 0 ORDER BY byte_offset LIMIT 1`,
			sessionID).Scan(&at, &n)
		if errors.Is(err, sql.ErrNoRows) {
			return dropped, nil
		}
		if err != nil {
			return dropped, fmt.Errorf("content: session output: find the oldest chunk: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM session_output_chunks WHERE session_id = ? AND byte_offset = ?`,
			sessionID, at); err != nil {
			return dropped, fmt.Errorf("content: session output: drop the oldest chunk: %w", err)
		}
		row.byteLen -= n
		dropped += n
	}
	return dropped, nil
}

// sessionOutputHole derives the recording's gap from the chunks that are
// actually there, rather than accumulating it as bytes go. Derived, so it
// cannot drift: the hole is exactly the span between the last head byte and
// the first surviving tail byte, whatever sequence of evictions produced it.
func sessionOutputHole(ctx context.Context, tx *sql.Tx, sessionID string, row sessionOutputRow) (string, *string, error) {
	var headEnd, tailStart sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(byte_offset + length(body)) FROM session_output_chunks
		  WHERE session_id = ? AND head = 1`, sessionID).Scan(&headEnd); err != nil {
		return "", nil, fmt.Errorf("content: session output: measure the head: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT MIN(byte_offset) FROM session_output_chunks
		  WHERE session_id = ? AND head = 0`, sessionID).Scan(&tailStart); err != nil {
		return "", nil, fmt.Errorf("content: session output: measure the tail: %w", err)
	}
	from := row.first
	if headEnd.Valid {
		from = headEnd.Int64
	}
	to := row.next
	if tailStart.Valid {
		to = tailStart.Int64
	}
	if to <= from {
		return "[]", nil, nil
	}
	body, err := json.Marshal([]Gap{{Start: from, End: to, Reason: "cap"}})
	if err != nil {
		return "", nil, err
	}
	trunc := string(TruncCap)
	return string(body), &trunc, nil
}

// Read returns everything kept for a session, adjacent chunks joined into
// runs. An unknown session is an empty recording: nothing was produced, or
// all of it was dropped, and neither is a fault a caller can act on.
func (s *sqliteContent) Read(ctx context.Context, sessionID string) (SessionOutputRecording, error) {
	out := SessionOutputRecording{SessionID: sessionID}
	var first, next, byteLen int64
	var truncated sql.NullString
	var gaps string
	err := s.db.QueryRowContext(ctx,
		`SELECT first_offset, next_offset, byte_len, truncated, gaps
		   FROM session_output WHERE session_id = ?`, sessionID).
		Scan(&first, &next, &byteLen, &truncated, &gaps)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return SessionOutputRecording{}, fmt.Errorf("content: session output: read recording: %w", err)
	}
	out.Bytes = uint64(byteLen) //nolint:gosec // byte counts, never negative
	out.Produced = uint64(next) //nolint:gosec
	if truncated.Valid && truncated.String != "" {
		t := Truncation(truncated.String)
		out.Truncated = &t
	}
	if gaps != "" && gaps != "[]" {
		if decodeErr := json.Unmarshal([]byte(gaps), &out.Gaps); decodeErr != nil {
			return SessionOutputRecording{}, fmt.Errorf("content: session output: decode gaps: %w", decodeErr)
		}
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT byte_offset, body FROM session_output_chunks
		  WHERE session_id = ? ORDER BY byte_offset`, sessionID)
	if err != nil {
		return SessionOutputRecording{}, fmt.Errorf("content: session output: read chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var at int64
		var body []byte
		if err := rows.Scan(&at, &body); err != nil {
			return SessionOutputRecording{}, fmt.Errorf("content: session output: scan chunk: %w", err)
		}
		// Adjacent chunks are ONE run. A run boundary in the returned slice
		// is a claim that the bytes are not adjacent, so it may appear only
		// where the stream really has a hole.
		if n := len(out.Runs); n > 0 {
			prev := &out.Runs[n-1]
			if prev.Offset+uint64(len(prev.Body)) == uint64(at) { //nolint:gosec
				prev.Body = append(prev.Body, body...)
				continue
			}
		}
		out.Runs = append(out.Runs, SessionOutputRun{Offset: uint64(at), Body: body}) //nolint:gosec
	}
	if err := rows.Err(); err != nil {
		return SessionOutputRecording{}, fmt.Errorf("content: session output: read chunks: %w", err)
	}
	return out, nil
}
