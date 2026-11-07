package local

// Failure-domain naming for local filesystem work (ADR-8eej3e66 §6): a mount
// fails as a unit, so a hung mount must not cost another mount's listings.
//
// The table comes from the kernel, and resolving a path against it never
// touches that path. That is not an optimisation: os.Stat on a hung NFS mount
// blocks in an uninterruptible syscall, so a domain lookup built on a stat
// would hang at the very moment the answer matters. Reading the mount table
// is a read of /proc or a getfsstat on the running system, and it answers for
// a path that nothing has opened.
//
// The device is the identity rather than the mount point's inode: two mounts
// of one device fail together, which is the property the domain is for. A
// network export names its server in the device field, so two exports of one
// host share the domain that fails when that host stops answering.

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// mountPoint is one row of the kernel's mount table.
type mountPoint struct {
	Point  string
	Device string
}

// mountTableTTL is how long a read table is reused. A table is consulted on
// every filesystem call, and a mount arriving or leaving mid-call only moves
// a path between domains; it changes no answer the caller can observe, so a
// stale table costs at most one call bounded by the wrong (also finite)
// bulkhead.
const mountTableTTL = 30 * time.Second

var (
	mountMu       sync.Mutex
	mountCache    []mountPoint
	mountCachedAt time.Time
)

// mountDevice reports the device a path belongs to, from the mount table. It
// returns false when the path is not under any mount point in the table,
// which is the state a chroot or a namespace with an unreadable table leaves.
func mountDevice(path string) (string, bool) {
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	return resolveMount(mountTableCached(), path)
}

func mountTableCached() []mountPoint {
	mountMu.Lock()
	defer mountMu.Unlock()
	if mountCache != nil && time.Since(mountCachedAt) < mountTableTTL {
		return mountCache
	}
	mountCache = readMountTable()
	mountCachedAt = time.Now()
	return mountCache
}

// resolveMount answers the longest mount point that is a path prefix of
// path. The boundary matters: /home must not claim a path under /homework.
func resolveMount(table []mountPoint, path string) (string, bool) {
	sorted := append([]mountPoint(nil), table...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Point) > len(sorted[j].Point) })
	for _, m := range sorted {
		if m.Device == "" || m.Point == "" {
			continue
		}
		if path == m.Point {
			return m.Device, true
		}
		prefix := m.Point
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(path, prefix) {
			return m.Device, true
		}
	}
	return "", false
}
