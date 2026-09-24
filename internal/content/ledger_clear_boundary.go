package content

// A clear boundary — the SQLite half of nocx-2v80t.3.17. The WHY is on
// [RecordClearBoundary] and [ClearBoundaryRecorded] in ledger.go; this file
// is the mechanism, and it deliberately walks the same path OpenBlockOutput
// does: resolve the entry's own coordinates inside the write transaction,
// then commit one row.
//
// # Why the cursor excludes the command that is still running
//
// The erase this records happens INSIDE an authenticated interval — almost
// always the `clear` command's own — and that command's entry already
// exists by the time its output runs (the entry opens at the authenticated
// start, this sighting arrives after it). Taking "this pane's newest
// ingest_seq" as the boundary would therefore include that very entry, and a
// boundary that hides the command reporting the clear is not what "every
// block BEFORE it" asked for. Scoping to `phase = 'closed'` is what excludes
// it without needing this file to know which entry is "the current one":
// a running command's entry is never phase='closed' until its own
// completion, and by the rule ADR-0074 already enforces, that completion
// cannot itself be resolved before the erase inside it is sighted — so the
// running entry is provably excluded, structurally, not by an id comparison.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *sqliteContent) RecordClearBoundary(ctx context.Context, in RecordClearBoundary) (ClearBoundaryRecorded, error) {
	if in.SessionID == "" {
		return ClearBoundaryRecorded{}, errors.New("content: clear boundary: session id is required")
	}
	id := mintID()
	var out ClearBoundaryRecorded
	err := s.run(ctx, func(ctx context.Context) error {
		tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if txErr != nil {
			return txErr
		}
		defer func() { _ = tx.Rollback() }()

		// The pane this boundary bounds is resolved from the session's own
		// newest entry — the same edge every block already carries (design
		// §6.1) — because a boundary recorded against the session alone
		// could never be found again from the durable side restore reads
		// through (entries are read by pane_id, never by session_id). A
		// session with no entry at all (an erase before any command ever
		// ran in it) resolves no pane, and the boundary is still recorded,
		// for provenance, with nothing yet to apply it against.
		var paneID sql.NullString
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT pane_id FROM entries WHERE session_id = ? ORDER BY ingest_seq DESC LIMIT 1`,
			in.SessionID).Scan(&paneID)
		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			// No pane to resolve; ingestSeq stays 0 and the insert below
			// carries a NULL pane_id.
		case lookupErr != nil:
			return lookupErr
		}

		var ingestSeq int64
		if paneID.Valid {
			// Only SEALED entries count — see the file header's argument for
			// why the command whose own output triggered this sighting must
			// never be the entry the boundary bounds.
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(ingest_seq), 0) FROM entries WHERE pane_id = ? AND phase = 'closed'`,
				paneID.String).Scan(&ingestSeq); err != nil {
				return err
			}
		}

		// sql.NullString implements driver.Valuer, so it binds directly as
		// NULL or the string without this call branching on Valid itself.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO clear_boundaries (id, pane_id, session_id, ingest_seq, kind, created_at)
			 VALUES (?, ?, ?, ?, 'clear', ?)`,
			id, paneID, in.SessionID, ingestSeq, time.Now().UnixMilli(),
		); err != nil {
			return err
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
		out = ClearBoundaryRecorded{ID: id, PaneID: nullableString(paneID), IngestSeq: ingestSeq}
		return nil
	})
	if err != nil {
		return ClearBoundaryRecorded{}, err
	}
	return out, nil
}
