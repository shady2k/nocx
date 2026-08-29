//go:build linux

package coordinator

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID reads SO_PEERCRED, the credentials the kernel stamped on the
// socket when connect(2) ran. Linux answers with a full struct ucred; only
// the uid is a trust decision here, so only the uid is returned — a pid is
// racy the moment it is read and a gid says less than the uid does.
func peerUID(fd uintptr) (uint32, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return cred.Uid, nil
}

// ownerUID stats without following a final symlink — see SystemPathOwner
// for why that matters. Linux and darwin agree on the shape of Stat_t.Uid,
// so this half of the pair is duplicated rather than shared: the file is
// three lines and a build-tagged third file would have to exist anyway for
// the platforms that answer neither question.
func ownerUID(path string) (uint32, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, os.ErrInvalid
	}
	return st.Uid, nil
}

// peerPID reports the pid the kernel recorded for the peer at connect time.
// Linux's ucred carries it beside the uid, so the same getsockopt answers
// both questions.
//
// The SERVER deliberately does not use it — peerUID above says why a pid is
// the wrong thing to make a trust decision on. The CLIENT does, and for a
// different job: a coordinator that must be replaced has to be named to the
// kernel somehow, and a pid the kernel stamped is the only name a launcher
// can get that the daemon could not have made up.
func peerPID(fd uintptr) (int, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}
