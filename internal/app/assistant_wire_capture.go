package app

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/shady2k/nocx/internal/content"
)

// ledgerWireRecorder stores the provider exchange as two ordinary ledger
// artifacts owned by the turn. The payload labels the direction because the
// ledger's artifact schema intentionally has no second, dump-specific owner.
type ledgerWireRecorder struct {
	ledger   content.LedgerRepository
	sequence atomic.Uint64
}

func (r *ledgerWireRecorder) RecordWire(ctx context.Context, runID, entryID, kind string, body []byte, truncated bool) {
	if r == nil || r.ledger == nil || entryID == "" || (kind != "request" && kind != "response") {
		return
	}
	ordinal := r.sequence.Add(1)
	payload, err := json.Marshal(map[string]any{
		"wire":    kind,
		"runId":   runID,
		"ordinal": ordinal,
	})
	if err != nil {
		return
	}
	var trunc *content.Truncation
	if truncated {
		v := content.TruncCap
		trunc = &v
	}
	artifactID := uuid.NewString()
	if _, err := r.ledger.AppendArtifact(ctx, content.AppendArtifact{
		ID: artifactID, EntryID: entryID, MediaType: content.MediaText,
		Truncated: trunc, CaptureMethod: content.CaptureRawOutput,
		CaptureVersion: 1, Payload: string(payload),
	}); err != nil {
		return
	}
	_ = r.ledger.AppendChunk(ctx, artifactID, 1, body)
}
