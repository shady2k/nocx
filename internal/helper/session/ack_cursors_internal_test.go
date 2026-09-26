package session

import "github.com/shady2k/nocx/internal/helper/proto"

// TestAckedCursors reads one subscriber's two acknowledged cursors — the PTY
// stream's and the lifecycle stream's — so the external ack tests can see
// which half of an ack was applied.
func (s *Service) TestAckedCursors(id proto.HostSessionID, sub proto.SubscriberID) (pty, lifecycle proto.StreamOffset, ok bool) {
	hs, err := s.find(id)
	if err != nil {
		return 0, 0, false
	}
	hs.mu.Lock()
	sb, found := hs.subs[sub]
	hs.mu.Unlock()
	if !found {
		return 0, 0, false
	}
	sb.cursorMu.Lock()
	defer sb.cursorMu.Unlock()
	return sb.acked, sb.lifecycleAcked, true
}
