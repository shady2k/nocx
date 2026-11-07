package local

// Failure-domain naming for local filesystem work (ADR-8eej3e66 §6): a mount
// fails as a unit, so a hung mount must not cost another mount's listings.
//
// The table comes from the kernel — a read of /proc or a getfsstat — and it
// answers for a path that nothing has opened. That is not an optimisation:
// os.Stat on a hung NFS mount blocks in an uninterruptible syscall, so a domain
// lookup built on a stat would hang at the very moment the answer matters.
//
// A path is not always read under its spelling: a symlink on the local disk can
// lead to an export, and then a lexical answer charges the local disk for reads
// that reach the network (nocx-3an1s.2.14). So the lookup follows the links it
// can follow without leaving the mount it is naming — resolveDevice says where
// that line is, and why stopping there is not a compromise. What it never does
// is read the mount the answer names.
//
// The device is the identity rather than the mount point's inode: two mounts
// of one device fail together, which is the property the domain is for. A
// network export names its server in the device field, so two exports of one
// host share the domain that fails when that host stops answering.

import (
	"os"
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
	return resolveDevice(mountTableCached(), path, os.Readlink)
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

// readlinkFunc reports one path component's symlink target: os.Readlink in
// production, a fixture in the tests.
type readlinkFunc func(string) (string, error)

// maxLinkHops bounds a chain of links, as the kernel does (MAXSYMLINKS is 40
// on Linux, 32 on macOS). Beyond it the walk stops at the mount it is on,
// which is what the access would do too — a loop is an ELOOP, not a domain.
const maxLinkHops = 40

// resolveDevice answers the device of the mount an access to path will really
// read from, and false when no mount point in the table is an ancestor.
//
// IT ANSWERS WITH THE FIRST MOUNT THE ACCESS MUST TOUCH, and that is the whole
// of the fix for a lexical lookup (nocx-3an1s.2.14, finding 1). The mount
// table gives a device by longest mount-point prefix, but a path is not read
// under its spelling: /Users/me/far -> /Volumes/nfs is spelled on the local
// disk and read from the export, so a lexical answer charges the local disk
// for reads that reach NFS — and four abandoned reads then make the local
// domain suspect and refuse healthy local folders.
//
// The walk follows a symlink by reading the link, and it stops at the first
// mount point it reaches, before reading anything inside it. Two properties
// follow, and each is a test:
//
//   - The mount the answer names is never read. Reaching a mount point ends
//     the walk, so even a symlink whose target is the mount's own root costs
//     one readlink on the disk that holds the link and none on the target.
//     That is the property mounts.go exists for: a stat (or a readlink) on a
//     hung mount blocks in an uninterruptible syscall, and the lookup would
//     hang at the very moment the answer matters.
//   - No link inside the named mount can move the answer, because an access to
//     anything under it must pass through that mount first. A link deeper in
//     that points elsewhere is reached only if the mount answers, so the mount
//     is a domain the call genuinely draws on either way.
//
// What this cannot see is a mount the table does not list, which is what
// Getfsstat with MNT_NOWAIT can return for a mount arriving in the last
// instant: the walk continues into it and its first readlink is then a real
// call on that mount. And a path crossing a FIRMLINK is answered by the volume
// the spelling names, not the one the data lives on (the limitation
// mounts_darwin_test.go states).
func resolveDevice(table []mountPoint, path string, readlink readlinkFunc) (string, bool) {
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	resolved := "/"
	pending := splitComponents(path)
	hops := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			resolved = parentOf(resolved)
			continue
		}
		next := joinComponent(resolved, component)
		if dev, isPoint := mountPointDevice(table, next); isPoint {
			return dev, true
		}
		target, err := readlink(next)
		if err != nil {
			// Not a link, or nothing there yet. Either way nothing below it
			// can be a link, so the rest of the path is read lexically.
			resolved = next
			continue
		}
		hops++
		if hops > maxLinkHops {
			return resolveMount(table, resolved)
		}
		if strings.HasPrefix(target, "/") {
			resolved = "/"
		}
		pending = append(splitComponents(target), pending...)
	}
	return resolveMount(table, resolved)
}

// mountPointDevice reports the device of the mount whose point is exactly
// path. Exact, not longest-prefix: a path under a mount is not the mount, and
// the walk descends until it reaches the point itself.
func mountPointDevice(table []mountPoint, path string) (string, bool) {
	for _, m := range table {
		if m.Device != "" && m.Point == path {
			return m.Device, true
		}
	}
	return "", false
}

// splitComponents splits an absolute path into its components, keeping the
// empty and dot-dot components the kernel would walk: the walk decides what
// they mean, not the splitter.
func splitComponents(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

func joinComponent(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func parentOf(dir string) string {
	if dir == "/" {
		return "/"
	}
	if i := strings.LastIndexByte(dir, '/'); i > 0 {
		return dir[:i]
	}
	return "/"
}
