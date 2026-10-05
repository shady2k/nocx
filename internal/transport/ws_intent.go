package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/session"
)

const (
	maxSessionIntentBytes    = 1 << 20
	sessionIntentBudgetNanos = int64(5_000_000_000)
)

type sessionIntentParams struct {
	SessionID   string `json:"sessionId"`
	AccessEpoch uint64 `json:"accessEpoch"`
	Kind        string `json:"kind"`
	Payload     []byte `json:"payload"`
}

type sessionIntentResult struct {
	State        string               `json:"state"`
	BytesWritten int                  `json:"bytesWritten"`
	FenceAfter   uint64               `json:"fenceAfter"`
	RetryAfterMs int                  `json:"retryAfterMs,omitempty"`
	Refusal      *proto.IntentRefusal `json:"refusal,omitempty"`
}

func validateSessionIntentRaw(raw json.RawMessage) string {
	var p sessionIntentParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "invalid session.intent params"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "invalid session.intent params"
	}
	for name := range fields {
		switch name {
		case "sessionId", "accessEpoch", "kind", "payload":
		default:
			return "invalid session.intent params: unknown field " + name
		}
	}
	if p.SessionID == "" || p.AccessEpoch == 0 || (p.Kind != "key" && p.Kind != "text" && p.Kind != "paste") || len(p.Payload) > maxSessionIntentBytes {
		return "invalid session.intent params"
	}
	if _, err := session.IDToBytes(session.ID(p.SessionID)); err != nil {
		return "invalid session.intent params: invalid sessionId"
	}
	return ""
}

type sessionIntentHandler struct {
	source paneIntentSource
	r      Responder
	state  *connState
}

func (h sessionIntentHandler) handle(ctx context.Context, req jsonrpcRequest) {
	var p sessionIntentParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if !h.state.has(session.ID(p.SessionID)) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	if h.source == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: session intent not wired"})
		return
	}
	// This is structured intent on the control plane, not PTY bytes. The
	// helper's runtime encodes it against the program's current modes.
	result, err := h.source.Intent(ctx, p.SessionID, proto.IntentParams{
		Interactive: true,
		AccessEpoch: p.AccessEpoch,
		CommitBy:    int64(monoclock.Now()) + sessionIntentBudgetNanos,
		Kind:        p.Kind,
		Payload:     p.Payload,
	})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("session.intent: %v", err)})
		return
	}
	raw, err := json.Marshal(sessionIntentResult{
		State: result.State, BytesWritten: result.BytesWritten,
		FenceAfter: result.FenceAfter, RetryAfterMs: result.RetryAfterMs,
		Refusal: result.Refusal,
	})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Internal error"})
		return
	}
	_ = h.r.TryResult(req.ID, raw)
}
