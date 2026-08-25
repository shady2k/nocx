package transport

import (
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

// withdrawPaneGrid closes the interval when a session's output ends. The
// amendment requires an interval with both ends, and this is the end: a
// session whose output is over cannot produce another frame, so keeping its
// grid alive would hold an emulator for a pane nobody can observe.
func (s *WSServer) withdrawPaneGrid(sid session.ID) {
	if s.paneGrid == nil {
		return
	}
	s.paneGrid.Withdraw(string(sid))
}
