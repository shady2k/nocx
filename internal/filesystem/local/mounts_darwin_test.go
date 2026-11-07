//go:build darwin

package local

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheRealMountTableNamesADeviceForEveryMount is the paired "and on a
// normal machine it succeeds" check for macOS's reader: Getfsstat must answer
// a device (Mntfromname) for every mount, and a machine with more than one
// mount must report more than one device. A reader that filled every row from
// one field, or left the device empty, would make the whole machine one
// failure domain and every other test in the package would still pass — the
// resolver tests use fixture tables.
func TestTheRealMountTableNamesADeviceForEveryMount(t *testing.T) {
	table := readMountTable()
	if len(table) == 0 {
		t.Fatal("Getfsstat answered no mounts; a Mac always has at least its root volume")
	}
	devices := map[string]bool{}
	for _, m := range table {
		if m.Point == "" || m.Device == "" {
			t.Errorf("a mount row is incomplete: %+v", m)
			continue
		}
		devices[m.Device] = true
	}
	if len(devices) < 2 {
		t.Fatalf("%d mounts reported %d device(s) (%v): the machine's mounts collapsed into one failure domain", len(table), len(devices), devices)
	}
	dev, ok := mountDevice("/")
	if !ok || dev == "" {
		t.Fatalf("mountDevice(/) = %q, found=%v; the root volume must name a device", dev, ok)
	}
	t.Logf("/ is on %q; %d mounts, %d devices", dev, len(table), len(devices))
}

// TestASymlinkIntoADarwinMountTakesThatMountsDomain is the macOS half of
// finding 1 (nocx-3an1s.2.14), written against the reader this platform
// actually has. The case is /Users/me/far -> /Volumes/<export>: spelled on the
// local disk, read from the export. Three things only a Mac can check:
//
//   - Getfsstat's Mntonname is the spelling the walk matches a mount point by,
//     and Mntfromname is the device the answer must carry. A walk built on a
//     different spelling would answer nothing here and every fixture would
//     still pass.
//   - /dev is devfs on every Mac, and it is a mount point, so a link to it
//     takes devfs as its domain and nothing inside it is read.
//   - the answer differs from the root volume's, which is the whole defect: one
//     shared domain means reads on the export refuse the healthy local disk.
func TestASymlinkIntoADarwinMountTakesThatMountsDomain(t *testing.T) {
	table := readMountTable()
	rootDevice, ok := resolveMount(table, "/")
	if !ok {
		t.Fatal("Getfsstat named no root volume; a Mac always has one")
	}
	var devfs string
	for _, m := range table {
		if m.Point == "/dev" {
			devfs = m.Device
		}
	}
	if devfs == "" {
		t.Skip("/dev is not a mount on this machine")
	}
	if devfs == rootDevice {
		t.Fatalf("/dev and / are both %q; the mount table is not naming devices", devfs)
	}

	link := filepath.Join(t.TempDir(), "volumes")
	if err := os.Symlink("/dev", link); err != nil {
		t.Fatalf("symlink -> /dev: %v", err)
	}
	got, ok := mountDevice(link)
	if !ok || got != devfs {
		t.Fatalf("a link to /dev resolved to %q, %v; want %q", got, ok, devfs)
	}
	if got == rootDevice {
		t.Fatalf("a link to /dev took the root volume's domain %q: reads on devfs would refuse the local disk", got)
	}
	t.Logf("/ is %q, /dev is %q, and a link across them takes %q", rootDevice, devfs, got)
}
