package transport

// The pane-observation control plane (nocx-szb40.3; contracts/
// session.observationChanged.schema.json).
//
// The grid lives in the backend because a TUI sends diffs and a frame cannot
// be reconstructed from the tail of a stream. So the classification is done
// where the grid is, and what crosses is a TYPED FACT under AD-1 — never
// bytes, and never a screen. This file is the crossing and nothing else: it
// decides no state, and the two things it knows that the watcher does not are
// which connection is subscribed to a session and what that session's identity
// is.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
)

// paneObserverSweep is how often the backend asks its watched panes what they
// are now.
//
// It is a COALESCER, not a poll: Touch marks a pane dirty on the session read
// path, and a pane that has not moved costs nothing at all. The interval is
// what keeps an agent that repaints its token counter on every response chunk
// from producing a classification per chunk. Nothing waits on it — a test
// drives Sweep directly and asserts on the state change it produces, which is
// why no test in this repository depends on this number.
const paneObserverSweep = 120 * time.Millisecond

// paneObserver is the transport's half of the seam (AD-8). Narrow on purpose:
// the transport may say a pane moved, may close an observation when the
// session ends, and may ask what a pane currently is. It may not classify.
type paneObserver interface {
	Touch(paneID string)
	Unwatch(paneID string)
	Sweep()
	Snapshot(paneID string) (paneobserve.Observation, bool)
}

// WithPaneObserver attaches the backend's pane-observation watcher.
//
// When it is not wired nothing is classified and every session runs exactly as
// before — like the grid it reads, the observation is an addition and never a
// dependency of the byte path.
func WithPaneObserver(o paneObserver) WSServerOption {
	return func(s *WSServer) { s.paneObserver = o }
}

// observationChangedParams is the notification's DTO. Contracted, like every
// other server-initiated fact: nothing correlates it and nothing checks its
// shape at a call site, which is exactly why the schema exists.
type observationChangedParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	Agent        string `json:"agent"`
	State        string `json:"state"`
}

// runPaneObserverSweeps drives the coalescer for the life of the server.
func (s *WSServer) runPaneObserverSweeps(ctx context.Context) {
	t := time.NewTicker(paneObserverSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.paneObserver.Sweep()
		}
	}
}

// EmitPaneObservation is where the watcher reports to. Bound
// post-construction at the composition root, for the same reason the lifecycle
// publisher's emitter is: the server is built after the things that enrol into
// it, and the window before the binding is empty because no pane can have been
// enrolled yet.
func (s *WSServer) EmitPaneObservation(o paneobserve.Observation) {
	s.emitPaneObservation(session.ID(o.PaneID), o)
}

func (s *WSServer) emitPaneObservation(sid session.ID, o paneobserve.Observation) {
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		// Nobody is attached. This is not a loss: the observation is a
		// STATE, the watcher still holds it, and whoever attaches next is
		// answered by replayPaneObservation below.
		return
	}
	// The identity the renderer binds the observation to (AD-7, and the same
	// vocabulary session.integrationChanged uses). A session that has left
	// the registry is gone, and an observation addressed to nobody honest is
	// dropped rather than sent without one.
	sess, err := s.registry.Get(sid)
	if err != nil {
		return
	}
	ident := sess.Identity()
	params := observationChangedParams{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Agent:        o.Agent,
		State:        string(o.State),
	}
	if err := wconn.TryNotify("session.observationChanged", mustMarshal(params)); err != nil {
		s.log.Debug("write session.observationChanged", "session", sid, "error", err)
	}
}

// replayPaneObservation re-sends a pane's current classification on reattach,
// beside replayIntegration and for the identical reason: a state is not an
// event. Only changes are pushed, so a renderer that reconnects to a settled
// idle agent would otherwise wait forever for a transition that is never
// coming — and an indicator that shows nothing for a pane nocx is actively
// watching is the silent degrade AGENTS.md names.
func (s *WSServer) replayPaneObservation(sid session.ID) {
	if s.paneObserver == nil {
		return
	}
	o, ok := s.paneObserver.Snapshot(string(sid))
	if !ok {
		return
	}
	s.emitPaneObservation(sid, o)
}
