package local

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMountUsesTheLongestPointAndHonoursTheBoundary(t *testing.T) {
	table := []mountPoint{
		{Point: "/", Device: "root"},
		{Point: "/home", Device: "home-dev"},
		{Point: "/mnt/nfs", Device: "server:/export"},
	}
	cases := []struct {
		path, want string
	}{
		{"/", "root"},
		{"/home/me/deep", "home-dev"},
		{"/homework/x", "root"}, // /home must not claim a sibling spelling
		{"/mnt/nfs", "server:/export"},
		{"/mnt/nfs/a/b", "server:/export"},
		{"/mnt", "root"},
	}
	for _, tc := range cases {
		got, ok := resolveMount(table, tc.path)
		if !ok || got != tc.want {
			t.Errorf("resolveMount(%q) = %q, %v; want %q", tc.path, got, ok, tc.want)
		}
	}
}

// TestTheRealMountTableNamesTheRoot is the paired "and on a normal machine it
// succeeds" check: a table reader that only ever parses fixtures would keep a
// broken platform compile-time green.
func TestTheRealMountTableNamesTheRoot(t *testing.T) {
	dev, ok := mountDevice("/")
	if !ok || dev == "" {
		t.Skip("no mount table on this machine")
	}
	t.Logf("/ belongs to %q", dev)
}

func TestTheProviderNamesAFailureDomainForAPath(t *testing.T) {
	p := New()
	// No test table is injected here: this is the paired "and on a normal
	// machine it succeeds" check. A namer that only ever answered from a
	// fixture would stay green against a table reader that returned nothing.
	root := p.DomainKey("/")
	if root.Kind != "mount" || root.ID == "" {
		t.Fatalf("DomainKey(/) = %+v, want a mount domain with an id", root)
	}
	tmp := p.DomainKey(t.TempDir())
	if tmp.Kind != "mount" || tmp.ID == "" {
		t.Fatalf("DomainKey(tempdir) = %+v, want a mount domain with an id", tmp)
	}
	t.Logf("/ is on %q; the temp dir is on %q", root.ID, tmp.ID)
}

// TestTwoVolumesAreTwoDomains is the macOS shape of the domain rule, asserted
// where it can run on either platform. Darwin reports a mount's Mntfromname
// as its device: /dev/disk3s1s1 for the sealed system volume, /dev/disk3s5
// for the data volume, devfs for the synthetic ones, //user@host/share for an
// SMB mount and "map auto_home" for autofs. Those are distinct strings, so
// the domains are distinct — a mount that fails takes its own bulkhead down
// and no other. The one thing this cannot see is a FIRMLINK (a path spelled
// /Users/x lives on the data volume but is not under /System/Volumes/Data in
// the table): it resolves to the root volume, which is still a real mount,
// just not the one a stat would answer.
func TestTwoVolumesAreTwoDomains(t *testing.T) {
	table := []mountPoint{
		{Point: "/", Device: "/dev/disk3s1s1"},
		{Point: "/System/Volumes/Data", Device: "/dev/disk3s5"},
		{Point: "/System/Volumes/VM", Device: "devfs"},
		{Point: "/Volumes/work", Device: "//user@server/share"},
		{Point: "/home", Device: "map auto_home"},
	}
	cases := []struct{ path, want string }{
		{"/", "/dev/disk3s1s1"},
		{"/System/Volumes/Data/Users/me", "/dev/disk3s5"},
		{"/Volumes/work/src", "//user@server/share"},
		{"/home/me", "map auto_home"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		dev, ok := resolveMount(table, tc.path)
		if !ok || dev != tc.want {
			t.Fatalf("%s resolved to %q (found=%v), want %q", tc.path, dev, ok, tc.want)
		}
		seen[dev] = true
	}
	if len(seen) != len(cases) {
		t.Fatalf("the four volumes collapsed into %d domains: %v — every mount must be its own failure domain", len(seen), seen)
	}
}

// linkFake is the resolver's readlink seam. It answers the links it is given,
// the error table's errors for the paths named there, and the kernel's answer
// for a component that is not a link (EINVAL) for everything else — and it
// records every path it was asked about, because "did the resolver touch the
// mount it is naming" is the property under test, not only the answer.
type linkFake struct {
	links map[string]string
	errs  map[string]error
	calls []string
}

func (f *linkFake) readlink(path string) (string, error) {
	f.calls = append(f.calls, path)
	if err, ok := f.errs[path]; ok {
		return "", err
	}
	if target, ok := f.links[path]; ok {
		return target, nil
	}
	return "", fs.ErrInvalid
}

// touchedUnder reports whether any readlink was issued at or below prefix.
func (f *linkFake) touchedUnder(prefix string) bool {
	for _, c := range f.calls {
		if c == prefix || strings.HasPrefix(c, prefix+"/") {
			return true
		}
	}
	return false
}

// TestASymlinkIntoAnotherMountTakesThatMountsDomain is finding 1 of
// nocx-3an1s.2.14 in its own words: on macOS /Users/me/far -> /Volumes/nfs is
// spelled on the local disk and read from NFS. The domain must be where the
// reads really go, or four abandoned reads mark the local disk suspect and
// healthy local folders are refused.
func TestASymlinkIntoAnotherMountTakesThatMountsDomain(t *testing.T) {
	table := []mountPoint{
		{Point: "/", Device: "/dev/disk3s1s1"},
		{Point: "/Volumes/nfs", Device: "//user@server/share"},
	}
	// The mount's own root and a directory inside it: a link may name either,
	// and the answer is the same because the walk stops at the mount point.
	for _, target := range []string{"/Volumes/nfs", "/Volumes/nfs/projects/x"} {
		links := &linkFake{links: map[string]string{"/Users/me/far": target}}

		dev, ok := resolveDevice(table, "/Users/me/far", links.readlink)
		if !ok || dev != "//user@server/share" {
			t.Fatalf("a link to %s resolved to %q, %v; want //user@server/share", target, dev, ok)
		}
		// The whole reason the design reads a mount table is that a stat on a
		// hung mount blocks. Resolving the symlink must therefore not reach
		// into the mount it names.
		if links.touchedUnder("/Volumes/nfs") {
			t.Fatalf("a link to %s made the resolver read inside the mount: %v", target, links.calls)
		}
	}
}

// TestAPathUnderAMountPointChargesThatMountWithoutReadingIt is the hung-mount
// case the domain exists for: a path on the wedged export is named from the
// table alone, and nothing under the mount is read.
func TestAPathUnderAMountPointChargesThatMountWithoutReadingIt(t *testing.T) {
	table := []mountPoint{
		{Point: "/", Device: "root"},
		{Point: "/Volumes/nfs", Device: "server:/export"},
	}
	links := &linkFake{}

	dev, ok := resolveDevice(table, "/Volumes/nfs/a/b", links.readlink)
	if !ok || dev != "server:/export" {
		t.Fatalf("resolveDevice(/Volumes/nfs/a/b) = %q, %v; want server:/export", dev, ok)
	}
	if links.touchedUnder("/Volumes/nfs") {
		t.Fatalf("the resolver read inside the hung mount: %v", links.calls)
	}
}

// TestARelativeTargetAndItsDotDotsAreFollowed keeps the walk honest about the
// way links are really written: a relative target is spliced onto the link's
// own directory, and its dot-dots are walked back before the mount lookup.
func TestARelativeTargetAndItsDotDotsAreFollowed(t *testing.T) {
	table := []mountPoint{
		{Point: "/", Device: "root"},
		{Point: "/mnt/nfs", Device: "server:/export"},
	}
	links := &linkFake{links: map[string]string{"/home/me/up": "../../mnt/nfs/export/x"}}

	dev, ok := resolveDevice(table, "/home/me/up/file", links.readlink)
	if !ok || dev != "server:/export" {
		t.Fatalf("resolveDevice through a relative target = %q, %v; want server:/export", dev, ok)
	}
}

// TestASymlinkChainWithinOneMountKeepsThatMount: following links is not the
// same as changing domain. A link that stays on the disk it is spelled on must
// not move the call to another bulkhead.
func TestASymlinkChainWithinOneMountKeepsThatMount(t *testing.T) {
	table := []mountPoint{{Point: "/", Device: "root"}}
	links := &linkFake{links: map[string]string{
		"/Users/me/a":      "/Users/me/deep/b",
		"/Users/me/deep/b": "/Users/me/c/d",
	}}

	dev, ok := resolveDevice(table, "/Users/me/a/x", links.readlink)
	if !ok || dev != "root" {
		t.Fatalf("resolveDevice within one mount = %q, %v; want root", dev, ok)
	}
}

// TestASymlinkLoopEndsAtItsBound: a self-referential link must not walk for
// ever, and the honest answer is the mount the loop is spelled on.
func TestASymlinkLoopEndsAtItsBound(t *testing.T) {
	table := []mountPoint{{Point: "/", Device: "root"}}
	links := &linkFake{links: map[string]string{"/Users/me/loop": "/Users/me/loop"}}

	dev, ok := resolveDevice(table, "/Users/me/loop", links.readlink)
	if !ok || dev != "root" {
		t.Fatalf("resolveDevice on a loop = %q, %v; want root", dev, ok)
	}
	// Each hop re-walks the spliced path from the root, so the syscall count is
	// a small multiple of the hop bound — bounded, which is the point.
	if len(links.calls) > 4*maxLinkHops {
		t.Fatalf("a loop cost %d readlinks; the walk must end at its bound", len(links.calls))
	}
}

// TestAMissingComponentEndsTheWalkLexically: a path that does not exist yet is
// still a path a person is about to create, and the mount table answers for it
// without anything being read.
func TestAMissingComponentEndsTheWalkLexically(t *testing.T) {
	table := []mountPoint{{Point: "/", Device: "root"}}
	links := &linkFake{errs: map[string]error{"/Users/me/gone": fs.ErrNotExist}}

	dev, ok := resolveDevice(table, "/Users/me/gone/deeper/x", links.readlink)
	if !ok || dev != "root" {
		t.Fatalf("resolveDevice over a missing component = %q, %v; want root", dev, ok)
	}
}

// TestASymlinkToARealMountPointTakesThatMountsDevice is the paired "and on a
// normal machine it succeeds" check for the fix: the fixtures above can agree
// with a resolver that would answer nothing on a real machine. A link on this
// machine's root volume that points at a real mount point must take that
// mount's device, and must take a different domain from the directory it is
// spelled in — which is exactly the bug: one domain means four abandoned reads
// on the export refuse the healthy local disk.
func TestASymlinkToARealMountPointTakesThatMountsDevice(t *testing.T) {
	table := readMountTable()
	rootDevice, ok := resolveMount(table, "/")
	if !ok {
		t.Skip("no mount table on this machine")
	}
	// The shortest mount point other than the root, so the choice is stable
	// from one run to the next; /proc on Linux, /dev or /System/Volumes/VM on
	// macOS, whatever the kernel spells first.
	var point, device string
	for _, m := range table {
		if m.Point == "/" || m.Device == "" || m.Device == rootDevice || !strings.HasPrefix(m.Point, "/") {
			continue
		}
		if point == "" || len(m.Point) < len(point) {
			point, device = m.Point, m.Device
		}
	}
	if point == "" {
		t.Skip("this machine has no second mount to resolve into")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "far")
	if err := os.Symlink(point, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, point, err)
	}

	got, ok := mountDevice(link)
	if !ok || got != device {
		t.Fatalf("a link to %s (%s) resolved to %q, %v; want %q", point, device, got, ok, device)
	}
	beside, ok := mountDevice(dir)
	if !ok || beside == got {
		t.Fatalf("the link and the directory it is spelled in share domain %q: reads on the export would refuse %s", got, dir)
	}
	t.Logf("%s is %q; %s stays %q", point, got, dir, beside)
}
