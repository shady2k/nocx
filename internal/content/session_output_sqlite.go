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
//
// ── Two kinds of hole, and only one of them is derivable ─────────────────
//
// A cap eviction is DERIVED from the chunks that are actually there, so it
// can never drift from them — that has always been true here and still is.
// A skipped range cannot be derived from anything: the chunk layout of "the
// cap took these" and "nobody was ever here to offer these" is identical, so
// the difference lives only in what was recorded at the time (nocx-k6p18.2).
//
// So the `gaps` column carries BOTH and is read back as well as written: the
// entries whose reason is not `cap` are facts nothing can recompute and are
// carried forward, the cap's own hole is re-derived from the chunks on every
// write, and the two are merged into one non-overlapping list. Where a cap
// eviction later spans a skipped range, the skip wins that sub-range and the
// cap keeps the rest — bytes the store never ingested cannot have been
// evicted from it, and a single span carrying one reason for both would be
// the false statement the whole bead exists to remove.

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

// Skip advances the recording across a range nobody recorded. The contract
// is on SessionOutputRepository; this is the mechanism.
//
// The two refusals are decided before any transaction opens, exactly as
// Append's are, so nothing is written for a call that was never going to be
// kept. The empty reason is a refusal and not a default: a hole with no
// reason is what makes a reader guess, and the guess it used to make was the
// cap.
func (s *sqliteContent) Skip(ctx context.Context, sessionID string, resumeAt uint64, reason string) (SessionOutputResult, error) {
	if sessionID == "" {
		return SessionOutputResult{}, errors.New("content: session output: session id is required")
	}
	if reason == "" {
		return SessionOutputResult{}, errors.New("content: session output: a skipped range needs a reason; an unexplained hole is one a reader has to guess about")
	}
	if s.Stance() != SessionOutputKept {
		return SessionOutputResult{}, nil
	}
	capBytes := int64(s.policy.OutputCapBytes())
	// Offsets are byte counts of a stream one process produced; they cannot
	// reach the int64 ceiling in any run this program survives.
	at := int64(resumeAt) //nolint:gosec

	var out SessionOutputResult
	err := s.run(ctx, func(ctx context.Context) error {
		var runErr error
		out, runErr = s.skipSessionOutput(ctx, sessionID, at, reason, capBytes)
		return runErr
	})
	if err != nil {
		return SessionOutputResult{}, err
	}
	return out, nil
}

// skipSessionOutput runs ON the writer goroutine (see run's contract) and
// must never call back into run.
//
// What is true on disk if it fails at each step, and how the next start
// recovers: everything below happens inside ONE transaction, so a failure
// before the commit leaves the recording exactly as it was — same cursor,
// same gaps, same chunks — and the caller's next Skip does the whole thing
// again from that unchanged state (resumeAt is the caller's own number, not
// a delta, so a retry is the same call). A failure after the commit is not a
// failure of this function: the hole is durable and the cursor has moved, and
// a repeated Skip to the same resumeAt is the no-op below. There is no window
// in which the cursor has advanced and the gap has not, because they are one
// UPDATE.
func (s *sqliteContent) skipSessionOutput(ctx context.Context, sessionID string, at int64, reason string, capBytes int64) (SessionOutputResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SessionOutputResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	// The recording's coordinate space begins at the START OF THE STREAM and
	// not at `at`, because that is what makes the hole measurable: a
	// recording opened by a skip is missing [0,at), and anchoring it at `at`
	// instead would report a session that produced its first `at` bytes
	// invisibly. The head is still reserved where the bytes will actually
	// arrive — there is nothing before `at` for it to protect.
	row, err := loadOrCreateSessionOutput(ctx, tx, sessionID, 0, at, capBytes, now)
	if err != nil {
		return SessionOutputResult{}, err
	}

	switch {
	case at == row.next:
		// The same skip, sent again — a retry after an error the caller
		// could not classify. A zero-width gap is not a hole, and recording
		// one would turn a retry into a fiction.
		if commitErr := tx.Commit(); commitErr != nil {
			return SessionOutputResult{}, commitErr
		}
		return SessionOutputResult{Kept: true}, nil
	case at < row.next:
		// The caller HAD these bytes: the recording already covers them.
		// This is the half of the old sentence that survives.
		return SessionOutputResult{}, fmt.Errorf(
			"%w: session %s is at %d, skip resumes at %d",
			ErrSessionOutputDiscontinuous, sessionID, row.next, at)
	}

	recorded, err := sessionOutputRecordedGaps(row.gaps)
	if err != nil {
		return SessionOutputResult{}, err
	}
	recorded = append(recorded, Gap{Start: row.next, End: at, Reason: reason})
	row.next = at

	gaps, truncated, err := sessionOutputGaps(ctx, tx, sessionID, row, recorded)
	if err != nil {
		return SessionOutputResult{}, err
	}
	if _, updateErr := tx.ExecContext(ctx,
		`UPDATE session_output
		    SET next_offset = ?, truncated = ?, gaps = ?, updated_at = ?
		  WHERE session_id = ?`,
		row.next, truncated, gaps, now, sessionID); updateErr != nil {
		return SessionOutputResult{}, fmt.Errorf("content: session output: record a skipped range: %w", updateErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return SessionOutputResult{}, commitErr
	}
	// Dropped is zero and not merely unset: a skip evicts nothing. It cannot
	// — the bytes it names were never here to evict.
	return SessionOutputResult{Kept: true}, nil
}

// sessionOutputRow is the recording's own row, read at the top of an append.
type sessionOutputRow struct {
	first   int64
	next    int64
	byteLen int64
	headEnd int64
	// gaps is the row's stored gap list, carried in because half of it —
	// every entry whose reason is not `cap` — cannot be recomputed from the
	// chunks and must survive the next write.
	gaps string
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
	row, err := loadOrCreateSessionOutput(ctx, tx, in.SessionID, offset, offset, capBytes, now)
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

	// The ranges nobody recorded are carried forward: an append cannot
	// recompute them and cannot invalidate them either, since a skipped
	// range lies behind the cursor and nothing will ever be written into it.
	recorded, err := sessionOutputRecordedGaps(row.gaps)
	if err != nil {
		return SessionOutputResult{}, err
	}
	gaps, truncated, err := sessionOutputGaps(ctx, tx, in.SessionID, row, recorded)
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
// first append or the first skip. The head is reserved HERE and nowhere
// else — see the header.
//
// `first` is where the recording's coordinate space begins and `headStart`
// is where the bytes it will actually hold begin. An append passes the same
// offset for both, which is what it has always done. A skip passes 0 and the
// resume point, because the range it is about lies BEFORE anything this
// recording will ever hold, and a recording anchored past it could not
// describe it.
func loadOrCreateSessionOutput(ctx context.Context, tx *sql.Tx, sessionID string, first, headStart, capBytes, now int64) (sessionOutputRow, error) {
	var row sessionOutputRow
	err := tx.QueryRowContext(ctx,
		`SELECT first_offset, next_offset, byte_len, head_end, gaps FROM session_output WHERE session_id = ?`,
		sessionID).Scan(&row.first, &row.next, &row.byteLen, &row.headEnd, &row.gaps)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("content: session output: read recording: %w", err)
	}
	row = sessionOutputRow{first: first, next: first, headEnd: headStart + capBytes/2, gaps: "[]"}
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

// sessionOutputSpan is one half-open byte range [start,end) of the stream.
type sessionOutputSpan struct {
	start int64
	end   int64
}

// sessionOutputRecordedGaps returns the gaps a row carries that nothing can
// recompute: the ranges nobody was there to offer. Everything reasoned `cap`
// is dropped, because the chunks say where the cap's hole is and a stored
// copy could only ever drift from them.
func sessionOutputRecordedGaps(encoded string) ([]Gap, error) {
	if encoded == "" || encoded == "[]" {
		return nil, nil
	}
	var all []Gap
	if err := json.Unmarshal([]byte(encoded), &all); err != nil {
		return nil, fmt.Errorf("content: session output: decode gaps: %w", err)
	}
	out := all[:0]
	for _, g := range all {
		if g.Reason != GapReasonCap && g.End > g.Start {
			out = append(out, g)
		}
	}
	return out, nil
}

// sessionOutputHoles derives every range inside [first,next) that the
// recording does not hold, from the chunks that are actually there. Derived,
// so it cannot drift from them — which is the property that made the old
// two-aggregate version right and the reason this one replaces it rather
// than sitting beside it: that version assumed the chunks were contiguous
// apart from ONE evicted middle, and a skipped range breaks that assumption
// (head chunks either side of a skip make MAX(head end) jump straight over
// it, and the hole disappeared).
//
// Three sources of a hole, and the middle one is the only one SQLite has to
// work for: before the first chunk, between two chunks, and after the last.
func sessionOutputHoles(ctx context.Context, tx *sql.Tx, sessionID string, row sessionOutputRow) ([]sessionOutputSpan, error) {
	var firstChunk, lastEnd sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MIN(byte_offset), MAX(byte_offset + length(body))
		   FROM session_output_chunks WHERE session_id = ?`, sessionID).
		Scan(&firstChunk, &lastEnd); err != nil {
		return nil, fmt.Errorf("content: session output: measure the recording: %w", err)
	}
	if !firstChunk.Valid {
		// Nothing kept at all. Everything produced is a hole, and a
		// recording that has produced nothing has no hole either.
		if row.next > row.first {
			return []sessionOutputSpan{{start: row.first, end: row.next}}, nil
		}
		return nil, nil
	}

	var holes []sessionOutputSpan
	if firstChunk.Int64 > row.first {
		holes = append(holes, sessionOutputSpan{start: row.first, end: firstChunk.Int64})
	}
	// LAG rather than reading every chunk row back: the answer is the few
	// boundaries where one chunk does not touch the next, and at the largest
	// cap the user can set that would be five hundred rows crossing the
	// driver on every append to find them.
	rows, err := tx.QueryContext(ctx,
		`SELECT prev_end, byte_offset FROM (
		     SELECT byte_offset,
		            LAG(byte_offset + length(body)) OVER (ORDER BY byte_offset) AS prev_end
		       FROM session_output_chunks WHERE session_id = ?
		   )
		  WHERE prev_end IS NOT NULL AND byte_offset > prev_end
		  ORDER BY byte_offset`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("content: session output: find the holes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var span sessionOutputSpan
		if err := rows.Scan(&span.start, &span.end); err != nil {
			return nil, fmt.Errorf("content: session output: scan a hole: %w", err)
		}
		holes = append(holes, span)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: session output: find the holes: %w", err)
	}

	if row.next > lastEnd.Int64 {
		holes = append(holes, sessionOutputSpan{start: lastEnd.Int64, end: row.next})
	}
	return holes, nil
}

// attributeSessionOutputGaps names every hole. A range the store was told
// about is that range's reason; everything else in a hole is the cap, which
// is the only other thing that can take bytes out of a recording.
//
// A recorded range always lies wholly inside a hole — the store cannot hold
// bytes it was told it never received — so this only ever SPLITS a hole, and
// the cap keeps whatever is left either side. That is the honest reading of
// an eviction that later spans a skipped range: the cap took what it had, and
// it never had the rest.
//
// `recorded` must be in stream order and non-overlapping, which is what
// coalescing below preserves from one write to the next.
func attributeSessionOutputGaps(holes []sessionOutputSpan, recorded []Gap) []Gap {
	var out []Gap
	for _, hole := range holes {
		cursor := hole.start
		for _, g := range recorded {
			if g.End <= cursor || g.Start >= hole.end {
				continue
			}
			start, end := g.Start, g.End
			if start < cursor {
				start = cursor
			}
			if end > hole.end {
				end = hole.end
			}
			if start > cursor {
				out = append(out, Gap{Start: cursor, End: start, Reason: GapReasonCap})
			}
			out = append(out, Gap{Start: start, End: end, Reason: g.Reason})
			cursor = end
		}
		if cursor < hole.end {
			out = append(out, Gap{Start: cursor, End: hole.end, Reason: GapReasonCap})
		}
	}
	// Adjacent ranges with the SAME reason are one range. Two skips with
	// nothing appended between them are one hole nobody recorded, and
	// reporting them separately would make the stored list grow with every
	// coordinator that came and went.
	merged := out[:0]
	for _, g := range out {
		if n := len(merged); n > 0 && merged[n-1].End == g.Start && merged[n-1].Reason == g.Reason {
			merged[n-1].End = g.End
			continue
		}
		merged = append(merged, g)
	}
	return merged
}

// sessionOutputGaps is what goes into the row: the whole of what is missing,
// in stream order, each range with the reason that is true of it, plus the
// PRIMARY reason for the recording as a whole.
//
// The primary reason is `cap` whenever the bound evicted anything, because
// that is the one a user can act on — the knob that dropped those bytes is
// the knob that would have kept them — and `gap` for a recording holed only
// by ranges nobody recorded, where no bound acted at all and saying `cap`
// would name a setting that did nothing.
func sessionOutputGaps(ctx context.Context, tx *sql.Tx, sessionID string, row sessionOutputRow, recorded []Gap) (string, *string, error) {
	holes, err := sessionOutputHoles(ctx, tx, sessionID, row)
	if err != nil {
		return "", nil, err
	}
	gaps := attributeSessionOutputGaps(holes, recorded)
	if len(gaps) == 0 {
		return "[]", nil, nil
	}
	body, err := json.Marshal(gaps)
	if err != nil {
		return "", nil, err
	}
	trunc := string(TruncGap)
	for _, g := range gaps {
		if g.Reason == GapReasonCap {
			trunc = string(TruncCap)
			break
		}
	}
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
