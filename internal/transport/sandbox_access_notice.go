package transport

import (
	"github.com/shady2k/nocx/internal/session"
)

// SandboxAccessNoticeCurrent confirms the launch still owns the pane's exact
// helper session before a best-effort renderer hint is published.
func (c *SandboxCoordinator) SandboxAccessNoticeCurrent(paneID, launchID string) bool {
	if c.root.Err() != nil {
		return false
	}
	head, _, err := c.diagnosticTarget(c.root, paneID, launchID)
	if err != nil || head.Helper == nil {
		return false
	}
	current, err := c.registry.Get(session.ID(head.Helper.SessionID))
	if err != nil || current.PaneID() != paneID {
		return false
	}
	binding, ok := c.registry.LaunchBinding(current.ID())
	return ok && binding.Mode == string(head.Mode) && binding.LaunchID == head.ID
}

// PublishSandboxAccessChanged is best-effort: the inbox remains authoritative,
// and a missed hint is recovered by the next explicit list request.
func (s *WSServer) PublishSandboxAccessChanged(change SandboxAccessChanged) {
	if change.PaneID == "" || change.LaunchID == "" {
		return
	}
	if s.sandbox == nil || !s.sandbox.SandboxAccessNoticeCurrent(change.PaneID, change.LaunchID) {
		return
	}
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()
	params := mustMarshal(change)
	for _, wc := range conns {
		_ = wc.TryNotify("sandbox.access.changed", params)
	}
}
