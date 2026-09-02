//go:build linux

package lifecyclechannel

// The Linux half of the socketpair pair (nocx-1w69). Linux can set
// close-on-exec ATOMICALLY, in the same syscall that creates the pair, and
// that is worth keeping as its own file rather than levelling both platforms
// down to the portable dance: between creating a descriptor and marking it,
// any other goroutine's fork/exec inherits it. On Linux there is no such
// window at all.
//
// The other half (socketpair_other.go) closes the same window with
// syscall.ForkLock, which is what the standard library does where the atomic
// flag does not exist. Both halves must stay listed in the deadcode
// baseline: the update script sees one OS at a time and reads the half it
// did not compile as a free shrink.

import (
	"os"

	"golang.org/x/sys/unix"
)

// socketpairCloexec returns a connected SOCK_STREAM pair with close-on-exec
// already set on both descriptors. The caller passes the child end to the
// shell through exec.Cmd.ExtraFiles, which dups it onto the child's fd 3 —
// and the dup is what clears close-on-exec for that copy alone, so the
// descriptor reaches the shell while never leaking into any other exec.
func socketpairCloexec() ([2]int, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return [2]int{}, err
	}
	return [2]int{fds[0], fds[1]}, nil
}

// NewSocketPair returns the parent and child ends of an authenticated-channel
// carrier. The child is intended for exec.Cmd.ExtraFiles; the parent remains
// with the owner that forwards raw bytes to the coordinator.
func NewSocketPair() (*os.File, *os.File, error) {
	fds, err := socketpairCloexec()
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "lifecycle-parent"),
		os.NewFile(uintptr(fds[1]), "lifecycle-child"), nil
}
