package transport

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// WithProfileUsageStore attaches a profile usage tracker for the
// sessions.status JSON-RPC method (nocx-uxs5.4). When not wired, the
// handler still reports live sessions but last-used timestamps are
// unavailable.
func WithProfileUsageStore(t session.ProfileUsageTracker) WSServerOption {
	return func(s *WSServer) { s.profileUsage = t }
}

// sessionsStatusParams is the payload of the "sessions.status" RPC method.
type sessionsStatusParams struct {
	ProfileIDs []string `json:"profileIds"`
}

// sessionsStatusResult carries the response for sessions.status.
type sessionsStatusResult struct {
	Statuses map[string]profileSessionStatus `json:"statuses"`
}

// profileSessionStatus reports whether a profile has a live session and
// when it was last used.
type profileSessionStatus struct {
	Live     bool   `json:"live"`
	LastUsed string `json:"lastUsed,omitempty"`
}

// sessionsStatusHandlers answers sessions.status. It holds the whole-domain
// SessionOperation (built in seamSpecs over the session gate) and the
// Responder; every registry and tracker read goes through the service inside
// op.Run.
type sessionsStatusHandlers struct {
	op  capability.SessionOperation
	r   Responder
	log log.Logger
}

// handleSessionsStatus reports, for a set of profile IDs, which are live
// right now and when each was last used — one call for the whole list.
// The frontend renders ~40 rows; this avoids N round trips.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"sessions.status","params":{"profileIds":["ssh:p1:1","ssh:p2:2"]}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"statuses":{"ssh:p1:1":{"live":true,"lastUsed":"2026-07-29T12:00:00Z"},"ssh:p2:2":{"live":false,"lastUsed":"2026-07-29T11:30:00Z"}}}}
func (h sessionsStatusHandlers) handleSessionsStatus(ctx context.Context, req jsonrpcRequest) {
	var params sessionsStatusParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		// Build a set of profile IDs that currently have live sessions.
		liveProfiles := make(map[string]string, 8) // profileID -> sessionID (not exposed in v1)
		for _, sess := range svc.List() {
			if pid := sess.ProfileID(); pid != "" {
				liveProfiles[pid] = string(sess.ID())
			}
		}

		// Query persisted last-used timestamps.
		var lastUsed map[string]time.Time
		lu, err := svc.LastUsedForProfiles(params.ProfileIDs)
		if err != nil {
			h.log.Warn("sessions.status: failed to query last-used", "error", err)
			// Continue with nil lastUsed — live state is still valid.
		} else {
			lastUsed = lu
		}

		result := sessionsStatusResult{
			Statuses: make(map[string]profileSessionStatus, len(params.ProfileIDs)),
		}
		for _, pid := range params.ProfileIDs {
			st := profileSessionStatus{
				Live: liveProfiles[pid] != "",
			}
			if lastUsed != nil {
				if t, ok := lastUsed[pid]; ok {
					st.LastUsed = t.Format(time.RFC3339)
				}
			}
			result.Statuses[pid] = st
		}

		_ = h.r.TryResult(req.ID, mustMarshal(result))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}
