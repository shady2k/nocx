//go:build unix

package mux

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Descriptor passing, which is what makes a mux session a session at all: the
// master does not copy bytes for us, it takes three of OUR descriptors and
// wires the far channel straight onto them.
//
// The send goes through the connection's RawConn rather than a bare
// unix.Sendmsg on the numeric descriptor. Go's netpoller owns that
// descriptor; writing to it behind the runtime's back races the poller and
// mishandles EAGAIN, and RawConn.Write is the documented way to borrow it
// (it retries when the callback reports "not done").

// socketPair returns a connected pair of stream sockets: the first is handed
// to the master, the second stays here. A socketpair rather than a pipe
// because each half must be a full descriptor the other side can poll, and
// because a pipe is one-directional — the master expects to be able to treat
// all three the way it treats a terminal's.
func socketPair() (theirs, ours *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("mux: socketpair: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "mux-remote"), os.NewFile(uintptr(fds[1]), "mux-local"), nil
}

// sendFD passes one descriptor over the control socket with SCM_RIGHTS, in
// the shape OpenSSH's own mux client uses: one byte of payload per
// descriptor, one sendmsg each, in stdin/stdout/stderr order.
func sendFD(c *net.UnixConn, f *os.File) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	rights := unix.UnixRights(int(f.Fd()))
	var opErr error
	ctrlErr := raw.Write(func(fd uintptr) bool {
		opErr = unix.Sendmsg(int(fd), []byte{0}, rights, nil, 0)
		if opErr == unix.EAGAIN || opErr == unix.EWOULDBLOCK {
			return false // not done: let the poller wait for writability
		}
		return true
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return opErr
}
