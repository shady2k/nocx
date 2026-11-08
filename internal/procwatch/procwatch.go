// Package procwatch observes the lifecycle of processes THIS backend
// started: it answers "is the executable I launched still the one running
// under that pid, and if not, what is?" and "is that pid gone yet?" ([WaitForExit]).
//
// It exists because the product had exactly one detector for a shell that
// will never integrate — the handshake bound expiring ten seconds later
// (nocx-cgzc). The shell that gets taken over is our own child, and it
// becomes somebody else's image within milliseconds: a user's ~/.zshrc that
// execs a pty wrapper before nocx's own line runs leaves a tab that looks
// integrated for ten seconds and then is not.
//
// # This is not stream sniffing
//
// AD-6 forbids the backend to interpret the BYTES a session produces. This
// package never reads them. It asks the kernel about the process table entry
// of a pid the backend itself forked, which is the same class of knowledge as
// "the shell exited" (exec.Cmd.Wait) and carries none of the terminal's
// content. ADR-0024 separately rules out TIOCGPGRP as an authority on "who
// wrote these bytes"; nothing here claims that authority either — an
// observation is a GUESS, the contract says so
// (contracts/session.integrationChanged.schema.json: detail is "best-effort
// observation for the details surface, never authority"), and the product
// labels it as one. The domain is still established only by the
// authenticated hello.
//
// # By name only
//
// An Observation carries a bare executable name — no path, no arguments, no
// command line. That is what the schema permits, and the reason is not
// brevity: a command line carries the user's own text into a surface that is
// not theirs. On darwin the source is the kernel's own p_comm, which is
// derived from the image's basename and truncated to 16 characters, so the
// restriction is enforced by where the value comes from and not only by a
// filter here. Note what this is NOT: KERN_PROCARGS2, whose environment half
// macOS has refused to answer for any other pid since 10.15 (that read was
// removed in nocx-58gq/nocx-65v6). p_comm is a different sysctl and is public
// — it is what `ps` prints.
//
// # One platform observes, the rest say so
//
// The platform half sits behind one build-tagged constructor, the house
// pattern (internal/contentkey, internal/nativeports):
//
//   - darwin: kqueue EVFILT_PROC with NOTE_EXEC. The kernel tells us the
//     moment our own child replaces its image, which is the whole point —
//     the answer arrives in milliseconds and does not depend on us asking at
//     the right moment.
//   - everywhere else: ErrUnsupported, deliberately. Linux has no NOTE_EXEC.
//     The three alternatives were considered and none is worth having:
//     polling /proc/<pid>/exe is a race by construction (a wrapper that execs
//     and exits between two polls is invisible, and a poll interval is the
//     timing dependence AGENTS.md forbids); the netlink proc connector needs
//     a privilege a terminal has no business asking for; and ptrace changes
//     the child's behaviour to observe it. An honest "this platform does not
//     observe exec" leaves the handshake bound as the detector it already is,
//     which is a slower correct answer rather than a fast wrong one. The MVP
//     desktop shell is macOS (AD-3), and this is the platform the defect was
//     measured on.
//
// # A measurement worth keeping
//
// macOS's /bin/sh replaces its own image with bash about twenty milliseconds
// after it starts — p_comm goes "sh" then "bash". It is a real, benign
// self-replacement and it is why nothing here is tested with sh. It reaches
// no session: LocalShellKind maps sh to ShellUnknown, and an unknown shell is
// reported conventional and never watched. zsh and bash, the two shells nocx
// does start an integrated session with, hold their name for the life of the
// process, and their p_comm is the basename of the path that was exec'd.
package procwatch

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// ErrUnsupported reports that this platform cannot observe a process being
// replaced. It is typed rather than silent so a caller can tell "no takeover
// happened" from "nobody was looking" — the difference between a working
// detector and a soft degrade nothing in the product would contradict.
var ErrUnsupported = errors.New("procwatch: this platform does not observe process replacement")

// ErrClosed reports a watch requested after the watcher was closed.
var ErrClosed = errors.New("procwatch: watcher closed")

// Observation is one thing the watcher saw happen to a process the backend
// started: the executable running under that pid is not the one that was
// launched.
type Observation struct {
	// PID is the process the backend started — still the same pid; an exec
	// replaces the image, not the process.
	PID int
	// Name is the executable now running there, by name only. Never a path,
	// never arguments, never a command line.
	Name string
}

// Sink receives an observation. It is called at most once per watch, on the
// watcher's own goroutine or inside Started, and must not block: a sink that
// waits stalls every other watch on the same queue.
type Sink func(Observation)

// Watcher observes processes the backend started. One implementation per
// platform, chosen by New; the interface is what the composition root
// injects, so nothing downstream knows which platform it is on.
type Watcher interface {
	// Started begins observing pid, which the backend launched as
	// expectedExecutable (a path or a bare name — only the name is used).
	// From the moment it returns until stop is called, the sink is invoked
	// at most once, with the name of the executable found running in place
	// of the expected one.
	//
	// The returned stop is always non-nil and always safe to call, including
	// after an error and more than once. An error means no observation will
	// ever arrive — ErrUnsupported on a platform that cannot look, or the
	// kernel refusing the watch (a process that has already exited) — and
	// the caller carries on, because the handshake bound is still the
	// backstop it always was.
	Started(pid int, expectedExecutable string, sink Sink) (stop func(), err error)

	// Close releases the watcher's kernel resources and its goroutine.
	// Idempotent.
	Close() error
}

// unsupported is the watcher for a platform that cannot observe an exec, and
// for a darwin that could not open a kqueue. It accepts nothing and hides
// nothing.
type unsupported struct{ err error }

func (u unsupported) Started(int, string, Sink) (func(), error) {
	return func() {}, u.err
}

func (unsupported) Close() error { return nil }

// ---------------------------------------------------------------------------
// Waiting for a process to be gone
// ---------------------------------------------------------------------------

// WaitForExit blocks until the process named by pid is gone.
//
// THE KERNEL'S OWN ANSWER FIRST, WHERE IT EXISTS. waitForExitPidfd
// (wait_linux.go) is one descriptor that becomes readable only when the
// process it names has exited, so there is no polling window and no pid reuse
// to get wrong. pidfd_open lives only in x/sys/unix's Linux half, so every
// other platform answers supported=false and takes the poll below
// (wait_other.go) — which is also where macOS lands: its update restart runs
// the same waiter (relaunch.go) and polls the window's pid.
//
// The poll is the same fact at a coarser grain: it asks whether the pid still
// exists, never a sleep that assumes the answer.
//
// This is the second question this package asks the kernel about a pid, and
// the two answer differently on purpose. "Has this process been replaced?" has
// no Linux answer short of a race or a privilege (see the package comment),
// while "is this process gone?" has had one since 5.3. Both are observations
// about a pid the backend or its own waiter started — never about the bytes a
// session produces (AD-6).
func WaitForExit(pid int) error {
	if supported, err := waitForExitPidfd(pid); supported {
		return err
	}
	return pollExit(pid)
}

// pollExit asks whether pid still exists, until it does not.
func pollExit(pid int) error {
	for {
		killErr := unix.Kill(pid, 0)
		switch {
		case errors.Is(killErr, unix.ESRCH):
			return nil
		case killErr != nil && !errors.Is(killErr, unix.EPERM):
			return fmt.Errorf("poll pid %d: %w", pid, killErr)
		}
		time.Sleep(exitPollInterval)
	}
}

// exitPollInterval is how often a platform without pidfd_open asks. It is a
// poll interval, not a deadline: nothing here ever concludes that a process
// is gone because time passed.
const exitPollInterval = 20 * time.Millisecond
