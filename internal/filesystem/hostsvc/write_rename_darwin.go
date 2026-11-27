//go:build darwin

package hostsvc

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplace installs the temporary only if the target name is still
// free, atomically — RENAME_EXCL refuses an existing name in the same call
// that installs, which is what makes `expect.absent` a condition rather than a
// hope (the plan's renamex_np half).
func renameNoReplace(oldpath, newpath string) error {
	err := unix.RenamexNp(oldpath, newpath, unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return renameNoReplaceFallback(oldpath, newpath)
	}
	return err
}
