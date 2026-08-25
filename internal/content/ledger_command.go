package content

// RecordCompleted — the ledger's answer to history.record (nocx-rtg0.19).
//
// THE CUTOVER, IN ONE METHOD. command_history held "a command and what it
// printed" and stored no output at all; the ledger is what the product object
// actually needs. history.record's SHAPE does not change — the renderer sends
// one message after a command ends, as it always has — and this is where that
// message lands now.
//
// # Why a completed command is one transaction and not three calls
//
// The ledger's own lifecycle is Submit → StartExecution → FinishExecution,
// three events for a command the renderer watches happen. history.record
// arrives AFTER the fact: there is nothing to watch, only a row to write. Made
// of three separate calls it would leave an entry with no execution whenever
// the second failed — a row that says a command was intended and nothing about
// whether it ran. The startup sweep would later close it as `unknown`, which
// is an honest answer to the wrong question.
//
// So the intent, its single execution and its outcome commit together. The
// shape is CaptureFrame's (ledger_agent_sqlite.go), which does the same thing
// for the same reason, down to sharing ensureLedgerContext.
//
// # Why the backend mints an id when no lifecycle attempt exists
//
// history.record traditionally carried no id, so the backend minted one for
// completed-only callers. A lifecycle submit now carries AttemptID; that
// path updates the already-open row and returns its id instead. RecordCompleted
// remains for callers that have no authenticated lifecycle attempt.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CompletedCommand is one finished command, as history.record knows it.
//
// It carries no output: history.record never did, and inventing an empty
// artifact would put a row in `artifacts` that claims a capture happened.
type CompletedCommand struct {
	// Client binds the row to who wrote it, as every entry's does.
	Client string
	// AttemptID is the lifecycle attempt row opened at submit. When present,
	// this completion updates that row instead of minting a second identity.
	AttemptID string
	// Env is the environment the command ran in, derived from the facts the
	// caller holds and never from a session (design §3.1). Ensured here.
	Env Environment
	// PaneID is nocx-rtg0.28's durable anchor. Nil when the caller knows no
	// pane, or names one the layout chain does not hold — the command is
	// recorded unanchored rather than refused, because losing the restore
	// hint is cheaper than losing the command.
	PaneID *string
	// Cwd, Intent: where it ran and what was run. Intent is the MASKED
	// command; the durable text is always the masked one.
	Cwd    string
	Intent string
	// Sensitivity and Payload are the entry's own columns. Payload carries
	// the redaction receipt and the shell arm's exit code, built by the
	// caller so this method has no opinion about either.
	Sensitivity Sensitivity
	Payload     string
	// Status is the outcome as the renderer reported it. `pending` is not a
	// value history.record can send: a command it is telling us about has
	// already ended.
	Status EntryStatus
	// StartedAt and EndedAt are the renderer's wall clock; DurationMs is its
	// own measurement, never the difference of two clocks (nocx-rtg0.23).
	StartedAt  *int64
	EndedAt    *int64
	DurationMs *int64
	// TerminationReason is the execution's own fact: which of the outcomes a
	// status plus an exit code cannot separate (ADR-0020 §4) this run had.
	TerminationReason TerminationReason
	// Source is the IMMEDIATE subject that submitted the command (design
	// §3.1, nocx-iadtt/nocx-e5vsc): SourceUser is the person at the
	// keyboard, SourceAssistant is the assistant's lane. A command is a
	// SHELL entry whatever its source — the kind says what the row is, and
	// source says who asked for it. It is carried from the renderer's
	// submit — the one place that knows which input target ran the line —
	// and never derived here from a lane or a run state, or a human command
	// typed while the agent works would be recorded as the assistant's.
	// REQUIRED, and deliberately not defaulted. It used to default to the
	// person's shell, on the argument that a caller naming no author was the
	// ordinary path — true while this was the author of a command, and wrong
	// now that it is provenance the audit reads. "Nobody said who submitted
	// this" must not become "the person did": that is a claim, it is the one
	// this column exists to make, and a silent default makes it without
	// anybody deciding. A caller that forgets is a caller with a bug, and it
	// is told so.
	Source Source
}

// RecordCompleted closes the lifecycle row named by AttemptID when supplied.
// Without it, the completed-only path mints and returns a new entry id.
func (s *sqliteContent) RecordCompleted(ctx context.Context, in CompletedCommand) (string, error) {
	if in.Client == "" {
		return "", fmt.Errorf("content: record: client is required — it binds the row to who wrote it")
	}
	if in.Status == "" || in.Status == EntryPending {
		return "", fmt.Errorf("content: record: %q is not an outcome; history.record reports commands that ended", in.Status)
	}
	if in.Sensitivity == "" {
		in.Sensitivity = SensitivityNormal
	}
	if in.Payload == "" {
		in.Payload = "{}"
	}
	if in.TerminationReason == "" {
		in.TerminationReason = TermCompleted
	}
	switch in.Source {
	case SourceUser, SourceAssistant:
	default:
		// `source`, `action` and `text` are not subjects a command can have
		// asked for, and an unknown source would write a row the CHECK
		// constraint refuses halfway through the transaction. Refused here,
		// where the message can say what the vocabulary is.
		return "", fmt.Errorf("content: record: %q is not a command source; want user or assistant", in.Source)
	}
	// Keep-history-off: a command runs and no row appears, and that is not an
	// error — the same rule the interim table's Add followed, moved here with
	// the write path it belonged to. Decided before the writer is reached, so
	// nothing is serialized for a record nobody wants. The empty id is the
	// caller's signal that there is no row to reference.
	if !s.policy.Enabled() {
		return "", nil
	}

	var id string
	err := s.run(ctx, func(ctx context.Context) error {
		// BEGIN IMMEDIATE, for the reason Submit states: the write lock is
		// taken at BEGIN rather than at the first write, so a second writer
		// waits instead of failing an upgrade with SQLITE_BUSY_SNAPSHOT
		// (nocx-rtg0.18).
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		var lookupErr error
		if in.AttemptID != "" {
			var phase, client string
			lookupErr = tx.QueryRowContext(ctx, `SELECT phase, client FROM entries WHERE id = ?`, in.AttemptID).Scan(&phase, &client)
			if lookupErr == nil {
				if client != in.Client {
					return fmt.Errorf("content: record: attempt %q belongs to another client", in.AttemptID)
				}
				if phase == string(PhaseClosed) {
					id = in.AttemptID
					return tx.Commit()
				}
				var executionID int64
				if executionErr := tx.QueryRowContext(ctx,
					`SELECT id FROM executions WHERE entry_id = ? ORDER BY attempt DESC LIMIT 1`,
					in.AttemptID).Scan(&executionID); executionErr != nil {
					return fmt.Errorf("content: record: find attempt execution: %w", executionErr)
				}
				now := time.Now().UnixMilli()
				if _, updateErr := tx.ExecContext(ctx,
					`UPDATE executions SET ended_at = ?, termination_reason = ? WHERE id = ?`,
					coalesceTime(in.EndedAt, now), string(in.TerminationReason), executionID); updateErr != nil {
					return updateErr
				}
				if _, entryErr := tx.ExecContext(ctx,
					`UPDATE entries SET phase = 'closed', status = ?,
					   started_at = COALESCE(started_at, ?),
					   ended_at = ?, duration_ms = COALESCE(?, duration_ms),
					   payload = json_patch(payload, ?)
					 WHERE id = ?`,
					string(in.Status), in.StartedAt, coalesceTime(in.EndedAt, now),
					in.DurationMs, in.Payload, in.AttemptID); entryErr != nil {
					return entryErr
				}
				id = in.AttemptID
				return tx.Commit()
			}
			if lookupErr != sql.ErrNoRows {
				return lookupErr
			}
		}

		// The anchor is RESOLVED before the write, never left to the foreign
		// key — the same rule Submit follows — but its failure is NOT fatal
		// here. A pane the chain does not hold costs the restore hint; the
		// command itself is still worth recording, and history.record's
		// caller has no way to fix the id anyway.
		pane := in.PaneID
		if pane != nil {
			if _, paneErr := paneByID(ctx, tx, *pane); paneErr != nil {
				pane = nil
			}
		}

		obsID, err := ensureLedgerContext(ctx, tx, in.Env, nil)
		if err != nil {
			return err
		}

		now := time.Now().UnixMilli()
		entryID := mintID()
		var seq int64
		if seqErr := tx.QueryRowContext(ctx,
			`UPDATE ledger_sequence SET next = next + 1 WHERE id = 1 RETURNING next`).Scan(&seq); seqErr != nil {
			return fmt.Errorf("content: record: assign ingest_seq: %w", seqErr)
		}

		// The digest binds the row's content the way Submit's does. Nothing
		// replays this id — the backend minted it — so it is provenance here
		// rather than an idempotency check, and it is written because every
		// entry has one and a NULL would be a second shape of row.
		// The kind is ALWAYS shell: a command is WHAT the row is, and the
		// source says WHO asked for it.
		digest := entryDigest(SubmitEntry{
			Client: in.Client, EnvironmentID: in.Env.ID, Cwd: in.Cwd, Intent: in.Intent,
			Payload: in.Payload, Kind: EntryShell, Source: in.Source, Sensitivity: in.Sensitivity,
			PaneID: pane,
		})

		if _, err := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, session_id, cwd, kind, source, intent,
			 phase, status, submitted_at, started_at, ended_at, duration_ms, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, NULL, ?, 'shell', ?, ?, 'closed', ?, ?, ?, ?, ?, ?, ?)`,
			entryID, seq, in.Client, digest, in.Env.ID, pane, in.Cwd, string(in.Source), in.Intent,
			string(in.Status), now, in.StartedAt, in.EndedAt, in.DurationMs,
			string(in.Sensitivity), in.Payload); err != nil {
			return err
		}

		// session_id is NULL above and that is deliberate: history.record
		// does not carry one, and a session row invented here would be a
		// second writer of the table nocx-49d4 is about. The entry keeps its
		// pane, which is the durable anchor anyway (design §6.1).
		//
		// state is NULL on the execution — the startup sweep only touches
		// runs it might have to interrupt, and this one ended before it was
		// written.
		if _, err := tx.ExecContext(ctx, `INSERT INTO executions
			(entry_id, attempt, environment_obs_id, interactivity, started_at, ended_at, termination_reason)
			VALUES (?, 1, ?, 'none', ?, ?, ?)`,
			entryID, obsID, coalesceTime(in.StartedAt, now), coalesceTime(in.EndedAt, now),
			string(in.TerminationReason)); err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		id = entryID
		return nil
	})
	return id, err
}

// coalesceTime is "the renderer's clock, or ours if it sent none". The
// execution's start and end are NOT NULL in spirit — a run that happened has
// bounds — while the entry's own copies stay nullable, because there they
// record what the renderer measured and a null is the honest answer when it
// measured nothing.
func coalesceTime(v *int64, fallback int64) int64 {
	if v == nil {
		return fallback
	}
	return *v
}
