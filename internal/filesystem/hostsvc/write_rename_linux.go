//go:build linux

package hostsvc

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplace installs the temporary only if the target name is still
// free, atomically — the check and the install are one system call, which is
// what makes `expect.absent` a condition rather than a hope (the plan's
// renameat2 half).
func renameNoReplace(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	// A filesystem or kernel that does not carry the flag answers one of
	// these; the link+unlink pair has the same meaning. Any other error —
	// EEXIST above all — is the answer itself.
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return renameNoReplaceFallback(oldpath, newpath)
	}
	return err
}
