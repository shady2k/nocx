package transport

// session.focus (nocx-jiwq.1, plan D1): the backend asking the renderer to
// bring the pane holding a session to the front.
//
// It exists because a banner click has to land somewhere. The OS hands the
// click back with the session id the banner carried (internal/notify/
// wailsadapter), the shell can raise the window, and the tab is the part
// only the renderer can do — so the backend asks for a SESSION and the
// renderer resolves it with the one lookup it already owns
// (PaneManager.findBySession). The earlier design wanted a sessionID -> tab
// step inside the adapter; the backend cannot supply one at all, because
// Attribution.Tab is a WebSocket connection id rather than a tab
// (nocx-wyp3p), and every click was therefore discarded.
//
// A JSON-RPC notification on the existing control plane, not a new channel
// (AD-1). It is addressed the way every other session-scoped push is: the
// destination is resolved at emit time from the session's current subscriber
// (PublishLifecycle does the same), so a session no renderer holds is a drop
// rather than an error.

import "github.com/shady2k/nocx/internal/session"

// sessionFocusParams is the params object of the session.focus notification:
// a session id and nothing else (contracts/session.focus.schema.json).
// Contracted like every other unsolicited notification, because a
// server-initiated frame has no request to correlate against and nothing
// checking its shape at the call site.
type sessionFocusParams struct {
	SessionID string `json:"sessionId"`
}

// FocusSession asks the renderer holding sid to focus its pane. It is
// best-effort by design and says so by returning nothing: with no renderer
// attached the push is dropped, without error and without blocking. A click
// cannot be honoured by a renderer that is not there, and reporting a failure
// the caller could do nothing about would only stall the sink that carries
// it.
//
// It decides nothing about WHICH pane: the notification names the session and
// the renderer resolves it, or does nothing when the pane is gone.
func (s *WSServer) FocusSession(sessionID string) {
	if sessionID == "" {
		return
	}
	sid := session.ID(sessionID)
	rx := s.getRx(sid)
	if rx == nil {
		s.log.Debug("session.focus dropped: no receiver", "session", sessionID)
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		// Said out loud, because the drop is otherwise invisible: the user
		// clicked a banner and nothing moved.
		s.log.Debug("session.focus dropped: no subscriber", "session", sessionID)
		return
	}
	// TryNotify, not a blocking write: this runs on the notification click
	// callback, and a client whose queue is full must not hold it.
	if err := wconn.TryNotify("session.focus", mustMarshal(sessionFocusParams{SessionID: sessionID})); err != nil {
		s.log.Debug("write session.focus", "session", sessionID, "error", err)
	}
}
