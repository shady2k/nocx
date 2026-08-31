package transport

import (
	"context"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/transport/control"
)

// HostSessionInventory is the coordinator-side seam for asking active helper
// generations what they hold. Its result uses helper/client DTOs, never the
// frozen helper protocol types.
type HostSessionInventory interface {
	Sessions(context.Context) ([]helperclient.SessionEntry, error)
}

// WithHostSessionInventory wires the helper inventory read. Without it the
// method is unavailable; an unwired helper plane must not look like an empty
// answer, because an empty answer is safe to interpret as no sessions only
// after a helper actually answered.
func WithHostSessionInventory(inventory HostSessionInventory) WSServerOption {
	return func(s *WSServer) { s.hostSessionInventory = inventory }
}

type hostSessionInventoryResult struct {
	Sessions []helperclient.SessionEntry `json:"sessions"`
}

type hostSessionInventoryHandlers struct {
	inventory HostSessionInventory
	r         Responder
}

func (h hostSessionInventoryHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	entries, err := h.inventory.Sessions(ctx)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	if entries == nil {
		entries = []helperclient.SessionEntry{}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(hostSessionInventoryResult{Sessions: entries}))
}

func (s *WSServer) hostSessionInventorySpecs(sub control.Submission) []methodSpec {
	return []methodSpec{regResponder(sub, "sessions.inventory", noParams(), func(r Responder) handlerFunc {
		h := hostSessionInventoryHandlers{inventory: s.hostSessionInventory, r: r}
		return func(ctx context.Context, req jsonrpcRequest) { h.handle(ctx, req) }
	})}
}
