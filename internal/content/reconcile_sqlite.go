package content

// The SQLite half of restart reconciliation (nocx-k6p18.5). Read reconcile.go
// first: it carries the decision, this file carries the statements.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// pendingSession is one carried-over session as this incarnation holds it. The
// durable half (id, start, bytes, open blocks) is read once at `Open`; the
// cause is the only field a verdict writes, and it is memory-only because it
// describes THIS incarnation's attempts and means nothing to the next one.
type pendingSession struct {
	id         string
	host       string
	account    string
	generation string
	sinceMs    int64
	bytes      uint64
	openRows   int
	cause      UnreconciledCause
	detail     string
}

// carryOver reads the set of sessions a previous incarnation left behind. It
// runs on `Open`'s creation connection, after the schema and before the store
// is handed out, which is what makes it exact: nothing this incarnation writes
// exists yet, so every row it sees is a previous one's.
//
// The set is the UNION of two tables, because the two can legitimately
// disagree. A `sessions` row without a recording is a session that printed
// nothing; a recording without a `sessions` row is every recording written
// today, since the row needs a workspace and nothing mints one yet
// (ws_ledger.go says so in as many words). Judging only one of them would
// leave the other unbounded.
func carryOver(ctx context.Context, conn *sql.Conn) (map[string]*pendingSession, error) {
	out := map[string]*pendingSession{}

	rows, err := conn.QueryContext(ctx, `SELECT id, started_at, payload FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("content: carry-over sessions: %w", err)
	}
	for rows.Next() {
		var id, payload string
		var started int64
		if scanErr := rows.Scan(&id, &started, &payload); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("content: carry-over sessions: %w", scanErr)
		}
		var metadata struct {
			Generation string `json:"generation"`
			Host       string `json:"host"`
			Account    string `json:"account"`
		}
		if decodeErr := json.Unmarshal([]byte(payload), &metadata); decodeErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("content: carry-over session metadata: %w", decodeErr)
		}
		out[id] = &pendingSession{
			id: id, host: metadata.Host, account: metadata.Account,
			generation: metadata.Generation, sinceMs: started, cause: CauseNotYetAsked,
		}
	}
	if closeErr := errors.Join(rows.Err(), rows.Close()); closeErr != nil {
		return nil, fmt.Errorf("content: carry-over sessions: %w", closeErr)
	}

	recs, err := conn.QueryContext(ctx, `SELECT session_id, started_at, byte_len FROM session_output`)
	if err != nil {
		return nil, fmt.Errorf("content: carry-over recordings: %w", err)
	}
	for recs.Next() {
		var id string
		var started, byteLen int64
		if scanErr := recs.Scan(&id, &started, &byteLen); scanErr != nil {
			_ = recs.Close()
			return nil, fmt.Errorf("content: carry-over recordings: %w", scanErr)
		}
		p, ok := out[id]
		if !ok {
			p = &pendingSession{id: id, sinceMs: started, cause: CauseNotYetAsked}
			out[id] = p
		}
		if byteLen > 0 {
			p.bytes = uint64(byteLen) //nolint:gosec // byte_len is a length, never negative
		}
	}
	if closeErr := errors.Join(recs.Err(), recs.Close()); closeErr != nil {
		return nil, fmt.Errorf("content: carry-over recordings: %w", closeErr)
	}

	// How many blocks are hanging on each answer. Counted here rather than
	// per read: it is a fact about the carried-over state, and it stops
	// changing the moment reconciliation starts closing entries.
	open, err := conn.QueryContext(ctx,
		`SELECT session_id, COUNT(*) FROM entries
		  WHERE phase != 'closed' AND session_id IS NOT NULL GROUP BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("content: carry-over open entries: %w", err)
	}
	for open.Next() {
		var id string
		var n int
		if scanErr := open.Scan(&id, &n); scanErr != nil {
			_ = open.Close()
			return nil, fmt.Errorf("content: carry-over open entries: %w", scanErr)
		}
		if p, ok := out[id]; ok {
			p.openRows = n
		}
	}
	if closeErr := errors.Join(open.Err(), open.Close()); closeErr != nil {
		return nil, fmt.Errorf("content: carry-over open entries: %w", closeErr)
	}
	return out, nil
}

// Reconcile returns the store's own reconciler. One owner, and it is the store
// itself: the carried-over set and the rows it names are the same fact.
func (s *sqliteContent) Reconcile() SessionReconciler { return s }

func (s *sqliteContent) Pending(_ context.Context) ([]PendingSession, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	s.pendingMu.Lock()
	out := make([]PendingSession, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, PendingSession{
			SessionID:     p.id,
			Host:          p.host,
			Account:       p.account,
			Generation:    p.generation,
			Since:         time.UnixMilli(p.sinceMs),
			Cause:         p.cause,
			Detail:        p.detail,
			OpenEntries:   p.openRows,
			RecordedBytes: p.bytes,
		})
	}
	s.pendingMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since.Equal(out[j].Since) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out, nil
}

func (s *sqliteContent) Apply(ctx context.Context, j SessionJudgement) error {
	s.pendingMu.Lock()
	p, ok := s.pending[j.SessionID]
	s.pendingMu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotPending, j.SessionID)
	}

	switch j.Verdict {
	case VerdictUnknown:
		// Nothing is written and nothing is judged. The cause is recorded so
		// the product can say why, and since when.
		s.pendingMu.Lock()
		p.cause = j.Cause
		if p.cause == "" {
			p.cause = CauseNotYetAsked
		}
		p.detail = j.Detail
		s.pendingMu.Unlock()
		return nil
	case VerdictLive:
		// The mark is cleared and NOTHING else happens: the row stays, its
		// entries keep their session_id, its recording keeps its bytes, and
		// its open entry stays open because the command is still running.
		s.forget(j.SessionID)
		return nil
	case VerdictAbsent:
		if err := s.run(ctx, func(ctx context.Context) error {
			return sweepAbsentSession(ctx, s.db, j.SessionID)
		}); err != nil {
			// The session stays pending, so the next pass repeats exactly
			// what this one could not finish. A verdict that failed to land
			// is not a verdict that was reached.
			return err
		}
		s.forget(j.SessionID)
		if s.log != nil {
			s.log.Info("content: a host reported this session gone; its recording and provenance are ended",
				"session", j.SessionID)
		}
		return nil
	default:
		return fmt.Errorf("content: unknown session verdict %q", j.Verdict)
	}
}

func (s *sqliteContent) forget(id string) {
	s.pendingMu.Lock()
	delete(s.pending, id)
	s.pendingMu.Unlock()
}

// sweepAbsentSession is what `Open` used to do to every session, applied to
// ONE, in ONE transaction.
//
// The transaction boundary is `closeOpenEntries`'s own reason for being one,
// kept: the interval it guards has one closing event — the session that ended
// also interrupted the run and closed the entry that asked — and a reader
// between two commits would see an inconsistency that never existed in any
// running process.
//
// `pane_id` appears nowhere in it. The anchor is untouched: a block whose
// session is gone is still a command that ran, in the pane it ran in.
func sweepAbsentSession(ctx context.Context, db *sql.DB, sessionID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: reconcile absent session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE entries SET phase = 'closed', status =
		   CASE WHEN kind = 'ask' THEN 'interrupted' ELSE 'unknown' END
		 WHERE phase != 'closed' AND session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("content: reconcile absent session: close entries: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE executions SET state = 'interrupted', termination_reason = 'interrupted', ended_at = ?
		  WHERE state IS NOT NULL AND state NOT IN ('completed','cancelled','failed','interrupted')
		    AND entry_id IN (SELECT id FROM entries WHERE session_id = ?)`,
		time.Now().UnixMilli(), sessionID); err != nil {
		return fmt.Errorf("content: reconcile absent session: interrupt runs: %w", err)
	}
	// The recording goes before the row, because deleting the row is what
	// nulls the provenance the recording is keyed against — order the two the
	// other way and a crash between them leaves a recording nothing names.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_output WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("content: reconcile absent session: drop recording: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("content: reconcile absent session: drop session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: reconcile absent session: %w", err)
	}
	return nil
}

func (s *sqliteContent) SweepStale(ctx context.Context, age time.Duration) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	cutoff := time.Now().Add(-age).UnixMilli()

	s.pendingMu.Lock()
	stale := make([]string, 0, len(s.pending))
	for id, p := range s.pending {
		if p.sinceMs <= cutoff {
			stale = append(stale, id)
		}
	}
	s.pendingMu.Unlock()
	sort.Strings(stale)

	swept := 0
	for _, id := range stale {
		if err := s.run(ctx, func(ctx context.Context) error {
			return boundStaleRecording(ctx, s.db, id)
		}); err != nil {
			return swept, err
		}
		// It stays out of the pending set for this incarnation, and it does
		// not come back on the next start either: its row and its recording
		// are what put it there. The blocks that named it are now
		// session-less, and `Open` closes those.
		s.forget(id)
		swept++
	}
	if swept > 0 && s.log != nil {
		s.log.Info("content: recordings of hosts that never became reachable were bounded by age",
			"sessions", swept, "age", age.String())
	}
	return swept, nil
}

// boundStaleRecording removes what the age bound owns and nothing else: the
// recording and the session row. It does NOT close the session's entries and
// does not judge it absent — the product's statement is that the host was
// never reachable again, not that the session ended.
func boundStaleRecording(ctx context.Context, db *sql.DB, sessionID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: bound stale recording: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_output WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("content: bound stale recording: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("content: bound stale recording: drop session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: bound stale recording: %w", err)
	}
	return nil
}

// markUnreconciled stamps the third state onto a page of rows: an entry whose
// session is still awaiting a verdict is neither running nor finished, and the
// cause says why nobody could be asked.
//
// It is a read-time derivation and not a column. The pending set belongs to
// THIS incarnation — its attempts, its causes — so writing it onto the row
// would make the next start inherit an answer it must reach for itself. This
// is also the one place a surface can learn the fact: `Open` no longer forces
// these rows closed, so without it a restored block would render as RUNNING,
// which is the same lie as `closed/unknown` told from the other end.
func (s *sqliteContent) markUnreconciled(rows []LedgerEntrySummary) {
	for i := range rows {
		rows[i].Unreconciled = s.unreconciledCause(rows[i].SessionID)
	}
}

// unreconciledCause is the derivation itself, for ONE row, and it is where
// both readers of the third state come: the page (markUnreconciled) and the
// detail read (Entry). Two surfaces answering "is this row awaiting a verdict"
// from two derivations would agree everywhere anyone looked and disagree the
// first time either changed — which is the defect AD-8 states as a habit.
func (s *sqliteContent) unreconciledCause(sessionID *string) *UnreconciledCause {
	if sessionID == nil {
		return nil
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p, ok := s.pending[*sessionID]
	if !ok {
		return nil
	}
	cause := p.cause
	return &cause
}
