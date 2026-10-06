//go:build linux

package sandbox

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openPinned(path string, _ RootKind) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "sandbox-root"), nil
}

func dupPinned(source *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "sandbox-workspace"), nil
}

func canonicalActual(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
