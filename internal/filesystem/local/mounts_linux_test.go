//go:build linux

package local

import "testing"

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
