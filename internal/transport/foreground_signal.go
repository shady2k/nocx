package transport

// ONE OWNER FOR "SIGNAL WHAT IS RUNNING IN THIS SESSION" (nocx-23rph).
//
// The escalation ladder INT → TERM → KILL was written for the agent's run
// lease (ADR-0020 decision 2) and lived inside it. It has a second caller
// now: a person's Stop on a running block's ⋮ menu asks exactly the same
// question the lease's timeout asks — how do you stop a process, and how
// long do you wait for it to go quietly — and a second answer would be two
// policies that agree on every command anyone tries and disagree on the one
// that traps SIGTERM. So the ladder moved here, the lease calls it, and the
// wire method calls it.
//
// The two intents over that one policy:
//
//   - interrupt is ONE SIGINT. It is what the byte 0x03 causes through the
//     line discipline, and what a keyboard Ctrl+C means: interrupt this, and
//     I may press it again. It does not wait, and it does not escalate — a
//     program that chooses to ignore SIGINT has made a decision the person
//     pressing Ctrl+C has not overruled.
//   - stop is the ladder. It is what a menu item called Stop promises: when
//     it answers, the execution is gone rather than merely asked to leave.
//
// ONE POLICY, TWO MECHANISMS — never two policies (nocx-7l4ex.10/.12). The
// kernel's answer about foreground TOPOLOGY chooses the mechanism, and
// nothing else does; there is no mode string and no caller preference.
//
//   - An INDEPENDENT foreground group is the ordinary case. The signal goes
//     to that group with kill(2), and stop escalates through it.
//   - A PROTECTED group — the launcher shell's own, which job control off
//     (`set +m`, ADR-0024) or `exec` produces, and which is also what an
//     idle prompt looks like — may hold a running program, and it may never
//     be killed. Over it the mechanism is the terminal's own interrupt byte,
//     0x03, written to the pty exactly as a focused Ctrl+C writes it, and it
//     is available only while an authenticated lifecycle attempt says a
//     program is there (protectedForeground below). There is no escalation
//     over a protected group: nothing here will ever send TERM or KILL into
//     the shell's group, so `stop` keeps its promise by WAITING for that
//     exact attempt to end and says `unreconciled` when it cannot.
//
// Two honesty rules the split exists to keep. A kill(2) that FAILED is never
// reported as delivery and never selects the fallback — it is not evidence
// about topology, only about the call. And 0x03 is a byte, not a signal: the
// line discipline turns it into SIGINT for the whole foreground group while
// ISIG is set, which in the protected case includes the launcher shell (it
// ignores it), and a program that has cleared ISIG receives it as input. So
// the promise is "the terminal's own interrupt was written", never "a SIGINT
// arrived" — see contracts/session.signal.schema.json.
//
// THE TWO INTENTS DIFFER IN THEIR ADDRESSEE, AND THAT IS THE DISTINCTION
// (nocx-uvac6.11). An interrupt is about the present moment, so it asks the
// kernel what is in front and signals that. A stop is a conversation with one
// job across several seconds, so it names that job's process group ONCE and
// keeps it for every rung and for the existence poll. Built out of repeated
// "what is in front" questions instead, the ladder walked onto whatever the
// person started after the job it was stopping had already exited.
//
//
// Nothing here reads the byte stream to decide anything (AD-6): the
// foreground process group comes from the kernel through TIOCGPGRP, and
// whether a program is inside the protected group is answered by the
// authenticated lifecycle (ADR-0024), never inferred from output.

import (
	"errors"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

// foregroundOutcome is what a signal actually did, in the wire's words.
// The contract (contracts/session.signal.schema.json) carries the same set,
// which is deliberate: the handler translates nothing, so the words a
// person is shown are the words this decided.
type foregroundOutcome string

const (
	// foregroundDelivered — the foreground group existed and the signal
	// reached it.
	foregroundDelivered foregroundOutcome = "delivered"
	// foregroundNothingRunning — the pty's foreground group is the
	// interactive shell's own, so there is no execution to signal.
	foregroundNothingRunning foregroundOutcome = "nothing-running"
	// foregroundUnsupported means the session has no local process group the
	// backend can signal, notably a remote host. It is distinct from
	// foregroundNothingRunning so a caller cannot claim a command was stopped.
	foregroundUnsupported foregroundOutcome = "unsupported"
	// foregroundUnreconciled means lifecycle still names an execution after
	// the shell-group guard prevented direct signalling and the safe terminal
	// interrupt could not prove that exact attempt ended.
	foregroundUnreconciled foregroundOutcome = "unreconciled"
)

// protectedForeground is what this package cannot answer for itself over a
// protected group: whether a program is in there, and how to reach it
// without kill(2). The caller that holds the lifecycle projection and the
// session's input queue supplies it (ws_signal.go); a caller that holds
// neither passes nil and gets the honest refusal, which is what the run
// lease does — it has no authenticated attempt to name, and inventing one
// would be exactly the guess AD-6 forbids.
//
// The request's context is captured by the implementation rather than passed
// through here, because it belongs to one request and this policy is shared
// by callers that have no request at all.
type protectedForeground interface {
	// Attempt names the single authenticated, STARTED execution the backend
	// projects for this session, or reports that there is not exactly one.
	// Started is load-bearing: an app attempt is open from SUBMIT, before
	// the bytes that could cause it are written (lifecycle-protocol §7), so
	// a merely-submitted attempt is not evidence that anything is running.
	Attempt() (lifecycle.AttemptID, bool)
	// Interrupt writes the terminal's own interrupt byte through the
	// session's ordinary input path. False means the write was refused —
	// the queue is full, closed, or quarantined — and nothing was sent.
	Interrupt(attempt lifecycle.AttemptID) bool
	// Ended reports whether that exact attempt has left `open`, waiting at
	// most the cooperative bound. It observes the backend's own lifecycle
	// read model; it never reads the terminal.
	Ended(attempt lifecycle.AttemptID, grace time.Duration) bool
}

// foregroundReach is what one SignalForeground call actually established,
// which is four things and used to be collapsed into two (nocx-7l4ex.10).
type foregroundReach int

const (
	// reachDelivered: the group existed, was not the shell's, and the
	// signal was accepted by the kernel.
	reachDelivered foregroundReach = iota
	// reachProtected: the foreground group is the launcher shell's own. A
	// program may be running inside it; the kernel cannot say.
	reachProtected
	// reachGone: there is no foreground group left to signal.
	reachGone
	// reachFailed: the call itself failed. It says nothing about what is
	// running, so it may not select a mechanism and may not claim delivery.
	reachFailed
)

func classifyForeground(err error) foregroundReach {
	switch {
	case err == nil:
		return reachDelivered
	case errors.Is(err, pty.ErrProtectedForeground):
		return reachProtected
	case errors.Is(err, pty.ErrNoForeground):
		return reachGone
	default:
		return reachFailed
	}
}

// interruptForeground sends one SIGINT to the session's foreground process
// group, or the terminal's own interrupt byte when that group is protected
// and lifecycle names a started execution inside it. It does not wait and
// does not escalate; see the package comment for why.
func interruptForeground(lg log.Logger, sid session.ID, sess runLeaseSession, fb protectedForeground) foregroundOutcome {
	err := sess.SignalForeground(syscall.SIGINT)
	switch classifyForeground(err) {
	case reachDelivered:
		return foregroundDelivered
	case reachProtected:
		attempt, ok := protectedAttempt(lg, sid, fb)
		if !ok {
			// The ordinary prompt: the shell is waiting and nothing is
			// running. This is the refusal a person can act on.
			return foregroundNothingRunning
		}
		if !fb.Interrupt(attempt) {
			lg.Warn("foreground signal: the session refused the terminal interrupt",
				"session_id", string(sid), "attempt", string(attempt))
			return foregroundUnreconciled
		}
		lg.Info("foreground signal: terminal interrupt written to a protected group",
			"session_id", string(sid), "attempt", string(attempt))
		return foregroundDelivered
	case reachGone:
		return foregroundNothingRunning
	default:
		// The group was there and the call failed — a permission error or a
		// race we cannot describe better than "it did not arrive". NOT
		// nothing-running, which would be a lie a person acts on, and NOT
		// the fallback, which this failure is no evidence for.
		lg.Warn("foreground signal: interrupt failed",
			"session_id", string(sid), "error", err)
		return foregroundUnreconciled
	}
}

// stopForeground keeps Stop's promise, and WHICH promise it can keep is
// decided by the kernel's answer about foreground topology — never by a
// caller's preference. This is where nocx-uvac6.11 and nocx-7l4ex.10 meet.
//
// Over an INDEPENDENT group it runs the ladder INT → TERM → KILL against ONE
// process group, NAMED ONCE before the first signal, and returns only when
// that group is gone, the KILL is sent, or there was nothing to signal.
// Over a PROTECTED group there is no ladder at all — every rung would reach
// the launcher shell — so it writes the terminal's interrupt once and waits
// for that exact authenticated attempt to leave `open`, saying
// `unreconciled` when it cannot. Runs on the caller's goroutine and spawns
// nothing (ADR-0026).
//
// IT CLASSIFIES THE ERROR ITSELF RATHER THAN GOING THROUGH foregroundJob,
// and that is load-bearing rather than a refactor. ErrProtectedForeground
// WRAPS ErrNoForeground, so a plain errors.Is(err, ErrNoForeground) is true
// for BOTH and would fold the protected case into "nothing is running" —
// the exact lie the incident was reported as (nocx-7l4ex.12), leaving the
// protected branch unreachable with the compiler silent about it. Specific
// before general, and the order is the correctness.
func stopForeground(lg log.Logger, sid session.ID, sess runLeaseSession, grace time.Duration, fb protectedForeground) foregroundOutcome {
	pgid, err := sess.ForegroundJob()
	switch classifyForeground(err) {
	case reachDelivered:
		return stopProcessGroup(lg, sid, sess, pgid, grace)
	case reachProtected:
		return stopProtected(lg, sid, fb, grace)
	case reachGone:
		return foregroundNothingRunning
	default:
		// The call itself failed. Something may well be there and nocx
		// cannot prove it stopped — never nothing-running, which is a
		// refusal a person acts on.
		lg.Warn("foreground signal: could not name the foreground job",
			"session_id", string(sid), "error", err)
		return foregroundUnreconciled
	}
}

// stopProtected is Stop over a group that may never be killed. The byte goes
// in, and then the ONLY proof available is the authenticated attempt closing
// — the foreground group cannot supply it, because over a protected group
// the shell's group is the foreground group whether the program lives or
// dies (which is what made the incident's contradiction possible).
func stopProtected(lg log.Logger, sid session.ID, fb protectedForeground, grace time.Duration) foregroundOutcome {
	attempt, ok := protectedAttempt(lg, sid, fb)
	if !ok {
		return foregroundNothingRunning
	}
	if !fb.Interrupt(attempt) {
		lg.Warn("foreground signal: the session refused the terminal interrupt",
			"session_id", string(sid), "attempt", string(attempt))
		return foregroundUnreconciled
	}
	if fb.Ended(attempt, grace) {
		return foregroundDelivered
	}
	lg.Warn("foreground signal: the attempt stayed open after the terminal interrupt",
		"session_id", string(sid), "attempt", string(attempt))
	return foregroundUnreconciled
}

// protectedAttempt asks the caller's projection whether a program is inside
// the protected group. A nil fallback is not a failure: it is a caller that
// holds no lifecycle, and the honest answer there is the prompt's.
func protectedAttempt(lg log.Logger, sid session.ID, fb protectedForeground) (lifecycle.AttemptID, bool) {
	if fb == nil {
		return "", false
	}
	attempt, ok := fb.Attempt()
	if !ok {
		// Warn rather than Debug: this line is the difference between "the
		// pane really was at a prompt" and "a program was in there and nocx
		// would not touch it", and the second is a person's Stop doing
		// nothing. It is the first thing to read when that is reported.
		lg.Warn("foreground signal: protected group, no authenticated started attempt — reporting the prompt",
			"session_id", string(sid))
		return "", false
	}
	return attempt, true
}

// foregroundJob names the group a stop is about to address, or reports why
// there is none. Separate from the ladder because a caller can need the NAME
// earlier than the signals: withdrawing an assistant request can let its
// command exit, and after that "what is in front" is somebody else's
// (nocx-uvac6.11).
func foregroundJob(lg log.Logger, sid session.ID, sess runLeaseSession) (int, foregroundOutcome, bool) {
	pgid, err := sess.ForegroundJob()
	switch classifyForeground(err) {
	case reachDelivered:
		return pgid, foregroundDelivered, true
	case reachProtected, reachGone:
		// A protected group is "not named" here, and that is honest rather
		// than a fold: this seam hands back a pgid to signal, and a
		// protected group has none that may be signalled. It is NOT a claim
		// that nothing runs — a caller holding a lifecycle projection can
		// still reach in, which is why stopForeground classifies the error
		// itself instead of coming through here. The one caller that does
		// (run_lease's cancelExecution) holds no projection, and the
		// prompt's answer is what a nil fallback already gave it.
		return 0, foregroundNothingRunning, false
	default:
		lg.Warn("foreground signal: could not name the foreground job",
			"session_id", string(sid), "error", err)
		return 0, foregroundNothingRunning, false
	}
}

// stopProcessGroup runs the ladder against ONE group, named by an earlier
// foregroundJob. Everything the package comment says about a stop's addressee
// is enforced here: this function never asks what is in front.
func stopProcessGroup(lg log.Logger, sid session.ID, sess runLeaseSession, pgid int, grace time.Duration) foregroundOutcome {
	if grace <= 0 {
		grace = defaultRunSignalGrace
	}
	escalation := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	delivered := false
	for i, sig := range escalation {
		err := sess.SignalProcessGroup(pgid, sig)
		if err != nil {
			if errors.Is(err, pty.ErrNoForeground) {
				// The group is gone — either from the start or because an
				// earlier rung already ended it.
				if delivered {
					return foregroundDelivered
				}
				return foregroundNothingRunning
			}
			lg.Warn("foreground signal: escalation step failed",
				"session_id", string(sid), "signal", int(sig), "error", err)
			continue
		}
		delivered = true
		if i == len(escalation)-1 {
			return foregroundDelivered // KILL — the execution is gone
		}
		if groupGone(sess, pgid, grace) {
			return foregroundDelivered
		}
		lg.Info("foreground signal: execution did not cooperate, escalating",
			"session_id", string(sid), "signal", int(sig), "pgid", pgid)
	}
	if delivered {
		return foregroundDelivered
	}
	// Every rung failed and none of them was the guard: something is there
	// and nocx cannot prove it stopped. NOT nothing-running — that sentence
	// is shown to a person beside a block that is still running, and it is
	// the lie nocx-7l4ex.12 was filed as. Reachable: SignalProcessGroup
	// folds only ESRCH and pgid <= 0 into ErrNoForeground, so an EPERM from
	// kill(-pgid) — a group the backend may not signal, which is what
	// `sudo` inside the execution produces — lands here three times over.
	return foregroundUnreconciled
}

// groupGone polls ONE process group until it is gone — ErrNoForeground means
// that group has exited — or `grace` elapses. A zombie waits for its parent's
// reap, so the poll has to outlive it; the shell reaps promptly.
//
// It asks about the group the ladder addressed, never about "is anything in
// the foreground": the second question answers yes for a command the person
// started afterwards, which is how the escalation used to walk onto it.
func groupGone(sess runLeaseSession, pgid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := sess.SignalProcessGroup(pgid, 0); err != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
