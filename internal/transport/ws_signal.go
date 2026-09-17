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
	"errors"
	"syscall"
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
	// recordStop says this caller's interrupt IS a person's Stop, so the
	// writer's verdict on its byte goes into the attempt's delivery record
	// (stopState) — whenever that verdict comes, including after this call
	// stopped waiting for it. The request path sets it for the stop intent;
	// the run lease, which keeps its own accounting, and the interrupt
	// intent, which is not a Stop, do not.
	recordStop bool
	// settled, when set, makes Interrupt a caller that must not wait for the
	// channel AT ALL: the byte is queued with its condition, Interrupt answers
	// interruptQueued, and the writer's verdict is handed here instead — with
	// write-refused when the queue would not take it. Only the held delivery
	// sets it, and it is not an optimisation: it is the fix for a timeout that
	// could not settle a write (nocx-zas0d, review of 6830b43d, blocker 1). A
	// caller that stops waiting knows nothing about the byte it abandoned, and
	// telling the person it did not land invites a retry that lands a SECOND
	// interrupt.
	settled func(written bool, err error)
	// handedOff, when set, is marked on the CALLING goroutine the moment
	// Interrupt hands the verdict to settled — so the held delivery, which
	// settles every other exit itself, knows not to settle this one twice.
	handedOff *bool
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
// IT READS THE DOMAIN'S OPEN ATTEMPTS, NOT THE LANE'S PROJECTION, and that is
// the whole difference for the window this method exists to serve. The lane
// stops projecting an attempt as soon as the shell reaches a prompt over it
// (kernel.go's applyPromptReady clears the reference on the FIRST prompt_ready,
// because an attempt the shell reached a prompt over may still be the start's
// target) — so a Stop landing in that measured 7-10 ms primed interval used to
// resolve nothing at all and be dropped, in exactly the race it is meant to
// survive (review finding 3 of 1e899f6a). The open attempts of the session's
// lanes are the authority on what may still be interrupted; the projection is
// not.
//
// FOUR THINGS IT REFUSES, each bought by a way the incident's fix could
// have lied (nocx-7l4ex.10):
//
//   - Nothing, when the projection names no open attempt at all. This is the
//     ordinary prompt.
//   - More than one candidate of the winning kind. lane→session is many-to-one
//     by construction — replayLifecycleFacts collects every lane of a session
//     and unregisterLifecycleLanes deletes every one — so "the first match" was
//     an answer chosen by Go's map iteration order. Two running lanes have no
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
// A started attempt WINS over a pending one, and THAT IS DECIDED AFTER THE
// SCAN, never during it: returning on the first second-candidate met made the
// answer depend on the map's order, so one running command plus two attempts
// on their way refused nondeterministically (review finding 6 of 1e899f6a).
// The counts are what decide: more than one started refuses, exactly one
// started wins whatever else is in flight, and only with no started attempt
// does a single pending one become the answer.
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
	startedCount, pendingCount := 0, 0
	for lane, owner := range p.s.lifecycleLanes {
		if owner != p.sid {
			continue
		}
		snapshot, err := p.s.lifecyclePub.State(lane)
		if err != nil {
			continue
		}
		for _, id := range snapshot.OpenAttempts {
			attempt, ok := p.s.lifecyclePub.Attempt(id)
			if !ok || attempt.State != lifecycle.AttemptOpen {
				continue
			}
			if attempt.Started {
				started, startedCount = id, startedCount+1
				continue
			}
			pending, pendingCount = id, pendingCount+1
		}
	}
	switch {
	case startedCount > 1:
		p.s.log.Warn("foreground signal: more than one started execution — refusing to guess",
			"session_id", string(p.sid), "attempt", string(started), "count", startedCount)
		return foregroundTarget{}, false
	case startedCount == 1:
		return foregroundTarget{Attempt: started, Started: true}, true
	case pendingCount > 1:
		p.s.log.Warn("foreground signal: more than one attempt on its way — refusing to guess",
			"session_id", string(p.sid), "attempt", string(pending), "count", pendingCount)
		return foregroundTarget{}, false
	case pendingCount == 1:
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
	p.s.armHeldStopAndStateLocked(target.Attempt, p.sid)
	return target, true
}

// Interrupt puts the terminal's own interrupt byte on the USER's input path —
// the same queue a focused Ctrl+C travels, and bounded and refused by exactly
// what a keystroke is bounded and refused by, the bootstrap quarantine
// included. It is deliberately NOT sess.Write, which is the backend's own path
// PAST that quarantine (internal/session/bootstrap_window.go): a byte that
// behaves like a keystroke must be subject to what keystrokes are subject to.
//
// THE CONDITION IS CHECKED AT THE WRITE (review finding 1 of 1e899f6a). An
// interrupt that has been resolved to an attempt and dropped on the queue has
// serialized nothing: the queue may hold it long enough for that attempt to
// end, and the byte would then land in a prompt — or inside whatever command
// the person started next. So the write carries the condition with it
// (session.EnqueueInputIf) and is DISCARDED rather than written when the
// attempt has gone; writeLoop is the one goroutine that writes, which is why
// the check has to travel that far.
//
// The wait for that answer is bounded by the request's own context and by the
// cooperative grace the ladder waits — for a REQUEST, which owes an answer and
// whose outcome word ("unreconciled, the command may still be running") is
// honest about a write it could not confirm. What the wait never does is
// DECIDE the byte: it stays queued, the writer still settles it, and a Stop's
// record takes that verdict whenever it comes (recordStop). A caller that has
// no answer to give at all does not wait (settled).
//
// WHAT THAT DOES NOT CLAIM: the check and the write are not atomic with the
// kernel's attempt state. No lock spans the terminal's writer and the
// lifecycle kernel, so the attempt can still leave `open` between the answer
// and the syscall — a window one write(2) wide. What the ordering DOES give is
// that this byte cannot overtake, or be overtaken by, anything else on this
// session's input path: it keeps its place in the one queue writeLoop drains,
// so a command line typed after it reaches the terminal after it (and a later
// attempt cannot start before this one has left `open`, which is the state the
// check refuses).
//
// AND WHAT THE RESIDUAL BYTE CAN REACH IS THE PROMPT, NOT A PARSER (review of
// 6830b43d, item D). The condition can only pass while the attempt is open AND
// the shell has authenticated its start, so the line it belongs to has been
// parsed and a program is running: there is no line for bash to resume at the
// index readline had reached, which is the suffix hazard this whole mechanism
// exists for. If the attempt ends between the answer and the syscall, the byte
// arrives at a prompt the shell has ALREADY returned to, and FIFO guarantees no
// later input precedes it — so the worst it can do is what a keyboard Ctrl+C
// typed at that instant does: discard whatever sits in readline's buffer,
// including a half-typed line the person owns. That is a real, bounded cost,
// the same one the same gesture has from the keyboard, and it is not a reason
// for machinery that cannot exist: no lock spans the writer and the kernel.
func (p sessionProtectedForeground) Interrupt(attempt lifecycle.AttemptID) interruptResult {
	if p.recordStop {
		// BEFORE the byte is queued, so a closure the shell reports after the
		// write can never be published ahead of this verdict (signalDeliveryFor
		// waits for a delivery in flight).
		p.s.beginStopDelivery(p.sid, attempt)
	}
	if p.settled != nil && p.handedOff != nil {
		*p.handedOff = true
	}
	verdict := make(chan interruptResult, 1)
	queued := p.sess.EnqueueInputIf([]byte{0x03},
		func() bool { return p.s.attemptIsOpenAndStarted(attempt) },
		func(written bool, err error) {
			if p.recordStop {
				p.s.settleStopDelivery(attempt, written)
			}
			if p.settled != nil {
				p.settled(written, err)
			}
			switch {
			case written:
				verdict <- interruptWritten
			case err == nil:
				verdict <- interruptDiscarded
			default:
				verdict <- interruptRefused
			}
		})
	if !queued {
		// The queue refused it: nothing was queued, so no verdict is coming and
		// this call is the settlement.
		if p.recordStop {
			p.s.settleStopDelivery(attempt, false)
		}
		if p.settled != nil {
			p.settled(false, session.ErrInputRefused)
		}
		return interruptRefused
	}
	if p.settled != nil {
		return interruptQueued
	}
	grace := p.s.effectiveRunLease().SignalGrace
	if grace <= 0 {
		grace = defaultRunSignalGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case r := <-verdict:
		return r
	case <-timer.C:
		// The request stops waiting; the byte is still queued and still
		// settled by the writer. The outcome word carries the uncertainty to
		// the person, and the record takes the verdict when it comes.
		return interruptRefused
	case <-p.ctx.Done():
		return interruptRefused
	case <-p.sess.Done():
		return interruptRefused
	}
}

// attemptIsOpenAndStarted is the condition the interrupt byte is written under:
// the exact attempt is still open AND the shell has authenticated its start. An
// attempt that never started must never be interrupted (that is the parser
// window the hold exists for), and one that has closed is someone else's
// terminal by now.
func (s *WSServer) attemptIsOpenAndStarted(attempt lifecycle.AttemptID) bool {
	if s.lifecyclePub == nil {
		return false
	}
	current, ok := s.lifecyclePub.Attempt(attempt)
	return ok && current.State == lifecycle.AttemptOpen && current.Started
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
				// A Stop's outcome is the ATTEMPT's record, whichever mechanism
				// carries it — the byte (recordStop) or the ladder (the
				// recording signaller) — so that a Stop that lands after an
				// earlier one failed is what the block and the ledger say
				// (nocx-zas0d, review 3 item 2).
				fb.recordStop = true
				ladder := &stopRecordingSignaller{runLeaseSession: sg, s: h.machine, sid: sid, attempt: fb.Attempt}
				outcome = stopForeground(h.machine.log, sid, ladder, h.machine.effectiveRunLease().SignalGrace, fb)
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

// The closed set of reasons a Stop can fail to reach its command with, and the
// whole vocabulary the renderer is given (contracts/session.signalUndelivered
// .schema.json). Each names what happened, never what to do about it: the
// sentence is the surface's, and a reason the surface does not know is a type
// error there rather than a wrong sentence here.
//
// write-unconfirmed is deliberately NOT in the set. It used to be, for a write
// whose caller gave up waiting — and that was wrong twice over (nocx-zas0d,
// review of 6830b43d, blocker 1): a timeout says "I stopped waiting", never
// "nothing was written", so reporting it as undelivered told the person a stop
// had failed while its byte was still queued, and the retry it invited was a
// SECOND interrupt. The only caller that reports undelivered now settles when
// the writer settles (session.EnqueueInputIf), so every reason here is a verdict
// the writer actually gave.
const (
	// undeliveredAttemptClosed: the attempt left `open` before the byte could
	// be written — the command ended, or never started at all. Nothing landed
	// in the terminal, which is the point of checking at the write.
	undeliveredAttemptClosed = "attempt-closed"
	// undeliveredWriteRefused: the session's input path refused the byte — the
	// queue was full, or the session was inside its bootstrap quarantine (a
	// keystroke is refused there, never buffered), or it was already closed.
	undeliveredWriteRefused = "write-refused"
	// undeliveredWriteFailed: the terminal itself did not take the byte.
	undeliveredWriteFailed = "write-failed"
	// undeliveredLaneRefused: the delivery never reached the terminal — the
	// signal lane refused the task, or the session's identity gate did.
	undeliveredLaneRefused = "lane-refused"
	// undeliveredUnsupported: this session's channel cannot be signalled at
	// all, which is the same answer the request path calls "unsupported".
	undeliveredUnsupported = "unsupported"
)

// The closed set of values a Stop's outcome carries on the wire, where the
// renderer reads it from the LIFECYCLE FACT rather than from a notification
// (nocx-zas0d, review of 6830b43d, major 2): the outcome has to be state, so a
// dropped frame or a reconnect cannot leave a block claiming a stop that never
// happened. See signalDeliveryFor for when each is published.
const (
	signalDeliveryDelivered   = "delivered"
	signalDeliveryUndelivered = "undelivered"
)

// stopState is ONE ATTEMPT's delivery record: what the Stops accepted for it
// came to, whichever path carried them — a held Stop's byte, a person's Stop on
// the request path, a rung of the process-group ladder.
//
// IT IS THE ATTEMPT'S RECORD AND NOT A STOP'S, and that is the fix for review 3
// item 2. A record per Stop settled first-wins let an earlier failed held Stop
// outrank a later Stop that landed: the block painted the person's successful
// retry as the program's own failure, and the ledger never learned the command
// was user-killed. What a person and the store need is not "what did the first
// Stop do" but "did an interrupt reach this command", so DELIVERED IS STICKY:
// any delivery settles the question for good, and undelivered is only ever the
// answer when no delivery happened.
//
// inflight counts deliveries whose verdict is not in yet — a hold counts from
// its arm — and changed is closed on every settlement. Together they let a fact
// reporting the attempt's closure WAIT for a byte already on its way instead of
// racing it (signalDeliveryFor).
type stopState struct {
	sid         session.ID
	delivered   bool
	undelivered bool
	inflight    int
	changed     chan struct{}
	// dropped marks a record its session took with it, so a fact waiting on
	// it stops waiting.
	dropped bool
}

// whileOpen is what the record says about an attempt that is still open:
// delivered once anything landed, undelivered once something failed and
// nothing else is on its way, and nothing while a delivery is in flight —
// which is still being decided, and a guess in either direction would be the
// statement this field exists to stop making. The caller holds stopStateMu.
func (rec *stopState) whileOpen() string {
	switch {
	case rec.delivered:
		return signalDeliveryDelivered
	case rec.undelivered && rec.inflight == 0:
		return signalDeliveryUndelivered
	}
	return ""
}

// beginStopDelivery records that a Stop's delivery is on its way for this
// attempt, creating the attempt's record on its first Stop. It is called BEFORE
// the byte is queued or the signal sent, and every call is paired with exactly
// one settleStopDelivery.
func (s *WSServer) beginStopDelivery(sid session.ID, attempt lifecycle.AttemptID) {
	s.stopStateMu.Lock()
	defer s.stopStateMu.Unlock()
	if s.stopStates == nil {
		s.stopStates = make(map[lifecycle.AttemptID]*stopState)
	}
	rec, ok := s.stopStates[attempt]
	if !ok {
		rec = &stopState{sid: sid, changed: make(chan struct{})}
		s.stopStates[attempt] = rec
	}
	rec.inflight++
}

// settleStopDelivery records one delivery's verdict and wakes whoever waits on
// the record.
//
// AND WHILE THE ATTEMPT IS STILL OPEN IT REPUBLISHES THE LANE (review 3 item 4)
// when what the record says has changed. The settlement is state the renderer
// derives "Stopped" from, and until now it reached an open block only as the
// session.signalUndelivered notice — a TryNotify a full queue drops. The lane's
// fact carries the record, so re-emitting it is the same carrier a completion
// uses, and a dropped notice leaves the block no less correct. A closed attempt
// is not republished: the fact reporting its closure already waited for this
// verdict (signalDeliveryFor).
func (s *WSServer) settleStopDelivery(attempt lifecycle.AttemptID, delivered bool) {
	s.stopStateMu.Lock()
	rec, ok := s.stopStates[attempt]
	if !ok {
		// Dropped with its session: nobody is left to read it.
		s.stopStateMu.Unlock()
		return
	}
	before := rec.whileOpen()
	if rec.inflight > 0 {
		rec.inflight--
	}
	if delivered {
		rec.delivered = true
	} else {
		rec.undelivered = true
	}
	close(rec.changed)
	rec.changed = make(chan struct{})
	after := rec.whileOpen()
	s.stopStateMu.Unlock()
	if after != before {
		s.republishOpenAttempt(attempt)
	}
}

// republishOpenAttempt re-emits the lane of an attempt that is still open, so
// its fact carries what its record says now.
func (s *WSServer) republishOpenAttempt(attempt lifecycle.AttemptID) {
	if s.lifecyclePub == nil {
		return
	}
	current, ok := s.lifecyclePub.Attempt(attempt)
	if !ok || current.State != lifecycle.AttemptOpen {
		return
	}
	s.lifecyclePub.ReplayLane(current.Lane)
}

// dropStopStatesFor forgets one session's Stop records. Like the holds, they
// cannot outlive the session that owns them — and a fact still waiting on one
// is woken, because nothing will settle it now.
func (s *WSServer) dropStopStatesFor(sid session.ID) {
	s.stopStateMu.Lock()
	defer s.stopStateMu.Unlock()
	for attempt, rec := range s.stopStates {
		if rec.sid != sid {
			continue
		}
		rec.dropped = true
		close(rec.changed)
		rec.changed = make(chan struct{})
		delete(s.stopStates, attempt)
	}
}

// stopRecordingSignaller is the session a Stop's LADDER runs against: every
// rung it sends is recorded against the attempt the Stop is about, at the
// moment the signal is sent. That moment matters — the program dies of the
// signal and the shell reports it a round trip later, so recording only when
// the ladder returns (after it has watched the group go) would put the verdict
// behind the very completion it explains. The probes the ladder polls with
// (signal 0) are not deliveries and pass straight through.
//
// The attempt is named at the first rung, not before: a Stop whose ladder
// finds nothing to signal has nothing to record. One goroutine drives a ladder,
// so the fields need no lock.
type stopRecordingSignaller struct {
	runLeaseSession
	s       *WSServer
	sid     session.ID
	attempt func() (lifecycle.AttemptID, bool)

	named    lifecycle.AttemptID
	found    bool
	resolved bool
}

func (r *stopRecordingSignaller) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	if sig == 0 {
		return r.runLeaseSession.SignalProcessGroup(pgid, sig)
	}
	if !r.resolved {
		r.resolved = true
		r.named, r.found = r.attempt()
	}
	if !r.found {
		return r.runLeaseSession.SignalProcessGroup(pgid, sig)
	}
	r.s.beginStopDelivery(r.sid, r.named)
	err := r.runLeaseSession.SignalProcessGroup(pgid, sig)
	r.s.settleStopDelivery(r.named, err == nil)
	return err
}

// settleHeldStop is the ONE place a held Stop's own verdict is recorded and,
// when it did not land, said out loud. It is the product-visible half of the
// obligation (AGENTS.md: a soft degrade must be visible in the product, not
// only in a log): the record carries it on the lifecycle fact, and the notice
// gives the person a sentence to read.
//
// ONLY THE PARTY THAT CLAIMED THE HOLD CALLS IT, and that is the whole of its
// idempotence: claimHeldStop removes the obligation, so exactly one party can
// hold it, and that party settles it exactly once. The obligation is never
// re-armed (review major 1 of 6830b43d): a hold nobody would discharge, left on
// an attempt that had already started, is what let a closure report a second
// failure over a retry that had succeeded.
func (s *WSServer) settleHeldStop(sid session.ID, attempt lifecycle.AttemptID, delivered bool, reason string) {
	s.settleStopDelivery(attempt, delivered)
	if delivered {
		s.log.Info("foreground signal: the held Stop reached the command",
			"session_id", string(sid), "attempt", string(attempt))
		return
	}
	s.log.Warn("foreground signal: the held Stop did not reach the command",
		"session_id", string(sid), "attempt", string(attempt), "reason", reason)
	s.notifySignalUndelivered(sid, attempt, reason)
}

// discardHeldStop takes the obligation for attempt and, if this call got it,
// settles it as undelivered. It is how a party that did NOT run the delivery —
// the attempt's closure, a lane that refused the task — ends a hold: finding
// nothing to take means the delivery has it, and the delivery settles.
func (s *WSServer) discardHeldStop(attempt lifecycle.AttemptID, reason string) {
	sid, ok := s.claimHeldStop(attempt)
	if !ok {
		return
	}
	s.settleHeldStop(sid, attempt, false, reason)
}

// notifySignalUndelivered tells the session's current subscriber that a Stop it
// was told was held will not reach its command. The destination is resolved at
// emit time, exactly like lifecycle.changed and files.changed — with no
// subscriber the notice is dropped, and the record is what is left: the next
// fact for the attempt carries it.
func (s *WSServer) notifySignalUndelivered(sid session.ID, attempt lifecycle.AttemptID, reason string) {
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	params := signalUndeliveredParams{
		SessionID: string(sid),
		Signal:    signalStop,
		Attempt:   string(attempt),
		Reason:    reason,
	}
	if err := wconn.TryNotify("session.signalUndelivered", mustMarshal(params)); err != nil {
		s.log.Debug("write session.signalUndelivered", "session", string(sid), "error", err)
	}
}

// signalUndeliveredParams is the notification's params
// (contracts/session.signalUndelivered.schema.json): what failed to land, for
// which attempt, and why — addressed by session because that is what the
// renderer routes on, and by ATTEMPT because a mark belongs to one block.
type signalUndeliveredParams struct {
	SessionID string `json:"sessionId"`
	Signal    string `json:"signal"`
	Attempt   string `json:"attempt"`
	Reason    string `json:"reason"`
}

// armHeldStopAndStateLocked records an accepted Stop for attempt — the
// obligation and the delivery it counts in the attempt's record, in ONE step.
// They are armed together because they answer two halves of one question ("who
// will write this" and "what happened to it"): a fact reporting the attempt's
// closure must see the hold as a delivery on its way. The caller holds heldMu,
// because StopTarget requires the arm to be one step with the read that decided
// it.
func (s *WSServer) armHeldStopAndStateLocked(attempt lifecycle.AttemptID, sid session.ID) {
	if s.heldStops == nil {
		s.heldStops = make(map[lifecycle.AttemptID]session.ID)
	}
	// Idempotent by construction: a second Stop while one is held is the SAME
	// obligation, not a second interrupt, and not a second delivery either.
	if _, ok := s.heldStops[attempt]; ok {
		return
	}
	s.heldStops[attempt] = sid
	s.beginStopDelivery(sid, attempt)
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
// is the one that settles it. Taking removes it, so a start reported twice (a
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
// queued task, immediately before the attempt is re-read — so that a concurrent
// discard (the attempt closing) is decided by whoever gets there first rather
// than by the order two goroutines happened to run in.
func (s *WSServer) PublishAttemptStarted(attempt lifecycle.AttemptID) {
	if !s.heldStopArmed(attempt) {
		return
	}
	s.submitHeldStop(attempt)
}

// PublishAttemptClosed is the Emitter half for the other transition: the
// attempt left `open` with no start ever authenticated for it, so the Stop held
// for that start can never be delivered and is discarded — and the person is
// told, because a Stop that quietly evaporates because the command ended (or
// never started) is the failure the whole notice exists for.
func (s *WSServer) PublishAttemptClosed(attempt lifecycle.AttemptID) {
	s.discardHeldStop(attempt, undeliveredAttemptClosed)
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
			// Someone else has it: the attempt has closed and its hold was
			// discarded.
			return
		}
		s.deliverHeldStop(taskCtx, sid, attempt)
	}})
	if rej != nil {
		// The signal lane refused the task: the same refusal a person's own
		// session.signal gets at that depth, and nothing was written.
		s.log.Warn("foreground signal: the held Stop was refused a lane", "reason", rej.Reason)
		s.discardHeldStop(attempt, undeliveredLaneRefused)
	}
}

// deliverHeldStop discharges one held Stop, whose obligation the caller has
// claimed: the same policy the request path runs, against the EXACT attempt the
// Stop was accepted for, so nothing is written if that attempt has closed in
// the meantime — a byte into whatever holds the terminal next is precisely what
// a Stop must never do.
//
// EXACTLY ONE SETTLEMENT, and verdictOwned is how that is kept on one
// goroutine. The byte path does not wait for the channel and cannot time out on
// it (review of 6830b43d, blocker 1): it queues the byte with its condition and
// the writer settles it, later, through fb.settled — so once Interrupt has
// handed the verdict over, nothing here may settle it again. Every other exit
// settles here.
func (s *WSServer) deliverHeldStop(ctx context.Context, sid session.ID, attempt lifecycle.AttemptID) {
	op, err := s.signalOps.ForSession(sid)
	if err != nil {
		// ForSession refuses only when the registry has no such session, so
		// this is the session-GONE case and not a refusal to run: there is no
		// subscriber left to tell (notification resolves the session's CURRENT
		// subscriber, and a session that is not in the registry has none), and
		// the record dies with the session at dropStopStatesFor. The delivery
		// is still settled rather than left counted as on its way.
		s.log.Info("foreground signal: the held Stop ends with its session",
			"session_id", string(sid), "attempt", string(attempt), "error", err)
		s.settleStopDelivery(attempt, false)
		return
	}
	verdictOwned := false
	err = op.Run(ctx, func(runCtx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			return gerr
		}
		verdictOwned = true
		sg, ok := sess.(runLeaseSession)
		if !ok {
			s.settleHeldStop(sid, attempt, false, undeliveredUnsupported)
			return nil
		}
		handedOff := false
		fb := sessionProtectedForeground{
			ctx: runCtx, s: s, sid: sid, sess: sess,
			exactAttempt: func() (lifecycle.AttemptID, bool) { return attempt, true },
			handedOff:    &handedOff,
			settled: func(written bool, werr error) {
				s.settleHeldStop(sid, attempt, written, undeliveredReasonFor(werr))
			},
		}
		ladder := &stopRecordingSignaller{
			runLeaseSession: sg, s: s, sid: sid,
			attempt: func() (lifecycle.AttemptID, bool) { return attempt, true },
		}
		outcome := stopForeground(s.log, sid, ladder, s.effectiveRunLease().SignalGrace, fb)
		if handedOff {
			return nil // the writer settles it, or already has
		}
		if outcome == foregroundDelivered {
			s.settleHeldStop(sid, attempt, true, "")
			return nil
		}
		s.settleHeldStop(sid, attempt, false, undeliveredReasonForOutcome(outcome))
		return nil
	})
	if err != nil && !verdictOwned {
		// The lane or the session gate refused the delivery before the
		// terminal was reached: nothing was written, so it is settled here.
		s.log.Warn("foreground signal: the held Stop could not run on the signal lane",
			"session_id", string(sid), "attempt", string(attempt), "error", err)
		s.settleHeldStop(sid, attempt, false, undeliveredLaneRefused)
	}
}

// undeliveredReasonFor names what the WRITER said, in the closed vocabulary the
// renderer is given. err == nil means the condition went false at the write: the
// addressee had gone, which is not a failure of the write.
func undeliveredReasonFor(err error) string {
	switch {
	case err == nil:
		return undeliveredAttemptClosed
	case errors.Is(err, session.ErrInputRefused), errors.Is(err, session.ErrSessionClosed):
		return undeliveredWriteRefused
	default:
		return undeliveredWriteFailed
	}
}

// undeliveredReasonForOutcome is the same for a delivery that never queued a
// byte: the ladder's answer, in the same vocabulary.
func undeliveredReasonForOutcome(outcome foregroundOutcome) string {
	switch outcome {
	case foregroundNothingRunning:
		return undeliveredAttemptClosed
	case foregroundUnsupported:
		return undeliveredUnsupported
	default:
		return undeliveredWriteFailed
	}
}
