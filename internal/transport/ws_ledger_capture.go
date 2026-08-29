package transport

// ledger.capture — one body of a frozen block, from the renderer to the store
// (nocx-2f0f, design §4).
//
// WHY IT IS ITS OWN METHOD and not part of ledger.close. The close travels
// through the renderer's outbox and is retried on a socket drop
// (nocx-rtg0.4); a close carrying a megabyte would resend that megabyte on
// every retry, and a capture that failed would then cost the entry its
// outcome as well as its body. They are separate facts and they fail
// separately.
//
// WHAT THE CALLER MAY SAY, and what it may not. The artifact id is the
// renderer's — it must survive a lost ack, so it cannot come from a backend
// instance — and everything about it is checked and nothing believed. The
// capture METHOD is not a parameter: it is terminal-cells because that is
// what this path is, and a renderer that could name its own provenance could
// claim a fidelity it did not have.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
)

// The two ceilings. They are input validation, not preferences: the user's
// per-command cap (nocx-2f0f, the settings knob) decides how much of an
// output is worth keeping, and these decide what one untrusted message may
// make the store do regardless of any setting.
const (
	// maxCaptureChunkBytes bounds ONE message. The renderer splits a body at
	// the same number, so a well-behaved client is never refused for it.
	maxCaptureChunkBytes = 64 << 10
)

// ledgerCaptureParams is the request. The earlier decision to leave params
// unpinned was wrong: ledger.capture.params.schema.json is now the wire contract,
// and its registered validator remains runtime enforcement for the bounded chunk.
//
//	entryId        — required; the row the body belongs to, as history.record
//	                 answered. Backend-minted, so its shape is not a UUIDv7
//	artifactId     — required; client-minted UUIDv7, the idempotency key
//	mediaType      — required; application/vt or text/plain, and nothing else
//	                 travels this path
//	derivedFrom    — optional; the artifact this one was produced from — the
//	                 plain body names the SGR body
//	truncated      — optional; cap | gap | suppressed
//	captureVersion — required; the renderer's SERIALIZER_VERSION
//	terminalCols   — optional; the grid the serializer saw
//	terminalRows   — optional
//	seq            — required; the chunk's position, from 1
//	body           — required; at most maxCaptureChunkBytes
type ledgerCaptureParams struct {
	EntryID        string  `json:"entryId"`
	ArtifactID     string  `json:"artifactId"`
	MediaType      string  `json:"mediaType"`
	DerivedFrom    *string `json:"derivedFrom"`
	Truncated      *string `json:"truncated"`
	CaptureVersion int     `json:"captureVersion"`
	TerminalCols   *int    `json:"terminalCols"`
	TerminalRows   *int    `json:"terminalRows"`
	Seq            int     `json:"seq"`
	Body           string  `json:"body"`
}

// ledgerCaptureResponse is the ack. `stored` is false when the body was not
// kept — output retention off, or a sensitive entry — which is NOT a failure
// and must not be reported as one: the renderer reads it and stops sending
// the remaining chunks.
type ledgerCaptureResponse struct {
	ArtifactID string `json:"artifactId"`
	Stored     bool   `json:"stored"`
}

// ledgerCaptureOf turns the request into the store's own input, or names what
// is wrong with it. Every refusal here would otherwise become a row somebody
// has to explain later.
func ledgerCaptureOf(p ledgerCaptureParams) (content.CaptureOutput, string) {
	var in content.CaptureOutput
	if strings.TrimSpace(p.EntryID) == "" || utf8.RuneCountInString(p.EntryID) > maxIDRunes {
		return in, "entryId is required and bounded"
	}
	if msg := layoutID("artifactId", p.ArtifactID); msg != "" {
		return in, msg
	}
	switch content.MediaType(p.MediaType) {
	case content.MediaVT, content.MediaText:
	default:
		return in, "mediaType must be application/vt or text/plain"
	}
	if p.DerivedFrom != nil {
		if msg := layoutID("derivedFrom", *p.DerivedFrom); msg != "" {
			return in, msg
		}
	}
	if p.Truncated != nil {
		switch content.Truncation(*p.Truncated) {
		case content.TruncCap, content.TruncGap, content.TruncSuppressed:
		default:
			return in, "truncated must be one of cap, gap, suppressed"
		}
	}
	if p.CaptureVersion < 1 {
		return in, "captureVersion is the renderer's serializer version and starts at 1"
	}
	if p.TerminalCols != nil && *p.TerminalCols < 1 {
		return in, "terminalCols must be positive"
	}
	if p.TerminalRows != nil && *p.TerminalRows < 1 {
		return in, "terminalRows must be positive"
	}
	if p.Seq < 1 {
		return in, "seq is the chunk's position and starts at 1"
	}
	if len(p.Body) > maxCaptureChunkBytes {
		return in, "body exceeds the per-chunk ceiling; split it"
	}

	in = content.CaptureOutput{
		EntryID:    p.EntryID,
		ArtifactID: p.ArtifactID,
		MediaType:  content.MediaType(p.MediaType),
		// The method's own provenance, never the caller's.
		CaptureMethod:  content.CaptureTerminalCells,
		CaptureVersion: p.CaptureVersion,
		TerminalCols:   p.TerminalCols,
		TerminalRows:   p.TerminalRows,
		Seq:            p.Seq,
		Body:           []byte(p.Body),
	}
	in.DerivedFrom = p.DerivedFrom
	if p.Truncated != nil {
		tr := content.Truncation(*p.Truncated)
		in.Truncated = &tr
	}
	return in, ""
}

func validateLedgerCaptureRaw(raw json.RawMessage) string {
	var p ledgerCaptureParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	_, msg := ledgerCaptureOf(p)
	return msg
}

// ledgerCaptureHandlers answers ledger.capture. Like the read handlers it
// holds no connection and no connState: a body belongs to an ENTRY, and an
// entry outlives the session it ran in (ADR-0019 §5).
type ledgerCaptureHandlers struct {
	op capability.LedgerOperation // nil → the content store is not wired
	r  Responder
}

func (h ledgerCaptureHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	var p ledgerCaptureParams
	if msg := decodeParams(req.Params, &p); msg != "" {
		h.invalid(req, msg)
		return
	}
	in, msg := ledgerCaptureOf(p)
	if msg != "" {
		h.invalid(req, msg)
		return
	}

	stored := false
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.LedgerService) error {
		kept, err := svc.CaptureOutput(ctx, in)
		stored = kept
		return err
	})
	if err != nil {
		// An unknown entry and a conflicting id are facts about the REQUEST,
		// so they are invalid params rather than server faults — and never a
		// silent success, which would leave the renderer believing a body it
		// will never see again is safe.
		switch {
		case isCaptureRequestFault(err):
			h.invalid(req, err.Error())
		default:
			answerOperationRefusal(h.r, req, err)
		}
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(ledgerCaptureResponse{
		ArtifactID: in.ArtifactID, Stored: stored,
	}))
}

func (h ledgerCaptureHandlers) invalid(req jsonrpcRequest, msg string) {
	_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
}

// isCaptureRequestFault separates "you asked for something that is not there"
// from "the store could not do it". The first is the caller's to fix and is
// reported as invalid params; the second is a refusal or a fault, and
// answerOperationRefusal already knows how to say each.
func isCaptureRequestFault(err error) bool {
	return errors.Is(err, content.ErrNoSuchEntry) ||
		errors.Is(err, content.ErrIDConflict) ||
		errors.Is(err, content.ErrArtifactTooLarge)
}
