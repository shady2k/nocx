package sessionruntime

import (
	"github.com/shady2k/nocx/internal/emulator"
)

// ApplyScrollback applies the session's scrollback budget — how far the live
// terminal scrolls back, the person's terminal.scrollbackLines — to the
// emulator this runtime directs.
//
// It is the ONE emulator-configuration path for retention, beside
// [Session.CommitGeometry]'s for size: a caller with a new budget hands the
// number here rather than reaching the emulator behind the runtime's back,
// and the runtime's mutex makes the application indivisible against a feed —
// the adapter re-baselines its departed-rows measurement across the
// application, and that must not interleave with an ingest reading the same
// depth.
//
// The emulator carries the capability or it does not. One that cannot carry
// a budget refuses with [emulator.ErrUnsupported] — a named answer the
// caller acts on, never a silent no-op: a control that changed nothing must
// not report success.
//
// A session that has ended has no terminal to configure and is refused with
// [ErrUnavailable].
func (s *Session) ApplyScrollback(maxLines uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	budget, ok := s.emulator.(emulator.ScrollbackBudget)
	if !ok {
		return emulator.ErrUnsupported
	}
	return budget.ApplyScrollback(maxLines)
}
