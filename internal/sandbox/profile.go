//go:build darwin

// Seatbelt is the macOS backend and nothing else builds an SBPL profile, so
// this file is darwin-only. It was once untagged so the renderer could be
// unit-tested from Linux, and the price was five entries in the deadcode
// baseline — which is a UNION across darwin and linux, so baselining them
// here also blinded the ratchet on the one platform where these functions are
// the enforcement path (nocx-ru6kq). The tests move with the file; ci-mac runs
// internal/sandbox natively.

package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// SBPL (Seatbelt Profile Language) rendering for the macOS backend
// (design spec §9.2). The renderer is deterministic: the same Policy always
// produces the same profile. It is deliberately platform-neutral string
// logic (no darwin build tag) so the injection and determinism tests run on
// every CI platform; only the probe and launch wrapper are darwin-tagged.
//
// The clause set is nocx's own, written against the public Seatbelt
// semantics; termic behavior was compared in the research report §1.4 but no
// AGPL code was copied.

// renderProfile renders a fresh SBPL profile for the common policy. A single
// optional deny message correlates macOS diagnostics; more than one is invalid.
// Escaping is centralized and rejects newline/NUL/control characters before
// anything reaches the profile.
func renderProfile(p *Policy, messages ...string) (string, error) {
	if len(messages) > 1 {
		return "", errors.New("multiple Seatbelt deny messages")
	}
	message := ""
	if len(messages) == 1 {
		message = messages[0]
	}
	var b strings.Builder
	b.WriteString("(version 1)\n")
	if message == "" {
		b.WriteString("(deny default)\n")
	} else {
		escaped, err := escapeSBPL(message)
		if err != nil {
			return "", err
		}
		b.WriteString("(deny default (with message \"" + escaped + "\"))\n")
	}
	// Process essentials for an interactive shell: exec/fork for children,
	// signals within the cage, Mach/IPC lookups, and process introspection
	// of self. file-map-executable and file-read-metadata are global because
	// reads themselves stay bounded by the per-root file-read* clauses.
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow signal (target self))\n")
	b.WriteString("(allow signal (target children))\n")
	b.WriteString("(allow process-info* (target self))\n")
	b.WriteString("(allow mach-priv-task-port)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow sysctl-write (sysctl-name \"kern.grade_cputype\"))\n")
	b.WriteString("(allow iokit-open (iokit-registry-entry-class \"RootDomainUserClient\"))\n")
	// A deny-default Seatbelt child still needs these kernel-mediated runtime
	// services. They do not grant filesystem paths: filesystem access remains
	// governed solely by the root/file clauses below.
	b.WriteString("(allow user-preference-read)\n")
	b.WriteString("(allow ipc-posix-shm)\n")
	b.WriteString("(allow ipc-posix-sem)\n")
	b.WriteString("(allow ipc-sysv-sem)\n")
	b.WriteString("(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))\n")
	b.WriteString("(allow pseudo-tty)\n")
	b.WriteString("(allow file-read-metadata)\n")
	b.WriteString("(allow file-map-executable)\n")
	// Network isolation is out of scope: the contract leaves network
	// unrestricted (design spec §2).
	b.WriteString("(allow network*)\n")

	// subpath clauses do not match the root directory node itself. dyld and
	// libSystem stat/open that node during process startup; grant the node
	// literally without exposing any descendant content.
	b.WriteString("(allow file-read* (literal \"/\"))\n")

	for _, root := range p.ReadOnlyRoots {
		esc, err := escapeSBPL(root)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-read* (subpath \"" + esc + "\"))\n")
	}
	for _, file := range p.ReadOnlyFiles {
		esc, err := escapeSBPL(file)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-read* (literal \"" + esc + "\"))\n")
	}
	for _, root := range p.WritableRoots {
		esc, err := escapeSBPL(root)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-read* (subpath \"" + esc + "\"))\n")
		b.WriteString("(allow file-write* (subpath \"" + esc + "\"))\n")
	}
	for _, dir := range p.WritableDirs {
		esc, err := escapeSBPL(dir)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-read* (subpath \"" + esc + "\"))\n")
		b.WriteString("(allow file-write* (subpath \"" + esc + "\"))\n")
	}
	for _, file := range p.WritableFiles {
		esc, err := escapeSBPL(file)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-read* (literal \"" + esc + "\"))\n")
		b.WriteString("(allow file-write* (literal \"" + esc + "\"))\n")
		b.WriteString("(allow file-ioctl (literal \"" + esc + "\"))\n")
	}
	return b.String(), nil
}

// escapeSBPL validates and escapes a path for use inside a quoted SBPL
// string. Control characters (including newline and NUL) are rejected before
// rendering; backslash and double quote are escaped.
func escapeSBPL(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("sandbox: empty path in SBPL clause")
	}
	var b strings.Builder
	for _, r := range s {
		if r == 0 || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("sandbox: control character 0x%02x in SBPL clause (path injection rejected)", r)
		}
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}
