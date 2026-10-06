//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func backendAvailable() error {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, 1)
	if errno != 0 || abi < 9 {
		return fmt.Errorf("Landlock ABI 9 unavailable")
	}
	return nil
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
	return FileIdentity{Device: s.Dev, Inode: s.Ino}, nil
}

func verifyPinned(f *os.File, want FileIdentity, kind RootKind) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Dev != want.Device || st.Ino != want.Inode {
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
