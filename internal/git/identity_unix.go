//go:build unix

package git

import (
	"os"
	"strconv"
	"syscall"
)

// mountDomain names the mount a local checkout sits on by its device id. Two
// checkouts on one disk fail together; a FUSE or network mount has its own
// device and therefore its own bulkhead.
func mountDomain(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "checkout:" + path
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "checkout:" + path
	}
	return "mount:" + strconv.FormatUint(uint64(stat.Dev), 10) //nolint:unconvert,gosec // stat.Dev is int32 on darwin (conversion required; device id, overflow benign), uint64 on linux
}
