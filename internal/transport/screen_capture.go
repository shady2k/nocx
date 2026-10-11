package transport

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/assistant"
)

// ScreenCaptureSource reads the active buffer from the helper that owns the
// session. It returns the assistant's frame-body JSON shape so the executor
// consumes the same absolute range and identity the backend captured.
type ScreenCaptureSource interface {
	CaptureScreen(ctx context.Context, sessionID string, region *assistant.FrameRegion) (json.RawMessage, error)
}

// WithScreenCaptureSource wires the backend-owned session-runtime capture used
// by the assistant's session.read tool. It does not depend on an attached
// renderer or its local cell model.
func WithScreenCaptureSource(source ScreenCaptureSource) WSServerOption {
	return func(ws *WSServer) { ws.screenCaptureSource = source }
}

// RequestScreen supplies the assistant's session.read source from the owning
// backend runtime. It never asks an attached renderer to reconstruct a screen.
func (s *WSServer) RequestScreen(ctx context.Context, sessionID string, region *assistant.FrameRegion) (json.RawMessage, error) {
	if s.screenCaptureSource == nil {
		return nil, errors.New("session.read: no backend screen capture source is wired")
	}
	return s.screenCaptureSource.CaptureScreen(ctx, sessionID, region)
}
