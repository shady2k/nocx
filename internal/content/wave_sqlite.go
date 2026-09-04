package content

// The durable half of the wave record (nocx-dkawo.2).
//
// internal/wave owns the SEMANTICS — the interval, the order that is the
// rollback, the reduction of two independent facts into one state — and owns
// no rows. The rows are here because ADR-0043 puts one connection on the
// encrypted store and AD-8 puts one owner on a behaviour: a second database
// beside content.db would be a second owner of "what nocx durably knows",
// with its own key lifecycle, its own budget and its own restart story.
//
// The direction of the dependency is deliberate. content imports wave for its
// vocabulary, exactly as it imports lineage; wave imports nothing from here,
// so the semantics can be tested against a double and the rows can be tested
// against a real file, and neither test needs the other half to be right.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/wave"
)

var _ wave.Store = (*sqliteContent)(nil)

// Waves returns the store as the wave record's durable half. It exists so the
// composition root names a SEAM rather than a concrete store, which is what
// lets the registrar be constructed against a double.
func (s *sqliteContent) Waves() wave.Store { return s }

func (s *sqliteContent) EnsureWave(ctx context.Context, id wave.ID, coordinatorSession string) error {
	return s.run(ctx, func(ctx context.Context) error {
		// DO NOTHING and not an upsert: the coordinator session of a wave is
		// its identity, and a second call reassigning it would move every
		// participant to a controller that never spawned them.
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO waves (id, coordinator_session, created_at) VALUES (?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			string(id), coordinatorSession, time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("content: ensure wave %q: %w", id, err)
		}
		return nil
	})
}

// CommitPrepared writes the participant and its membership in ONE transaction.
// It is the opening end of the interval and it happens before the fork, for
// the reason the vault journal is written before the provider call: a spawn
// that times out may still have forked, and a fork nobody recorded is
// permanently undiscoverable.
func (s *sqliteContent) CommitPrepared(ctx context.Context, p wave.Participant) error {
	return s.run(ctx, func(ctx context.Context) error {
		epoch, err := epochToSQL(p.Liveness.Epoch)
		if err != nil {
			return fmt.Errorf("content: commit participant %q: %w", p.ID, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("content: commit participant: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wave_participants
			   (id, wave_id, role, state, task, registered_at,
			    backend_instance, session_id, epoch, lane, attempt, output_offset)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(p.ID), string(p.Wave), string(p.Role), string(p.State), p.Task,
			p.RegisteredAt.UnixMilli(),
			p.Liveness.BackendInstance, p.Liveness.SessionID, epoch,
			p.Liveness.Lane, p.Liveness.Attempt, p.Liveness.OutputOffset,
		); err != nil {
			return fmt.Errorf("content: commit participant %q: %w", p.ID, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("content: commit participant %q: %w", p.ID, err)
		}
		return nil
	})
}

// MarkLive is called only on the strength of an enrolment that arrived, and it
// writes the incarnation that enrolment came in on. The WHERE clause names the
// state it expects: a participant that is no longer prepared has been
// terminalized by a compensation or a sweep, and marking it live would
// resurrect a record something else already closed.
func (s *sqliteContent) MarkLive(ctx context.Context, id wave.ParticipantID, l wave.Liveness) error {
	return s.run(ctx, func(ctx context.Context) error {
		epoch, err := epochToSQL(l.Epoch)
		if err != nil {
			return fmt.Errorf("content: mark live %q: %w", id, err)
		}
		res, err := s.db.ExecContext(ctx,
			`UPDATE wave_participants
			    SET state = 'live', backend_instance = ?, session_id = ?, epoch = ?,
			        lane = ?, attempt = ?, output_offset = ?
			  WHERE id = ? AND state = 'prepared'`,
			l.BackendInstance, l.SessionID, epoch, l.Lane, l.Attempt, l.OutputOffset,
			string(id))
		if err != nil {
			return fmt.Errorf("content: mark live %q: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("content: mark live %q: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("content: mark live %q: %w", id, wave.ErrNoSuchParticipant)
		}
		return nil
	})
}

// Terminalize writes a terminal state over a non-terminal one. The WHERE
// clause is what makes it safe to call twice and what stops it overwriting a
// state already established: a terminal record is not re-terminalized, so a
// retried compensation cannot turn a completed participant into an interrupted
// one.
func (s *sqliteContent) Terminalize(ctx context.Context, id wave.ParticipantID, st wave.State) error {
	if !st.Terminal() {
		return fmt.Errorf("content: terminalize %q: %q is not a terminal state", id, st)
	}
	return s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`UPDATE wave_participants SET state = ?
			  WHERE id = ? AND state IN ('prepared','live')`,
			string(st), string(id))
		if err != nil {
			return fmt.Errorf("content: terminalize %q: %w", id, err)
		}
		return nil
	})
}

func (s *sqliteContent) RecordDeclaration(ctx context.Context, id wave.ParticipantID, d wave.Declaration) (wave.Participant, error) {
	ok := 0
	if d.OK {
		ok = 1
	}
	if err := s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`UPDATE wave_participants SET declared_ok = ?, declared_summary = ?, declared_at = ?
			  WHERE id = ?`,
			ok, d.Summary, d.At.UnixMilli(), string(id))
		if err != nil {
			return fmt.Errorf("content: record declaration %q: %w", id, err)
		}
		return nil
	}); err != nil {
		return wave.Participant{}, err
	}
	return s.Participant(ctx, id)
}

func (s *sqliteContent) RecordExit(ctx context.Context, id wave.ParticipantID, e wave.Exit) (wave.Participant, error) {
	if err := s.run(ctx, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx,
			`UPDATE wave_participants SET exit_cause = ?, exit_code = ?, exited_at = ?
			  WHERE id = ?`,
			e.Cause, e.Code, e.At.UnixMilli(), string(id))
		if err != nil {
			return fmt.Errorf("content: record exit %q: %w", id, err)
		}
		return nil
	}); err != nil {
		return wave.Participant{}, err
	}
	return s.Participant(ctx, id)
}

func (s *sqliteContent) PutDelegation(ctx context.Context, d wave.Delegation) error {
	return s.run(ctx, func(ctx context.Context) error {
		epoch, err := epochToSQL(d.Epoch)
		if err != nil {
			return fmt.Errorf("content: put delegation %q: %w", d.Participant, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("content: put delegation: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wave_delegations
			   (participant_id, controller_session, epoch, created_by_run_id, state)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(participant_id) DO UPDATE SET
			   controller_session = excluded.controller_session,
			   epoch              = excluded.epoch,
			   created_by_run_id  = excluded.created_by_run_id,
			   state              = excluded.state`,
			string(d.Participant), d.ControllerSession, epoch, d.CreatedByRunID, string(d.State),
		); err != nil {
			return fmt.Errorf("content: put delegation %q: %w", d.Participant, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM wave_delegation_effects WHERE participant_id = ?`, string(d.Participant)); err != nil {
			return fmt.Errorf("content: put delegation %q: %w", d.Participant, err)
		}
		for _, e := range d.Effects {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO wave_delegation_effects (participant_id, effect) VALUES (?, ?)`,
				string(d.Participant), string(e)); err != nil {
				return fmt.Errorf("content: put delegation %q effect %q: %w", d.Participant, e, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("content: put delegation %q: %w", d.Participant, err)
		}
		return nil
	})
}

const participantColumns = `id, wave_id, role, state, task, registered_at,
	backend_instance, session_id, epoch, lane, attempt, output_offset,
	declared_ok, declared_summary, declared_at, exit_cause, exit_code, exited_at`

func (s *sqliteContent) Participant(ctx context.Context, id wave.ParticipantID) (wave.Participant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+participantColumns+` FROM wave_participants WHERE id = ?`, string(id))
	p, err := scanParticipant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return wave.Participant{}, fmt.Errorf("content: participant %q: %w", id, wave.ErrNoSuchParticipant)
	}
	if err != nil {
		return wave.Participant{}, fmt.Errorf("content: participant %q: %w", id, err)
	}
	return p, nil
}

// CoordinatorSession answers who must judge a fact about this wave. A wave
// with no row is not an empty answer: a fact that entered against a wave the
// store does not hold has nobody to reach, and saying so is what stops a wake
// being addressed to the empty string.
func (s *sqliteContent) CoordinatorSession(ctx context.Context, id wave.ID) (string, error) {
	var out string
	err := s.db.QueryRowContext(ctx,
		`SELECT coordinator_session FROM waves WHERE id = ?`, string(id)).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("content: wave %q: %w", id, wave.ErrNoSuchParticipant)
	}
	if err != nil {
		return "", fmt.Errorf("content: wave %q: %w", id, err)
	}
	return out, nil
}

func (s *sqliteContent) NonTerminal(ctx context.Context, id wave.ID) ([]wave.Participant, error) {
	return s.queryParticipants(ctx,
		`SELECT `+participantColumns+` FROM wave_participants
		  WHERE wave_id = ? AND state IN ('prepared','live')
		  ORDER BY registered_at, id`, string(id))
}

func (s *sqliteContent) AllNonTerminal(ctx context.Context) ([]wave.Participant, error) {
	return s.queryParticipants(ctx,
		`SELECT `+participantColumns+` FROM wave_participants
		  WHERE state IN ('prepared','live')
		  ORDER BY registered_at, id`)
}

// HeldBy answers D3 by joining through the wave rather than through the
// delegation, and the difference matters. A delegation is revocable and can be
// suspended; membership is not. A coordinator that has just restarted needs to
// be told about every worker it is responsible for, including one whose
// control it has temporarily lost to a human takeover — otherwise a takeover
// would make a worker disappear from its own coordinator's account of the
// wave, which is the exact conflation this record is built to refuse.
func (s *sqliteContent) HeldBy(ctx context.Context, coordinatorSession string) ([]wave.Participant, error) {
	return s.queryParticipants(ctx,
		`SELECT `+prefixed(participantColumns, "p")+`
		   FROM wave_participants p
		   JOIN waves w ON w.id = p.wave_id
		  WHERE w.coordinator_session = ? AND p.role <> 'coordinator'
		  ORDER BY p.registered_at, p.id`, coordinatorSession)
}

func (s *sqliteContent) queryParticipants(ctx context.Context, query string, args ...any) ([]wave.Participant, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("content: wave participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []wave.Participant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, fmt.Errorf("content: wave participants: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: wave participants: %w", err)
	}
	return out, nil
}

// scanner is what a *sql.Row and a *sql.Rows have in common, so one scan
// function serves both and the column order is written once.
type scanner interface{ Scan(dest ...any) error }

func scanParticipant(sc scanner) (wave.Participant, error) {
	var (
		p                wave.Participant
		id, waveID       string
		role, state      string
		registeredAt     int64
		declaredOK       sql.NullInt64
		declaredSummary  sql.NullString
		declaredAt       sql.NullInt64
		exitCause        sql.NullString
		exitCode         sql.NullInt64
		exitedAt         sql.NullInt64
		epoch, outOffset int64
	)
	if err := sc.Scan(&id, &waveID, &role, &state, &p.Task, &registeredAt,
		&p.Liveness.BackendInstance, &p.Liveness.SessionID, &epoch, &p.Liveness.Lane,
		&p.Liveness.Attempt, &outOffset,
		&declaredOK, &declaredSummary, &declaredAt, &exitCause, &exitCode, &exitedAt); err != nil {
		return wave.Participant{}, err
	}
	p.ID = wave.ParticipantID(id)
	p.Wave = wave.ID(waveID)
	p.Role = wave.Role(role)
	p.State = wave.State(state)
	p.RegisteredAt = time.UnixMilli(registeredAt).UTC()
	if epoch < 0 {
		return wave.Participant{}, fmt.Errorf("content: participant %q: epoch %d is negative", id, epoch)
	}
	p.Liveness.Epoch = uint64(epoch)
	p.Liveness.OutputOffset = outOffset
	// The two facts are read back independently, because they ARE
	// independent: a declaration with no exit is a real and ordinary state,
	// and so is the reverse.
	if declaredOK.Valid {
		p.Declared = &wave.Declaration{
			OK:      declaredOK.Int64 == 1,
			Summary: declaredSummary.String,
			At:      time.UnixMilli(declaredAt.Int64).UTC(),
		}
	}
	if exitCause.Valid {
		p.Exited = &wave.Exit{
			Cause: exitCause.String,
			Code:  int(exitCode.Int64),
			At:    time.UnixMilli(exitedAt.Int64).UTC(),
		}
	}
	return p, nil
}

// epochToSQL narrows the session's own uint64 epoch to the INTEGER SQLite
// stores, and REFUSES rather than wrapping.
//
// The type is uint64 because that is what internal/session and
// internal/lifecycle already call an epoch, and matching them is the point:
// a second epoch type would be a second answer to "which incarnation is
// this". SQLite's INTEGER is signed, so the narrowing is real, and it is the
// one place the two representations meet.
//
// Wrapping would not produce a wrong number, it would produce evidence
// attached to the WRONG INCARNATION — which is the single thing this field
// exists to prevent. So the boundary refuses. The value is unreachable in
// practice (an epoch counts session incarnations), and that is exactly why a
// silent wrap would never be noticed if it ever happened.
func epochToSQL(e uint64) (int64, error) {
	if e > math.MaxInt64 {
		return 0, fmt.Errorf("epoch %d does not fit the store's INTEGER", e)
	}
	return int64(e), nil
}

// prefixed qualifies a column list for a join. It exists so the column order
// lives in exactly one string: a second hand-written list for the joined query
// is the shape that drifts the day a column is added.
func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}
