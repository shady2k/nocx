package transport

import (
	"github.com/shady2k/nocx/internal/session"
)

type toolSurfaceStatus struct {
	available bool
	reason    string
}

type toolSurfaceChangedParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
}

// BroadcastToolSurface retains and publishes the endpoint's settled launch
// result. Retention matters because the endpoint may settle before a renderer
// attaches; replay then gives a new subscriber the same state.
func (s *WSServer) BroadcastToolSurface(sessionID, status, reason string) {
	sid := session.ID(sessionID)
	if sid == "" || status == "" {
		return
	}
	s.toolSurfaceMu.Lock()
	if s.toolSurfaces == nil {
		s.toolSurfaces = make(map[session.ID]toolSurfaceStatus)
	}
	s.toolSurfaces[sid] = toolSurfaceStatus{available: status == "available", reason: reason}
	s.toolSurfaceMu.Unlock()
	s.emitToolSurface(sid)
}

func (s *WSServer) emitToolSurface(sid session.ID) {
	s.toolSurfaceMu.Lock()
	status, ok := s.toolSurfaces[sid]
	s.toolSurfaceMu.Unlock()
	if !ok {
		return
	}
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	sess, err := s.registry.Get(sid)
	if err != nil {
		return
	}
	ident := sess.Identity()
	params := toolSurfaceChangedParams{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Status:       "unavailable",
		Reason:       status.reason,
	}
	if status.available {
		params.Status = "available"
		params.Reason = ""
	}
	if err := wconn.TryNotify("session.toolSurfaceChanged", mustMarshal(params)); err != nil {
		s.log.Debug("write session.toolSurfaceChanged", "session", sid, "error", err)
	}
}

func (s *WSServer) replayToolSurface(sid session.ID) {
	s.emitToolSurface(sid)
}
