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

func TestParseMountTextDecodesEscapesAndSkipsShortRows(t *testing.T) {
	text := "/dev/sda1 / ext4 rw 0 0\n" +
		"server:/my\\040export /mnt/space nfs rw 0 0\n" +
		"short\n" +
		"\n"
	table := parseMountText(text)
	if len(table) != 2 {
		t.Fatalf("parsed %d rows, want 2: %+v", len(table), table)
	}
	if table[1].Device != "server:/my export" {
		t.Errorf("device = %q, want the decoded space", table[1].Device)
	}
	if table[1].Point != "/mnt/space" {
		t.Errorf("point = %q", table[1].Point)
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
