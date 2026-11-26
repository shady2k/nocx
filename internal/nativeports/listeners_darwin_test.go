//go:build darwin

package nativeports

// Darwin unit tests: the lsof row parse against a canned table, driven through
// the exported parser the remote ladder also uses. The real-machine acceptance
// test lives in provider_test.go (it drives the Provider, the user-facing
// seam).
//
// The table below is REAL `/usr/sbin/lsof -nP -iTCP -sTCP:LISTEN` output,
// pasted unedited, and that is the whole point of the file. This package
// shipped with a Linux parser test and no darwin one, over a SECOND copy of
// the lsof parse that read the bind address out of the LAST whitespace-
// separated field. lsof's last field is the connection state — "TCP *:7000
// (LISTEN)" — so splitHostPort was handed "(LISTEN)", rejected it, and dropped
// every row: an empty table on every Mac, which the product renders as "this
// machine has no listening ports" (nocx-ou3e). The copy is gone; this file
// keeps the darwin dialect under test where it is consumed.
import (
	"testing"

	"github.com/shady2k/nocx/internal/portsprobe"
)

// Real output, including the header line and the trailing state column.
const lsofFixture = `COMMAND     PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
ControlCe   564 shady    8u  IPv4 0xe7cce2764a2a4baa      0t0  TCP *:7000 (LISTEN)
ControlCe   564 shady    9u  IPv6 0x317d1f8fdb4ac985      0t0  TCP *:7000 (LISTEN)
rapportd    601 shady    9u  IPv4 0x2ac9e2a1b8b1c3d4      0t0  TCP 127.0.0.1:52698 (LISTEN)
node      12345 shady   23u  IPv6 0x9f1a2b3c4d5e6f70      0t0  TCP [::1]:5173 (LISTEN)
`

// TestDarwinLsofDialect_EveryRowYieldsItsAddress is the regression the empty
// table needed: every row here ends in "(LISTEN)", and every row must still
// yield its address, port and process.
func TestDarwinLsofDialect_EveryRowYieldsItsAddress(t *testing.T) {
	got, ok := portsprobe.ParseLsof([]byte(lsofFixture))
	if !ok {
		t.Fatal("ParseLsof rejected real macOS lsof output")
	}
	if len(got) != 4 {
		t.Fatalf("parsed %d rows, want 4 — every row carries a state suffix, and dropping them is the defect this test exists for: %+v", len(got), got)
	}

	want := []struct {
		addr string
		port int
		name string
		pid  int
	}{
		{"*", 7000, "ControlCe", 564},
		{"*", 7000, "ControlCe", 564},
		{"127.0.0.1", 52698, "rapportd", 601},
		{"::1", 5173, "node", 12345},
	}
	for i, w := range want {
		g := got[i]
		if g.Address != w.addr || g.Port != w.port {
			t.Errorf("row %d = %s:%d, want %s:%d", i, g.Address, g.Port, w.addr, w.port)
		}
		if g.Process.Name != w.name || g.Process.PID != w.pid {
			t.Errorf("row %d process = %s/%d, want %s/%d", i, g.Process.Name, g.Process.PID, w.name, w.pid)
		}
		if g.Process.Evidence != portsprobe.EvidenceKnown {
			t.Errorf("row %d evidence = %q, want %q — an lsof row always names its process", i, g.Process.Evidence, portsprobe.EvidenceKnown)
		}
	}
}

// TestDarwinLsofDialect_FamilyComesFromTheAddress pins what the shared parser
// actually decides, including where it cannot decide.
//
// A literal address carries its own family and is classified correctly. The
// WILDCARD does not: lsof prints "*:7000" for an IPv6 bind exactly as it does
// for an IPv4 one, and only the TYPE column tells them apart — which the
// parser does not read. So both rows of a dual-stack listener come back ipv4
// and are otherwise identical.
//
// Pinned rather than fixed here: the classification is the remote ladder's
// too (internal/portsprobe/parse.go familyOf), a fix belongs in that one owner,
// and it is cosmetic — the panel shows a family badge and a duplicate row, not
// a wrong port. Fixing it in this package alone would re-fork the parse that
// this file exists because someone forked.
func TestDarwinLsofDialect_FamilyComesFromTheAddress(t *testing.T) {
	got, ok := portsprobe.ParseLsof([]byte(lsofFixture))
	if !ok {
		t.Fatal("ParseLsof rejected real macOS lsof output")
	}
	if got[2].Family != portsprobe.FamilyIPv4 {
		t.Errorf("127.0.0.1 classified %q, want ipv4", got[2].Family)
	}
	if got[3].Family != portsprobe.FamilyIPv6 {
		t.Errorf("::1 classified %q, want ipv6", got[3].Family)
	}
	if got[0].Family != portsprobe.FamilyIPv4 || got[1].Family != portsprobe.FamilyIPv4 {
		t.Errorf("wildcard rows classified %q/%q; today both read ipv4 because the address is bare \"*\" — if this now fails, the family fix landed and this test should assert it instead", got[0].Family, got[1].Family)
	}
}

// TestDarwinLsofDialect_RejectsWhatItCannotRead: a body that is not this
// dialect is refused outright. For the darwin provider that becomes a
// could-not-determine error, never an empty list — a table the read failed on
// must not render as "this machine has no listening ports".
func TestDarwinLsofDialect_RejectsWhatItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"usage text":  "lsof: illegal option -- q\nusage: lsof [-?ab...]\n",
		"missing pid": "COMMAND     PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME\nnotapid     abc shady    8u  IPv4 0xe7cc      0t0  TCP *:7000 (LISTEN)\n",
		"no address":  "COMMAND     PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME\nnoport      602 shady    9u  IPv4 0x2ac9      0t0  TCP 127.0.0.1 (LISTEN)\n",
	} {
		if _, ok := portsprobe.ParseLsof([]byte(body)); ok {
			t.Errorf("%s: accepted, want rejection — a guessed table is worse than an admitted failure", name)
		}
	}
}
