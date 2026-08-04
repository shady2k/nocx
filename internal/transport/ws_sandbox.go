package transport

import "context"

// handleSandboxStatus answers sandbox.status with the backend availability
// status {available, backend, reason?, detail?, abi?} (design spec §4.2). The
// Quick Connect surface renders the non-activatable "Sandbox unavailable"
// row from the reason. Without a wired service the method is unavailable
// (-32601), like every other capability seam.
func (s *WSServer) handleSandboxStatus(wconn *wsConn, req jsonrpcRequest) {
	if s.sandboxSvc == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "sandbox not available"))
		return
	}
	st := s.sandboxSvc.Status(context.Background())
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(sandboxStatusResponse{
		Available: st.Available,
		Backend:   st.Backend,
		Reason:    st.Reason,
		Detail:    st.Detail,
		ABI:       st.ABI,
	})))
}

// sandboxStatusResponse is the wire shape of sandbox.status.
type sandboxStatusResponse struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	ABI       int    `json:"abi,omitempty"`
}
