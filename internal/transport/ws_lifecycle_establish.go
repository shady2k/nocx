package transport

// The lifecycle.establishAck control plane (ADR-0024 decision 9; bead
// nocx-u7uh.10): the renderer's acknowledgement that it has processed the
// published prompt_ready fact for the exact {lane, domain, epoch,
// generation} and committed the presentation that makes an editor available.
// The backend flushes the pending accept ONLY on this acknowledgement —
// no acknowledgement, no accept, and the shell's bounded handshake wait
// expires with a visible native prompt (fail-open). The publisher owns the
// pending accept and the flush; this file is the transport's half of the
// boundary: validating session ownership and the acknowledging connection,
// then forwarding the ack.
//
// The acknowledgement is deliberately narrow, like lifecycle.recoverAck:
// session identity, the lane/domain/epoch addressing tuple and the
// backend-minted generation the published fact carried — nothing else. No
// capability, no raw frame, no attempt, no status.

import (
	"encoding/json"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// lifecycleEstablishAckParams is the payload of the "lifecycle.establishAck"
// RPC. The generation is the backend-minted value the published prompt_ready
// fact carried (decision 9): the acknowledgement must name the exact
// generation of the pending accept, so an old connection's ack (an old
// generation) can never release a newer accept.
type lifecycleEstablishAckParams struct {
	SessionID  string `json:"sessionId"`
	Lane       string `json:"lane"`
	Domain     string `json:"domain"`
	Epoch      uint64 `json:"epoch"`
	Generation string `json:"generation"`
}

// handleLifecycleEstablishAck is the establishment acknowledgement (ADR-0024
// decision 9): the renderer confirms it applied the published fact and
// committed the editor presentation, so the pending accept may be flushed.
// The kernel transition is the only thing it permits — flushing the accept
// makes the domain live past ACCEPT; it can never grant the renderer any
// authority it did not already have.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"lifecycle.establishAck","params":{"sessionId":"...","lane":"lane-1","domain":"dom-1","epoch":1,"generation":"est-..."}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"ok":true}}
//
// Rejections, one per decision-9 acceptance rule:
//   - (a) an unknown field or a missing sessionId/lane/domain/epoch/generation
//     is invalid params (the schema pins the exact key set);
//   - (b) a session that is not open, or not owned by this connection, is
//     refused — session exit and foreign connections invalidate the ack;
//   - (c) a lane not registered to this session is refused — an ack for a
//     lane of another session cannot release this session's accept;
//   - (d) an ack from a connection that is no longer the session's current
//     subscriber is refused — a detached or replaced connection's ack must
//     not release an accept after the subscriber changed (the subscriber
//     slot is cleared on teardown, so a dead connection fails this too);
//   - (e) no pending establishment for the tuple, or a generation mismatch,
//     is refused at the publisher — stale or foreign acks release nothing.
func (s *WSServer) handleLifecycleEstablishAck(wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	if s.lifecyclePub == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "lifecycle not available"})
		return
	}
	var params lifecycleEstablishAckParams
	if err := json.Unmarshal(req.Params, &params); err != nil ||
		params.SessionID == "" || params.Lane == "" || params.Domain == "" ||
		params.Epoch == 0 || params.Generation == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId, lane, domain, epoch and generation required"})
		return
	}
	sid := session.ID(params.SessionID)
	// Every arm below refuses with -32603, and the renderer logs the code
	// without the message, so all five reasons arrived at a reader as one
	// indistinguishable "establishment acknowledgement refused". A refusal
	// leaves the session conventional — no editor authority — which is the
	// visible symptom of six e2e specs whose cause stayed "unknown" across
	// three triage rounds (nocx-cbtc). The refusal is the fail-open
	// direction and stays a warning rather than an error, but which rule
	// refused it is the whole diagnostic value, so it is named here.
	refuse := func(rule, msg string) {
		s.log.Warn("establishment acknowledgement refused",
			"rule", rule, "reason", msg, "session", string(sid),
			"lane", params.Lane, "domain", params.Domain,
			"epoch", params.Epoch, "generation", params.Generation)
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
	}
	// (b) alive: the session must be open, and owned by this connection.
	if _, err := s.registry.Get(sid); err != nil || !state.has(sid) {
		refuse("b-session-open", "session is not open")
		return
	}
	// (c) the lane must belong to this session.
	s.lifecycleMu.Lock()
	registered, ok := s.lifecycleLanes[lifecycle.LaneID(params.Lane)]
	s.lifecycleMu.Unlock()
	if !ok || registered != sid {
		refuse("c-lane-registered", "lane is not registered to this session")
		return
	}
	// (d) the acknowledging connection must still be the session's current
	// subscriber. The subscriber slot is cleared when a connection dies, and
	// replaced when a newer connection attaches, so this rejects both a dead
	// connection's in-flight ack and a replaced connection's late ack.
	rx := s.getRx(sid)
	if rx == nil {
		refuse("d-no-receiver", "session is not open")
		return
	}
	sub, _ := rx.getSubscriber()
	if sub != wconn {
		refuse("d-not-subscriber", "not the current subscriber")
		return
	}
	// (e) the publisher validates the pending establishment and flushes the
	// accept only for the exact generation (and only while the domain is
	// still established and current).
	if err := s.lifecyclePub.AcknowledgeEstablishment(
		lifecycle.LaneID(params.Lane), lifecycle.DomainID(params.Domain), params.Epoch, params.Generation,
	); err != nil {
		refuse("e-publisher", err.Error())
		return
	}
	// The SUCCESS is logged too, and that is the point of logging here at all.
	// A refusal alone cannot be read: "no establishment is pending
	// acknowledgement" is what a stale ack says AND what a second ack of an
	// already-flushed generation says, and those mean opposite things — one is
	// a session that never went live, the other is normal. With only refusals
	// in the log the two are indistinguishable, which is exactly where the
	// nocx-xplc investigation stalled. Paired with the refusal above, a
	// generation that was accepted once and re-acked twice now reads as such.
	//
	// Debug, not Info: one line per established domain per session is noise on
	// a healthy machine and the whole story on a sick one. NOCX_LOG_LEVEL=debug
	// turns it on.
	s.log.Debug("establishment acknowledged",
		"session", string(sid), "lane", params.Lane, "domain", params.Domain,
		"epoch", params.Epoch, "generation", params.Generation)
	_ = r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}
