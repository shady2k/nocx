//go:build darwin

package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Seatbelt is path-based; retain a no-follow descriptor for identity recheck.
func openPinned(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		fd, err = unix.Open(path, 0x8000|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "sandbox-root"), nil
}

func identity(path string) (FileIdentity, error) {
	st, e := os.Stat(path)
	if e != nil {
		return FileIdentity{}, e
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, fmt.Errorf("stat identity unavailable")
	}
	return FileIdentity{Device: uint64(s.Dev), Inode: s.Ino}, nil
}

func verifyPinned(f *os.File, want FileIdentity, kind RootKind) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if uint64(st.Dev) != want.Device || st.Ino != want.Inode {
		return fmt.Errorf("object identity changed")
	}
	mode := st.Mode & syscall.S_IFMT
	switch kind {
	case DirectoryRoot:
		if mode != syscall.S_IFDIR {
			return fmt.Errorf("root kind changed")
		}
	case ArtifactRoot:
		if mode != syscall.S_IFREG {
			return fmt.Errorf("root kind changed")
		}
	case DeviceRoot:
		if mode != syscall.S_IFCHR {
			return fmt.Errorf("root kind changed")
		}
	default:
		return fmt.Errorf("invalid root kind")
	}
	return nil
}

func dupPinned(source *os.File) (*os.File, error) {
	fd, e := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), "sandbox-workspace"), nil
}

func canonicalActual(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		fd, err = unix.Open(path, 0x8000|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()
	return descriptorActual(fd)
}

func descriptorActual(fd int) (string, error) {
	var buf [4096]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), 50, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		return "", fmt.Errorf("path identity unavailable")
	}
	actual := string(buf[:end])
	if !utf8.Valid(buf[:end]) || !filepath.IsAbs(actual) || len(actual) > MaxPathBytes {
		return "", fmt.Errorf("path identity invalid")
	}
	return filepath.Clean(actual), nil
}
