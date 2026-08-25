package transport

import (
	"errors"

	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/session"
)

// WithPaneGrid attaches the backend's pane-grid observer (nocx-szb40.2).
//
// When it is not wired, nothing is observed and every session runs exactly
// as before — the grid is an addition, never a dependency of the byte path.
func WithPaneGrid(o panegrid.Observer) WSServerOption {
	return func(s *WSServer) { s.paneGrid = o }
}

// The ENROLMENT ACT has no wire method yet, and that is deliberate rather
// than unfinished. A JSON-RPC method lands with its schema and its caller in
// the same commit; nothing in the renderer has a reason to observe a pane
// until nocx-szb40.3's driver reads a frame, and the frontend's dead-export
// ratchet refuses a generated contract type nobody imports — correctly. So
// enrolment is a Go seam today, wired at the composition root, and becomes
// session.observe when the thing that calls it exists.

// feedPaneGrid tees a session's bytes into its grid, if it has one.
//
// It sits on the BACKEND's read path — pumpToRing, which is started per
// session and lives connection-independently — and not on the subscriber
// path. That placement is the whole of the acceptance criterion "closing the
// frontend does not stop it being fed": there is no client in this code path
// to close.
func (s *WSServer) feedPaneGrid(sid session.ID, data []byte) {
	if s.paneGrid == nil {
		return
	}
	s.paneGrid.Feed(string(sid), data)
}

// withdrawPaneGrid closes the interval when the SESSION is done — called from
// monitorExit, which waits on sess.Done(). The amendment requires an interval
// with both ends, and this is the end that does not depend on the enrolling
// shell surviving to send its own withdrawal: a session that is over cannot
// produce another frame, so keeping its grid alive would hold an emulator for
// a pane nobody can observe.
//
// It used to sit after StartOutput in pumpToRing, on the belief that the call
// blocks until the output ends. It does not — it returns as soon as the
// handler is installed — so the withdrawal ran at session START, raced the
// enrolment, and left the interval with no second end at all.
func (s *WSServer) withdrawPaneGrid(sid session.ID) {
	if s.paneGrid == nil {
		return
	}
	s.paneGrid.Withdraw(string(sid))
}

// resizePaneGrid follows a pane's geometry for the life of the interval.
//
// It sits on the resize lane's APPLY, which is the only place that knows what
// size the session actually took: the lane coalesces, so the size a caller
// asked for is often not the size that landed. An unenrolled pane answers
// ErrNotEnrolled and that is the ordinary case — most panes never hold a grid
// and every one of them is resized — so it is not logged.
func (s *WSServer) resizePaneGrid(sid session.ID, cols, rows uint16) {
	if s.paneGrid == nil {
		return
	}
	if err := s.paneGrid.Resize(string(sid), int(cols), int(rows)); err != nil &&
		!errors.Is(err, panegrid.ErrNotEnrolled) {
		s.log.Warn("pane grid resize failed; the grid now answers at the wrong geometry",
			"session_id", string(sid), "cols", cols, "rows", rows, "error", err)
	}
}
