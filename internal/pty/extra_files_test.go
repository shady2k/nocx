package pty

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/shady2k/nocx/internal/log"
)

// TestLocalPty_ExtraFilesReachTheShell proves the lifecycle descriptor
// mechanism: an extra file passed through WithExtraFiles is inherited by the
// spawned shell as fd 3, and the shell can write to it. The child end's
// parent copy must be closed after spawn, or EOF never arrives.
func TestLocalPty_ExtraFilesReachTheShell(t *testing.T) {
	// SOCK_CLOEXEC is a Linux-only flag, and naming it here is what kept
	// this package's tests from building on macOS (nocx-1w69). The portable
	// form is create-then-mark under ForkLock — the same dance the standard
	// library does — and it is what the product's own socketpair helper does
	// off Linux.
	syscall.ForkLock.RLock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(fds[0])
		unix.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	// Non-blocking on OUR end only, and before os.NewFile: a file handed a
	// blocking socket is not registered with the runtime poller, and
	// SetReadDeadline on it is a no-op that returns ErrNoDeadline — which
	// this test used to discard, so its 3-second bound existed only on
	// paper. When the shell never wrote, Read blocked in the kernel until
	// the package's own 10-minute timeout panicked the whole run
	// (nocx-58gq). The child end stays blocking: it is fd 3 in the shell.
	if nberr := unix.SetNonblock(fds[0], true); nberr != nil {
		t.Fatalf("SetNonblock: %v", nberr)
	}
	parent := os.NewFile(uintptr(fds[0]), "test-parent")
	child := os.NewFile(uintptr(fds[1]), "test-child")
	defer func() { _ = parent.Close() }()

	// A named shell, not the developer's. What is under test is the
	// descriptor mechanism, and resolveShell would otherwise run whatever
	// $SHELL says with whatever rc files that user has — on the runner a
	// default bash, on this developer's machine a zsh that never reached
	// the write. /bin/sh reading commands from the pty needs no rc file to
	// prove an inherited fd works.
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Cols:    80,
		Rows:    24,
		Command: "/bin/sh",
		// Set on the config directly. It used to arrive through a
		// WithExtraFiles option, which had exactly one production caller —
		// internal/app's local pty factory — and went with it when the
		// daemon became the only thing that forks a shell (nocx-ie23r.3).
		ExtraFiles: []*os.File{child},
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = lp.Close() }()
	_ = child.Close() // the shell holds its own copy; ours must not keep EOF away

	if _, err := lp.Write([]byte("printf EXTRA_FILE_PROOF >&3\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	if err := parent.SetReadDeadline(deadline); err != nil {
		t.Fatalf("SetReadDeadline: %v — the wait would be unbounded", err)
	}
	var got strings.Builder
	for {
		n, readErr := parent.Read(buf)
		got.Write(buf[:n])
		if strings.Contains(got.String(), "EXTRA_FILE_PROOF") {
			return
		}
		if readErr != nil {
			t.Fatalf("shell output never arrived on the extra fd: %v (read %q)", readErr, got.String())
		}
	}
}
