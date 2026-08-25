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

	"github.com/shady2k/nocx/internal/capability"
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

// handleSignal addresses one signal to one session's foreground process
// group.
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
	err = op.Run(ctx, func(_ context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId"))
			return nil
		}
		h.answer(req, sid, sess, params.Signal)
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// answer runs the intent against the session and writes the result.
//
// A REMOTE SESSION IS SAID OUT LOUD RATHER THAN COLLAPSED INTO
// "nothing-running". realSession.SignalForeground reports pty.ErrNoForeground
// for a remote channel just as it does for a shell at a prompt — it has no
// local process group either way — so the two are indistinguishable by the
// error alone, and telling a person "nothing is running" while their command
// is plainly running on the far host is a lie they would act on. The kind is
// the fact that separates them, and it is asked here rather than guessed.
// Reaching a process on the far host is the remote-footprint work's, not
// this method's.
func (h signalHandlers) answer(req jsonrpcRequest, sid session.ID, sess session.Session, intent string) {
	outcome := foregroundOutcome("unsupported")
	if sess.Kind() != session.KindRemote {
		sg, ok := sess.(runLeaseSession)
		if !ok {
			// A session whose channel cannot be signalled at all. Same
			// honest answer as a remote one: this process cannot reach it.
			outcome = "unsupported"
		} else if intent == signalStop {
			outcome = stopForeground(h.machine.log, sid, sg, h.machine.effectiveRunLease().SignalGrace)
		} else {
			outcome = interruptForeground(h.machine.log, sid, sg)
		}
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
// read loop, which is the whole requirement — the work itself reads
// TIOCGPGRP and calls kill(2), and mutates no session state.
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
