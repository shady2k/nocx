package transport

// session.liveness — the reachability axis on the wire (nocx-iarf9).
//
// WHAT THIS NOTIFICATION OWNS, AND WHAT IT DOES NOT. The session record holds
// four values (alive | dead | unknown | interrupted); this notification
// carries two. That is not an omission, it is the AD-8 line:
//
//   - `exit` is an EVENT: this session ENDED, and here is the cause. It closes
//     or marks the tab, and it already discriminates an authoritative exit
//     from a loss (nocx-ictcq) — which is the terminal half of the liveness
//     set, in the same two words. Sending it again here would be a second
//     answer to one question, which is exactly what was removed from the open
//     ack for shellIntegrationReason (nocx-dvql).
//   - `session.liveness` is a STATE: what the backend currently believes about
//     reaching a session that has NOT ended. Revisable in both directions,
//     carried with an epoch so a late observation can be refused.
//
// So the enum is exactly the set an observation may assert, and the terminal
// half is exactly the set only the session's own end may produce. A shape that
// cannot exist cannot be sent.

import (
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// livenessNotificationParams is the session.liveness payload, declared once
// (contracts/session.liveness.schema.json) and pinned by the contract tests.
type livenessNotificationParams struct {
	SessionID     string `json:"sessionId"`
	InstanceID    string `json:"instanceId"`
	SessionEpoch  uint64 `json:"sessionEpoch"`
	Liveness      string `json:"liveness"`
	LivenessEpoch uint64 `json:"livenessEpoch"`
	ObservedAt    string `json:"observedAt"`
	// RoundTripMS is how long the last probe to this session's host took.
	// Omitted when nothing measured one — a local session, or a host that
	// never answered. Absent and zero are the same statement here and the
	// renderer must treat them alike: "no measurement", never "instant".
	RoundTripMS int64 `json:"roundTripMs,omitempty"`
	// Slow is the backend's GRADE of that measurement. It is sent rather
	// than left to the renderer to threshold for itself, because the grade
	// has hysteresis — it enters and leaves at different numbers — and a
	// reader holding only the milliseconds cannot reproduce it. Two
	// derivations of one concept would agree everywhere anyone looked and
	// disagree exactly at the boundary.
	Slow bool `json:"slow,omitempty"`
}

// PublishLiveness tells the session's subscriber that the backend's belief
// about reaching it changed. It is the registry's liveness watcher, wired at
// the composition root.
//
// It is handed a Ref rather than a state on purpose: the record is the
// authority, so this reads it back instead of publishing a value that was
// captured before the lookup and may already have been overtaken. That read is
// also where the two refusals live — a ref naming a different incarnation, and
// a session that has ended since the observation — and both are answered by
// the same owners the rest of the product uses (SameIncarnation, and the
// session's own liveness derivation).
func (s *WSServer) PublishLiveness(ref session.Ref) {
	sess, err := s.registry.Get(ref.ID)
	if err != nil {
		return
	}
	// The observation was about one incarnation; the id may since name
	// another. Refusing here is what stops a late report closing over a
	// session that merely reuses the id (nocx-3oupk).
	if !ref.Identity.SameIncarnation(ref.ID, sess) {
		return
	}
	st := sess.Liveness()
	if st.Liveness.Terminal() {
		// The session ended between the observation and here. That is the
		// exit notification's news, in its own vocabulary; this axis does not
		// repeat it.
		return
	}

	rx := s.getRx(ref.ID)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		// Nobody is attached. The record keeps the value, and a reattach
		// learns it the next time the projection changes — this axis carries
		// no history and promises none (AD-9 owns replay, for bytes).
		return
	}
	if err := wconn.TryNotify("session.liveness", mustMarshal(livenessNotificationParams{
		SessionID:     string(ref.ID),
		InstanceID:    string(ref.Identity.InstanceID),
		SessionEpoch:  ref.Identity.Epoch,
		Liveness:      string(st.Liveness),
		LivenessEpoch: st.Epoch,
		ObservedAt:    st.ObservedAt.UTC().Format(time.RFC3339),
		RoundTripMS:   st.RoundTrip.Milliseconds(),
		Slow:          st.Slow,
	})); err != nil {
		s.log.Debug("write session.liveness notification", "error", err)
	}
}
