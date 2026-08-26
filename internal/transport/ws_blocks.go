package transport

// The block source (nocx-5u3oz.6): the transport half of blocks.list and
// blocks.read. It answers the agent's two block tools from the LEDGER — the
// authoritative record of what has been run (ADR-0019 decision 1: one
// authoritative ledger, disposable projections) — rather than from the
// renderer, and internal/assistant/blocks.go carries the whole argument for
// that choice. There is no wire method here and no renderer round trip: the
// renderer already wrote every one of these rows (history.record for the
// entry, ledger.capture for the body), and this reads them back.
//
// WHAT IS SCOPED, AND WHERE. The run's grant names a SESSION, and a block is
// anchored to a PANE — entries.session_id is deliberately NULL for a command
// and pane_id is the durable anchor (ws_ledger.go says why). So the scope of
// a granted session is derived here, from the one owner of that fact: the
// session says which pane it is the pipe of, and when it was opened. Blocks
// of another tab are another pane; blocks recorded before this session
// existed — the same pane, an earlier run of the app — are before its floor.
// Neither is ever in the answer to be filtered: the query carries both
// bounds, and the read applies the same predicate to the row it resolves, so
// an id guessed from another pane answers exactly as an id that never
// existed (assistant.ErrSessionItemNotFound).
//
// The ledger handle is the raw repository, the same one the tool pipeline's
// attempt writes use (ws_agent.go's attemptLedger) and for the same reason:
// a tool runs on the ask stream, outside the content queue, and taking the
// content gate here would put a tool call behind the queue that is waiting
// for it. The wire methods keep the gate; this is not one.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

const defaultSessionItemLines = 200

// blockScope is what a granted session narrows to in the ledger's own
// vocabulary: the pane its blocks are anchored to, and the wall-clock floor
// below which a row belongs to an earlier session of the same pane.
type blockScope struct {
	paneID string
	since  int64
}

// blockScopeFor resolves the granted session to its ledger scope. A session
// this backend does not hold, or one attached to no recorded pane, is an
// ERROR and never an empty list: "this session has no blocks" and "there is
// no such session" must not look alike, or a model reads the second as the
// first and tells the person they have run nothing.
func (s *WSServer) blockScopeFor(sessionID string) (blockScope, error) {
	if s.registry == nil {
		return blockScope{}, errors.New("no session registry is wired")
	}
	sess, err := s.registry.Get(session.ID(sessionID))
	if err != nil {
		return blockScope{}, fmt.Errorf("no such session: %s", sessionID)
	}
	pane := sess.PaneID()
	if pane == "" {
		return blockScope{}, errors.New("this session is attached to no recorded pane, so none of its blocks are in the record")
	}
	// The floor INCLUDES the millisecond the session opened in: a row
	// stamped in that same tick cannot be attributed either way, and
	// counting it in errs toward showing the person's own pane rather than
	// hiding a block they are looking at.
	return blockScope{paneID: pane, since: sess.OpenedAt().UnixMilli()}, nil
}

// ledgerForBlocks is the repository the two reads run against, or nil when
// the content store is not wired in this build.
func (s *WSServer) ledgerForBlocks() content.LedgerRepository {
	if s.contentDB == nil {
		return nil
	}
	return s.contentDB.Ledger()
}

// ListSessionItems implements assistant.SessionSource. The ledger is the
// source of item identity and state; an empty page is a valid answer for a
// pane that has no recorded command blocks.
func (s *WSServer) ListSessionItems(ctx context.Context, sessionID string, limit int) (assistant.SessionItems, error) {
	ledger := s.ledgerForBlocks()
	if ledger == nil {
		return assistant.SessionItems{}, errors.New("no content store is wired, so nothing has been recorded to read")
	}
	scope, err := s.blockScopeFor(sessionID)
	if err != nil {
		return assistant.SessionItems{}, err
	}
	since := scope.since
	page, err := ledger.QueryEntries(ctx, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Kind: content.EntryShell, PaneID: scope.paneID,
		Since: &since, Limit: limit,
	})
	if err != nil {
		return assistant.SessionItems{}, err
	}
	out := assistant.SessionItems{Items: make([]assistant.SessionItem, 0, len(page.Entries)), More: !page.Exhausted}
	for _, row := range page.Entries {
		item := assistant.SessionItem{ID: row.ID, Command: row.Intent}
		if row.Phase == content.PhaseClosed {
			item.State = "exited"
			item.ExitCode, _ = content.ShellExitCodeOf(row.Payload)
			body, bodyErr := s.blockBody(ctx, ledger, row.ID)
			if bodyErr != nil {
				return assistant.SessionItems{}, bodyErr
			}
			if body.kept {
				item.Lines = len(splitBlockLines(body.text))
			}
		} else {
			item.State = "running"
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ReadSessionItem returns the ledger state for one item. Exited output is
// read here; running output is deliberately left to the renderer's current
// screen capture, preserving AD-6.
func (s *WSServer) ReadSessionItem(ctx context.Context, sessionID, itemID string, start, count int) (assistant.SessionItemRead, error) {
	ledger := s.ledgerForBlocks()
	if ledger == nil {
		return assistant.SessionItemRead{}, errors.New("no content store is wired, so nothing has been recorded to read")
	}
	scope, err := s.blockScopeFor(sessionID)
	if err != nil {
		return assistant.SessionItemRead{}, err
	}
	entry, err := ledger.Entry(ctx, itemID)
	if err != nil {
		return assistant.SessionItemRead{}, err
	}
	if entry == nil || entry.PaneID == nil || *entry.PaneID != scope.paneID || entry.SubmittedAt < scope.since || entry.Kind != content.EntryShell {
		return assistant.SessionItemRead{}, assistant.ErrSessionItemNotFound
	}
	item := assistant.SessionItemRead{ID: entry.ID, Command: entry.Intent}
	if entry.Phase != content.PhaseClosed {
		item.State = "running"
		return item, nil
	}
	item.State = "exited"
	item.ExitCode, _ = content.ShellExitCodeOf(entry.Payload)
	body, err := s.blockBody(ctx, ledger, itemID)
	if err != nil {
		return assistant.SessionItemRead{}, err
	}
	if !body.kept {
		item.Note = "this item's output was not kept: history or output retention is off, or the command was marked sensitive"
		return item, nil
	}
	lines := splitBlockLines(body.text)
	item.Total = len(lines)
	if start < 0 {
		start = 0
	}
	if count <= 0 {
		count = defaultSessionItemLines
	}
	if start > item.Total {
		start = item.Total
	}
	end := start + count
	if end > item.Total {
		end = item.Total
	}
	item.Start, item.End = start, end
	item.Text = strings.Join(lines[start:end], "\n")
	return item, nil
}

// blockBodyResult is what the store kept for one block: the text, whether it
// kept anything at all, and whether what it kept lost its middle to the
// capture cap.
type blockBodyResult struct {
	text      string
	kept      bool
	truncated string
}

// blockBody reads one block's plain body. Two artifacts hang on a frozen
// block — the SGR body a restore draws, and the plain body derived from it,
// which is what search, copy and this read use (capture-client.ts) — so this
// takes the derived one and never re-derives text from the escape sequences.
// No such artifact is not a failure: history off, output retention off or a
// sensitive command all end here, and the tools state it as an absence
// rather than as an empty output.
//
// The shape of the read — entry, then its execution's artifacts, then the
// artifact's chunks — is the one capability.agentService.FrameText already
// uses: the recall read never hauls bytes, so the body is a second,
// deliberate fetch.
//
// A TURN takes the second path, and it is not a special case so much as the
// same read one level down: since ADR-0040 an assistant turn owns no body of
// its own — its answer is the `text` children the run wrote, one per run of
// prose — so a block that kept nothing on its own attempts is asked for its
// prose before it is reported as a block that kept nothing at all. Without
// this, `blocks.read` of an earlier answer would say the assistant had
// printed nothing, which is a sentence about a turn that plainly did.
func (s *WSServer) blockBody(ctx context.Context, ledger content.LedgerRepository, entryID string) (blockBodyResult, error) {
	entry, err := ledger.Entry(ctx, entryID)
	if err != nil {
		return blockBodyResult{}, err
	}
	if entry == nil {
		return blockBodyResult{}, nil
	}
	for _, ex := range entry.Executions {
		for _, a := range ex.Artifacts {
			if a.MediaType != content.MediaText {
				continue
			}
			art, artErr := ledger.Artifact(ctx, a.ID)
			if artErr != nil {
				return blockBodyResult{}, artErr
			}
			if art == nil {
				// The metadata is there and the body is not: retention
				// evicted it. A hole, and it is reported as one (ADR-0019
				// §7) rather than as an empty output.
				continue
			}
			var sb strings.Builder
			for _, c := range art.Chunks {
				sb.Write(c)
			}
			out := blockBodyResult{text: sb.String(), kept: true}
			if a.Truncated != nil {
				out.truncated = string(*a.Truncated)
			}
			return out, nil
		}
	}
	return s.proseBody(ctx, ledger, entryID)
}

// proseBody is a turn's answer: its `text` children, in seat order, joined.
// ONE answer however many pieces it took, because the pieces are a fact about
// where the turn's calls fell and not about the prose — a reader asking for
// the block's body is asking what the assistant said.
//
// A block with no prose children answers "nothing kept", which is the same
// absence the loop above reports and reaches the model the same way. A child
// whose body retention evicted is skipped rather than substituted, exactly as
// above: ADR-0019 §7 leaves the entry and takes the body, and a hole is
// reported as one.
func (s *WSServer) proseBody(ctx context.Context, ledger content.LedgerRepository, entryID string) (blockBodyResult, error) {
	kids, err := ledger.Caused(ctx, entryID)
	if err != nil {
		return blockBodyResult{}, err
	}
	var sb strings.Builder
	kept := false
	for _, k := range kids {
		if k.Kind != content.EntryText {
			continue
		}
		child, childErr := ledger.Entry(ctx, k.EntryID)
		if childErr != nil {
			return blockBodyResult{}, childErr
		}
		if child == nil {
			continue
		}
		for _, a := range child.Artifacts {
			art, artErr := ledger.Artifact(ctx, a.ID)
			if artErr != nil {
				return blockBodyResult{}, artErr
			}
			if art == nil {
				continue
			}
			for _, c := range art.Chunks {
				sb.Write(c)
			}
			kept = true
		}
	}
	if !kept {
		return blockBodyResult{}, nil
	}
	return blockBodyResult{text: sb.String(), kept: true}, nil
}

// splitBlockLines is the ONE derivation of a block's lines. The captured
// body is the block's rows joined by '\n' by the serializer, and a row never
// contains one, so the split is exact. An empty body is ZERO lines and not
// one empty line: a command that printed nothing has no output to window.
func splitBlockLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
