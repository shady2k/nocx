package transport

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/sandbox"
)

// sandboxHandlers answers sandbox.status through the one platform service
// injected at the composition root. It holds no server and therefore cannot
// reach unrelated stores.
type sandboxHandlers struct {
	svc sandbox.Service
	r   Responder
}

// handleStatus answers native-backend availability. The Quick Connect surface
// renders an unavailable backend as a non-activatable row. An unwired service
// is a missing capability (-32601).
func (h sandboxHandlers) handleStatus(ctx context.Context, req jsonrpcRequest) {
	if h.svc == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "sandbox not available"})
		return
	}
	st := h.svc.Status(ctx)
	_ = h.r.TryResult(req.ID, mustMarshal(sandboxStatusResponse{
		Learn:   st.Learn,
		Enforce: st.Enforce,
	}))
}

func sandboxRPCError(code int, message, reason string) RPCError {
	return RPCError{Code: code, Message: message, Data: &vaultErrorData{Reason: reason}}
}

func sandboxOpenError(err error) (RPCError, bool) {
	var statusErr *sandbox.StatusError
	if errors.As(err, &statusErr) {
		reason := statusErr.Status.Enforce.Reason
		if reason == "" {
			reason = statusErr.Status.Reason
		}
		return sandboxRPCError(-32011, "Filesystem sandbox backend unavailable", reason), true
	}
	var setupErr *sandbox.SetupError
	if errors.As(err, &setupErr) {
		reason := setupErr.Reason
		if reason == "" {
			reason = "setup-failed"
		}
		code := -32012
		if reason == sandbox.ReasonPolicyTooLarge {
			code = -32007
		}
		return sandboxRPCError(code, "Filesystem sandbox setup failed", reason), true
	}
	return RPCError{}, false
}

// sandboxStatusResponse is the wire shape of sandbox.status.
type sandboxStatusResponse struct {
	Learn   sandbox.ModeStatus `json:"learn"`
	Enforce sandbox.ModeStatus `json:"enforce"`
}
