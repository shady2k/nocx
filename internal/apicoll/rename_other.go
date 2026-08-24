//go:build !linux && !darwin

package apicoll

import "errors"

// ErrRenameUnsupported — this platform has no no-replace rename syscall,
// and the move is refused rather than approximated.
//
// The two approximations are each wrong in the way the pair files name:
// os.Rename overwrites a destination without being asked (the collision
// criterion is the entire point of MoveRequest), and a copy-then-unlink
// leaves a window with the file at both paths and a window with it at
// neither — the exact states the move exists to forbid. A platform with
// neither fallback working correctly has no honest move, and a moved-on-
// other machines collection arriving here can be told so by name.
var errRenameUnsupported = errors.New("apicoll: a no-replace move is not supported on this platform")

func renameNoReplace(src, dst string) error {
	return errRenameUnsupported
}
