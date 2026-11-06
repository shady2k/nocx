//go:build linux

package local

import "os"

// readMountTable reads the kernel's mount table for this process. It is
// /proc/self/mounts rather than /proc/mounts so a container's own namespace
// is what it names.
func readMountTable() []mountPoint {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	return parseMountText(string(raw))
}
