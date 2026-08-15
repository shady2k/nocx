package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/vaultreset"
)

// VaultResetService is the reset orchestrator seen from the transport. Two
// methods, both of which the renderer calls directly and neither of which
// takes anything the renderer supplies: what a reset destroys is decided
// entirely by what is already stored.
type VaultResetService interface {
	Preview(ctx context.Context) (vaultreset.Preview, error)
	Execute(ctx context.Context) (vaultreset.Result, error)
}

// WithVaultReset attaches the reset orchestrator, enabling vault.resetPreview
// and vault.reset.
func WithVaultReset(s VaultResetService) WSServerOption {
	return func(ws *WSServer) { ws.vaultReset = s }
}

// Wire shapes. Both are pinned by contracts/vault.reset*.schema.json and the
// renderer's types are generated from those files, so a field added here that
// is not added there fails the contract test rather than reaching a renderer
// that cannot see it.

type vaultResetPreviewResponse struct {
	SecretCount             int  `json:"secretCount"`
	ProfileCount            int  `json:"profileCount"`
	EndpointCount           int  `json:"endpointCount"`
	SystemKeychainReachable bool `json:"systemKeychainReachable"`
	VaultInitialized        bool `json:"vaultInitialized"`
}

type vaultResetResidueEntry struct {
	Store  string `json:"store"`
	Reason string `json:"reason,omitempty"`
}

type vaultResetResponse struct {
	SecretCount   int                      `json:"secretCount"`
	ProfileCount  int                      `json:"profileCount"`
	EndpointCount int                      `json:"endpointCount"`
	Residue       []vaultResetResidueEntry `json:"residue"`
}

// vaultResetHandlers answers vault.resetPreview and vault.reset. Reset is
// deliberately its own operation: it must work on a vault that is broken or
// half-built, so VaultResetOperation is built from the reset orchestrator
// alone, independent of the vault lifecycle. The handler holds the operation,
// the Responder and the vault.changed fan-out; nothing else.
type vaultResetHandlers struct {
	op      capability.VaultResetOperation // nil → reset not wired
	r       Responder
	machine vaultMachine
}

func (h vaultResetHandlers) handleResetPreview(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "vault reset not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.VaultResetService) error {
		p, err := svc.Preview(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "vault.resetPreview: ", err))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(vaultResetPreviewResponse{
			SecretCount:             p.Impact.SecretCount,
			ProfileCount:            p.Impact.ProfileCount,
			EndpointCount:           p.Impact.EndpointCount,
			SystemKeychainReachable: p.SystemKeychainReachable,
			VaultInitialized:        p.VaultInitialized,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

func (h vaultResetHandlers) handleReset(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "vault reset not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.VaultResetService) error {
		result, err := svc.Execute(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "vault.reset: ", err))
			return nil
		}

		// Empty, not nil. The contract declares an array and the renderer types it
		// as one; `residue: null` reaching a `.length` is the same defect this
		// project already shipped once on the inventory (nocx-25k9.14).
		residue := make([]vaultResetResidueEntry, 0, len(result.Residue))
		for _, r := range result.Residue {
			residue = append(residue, vaultResetResidueEntry{Store: r.Store, Reason: r.Reason})
		}

		h.machine.broadcastVaultChanged()

		_ = h.r.TryResult(req.ID, mustMarshal(vaultResetResponse{
			SecretCount:   result.Impact.SecretCount,
			ProfileCount:  result.Impact.ProfileCount,
			EndpointCount: result.Impact.EndpointCount,
			Residue:       residue,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}
