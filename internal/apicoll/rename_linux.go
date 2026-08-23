//go:build linux

package apicoll

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace moves the file at src to dst with ONE property beyond
// os.Rename: it REFUSES to replace an existing destination. The move is a
// rename(2), the same syscall a plain rename is, so there is never a window
// with the file at both paths or at neither — but with RENAME_NOREPLACE the
// kernel answers EEXIST instead of overwriting when dst already holds
// something.
//
// The two halves of the property matter equally for a move: a
// check-then-rename would report "free" and then replace a file somebody's
// git pull landed in between, and a plain rename would overwrite the
// destination without ever being asked whether it could.
//
// Linux spells the no-replace flag RENAME_NOREPLACE on renameat2(2);
// darwin spells the same refusal RENAME_EXCL on renameatx_np(2)
// (rename_darwin.go). The sentinel on the error is os.ErrExist either
// way, so the caller distinguishes "something is there" from "the rename
// failed" without a per-platform spelling of the errno.
func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return os.ErrExist
	}
	return err
}
