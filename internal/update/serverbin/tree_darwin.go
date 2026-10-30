//go:build darwin

package serverbin

import "golang.org/x/sys/unix"

func (osFS) RenameNew(source, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}
