//go:build darwin

package session

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// NewInspector is the composition root's per-OS choice; see the linux sibling
// for the shape.
//
// macOS reads sysctl. This is the platform nocx ships on first, and until
// nocx-k6p18.10 it resolved to no inspector at all: ProcFS answered nil off
// Linux, so the shipped platform had no observations and the inventory fell
// back to launch metadata alone — correct, because launch is the authority,
// and silent about everything that changes after launch.
func NewInspector() Inspector {
	return &osInspector{src: darwinSource{}}
}

// darwinSource answers from sysctl, cgo-free.
//
// # What sysctl can and cannot answer, measured rather than assumed
//
// ANSWERED, through golang.org/x/sys/unix — which the helper already links on
// darwin, because internal/pty needs it, so this adds no dependency:
//
//	argv                kern.procargs2.<pid>
//	command name        kern.proc.pid.<pid> -> kinfo_proc.kp_proc.p_comm
//	liveness            the same call failing is how a dead pid is detected
//	start time          the same answer -> kp_proc.p_starttime
//	parent              the same answer -> kp_eproc.e_ppid
//	process state       the same answer -> kp_proc.p_stat
//
// NOT ANSWERED: the working directory. macOS exposes no sysctl for another
// process's cwd — KERN_PROC's subtypes are pid/pgrp/tty/uid/all and none of
// them carry it, and kern.proc.cwd is FreeBSD's, not Darwin's. The only route
// is proc_pidinfo(PROC_PIDVNODEPATHINFO) in libproc, and that is a libSystem
// symbol: reaching it needs cgo, or hand-written per-architecture assembly
// trampolines duplicating what x/sys/unix generates — unverifiable on any
// machine that is not a Mac.
//
// Nothing forbids cgo, and an earlier version of this comment claimed D3's
// size argument did. It does not: D3 argues for a THIN PRODUCT, never for a
// toolchain. What cgo actually costs here is the BUILD, and it is a real cost
// rather than a preference: darwin/* would have to be built on a Mac, which
// splits nocx-6de7c's one-origin decision into one origin per platform, and
// the deployment floor stops being Go's and becomes whatever
// -mmacosx-version-min the CI runner happens to pick, so it has to be pinned
// and asserted. nocx-k6p18.14 is where that is decided, not here.
//
// Shelling out to lsof was the third option and is not taken: it
// forks a process per inventory row on a host we do not own, and makes a
// diagnostic depend on a tool being present.
//
// So cwd errors here, and inspect.go turns that into an explicit "unavailable"
// on the wire. That is the honest answer rather than a failure: the product
// can say "we do not know where this shell is", which is the one thing it
// could not say before. What it must never do is leave the field empty and let
// a reader fall back to the launch record.
type darwinSource struct{}

// errNoDarwinCwd is the one diagnostic this platform cannot answer at all. It
// is an ordinary error on purpose: inspect.go reports a call that failed and a
// call that cannot exist the same way, because the reader's problem is
// identical and the reason is not its business.
var errNoDarwinCwd = errors.New("macOS exposes no sysctl for another process's working directory")

func (darwinSource) source() string { return "sysctl" }

// alive asks the kernel for the process. kern.proc.pid answers a fixed-size
// kinfo_proc for a live pid and fails for one it does not have, so the same
// call is both the liveness check and the source of p_comm.
func (darwinSource) alive(pid int) bool {
	_, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil
}

func (darwinSource) cwd(int) (string, error) { return "", errNoDarwinCwd }

// argv returns the raw KERN_PROCARGS2 block. SysctlRaw sizes the buffer from
// the kernel's own answer, so a shell with a long argument vector is not
// truncated by a constant chosen here.
func (darwinSource) argv(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}

func (darwinSource) argvFormat() argvFormat { return argvProcArgs2 }

// comm returns the command name the kernel holds for the process. It is
// MAXCOMLEN-truncated, exactly as /proc/<pid>/comm is on Linux.
func (darwinSource) comm(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	// p_comm is a NUL-padded fixed array; anything after the first NUL is
	// padding rather than part of the name.
	name := string(kp.Proc.P_comm[:])
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name), nil
}

// macOS's process states, from <sys/proc.h>. Named here rather than used as
// bare integers because 3 and 4 are one apart and mean "asleep at a prompt"
// and "suspended", which is the whole distinction the state field exists for.
const (
	darwinSIDL   int8 = 1 // being created by fork
	darwinSRUN   int8 = 2 // runnable
	darwinSSLEEP int8 = 3 // asleep on an address
	darwinSSTOP  int8 = 4 // suspended, or held by a debugger
	darwinSZOMB  int8 = 5 // exited, awaiting collection by its parent
)

// status answers the process-status triple from the SAME kern.proc.pid call
// that already answers comm and liveness — one sysctl, and kinfo_proc carries
// all three beside the command name. This is the platform on which
// nocx-k6p18.12's "free" is literally true: no new syscall, no new dependency,
// no new parse.
func (darwinSource) status(pid int) (procStatus, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return procStatus{}, err
	}
	out := procStatus{
		Ppid:  int(kp.Eproc.Ppid),
		State: darwinProcessState(kp.Proc.P_stat),
	}
	// p_starttime is an absolute wall-clock timeval, so unlike Linux's
	// ticks-since-boot it needs no boot instant to become one — which is why
	// the wire carries an instant rather than a counter: this platform has no
	// counter to send.
	if tv := kp.Proc.P_starttime; tv.Sec > 0 {
		out.StartTime = time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond))
	}
	return out, nil
}

// darwinProcessState maps <sys/proc.h>'s codes onto the closed vocabulary.
//
// SIDL — a process still being forked — has no member there and answers the
// empty state, which Observe reports as unavailable. It is not a session shell
// the helper spawned minutes ago, and giving it a plausible name would be
// inventing a value rather than reporting one. macOS also does not
// distinguish an uninterruptible wait from an ordinary one, so a process Linux
// would call `uninterruptible` is reported `sleeping` here: less precise, and
// still true.
func darwinProcessState(code int8) proto.ProcessState {
	switch code {
	case darwinSRUN:
		return proto.ProcessRunning
	case darwinSSLEEP:
		return proto.ProcessSleeping
	case darwinSSTOP:
		return proto.ProcessStopped
	case darwinSZOMB:
		return proto.ProcessZombie
	case darwinSIDL:
		return ""
	}
	return ""
}
