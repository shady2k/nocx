//go:build !linux

package lifecyclechannel

// The non-Linux half of the socketpair pair (nocx-1w69). Darwin — the other
// platform this product ships on — has no SOCK_CLOEXEC for socketpair(2), so
// close-on-exec has to be set in a second step, and the gap between the two
// is a real leak: a concurrent fork/exec anywhere in the process inherits a
// descriptor that was never meant for it. On a lifecycle channel that gap
// matters more than usual, because the descriptor IS the authority the
// protocol authenticates.
//
// syscall.ForkLock is the lock the standard library uses for exactly this:
// os/exec takes it exclusively across fork, so holding it for read across
// create-then-mark makes the pair unforkable-through. This is the same dance
// net and os/exec perform on platforms without the atomic flag.
//
// The Linux half keeps the atomic single-syscall form; see socketpair_linux.go
// for why the two are separate files rather than one levelled-down path.

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// socketpairCloexec returns a connected SOCK_STREAM pair with close-on-exec
// set on both descriptors. The caller passes the child end to the shell
// through exec.Cmd.ExtraFiles, which dups it onto the child's fd 3 — the dup
// clears close-on-exec for that copy alone, so the descriptor reaches the
// shell and no other exec inherits it.
func socketpairCloexec() ([2]int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return [2]int{}, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	return [2]int{fds[0], fds[1]}, nil
}

func NewSocketPair() (*os.File, *os.File, error) {
	fds, err := socketpairCloexec()
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "lifecycle-parent"),
		os.NewFile(uintptr(fds[1]), "lifecycle-child"), nil
}
