//go:build unix

package hostsvc

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity names the object behind an open descriptor as dev:ino. Two
// reads of one file that an append grew share it; a replacement changes it,
// because the new file is a different inode. Empty means this platform cannot
// say — a caller then has only the size and the mtime, and is told no more.
func fileIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}
