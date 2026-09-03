//go:build linux

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// NewInspector is the composition root's per-OS choice of evidence source,
// the shape internal/contentkey uses for readMachineID: one set of reporting
// rules (inspect.go), one leaf per platform, chosen by the BUILD rather than
// by a runtime switch nothing can typecheck.
//
// Linux reads /proc.
func NewInspector() Inspector {
	return &osInspector{src: &procfsSource{root: "/proc"}}
}

// procfsSource answers from /proc. It reports what it finds and returns the
// read's error otherwise; whether a failure counts as a missing diagnostic is
// inspect.go's decision and not this file's.
type procfsSource struct {
	// root is the procfs mount, "/proc" in production and a temporary
	// directory in tests. A field rather than a constant so the reads are
	// testable without the OS agreeing to cooperate.
	root string
	// bootOnce guards boot, which is read from /proc/stat at most once per
	// source. See bootTime for why once rather than per call.
	bootOnce sync.Once
	boot     time.Time
}

func (p *procfsSource) source() string { return "procfs" }

func (p *procfsSource) dir(pid int) string {
	return filepath.Join(p.root, strconv.Itoa(pid))
}

// alive asks whether /proc still has an entry for the process. A pid that
// exited leaves none, which is how the inspector knows to say nothing at all.
func (p *procfsSource) alive(pid int) bool {
	_, err := os.Stat(p.dir(pid))
	return err == nil
}

// cwd resolves /proc/<pid>/cwd. It is refused often enough to matter — a
// process that changed uid, a hardened container, a pid that exited between
// the stat above and this read — and a refusal here becomes an explicitly
// named missing diagnostic rather than a blank the reader mistakes for "the
// shell has not moved".
func (p *procfsSource) cwd(pid int) (string, error) {
	return os.Readlink(filepath.Join(p.dir(pid), "cwd"))
}

// argv reads /proc/<pid>/cmdline: NUL-separated arguments and nothing else.
func (p *procfsSource) argv(pid int) ([]byte, error) {
	// #nosec G304 — the path is this source's own procfs root joined with an
	// integer pid the helper itself recorded at spawn. No caller-supplied
	// string reaches it, and the root is a constant outside tests.
	return os.ReadFile(filepath.Join(p.dir(pid), "cmdline"))
}

func (p *procfsSource) argvFormat() argvFormat { return argvPlain }

// comm reads /proc/<pid>/comm, which the kernel truncates — evidence, and
// evidence of the same shape darwin's p_comm has.
func (p *procfsSource) comm(pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join(p.dir(pid), "comm")) // #nosec G304 — see argv
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// # The process-status triple on Linux, and the two traps in reading it
//
// /proc/<pid>/stat is one line holding all three facts, so this is one extra
// read beside the cmdline and comm reads already made — the same read `ps`
// makes, and cheap rather than free, which is the correction nocx-k6p18.12
// recorded rather than repeating its own title.

// procStatStartTime is where proc(5)'s field 22 lands once the fields BEFORE
// the command name are gone: fields[0] is field 3, so field 22 is index 19.
const procStatStartTime = 19

// procStatClockTick is how long one of /proc/<pid>/stat's starttime ticks
// lasts. The rate is USER_HZ, which the kernel fixes at 100 for everything it
// reports to user space regardless of CONFIG_HZ — the value
// sysconf(_SC_CLK_TCK) returns on every Linux port — so one tick is a
// hundredth of a second.
//
// A constant rather than a sysconf call because sysconf is libc and reaching
// it needs cgo, which this helper deliberately does not use. The cost is that
// a start time is accurate to 10ms, which is an identity rather than a
// stopwatch.
//
// It is a DURATION rather than a rate so the conversion cannot overflow: ticks
// multiplied by a whole second overflows int64 nanoseconds after eight years
// of uptime, and multiplied by this it does not overflow in any lifetime.
const procStatClockTick = 10 * time.Millisecond

var errProcStatMalformed = errors.New("procfs: /proc/<pid>/stat is not the layout proc(5) describes")

// status reads /proc/<pid>/stat and turns it into the triple.
func (p *procfsSource) status(pid int) (procStatus, error) {
	raw, err := os.ReadFile(filepath.Join(p.dir(pid), "stat")) // #nosec G304 — see argv
	if err != nil {
		return procStatus{}, err
	}
	return parseProcStat(raw, p.bootTime())
}

// parseProcStat reads the one line /proc/<pid>/stat holds.
//
// THE TRAP IS FIELD 2. It is the command name in parentheses and the kernel
// does not escape it, so a process named `weird) 0 0` puts spaces, digits and
// a closing parenthesis inside a field a whitespace split would tear apart —
// and the tear is silent, because what follows still parses as a state letter
// and a number. Everything before the LAST ')' is therefore skipped whole,
// which is the only reading that cannot be fooled: no field after it may
// contain a parenthesis.
//
// A boot instant that is not known yields no start time rather than one
// measured from the epoch: a start time in 1970 is a value, and a value is
// what this whole record exists to avoid inventing.
func parseProcStat(raw []byte, boot time.Time) (procStatus, error) {
	line := string(raw)
	end := strings.LastIndexByte(line, ')')
	if end < 0 {
		return procStatus{}, errProcStatMalformed
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) <= procStatStartTime {
		return procStatus{}, errProcStatMalformed
	}

	out := procStatus{State: linuxProcessState(fields[0])}
	if ppid, err := strconv.Atoi(fields[1]); err == nil && ppid > 0 {
		out.Ppid = ppid
	}
	if !boot.IsZero() {
		if ticks, err := strconv.ParseInt(fields[procStatStartTime], 10, 64); err == nil && ticks >= 0 {
			out.StartTime = boot.Add(time.Duration(ticks) * procStatClockTick)
		}
	}
	return out, nil
}

// linuxProcessState maps proc(5)'s state letter onto the closed vocabulary.
// A letter outside it — `X`, `x`, `W`, `P`, `K`, `I`, or anything a later
// kernel invents — answers the empty state, which Observe reports as the
// state being unavailable. None of them can describe a session shell, and
// guessing at one is how a vocabulary stops being closed.
func linuxProcessState(code string) proto.ProcessState {
	switch code {
	case "R":
		return proto.ProcessRunning
	case "S":
		return proto.ProcessSleeping
	case "D":
		return proto.ProcessUninterruptible
	case "T", "t":
		return proto.ProcessStopped
	case "Z":
		return proto.ProcessZombie
	}
	return ""
}

// bootTime is the instant /proc/stat's btime names, read AT MOST ONCE.
//
// Once rather than per call because boot time is a constant fact about the
// machine and the kernel's answer is not quite: btime is derived from the
// current wall clock less the uptime, so NTP stepping the clock moves it by
// milliseconds. Re-deriving it per inventory row would make a session's start
// time wobble between two reads of the same unchanged process, and a start
// time that wobbles cannot be compared against a recorded one — which is the
// whole job the pid-reuse guard has.
//
// A /proc/stat that cannot be read or carries no btime leaves the zero time,
// and parseProcStat then reports no start time at all.
func (p *procfsSource) bootTime() time.Time {
	p.bootOnce.Do(func() {
		raw, err := os.ReadFile(filepath.Join(p.root, "stat")) // #nosec G304 — see argv
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			rest, ok := strings.CutPrefix(line, "btime ")
			if !ok {
				continue
			}
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil || secs <= 0 {
				return
			}
			p.boot = time.Unix(secs, 0)
			return
		}
	})
	return p.boot
}
