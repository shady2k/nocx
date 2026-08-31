package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ProcFS is the OS-evidence inspector: it reads /proc and reports what it
// finds, or nothing.
//
// EVIDENCE, NEVER AUTHORITY (D10). Every fact here can be wrong in a way the
// launch record cannot: argv is mutable by the process itself, a process can
// be replaced by exec, and the foreground group changes between one read and
// the next. So this never writes into a LaunchRecord and never decides what a
// session IS; it fills proto.Observation, which the inventory carries beside
// the record and never instead of it.
//
// It reports NIL rather than an empty Observation when it cannot answer, and
// that distinction is the whole reason the field is a pointer: macOS has no
// /proc at all, so "nothing to say" is the ordinary answer on an entire
// platform, and an empty record there would decode as "we looked and this
// process has no working directory".
//
// # Why not proc_pidinfo on macOS
//
// It is the right call there and it is deliberately not made here: it needs
// cgo, and the helper is the binary whose size and portability are the point
// (~2.8 MiB against ~40 MiB). A cgo-free darwin implementation belongs behind
// this same interface, added when the diagnostics are wanted on macOS hosts
// enough to pay for it — which is why this is an interface with a default of
// "no evidence" rather than a function everything calls.
type ProcFS struct {
	// root is the procfs mount, "/proc" in production and a temporary
	// directory in tests. A field rather than a constant so the parsing is
	// testable without the OS agreeing to cooperate.
	root string
}

// NewProcFS builds the production inspector over /proc.
func NewProcFS() *ProcFS { return &ProcFS{root: "/proc"} }

// Observe reads what /proc says about the shell and about whatever is
// currently in the foreground of its terminal. A missing /proc, a dead
// process or an unreadable entry all answer nil: partial evidence is
// reported, absent evidence is absent, and neither is ever an error the
// inventory fails on — a session the helper holds must be listable even on a
// platform that can say nothing about it.
func (p *ProcFS) Observe(pid, foregroundPgid int) *proto.Observation {
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	obs := &proto.Observation{Source: "procfs"}
	if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		obs.Cwd = cwd
	}
	// #nosec G304 — the path is this inspector's own procfs root joined with an
	// integer pid the helper itself recorded at spawn. No caller-supplied
	// string reaches it, and the root is a constant outside tests.
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		obs.Argv = splitCmdline(raw)
	}
	// The foreground group is what a person actually wants to know — "what is
	// running in this session right now" — and it is the most volatile fact
	// here, which is exactly why it is evidence. A zero group means the shell
	// itself is in the foreground: there is no job, and saying so by omission
	// is more honest than naming the shell as though it were one.
	if foregroundPgid > 0 && foregroundPgid != pid {
		obs.ForegroundPgid = foregroundPgid
		if comm, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(foregroundPgid), "comm")); err == nil {
			obs.ForegroundCommand = strings.TrimSpace(string(comm))
		}
	}
	return obs
}

// splitCmdline turns /proc's NUL-separated argument vector into a slice. A
// trailing NUL is a separator and not an empty final argument, which is the
// one thing a naive split gets wrong.
func splitCmdline(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, string(part))
	}
	return out
}
