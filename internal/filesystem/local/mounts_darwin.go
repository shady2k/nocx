//go:build darwin

package local

import "golang.org/x/sys/unix"

// readMountTable asks the kernel for every mounted filesystem. Getfsstat is
// the call, not Statfs: Statfs names the filesystem a path is on and would
// have to touch the path, which is the hang this answers around.
func readMountTable() []mountPoint {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n == 0 {
		return nil
	}
	buf := make([]unix.Statfs_t, n)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil
	}
	out := make([]mountPoint, 0, n)
	for i := range n {
		out = append(out, mountPoint{
			Point:  unix.ByteSliceToString(buf[i].Mntonname[:]),
			Device: unix.ByteSliceToString(buf[i].Mntfromname[:]),
		})
	}
	return out
}
