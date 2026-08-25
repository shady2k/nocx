package transport

// policy.get / policy.set — the ONE global agent policy (ADR-0020 §7 as
// amended 2026-08-16, accepted), ON THE WIRE because the settings surface
// edits it: the matrix the run grants are minted from (runGrantFor). The result shape is declared once in
// contracts/policy.get.schema.json, generated into the renderer, and the Go
// side is validated against it (DTO + over the socket, ws_contract_test.go).
//
// The set path is the person's configuration surface, and the tool-name rule
// (ADR-0028 decision 4: the grant is over resources and effects, never over
// tool names) is enforced HERE, by trying: the validator parses the policy
// with content.ParseEffectPolicy, and unknown keys — a tool name where an
// effect goes — are an invalid params error. There is no second vocabulary in
// which a tool name could be expressed.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// policyResult is the policy.get result: the resolved global policy, plus
// which of its rows govern anything at all. The matrix marshals as the
// canonical seven effect rows (EffectPolicy's wire form); the world gets no
// preset enum here — a person reads what a run may do, one effect at a time.
//
// Live is the second half of that sentence and the reason it is on the wire:
// seven rows are drawn, and today only two have a declared tool behind them.
// The settings surface cannot derive which — nothing in the matrix says —
// and the renderer must never map a tool name to an effect (ADR-0028
// decision 4), so the tool declaration table answers, once. This file does
// not read that table: the composition root does, and hands the answer in
// through WithLiveEffects. A control that governs nothing must not look like
// one that does.
type policyResult struct {
	Policy content.EffectPolicy `json:"policy"`
	Live   []content.Effect     `json:"live"`
}

// MarshalJSON sends an unset Live as [] rather than null. The contract
// declares live an array with no null branch, and "never a null" belongs to
// the shape rather than to every place that builds one — a nil slice
// reaching the wire is the defect the schemas caught the first time they
// ran, when vault.status marshalled providers as null.
func (p policyResult) MarshalJSON() ([]byte, error) {
	type wire policyResult // no MarshalJSON of its own: this is the recursion break
	w := wire(p)
	if w.Live == nil {
		w.Live = []content.Effect{}
	}
	return json.Marshal(w)
}

// policySetParams is the policy.set params: the matrix under "policy".
type policySetParams struct {
	Policy json.RawMessage `json:"policy"`
}

// policySetResult acknowledges a persisted policy.
type policySetResult struct {
	OK bool `json:"ok"`
}

// validatePolicySetRaw is the policy.set params validator: the envelope must
// parse, and the policy must BE a policy — strict matrix parse (unknown keys,
// a tool name, a bad decision or a bad scope are all refused here, so a
// config path that names a tool cannot exist).
func validatePolicySetRaw(raw json.RawMessage) string {
	var p policySetParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if len(p.Policy) == 0 {
		return "policy is required"
	}
	if _, err := content.ParseEffectPolicy(p.Policy); err != nil {
		return "policy: " + err.Error()
	}
	return ""
}

// policyHandlers serves policy.get and policy.set off one store seam, plus
// the live list the root named (static for the process's life, so it is
// copied in with the store rather than looked up per call).
type policyHandlers struct {
	store assistant.GlobalPolicy
	live  []content.Effect
	wired bool
	r     Responder
}

// policySpecs registers policy.get and policy.set on the lane submission:
// config-shaped work, no domain gate of its own (the lane is the admission).
func (s *WSServer) policySpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.lane, "policy.get", noParams(), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, live: s.liveEffects, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyGet(ctx, req) }
		}),
		regResponder(s.lane, "policy.set", params(validatePolicySetRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicySet(ctx, req) }
		}),
	}
}

// handlePolicyGet answers with the resolved policy (global default now; the
// workspace override resolves inside the same content.ResolvePolicy the mint
// uses, so the settings surface and the runs it configures read one value).
func (h policyHandlers) handlePolicyGet(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.get not available"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policyResult{
		Policy: h.store.Policy(),
		Live:   h.live,
	}))
}

// handlePolicySet persists a validated policy. The next ask run's grant is
// minted from it — no restart, the run mint reads the store live.
func (h policyHandlers) handlePolicySet(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.set not available"})
		return
	}
	var p policySetParams
	if msg := decodeObject(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.set: " + msg})
		return
	}
	policy, err := content.ParseEffectPolicy(p.Policy)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.set: " + err.Error()})
		return
	}
	if err := h.store.SetPolicy(policy); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "policy.set: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policySetResult{OK: true}))
}
