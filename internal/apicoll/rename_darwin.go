//go:build darwin

package apicoll

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace is the darwin half of the pair rename_linux.go
// describes: the same no-overwrite rename, spelled RENAME_EXCL on
// renameatx_np(2). The machine on the other side of rename_linux.go's
// comment is exactly the one this compiles for, so "the collection is
// shared through git" holds on a Mac with the same refusal, not a
// weaker one.
//
// renameatx_np's flags are the reason this file exists: FreeBSD's
// renameatx is a netbsd-imported syscall with a different flagset, and
// Windows has no no-replace rename at all — the refusal there lives in
// rename_other.go as the honest answer for a platform this product does
// not ship a move on.
func renameNoReplace(src, dst string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		return os.ErrExist
	}
	return err
}
