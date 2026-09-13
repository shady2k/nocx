package discovery

import (
	"bytes"
	"strings"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// sentinel is the fixed framing marker every port probe prints first and last
// on stdout. A sample without it is rejected WHOLE: a forced command, a login
// banner or a policy wrapper can prepend text, and we never scan arbitrary
// stdout for plausible-looking port numbers (spec §3.1).
//
// It is remoteprobe's own constant, not a copy: the SCRIPT that prints it and
// the parser that looks for it are on opposite sides of the helper wire now,
// and two copies of a marker are the drift this package's framing rule exists
// to make impossible.
const sentinel = remoteprobe.Sentinel

var sentinelBytes = []byte(sentinel)

// step is one probe on the ladder: which rung it is, the parser for its
// dialect, and the exit status that means "ran fine, nothing matched" (lsof
// exits 1 with no matches — a valid empty sample, not a failure).
//
// There is no command here, and its absence is the point: what runs is the
// helper's, named by step.name, and the text lives in internal/remoteprobe
// beside the scripts that this package's parsers read.
type step struct {
	name        ProbeName
	parse       func(body []byte) ([]Listener, bool)
	noMatchExit int
}

// probeLadder is the capability-selection order (spec §5): ss → netstat
// (flags verified by the run itself, never hopeful — a -lntp rejection
// caches netstat and the next step verifies busybox's -ltn) → busybox
// netstat (detected explicitly; -p may be unavailable, so process evidence
// is unsupported) → lsof → sockstat → unavailable.
var probeLadder = []*step{
	{name: ProbeSS, parse: parseSS},
	{name: ProbeNetstat, parse: parseNetstat},
	{name: ProbeBusyboxNetstat, parse: parseBusyboxNetstat},
	{name: ProbeLsof, parse: ParseLsof, noMatchExit: 1},
	{name: ProbeSockstat, parse: parseSockstat},
}

// ladderIndex returns the ladder position of the named probe, or 0 when
// unknown (an unknown name means no selection — start at the top).
func ladderIndex(name ProbeName) int {
	for i, st := range probeLadder {
		if st.name == name {
			return i
		}
	}
	return 0
}

// splitFrame validates the sentinel framing of an exec's stdout and returns
// the body between the leading and trailing sentinel lines.
//
// leading is false when the output does not start with the sentinel — a
// framing violation: the exec did not run our probe, so the whole sample is
// rejected. trailing is false when the body was cut short — the output bound
// was hit or the remote died mid-write — an incomplete table that must not
// surface as "no ports".
func splitFrame(out []byte) (body []byte, leading, trailing bool) {
	trimmed := bytes.TrimSuffix(out, []byte("\n"))
	if len(trimmed) == 0 {
		return nil, false, false
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	if !bytes.Equal(lines[0], sentinelBytes) {
		return nil, false, false
	}
	if !bytes.Equal(lines[len(lines)-1], sentinelBytes) {
		return nil, true, false
	}
	return bytes.Join(lines[1:len(lines)-1], []byte("\n")), true, true
}

// notFoundOnStderr reports whether stderr says the tool was not found — the
// shell's "sh: ss: not found" / "command not found" — independent of the
// exit status, for shells that report it with a non-127 status.
func notFoundOnStderr(stderr []byte) bool {
	low := strings.ToLower(string(stderr))
	return strings.Contains(low, "not found") || strings.Contains(low, "no such file")
}

// stderrExcerpt bounds the stderr carried on a Sample for diagnostics: the
// design shows truncated stderr, never an uncontrolled dump of remote output
// (spec §6).
const stderrExcerptCap = 2 << 10

func stderrExcerpt(b []byte) string {
	if len(b) > stderrExcerptCap {
		return string(b[:stderrExcerptCap]) + " [truncated]"
	}
	return string(b)
}
