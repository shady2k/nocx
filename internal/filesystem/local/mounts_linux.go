//go:build linux

package local

import (
	"os"
	"strings"
)

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

// parseMountText splits kernel mount-table text into rows: the first field is
// the device, the second the mount point, and both are octal-escaped (a space
// arrives as \040).
func parseMountText(text string) []mountPoint {
	var out []mountPoint
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out = append(out, mountPoint{
			Device: unescapeMountField(fields[0]),
			Point:  unescapeMountField(fields[1]),
		})
	}
	return out
}

// unescapeMountField decodes the octal escapes the kernel writes for
// whitespace and backslashes in a mount-table field.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '7' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
