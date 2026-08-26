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

// SignalForeground sends sig to the pty's current foreground process group —
// the execution's own group, so the signal reaches the command and its
// children, never only the shell. At a prompt the foreground group is the
// shell's own and there is nothing to signal: ErrNoForeground (the shell
// must never be signaled here — it is not part of the execution it is
// waiting on). sig of 0 is the POSIX existence check: ErrNoForeground when
// the group is gone.
func (lp *LocalPty) SignalForeground(sig syscall.Signal) error {
	pgid, err := lp.ForegroundProcessGroup()
	if err != nil {
		return err
	}
	if lp.cmd.Process != nil && pgid == lp.cmd.Process.Pid {
		return ErrNoForeground
	}
	if err := unix.Kill(-pgid, sig); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrNoForeground
		}
		return fmt.Errorf("pty: signal foreground group %d: %w", pgid, err)
	}
	return nil
}
