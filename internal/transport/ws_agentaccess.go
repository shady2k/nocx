// agentAccess.list / agentAccess.forget — reading back the answers a person
// gave to "may this agent use nocx's tools", and unmaking one.
//
// The document behind them was write-only from the product's side. A denial is
// never silently retried (nocx-rowqt.12), which is right — a question that
// comes back every time is one a person can be worn down into answering yes —
// and it is only defensible while there is somewhere to reconsider. There was
// not: no surface read agent-approvals.json, so the way back was editing JSON
// in the profile directory by hand (nocx-6jbad).
//
// Facts on the wire, words in the renderer, exactly as the approval window
// itself now works: the path, the digest, the workspace NAME and the store's
// own two answers. The durable scope key is not sent in either direction —
// it is composed here, from the workspace, so its grammar keeps one owner.
package transport

import (
	"context"
	"encoding/json"
)

// AgentAccessRecord is one answer in the terms this surface speaks: three
// facts and the answer, with no durable scope key. The key's grammar lives
// where it is composed (internal/app) and is not spelled a second time here.
type AgentAccessRecord struct {
	Executable string
	Digest     string
	Workspace  string
	Answer     string
}

// AgentAccessStore is the seam: this file must not learn where the answers
// live, how they are written, or how a scope key is spelled — only that they
// can be listed and forgotten by the facts a person was shown.
type AgentAccessStore interface {
	ListAgentAccess() []AgentAccessRecord
	ForgetAgentAccess(executable, digest, workspace string) (bool, error)
}

type agentAccessAnswer struct {
	Executable string `json:"executable"`
	Digest     string `json:"digest"`
	Workspace  string `json:"workspace"`
	Answer     string `json:"answer"`
}

type agentAccessListResult struct {
	Answers []agentAccessAnswer `json:"answers"`
}

type agentAccessForgetParams struct {
	Executable string `json:"executable"`
	Digest     string `json:"digest"`
	Workspace  string `json:"workspace"`
}

type agentAccessForgetResult struct {
	Forgotten bool `json:"forgotten"`
}

func validateAgentAccessForgetRaw(raw json.RawMessage) string {
	var p agentAccessForgetParams
	if msg := decodeObject(raw, &p, "executable", "digest", "workspace"); msg != "" {
		return msg
	}
	if p.Executable == "" {
		return "executable is required"
	}
	if p.Workspace == "" {
		return "workspace is required"
	}
	if !isSHA256Hex(p.Digest) {
		return "digest must be a sha256 hex digest"
	}
	return ""
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

type agentAccessHandlers struct {
	store AgentAccessStore
	wired bool
	r     Responder
}

func (s *WSServer) agentAccessSpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.lane, "agentAccess.list", noParams(), func(r Responder) handlerFunc {
			h := agentAccessHandlers{store: s.agentAccess, wired: s.agentAccess != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleList(ctx, req) }
		}),
		regResponder(s.lane, "agentAccess.forget", params(validateAgentAccessForgetRaw), func(r Responder) handlerFunc {
			h := agentAccessHandlers{store: s.agentAccess, wired: s.agentAccess != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleForget(ctx, req) }
		}),
	}
}

// handleList answers with every remembered answer this surface can address.
// The store drops the rest: a row a person cannot act on is worse than no row.
func (h agentAccessHandlers) handleList(_ context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "agentAccess.list not available"})
		return
	}
	answers := []agentAccessAnswer{}
	for _, record := range h.store.ListAgentAccess() {
		// The wire type and the seam's record carry the same four strings by
		// design — one is the JSON shape, the other the boundary's vocabulary
		// — so the conversion is the whole mapping.
		answers = append(answers, agentAccessAnswer(record))
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentAccessListResult{Answers: answers}))
}

func (h agentAccessHandlers) handleForget(_ context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "agentAccess.forget not available"})
		return
	}
	var p agentAccessForgetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	forgotten, err := h.store.ForgetAgentAccess(p.Executable, p.Digest, p.Workspace)
	if err != nil {
		// The write failed and the answer still stands. Reported as an error
		// rather than forgotten:false, which would tell a person their
		// revocation landed when the agent is still admitted.
		_ = h.r.TryError(req.ID, RPCError{Code: -32000, Message: "could not forget this answer: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentAccessForgetResult{Forgotten: forgotten}))
}
