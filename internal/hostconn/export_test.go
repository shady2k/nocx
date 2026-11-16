package hostconn

// Test seams for the guard, in the file the dead-code ratchet keeps them in: an
// `_test.go` file is never compiled into a binary, so a seam here is not
// production API (`.githooks/check-deadcode.mjs`).

// ControlEdgesSeen answers how many OS control edges this connection's guard has
// FINISHED processing — each applied, or refused as older than the edge already
// in hand — since the connection was dialled. A refused edge produces no state
// change and no other trace, so this count is how a test that must assert the
// ABSENCE of an effect after an edge reads it after the guard was certainly done
// with it, instead of after a pause.
func ControlEdgesSeen(s *Supervisor) uint64 {
	s.mu.Lock()
	l := s.live
	s.mu.Unlock()
	if l == nil {
		return 0
	}
	return l.controlProcessed.Load()
}

func (s *Supervisor) RetryScheduled() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.retryScheduled }
