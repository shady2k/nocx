//go:build windows

package pty

import "syscall"

// Windows has no POSIX process groups and no kill(2): there is no process
// group to discover or signal, so both seams report ErrNoForeground and the
// lease's INT → TERM → KILL escalation is a no-op there. Process supervision
// on Windows — the phase-3 platform, where the ADR-0020 consequence "must
// work on macOS, Linux and Windows" lands — is a separate bead (Job Objects,
// not kill(2)); this stub keeps the package compiling and the lease honest
// (it terminalizes the run either way; only the kill is unavailable).
func (lp *LocalPty) ForegroundProcessGroup() (int, error) { return 0, ErrNoForeground }

func (lp *LocalPty) SignalForeground(_ syscall.Signal) error { return ErrNoForeground }
