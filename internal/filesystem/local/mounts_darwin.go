//go:build darwin

package local

import "golang.org/x/sys/unix"

// readMountTable asks the kernel for every mounted filesystem. Getfsstat is
// the call, not Statfs: Statfs names the filesystem a path is on and would
// have to touch the path, which is the hang this answers around.
//
// The resolver matches a mount point by Mntonname exactly as this call spells
// it, and answers with Mntfromname. /Volumes/<name> is the row the macOS case
// turns on: a link on the local disk to /Volumes/nfs is read from the export,
// and its domain is the export's device. MNT_NOWAIT returns the table without
// waiting for it to settle, so a volume mounted in the last instant can still
// be missing — resolveDevice states what that costs.
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
