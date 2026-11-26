package portsprobe

// One parser per probe dialect: the five tables the ladder can read, plus
// the local machine's lsof on darwin. Each answers two things at once — the
// rows, and whether the text was that dialect at all — because "I read the
// table and it was empty" and "this is not the table I asked for" are
// different facts and a caller must be able to advance the ladder on the
// second without losing the first. Every parser is exported: the far helper
// keeps its own ladder (nocx-oalo1.13.2) beside this package's, and one
// implementation with two callers is the whole point of the split.

import (
	"regexp"
	"strconv"
	"strings"
)

// splitHostPort splits "host:port" in the forms the probes emit:
// "127.0.0.1:631", "[::1]:631", "127.0.0.53%lo:53" (interface zone),
// "[fe80::1%eth0]:22", "*:22".
func splitHostPort(s string) (host string, port int, ok bool) {
	if strings.HasPrefix(s, "[") {
		close := strings.IndexByte(s, ']')
		if close < 0 {
			return "", 0, false
		}
		host = s[1:close]
		rest := s[close+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, false
		}
		p, err := strconv.Atoi(rest[1:])
		if err != nil || p < 0 || p > 65535 {
			return "", 0, false
		}
		return host, p, true
	}
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil || p < 0 || p > 65535 {
		return "", 0, false
	}
	return s[:i], p, true
}

// familyOf classifies a bind address by shape. "*" is the IPv4 wildcard lsof
// prints for 0.0.0.0; a bracketed or colon-bearing address is IPv6.
func familyOf(host string) AddressFamily {
	if strings.Contains(host, ":") || strings.HasPrefix(host, "[") {
		return FamilyIPv6
	}
	return FamilyIPv4
}

func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\r\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ssUsersRe matches the process column of `ss -p` output:
// users:(("systemd-resolve",pid=714,fd=18)).
var ssUsersRe = regexp.MustCompile(`users:\(\(\"([^\"]*)\",pid=(\d+)`)

// ParseSS parses `LC_ALL=C ss -H -lntp` output: whitespace-separated columns
// State Recv-Q Send-Q Local Peer [Process]. A row without a users: column is
// a socket whose owner the probe was not allowed to see — permission-denied
// evidence, never "unowned" (spec §5).
func ParseSS(body []byte) ([]Listener, bool) {
	var out []Listener
	for _, line := range splitLines(body) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] != "LISTEN" {
			return nil, false // not ss -lntp shape
		}
		if len(fields) < 5 {
			return nil, false
		}
		host, port, ok := splitHostPort(fields[3])
		if !ok {
			return nil, false
		}
		ev := Process{Evidence: EvidencePermissionDenied}
		if len(fields) >= 6 {
			if m := ssUsersRe.FindStringSubmatch(fields[5]); m != nil {
				if pid, err := strconv.Atoi(m[2]); err == nil {
					ev = Process{Evidence: EvidenceKnown, Name: m[1], PID: pid}
				}
			}
		}
		out = append(out, Listener{Family: familyOf(host), Address: host, Port: port, Process: ev})
	}
	return out, true
}

// ParseNetstat parses net-tools `netstat -lntp` output. Rows are tcp/tcp6
// lines with State LISTEN and a PID/Program name column; a bare "-" in that
// column is a socket whose owner was not visible — permission-denied
// evidence. Anything else (BSD netstat, usage text) is not this dialect and
// reports unsupported, so the ladder advances.
func ParseNetstat(body []byte) ([]Listener, bool) {
	var out []Listener
	for _, line := range splitLines(body) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Proto" || fields[0] == "Active" {
			continue // header lines
		}
		if fields[0] != "tcp" && fields[0] != "tcp6" {
			return nil, false
		}
		if len(fields) < 6 || fields[5] != "LISTEN" {
			return nil, false
		}
		host, port, ok := splitHostPort(fields[3])
		if !ok {
			return nil, false
		}
		ev := Process{Evidence: EvidencePermissionDenied}
		if len(fields) >= 7 && fields[6] != "-" {
			if i := strings.IndexByte(fields[6], '/'); i > 0 {
				if pid, err := strconv.Atoi(fields[6][:i]); err == nil {
					ev = Process{Evidence: EvidenceKnown, Name: fields[6][i+1:], PID: pid}
				}
			}
		}
		out = append(out, Listener{Family: familyOf(host), Address: host, Port: port, Process: ev})
	}
	return out, true
}

// ParseBusyboxNetstat parses `netstat -ltn` output on a host whose netstat
// rejected -p (busybox, detected explicitly): the same table without the PID
// column, so process evidence is unsupported for every row (spec §5:
// three-valued, never an empty string).
func ParseBusyboxNetstat(body []byte) ([]Listener, bool) {
	var out []Listener
	for _, line := range splitLines(body) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Proto" || fields[0] == "Active" {
			continue
		}
		if fields[0] != "tcp" && fields[0] != "tcp6" {
			return nil, false
		}
		if len(fields) < 6 || fields[5] != "LISTEN" {
			return nil, false
		}
		host, port, ok := splitHostPort(fields[3])
		if !ok {
			return nil, false
		}
		out = append(out, Listener{
			Family:  familyOf(host),
			Address: host,
			Port:    port,
			Process: Process{Evidence: EvidenceUnsupported},
		})
	}
	return out, true
}

// ParseLsof parses `lsof -nP -iTCP -sTCP:LISTEN` output: columns COMMAND PID
// USER FD TYPE DEVICE SIZE/OFF NODE NAME, where NAME ends in " (LISTEN)" and
// carries the bind address ("*:22", "127.0.0.1:631", "[::1]:631"). Rows lsof
// prints always carry process evidence. NOTE: as non-root lsof only lists
// sockets it may read, so other users' listeners can be absent from the rows
// entirely — a row-visibility limit, not per-row evidence.
//
// `ok` is false when the body is not this dialect at all. For the remote
// ladder that means "advance to the next rung"; for a caller whose only tool
// is lsof it means the table could not be read, which is never the same
// answer as an empty one.
//
// It has two callers and must only ever have one implementation: the
// ladder's lsof rung, and internal/nativeports on darwin, where lsof IS the
// local read. That package briefly kept a second
// copy, which read the bind address out of the last whitespace-separated
// field — lsof's last field is the state, so it rejected every row and every
// Mac reported "no listening ports" (nocx-ou3e).
func ParseLsof(body []byte) ([]Listener, bool) {
	var out []Listener
	for _, line := range splitLines(body) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "COMMAND" {
			continue // header
		}
		if len(fields) < 9 {
			return nil, false
		}
		name := strings.Join(fields[8:], " ")
		addr, found := strings.CutSuffix(name, " (LISTEN)")
		if !found {
			return nil, false
		}
		// lsof's NAME column carries the protocol prefix ("TCP *:22").
		addr = strings.TrimPrefix(addr, "TCP ")
		host, port, ok := splitHostPort(addr)
		if !ok {
			return nil, false
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, false
		}
		out = append(out, Listener{
			Family:  familyOf(host),
			Address: host,
			Port:    port,
			Process: Process{Evidence: EvidenceKnown, Name: fields[0], PID: pid},
		})
	}
	return out, true
}

// ParseSockstat parses FreeBSD `sockstat -4 -l` output: columns USER COMMAND
// PID FD PROTO LOCAL ADDRESS FOREIGN ADDRESS. LOCAL ADDRESS is host:port;
// wildcards print as "*.22". Rows carry process evidence.
func ParseSockstat(body []byte) ([]Listener, bool) {
	var out []Listener
	for _, line := range splitLines(body) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "USER" || strings.HasPrefix(fields[0], "Active") {
			continue
		}
		if len(fields) < 6 {
			return nil, false
		}
		proto := fields[4]
		if proto != "tcp4" && proto != "tcp6" {
			return nil, false
		}
		host, port, ok := splitHostPort(fields[5])
		if !ok {
			// FreeBSD prints wildcards as "*.22".
			if strings.HasPrefix(fields[5], "*.") {
				p, err := strconv.Atoi(fields[5][2:])
				if err != nil {
					return nil, false
				}
				host, port = "*", p
			} else {
				return nil, false
			}
		}
		pid, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, false
		}
		fam := familyOf(host)
		if proto == "tcp6" {
			fam = FamilyIPv6
		}
		out = append(out, Listener{
			Family:  fam,
			Address: host,
			Port:    port,
			Process: Process{Evidence: EvidenceKnown, Name: fields[1], PID: pid},
		})
	}
	return out, true
}
