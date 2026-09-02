package session

import (
	"encoding/binary"
	"time"

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
	// status is the process-status triple: when the kernel says the process
	// began, who its parent is now, and what state it is in.
	//
	// ONE CALL FOR THREE FACTS because that is how both kernels hand them
	// over — macOS's kern.proc.pid answer already carries all three beside
	// the p_comm this source reads anyway, and Linux's /proc/<pid>/stat is a
	// single line holding all three. Three methods would be three reads of
	// one file on Linux and three identical sysctls on macOS, and would
	// invite three inconsistent snapshots of a thing that changes.
	//
	// A field the source could not fill is left at its zero value and the
	// judgement is Observe's, exactly as it is for cwd: this half answers,
	// the other half decides what counts as missing.
	status(pid int) (procStatus, error)
}

// procStatus is the kernel's answer to the process-status triple, in units
// this package owns rather than in either kernel's own spelling.
//
// StartTime is an ABSOLUTE INSTANT and not the raw counter Linux keeps. That
// is a real conversion with a real cost — /proc/<pid>/stat measures start in
// clock ticks since boot, so procfsSource has to learn the boot instant to
// answer at all — and the alternative was worse. Ticks-since-boot plus a boot
// identifier would put the SAME FIELD in two different units depending on
// which kernel answered, and every reader would have to hold both
// derivations; macOS has no such counter to report, so the field would be a
// darwin instant next to a linux pair, which is two facts wearing one name.
// An instant is what the reader wants in both cases and is what the wire
// carries everywhere else (proto.FormatTime).
type procStatus struct {
	// StartTime is when the kernel says the process began. The zero time
	// means the source could not work it out.
	StartTime time.Time
	// Ppid is the parent as the kernel reports it now. Zero means unknown:
	// no process the helper spawns has a parent of 0, so the zero value
	// cannot collide with an answer.
	Ppid int
	// State is the normalised kernel state, empty when the source could not
	// read it or answered a code the closed vocabulary cannot spell.
	State proto.ProcessState
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

	// The process-status triple. One read, three verdicts: a source that
	// answered the read but could not fill one field names THAT field, so a
	// reader is never told a parent is unknown because a start time was.
	status, err := i.src.status(pid)
	if err != nil {
		status = procStatus{}
	}
	if !status.StartTime.IsZero() {
		obs.StartTime = proto.FormatTime(status.StartTime)
	} else {
		obs.Unavailable = append(obs.Unavailable, proto.DiagnosticStartTime)
	}
	if status.Ppid > 0 {
		obs.Ppid = status.Ppid
	} else {
		obs.Unavailable = append(obs.Unavailable, proto.DiagnosticPpid)
	}
	if status.State != "" {
		obs.State = status.State
	} else {
		obs.Unavailable = append(obs.Unavailable, proto.DiagnosticState)
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
