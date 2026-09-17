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

// paneAdmissions is the transport's narrow view of the ADMISSION INTERVAL a
// pane's enrolment opened (AD-8, ADR-0058): a session that is over holds no
// admitted tool caller.
//
// It is a seam of its own rather than part of the pane store, because the thing
// that ends is not a reading of the screen: closing the watch and withdrawing
// the frame say nothing about the grant an agent's tool connection is holding,
// and that grant is what outlives a session nobody ended explicitly
// (nocx-9mn6z).
type paneAdmissions interface {
	// SessionEnded ends the interval a session's connections were admitted
	// under. Ending is idempotent and cannot fail: a caller racing a session
	// teardown must not have to care who won.
	SessionEnded(sessionID string)
}

// WithPaneAdmissions attaches the end of the admission interval.
//
// When it is not wired, a session's end closes the observation and the frame
// and leaves anything an agent's tool connection was admitted under alone —
// which is what the transport did before this seam existed, and is why the
// composition root wires it.
func WithPaneAdmissions(a paneAdmissions) WSServerOption {
	return func(ws *WSServer) { ws.paneAdmissions = a }
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
//
// It is also the end that a session's own death reaches — an explicit close and
// a program that exited both surface as sess.Done() — so the ADMISSION goes with
// it, from the same call and in the same place. Leaving that to the enrolling
// shell's withdrawal was the defect (nocx-9mn6z): a pane whose shell never
// returned kept a live tool connection, and with it a grant nobody held, for as
// long as the socket happened to stay open.
func (s *WSServer) unwatchPane(sid session.ID) {
	if s.paneObserver != nil {
		s.paneObserver.Unwatch(string(sid))
	}
	if s.paneScreens != nil {
		s.paneScreens.Withdraw(string(sid))
	}
	if s.paneAdmissions != nil {
		s.paneAdmissions.SessionEnded(string(sid))
	}
}
