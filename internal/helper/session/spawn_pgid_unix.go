//go:build !windows

package session

import "syscall"

// processGroupOf cross-checks the process group the helper owns for a shell.
//
// The ANSWER is fixed by construction rather than by this call: internal/pty
// starts the shell with setsid, so it leads its own group and the group id is
// the pid. This asks the kernel anyway, because a cross-check that agrees is
// worth having and one that disagrees is worth seeing — and it falls back to
// the construction when the kernel cannot answer, since a shell that exists
// must not be refused over a bookkeeping call.
func processGroupOf(pid int) int {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		return pgid
	}
	return pid
}
