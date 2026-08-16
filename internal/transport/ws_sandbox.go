package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/sandbox"
)

// sandboxHandlers answers sandbox.status through the one platform service
// injected at the composition root. It holds no server and therefore cannot
// reach unrelated stores.
type sandboxHandlers struct {
	svc sandbox.Service
	r   Responder
}

// handleStatus answers native-backend availability plus the advisory fixed
// opencode launch intent (ADR-0035). The Quick Connect surface renders either
// unavailable reason as a non-activatable row. An unwired service is a missing
// capability (-32601).
func (h sandboxHandlers) handleStatus(ctx context.Context, req jsonrpcRequest) {
	if h.svc == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox not available"})
		return
	}
	st := h.svc.Status(ctx)
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxStatusResponse{
		Available: st.Available,
		Backend:   st.Backend,
		Reason:    st.Reason,
		Detail:    st.Detail,
		ABI:       st.ABI,
		Intent:    sandbox.OpenCodeStatus(),
	}))
}

// sandboxStatusResponse is the wire shape of sandbox.status.
type sandboxStatusResponse struct {
	Available bool                 `json:"available"`
	Backend   string               `json:"backend"`
	Reason    string               `json:"reason,omitempty"`
	Detail    string               `json:"detail,omitempty"`
	ABI       int                  `json:"abi,omitempty"`
	Intent    sandbox.IntentStatus `json:"intent"`
}
