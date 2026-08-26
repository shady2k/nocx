package content

// Retention's durable watermark and the eviction of ledger entries
// (nocx-rtg0.12), design §5.4.
//
// The design states the constraint this file exists for, and states it as an
// impossibility rather than a preference:
//
//	Search states its coverage — and coverage cannot be computed from the
//	rows that remain. Once eviction has deleted the rows, there is nothing
//	left to count.
//
// So the store keeps a watermark: a durable record of what eviction removed
// and how far its knowledge is now incomplete, written in the SAME
// transaction as the deletion. Without it, `MIN(ended_at)` over the survivors
// answers "how far back can this store see" with the horizon of whatever
// happened to be left — and answers it most confidently when eviction has
// taken the most, because an emptied table reports no horizon at all, which
// reads as "nothing was ever here".
//
// # Why the watermark is one row and not a journal of passes
//
// The spec calls it a journal. A journal of passes would grow one row per
// eviction, and eviction runs on the write path — that is a second history
// beside the one being evicted, which is the opposite of the point. The two
// questions it must answer ("how many entries has this store ever evicted"
// and "what is the oldest moment it can still speak for") are both
// accumulators, so a single accumulating row answers them in O(1) and can
// never itself need eviction. It follows `ledger_sequence`, which is the same
// shape for the same reason: one row, `CHECK (id = 1)`, seeded idempotently.
//
// # Two drivers, two sweeps, and only one of them moves the watermark
//
// Retention has two drivers: age (Policy.RetentionDays) and size
// (Budget.RetentionBytes). The age sweep landed first and completely; the
// size sweep is at the foot of this file, and the split between them is not
// a staging accident — they remove different things.
//
// The age sweep DELETES ROWS, so it must move the watermark: coverage cannot
// be computed from what is left. Its horizon is a TIME coordinate and an age
// cutoff is already one, so the two compose without a second derivation.
//
// The size sweep FREES BODIES and deletes nothing (ADR-0019 §7), so it moves
// no watermark at all: every row is still there, and what was lost is
// reported on the block that lost it. That also answers the objection this
// note used to record against size-driven eviction — that a byte budget
// would need a per-entry byte sum nothing owns. It does not: the budget's
// unit is `artifacts.byte_len`, the sweep acts on artifacts, and no second
// owner of "how big is this entry" is invented anywhere.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// evictionPassLimit bounds one pass run from the write path. Eviction shares
// the single writer goroutine with every other mutation (design §5.3), so an
// unbounded DELETE over a large backlog would stall every command behind it;
// the next write continues where this one stopped.
const evictionPassLimit = 256

// EvictionRequest is one bounded retention pass.
type EvictionRequest struct {
	// Before is the retention cutoff in Unix milliseconds: an entry is a
	// candidate when it has COMPLETED and ended strictly before this. An
	// entry that never ended is unfinished, not old, and is never a
	// candidate however long ago it was submitted.
	Before int64
	// Max bounds how many entries this pass may remove. It is what makes the
	// ingest_seq ordering load-bearing: under a cap, WHICH rows go is
	// decided by the ledger's total order.
	//
	// It counts BLOCKS, and a pass overruns it by the prose of the last run
	// it takes: a turn is removed with its `text` children or not at all
	// (ADR-0040), so the cap can stop the pass between runs and never inside
	// one.
	Max int
}

// EvictionResult is what one pass did.
type EvictionResult struct {
	// Evicted is how many entries this pass removed — every row, including
	// the runs of prose that went with the turns that held them. Prose
	// blocks are entries, so a turn of three sentences accounts for four.
	Evicted int64
	// TotalEvicted is the store's running total after this pass — the
	// watermark's count, which no query over the surviving rows can produce.
	TotalEvicted int64
	// Horizon is the store-wide horizon after this pass. Nil only when
	// nothing has ever been evicted.
	Horizon *int64
}

// RetentionWatermark is the durable answer to "what has this store lost".
// Both fields are read from the watermark row, never from the entries that
// remain — that independence is the whole reason it exists.
type RetentionWatermark struct {
	// EvictedCount is how many entries this store has EVER evicted. It is
	// monotonic and routinely larger than the number of rows the table
	// holds, which is exactly what makes it underivable from them.
	EvictedCount int64
	// Horizon is the newest instant eviction has removed, in Unix
	// milliseconds: the store's knowledge is complete only AFTER it. Nil
	// until the first eviction commits.
	//
	// It advances and never retreats. A pass whose newest victim is older
	// than the standing horizon has learned nothing — the store was already
	// incomplete that far back — and a horizon that moved backwards would
	// claim the store had recovered history it never regained.
	Horizon *int64
	// LastEvictedAt is the wall clock at the last pass that removed
	// something. Nil until then. Display and diagnosis only: it says when
	// the store last lost something, never what it can answer.
	LastEvictedAt *int64
}

// validateEvictionRequest refuses a pass that cannot mean anything, rather
// than running it and reporting that it evicted nothing — the two outcomes
// look identical to a caller and only one of them is a bug in the caller.
func validateEvictionRequest(req EvictionRequest) error {
	if req.Max < 1 {
		return fmt.Errorf("content: evict: max %d is not a bound — a pass removes at least one row or is not a pass", req.Max)
	}
	if req.Before < 0 {
		return fmt.Errorf("content: evict: before %d is not a wall clock", req.Before)
	}
	return nil
}

// EvictEntries removes the oldest entries that retention no longer covers and
// records what it removed, in ONE transaction. Serialized through the writer
// goroutine like every other mutation.
func (s *sqliteContent) EvictEntries(ctx context.Context, req EvictionRequest) (EvictionResult, error) {
	if err := validateEvictionRequest(req); err != nil {
		return EvictionResult{}, err
	}
	var out EvictionResult
	err := s.run(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.evictEntries(ctx, req)
		return err
	})
	return out, err
}

// evictEntries is the pass itself, on the pool. It runs ON the writer
// goroutine (either through EvictEntries' run, or called directly by a write
// path that is already there) and must never call back into run — that would
// deadlock the writer against itself.
func (s *sqliteContent) evictEntries(ctx context.Context, req EvictionRequest) (EvictionResult, error) {
	// BEGIN IMMEDIATE, for the reason Submit states: the write lock is taken
	// at BEGIN rather than at the first write, so a second process's writer
	// waits instead of failing an upgrade with SQLITE_BUSY_SNAPSHOT.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return EvictionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	ids, newest, err := evictionVictims(ctx, tx, req)
	if err != nil {
		return EvictionResult{}, err
	}
	if len(ids) == 0 {
		// Nothing to remove: the store lost nothing, so the watermark must
		// not move. A pass that recorded a horizon here would narrow the
		// store's stated coverage without any row having gone.
		if commitErr := tx.Commit(); commitErr != nil {
			return EvictionResult{}, commitErr
		}
		wm, wmErr := s.watermark(ctx, s.db)
		if wmErr != nil {
			return EvictionResult{}, wmErr
		}
		return EvictionResult{TotalEvicted: wm.EvictedCount, Horizon: wm.Horizon}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	// THE PROSE GOES WITH ITS RUN, AND IT GOES FIRST.
	//
	// A `text` block cannot exist without its parent: the schema's CHECK
	// requires parent_id IS NOT NULL, and parent_id is ON DELETE SET NULL
	// (ADR-0040 — a tool call whose turn was evicted is still a command that
	// ran). Deleting a turn while its prose is still there therefore asks
	// the engine to null a column a CHECK forbids to be null, and the whole
	// statement fails. Nothing about it is recoverable at the next attempt
	// either: the same victim is chosen again, so one aged turn with prose
	// under it stops age retention for the entire store — silently, because
	// this pass is best-effort and its caller only logs.
	//
	// So the children are removed in their own statement, before the blocks
	// they hang under, and the run is the unit here exactly as it is for the
	// size sweep: the prose of one run is evicted with the run or not at
	// all. This overruns Max by a run's pieces for the same reason
	// EvictBodies does — a cap may stop the pass between units and never
	// inside one.
	//
	// placeholders is "?,?,…" derived from len(ids) alone: no value reaches
	// the statement text, every id is bound.
	prose := `DELETE FROM entries WHERE kind = 'text' AND parent_id IN (` + placeholders + `)` //nolint:gosec // see above
	proseRes, err := tx.ExecContext(ctx, prose, args...)
	if err != nil {
		return EvictionResult{}, fmt.Errorf("content: evict: remove the prose of the run: %w", err)
	}
	proseRemoved, err := proseRes.RowsAffected()
	if err != nil {
		return EvictionResult{}, err
	}
	// THE SEAT GOES WITH THE PARENT. ON DELETE SET NULL nulls the
	// children's parent_id, and the schema's CHECK (parent_id IS NOT NULL
	// OR pos IS NULL) then refuses the surviving child, which still holds
	// its seat. The seat is derived from what is stored (AddCause) —
	// a row with no parent has no place in any ordering — so the children
	// of the victims are detached in their own UPDATE, before the DELETE.
	detach := `UPDATE entries SET pos = NULL WHERE parent_id IN (` + placeholders + `)` //nolint:gosec // see above
	if _, detErr := tx.ExecContext(ctx, detach, args...); detErr != nil {
		return EvictionResult{}, fmt.Errorf("content: evict: detach the children of the run: %w", detErr)
	}

	// Edges, executions, artifacts, chunks and grants cascade from the
	// entry (schema question 5) — the DELETE is the whole removal.
	del := `DELETE FROM entries WHERE id IN (` + placeholders + `)` //nolint:gosec // see above
	res, err := tx.ExecContext(ctx, del, args...)
	if err != nil {
		return EvictionResult{}, err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return EvictionResult{}, err
	}
	removed += proseRemoved

	// The watermark moves in the SAME transaction. max() keeps the horizon
	// monotonic; COALESCE makes the first pass, where the column is still
	// NULL, take this pass's value rather than propagating NULL through the
	// comparison.
	var total int64
	var horizon *int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE retention_watermark
		    SET evicted_count   = evicted_count + ?,
		        horizon         = max(COALESCE(horizon, ?), ?),
		        last_evicted_at = ?
		  WHERE id = 1
		RETURNING evicted_count, horizon`,
		removed, newest, newest, time.Now().UnixMilli(),
	).Scan(&total, &horizon); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EvictionResult{}, errors.New("content: evict: the retention watermark row is missing")
		}
		return EvictionResult{}, fmt.Errorf("content: evict: record watermark: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return EvictionResult{}, err
	}
	return EvictionResult{Evicted: removed, TotalEvicted: total, Horizon: horizon}, nil
}

// evictionVictims chooses this pass's rows and reports the newest instant
// among them.
//
// The order is ingest_seq — the ledger's only total order (ADR-0019 §2) —
// and never ended_at. Two entries can carry the same wall clock, and the
// clock can move backwards; commit order cannot. Under a cap this is what
// decides which rows go, so it is load-bearing rather than decorative.
//
// The horizon is the newest ended_at actually SELECTED, not the requested
// cutoff. When a cap stops the pass early the store remains complete from
// somewhere before the cutoff, and claiming the cutoff would assert coverage
// it does not have. It is computed here, over the rows about to be deleted,
// because a moment later they are gone — which is the whole difficulty this
// file addresses, in miniature.
func evictionVictims(ctx context.Context, tx *sql.Tx, req EvictionRequest) ([]string, int64, error) {
	// A pinned artifact exempts its entry from BACKGROUND eviction (schema
	// question 4): a capsule whose content can be evicted underneath it is a
	// broken promise. A pin protects against this, never against an explicit
	// DeleteEntry.
	//
	// The exemption reaches into the RUN, because the deletion does: a turn
	// takes its prose with it (see evictEntries), so a pin on any piece of
	// that prose has to exempt the turn or the pin is worth nothing. Same
	// rule as the size sweep's, and for the same reason.
	//
	// And a `text` block is never a victim in its own right. Prose is
	// evicted with the run that wrote it or not at all — a pass that picked
	// pieces out of a turn would leave a complete-looking answer with its
	// middle missing, which is the outcome both sweeps exist to prevent.
	rows, err := tx.QueryContext(ctx,
		`SELECT e.id, e.ended_at
		   FROM entries e
		  WHERE e.ended_at IS NOT NULL
		    AND e.ended_at < ?
		    AND e.kind <> 'text'
		    AND NOT EXISTS (
		          SELECT 1
		            FROM artifacts a
		           WHERE a.entry_id = e.id AND a.pinned = 1)
		    AND NOT EXISTS (
		          SELECT 1
		            FROM entries c
		            JOIN artifacts a ON a.entry_id = c.id
		           WHERE c.parent_id = e.id AND c.kind = 'text' AND a.pinned = 1)
		  ORDER BY e.ingest_seq
		  LIMIT ?`, req.Before, req.Max)
	if err != nil {
		return nil, 0, fmt.Errorf("content: evict: select victims: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	var newest int64
	for rows.Next() {
		var id string
		var endedAt int64
		if err := rows.Scan(&id, &endedAt); err != nil {
			return nil, 0, fmt.Errorf("content: evict: select victims: %w", err)
		}
		if len(ids) == 0 || endedAt > newest {
			newest = endedAt
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("content: evict: select victims: %w", err)
	}
	return ids, newest, nil
}

// Watermark reports what this store has lost. It is a plain read — no writer
// turn — because it answers from the watermark row alone.
func (s *sqliteContent) Watermark(ctx context.Context) (RetentionWatermark, error) {
	return s.watermark(ctx, s.db)
}

// watermark reads the one row through rowQuerier — the seam this package
// already has for "something rows can be read through" — so the query path
// can read it inside its own transaction and see a horizon consistent with
// the page it is answering.
func (s *sqliteContent) watermark(ctx context.Context, q rowQuerier) (RetentionWatermark, error) {
	var wm RetentionWatermark
	if err := q.QueryRowContext(ctx,
		`SELECT evicted_count, horizon, last_evicted_at FROM retention_watermark WHERE id = 1`,
	).Scan(&wm.EvictedCount, &wm.Horizon, &wm.LastEvictedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RetentionWatermark{}, errors.New("content: watermark: the retention watermark row is missing")
		}
		return RetentionWatermark{}, fmt.Errorf("content: watermark: %w", err)
	}
	return wm, nil
}

// evictOnWrite is retention on the write path: both sweeps, from a caller
// that is already on the writer goroutine.
//
// They are two passes and not one because they answer to two different
// limits — an age the user set and a size budget the store always has — and
// because they remove different things: the age pass deletes rows, the size
// pass frees bodies and deletes nothing. Running them together here is what
// keeps that one moment, the moment the ledger is known to have grown, the
// only place either of them is triggered from.
func (s *sqliteContent) evictOnWrite(ctx context.Context) {
	s.evictAgedOnWrite(ctx)
	s.evictBodiesOnWrite(ctx)
}

// evictAgedOnWrite runs one bounded AGE pass.
//
// Best-effort, exactly like the command-history sweep beside it and for the
// same reason: the entry it follows is already committed and durable, so an
// eviction failure must not turn a successful write into an error a caller
// would retry — the retry would be a second submit of the same intent. The
// degrade is a warning, and it is safe to be quiet about because the
// watermark is only ever written WITH a deletion: a pass that failed left the
// store's stated coverage exactly as truthful as it was before.
func (s *sqliteContent) evictAgedOnWrite(ctx context.Context) {
	days := s.policy.RetentionDays()
	if days <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	if cutoff < 0 {
		return
	}
	if _, err := s.evictEntries(ctx, EvictionRequest{Before: cutoff, Max: evictionPassLimit}); err != nil {
		s.log.Warn("ledger retention eviction failed", "error", err)
	}
}

// ── the size-driven sweep: bodies go, blocks stay ────────────────────────
//
// The header above says age-based eviction landed first and that size-driven
// eviction "reuses everything below, needing only a different way to choose
// the victim set". This is that different way, and it does NOT reuse one
// thing: it never deletes a row. ADR-0019 §7 evicts BODIES and leaves the
// entries, so the block, its artifact row and everything the row says about
// the capture survive; what goes is the chunks and the bytes the budget
// accounts for.
//
// # Why the unit is the RUN and not the artifact
//
// ADR-0040 turned an assistant turn's answer from one artifact into several:
// each run of prose between two tool calls is its own `text` block with its
// own body. A sweep deciding per artifact would take pieces 1, 3 and 7 of a
// turn and leave the rest, and what a reader then meets is a COMPLETE-LOOKING
// answer with its middle missing — strictly worse than an answer that is
// plainly gone, because nothing on screen says a sentence was removed.
//
// So the prose of one run is retained or evicted as a unit. Not per artifact,
// not per block: per run. Everything else is its own unit, which is what
// keeps §7 intact around it — a command's terminal body evicts independently
// of the prose beside it, and a turn whose prose has gone keeps every block
// it had.
//
// # Why the watermark does not move
//
// The watermark answers "what rows has this store lost, and how far back can
// it still speak for them". A stripped body loses no row: the block is there,
// its place in the tree is there, when it ran and what it was are there. What
// is gone is one body, and that loss is reported where a reader meets it —
// on the block itself (Artifact.Evicted) and, for prose, once on the run
// (LedgerEntry.ProseEvicted). Moving the horizon here would tell a user the
// store cannot speak for a period it can speak for completely.

// bodyEvictionPassLimit bounds one size pass run from the write path, in
// UNITS. It is separate from evictionPassLimit above because the two count
// different things — rows there, bodies here — and a shared constant would
// be one number pretending to two meanings.
const bodyEvictionPassLimit = 64

// BodyEvictionRequest is one bounded pass of the size-driven sweep.
type BodyEvictionRequest struct {
	// KeepBytes is the retained-content budget this pass sweeps down to:
	// Budget.RetentionBytes, the number the user reasons about (budget.go).
	// Zero is legitimate and means keep nothing.
	KeepBytes int64
	// Max bounds how many bodies this pass may free. It is measured BETWEEN
	// units: a pass overruns it to finish the run it is inside rather than
	// take half of one, because half a run is the defect this sweep exists
	// to prevent.
	Max int
}

// BodyEvictionResult is what one pass did.
type BodyEvictionResult struct {
	// Bodies is how many bodies this pass freed.
	Bodies int64
	// BytesFreed is what they were worth to the budget.
	BytesFreed int64
	// RetainedBytes is the store's logical retained content AFTER the pass —
	// the number the budget is a limit on.
	RetainedBytes int64
}

// evictionUnit is one all-or-nothing group of bodies: a run of prose with
// every piece of it, or a single body of anything else.
type evictionUnit struct {
	key    string // 'run:<turn id>' or 'body:<artifact id>'
	oldest int64  // MIN(ingest_seq) over the blocks holding it
	bytes  int64
	bodies int64
}

// validateBodyEvictionRequest refuses a pass that cannot mean anything,
// rather than running it and reporting that it freed nothing — the two
// outcomes look identical to a caller and only one of them is a bug in the
// caller. It is the same rule validateEvictionRequest states for the age
// pass, and the same reason.
func validateBodyEvictionRequest(req BodyEvictionRequest) error {
	if req.Max < 1 {
		return fmt.Errorf("content: evict bodies: max %d is not a bound — a pass frees at least one body or is not a pass", req.Max)
	}
	if req.KeepBytes < 0 {
		return fmt.Errorf("content: evict bodies: keep %d is not a budget", req.KeepBytes)
	}
	return nil
}

// EvictBodies frees the oldest bodies the retention budget no longer covers,
// in ONE transaction. Serialized through the writer goroutine like every
// other mutation.
func (s *sqliteContent) EvictBodies(ctx context.Context, req BodyEvictionRequest) (BodyEvictionResult, error) {
	if err := validateBodyEvictionRequest(req); err != nil {
		return BodyEvictionResult{}, err
	}
	var out BodyEvictionResult
	err := s.run(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.evictBodies(ctx, req)
		return err
	})
	return out, err
}

// evictBodies is the pass itself, on the pool. Like evictEntries it runs ON
// the writer goroutine and must never call back into run.
func (s *sqliteContent) evictBodies(ctx context.Context, req BodyEvictionRequest) (BodyEvictionResult, error) {
	// BEGIN IMMEDIATE, for the reason Submit and evictEntries both state.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return BodyEvictionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var retained int64
	if err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(byte_len), 0) FROM artifacts`).Scan(&retained); err != nil {
		return BodyEvictionResult{}, fmt.Errorf("content: evict bodies: measure what is retained: %w", err)
	}
	need := retained - req.KeepBytes
	if need <= 0 {
		// Inside the budget: there is nothing to free, and a pass that
		// stripped a body here would be data loss rather than housekeeping.
		if err = tx.Commit(); err != nil {
			return BodyEvictionResult{}, err
		}
		return BodyEvictionResult{RetainedBytes: retained}, nil
	}

	units, err := evictionUnits(ctx, tx, req.Max)
	if err != nil {
		return BodyEvictionResult{}, err
	}
	var chosen []string
	var freed, bodies int64
	for _, u := range units {
		chosen = append(chosen, u.key)
		freed += u.bytes
		bodies += u.bodies
		// Both bounds are read HERE, between units, which is what makes a
		// run indivisible: whichever stops the pass, it stops it after a
		// whole unit.
		if freed >= need || bodies >= int64(req.Max) {
			break
		}
	}
	if len(chosen) == 0 {
		// Over budget with nothing eligible — every remaining body is
		// pinned, or belongs to a block that has not closed. That is not an
		// error: the store is honestly unable to shrink, and says so by
		// reporting what it still retains.
		if err = tx.Commit(); err != nil {
			return BodyEvictionResult{}, err
		}
		return BodyEvictionResult{RetainedBytes: retained}, nil
	}

	victims, err := unitBodies(ctx, tx, chosen)
	if err != nil {
		return BodyEvictionResult{}, err
	}
	now := time.Now().UnixMilli()
	var actuallyFreed, actuallyBodies int64
	for _, v := range victims {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM artifact_chunks WHERE artifact_id = ?`, v.id); err != nil {
			return BodyEvictionResult{}, fmt.Errorf("content: evict bodies: free chunks: %w", err)
		}
		// The receipt and the freeing are ONE transaction, for the reason
		// the watermark and the DELETE are in evictEntries: a body freed
		// with nothing recording it comes back as an empty body, which reads
		// as a command that printed nothing — a lie the reader cannot
		// detect. byte_len goes to zero because the budget's unit is logical
		// content bytes and there are none left; the size that went is kept
		// in the receipt, where it is a fact about the past rather than a
		// claim about what the store holds.
		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts
			    SET byte_len = 0,
			        state    = 'sealed',
			        payload  = json_set(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,
			                            '$.evicted', json_object('at', ?, 'bytes', ?))
			  WHERE id = ?`, now, v.bytes, v.id); err != nil {
			return BodyEvictionResult{}, fmt.Errorf("content: evict bodies: record the receipt: %w", err)
		}
		actuallyFreed += v.bytes
		actuallyBodies++
	}
	if err := tx.Commit(); err != nil {
		return BodyEvictionResult{}, err
	}
	return BodyEvictionResult{
		Bodies:        actuallyBodies,
		BytesFreed:    actuallyFreed,
		RetainedBytes: retained - actuallyFreed,
	}, nil
}

// unitKeyExpr is the ONE derivation of "which all-or-nothing group is this
// body in", written once and used by both statements below so the grouping
// and the membership cannot drift into two answers. A `text` body's group is
// its RUN — the turn it hangs under — and everything else is its own.
const unitKeyExpr = `CASE WHEN e.kind = 'text' THEN 'run:' || e.parent_id ELSE 'body:' || a.id END`

// evictionUnits lists the oldest units the sweep may take, in the order it
// takes them.
//
// The order is the owning block's ingest_seq — the ledger's only total order
// (ADR-0019 §2), the same one evictEntries walks — and never a wall clock. A
// run is ordered by its OLDEST piece, so a turn is taken as of when it began
// rather than as of whichever sentence happened to be written last.
//
// Two exemptions, both of them refusals to take a unit at all rather than
// permission to take part of it:
//
//   - PINNED. A pin on any piece exempts the whole run, because a run that
//     cannot be evicted whole must not be evicted at all: a pinned sentence
//     surrounded by holes is the complete-looking answer with its middle
//     missing, in the one place a user asked for the opposite.
//   - NOT CLOSED. A body belongs to a block that is still running — a turn
//     mid-stream, a command still printing — and freeing it would tear the
//     answer out from under the deltas still arriving. For prose the block
//     that must have closed is the RUN, because the run is the unit; a
//     `text` block is born closed (ADR-0040) and so cannot speak for itself
//     here. It is the same rule the age pass states as "an entry that never
//     ended is unfinished, not old".
//
// LIMIT bounds the pass at Max UNITS. Each unit holds at least one body, so
// Max units is at least Max bodies: the cap can only be overrun by the run
// the pass is finishing, never by an unbounded backlog.
func evictionUnits(ctx context.Context, tx *sql.Tx, max int) ([]evictionUnit, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+unitKeyExpr+` AS unit,
		        MIN(e.ingest_seq)                                AS oldest,
		        SUM(a.byte_len)                                  AS bytes,
		        SUM(CASE WHEN a.byte_len > 0 THEN 1 ELSE 0 END)  AS bodies
		   FROM artifacts a
		   JOIN entries e ON e.id = a.entry_id
		   LEFT JOIN entries p ON p.id = e.parent_id
		  WHERE (CASE WHEN e.kind = 'text' THEN p.phase ELSE e.phase END) = 'closed'
		  GROUP BY unit
		 HAVING MAX(a.pinned) = 0 AND SUM(a.byte_len) > 0
		  ORDER BY oldest
		  LIMIT ?`, max)
	if err != nil {
		return nil, fmt.Errorf("content: evict bodies: select units: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []evictionUnit
	for rows.Next() {
		var u evictionUnit
		if err := rows.Scan(&u.key, &u.oldest, &u.bytes, &u.bodies); err != nil {
			return nil, fmt.Errorf("content: evict bodies: select units: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: evict bodies: select units: %w", err)
	}
	return out, nil
}

// The two halves of the membership statement, split only so the one line
// that concatenates the bound placeholders is a line an annotation fits on —
// the shape evictEntries' DELETE already has.
const (
	unitBodiesHead = `SELECT a.id, a.byte_len
	                    FROM artifacts a
	                    JOIN entries e ON e.id = a.entry_id
	                   WHERE a.byte_len > 0 AND (` + unitKeyExpr + `) IN (`
	unitBodiesTail = `) ORDER BY e.ingest_seq, a.id`
)

// victimBody is one body about to be freed and what it was worth.
type victimBody struct {
	id    string
	bytes int64
}

// unitBodies resolves the chosen units into the bodies they are made of.
// This is where "as a unit" becomes true rather than intended: the
// membership is derived from the same expression the grouping used, so a
// unit's pieces cannot be enumerated one way and grouped another.
func unitBodies(ctx context.Context, tx *sql.Tx, units []string) ([]victimBody, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(units)), ",")
	args := make([]any, 0, len(units))
	for _, u := range units {
		args = append(args, u)
	}
	// placeholders is "?,?,…" derived from len(units) alone, and unitKeyExpr
	// is a package constant: no value reaches the statement text, every key
	// is bound.
	q := unitBodiesHead + placeholders + unitBodiesTail //nolint:gosec // see above
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("content: evict bodies: select bodies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []victimBody
	for rows.Next() {
		var v victimBody
		if err := rows.Scan(&v.id, &v.bytes); err != nil {
			return nil, fmt.Errorf("content: evict bodies: select bodies: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: evict bodies: select bodies: %w", err)
	}
	return out, nil
}

// bodyEvictedExpr is the ONE reading of the receipt evictBodies writes: the
// read side of "retention took this body", derived from the stored fact
// rather than kept as a second copy of it. alias is the artifacts alias in
// the statement it is spliced into.
//
// json_valid guards the CASE because payload is an artifact's sparse
// extension and a caller may put anything in it; json_extract over
// non-JSON would fail the whole read for a body that is plainly not evicted.
func bodyEvictedExpr(alias string) string {
	return `(CASE WHEN json_valid(` + alias + `.payload)
	              THEN json_extract(` + alias + `.payload, '$.evicted.at') IS NOT NULL
	              ELSE 0 END)`
}

// evictBodiesOnWrite runs one bounded size pass from a write path that is
// already on the writer goroutine.
//
// Best-effort for the reason evictOnWrite gives, and safe to be quiet about
// for a stronger one than the age pass has: this sweep writes no watermark,
// so a pass that failed left every stated fact exactly as true as it was.
//
// The budget is the store's own (Budget.RetentionBytes, refused at Open if it
// is not positive), so unlike the age limit there is no "switched off" state
// to check for: every store has a ceiling, and being under it is the ordinary
// case the pass reports by freeing nothing.
func (s *sqliteContent) evictBodiesOnWrite(ctx context.Context) {
	if s.cfg.Budget.RetentionBytes <= 0 {
		return
	}
	if _, err := s.evictBodies(ctx, BodyEvictionRequest{
		KeepBytes: s.cfg.Budget.RetentionBytes, Max: bodyEvictionPassLimit,
	}); err != nil {
		s.log.Warn("ledger retention body eviction failed", "error", err)
	}
}
