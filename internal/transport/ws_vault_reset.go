package transport

import (
	"context"

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
	SystemKeychainReachable bool `json:"systemKeychainReachable"`
	VaultInitialized        bool `json:"vaultInitialized"`
}

type vaultResetResidueEntry struct {
	Store  string `json:"store"`
	Reason string `json:"reason,omitempty"`
}

type vaultResetResponse struct {
	SecretCount  int                      `json:"secretCount"`
	ProfileCount int                      `json:"profileCount"`
	Residue      []vaultResetResidueEntry `json:"residue"`
}

func (s *WSServer) handleVaultResetPreview(wconn *wsConn, req jsonrpcRequest) {
	if s.vaultReset == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "vault reset not available"))
		return
	}
	p, err := s.vaultReset.Preview(context.Background())
	if err != nil {
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "vault.resetPreview: ", err))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(vaultResetPreviewResponse{
		SecretCount:             p.Impact.SecretCount,
		ProfileCount:            p.Impact.ProfileCount,
		SystemKeychainReachable: p.SystemKeychainReachable,
		VaultInitialized:        p.VaultInitialized,
	})))
}

func (s *WSServer) handleVaultReset(wconn *wsConn, req jsonrpcRequest) {
	if s.vaultReset == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "vault reset not available"))
		return
	}
	result, err := s.vaultReset.Execute(context.Background())
	if err != nil {
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "vault.reset: ", err))
		return
	}

	// Empty, not nil. The contract declares an array and the renderer types it
	// as one; `residue: null` reaching a `.length` is the same defect this
	// project already shipped once on the inventory (nocx-25k9.14).
	residue := make([]vaultResetResidueEntry, 0, len(result.Residue))
	for _, r := range result.Residue {
		residue = append(residue, vaultResetResidueEntry{Store: r.Store, Reason: r.Reason})
	}

	s.broadcastVaultChanged()

	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(vaultResetResponse{
		SecretCount:  result.Impact.SecretCount,
		ProfileCount: result.Impact.ProfileCount,
		Residue:      residue,
	})))
}
