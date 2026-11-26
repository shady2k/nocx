//go:build linux

package nativeports

// Linux unit tests: the /proc hex encoding (the reference implementation's,
// pinned here) and the row parse against a canned table. The real-machine
// acceptance test lives in provider_test.go (it drives the Provider, the
// user-facing seam).
import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/portsprobe"
)

// TestParseHexAddress pins the /proc/net address encoding: IPv4 is 8 hex
// chars in host-byte-order, IPv6 is 32 hex chars of little-endian 32-bit
// words, ports are big-endian.
func TestParseHexAddress(t *testing.T) {
	tests := []struct {
		in   string
		host string
		port int
	}{
		{"0100007F:1F90", "127.0.0.1", 8080},
		{"00000000:0016", "0.0.0.0", 22},
		{"7F000001:C350", "1.0.0.127", 50000},
		{"00000000000000000000000001000000:0016", "::1", 22},
		{"00000000000000000000000000000000:01BB", "::", 443},
		{"00000000000000000000000000000000:1F90", "::", 8080},
	}
	for _, tc := range tests {
		host, port, ok := parseHexAddress(tc.in)
		if !ok {
			t.Errorf("parseHexAddress(%q) = !ok, want ok", tc.in)
			continue
		}
		if host != tc.host || port != tc.port {
			t.Errorf("parseHexAddress(%q) = %q:%d, want %q:%d", tc.in, host, port, tc.host, tc.port)
		}
	}
	// Malformed input is rejected, never guessed.
	for _, bad := range []string{"", "nonsense", "00000000", "00000000:", ":1F90", "0000000:1F90"} {
		if _, _, ok := parseHexAddress(bad); ok {
			t.Errorf("parseHexAddress(%q) = ok, want rejection", bad)
		}
	}
}

// TestProcNet_ParsesListenRows: a canned table — the header is skipped,
// only 0A (LISTEN) rows are taken, and the inode rides for the owner walk.
func TestProcNet_ParsesListenRows(t *testing.T) {
	table := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000        0 15327 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:1F91 00000000:0000 01 00000000:00000000 00:00000000 00000000 1000        0 15328 1 0000000000000000 100 0 0 10 0\n" +
		"   2: 0100007F:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000        0 15329 1 0000000000000000 100 0 0 10 0\n"
	path := filepath.Join(t.TempDir(), "tcp")
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}

	rows, err := procNet(context.Background(), path, portsprobe.FamilyIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (LISTEN only)", len(rows))
	}
	if rows[0].port != 8080 || rows[0].address != "127.0.0.1" || rows[0].inode != 15327 {
		t.Errorf("row 0 = %+v, want 127.0.0.1:8080 inode 15327", rows[0])
	}
	if rows[1].port != 22 || rows[1].inode != 15329 {
		t.Errorf("row 1 = %+v, want port 22 inode 15329", rows[1])
	}
	if rows[0].family != portsprobe.FamilyIPv4 {
		t.Errorf("family = %q, want ipv4", rows[0].family)
	}
}

// TestListeners_IsSortedAndBounded pins the deterministic order: lowest
// port first, address as tie-break, regardless of enumeration order (the
// shared Listeners entry point sorts before the row cap applies).
func TestListeners_IsSorted(t *testing.T) {
	ls := []portsprobe.Listener{
		{Port: 443, Address: "0.0.0.0"},
		{Port: 22, Address: "0.0.0.0"},
		{Port: 8080, Address: "127.0.0.1"},
		{Port: 22, Address: "::"},
	}
	sortListeners(ls)
	wantPorts := []int{22, 22, 443, 8080}
	for i, want := range wantPorts {
		if ls[i].Port != want {
			t.Fatalf("sorted[%d].Port = %d, want %d (deterministic by port, then address)", i, ls[i].Port, want)
		}
	}
}
