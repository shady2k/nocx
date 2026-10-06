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

// sessionIntentKinds is the wire's closed kind set: the whole of
// sessionruntime's intent vocabulary and nothing else (nocx-zg3k3.3.1). It is a
// set rather than a chain of comparisons because the kinds are what the RUNTIME
// encodes, and a kind this protocol does not carry is the CALLER's error — a
// spelling missing here must be refused as a bad request rather than forwarded
// as an intent the runtime would then answer cannot_encode.
var sessionIntentKinds = map[string]struct{}{
	"key": {}, "text": {}, "paste": {}, "mouse": {}, "focus": {},
}

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
	// AccessEpoch is the epoch in force when the pane's session decided this
	// (nocx-zg3k3.3.1): the number the renderer presents with its next
	// intent, and the one a refusal answers with so the intent just refused
	// can be sent again.
	AccessEpoch uint64 `json:"accessEpoch,omitempty"`
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
	// AccessEpoch is deliberately NOT required (nocx-zg3k3.3.1): a controller
	// that has none yet sends none, and the session refuses the intent with
	// the epoch in force rather than this layer inventing one. The refusal
	// writes nothing, and the result names the epoch to present next.
	_, knownKind := sessionIntentKinds[p.Kind]
	if p.SessionID == "" || !knownKind || len(p.Payload) > maxSessionIntentBytes {
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
		Refusal: result.Refusal, AccessEpoch: result.AccessEpoch,
	})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Internal error"})
		return
	}
	_ = h.r.TryResult(req.ID, raw)
}
