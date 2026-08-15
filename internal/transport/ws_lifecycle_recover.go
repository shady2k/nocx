package transport

import (
	"encoding/json"
	"sync"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

// recoveryState tracks one restoration episode per session (ADR-0024
// decision 8's composite ACK). The episode is opened when a lost fact with a
// recovery fence routes to a live session, and resolved when the renderer's
// ack lands (the lane falls Lost → Native). It exists so the ack — which
// carries only the session id and the generation — can be validated against
// what the backend actually promised: accepted only while pending and alive,
// idempotent once resolved, and invalidated by session exit (closeSession
// cancels it, so a late ack is rejected).
type recoveryState struct {
	// mu serializes the claim→recover→resolve sequence: a duplicate ack
	// queues on it and sees resolved, never a double RecoverLane; and the
	// close race (session death wins) resolves to one of two benign
	// outcomes — the ack lost the episode and is rejected, or the ack
	// landed before closeSession cancelled it and the lane falls to Native
	// on a session that is already exiting, which the exit notification
	// supersedes.
	mu         sync.Mutex
	generation string
	lane       lifecycle.LaneID
	resolved   bool
}

// recoveryOf returns the session's recovery state, or nil.
func (s *WSServer) recoveryOf(sid session.ID) *recoveryState {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.recoveries[sid]
}

// openRecovery records the restoration episode a lost fact opens for a
// session, or reports that the session is not live and no claim may be made.
// A fresh generation supersedes an old episode (a re-established domain
// lost again is a new episode); a resolved episode of the same generation
// stays resolved, so a replay of the lost fact does not reopen it.
func (s *WSServer) openRecovery(sid session.ID, f lifecyclepub.Fact) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if s.recoveries == nil {
		s.recoveries = make(map[session.ID]*recoveryState)
	}
	cur := s.recoveries[sid]
	if cur != nil && cur.generation == f.Recovery.Generation {
		return // same episode already recorded (replayed lost fact)
	}
	s.recoveries[sid] = &recoveryState{
		generation: f.Recovery.Generation,
		lane:       lifecycle.LaneID(f.Lane),
	}
}

// cancelRecovery drops the session's episode — session death wins (decision
// 8): a late ack after the session ended must be rejected. Called from
// closeSession.
func (s *WSServer) cancelRecovery(sid session.ID) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	delete(s.recoveries, sid)
}

// lifecycleRecoverAckParams is the payload of the "lifecycle.recoverAck"
// RPC: deliberately narrow (decision 8 acceptance) — session identity and
// the recovery generation, and nothing else. No domain, no epoch, no
// attempt, no status, no prompt-readiness.
type lifecycleRecoverAckParams struct {
	SessionID  string `json:"sessionId"`
	Generation string `json:"generation"`
}

// handleLifecycleRecoverAck is the restoration acknowledgement (ADR-0024
// decision 8): the renderer confirms it matched the shell's one-shot
// recovery fence AND applied the conventional presentation, so the lane may
// fall Lost → Native. The kernel transition is the only thing it permits:
// RecoverLane can never revive a DomainLost, never grant ownership, never
// open or complete an attempt.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"lifecycle.recoverAck","params":{"sessionId":"...","generation":"<64 hex>"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"ok":true}}
//
// Rejections, one per decision-8 acceptance rule:
//   - (a) an unknown field or a missing sessionId/generation is invalid
//     params (the schema pins the exact key set);
//   - (b) a session with no pending episode, or a mismatched generation, is
//     refused — the backend only acks what it promised;
//   - (b,d) a session that is not open (closed, or never this connection's)
//     is refused — session exit invalidates the episode;
//   - (c) a lane that is no longer Lost is refused — the ack permits only
//     Lost → Native;
//   - (d) a duplicate ack for an already-resolved episode succeeds (the
//     transition already landed; idempotent by design).
func (s *WSServer) handleLifecycleRecoverAck(r Responder, state *connState, req jsonrpcRequest) {
	if s.lifecyclePub == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "lifecycle not available"})
		return
	}
	var params lifecycleRecoverAckParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" || params.Generation == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId and generation required"})
		return
	}
	sid := session.ID(params.SessionID)
	// (b,d) alive: the session must be open, and owned by this connection.
	if _, err := s.registry.Get(sid); err != nil || !state.has(sid) {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: "session is not open"})
		return
	}
	rec := s.recoveryOf(sid)
	if rec == nil || rec.generation != params.Generation {
		// (b) no pending episode for this generation — never promised, or
		// superseded by a fresh domain's episode.
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: "no pending recovery for this generation"})
		return
	}
	// The episode's own mutex serializes claim→recover→resolve (see
	// recoveryState.mu).
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.resolved {
		// (d) idempotent: the recovery already landed.
		_ = r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
		return
	}
	// (c) the kernel permits only Lost → Native.
	if err := s.lifecyclePub.RecoverLane(rec.lane); err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	rec.resolved = true
	_ = r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}
