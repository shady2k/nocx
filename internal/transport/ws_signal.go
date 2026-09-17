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

// sessionProtectedForeground is the protectedForeground the wire method
// supplies to the one policy in foreground_signal.go: it can say what attempt
// this session's protected group holds — the started one, and the one the shell
// has not begun yet — write the terminal's interrupt through the session's
// ordinary input path, and watch that exact attempt.
//
// It holds the request's context because it belongs to this one request; the
// policy it is handed to is shared with callers that have no request.
type sessionProtectedForeground struct {
	ctx          context.Context
	s            *WSServer
	sid          session.ID
	sess         session.Session
	exactAttempt func() (lifecycle.AttemptID, bool)
	// mayHold says whether THIS caller's Stop may be held for an attempt the
	// shell has not started (nocx-zas0d). It is true for the request path —
	// session.signal, which answers a person's gesture and owes them the
	// outcome — and false for the run lease, whose cancellation is a
	// withdrawal with its own accounting: an obligation it left in the
	// transport would be a byte delivered by nobody's authority, for a call
	// that answered the lease's own question already.
	mayHold bool
}

// resolve names the one attempt a protected-group interrupt can reach in this
// session, in a single read: the STARTED one if there is one, otherwise the
// attempt that is open and NOT started yet.
//
// FOUR THINGS IT REFUSES, each bought by a way the incident's fix could
// have lied (nocx-7l4ex.10):
//
//   - Nothing, when the projection names no open attempt at all. This is the
//     ordinary prompt.
//   - More than one candidate of the same kind. lane→session is many-to-one by
//     construction — replayLifecycleFacts collects every lane of a session and
//     unregisterLifecycleLanes deletes every one — so "the first match" was an
//     answer chosen by Go's map iteration order. Two running lanes have no
//     right answer here, and inventing one would interrupt a program the
//     person was not addressing. Two attempts on their way have the same
//     problem for a hold: one obligation, two candidates, and no honest way to
//     pick.
//   - A lane whose state moved while we were reading it. The lock is held
//     across the ownership scan AND the state read, so a lane reassigned or
//     torn down between the two cannot be answered about.
//   - An attempt that has left `open` since the projection named it. A closed
//     attempt may never be written to: its command either finished or never
//     ran, and in both cases the terminal in front of us belongs to something
//     else now.
//
// A started attempt WINS over a pending one, and that is not a preference: the
// started one is a program that is running (the app attempt whose start the
// shell authenticated), while the pending one is the same session's next
// command. Answering the running one is what Stop means.
//
// Holding lifecycleMu across a call into the publisher is safe in that
// direction and checked rather than assumed: the publisher emits to this
// server OUTSIDE both its own lock and the kernel's (lifecyclepub's
// publishLane says so and does so), and PublishLifecycle releases
// lifecycleMu before it does any further work — so there is no path where
// the kernel is held while this lock is wanted.
func (p sessionProtectedForeground) resolve() (foregroundTarget, bool) {
	if p.s.lifecyclePub == nil {
		return foregroundTarget{}, false
	}
	if p.exactAttempt != nil {
		attempt, ok := p.exactAttempt()
		if !ok {
			return foregroundTarget{}, false
		}
		current, ok := p.s.lifecyclePub.Attempt(attempt)
		if !ok || current.State != lifecycle.AttemptOpen {
			return foregroundTarget{}, false
		}
		return foregroundTarget{Attempt: attempt, Started: current.Started}, true
	}
	p.s.lifecycleMu.Lock()
	defer p.s.lifecycleMu.Unlock()
	var started, pending lifecycle.AttemptID
	for lane, owner := range p.s.lifecycleLanes {
		if owner != p.sid {
			continue
		}
		snapshot, err := p.s.lifecyclePub.State(lane)
		if err != nil || snapshot.Lifecycle != lifecycle.LifecycleRunning || snapshot.Attempt == "" {
			continue
		}
		attempt, ok := p.s.lifecyclePub.Attempt(snapshot.Attempt)
		if !ok || attempt.State != lifecycle.AttemptOpen {
			p.s.log.Warn("foreground signal: lane names an attempt that is no longer open",
				"session_id", string(p.sid), "lane", string(lane), "attempt", string(snapshot.Attempt),
				"known", ok, "state", string(attempt.State), "started", attempt.Started)
			continue
		}
		if !attempt.Started {
			// The command is on its way: the app opened the attempt at submit
			// and the shell has not begun the line. It is a candidate for a
			// Stop to be HELD against, never for a byte (nocx-zas0d).
			if pending != "" {
				p.s.log.Warn("foreground signal: two lanes name an unstarted attempt — refusing to guess",
					"session_id", string(p.sid), "attempt", string(pending), "other", string(snapshot.Attempt))
				return foregroundTarget{}, false
			}
			pending = snapshot.Attempt
			continue
		}
		if started != "" {
			p.s.log.Warn("foreground signal: two lanes name a started execution — refusing to guess",
				"session_id", string(p.sid), "attempt", string(started), "other", string(snapshot.Attempt))
			return foregroundTarget{}, false
		}
		started = snapshot.Attempt
	}
	if started != "" {
		return foregroundTarget{Attempt: started, Started: true}, true
	}
	if pending != "" {
		return foregroundTarget{Attempt: pending}, true
	}
	return foregroundTarget{}, false
}

// Attempt names the single authenticated, STARTED execution the backend
// projects for this session, or reports that there is not exactly one. It is
// the interrupt path's question: an interrupt is about the present moment, so
// it has no business with a command that has not begun.
func (p sessionProtectedForeground) Attempt() (lifecycle.AttemptID, bool) {
	target, ok := p.resolve()
	if !ok || !target.Started {
		return "", false
	}
	return target.Attempt, true
}

// StopTarget is resolve's Stop-shaped entry point: it answers the started
// attempt when there is one, and otherwise the attempt the shell has not begun
// — ARMING THE HOLD for it, unless this caller may not hold (see mayHold).
//
// THE ARMING IS THE READ. Both happen under the same lock, because the two
// must not be separable: a start that lands between a read reporting "not
// started" and the record of the person's Stop finds no hold to claim, delivers
// nothing, and the Stop is gone while the person was told it was accepted. With
// them together, whoever observes the start — the request that armed the hold
// or the emitter told about the start — claims the same arm, and exactly one of
// them delivers it.
func (p sessionProtectedForeground) StopTarget() (foregroundTarget, bool) {
	if !p.mayHold {
		target, ok := p.resolve()
		if !ok || !target.Started {
			return foregroundTarget{}, false
		}
		return target, true
	}
	p.s.heldMu.Lock()
	defer p.s.heldMu.Unlock()
	target, ok := p.resolve()
	if !ok || target.Started {
		return target, ok
	}
	p.s.armHeldStopLocked(target.Attempt, p.sid)
	return target, true
}

// Interrupt writes the terminal's own interrupt byte on the USER's input
// path — the same queue a focused Ctrl+C travels, and bounded and refused by
// exactly what a keystroke is bounded and refused by, the bootstrap
// quarantine included. It is deliberately NOT sess.Write, which is the
// backend's own path PAST that quarantine
// (internal/session/bootstrap_window.go): a byte that behaves like a
// keystroke must be subject to what keystrokes are subject to.
//
// It is not ordered against input already in flight, and cannot be: this is
// a gesture aimed at the pane by someone who may not hold the keyboard, so
// there is no "before" or "after" to preserve (see EnqueueWrite).
//
// False means the queue refused it and nothing was written. True means it
// was ACCEPTED, which is not the same as written — the channel write happens
// later, and the caller's promise is worded for that.
func (p sessionProtectedForeground) Interrupt(attempt lifecycle.AttemptID) bool {
	return p.sess.EnqueueWrite([]byte{0x03})
}

// Ended reports whether that exact attempt has left `open`, waiting at most
// the two cooperative graces the process-group ladder itself waits (after
// INT and after TERM). It polls the backend-owned lifecycle read model and
// never inspects terminal bytes (AD-6).
//
// "No longer open" is the whole claim. An attempt that has gone unknown
// through transport loss also satisfies it, and that is deliberate: Stop's
// promise is about the execution no longer being open when it answers, not
// about an authenticated successful completion, which is the completion
// fact's business and not this method's.
func (p sessionProtectedForeground) Ended(attempt lifecycle.AttemptID, grace time.Duration) bool {
	if grace <= 0 {
		grace = defaultRunSignalGrace
	}
	timer := time.NewTimer(2 * grace)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	ended := func() bool {
		current, ok := p.s.lifecyclePub.Attempt(attempt)
		return !ok || current.State != lifecycle.AttemptOpen
	}
	for {
		if ended() {
			return true
		}
		select {
		case <-p.ctx.Done():
			return false
		case <-ticker.C:
		case <-timer.C:
			return ended()
		}
	}
}

// answer runs the intent against the session and writes the result. A remote
// session stays distinct from nothing-running: this process has no host-side
// group to reach, while a local prompt has a protected shell group and no
// authenticated execution. Reaching the far host remains remote-footprint
// work. The choice between the process-group ladder and the terminal's own
// interrupt is not made here — it belongs to the one policy in
// foreground_signal.go, which reads it off the kernel's answer.
func (h signalHandlers) answer(ctx context.Context, req jsonrpcRequest, sid session.ID, sess session.Session, intent string) {
	outcome := foregroundUnsupported
	if sess.Kind() != session.KindRemote {
		sg, ok := sess.(runLeaseSession)
		if !ok {
			// A session whose channel cannot be signalled at all. Same
			// honest answer as a remote one: this process cannot reach it.
			outcome = foregroundUnsupported
		} else {
			// mayHold: this is a PERSON's gesture, answered here, and a Stop
			// accepted before the command's start is held for it (nocx-zas0d).
			// Only the Stop path consults it — an interrupt is about the
			// present moment and a command that has not begun is not it.
			fb := sessionProtectedForeground{ctx: ctx, s: h.machine, sid: sid, sess: sess, mayHold: true}
			if intent == signalStop {
				outcome = stopForeground(h.machine.log, sid, sg, h.machine.effectiveRunLease().SignalGrace, fb)
			} else {
				outcome = interruptForeground(h.machine.log, sid, sg, fb)
			}
		}
	}
	result, err := json.Marshal(signalResult{Signal: intent, Outcome: string(outcome)})
	if err != nil {
		_ = respond(h.r, newJSONRPCError(req.ID, -32603, "Internal error"))
		return
	}
	_ = respond(h.r, newJSONRPCResult(req.ID, result))
}

// signalSpecs registers session.signal on the signal lane (buildControlPlane).
//
// NOT on the ordered resize/close submission, and not holding anything a
// close could queue behind: `stop` waits out its escalation grace, and the
// one operation that can tear a wedged session down must never wait for it.
// The queue bounds how many signals may be in flight and runs each off the
// read loop. The ordinary route reads TIOCGPGRP and calls kill(2); the guarded
// shared-group route enqueues one terminal-input byte and may wait on the
// backend-owned lifecycle read model, so neither may occupy the socket loop.
//
// The lane is NOT built here (nocx-zas0d): the last paragraph's one byte is
// also what a held Stop writes when its attempt starts, and that delivery has
// no request behind it to build a lane from. Both users get the same lane,
// with the same bound, from the composition of the control plane.
func (s *WSServer) signalSpecs() []methodSpec {
	return []methodSpec{
		reg(s.signalSub, "session.signal", params(validateSignalRaw), func(_ *wsConn, state *connState, r Responder) handlerFunc {
			h := signalHandlers{ops: s.signalOps, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSignal(ctx, state, req) }
		}),
	}
}

// ── the held Stop: accepted before the start, delivered at it (nocx-zas0d) ──
//
// A Stop pressed in the window between the submit and the running fact used to
// be answered nothing-running and DROPPED — the attempt is open, its bytes are
// on their way to the pty, and the one mechanism a protected group accepts (the
// terminal interrupt) would be eaten by bash's parser: upstream bash 5.2
// resumes an accepted line at the index readline had reached, so a SIGINT
// arriving there makes it execute the line's SUFFIX (measured, nocx-xn63t.6.11
// /.6.12). So the gesture is ACCEPTED and held for that attempt instead, and
// the transport delivers it the moment the shell authenticates the start — by
// then the line is parsed and the program is the one in front.
//
// The obligation is this server's, keyed by attempt, and it is discharged by
// exactly one of the two parties that can observe the start:
//
//   - PublishAttemptStarted, the emitter half of
//     lifecyclepub.AttemptTransitionEmitter, told by the publisher after the
//     kernel accepted the attach. This is the ordinary path.
//   - the request that armed it, which re-reads the authority after arming and
//     delivers the byte itself when the start landed in between. The two cannot
//     both deliver: the arm is claimed by whichever gets there first, and the
//     loser finds nothing to take.
//
// And it is DROPPED when the attempt leaves `open` without ever starting — the
// same interface's PublishAttemptClosed (a prompt_ready the shell reaches a
// second time with no start, a fresh submit closing a primed attempt, an
// explicit abandon, a lost transport), and with the session when it ends.

// armHeldStopLocked records an accepted Stop for attempt. The caller holds
// heldMu — StopTarget requires the record to be one step with the read that
// decided it.
func (s *WSServer) armHeldStopLocked(attempt lifecycle.AttemptID, sid session.ID) {
	if s.heldStops == nil {
		s.heldStops = make(map[lifecycle.AttemptID]session.ID)
	}
	// Idempotent by construction: a second Stop while one is held is the SAME
	// obligation, not a second interrupt, and it is stored under the same key.
	s.heldStops[attempt] = sid
}

// heldStopArmed reports whether a hold is waiting for this attempt, WITHOUT
// taking it: the take belongs to the delivery (submitHeldStop), which is the
// only party that knows whether the Stop was actually discharged.
func (s *WSServer) heldStopArmed(attempt lifecycle.AttemptID) bool {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	_, ok := s.heldStops[attempt]
	return ok
}

// claimHeldStop takes the obligation for attempt, and the caller that takes it
// is the one that delivers it. Taking removes it, so a start reported twice (a
// publisher replay, a second snapshot naming a started attempt) can never
// produce a second byte.
func (s *WSServer) claimHeldStop(attempt lifecycle.AttemptID) (session.ID, bool) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	sid, ok := s.heldStops[attempt]
	if ok {
		delete(s.heldStops, attempt)
	}
	return sid, ok
}

// dropHeldStopsFor discards every held Stop of one session. The session is the
// closing event for all of them: an attempt cannot outlive it, and the
// registry must not outlive either.
func (s *WSServer) dropHeldStopsFor(sid session.ID) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	for attempt, owner := range s.heldStops {
		if owner == sid {
			delete(s.heldStops, attempt)
		}
	}
}

// PublishAttemptStarted is the Emitter half of
// lifecyclepub.AttemptTransitionEmitter: the shell has authenticated the start
// of an attempt that was already open, so a Stop held for it is due now.
//
// It does nothing but hand the arm to the signal lane, and deliberately does
// NOT take it here. The claim belongs where the byte is written — inside the
// queued task, immediately before the attempt is re-read — so that a queue
// refusal leaves the obligation where it was rather than eating a Stop the
// person was already told was accepted, and so that a concurrent discard (the
// attempt closing) is decided by whoever gets there first rather than by the
// order two goroutines happened to run in.
func (s *WSServer) PublishAttemptStarted(attempt lifecycle.AttemptID) {
	if !s.heldStopArmed(attempt) {
		return
	}
	s.submitHeldStop(attempt)
}

// PublishAttemptClosed is the Emitter half for the other transition: the
// attempt left `open` with no start ever authenticated for it, so the Stop held
// for that start can never be delivered and is discarded.
func (s *WSServer) PublishAttemptClosed(attempt lifecycle.AttemptID) {
	sid, ok := s.claimHeldStop(attempt)
	if !ok {
		return
	}
	// Warn rather than Info: this is a Stop a person pressed doing nothing,
	// and the line is the only thing that says why when they report it — the
	// sibling of the "no authenticated started attempt" line in
	// foreground_signal.go.
	s.log.Warn("foreground signal: the held Stop is discarded — the attempt closed without ever starting",
		"session_id", string(sid), "attempt", string(attempt))
}

// submitHeldStop runs one held Stop's delivery on the signal lane, and the
// delivery claims the arm itself (see PublishAttemptStarted).
func (s *WSServer) submitHeldStop(attempt lifecycle.AttemptID) {
	// Owner: this server, for the session the Stop was accepted on. The
	// closing event is that session's close (dropHeldStopsFor), which drops
	// the holds still waiting; no delivery outlives it by more than the
	// run-signal grace the ladder itself waits.
	ctx := context.Background()
	rej := s.signalSub.TrySubmit(ctx, control.Task{Run: func(taskCtx context.Context) {
		sid, ok := s.claimHeldStop(attempt)
		if !ok {
			// Someone else has it: the request that armed it is delivering it,
			// or the attempt has closed and its hold was discarded.
			return
		}
		s.deliverHeldStop(taskCtx, sid, attempt)
	}})
	if rej != nil {
		// The queue is saturated: the same refusal a person's own session.signal
		// gets at that depth. The obligation is NOT taken (nothing was written),
		// and it is said out loud with what it costs — a Stop the person was
		// told was accepted will not land unless they press it again, which
		// reaches a started attempt by the ordinary path.
		s.log.Warn("foreground signal: the held Stop was refused a lane and will not be delivered — press Stop again to reach the running command",
			"attempt", string(attempt), "reason", rej.Reason)
	}
}

// deliverHeldStop discharges one held Stop: the same policy the request path
// runs, against the EXACT attempt the Stop was accepted for, so nothing is
// written if that attempt has closed in the meantime — a byte into whatever
// holds the terminal next is precisely what a Stop must never do.
func (s *WSServer) deliverHeldStop(ctx context.Context, sid session.ID, attempt lifecycle.AttemptID) {
	op, err := s.signalOps.ForSession(sid)
	if err != nil {
		s.log.Info("foreground signal: the held Stop is discarded — the session is gone",
			"session_id", string(sid), "attempt", string(attempt), "error", err)
		return
	}
	err = op.Run(ctx, func(runCtx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			return gerr
		}
		sg, ok := sess.(runLeaseSession)
		if !ok {
			s.log.Warn("foreground signal: the held Stop cannot reach this session's channel",
				"session_id", string(sid), "attempt", string(attempt))
			return nil
		}
		fb := sessionProtectedForeground{
			ctx: runCtx, s: s, sid: sid, sess: sess,
			exactAttempt: func() (lifecycle.AttemptID, bool) { return attempt, true },
		}
		outcome := stopForeground(s.log, sid, sg, s.effectiveRunLease().SignalGrace, fb)
		s.log.Info("foreground signal: the held Stop was delivered",
			"session_id", string(sid), "attempt", string(attempt), "outcome", string(outcome))
		return nil
	})
	if err != nil {
		s.log.Warn("foreground signal: the held Stop was refused a lane",
			"session_id", string(sid), "attempt", string(attempt), "error", err)
	}
}
