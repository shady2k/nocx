package transport

import (
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
)

// paneScreens is the transport's narrow view of the pane store (AD-8): what the
// emitting view reads, and what a session's end closes. It may not enrol, may
// not classify and may not type — the observation interval belongs to the
// enrolment act, and a second way to open one is the defect this seam exists to
// prevent.
type paneScreens interface {
	Frame(paneID string) (paneview.Frame, error)
	Withdraw(paneID string)
}

// WithPaneScreens attaches the store a pane's frame is read from.
//
// When it is not wired nothing is observed and every session runs exactly as
// before — the reading is an addition, never a dependency of the byte path.
func WithPaneScreens(s paneScreens) WSServerOption {
	return func(ws *WSServer) { ws.paneScreens = s }
}

// unwatchPane closes the observation when the SESSION is done — called from
// monitorExit, which waits on sess.Done().
//
// It is the end that does not depend on the enrolling shell surviving to send
// its own withdrawal: a session that is over cannot produce another frame, so
// keeping its watch alive would read a runtime that is about to be destroyed.
// The observation closes BEFORE the store, so a sweep that ran between the two
// would find no frame for a pane it is still watching — the ordinary race, and
// handled — but there is no reason to open it here.
func (s *WSServer) unwatchPane(sid session.ID) {
	if s.paneObserver != nil {
		s.paneObserver.Unwatch(string(sid))
	}
	if s.paneScreens != nil {
		s.paneScreens.Withdraw(string(sid))
	}
}
