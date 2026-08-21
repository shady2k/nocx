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
// # Why the backend mints the id here
//
// Every other entry id is a client-minted UUIDv7 and an idempotency key
// (design §7). history.record carries none — it never has — so the backend
// mints one, exactly as CaptureFrame mints a frame's. The consequence is
// stated rather than hidden: a retried history.record writes a SECOND row,
// because there is no key to recognise the first by. That is unchanged from
// command_history, which had no idempotency either, and closing it belongs to
// nocx-rtg0.7, where the renderer starts sending the lifecycle it already
// tracks.

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
	// Author is WHO submitted the command, and it is the entry's own kind
	// (design §3.1, nocx-iadtt/nocx-e5vsc): EntryShell is the person at the
	// keyboard, EntryAgent is the assistant's lane. It is carried from the
	// renderer's submit — the one place that knows which input target ran
	// the line — and never derived here from a lane or a run state, or a
	// human command typed while the agent works would be recorded as the
	// agent's. Empty defaults to EntryShell: a caller that names no author
	// is the ordinary shell path, which is what every caller was before
	// the author existed.
	Author EntryKind
}

// RecordCompleted writes one finished command and returns the entry id the
// backend minted for it. That id is the handle every later reference uses —
// the capture rewrite, provenance detail, and history.query's page.
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
	switch in.Author {
	case "":
		in.Author = EntryShell
	case EntryShell, EntryAgent:
	default:
		// `action` is a no-block effect and can never be a command's author,
		// and an unknown kind would write a row the CHECK constraint refuses
		// halfway through the transaction. Refused here, where the message
		// can say what the vocabulary is.
		return "", fmt.Errorf("content: record: %q is not a command author; want shell or agent", in.Author)
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
		digest := entryDigest(SubmitEntry{
			Client: in.Client, EnvironmentID: in.Env.ID, Cwd: in.Cwd, Intent: in.Intent,
			Payload: in.Payload, Kind: in.Author, Sensitivity: in.Sensitivity,
			PaneID: pane,
		})

		if _, err := tx.ExecContext(ctx, `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, pane_id, session_id, cwd, kind, intent,
			 phase, status, submitted_at, started_at, ended_at, duration_ms, sensitivity, payload)
			VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, 'closed', ?, ?, ?, ?, ?, ?, ?)`,
			entryID, seq, in.Client, digest, in.Env.ID, pane, in.Cwd, string(in.Author), in.Intent,
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
