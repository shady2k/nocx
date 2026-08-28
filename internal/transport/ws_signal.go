package transport

// session.signal — the control-plane way for a person's UI to reach the
// command running in one session (nocx-23rph).
//
// WHAT WAS MISSING. Ctrl+C already reaches a running command, but only as
// the byte 0x03 on the data plane, which means only while the terminal grid
// holds the keyboard: click into another pane, a settings field, a frozen
// block, and the same key stops the same command no longer. Nothing on
// screen offered an alternative either — the running block's ⋮ menu had no
// Stop. The mechanism was never the gap: internal/pty has SignalForeground,
// and internal/transport/run_lease.go already owns the escalation policy.
// The gap was a door from the renderer to them, and this is that door.
//
// It carries an INTENT, not a signal number, and the two intents are the
// two gestures a person makes — see foreground_signal.go, which owns what
// each one does. The handler's own job is exactly three things: refuse
// params it cannot honour, resolve the session this connection actually
// holds, and say what happened.
//
// AND IT SAYS SO EVEN WHEN NOTHING HAPPENED. A signal addressed to a pane
// sitting at a prompt is not an error — the params were well formed and the
// session was real — but it is not a success either, and a control that
// silently does nothing is indistinguishable from a broken one. So the
// refusal travels in the result, where the renderer reads it and tells the
// person, rather than as an absence the caller has to infer.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// signalParams is the wire's request: which session, and which of the two
// intents. Both required — there is no default worth guessing when the
// answer might be SIGKILL.
type signalParams struct {
	SessionID string `json:"sessionId"`
	Signal    string `json:"signal"`
}

// signalResult is the wire's answer (contracts/session.signal.schema.json).
// The signal is echoed so a renderer that fired two gestures close together
// can tell the answers apart without holding correlation state of its own.
type signalResult struct {
	Signal  string `json:"signal"`
	Outcome string `json:"outcome"`
}

// The closed set of intents. Spelled once, here, and read by both the
// validator and the handler — a validator that admits a word the handler
// does not branch on is how a typo becomes a silent no-op.
const (
	signalInterrupt = "interrupt"
	signalStop      = "stop"
)

// validateSignalRaw is the registered validator: the session id shape
// (server-minted, so 32 hex is the honest check) and the closed intent set.
// A refusal is answered -32602 before the handler runs, so a bad request
// never reaches a process group.
func validateSignalRaw(raw json.RawMessage) string {
	var p signalParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	switch p.Signal {
	case signalInterrupt, signalStop:
	default:
		return `signal must be one of "` + signalInterrupt + `", "` + signalStop + `"`
	}
	return ""
}

type signalHandlers struct {
	ops     *capability.SessionOperations
	machine *WSServer
	r       Responder
}

// handleSignal addresses one signal intent to one session's foreground
// execution, through its process group or the lifecycle-confirmed terminal
// interrupt fallback.
//
// The session is resolved through the same two checks resize and close use,
// in the same order and with the same refusal: the connection must hold the
// session (state.has), and the registry must still have it. Neither is
// redundant — the first is authority (AD-9: a connection may act only on the
// sessions it attached), the second is existence.
func (h signalHandlers) handleSignal(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params signalParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: sessionId and signal required"))
		return
	}
	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId"))
		return
	}
	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId"))
		return
	}
	err = op.Run(ctx, func(signalCtx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId"))
			return nil
		}
		h.answer(signalCtx, req, sid, sess, params.Signal)
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// runningLifecycleAttempt returns the exact authenticated attempt the backend
// currently projects as running for sid. It is consulted only after the PTY
// foreground guard found the launcher shell's own group: lifecycle is evidence
// that an execution still exists, never permission to signal that shell group.
func (s *WSServer) runningLifecycleAttempt(sid session.ID) (lifecycle.AttemptID, bool) {
	if s.lifecyclePub == nil {
		return "", false
	}
	s.lifecycleMu.Lock()
	var lane lifecycle.LaneID
	for candidate, owner := range s.lifecycleLanes {
		if owner == sid {
			lane = candidate
			break
		}
	}
	s.lifecycleMu.Unlock()
	if lane == "" {
		return "", false
	}
	snapshot, err := s.lifecyclePub.State(lane)
	if err != nil || snapshot.Lifecycle != lifecycle.LifecycleRunning || snapshot.Attempt == "" {
		return "", false
	}
	return snapshot.Attempt, true
}

// waitLifecycleAttemptEnd keeps Stop's synchronous promise on the fallback
// path. The ordinary process-group ladder waits the same two cooperative
// graces (after INT and TERM); this rare path polls the backend-owned lifecycle
// read model for at most that bound and never inspects terminal bytes.
func (s *WSServer) waitLifecycleAttemptEnd(ctx context.Context, attempt lifecycle.AttemptID, grace time.Duration) bool {
	if grace <= 0 {
		grace = defaultRunSignalGrace
	}
	timer := time.NewTimer(2 * grace)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	ended := func() bool {
		current, ok := s.lifecyclePub.Attempt(attempt)
		return !ok || current.State != lifecycle.AttemptOpen
	}
	for {
		if ended() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		case <-timer.C:
			return ended()
		}
	}
}

// reconcileSharedShellForeground handles the real shell mode in which an
// external foreground program shares the launcher shell's process group. The
// TIOCGPGRP guard must not kill that group. An authenticated open lifecycle
// attempt makes one safer route available: write the terminal's ordinary
// Ctrl+C byte and let the line discipline/program handle it.
func (h signalHandlers) reconcileSharedShellForeground(ctx context.Context, sid session.ID, sess session.Session, intent string, outcome foregroundOutcome) foregroundOutcome {
	if outcome != foregroundNothingRunning {
		return outcome
	}
	attempt, ok := h.machine.runningLifecycleAttempt(sid)
	if !ok {
		return outcome
	}
	if !sess.EnqueueWrite([]byte{0x03}) {
		h.machine.log.Warn("foreground signal: lifecycle-owned fallback enqueue failed",
			"session_id", string(sid), "attempt", string(attempt))
		return foregroundUnreconciled
	}
	h.machine.log.Info("foreground signal: delivered terminal interrupt to lifecycle-owned shared group",
		"session_id", string(sid), "attempt", string(attempt), "intent", intent)
	if intent == signalInterrupt {
		return foregroundDelivered
	}
	if h.machine.waitLifecycleAttemptEnd(ctx, attempt, h.machine.effectiveRunLease().SignalGrace) {
		return foregroundDelivered
	}
	h.machine.log.Warn("foreground signal: lifecycle attempt stayed open after terminal interrupt",
		"session_id", string(sid), "attempt", string(attempt))
	return foregroundUnreconciled
}

// answer runs the intent against the session and writes the result. A remote
// session stays distinct from nothing-running: this process has no host-side
// group to reach, while a local prompt has a protected shell group and no
// authenticated execution. Reaching the far host remains remote-footprint work.

func (h signalHandlers) answer(ctx context.Context, req jsonrpcRequest, sid session.ID, sess session.Session, intent string) {
	outcome := foregroundUnsupported
	if sess.Kind() != session.KindRemote {
		sg, ok := sess.(runLeaseSession)
		if !ok {
			// A session whose channel cannot be signalled at all. Same
			// honest answer as a remote one: this process cannot reach it.
			outcome = foregroundUnsupported
		} else if intent == signalStop {
			outcome = stopForeground(h.machine.log, sid, sg, h.machine.effectiveRunLease().SignalGrace)
		} else {
			outcome = interruptForeground(h.machine.log, sid, sg)
		}
		outcome = h.reconcileSharedShellForeground(ctx, sid, sess, intent, outcome)
	}
	result, err := json.Marshal(signalResult{Signal: intent, Outcome: string(outcome)})
	if err != nil {
		_ = respond(h.r, newJSONRPCError(req.ID, -32603, "Internal error"))
		return
	}
	_ = respond(h.r, newJSONRPCResult(req.ID, result))
}

// signalSpecs registers session.signal on the session operation queue.
//
// NOT on the ordered resize/close submission, and not holding anything a
// close could queue behind: `stop` waits out its escalation grace, and the
// one operation that can tear a wedged session down must never wait for it.
// The queue bounds how many signals may be in flight and runs each off the
// read loop. The ordinary route reads TIOCGPGRP and calls kill(2); the guarded
// shared-group route enqueues one terminal-input byte and may wait on the
// backend-owned lifecycle read model, so neither may occupy the socket loop.
func (s *WSServer) signalSpecs(lane control.Admission, sessionGate control.Admission) []methodSpec {
	sessionOps := capability.NewSessionOperations(sessionGate, lane, s.registry, s.profileUsage)
	sub := s.operationQueue("signal")
	return []methodSpec{
		reg(sub, "session.signal", params(validateSignalRaw), func(_ *wsConn, state *connState, r Responder) handlerFunc {
			h := signalHandlers{ops: sessionOps, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSignal(ctx, state, req) }
		}),
	}
}
