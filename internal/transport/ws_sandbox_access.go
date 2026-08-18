package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/shady2k/nocx/internal/sandbox"
)

type sandboxAccessHandlers struct {
	inbox *sandbox.AccessInbox
	r     Responder
}

type sandboxAccessListParams struct {
	Limit int `json:"limit,omitempty"`
}

func decodeSandboxAccessObject(raw json.RawMessage, dst any) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return "params must be a strict JSON object"
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "params must contain one JSON object"
	}
	return ""
}

type sandboxAccessResolveParams struct {
	EventID  string                 `json:"eventId"`
	Decision sandbox.AccessDecision `json:"decision"`
}

func validateSandboxAccessListRaw(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	var params sandboxAccessListParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	if params.Limit < 0 || params.Limit > 200 {
		return "limit must be between 0 and 200"
	}
	return ""
}

func validateSandboxAccessResolveRaw(raw json.RawMessage) string {
	var params sandboxAccessResolveParams
	if msg := decodeSandboxAccessObject(raw, &params); msg != "" {
		return msg
	}
	if !isLowerHex(params.EventID, 32) {
		return "eventId must be 32 lowercase hex characters"
	}
	switch params.Decision {
	case sandbox.AccessDecisionDismiss, sandbox.AccessDecisionGlobalReadOnly, sandbox.AccessDecisionGlobalReadWrite:
		return ""
	default:
		return "decision is invalid"
	}
}

func (h sandboxAccessHandlers) handleStatus(_ context.Context, req jsonrpcRequest) {
	if h.inbox == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox access inbox not available"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(h.inbox.Status()))
}

func (h sandboxAccessHandlers) handleList(_ context.Context, req jsonrpcRequest) {
	if h.inbox == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox access inbox not available"})
		return
	}
	var params sandboxAccessListParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(h.inbox.List(sandbox.AccessListOptions{Limit: params.Limit})))
}

func (h sandboxAccessHandlers) handleResolve(_ context.Context, req jsonrpcRequest) {
	if h.inbox == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox access inbox not available"})
		return
	}
	var params sandboxAccessResolveParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	event, err := h.inbox.Resolve(sandbox.AccessResolveRequest{EventID: params.EventID, Decision: params.Decision})
	if err == nil {
		_ = h.r.TryResult(req.ID, mustMarshal(event))
		return
	}
	switch {
	case errors.Is(err, sandbox.ErrAccessEventNotFound):
		_ = h.r.TryError(req.ID, RPCError{Code: -32020, Message: "sandbox access event not found"})
	case errors.Is(err, sandbox.ErrAccessEventResolved):
		_ = h.r.TryError(req.ID, RPCError{Code: -32021, Message: "sandbox access event already resolved"})
	case errors.Is(err, sandbox.ErrInvalidAccessDecision):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid decision"})
	default:
		// Persistence and path-validation errors are intentionally path-free:
		// the renderer already has the displayed directory and needs only the
		// stable refusal class.
		_ = h.r.TryError(req.ID, RPCError{Code: -32022, Message: "sandbox access grant rejected"})
	}
}
