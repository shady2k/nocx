//go:build unix

package hostsvc

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusive takes a non-blocking exclusive advisory lock on f, which is the
// fact that separates a live daemon's journal directory from an abandoned one:
// the kernel drops the lock when the last descriptor holding it is closed, and
// a process killed by SIGKILL closes its descriptors like any other. The
// non-blocking form is what makes the question answerable at all — a sweep that
// waited for a lock would hang behind a daemon that is serving normally.
func lockExclusive(f *os.File) error {
	// #nosec G115 — a descriptor is a small non-negative int; flock takes one.
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
