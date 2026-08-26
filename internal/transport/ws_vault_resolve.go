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
	"errors"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
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

// handleResolveLine answers vault.resolveLine, in two halves with the
// material read between them and NO GATE HELD across it.
//
// The name → row handle → SecretID mapping is the vault's and the config
// store's, so it takes the operation (PlanLine). Reading the material is an
// operation-stance read: a sealed vault raises the unlock and this waits for
// a person to answer it — and the answer is vault.unseal, which needs the
// vault gate this operation would otherwise still be holding (nocx-o3606).
// So the plan comes out, the operation releases, and the reads happen here.
//
// A vault that is shut when the plan is built fails there, as the actionable
// sealed error the renderer already turns into the same dialog; a vault that
// shuts BETWEEN the halves is caught by the read, which now raises the unlock
// and continues rather than failing the line. Either way an unresolvable
// reference is reported, never silently substituted with nothing.
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
	var plan capability.LinePlan
	answered := false
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
		planned, planErr := svc.PlanLine(ctx, p.Line)
		if planErr != nil {
			_ = h.r.TryError(req.ID, vaultSecretError(-32603, "vault.resolveLine: ", planErr))
			answered = true
			return nil
		}
		plan = planned
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if answered {
		return
	}

	values, resolveErr := h.resolveLineMaterial(ctx, plan)
	if resolveErr != nil {
		_ = h.r.TryError(req.ID, vaultSecretError(-32603, "vault.resolveLine: ", resolveErr))
		return
	}
	line, refs := capability.SubstituteLine(plan, values)
	out := vaultResolveLineResponse{
		Line: line,
		Refs: make([]vaultResolveLineRef, 0, len(refs)),
	}
	for _, ref := range refs {
		out.Refs = append(out.Refs, vaultResolveLineRef{Name: ref.Name, Resolved: ref.Resolved})
	}
	_ = h.r.TryResult(req.ID, mustMarshal(out))
}

// resolveLineMaterial reads every planned reference, outside the operation.
//
// The two failures are deliberately different. A vault that shut, or an
// unlock the person dismissed, is an ERROR: a retry once the vault is open
// resolves differently, so answering with an unresolved reference would be a
// lie about what the command is going to run. Anything else — a reference
// whose secret was deleted since the inventory was read, a store that did
// not answer — leaves that one reference as written and reported unresolved,
// which is what the line contract has always said.
func (h vaultSecretHandlers) resolveLineMaterial(
	ctx context.Context, plan capability.LinePlan,
) (map[credential.SecretID]credential.Secret, error) {
	values := make(map[credential.SecretID]credential.Secret, len(plan.Refs))
	if h.secrets == nil {
		return values, nil
	}
	for _, ref := range plan.Refs {
		if ref.ID == "" {
			continue
		}
		if _, done := values[ref.ID]; done {
			continue
		}
		secret, err := h.secrets.Resolve(ctx, ref.ID, credential.Operation("resolve the command line"))
		switch {
		case err == nil:
			values[ref.ID] = secret
		case errors.Is(err, vault.ErrVaultSealed), errors.Is(err, ErrUnlockCancelled):
			return nil, err
		}
	}
	return values, nil
}
