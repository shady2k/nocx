package transport

// workers.tabCreated (nocx-ui8q6.3): a connected renderer learns that
// workers.spawn minted a participant's tab, while the person is looking
// rather than after the next reload.
//
// WHY THIS IS A BROADCAST AND NOT A SESSION-SCOPED PUSH, unlike
// lifecycle.changed, files.changed and session.focus. Every one of those
// resolves its destination from a SESSION's current subscriber (the lane's
// session for lifecycle.changed, the binding's session for files.changed,
// the named session for session.focus) because the fact is about that
// session's own state, and a session with no renderer attached to it has
// nobody to tell. A worker participant's session is exactly that kind of
// session — internal/transport/session_open.go's OpenSession creates no ring
// and no subscriber, by design, for a session the backend opened rather than
// a renderer — so it NEVER has a subscriber to resolve, and addressing this
// notification the same way would make it undeliverable on every spawn, not
// merely on the ones nobody happens to be watching. What the fact is actually
// about is the shared layout chain (ws_layout_handlers.go's own comment:
// "every backend→renderer address remains a sessionId... because a tab holds
// several panes and 'the tab that spoke' is not well defined" — read the
// other way, an event that is NOT about one pane's session has no sessionId
// to be addressed by at all). Every connected window reads that chain with
// layout.read and would otherwise learn of the new row only on its next one,
// so every connected window is this notification's audience, exactly as
// notify.feed.changed and settings.changed already broadcast their own
// global facts to every connection rather than to one session's subscriber.
//
// BOTH DROP PATHS ARE LOGGED (nocx-n14oo.8's rule, applied here): the
// broadcast model's two ways to deliver nothing are "nobody is connected at
// all" and "a connected client's queue refused the frame", and a silent drop
// on either is exactly the class of defect that hid nocx-ndfqe for a week.
// notify.feed.changed and settings.changed predate that rule and stay quiet
// on both; this does not repeat that.

import (
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

// workerTabCreatedParams is the params object of the workers.tabCreated
// notification (contracts/workers.tabCreated.schema.json). tab and firstPane
// reuse tabWire and paneWire — the exact shapes tabs.create and panes.create
// already return — because a renderer that knows how to fold a tabs.create
// response into its layout cache folds this in the same way (AD-8: one
// shape, one owner).
type workerTabCreatedParams struct {
	Tab          tabWire  `json:"tab"`
	FirstPane    paneWire `json:"firstPane"`
	SessionID    string   `json:"sessionId"`
	InstanceID   string   `json:"instanceId"`
	SessionEpoch uint64   `json:"sessionEpoch"`
	ReplayFrom   uint64   `json:"replayFrom"`
	Attached     bool     `json:"attached"`
}

// AnnounceWorkerTab tells every connected client that workers.spawn minted
// tab/pane, whose pipe is sess. It is called once, after Spawn has committed
// to succeeding — never before every failure path that would still
// compensate (delete the tab) has been passed, or a renderer could be told
// about a tab that is yanked away moments later.
//
// replayFrom and attached are read from the session's own receiver exactly
// as sessions.live reads them (ws_reclaim.go's handleSessionsLive) — a second
// derivation of the same two facts is exactly the "second answer to one
// question" AGENTS.md warns against. A worker's session has no receiver at
// the moment it is spawned (OpenSession creates none), so both are their
// zero values then; the fields still ride the wire because a renderer that
// reconnects and asks sessions.live later would receive the very same
// shape, and a fact that never carries them would need a client-side special
// case for its own first frame.
func (s *WSServer) AnnounceWorkerTab(tab content.Tab, pane content.Pane, sess session.Session) {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	if len(conns) == 0 {
		s.log.Debug("workers.tabCreated dropped: no connections",
			"tab_id", tab.ID, "pane_id", pane.ID, "session_id", string(sess.ID()))
		return
	}

	ident := sess.Identity()
	params := workerTabCreatedParams{
		Tab:          wireTab(tab),
		FirstPane:    wirePane(pane),
		SessionID:    string(sess.ID()),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
	}
	if rx := s.getRx(sess.ID()); rx != nil {
		params.ReplayFrom = rx.ring.oldestLocked()
		wconn, _ := rx.getSubscriber()
		params.Attached = wconn != nil
	}
	payload := mustMarshal(params)

	sent := 0
	for _, wc := range conns {
		if err := wc.TryNotify("workers.tabCreated", payload); err != nil {
			s.log.Debug("write workers.tabCreated", "conn", wc.id, "tab_id", tab.ID, "error", err)
			continue
		}
		sent++
	}
	if sent == 0 {
		// Said out loud like the drop above: every connection refused the
		// frame (a full outbound queue on each), so the tab exists and no
		// window was told — otherwise indistinguishable from the connections
		// being empty in the first place.
		s.log.Debug("workers.tabCreated dropped: every connection refused it",
			"tab_id", tab.ID, "pane_id", pane.ID, "session_id", string(sess.ID()), "connections", len(conns))
	}
}

// workerTabClosedParams is the params object of the workers.tabClosed
// notification (contracts/workers.tabClosed.schema.json) — the reverse of
// workerTabCreatedParams above, and deliberately not its mirror field for
// field: what a window needs in order to TAKE A TAB OFF the strip is the id it
// was announced under, and the rest of what tabCreated carried (the row, its
// first pane, the session to attach to) is either the renderer's own cache by
// now or, for a tab whose session is over, nothing at all.
type workerTabClosedParams struct {
	TabID      string `json:"tabId"`
	InstanceID string `json:"instanceId"`
}

// AnnounceWorkerTabClosed tells every connected client that the participant's
// tab has left the window — written by workers.close (internal/app/workers.go's
// workerCloser), once the content store has committed the close and never
// before it: a window told about a close that then failed would take a tab off
// its strip while the row is still in the chain, which is the same class of
// lie in the other direction.
//
// The instanceId is the SERVER's own (registry.InstanceID), not a session's:
// the participant's session is very often already gone by the time its tab is
// closed — a finished worker is exactly that case — so reading the identity
// off a session the way AnnounceWorkerTab does would leave this notification
// unable to name the backend it came from at the moment it matters most.
func (s *WSServer) AnnounceWorkerTabClosed(tabID string) {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	// Both drop paths logged, for the reason the notification above gives:
	// "nobody is connected" and "every queue refused it" are the two ways a
	// broadcast delivers nothing, and a silent drop on either is exactly the
	// class of defect that hid nocx-ndfqe.
	if len(conns) == 0 {
		s.log.Debug("workers.tabClosed dropped: no connections", "tab_id", tabID)
		return
	}

	payload := mustMarshal(workerTabClosedParams{
		TabID:      tabID,
		InstanceID: string(s.registry.InstanceID()),
	})

	sent := 0
	for _, wc := range conns {
		if err := wc.TryNotify("workers.tabClosed", payload); err != nil {
			s.log.Debug("write workers.tabClosed", "conn", wc.id, "tab_id", tabID, "error", err)
			continue
		}
		sent++
	}
	if sent == 0 {
		s.log.Debug("workers.tabClosed dropped: every connection refused it",
			"tab_id", tabID, "connections", len(conns))
	}
}
