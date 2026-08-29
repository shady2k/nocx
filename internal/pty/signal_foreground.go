//go:build darwin || linux

package pty

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// ForegroundProcessGroup reports the pty's current foreground process group:
// the execution's own process group while a job runs — the interactive
// shell's job control puts each foreground job in its own group — and the
// shell's own group at a prompt. 0 when no process was ever started.
//
// What internal/pty already did about process groups, for the record:
// creack/pty's StartWithSize sets Setsid + Setctty on the shell, so the
// shell is a session leader in its own process group, and nothing else —
// the per-execution group of ADR-0020 decision 2 ("each execution runs in
// its own process group so cancellation reaches the children") is the
// foreground job's group, which the SHELL creates and which is discovered
// here on the master via TIOCGPGRP (tcgetpgrp) — the kernel's answer, never
// a guess from the byte stream (AD-6).
func (lp *LocalPty) ForegroundProcessGroup() (int, error) {
	if lp.cmd.Process == nil {
		return 0, ErrNoForeground
	}
	pgid, err := unix.IoctlGetInt(int(lp.file.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0, fmt.Errorf("pty: read foreground process group: %w", err)
	}
	return pgid, nil
}

// ForegroundJob names the process group of the foreground JOB — the execution
// the interactive shell is waiting on. It differs from ForegroundProcessGroup
// in exactly one case, and that case is the whole reason it exists: at a
// prompt the foreground group is the SHELL'S OWN, and the shell must never be
// signalled — it is not part of the execution it is waiting on. That is
// ErrProtectedForeground here (nocx-7l4ex.10), which WRAPS ErrNoForeground:
// a caller that only wants "nothing to cancel" keeps reading one error,
// while a caller that can reach into a protected group — job control off
// (`set +m`) or `exec` can leave a running program in there — can tell the
// two apart.
//
// After nocx-uvac6.11 split the addressee from the signal, THIS IS THE ONLY
// PLACE THE SHELL BRANCH EXISTS. SignalForeground below is a composition and
// knows nothing about the shell; the ladder reaches the kernel through
// SignalProcessGroup. Returning the specific error from anywhere else would
// leave the ladder unable to see it.
//
// A caller that is going to signal more than once needs this, because a
// process group is a stable addressee and "whatever is in front" is not: the
// job it names can exit between two rungs of an escalation and the next
// command a person starts takes its place (nocx-uvac6.11).
func (lp *LocalPty) ForegroundJob() (int, error) {
	pgid, err := lp.ForegroundProcessGroup()
	if err != nil {
		return 0, err
	}
	if lp.cmd.Process != nil && pgid == lp.cmd.Process.Pid {
		return 0, ErrProtectedForeground
	}
	return pgid, nil
}

// SignalProcessGroup sends sig to one exact process group, named by an earlier
// ForegroundJob. sig of 0 is the POSIX existence check: ErrNoForeground once
// the group is gone. A group that has exited is not an error the caller can do
// anything about — it is the answer it was asking for.
func (lp *LocalPty) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return ErrNoForeground
	}
	if err := unix.Kill(-pgid, sig); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrNoForeground
		}
		return fmt.Errorf("pty: signal process group %d: %w", pgid, err)
	}
	return nil
}

// SignalForeground sends sig to the pty's current foreground job — the
// execution's own group, so the signal reaches the command and its children,
// never only the shell. It is the ONE-SHOT form, for an intent that is about
// the present moment (a person's Ctrl+C: interrupt this, and I may press it
// again). An escalation must not be built out of repeated calls to it; use
// ForegroundJob once and SignalProcessGroup for each rung.
func (lp *LocalPty) SignalForeground(sig syscall.Signal) error {
	pgid, err := lp.ForegroundJob()
	if err != nil {
		return err
	}
	return lp.SignalProcessGroup(pgid, sig)
}
