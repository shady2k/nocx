package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/loginshell"
	"golang.org/x/sys/unix"
)

type LocalPty struct {
	log    log.Logger
	cmd    *exec.Cmd
	file   *os.File
	mu     sync.Mutex
	done   chan struct{}
	closed bool

	// waitErr is what cmd.Wait returned when the shell process ended, and
	// waitSet whether it has been captured. Written by the watcher goroutine
	// BEFORE close(done), so a reader that has observed <-done sees the
	// write (channel-close ordering) and needs no additional synchronisation.
	// The exit-caused classification (nocx-ictcq) reads it to tell an
	// authoritative shell exit — nil, or an *exec.ExitError carrying the
	// status — from a teardown that never let the process report one.
	waitErr error
	waitSet bool

	// readyFd, wakeR and wakeW are WaitReadable's own machinery (nocx-6q1uh.18),
	// never touched by RawReadUntilAgain: readyFd is a unix.Dup of the
	// master's fd, held open ONLY so WaitReadable can unix.Poll it without
	// ever going through (*os.File).SyscallConn().Read — the call that used
	// to hold the master file's own read lock for the whole wait, which is
	// the lock RawReadUntilAgain (via the SAME call, on the SAME file) needed
	// next and could never get while a readiness wait was parked on an idle
	// program. wakeR/wakeW are a self-pipe: WakeReadiness writes one byte to
	// wakeW to unpark a WaitReadable blocked in the kernel's poll(2), which
	// no context cancellation reaches on its own — ctx is not part of the
	// syscall. Set once, at construction, in NewLocal; read-only afterward
	// except for the close in Close.
	readyFd int
	wakeR   int
	wakeW   int
	// readyMu guards the lifetime of those three descriptors (nocx-mrfe5).
	// readyClosed is Close's latch: once set, WaitReadable answers
	// os.ErrClosed rather than polling again. readyWaiters counts the
	// WaitReadable calls between their check of that latch and their return,
	// and the descriptors are closed only when it is zero — by Close if no
	// wait is in flight, otherwise by the last wait on its way out — so a
	// poll(2) never runs on a descriptor that has been closed, or on a number
	// the kernel has already handed to something else. readyFdsClosed is what
	// WakeReadiness checks before it writes.
	readyMu        sync.Mutex
	readyClosed    bool
	readyWaiters   int
	readyFdsClosed bool
}

// localeVars are checked in POSIX precedence order; any one of them present
// means the environment already states a locale.
var localeVars = []string{"LC_ALL=", "LC_CTYPE=", "LANG="}

// launcherSessionVars identify the SESSION that launched nocx, not the user's
// environment. A terminal hands out shells; it must not hand out its
// launcher's identity with them. When nocx is started from inside a coding
// agent — which is exactly how it gets developed — every shell it spawns
// inherited that agent's session markers, and a `claude` run in a tab saw
// CLAUDE_CODE_CHILD_SESSION and silently disabled transcript saving.
//
// Deliberately a precise list rather than a CLAUDE* wildcard: stripping
// something like an API key would break the very tool we are trying to fix.
// It grows as other launchers are found.
//
// NO_COLOR= and TERM= belong to the same class of leak: coding agents run
// nocx's dev harness with TERM=dumb / NO_COLOR=1 in their tool environment,
// and every spawned shell then tells its TUIs "no colors here" — claude
// renders black-and-white. A terminal emulator declares color capability
// itself (TERM=xterm-256color + COLORTERM=truecolor are appended below);
// the launcher's opinion must not leak into the PTY.
var launcherSessionVars = []string{
	"CLAUDECODE=",
	"CLAUDE_CODE_ENTRYPOINT=",
	"CLAUDE_CODE_EXECPATH=",
	"CLAUDE_CODE_SESSION_ID=",
	"CLAUDE_CODE_CHILD_SESSION=",
	"CLAUDE_PID=",
	"CLAUDE_EFFORT=",
	"NO_COLOR=",
	"TERM=",
	// NOCX_TOOL_SOCKET is the same class and the sharpest case of it: it
	// names ONE coordinator's agent tool endpoint, and a pane belongs to
	// whichever coordinator opened it. nocx is developed from inside nocx,
	// and a helper daemon is started by whichever coordinator got there
	// first while the generation's endpoint socket serves them all (D12) —
	// so without this line every pane inherited one coordinator's endpoint
	// and an agent in it reached a backend that never asked for that pane
	// (nocx-e7khb, seen on macOS CI as a pane printing the daemon's start
	// value instead of its own).
	//
	// The value a pane may have is rendered into its LAUNCH from the
	// request that opened it (shellintegration.LaunchOptions'
	// AgentToolSocketPath), which is the one owner of the variable; this
	// list only guarantees there is nothing underneath for that launch to
	// have to overwrite. A pane whose launch renders none therefore has
	// none, which is what the spawn contract already says it has. The
	// spelling is pinned to shellintegration.ToolSocketEnvVar by
	// TestScrubLauncherSession rather than by an import, so the low-level
	// terminal package keeps depending on nothing above it.
	"NOCX_TOOL_SOCKET=",
}

func scrubLauncherSession(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, prefix := range launcherSessionVars {
			if strings.HasPrefix(kv, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// withUTF8Locale guarantees the child shell knows it is on a UTF-8 terminal.
// A GUI app launched from Finder or the Dock inherits none of the shell's
// environment, so without this the shell has no locale, and any Python/Rich
// TUI downstream encodes its output with errors="replace" — turning every
// non-ASCII glyph into a literal '?'. That failure is invisible when launched
// from a terminal, where LANG is inherited, and it masquerades as a font bug.
// Only fills a gap: an inherited locale, UTF-8 or not, is left alone.
func withUTF8Locale(env []string) []string {
	for _, kv := range env {
		for _, prefix := range localeVars {
			if strings.HasPrefix(kv, prefix) {
				return env
			}
		}
	}
	return append(env, "LANG=en_US.UTF-8")
}

// resolveCwd picks where the shell starts. A GUI app launched from Finder or
// the Dock inherits "/" as its working directory, which is useless as a
// starting point and useless as a tab name, so an unset Cwd falls back to the
// user's home the way Terminal.app and iTerm do.
func resolveCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func NewLocal(logger log.Logger, cfg Config) (*LocalPty, error) {
	// The launcher may name an explicit command (e.g. a lifecycle bootstrap
	// that must start bash with `--rcfile` so the per-epoch capability
	// rides script text, never the environment — nocx-u7uh.21). Every
	// production local session arrives that way since nocx-wwz0: the
	// composition root resolves the login shell, chooses the tier and names
	// both. An empty Command is the library default for a caller that has no
	// composition root behind it, and it asks the SAME owner rather than
	// deriving a second answer of its own (internal/loginshell).
	var cmd *exec.Cmd
	if cfg.Command != "" {
		cmd = exec.Command(cfg.Command, cfg.Args...) //nolint:gosec // the launcher names its own shell
	} else {
		shell := loginshell.New().Resolve()
		// Logged, not merely decided. Which shell a session runs is the single
		// biggest thing that varies between two machines running the same code,
		// and each tier answers a different amount of the protocol — bash and
		// zsh emit the OSC 636 command snapshot (nocx-qduc gave zsh its half),
		// the POSIX tier emits none of it, so the shell still decides whether
		// tab completion ever learns a command name. This line is what lets a
		// run's account answer that without inference (nocx-z9s9.9).
		logger.Info("local pty shell resolved", "shell", shell.Path, "source", string(shell.Source))
		cmd = exec.Command(shell.Path, "-i") //nolint:gosec // shell is from the account record or a detected path
	}
	cmd.Dir = resolveCwd(cfg.Cwd)
	env := withUTF8Locale(append(
		scrubLauncherSession(os.Environ()),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	))
	env = append(env, cfg.Env...)
	cmd.Env = env
	cmd.ExtraFiles = cfg.ExtraFiles

	f, err := startWithSize(cmd, &pty.Winsize{
		Cols: cfg.Cols,
		Rows: cfg.Rows,
		X:    cfg.XPixel,
		Y:    cfg.YPixel,
	})
	if err != nil {
		return nil, err
	}

	// The readiness dup and the wake pipe (nocx-6q1uh.18): see the LocalPty
	// field doc for why WaitReadable needs a fd of its own rather than going
	// through f's SyscallConn like RawReadUntilAgain does. f.Fd() is safe to
	// call here — it never reverts the master to blocking, exactly as
	// Resize's own pty.Setsize(lp.file, ...) already relies on (see that
	// method's doc and master_nonblock_{linux,darwin}.go's longer note on
	// the same mechanism) — because openMaster set O_NONBLOCK on the raw fd
	// before this file was ever wrapped, so os.NewFile never marked it as a
	// descriptor Go itself must revert on a raw-fd escape.
	readyFd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		_ = f.Close()
		reapAfterFailedSetup(cmd)
		return nil, fmt.Errorf("pty: dup the master for readiness polling: %w", err)
	}
	unix.CloseOnExec(readyFd)
	wakeR, wakeW, err := newWakePipe()
	if err != nil {
		_ = unix.Close(readyFd)
		_ = f.Close()
		reapAfterFailedSetup(cmd)
		return nil, fmt.Errorf("pty: open the readiness wake pipe: %w", err)
	}

	lp := &LocalPty{
		log:     logger,
		cmd:     cmd,
		file:    f,
		done:    make(chan struct{}),
		readyFd: readyFd,
		wakeR:   wakeR,
		wakeW:   wakeW,
	}

	go func() {
		waitErr := cmd.Wait()
		// Record BEFORE close(done): the transport's exit monitor wakes on
		// Done and reads WaitErr to classify how the session ended — a
		// shell's own exit (authoritative, with a status) versus a loss
		// (nocx-ictcq). Channel-close ordering publishes the write.
		lp.mu.Lock()
		lp.waitErr = waitErr
		lp.waitSet = true
		lp.mu.Unlock()
		close(lp.done)
	}()

	return lp, nil
}

// startWithSize is creack/pty's StartWithAttrs, with the ONE difference this
// bead exists for: the master comes from this package's own openMaster
// (master_nonblock_linux.go, master_nonblock_darwin.go), non-blocking from
// the moment it is opened, rather than from creack/pty's Open — which on
// Darwin never becomes pollable at all and on Linux is reverted to blocking
// by its own ptsname/unlockpt ioctls (both files' doc comments have the
// measurement). Everything else here — the slave becomes the child's
// stdin/stdout/stderr, Setsid and Setctty, the size applied before Start —
// is what StartWithAttrs already does, over the master this package opened
// instead of creack/pty's own.
func startWithSize(cmd *exec.Cmd, ws *pty.Winsize) (master *os.File, err error) {
	master, slaveName, err := openMaster()
	if err != nil {
		return nil, err
	}
	closeMaster := true
	defer func() {
		if closeMaster {
			_ = master.Close()
		}
	}()

	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0) //nolint:gosec // slaveName is this package's own openMaster
	if err != nil {
		return nil, fmt.Errorf("pty: open the slave %s: %w", slaveName, err)
	}
	defer func() { _ = slave.Close() }() // the child has its own copy after Start; the parent needs none.

	if ws != nil {
		if err := pty.Setsize(master, ws); err != nil {
			return nil, fmt.Errorf("pty: set the initial size: %w", err)
		}
	}

	if cmd.Stdin == nil {
		cmd.Stdin = slave
	}
	if cmd.Stdout == nil {
		cmd.Stdout = slave
	}
	if cmd.Stderr == nil {
		cmd.Stderr = slave
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pty: start the shell: %w", err)
	}
	closeMaster = false
	return master, nil
}

// reapAfterFailedSetup is NewLocal's own cleanup for the narrow window
// between a successful cmd.Start() (inside startWithSize) and the readiness
// dup/wake-pipe setup that follows it: a failure there returns an error
// before the exit-watcher goroutine (NewLocal, below) is ever started, so
// nothing would otherwise call cmd.Wait() and the started child becomes an
// unreaped zombie the moment it exits. Best-effort and silent: this is
// already an error path (an out-of-descriptors machine, most likely), and
// there is no caller left to report a second failure to.
func reapAfterFailedSetup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// RawReadUntilAgain reads everything currently available off the master,
// through the runtime poller rather than a blocking Read: it issues raw,
// non-blocking read(2) calls over SyscallConn().Read, handing each one's
// bytes to deliver as they arrive, until read(2) answers EAGAIN — never
// until a deadline expires, because an expired deadline is a timeout
// answered before read(2) is ever issued and would report "nothing more to
// read right now" when the true answer is "the poller never got to ask".
//
// It is this method that makes the owner's drain step (spec §5.3) a single,
// bounded call: everything readable at the moment of the call is delivered
// before it returns, and a second call on an idle fd returns (false, nil)
// at once, having issued exactly one read(2) that answered EAGAIN.
//
// deliver is called with a slice into buf and must not retain it past the
// call: buf is reused for the next chunk read within the same call.
//
// It always starts by clearing any read deadline left on the file, whether
// or not one is set — belt and braces against a deadline some other caller
// left behind; ordinary operation never sets one on lp.file's read side any
// more (nocx-6q1uh.18: WaitReadable polls a SEPARATE dup fd, below, and
// never touches this file's own deadline or its SyscallConn at all, which is
// exactly what stops it sharing internal/poll's read lock with this call in
// the first place).
func (lp *LocalPty) RawReadUntilAgain(buf []byte, deliver func([]byte)) (eof bool, err error) {
	if err := lp.file.SetReadDeadline(time.Time{}); err != nil {
		return false, err
	}
	rc, scErr := lp.file.SyscallConn()
	if scErr != nil {
		return false, scErr
	}
	var readErr error
	ctlErr := rc.Read(func(fd uintptr) bool {
		for {
			n, e := unix.Read(int(fd), buf)
			switch {
			case e == nil && n > 0:
				deliver(buf[:n])
				continue
			case e == nil && n == 0:
				// The master's read side reached EOF: the slave has no more
				// writers. Report it and stop — there is nothing further to
				// drain and nothing further to wait for.
				eof = true
				return true
			case errors.Is(e, unix.EINTR):
				continue
			case errors.Is(e, unix.EAGAIN):
				// Everything currently available has been delivered. This is
				// the ordinary, successful end of a drain — not an error.
				return true
			default:
				readErr = e
				return true
			}
		}
	})
	if ctlErr != nil {
		return eof, ctlErr
	}
	return eof, readErr
}

// WaitReadable blocks until the master is readable, cancellable by ctx, and
// reads NOTHING itself: it is the readiness half spec §5.2 splits from the
// reading half, so that only the owner's own goroutine ever issues a
// read(2) against this fd (RawReadUntilAgain, above) and no byte is ever
// consumed by the goroutine that merely noticed readiness.
//
// # Why this does not go through the master file's own SyscallConn (nocx-6q1uh.18)
//
// The previous shape did — rc.Read's callback returning false parks the
// caller in the runtime poller until the fd is readable — and it deadlocked
// the merged-tree gate for 8+ minutes: internal/poll's SyscallConn().Read
// holds the file's OWN read lock (fdMutex) for the whole call, a parked wait
// included, not only while a read(2) is actually in flight. RawReadUntilAgain
// goes through that same call on the SAME *os.File, so a goroutine idling in
// this method on an idle program held exactly the lock the owner's own drain
// needed next, and nothing was ever going to make the fd readable to release
// it: the program was silent precisely because nobody had drained the input
// that might have prompted it to answer. A prior fix (fd99a9d1) tried
// forcing the parked wait to give the lock back with a deadline-in-the-past
// handshake; it did not hold up — the handshake itself still serialised the
// two goroutines through the same lock, RawReadUntilAgain during the
// interrupt-then-wait window included, and the merged-tree gate hung again.
//
// So this method never touches lp.file at all. readyFd is a unix.Dup of the
// master's underlying fd, opened once at construction (NewLocal) and never
// read from or written to — its only use is as a SECOND, independent
// descriptor onto the SAME open file description, wrapped in nothing that
// Go's own poller keeps a lock over. unix.Poll blocks in the kernel directly
// on readyFd, exactly as poll(2) blocks on any pollable descriptor — a real
// PTY master included: it is a character device with a normal driver
// read-queue on both platforms this ships for, so poll(2) reports POLLIN
// for it the same way it does for a pipe or a socket (POSIX.1-2017 poll(),
// "Regular files shall always poll TRUE for reading"; a pty master falls
// under "any other file" whose driver defines its own poll method — the
// pty line discipline's does, on both Linux's drivers/tty/pty.c and Darwin's
// XNU pty driver, and it is what select(2)/kqueue on a pty master have
// always relied on).
//
// wakeR is the read end of a self-pipe (NewLocal, newWakePipe): the second
// fd unix.Poll waits on, so a cancelled ctx or a Close can wake a parked
// call — ctx cancellation is not itself part of the poll(2) syscall, unlike
// SyscallConn().Read's deadline mechanism, so it has to be delivered this
// way. Readable on wakeR is drained and, only if ctx is already done,
// reported as ctx's own error; otherwise it is a spurious wake (WakeReadiness
// called with no real cancellation behind it) and this returns nil —
// harmless, because the owner's next RawReadUntilAgain simply finds nothing
// and answers EAGAIN at once.
//
// # After Close, os.ErrClosed — never a nil (nocx-mrfe5)
//
// Close used to wake a parked wait with that same spurious nil and then
// close readyFd and the wake pipe. A caller doing what the helper's
// readiness loop does — look again after a nil — re-entered poll(2) on
// descriptors that were closed, or already reused by some other open. On
// Linux a closed one answers POLLNVAL at once, so the loop spun; on Darwin,
// whose poll(2) is implemented over kqueue, closing a descriptor drops its
// registration, so a wait that had entered the call before the close was
// woken by nothing at all — the program ignoring the hangup, the wake byte
// already consumed — and a helper Service.Close waited on it for ten
// minutes. So Close now sets readyClosed and wakes, the woken wait answers
// os.ErrClosed, and the descriptors are closed only once no wait is inside
// the poll (readyMu's field doc).
func (lp *LocalPty) WaitReadable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !lp.enterReadiness() {
		return os.ErrClosed
	}
	defer lp.leaveReadiness()
	fds := []unix.PollFd{
		{Fd: int32(lp.readyFd), Events: unix.POLLIN}, //nolint:gosec // an fd is never near int32's range
		{Fd: int32(lp.wakeR), Events: unix.POLLIN},   //nolint:gosec // an fd is never near int32's range
	}
	for {
		if lp.readinessClosed() {
			return os.ErrClosed
		}
		fds[0].Revents, fds[1].Revents = 0, 0
		_, err := unix.Poll(fds, -1)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if fds[1].Revents != 0 {
			// Close's byte is left in the pipe, not drained: the pipe stays
			// readable, so every wait still inside this loop sees it too.
			if lp.readinessClosed() {
				return os.ErrClosed
			}
			lp.drainWake()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return nil
		}
		if fds[0].Revents != 0 {
			// Readable, POLLHUP and POLLERR alike: any of the three means
			// RawReadUntilAgain has something to say about this fd next —
			// bytes, EOF or the read error itself — and none of them is this
			// method's to interpret.
			return nil
		}
		// Neither fd reported anything: not expected with an infinite
		// timeout, but poll(2) permits a spurious return, so loop rather
		// than assume.
	}
}

// enterReadiness admits one WaitReadable, or refuses it once Close has
// begun. An admitted wait holds the readiness descriptors open until
// leaveReadiness.
func (lp *LocalPty) enterReadiness() bool {
	lp.readyMu.Lock()
	defer lp.readyMu.Unlock()
	if lp.readyClosed {
		return false
	}
	lp.readyWaiters++
	return true
}

// leaveReadiness releases what enterReadiness admitted, closing the
// readiness descriptors if Close ran while this wait was inside the poll.
func (lp *LocalPty) leaveReadiness() {
	lp.readyMu.Lock()
	defer lp.readyMu.Unlock()
	lp.readyWaiters--
	lp.closeReadinessFdsLocked()
}

func (lp *LocalPty) readinessClosed() bool {
	lp.readyMu.Lock()
	defer lp.readyMu.Unlock()
	return lp.readyClosed
}

// closeReadinessFdsLocked closes readyFd and the wake pipe once Close has
// asked for it and no wait is left inside the poll. The caller holds readyMu.
func (lp *LocalPty) closeReadinessFdsLocked() {
	if !lp.readyClosed || lp.readyWaiters > 0 || lp.readyFdsClosed {
		return
	}
	lp.readyFdsClosed = true
	_ = unix.Close(lp.readyFd)
	_ = unix.Close(lp.wakeR)
	_ = unix.Close(lp.wakeW)
}

// closeReadiness is Close's half of the readiness machinery: latch, wake
// whatever wait is parked, and close the descriptors now if nothing is.
func (lp *LocalPty) closeReadiness() {
	lp.readyMu.Lock()
	defer lp.readyMu.Unlock()
	if lp.readyClosed {
		return
	}
	lp.readyClosed = true
	lp.writeWakeLocked()
	lp.closeReadinessFdsLocked()
}

// drainWake empties the wake pipe after a readable report on it, so the next
// WakeReadiness (a real cancellation, or another spurious one) is what makes
// it readable again rather than a byte left over from this one.
func (lp *LocalPty) drainWake() {
	var b [64]byte
	for {
		n, err := unix.Read(lp.wakeR, b[:])
		if err != nil || n < len(b) {
			return
		}
	}
}

// WakeReadiness unparks a goroutine currently blocked in WaitReadable,
// without itself deciding whether that was a real cancellation or a spurious
// nudge — WaitReadable checks ctx itself once woken (nocx-6q1uh.18). The
// owner calls this whenever it cancels readCtx, because ctx cancellation
// alone never reaches a call already inside the kernel's poll(2); Close
// calls it too, before closing the fds this method's write would otherwise
// find already gone.
//
// Always safe to call, including after Close: the write happens under
// readyMu and only while the wake pipe is still open, so it can never land on
// a descriptor number the kernel has reused. A full pipe (EAGAIN) means a
// wake is already pending: nothing further to do.
func (lp *LocalPty) WakeReadiness() {
	lp.readyMu.Lock()
	defer lp.readyMu.Unlock()
	lp.writeWakeLocked()
}

// writeWakeLocked writes WakeReadiness's byte. The caller holds readyMu.
func (lp *LocalPty) writeWakeLocked() {
	if lp.readyFdsClosed {
		return
	}
	for {
		_, err := unix.Write(lp.wakeW, []byte{0})
		switch {
		case err == nil, errors.Is(err, unix.EAGAIN):
			return
		case errors.Is(err, unix.EINTR):
			continue
		default:
			return
		}
	}
}

// InterruptWrite unblocks a Write in flight on this master, the way spec
// §5.7 asks: a write deadline set in the past on the pollable file. It never
// touches the read side, so a drain already in progress (RawReadUntilAgain)
// is unaffected, and it is always safe to call more than once — the second
// call finds nothing blocked and simply re-arms an already-expired
// deadline.
func (lp *LocalPty) InterruptWrite() error {
	return lp.file.SetWriteDeadline(time.Unix(1, 0))
}

// Shell is the binary this pty actually started, as exec resolved it: an
// absolute path whenever PATH could supply one, and the bare name otherwise.
// It is read by the composition root for the session's integration status
// (nocx-dvql) — "nocx started /bin/bash" is the one fact a user cannot infer
// and cannot act without, because which shell a session runs is the single
// biggest thing that varies between two machines running the same code.
//
// It reports the launched process, never a preference: an enhanced session
// starts bash with an rcfile whatever $SHELL says, and saying otherwise
// would send the user to fix a file the session never read.
func (lp *LocalPty) Shell() string {
	return lp.cmd.Path
}

// Dir is the directory the shell was actually started in — the RESOLVED one,
// after an empty Cwd has become the user's home the way Terminal.app and iTerm
// do it (resolveCwd above). This package owns that resolution, so a caller
// recording where a session began asks here rather than repeating the rule and
// then disagreeing with it on a GUI launch (nocx-k6p18.3's launch record).
func (lp *LocalPty) Dir() string {
	return lp.cmd.Dir
}

// Pid is the process id of the shell this pty started, or 0 when the spawn
// never produced one. It is read by the composition root so the process
// observer can be told which process to watch (nocx-cgzc) — this is the only
// place that knows it, exactly as Shell() is the only place that knows which
// binary was exec'd.
//
// The pid stays the same across an exec: a shell replaced by a wrapper is the
// same process wearing a new image, which is why watching the pid answers the
// question at all.
func (lp *LocalPty) Pid() int {
	if lp.cmd.Process == nil {
		return 0
	}
	return lp.cmd.Process.Pid
}

func (lp *LocalPty) Read(p []byte) (int, error) {
	return lp.file.Read(p)
}

func (lp *LocalPty) Write(p []byte) (int, error) {
	return lp.file.Write(p)
}

// hangupProcessGroup targets the session's process group, whose id is the
// shell pid because pty.StartWithSize starts the shell with setsid. The
// foreground command shares that group when job control is disabled.
//
// Why the group and not the pid. NOT because of nocx-pibr3, whose 600 s
// deadlock turned out to be an artifact of how the gate was launched: `nohup`
// sets SIGHUP to SIG_IGN, that disposition survives exec and Go deliberately
// preserves signals ignored at entry, so every process below it — the test
// binary, the shell, the program — ignored SIGHUP too. Measured: the leaked
// `tail -f` processes carried SigIgn 0x1 and survived SIGHUP while dying at
// once on SIGTERM. No signal target could have helped there, and an ordinary
// foreground run has never reproduced it.
//
// The reason that survives is the macOS one the block below already records
// (nocx-wwz0): there the kernel does not hang up the foreground group when
// the master closes, so a pid-only SIGHUP leaves the running program's fate
// entirely to whether the shell hangs up its own jobs on the way out. That is
// a favour bash grants and dash, a shell killed some other way, or a program
// outside the job table do not. The group is what the kernel itself signals
// on a real terminal loss, so it is what Close names, and the program's death
// stops depending on anyone's bookkeeping.
func (lp *LocalPty) hangupProcessGroup() error {
	if lp.cmd.Process == nil {
		return nil
	}
	return lp.SignalProcessGroup(lp.cmd.Process.Pid, syscall.SIGHUP)
}

func (lp *LocalPty) Close() error {
	lp.mu.Lock()
	defer lp.mu.Unlock()

	if lp.closed {
		return nil
	}
	lp.closed = true

	// SIGHUP, not SIGTERM: this is a terminal closing, and SIGHUP is the
	// signal a terminal sends when it goes away. An INTERACTIVE shell ignores
	// SIGTERM — both bash and zsh do — so the signal that was sent here was
	// never the thing that ended the session; closing the master below was,
	// via the EOF the shell reads at its prompt. bash exits on that EOF, which
	// is why nobody noticed while bash was the only local shell nocx started.
	// zsh does not: measured on macOS 15, a `zsh -l -i` on a pty whose master
	// is closed sits at its prompt indefinitely (Ss+, still alive after 67
	// seconds), because the kernel does not deliver a hangup to the foreground
	// group here the way Linux's vhangup does. So every closed tab on the
	// platform this product ships to would have leaked a shell (nocx-wwz0).
	//
	// SIGHUP ends both immediately, and it is what they are written to handle:
	// a shell that receives it saves its history, runs its exit hooks and
	// hangs up its own jobs. The master is closed afterwards, so a shell that
	// wants to write on the way out still has somewhere to write.
	_ = lp.hangupProcessGroup()

	// The master first, then the readiness machinery (nocx-mrfe5): a wait
	// that closeReadiness wakes answers os.ErrClosed, and anything its caller
	// then asks of the master finds it already closed, never a last EAGAIN.
	// readyFd is a dup, so the open file description outlives this close
	// until a wait still inside its poll has left it.
	err := lp.file.Close()
	lp.closeReadiness()
	return err
}

// Resize takes the same lock Close does, because both reach the master's file
// descriptor and one of them destroys it.
//
// pty.Setsize calls File.Fd(), which READS the descriptor os.File is closing
// under it; the race detector reported exactly that pair on CI — Setsize's Fd
// against the deferred destroy inside the read pump's File.Read, once Close
// had dropped the last reference (nocx-vqziz). A resize arriving while a
// session closes is ordinary: the lane that carries it runs off the read loop,
// so a client's last resize and the tab's close are two goroutines by design.
//
// A closed pty REFUSES rather than silently succeeding. The size a caller
// asked for was not taken, and a resize that returns nil having done nothing
// is how a terminal ends up at a grid nobody chose — the lane already says so
// out loud for the session it cannot find, and this is the same fact one layer
// down.
func (lp *LocalPty) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	if lp.closed {
		return fmt.Errorf("pty: resize after close: %w", os.ErrClosed)
	}
	return pty.Setsize(lp.file, &pty.Winsize{
		Cols: cols,
		Rows: rows,
		X:    xpixel,
		Y:    ypixel,
	})
}

func (lp *LocalPty) Done() <-chan struct{} {
	return lp.done
}

// WaitErr reports what cmd.Wait returned when the shell process ended, and
// whether it has been captured yet (nocx-ictcq). The session layer maps the
// error to an exit cause: nil or an *exec.ExitError means the shell exited on
// its own (authoritative, with a status); anything else — and a not-yet-set
// outcome, which only happens when Done was closed by a path that never let
// the process report — is a loss. Recorded before close(done), so a caller
// that has observed <-Done sees it.
func (lp *LocalPty) WaitErr() (error, bool) {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	return lp.waitErr, lp.waitSet
}
