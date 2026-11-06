//go:build darwin

package local

import "testing"

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
