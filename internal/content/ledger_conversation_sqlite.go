package content

// The conversation read (ADR-0040's closing consequence): "the conversation is
// assembled from the children, in pos order, per run".
//
// It lives in the ledger and not in the caller, and that is the decision this
// file records. A turn's answer is no longer one string in one column — it is
// however many `text` children the run opened between its tool calls — so
// "what did that turn say" is now a join: the turn's children, narrowed to the
// run whose answer stands, ordered by seat, with their bodies read back. Every
// one of those three steps has exactly one right answer, and a reader that
// stitched them itself would be a second owner of the arrangement (AD-8), in
// the surface with the least idea what it means. That is the shape ADR-0040
// exists to remove, and putting it back one layer up would be the same defect
// under a different roof.
//
// Four things it is deliberate about, because each is a way of being silently
// wrong rather than loudly wrong:
//
//   - RETRY. `executions` permits several agent-lane rows per entry by design
//     (ADR-0020 decision 4), and sorting a turn's children by pos alone would
//     splice two attempts into one incoherent message. The run is CHOSEN, and
//     the choice is reported on the value (TurnProse.RunID/Attempt).
//   - INTERRUPTION. Whether a partial answer is a real message or an
//     unfinished attempt is a fact about the EXECUTION's state, never about
//     how many `text` children exist — an interrupted run leaves exactly the
//     rows a finished one leaves. So the state is carried out with the text
//     and the caller is told, rather than guessing from a length.
//   - EVICTION. Retention takes the prose of one run as a unit (ADR-0040's
//     retention rule). A turn whose prose is gone must be reported gone —
//     TurnProse.Evicted — so that a caller can say so instead of leaving a
//     hole or inventing text.
//   - THE THREAD IS THE PANE. A turn is anchored to a pane and not to a
//     session, because a session is gone by the time a restore runs
//     (nocx-4em1z). Reading the thread by anything else would put one tab's
//     conversation into another's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// agentLane is the executions.lane value an assistant run carries — the one
// that separates a run from the capture execution a frame writes and from a
// human's shell execution, which carry no lane at all.
const agentLane = "agent"

// PriorTurn returns the agent turn preceding beforeEntryID in paneID, with the
// prose of the run that answered it. See the interface for the contract.
func (s *sqliteContent) PriorTurn(ctx context.Context, paneID, beforeEntryID string) (*PriorTurn, error) {
	if beforeEntryID == "" {
		return nil, errors.New("content: prior turn: the turn to look before is required")
	}
	// No pane, no thread. A session that is the pipe of no recorded pane has
	// no conversation to read, and answering from every pane's turns would be
	// another tab's conversation presented as this one's.
	if paneID == "" {
		return nil, nil
	}
	// The cursor is a POSITION RESOLVED THROUGH A ROW, never a comparison of
	// ids — LedgerQuery.BeforeID's rule, and for its reason: a UUIDv7 sorts by
	// the moment a client minted it, which is not the moment the backend
	// accepted it.
	var seq int64
	err := s.db.QueryRowContext(ctx,
		`SELECT ingest_seq FROM entries WHERE id = ?`, beforeEntryID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("content: prior turn before %s: %w", beforeEntryID, ErrNoSuchEntry)
	}
	if err != nil {
		return nil, err
	}

	// A TURN is an agent-kind entry that HAS AN AGENT RUN, and the second half
	// is what keeps a captured frame out of the conversation: a frame lands as
	// kind=agent too (CaptureFrame), and its execution is a capture record
	// with no lane. Asking for the run rather than matching on the frame's
	// intent string means the two are told apart by the thing that actually
	// differs, not by a literal two packages would both have to hold.
	var out PriorTurn
	err = s.db.QueryRowContext(ctx, `SELECT e.id, e.intent FROM entries e
		 WHERE e.pane_id = ? AND e.kind = 'ask' AND e.ingest_seq < ?
		   AND EXISTS (SELECT 1 FROM executions x
		                WHERE x.entry_id = e.id AND x.lane = ?)
		 ORDER BY e.ingest_seq DESC LIMIT 1`, paneID, seq, agentLane).
		Scan(&out.EntryID, &out.Question)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prose, err := s.turnProse(ctx, out.EntryID)
	if err != nil {
		return nil, err
	}
	out.Prose = prose
	return &out, nil
}

// turnProse is one turn's answer: the prose of the run that stands, in seat
// order, with the two facts that keep an empty answer honest (the run's state
// and whether retention took the text).
//
// WHICH RUN, and why it is the latest. A turn's agent-lane executions are its
// attempts; the one whose text a person actually read is the last one, so it
// is the one a follow-up question is about. An earlier attempt's prose is not
// merged in and is not reported missing either — an attempt that was
// superseded is not part of the answer that stands.
//
// HOW A BLOCK IS ATTRIBUTED, and it is two rules rather than one on purpose.
// With ONE agent run on the turn — which is every path the product has today
// — every `text` child is that run's, because there is nothing else it could
// belong to; nothing is read out of the payload at all, so a block written by
// anything but OpenProse still assembles instead of vanishing. With SEVERAL,
// that reasoning fails and the recorded run is what separates them
// (ProseFacts). A block that records no run is then attributed to NO run: it
// cannot be placed, and placing it by guess is exactly the interleaving this
// method exists to prevent.
func (s *sqliteContent) turnProse(ctx context.Context, turnID string) (TurnProse, error) {
	var p TurnProse
	var state sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, attempt, state FROM executions
		  WHERE entry_id = ? AND lane = ?
		  ORDER BY attempt DESC, id DESC LIMIT 1`, turnID, agentLane).
		Scan(&p.RunID, &p.Attempt, &state)
	// No agent run under this id: there is no answer to assemble and no
	// missing one to report. The zero value says exactly that.
	if errors.Is(err, sql.ErrNoRows) {
		return TurnProse{}, nil
	}
	if err != nil {
		return TurnProse{}, err
	}
	if state.Valid {
		p.State = RunState(state.String)
	}

	var runs int
	if err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM executions WHERE entry_id = ? AND lane = ?`,
		turnID, agentLane).Scan(&runs); err != nil {
		return TurnProse{}, err
	}

	// The children, in SEAT order — the order on screen, and the whole
	// meaning: a sentence written before a call explains why the call was
	// made, and a sentence written after it is a conclusion drawn from its
	// output. The eviction receipt rides the same row, read with the ONE
	// expression that reads it anywhere (bodyEvictedExpr).
	children := `SELECT c.payload, a.id, ` + bodyEvictedExpr("a") + //nolint:gosec // constant fragments; the only value is bound
		` FROM entries c JOIN artifacts a ON a.entry_id = c.id
		   WHERE c.parent_id = ? AND c.kind = 'text'
		   ORDER BY c.pos, a.id`
	rows, err := s.db.QueryContext(ctx, children, turnID)
	if err != nil {
		return TurnProse{}, err
	}
	defer func() { _ = rows.Close() }()
	var bodies []string
	for rows.Next() {
		var payload, artifactID string
		var evicted bool
		if err := rows.Scan(&payload, &artifactID, &evicted); err != nil {
			return TurnProse{}, err
		}
		if runs > 1 {
			facts, factsErr := ProseFactsOf(payload)
			if factsErr != nil || facts.RunID != p.RunID {
				continue
			}
		}
		if evicted {
			p.Evicted = true
			continue
		}
		bodies = append(bodies, artifactID)
	}
	if err := rows.Err(); err != nil {
		return TurnProse{}, err
	}

	// The bodies, in the order their blocks were seated. Read after the cursor
	// is done rather than inside the loop: sqlite serializes on one connection
	// and a second query under an open one is how a read deadlocks itself.
	var sb strings.Builder
	for _, id := range bodies {
		text, err := s.artifactText(ctx, id)
		if err != nil {
			return TurnProse{}, err
		}
		sb.WriteString(text)
	}
	p.Blocks = len(bodies)
	p.Text = sb.String()
	return p, nil
}

// artifactText is one body's chunks joined in seq order. It is the same read
// FrameText does one level up, and it stays here because prose is read by the
// block and never by the execution — there is no attempt to reach through.
func (s *sqliteContent) artifactText(ctx context.Context, artifactID string) (string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM artifact_chunks WHERE artifact_id = ? ORDER BY seq`, artifactID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var sb strings.Builder
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return "", err
		}
		sb.Write(body)
	}
	return sb.String(), rows.Err()
}
