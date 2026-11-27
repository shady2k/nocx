package main

// The -ports-tools switch: a fixture directory of port-probe binaries the
// session's PATH finds FIRST, so the far helper's ladder meets a host whose
// port tool is a fact this run chose (nocx-oalo1.13.4, design §5.10 scenario 8).
//
// WHY THE SESSION'S PATH. The ladder resolves a rung on the DAEMON's PATH
// (internal/portsprobe/exec.go), and the far helper is started from a command
// this fixture runs for the SSH session — so the environment this function
// shapes is the helper's own, without the fixture reaching into that process.
//
// TWO MODES, and they are the two halves of one contract:
//
//	fake    an `ss` that prints ONE fixed listener. The serving case: Ports
//	        shows the same row for a saved and a hand-typed host.
//	absent  `ss`, `netstat`, `lsof` and `sockstat` that exit 127 with
//	        "not found" on stderr, which is the ladder's own definition of a
//	        host with no port tool at all (spec §5). The typed `unavailable`
//	        outcome, while Git and Files keep working — they do not use it.
//
// EVERY SCRIPT RECORDS ITS INVOCATION, into the file `calls` in the same
// directory. That file is the fixture's own account of whether the ladder ran:
// a raw host must leave it absent, and "no ports exec" is otherwise an
// assertion about the absence of something this side cannot observe.
//
// The directory is the fixture's own, created at startup and announced as
// PORTSTOOLS= before READY — a caller reads `calls` while the run is live, which
// is the only time it means anything. IT IS THE CALLER THAT REMOVES IT
// (e2e/sshd-fixture.ts's stopSshd): every spec ends this fixture with a signal,
// and a process killed by one runs no deferred function, so a directory only
// this side could remove outlived every run. The defer below is the orderly
// exit's half, which is why it stays.

import (
	"fmt"
	"os"
	"path/filepath"
)

// portsToolsModes is the closed set -ports-tools accepts, so a typo is a
// startup failure rather than a run that quietly changes nothing.
var portsToolsModes = map[string][]string{
	// One fixed listener, on a port no other part of the suite dials: the row
	// the serving case looks for. The process evidence is the shape `ss -p`
	// prints, so the panel's "known owner" path is the one exercised.
	"fake": {"ss"},
	// The ladder's whole set. `netstat` is absent in this mode, which the
	// ladder takes as "busybox netstat too" — its own documented rule, and the
	// reason busybox itself is not faked here.
	"absent": {"ss", "netstat", "lsof", "sockstat"},
}

// portsFixturePort is the port the fake `ss` reports. Fixed on purpose: a
// random one would make the spec assert on a string it had to carry through
// three processes, and a fake listener needs no free port — nothing binds it.
const portsFixturePort = "6768"

// portsToolsDir is where the -ports-tools switch put its binaries, or "" when
// the switch is off. sessionEnv reads it, which is what puts the directory on
// the session's PATH.
var portsToolsDir string

// installPortsTools writes the fixture binaries for one mode and returns the
// directory it made. An unknown mode is an error naming the closed set.
func installPortsTools(mode string) (string, error) {
	names, ok := portsToolsModes[mode]
	if !ok {
		return "", fmt.Errorf("-ports-tools=%q: want fake or absent", mode)
	}
	dir, err := os.MkdirTemp("", "nocx-e2e-ports-tools-")
	if err != nil {
		return "", fmt.Errorf("-ports-tools: %w", err)
	}
	for _, name := range names {
		body := absentToolScript(name)
		if mode == "fake" && name == "ss" {
			body = fakeSSScript()
		}
		path := filepath.Join(dir, name)
		//nolint:gosec // a fixture script under a directory this process just made.
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("-ports-tools: write %s: %w", name, err)
		}
	}
	return dir, nil
}

// recordLine is the shell the scripts use to append their own invocation to
// `calls`. The directory is the script's own, resolved at run time so the
// fixture needs no path baked into the script text.
func recordInvocation(program string) string {
	return `printf '%s %s\n' "` + program + `" "$*" >> "$(dirname "$0")/calls"` + "\n"
}

// fakeSSScript prints the one listener `ss -H -lntp` would print for it,
// preceded by the record of the call.
//
// The row is the real dialect's, field for field: `LISTEN`, the queues, the
// local address with its port, the peer, and `-p`'s users:(("name",pid=N,fd=N))
// — which is what makes the parsed listener a KNOWN owner rather than a
// permission-denied one.
func fakeSSScript() string {
	return "#!/bin/sh\n" + recordInvocation("ss") +
		`printf 'LISTEN 0 128 0.0.0.0:` + portsFixturePort +
		` 0.0.0.0:* users:(("node",pid=4242,fd=21))\n'` + "\n"
}

// absentToolScript is a tool that exists and cannot run: exit 127 with
// "not found" on stderr, which the ladder classifies as ABSENT rather than
// unsupported. A program missing from PATH entirely says the same thing — this
// shape is the one a host with a broken wrapper produces, and it keeps the
// classification testable on a machine that happens to have `lsof`.
func absentToolScript(program string) string {
	return "#!/bin/sh\n" + recordInvocation(program) +
		`printf '%s: not found\n' "` + program + `" >&2` + "\n" + "exit 127\n"
}

// portsToolsPathPrefix is the PATH entry sessionEnv prepends, or "" when the
// switch is off. Kept as one function so the environment has ONE spelling of
// the switch.
func portsToolsPathPrefix() string {
	if portsToolsDir == "" {
		return ""
	}
	return portsToolsDir + string(os.PathListSeparator)
}

// withPortsToolsString is the session environment's PATH under the switch:
// the same PATH a real sshd would hand on, with the fixture directory in front
// of it. Split out because it is the half a Go test can assert without a shell.
func withPortsToolsString(path string) string {
	prefix := portsToolsPathPrefix()
	if prefix == "" {
		return path
	}
	return prefix + path
}
