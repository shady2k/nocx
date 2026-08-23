// Package proc owns one behaviour for the whole repository: running a child
// process under our own control and being CERTAIN it is gone.
//
// It exists because the answer was about to be written twice. The git
// adapter (internal/git/local) already ran every invocation in its own
// process group and escalated INT → TERM → KILL against that group, for a
// reason that is not specific to git at all: a child that spawns helpers —
// a textconv filter, a shell pipeline, a completion enumeration — keeps the
// inherited pipe open, so killing the direct child alone leaves the work
// running and the read waiting for an EOF that never comes. Command
// discovery (internal/commandnames) needs exactly that guarantee, and a
// second implementation of it would agree with the first everywhere anyone
// looked and disagree somewhere nobody did.
//
// What is NOT here is git's `run`: it owns stdin plumbing, a stop-sink, a
// bounded stderr buffer and the attribution of WHY a child stopped (cut vs
// cancelled vs failed), which are that adapter's policy and not this
// behaviour. The nucleus below is the part both callers need and neither
// may re-decide.
package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// InOwnGroup makes cmd the leader of a new process group, so a signal sent
// to the negated pid reaches the child AND everything it spawns — and
// reaches nothing else, in particular not us. Call it before Start; after
// Start it has no effect, because the attribute is read at fork.
func InOwnGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillGroup escalates INT → TERM → KILL against cmd's process group and
// returns as soon as the child is reaped (done closes) or KILL has been
// sent. The group id is the child's own pid because InOwnGroup made it the
// leader.
//
// grace is the pause between escalation steps. It is a parameter rather
// than a constant because the right value is the caller's knowledge: git
// handles INT promptly and can afford to ask politely first, while an
// enumeration pipeline under a deadline has already spent its budget by the
// time this runs.
//
// The escalation is not decoration. A member that ignores TERM is the case
// this exists for; without the final KILL the group survives the deadline
// and the work the deadline was supposed to stop goes on running.
//
// KillGroup does not reap: the caller owns cmd.Wait, and `done` is how it
// tells us the reaping happened. Sending a signal into a group whose leader
// has already been reaped is what the done checks below avoid — that pid may
// by then belong to somebody else.
func KillGroup(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL} {
		select {
		case <-done:
			return
		default:
		}
		_ = syscall.Kill(-pgid, sig)
		if sig == syscall.SIGKILL {
			return
		}
		select {
		case <-done:
			return
		case <-time.After(grace):
		}
	}
}
