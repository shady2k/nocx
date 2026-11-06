package local

import "testing"

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
