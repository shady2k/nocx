package session

import (
	"encoding/binary"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The OS-evidence inspector: what the kernel says about a session's process
// NOW, as against what the helper recorded when it spawned it.
//
// EVIDENCE, NEVER AUTHORITY (D10). Every fact here can be wrong in a way the
// launch record cannot: argv is mutable by the process itself, a process can
// be replaced by exec, and the foreground group changes between one read and
// the next. So nothing here writes into a LaunchRecord and nothing here
// decides what a session IS; it fills proto.Observation, which the inventory
// carries beside the record and never instead of it.
//
// # One rule, one implementation per OS (nocx-k6p18.10)
//
// The shape is internal/contentkey's: the DERIVATION is written once and the
// only per-platform part is the leaf that talks to the kernel, chosen by the
// build rather than by a runtime switch nothing can typecheck. Two inspectors
// with a copy of the reporting rules each would be a second owner of the one
// decision that matters here — when a diagnostic counts as missing — and the
// two would agree everywhere anybody looked.
//
// It also keeps the darwin path honest on a machine that is not a Mac: the
// rules below and the argument-vector parse are exercised by the suite on
// every platform, against a source that answers from a table. Only the three
// syscalls are unrunnable off darwin.
type osInspector struct {
	src procSource
}

// procSource is the kernel half: five questions, and no reporting policy in
// any of them. An implementation answers what its OS can answer and returns an
// error for what it cannot — including "this platform has no such call at all",
// which is macOS's answer for cwd and is deliberately not a special case. The
// reason a diagnostic is missing does not change how it is reported, because
// the reader's problem is identical either way.
type procSource interface {
	// source names the evidence for proto.Observation.Source — "procfs",
	// "sysctl". Evidence that cannot say where it came from cannot be weighed
	// against a launch record it contradicts.
	source() string
	// alive answers whether the kernel still has this process. A pid it does
	// not have produces no observation at all.
	alive(pid int) bool
	// cwd is the process's current working directory: the fact that changes as
	// the user cds, and the one macOS cannot answer without cgo.
	cwd(pid int) (string, error)
	// argv is the raw argument blob as the kernel hands it over, in the layout
	// argvFormat names. Raw rather than parsed so that the parse — which has
	// the traps in it — is written once, here, and testable anywhere.
	argv(pid int) ([]byte, error)
	// argvFormat says how to read that blob.
	argvFormat() argvFormat
	// comm is a process's command name, as the kernel truncates it.
	comm(pid int) (string, error)
}

// Observe answers what this OS says about the shell and about whatever holds
// its terminal now.
//
// A process the kernel no longer has answers nil — never an empty record,
// which would decode as "we looked and this process has no working directory".
// A process it DOES have always answers something, even when every diagnostic
// failed: the process existing is itself evidence, and an observation that
// names what it could not find out is worth strictly more than silence.
func (i *osInspector) Observe(pid, foregroundPgid int) *proto.Observation {
	if !i.src.alive(pid) {
		return nil
	}
	// Non-nil from the first line: a nil slice marshals to `null`, and `null`
	// is not the same answer as `[]`. contracts caught that exact defect in
	// vault.status's providers field.
	obs := &proto.Observation{Source: i.src.source(), Unavailable: []proto.Diagnostic{}}

	if cwd, err := i.src.cwd(pid); err == nil && cwd != "" {
		obs.Cwd = cwd
	} else {
		// THIS IS THE BEAD. Left blank, a reader falls back to launch.cwd —
		// true once, stale the moment the user cds — and shows the directory
		// the shell STARTED in as though it were where the shell is. Named,
		// the product can say "we do not know", which is the one thing it
		// could not say before.
		obs.Unavailable = append(obs.Unavailable, proto.DiagnosticCwd)
	}

	if raw, err := i.src.argv(pid); err == nil {
		obs.Argv = parseArgv(raw, i.src.argvFormat())
	}
	if len(obs.Argv) == 0 {
		obs.Unavailable = append(obs.Unavailable, proto.DiagnosticArgv)
	}

	// A zero foreground group, or the shell's own, means no job is running:
	// there is nothing to ask about, so nothing is reported and nothing is
	// named missing. Saying so by omission is more honest than naming the
	// shell as though it were a job. Only a group we HAVE and cannot name is a
	// diagnostic that went missing.
	if foregroundPgid > 0 && foregroundPgid != pid {
		obs.ForegroundPgid = foregroundPgid
		if comm, err := i.src.comm(foregroundPgid); err == nil && comm != "" {
			obs.ForegroundCommand = comm
		} else {
			obs.Unavailable = append(obs.Unavailable, proto.DiagnosticForegroundCommand)
		}
	}
	return obs
}

// argvFormat names the layout a kernel hands its argument vector over in.
// There are two, they differ only by a header, and one parser reads both —
// rather than a parser per platform, which is how the two would agree
// everywhere anybody looked and differ on the padding.
type argvFormat int

const (
	// argvPlain is Linux's /proc/<pid>/cmdline: the vector and nothing else.
	argvPlain argvFormat = iota
	// argvProcArgs2 is darwin's KERN_PROCARGS2.
	argvProcArgs2
)

// parseArgv turns a kernel's argument blob into a vector.
//
// argvPlain — /proc/<pid>/cmdline — is NUL-separated arguments and nothing
// else. The trailing NUL is a SEPARATOR and not an empty final argument, which
// is the one thing a naive split gets wrong.
//
// argvProcArgs2 — macOS's kern.procargs2.<pid> — is laid out:
//
//	int32   argc, in the host's byte order
//	char[]  the executable path, NUL-terminated
//	byte[]  NUL padding to the kernel's alignment — a run of VARIABLE length,
//	        which is the first trap: skipping a fixed number of bytes reads
//	        argv[0] as an empty string on some processes and not others
//	char[]  argc NUL-terminated arguments
//	char[]  the environment, which is deliberately NOT read — it is not a
//	        diagnostic D3 permits and it carries secrets, and argc is what
//	        stops the read before it
//
// The second trap is argc itself: it comes from the kernel but the blob may be
// short — a process that exec'd between the size probe and the read — so the
// loop is bounded by the buffer too and reports what is actually there.
// Partial evidence is evidence; absent evidence is absent.
func parseArgv(raw []byte, format argvFormat) []string {
	count := -1 // -1: every argument in the blob
	if format == argvProcArgs2 {
		const argcSize = 4
		if len(raw) < argcSize {
			return nil
		}
		// Little-endian on both darwin architectures nocx builds for, and the
		// kernel writes it in host order.
		argc := binary.LittleEndian.Uint32(raw[:argcSize])
		// Every argument occupies at least its own terminating NUL, so a count
		// larger than the block cannot be a count — it is a truncated or
		// corrupt read, which is also what an all-ones word is. Bounding
		// against the buffer rather than against a constant means there is no
		// number here anybody has to justify.
		if argc == 0 || uint64(argc) > uint64(len(raw)) {
			return nil
		}
		count = int(argc)

		// Past the executable path, then past however many NULs pad it.
		rest := raw[argcSize:]
		i := 0
		for i < len(rest) && rest[i] != 0 {
			i++
		}
		for i < len(rest) && rest[i] == 0 {
			i++
		}
		raw = rest[i:]
	}

	var out []string
	i := 0
	for i < len(raw) && (count < 0 || len(out) < count) {
		start := i
		for i < len(raw) && raw[i] != 0 {
			i++
		}
		out = append(out, string(raw[start:i]))
		i++ // past the terminator
	}
	return out
}
