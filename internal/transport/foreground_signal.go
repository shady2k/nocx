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
// Nothing here reads the byte stream to decide anything (AD-6): the
// foreground process group comes from the kernel through TIOCGPGRP, and the
// shell's own group is never signalled — it is not part of the job it is
// waiting on (internal/pty.SignalForeground).

import (
	"errors"
	"syscall"
	"time"

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
)

// interruptForeground sends one SIGINT to the session's foreground process
// group and reports whether there was one. It does not wait and does not
// escalate; see the package comment for why.
func interruptForeground(lg log.Logger, sid session.ID, sess runLeaseSession) foregroundOutcome {
	err := sess.SignalForeground(syscall.SIGINT)
	if err == nil {
		return foregroundDelivered
	}
	if errors.Is(err, pty.ErrNoForeground) {
		return foregroundNothingRunning
	}
	// The group was there and the kill failed — a permission or a race we
	// cannot describe better than "it did not arrive". Reported as nothing
	// running rather than as delivered, because the one thing a caller must
	// not be told is that a signal landed when it did not.
	lg.Warn("foreground signal: interrupt failed",
		"session_id", string(sid), "error", err)
	return foregroundNothingRunning
}

// stopForeground runs the ladder INT → TERM → KILL against the session's
// foreground process group, waiting `grace` after each signal for the
// execution to cooperate. It returns only when the execution is gone
// (cooperated), the KILL is sent (nothing can ignore it), or there was
// nothing in the foreground to signal — never before, so a caller may
// assert on its return that the execution is actually dead rather than
// merely scheduled to die. Runs on the caller's goroutine and spawns
// nothing (ADR-0026).
func stopForeground(lg log.Logger, sid session.ID, sess runLeaseSession, grace time.Duration) foregroundOutcome {
	if grace <= 0 {
		grace = defaultRunSignalGrace
	}
	escalation := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	delivered := false
	for i, sig := range escalation {
		err := sess.SignalForeground(sig)
		if err != nil {
			if errors.Is(err, pty.ErrNoForeground) {
				// Nothing running — either from the start (a session at a
				// prompt) or because an earlier rung already ended it.
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
		if cooperatedForeground(sess, grace) {
			return foregroundDelivered
		}
		lg.Info("foreground signal: execution did not cooperate, escalating",
			"session_id", string(sid), "from", int(sig))
	}
	if delivered {
		return foregroundDelivered
	}
	return foregroundNothingRunning
}

// cooperatedForeground polls the foreground group until it is gone —
// ErrNoForeground means the shell took the foreground back (or the group
// vanished) — or `grace` elapses. A zombie waits for its parent's reap, so
// the poll has to outlive it; the shell reaps promptly.
func cooperatedForeground(sess runLeaseSession, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := sess.SignalForeground(0); err != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
