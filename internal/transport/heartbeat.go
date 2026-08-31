package transport

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
)

// DefaultHeartbeatReadWindow is the maximum interval without an inbound frame
// before the server releases a half-open WebSocket. A renderer sends its
// application heartbeat inside this window; ordinary control and data frames
// refresh it too, so active sessions never pay a heartbeat-specific cost.
const DefaultHeartbeatReadWindow = 30 * time.Second

type transportPingResult struct {
	ServerTimeMs int64 `json:"serverTimeMs"`
}

func (s *WSServer) heartbeatSpecs(immediate control.ImmediateSubmission) []methodSpec {
	return []methodSpec{
		regResponder(immediate, "transport.ping", noParams(), func(r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				result := transportPingResult{ServerTimeMs: time.Now().UnixMilli()}
				_ = r.TryResult(req.ID, mustMarshal(result))
			}
		}),
	}
}
