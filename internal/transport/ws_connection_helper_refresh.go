package transport

// connection.helperRefresh — the one action a person may take on a far helper
// that was serving and is gone (ADR-727g63tr decision 2; design §5.10 lifecycle
// scenario 4).
//
// # Why the press names a pane and an epoch
//
// The offer is drawn on pane.connection, which is keyed by the renderer's pane
// and ordered by that pane's epoch. So the press is checked against THE SAME
// frame the person is looking at: a pane this connection did not open has no
// offer to press, a stale epoch is about an axis that has moved on, and an axis
// without the refresh offer — serving, starting, a mode, a refused consent, a
// failed install — has no button that could produce this call. The guard is
// held here, in the transport, because it is the party that recorded what it
// told the renderer.
//
// # What it does, and what it never does
//
// It wakes the far helper's owner to redial the helper already installed and
// complete a new hello. It never installs, and no failure of the redial is
// reported through this call: the axis changes on pane.connection, which is
// where the person watches it.

import (
	"context"
	"encoding/json"
)

const helperRefreshRefusal = "no helper refresh is offered for this pane"

// helperRefreshPolicy is the far helper axis's action seam. The composition
// root adapts internal/panedriver to it, the same way pane.reconnect's policy
// is adapted: the transport holds the frame's offer and the driver holds the
// binding, and neither learns the other's business (AD-8).
type helperRefreshPolicy interface {
	RefreshHelper(context.Context, string, uint64) error
}

// connectionHelperRefreshParams is the request, declared once
// (contracts/connection.helperRefresh.params.schema.json) and pinned by the
// contract tests.
type connectionHelperRefreshParams struct {
	PaneID          string `json:"paneId"`
	ConnectionEpoch uint64 `json:"connectionEpoch"`
}

type connectionHelperRefreshResult struct {
	ConnectionID string `json:"connectionId"`
	Refreshed    bool   `json:"refreshed"`
}

// validateConnectionHelperRefreshRaw is the method's runtime half of the
// schema. The shape is pane.reconnect's exactly — a pane and the epoch that
// offered the action — so the check is that one, not a second copy that could
// drift from it.
func validateConnectionHelperRefreshRaw(raw json.RawMessage) string {
	return validatePaneReconnectRaw(raw)
}

// connectionHelperRefreshHandlers answers `connection.helperRefresh`.
type connectionHelperRefreshHandlers struct {
	machine *WSServer
	r       Responder
	w       *wsConn
}

func (h connectionHelperRefreshHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	var params connectionHelperRefreshParams
	if msg := decodeObject(req.Params, &params, "paneId", "connectionEpoch"); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if msg := requireFields(req.Params, "paneId", "connectionEpoch"); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if msg := validateStringBound("paneId", params.PaneID, maxIDRunes); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if params.ConnectionEpoch == 0 {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: connectionEpoch must be at least 1"))
		return
	}

	h.machine.paneMu.Lock()
	rec := h.machine.paneRecords[params.PaneID]
	var connectionID string
	var refusal string
	switch {
	case rec == nil || rec.wconn != h.w:
		refusal = "unknown paneId"
	case params.ConnectionEpoch != h.machine.paneEpochs[params.PaneID]:
		refusal = "stale connectionEpoch"
	case !rec.helperRefresh:
		refusal = helperRefreshRefusal
	default:
		connectionID = rec.connectionID
	}
	h.machine.paneMu.Unlock()
	if refusal != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+refusal))
		return
	}

	policy, ok := h.machine.paneConnectionPolicy.(helperRefreshPolicy)
	if !ok {
		_ = respond(h.r, newJSONRPCError(req.ID, -32603, "Internal error: Helper refresh is unavailable (no far helper owner wired)"))
		return
	}
	if err := policy.RefreshHelper(rec.ctx, params.PaneID, params.ConnectionEpoch); err != nil {
		// The offer was ours a moment ago and is not any more: the axis moved
		// between the frame and the press. That is a refused press, not a
		// server fault, and the log keeps the real cause.
		h.machine.log.Warn("helper refresh refused", "pane", params.PaneID, "error", err)
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+helperRefreshRefusal))
		return
	}
	raw, _ := json.Marshal(connectionHelperRefreshResult{ConnectionID: connectionID, Refreshed: true})
	_ = respond(h.r, newJSONRPCResult(req.ID, raw))
}
