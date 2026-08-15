package transport

// vault.resolveLine — the backend half of the reference seam
// ({{secret:NAME}} in a command line). A line may carry references to vault
// secrets BY NAME — the name vault.inventory reports, which is the only
// identifier a person can type; the opaque sec:v1:... reference and the
// secrow:... row handle are both minted ids nobody should ever put in a
// line, and parsing the reference grammar is private to internal/vault
// (.internal/plans/2026-07-30-vault-v1.md).
//
// The result shape is declared once in contracts/vault.resolveLine.schema.json.
//
// The invariant, and the whole point of the method: the resolved value goes
// to the caller for the PTY write and nowhere else. history.record receives
// the line with the REFERENCE intact — a command carrying a reference moves
// to another machine and resolves that machine's secret; a command carrying
// a pasted key is both dead and dangerous. The value is never logged, never
// persisted, never put in a finding or a ref — the refs list carries only
// the name and whether it resolved.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
)

// vaultResolveLineParams is the request: the line to substitute references
// in. There is deliberately no params schema (contracts/README.md): the
// handler is the check.
type vaultResolveLineParams struct {
	Line string `json:"line"`
}

// vaultResolveLineRef is one reference in the line, reported so an
// unresolved name is never silently left as literal text. Name is the
// reference as written ({{secret:NAME}}); Resolved is false when the vault
// holds no secret with that name or its store did not answer — the caller
// must surface that instead of running the literal reference.
type vaultResolveLineRef struct {
	Name     string `json:"name"`
	Resolved bool   `json:"resolved"`
}

// vaultResolveLineResponse is the result of vault.resolveLine. Line is the
// substituted line — it may carry resolved secret values, and the caller
// must not persist it. Refs is never nil: no references is [].
type vaultResolveLineResponse struct {
	Line string                `json:"line"`
	Refs []vaultResolveLineRef `json:"refs"`
}

// handleResolveLine answers vault.resolveLine. The whole reference seam
// lives in the SecretService: the name → row handle → SecretID → value
// resolution, the sealed-mid-flight error (actionable -32001/vault-sealed,
// distinct from "no such secret") and the no-references identity shortcut.
// The handler only re-shapes the refs for the wire.
func (h vaultSecretHandlers) handleResolveLine(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: h.notWired})
		return
	}
	var p vaultResolveLineParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
		line, refs, err := svc.ResolveLine(ctx, p.Line)
		if err != nil {
			_ = h.r.TryError(req.ID, vaultSecretError(-32603, "vault.resolveLine: ", err))
			return nil
		}
		out := vaultResolveLineResponse{
			Line: line,
			Refs: make([]vaultResolveLineRef, 0, len(refs)),
		}
		for _, ref := range refs {
			out.Refs = append(out.Refs, vaultResolveLineRef{Name: ref.Name, Resolved: ref.Resolved})
		}
		_ = h.r.TryResult(req.ID, mustMarshal(out))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}
