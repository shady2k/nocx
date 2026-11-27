package portsprobe

import (
	"testing"
)

// ssMixedFixture is REAL `LC_ALL=C ss -H -lntp` output measured on a Linux
// host as non-root: 9 listeners, 3 with visible processes, 6 bare (owned by
// another user — permission-denied evidence). Note the interface zone on
// 127.0.0.53%lo.
const ssMixedFixture = `LISTEN 0      4096   127.0.0.53%lo:53    0.0.0.0:*
LISTEN 0      4096      127.0.0.54:53    0.0.0.0:*
LISTEN 0      511          0.0.0.0:6768  0.0.0.0:* users:(("orca-ide",pid=871,fd=81))
LISTEN 0      4096         0.0.0.0:5355  0.0.0.0:*
LISTEN 0      511        127.0.0.1:40721 0.0.0.0:* users:(("MainThread",pid=1184,fd=22))
LISTEN 0      511        127.0.0.1:40461 0.0.0.0:* users:(("orca-ide",pid=871,fd=80))
LISTEN 0      128          0.0.0.0:22    0.0.0.0:*
LISTEN 0      4096            [::]:5355     [::]:*
LISTEN 0      128             [::]:22       [::]:*`

func TestParseSS_MixedEvidence(t *testing.T) {
	listeners, ok := ParseSS([]byte(ssMixedFixture))
	if !ok {
		t.Fatal("ParseSS rejected the real ss fixture")
	}
	if len(listeners) != 9 {
		t.Fatalf("listeners = %d, want 9", len(listeners))
	}

	// Three known (orca-ide ×2, MainThread), six permission-denied — the
	// three-valued contract: a bare row is permission-denied, never "".
	known, denied := 0, 0
	for _, l := range listeners {
		switch l.Process.Evidence {
		case EvidenceKnown:
			known++
			if l.Process.Name == "" || l.Process.PID == 0 {
				t.Errorf("known listener %d has empty name/pid", l.Port)
			}
		case EvidencePermissionDenied:
			denied++
		default:
			t.Errorf("listener %d evidence = %q, want known or permission-denied", l.Port, l.Process.Evidence)
		}
	}
	if known != 3 || denied != 6 {
		t.Errorf("known = %d (want 3), permission-denied = %d (want 6)", known, denied)
	}

	// Ports and families, including the zone-qualified address and the
	// dual-stack 5355/22 (one v4, one v6 row each).
	fams := map[int]map[AddressFamily]bool{}
	for _, l := range listeners {
		if fams[l.Port] == nil {
			fams[l.Port] = map[AddressFamily]bool{}
		}
		fams[l.Port][l.Family] = true
	}
	if !fams[53][FamilyIPv4] {
		t.Error("port 53 missing an ipv4 row")
	}
	if !fams[6768][FamilyIPv4] {
		t.Error("port 6768 missing an ipv4 row")
	}
	if !fams[5355][FamilyIPv4] || !fams[5355][FamilyIPv6] {
		t.Errorf("port 5355 families = %v, want both ipv4 and ipv6", fams[5355])
	}
	if !fams[22][FamilyIPv4] || !fams[22][FamilyIPv6] {
		t.Errorf("port 22 families = %v, want both ipv4 and ipv6", fams[22])
	}
}

func TestParseSS_ZoneQualifiedAddress(t *testing.T) {
	// 127.0.0.53%lo:53 — the %lo interface zone must not break the port
	// split.
	listeners, ok := ParseSS([]byte("LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*\n"))
	if !ok {
		t.Fatal("ParseSS rejected a zone-qualified row")
	}
	if len(listeners) != 1 {
		t.Fatalf("listeners = %d, want 1", len(listeners))
	}
	if listeners[0].Port != 53 || listeners[0].Address != "127.0.0.53%lo" {
		t.Errorf("got %+v, want port 53 on 127.0.0.53%%lo", listeners[0])
	}
}

func TestParseSS_EmptyTable(t *testing.T) {
	listeners, ok := ParseSS([]byte(""))
	if !ok {
		t.Fatal("ParseSS rejected an empty body")
	}
	if len(listeners) != 0 {
		t.Errorf("listeners = %d, want 0", len(listeners))
	}
}

func TestParseSS_NotSSShape(t *testing.T) {
	// Arbitrary text must report unsupported, not parse to zero rows.
	if _, ok := ParseSS([]byte("Welcome to example.com\nls /etc/passwd\n")); ok {
		t.Error("ParseSS accepted arbitrary output")
	}
}

// netstatFixture is the net-tools `netstat -lntp` table format (constructed
// fixture — netstat is not installed on the measurement host).
const netstatFixture = `Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 127.0.0.53:53           0.0.0.0:*               LISTEN      714/systemd-resolve
tcp        0      0 0.0.0.0:5355            0.0.0.0:*               LISTEN      -
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      -
tcp6       0      0 [::]:22                 [::]:*                  LISTEN      -`

func TestParseNetstat(t *testing.T) {
	listeners, ok := ParseNetstat([]byte(netstatFixture))
	if !ok {
		t.Fatal("ParseNetstat rejected the net-tools fixture")
	}
	if len(listeners) != 4 {
		t.Fatalf("listeners = %d, want 4", len(listeners))
	}
	if listeners[0].Process.Evidence != EvidenceKnown || listeners[0].Process.Name != "systemd-resolve" || listeners[0].Process.PID != 714 {
		t.Errorf("row 0 process = %+v, want known systemd-resolve/714", listeners[0].Process)
	}
	for _, l := range listeners[1:] {
		if l.Process.Evidence != EvidencePermissionDenied {
			t.Errorf("row %d evidence = %q, want permission-denied (bare '-')", l.Port, l.Process.Evidence)
		}
	}
	// The tcp6 row's family comes from the address shape.
	if listeners[3].Family != FamilyIPv6 {
		t.Errorf("tcp6 row family = %v, want ipv6", listeners[3].Family)
	}
}

func TestParseNetstat_BSDShapeIsUnsupported(t *testing.T) {
	// A BSD netstat table (no PID column, different State values) must not
	// be half-parsed as the Linux dialect.
	bsd := `Name  Mtu   Network       Address            Ipkts Ierrs Idrop
tcp4       0      0  *.22                 *.*                LISTEN`
	if _, ok := ParseNetstat([]byte(bsd)); ok {
		t.Error("ParseNetstat accepted a BSD-shaped table")
	}
}

// busyboxFixture is `netstat -ltn` on a busybox host: the same table without
// the PID/Program name column. Constructed fixture.
const busyboxFixture = `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN
tcp        0      0 127.0.0.1:3000          0.0.0.0:*               LISTEN
tcp6       0      0 [::]:22                 [::]:*                  LISTEN`

func TestParseBusyboxNetstat_EvidenceUnsupported(t *testing.T) {
	listeners, ok := ParseBusyboxNetstat([]byte(busyboxFixture))
	if !ok {
		t.Fatal("ParseBusyboxNetstat rejected the busybox fixture")
	}
	if len(listeners) != 3 {
		t.Fatalf("listeners = %d, want 3", len(listeners))
	}
	for _, l := range listeners {
		if l.Process.Evidence != EvidenceUnsupported {
			t.Errorf("listener %d evidence = %q, want unsupported (no -p column)", l.Port, l.Process.Evidence)
		}
	}
	if listeners[1].Port != 3000 || listeners[1].Address != "127.0.0.1" {
		t.Errorf("listener 1 = %+v, want 127.0.0.1:3000", listeners[1])
	}
}

// lsofFixture is the `lsof -nP -iTCP -sTCP:LISTEN` table format.
// Constructed fixture (lsof not installed on the measurement host).
const lsofFixture = `COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
sshd      1234 root    3u  IPv4  12345      0t0 12345 TCP *:22 (LISTEN)
node      4321 dev     11u  IPv6  54321      0t0 54321 TCP [::1]:3000 (LISTEN)`

func TestParseLsof(t *testing.T) {
	listeners, ok := ParseLsof([]byte(lsofFixture))
	if !ok {
		t.Fatal("ParseLsof rejected the fixture")
	}
	if len(listeners) != 2 {
		t.Fatalf("listeners = %d, want 2", len(listeners))
	}
	if listeners[0].Process.Evidence != EvidenceKnown || listeners[0].Process.Name != "sshd" || listeners[0].Process.PID != 1234 {
		t.Errorf("row 0 process = %+v, want known sshd/1234", listeners[0].Process)
	}
	if listeners[0].Address != "*" || listeners[0].Port != 22 || listeners[0].Family != FamilyIPv4 {
		t.Errorf("row 0 = %+v, want *:22 ipv4", listeners[0])
	}
	if listeners[1].Port != 3000 || listeners[1].Family != FamilyIPv6 || listeners[1].Process.Name != "node" {
		t.Errorf("row 1 = %+v, want [::1]:3000 ipv6 node", listeners[1])
	}
}

// sockstatFixture is the FreeBSD `sockstat -4 -l` table format. Constructed
// fixture — cannot be measured on Linux.
const sockstatFixture = `USER     COMMAND    PID   FD PROTO  LOCAL ADDRESS        FOREIGN ADDRESS
root     sshd       1234  3   tcp4   0.0.0.0:22           *:*
root     sshd       1235  4   tcp6   [::]:22              *:*`

func TestParseSockstat(t *testing.T) {
	listeners, ok := ParseSockstat([]byte(sockstatFixture))
	if !ok {
		t.Fatal("ParseSockstat rejected the fixture")
	}
	if len(listeners) != 2 {
		t.Fatalf("listeners = %d, want 2", len(listeners))
	}
	if listeners[0].Port != 22 || listeners[0].Family != FamilyIPv4 || listeners[0].Process.PID != 1234 {
		t.Errorf("row 0 = %+v, want 0.0.0.0:22 ipv4 pid 1234", listeners[0])
	}
	if listeners[1].Family != FamilyIPv6 {
		t.Errorf("row 1 family = %v, want ipv6 (tcp6)", listeners[1].Family)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"127.0.0.1:631", "127.0.0.1", 631, true},
		{"127.0.0.53%lo:53", "127.0.0.53%lo", 53, true},
		{"[::1]:631", "::1", 631, true},
		{"[fe80::1%eth0]:22", "fe80::1%eth0", 22, true},
		{"*:22", "*", 22, true},
		{"0.0.0.0:*", "", 0, false}, // wildcard peer — not a listen port
		{"host", "", 0, false},      // no port
		{":22", "", 0, false},       // empty host
		{"[::1]631", "", 0, false},  // missing colon
		{"127.0.0.1:notaport", "", 0, false},
	}
	for _, c := range cases {
		host, port, ok := splitHostPort(c.in)
		if ok != c.ok || host != c.host || port != c.port {
			t.Errorf("splitHostPort(%q) = (%q, %d, %v), want (%q, %d, %v)",
				c.in, host, port, ok, c.host, c.port, c.ok)
		}
	}
}
