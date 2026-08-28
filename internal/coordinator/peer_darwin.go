//go:build darwin

package coordinator

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID is the darwin half of the pair peer_linux.go describes. There is
// no SO_PEERCRED here; LOCAL_PEERCRED is the same question — the
// credentials the kernel stamped on the socket at connect time — spelled as
// a struct xucred at the SOL_LOCAL level. It is what getpeereid(3) itself
// calls, reached directly because x/sys/unix exposes this and not that.
//
// Only the uid is a trust decision, so only the uid crosses back.
func peerUID(fd uintptr) (uint32, error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return cred.Uid, nil
}

// ownerUID stats without following a final symlink — see SystemPathOwner.
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
