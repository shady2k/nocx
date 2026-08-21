package ssh

import (
	"context"
	"encoding/binary"
	"io"
	"sync"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// RealChannel implements the Channel interface over an SSH session.
type RealChannel struct {
	log     log.Logger
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	done    chan struct{}

	// shellIntegrationReason is why shell integration did not happen,
	// decided by openShell when the session started (nocx-r52q). ReasonNone
	// means integration succeeded or was never attempted.
	//
	// It is guarded because one row of design §6.4 can only be named LATER:
	// an exec request that was ACCEPTED and substituted is indistinguishable
	// from an accepted one until the loader fails to announce itself, which
	// is after the channel has been handed out. The reason is set once more
	// at that point, and the transport reads it from another goroutine.
	reasonMu               sync.Mutex
	shellIntegrationReason RefusalReason

	// waitMu guards waitErr/waitSet: the watcher records the outcome of
	// session.Wait before closing done, but an explicit Close may close done
	// first, and the exit monitor may then read the fields while the
	// watcher is still writing them. waitSet is what makes "not yet
	// recorded" distinguishable from "recorded as nil" (nocx-ictcq).
	waitMu  sync.Mutex
	waitErr error
	waitSet bool

	closeOnce sync.Once
	closeCb   func()
	// lifecycleClose releases the session's authenticated lifecycle
	// channel (ADR-0024 decision 2 "Over SSH"): its tunnel lease and the
	// remote listener. Closed exactly once, from Close, after closeOnce
	// fires; nil for channels without a channel (plain shells, non-ssh
	// transports). The lifecycle channel is established by Connect and
	// transferred here on the shell-open path; every path that opens a
	// plain shell closes it instead.
	lifecycleClose func()
	// inputGate is the bootstrap's input quarantine (design §5.3). Closed
	// for the whole bootstrap interval and opened at its one terminal
	// outcome; nil-safe, and open from the start for a session that runs no
	// bootstrap at all.
	inputGate *inputGate
	// bootstrapDone is closed when the bootstrap has finished with the
	// output stream. Read waits on it, which is what makes the handover
	// from the bootstrap driver to the terminal a sequence rather than a
	// race — and what publishes the driver's leftover bytes to it.
	bootstrapDone chan struct{}
	// releasePoolRef drops this channel's reference to the pooled ssh.Client.
	// Set by RealClient.Connect; invoked once from Close (after closeOnce
	// fires) so the connection closes when the last referencing tab closes,
	// including the jump transport (AD-4). Nil for non-pooled channels.
	releasePoolRef func()
}

// lifecycleHandle carries an established remote lifecycle channel from
// Connect to the RealChannel that owns it: the launch config the launcher
// substitutes into the start command, and the closer that releases the
// tunnel lease and ends the domain when the session ends. close is
// idempotent: the shell-open path and RealChannel.Close both call it, and
// whichever runs first wins.
type lifecycleHandle struct {
	launch RemoteLifecycleLaunch
	closer io.Closer
	once   sync.Once
}

func (h *lifecycleHandle) close() {
	if h == nil || h.closer == nil {
		return
	}
	h.once.Do(func() {
		_ = h.closer.Close()
	})
}

// Read hands the session's output to the terminal — after the bootstrap has
// finished with it.
//
// The wait is not a delay imposed on the user: during the bootstrap the far
// side emits nothing but protocol tokens, on a terminal it holds in raw mode
// with echo off, and those tokens are precisely what must NOT reach a pane.
// The first byte a user could want is the prompt, which comes after the
// terminal outcome.
func (c *RealChannel) Read(p []byte) (int, error) {
	if c.bootstrapDone != nil {
		select {
		case <-c.bootstrapDone:
		case <-c.done:
			return 0, &ErrDisconnected{}
		}
	}
	return c.stdout.Read(p)
}

// Write can block indefinitely, and that is deliberate. gossh's stdin pipe
// carries no deadline, so bounding it here would mean a goroutine and a
// timer per keystroke — and a write that "timed out" is not cancelled, it
// is merely abandoned, free to land after the frames that followed it and
// reorder the user's input. The blocking is contained instead of hidden:
// only the session's own write loop waits here (nocx-o2le), so a channel
// that has stopped accepting bytes costs that one tab, and a transport
// that is actually dead is closed by the keepalive prober, which is the
// component whose job that is.
//
// The done check is the one cheap thing worth doing: a channel the watcher
// has already seen exit says so instead of writing into a dead pipe.
func (c *RealChannel) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, &ErrDisconnected{}
	default:
	}
	// The input quarantine (design §5.3). One write is one decision, taken
	// under the gate's lock, so a keystroke arriving as the bootstrap ends
	// is either refused or delivered exactly once — never both, never
	// neither. Refused, never buffered: a buffered keystroke is a command
	// the user did not knowingly run, executed later.
	//
	// Resize and the other PTY control requests are NOT user bytes and do
	// not pass through here at all, which is why they keep working.
	if !c.inputGate.admit() {
		return 0, &ErrInputQuarantined{}
	}
	return c.stdin.Write(p)
}

func (c *RealChannel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.closeCb != nil {
			c.closeCb()
		}
		if c.lifecycleClose != nil {
			c.lifecycleClose()
		}
		if c.releasePoolRef != nil {
			c.releasePoolRef()
		}
	})
	return nil
}

// recordWait captures what session.Wait returned when the remote session
// ended, and is called by the watcher BEFORE Close closes done, so a reader
// that has observed <-Done sees the record (nocx-ictcq). It is first-wins
// under the mutex: an explicit Close can close done first and the watcher
// records afterwards, and the first record is the only truth worth keeping.
func (c *RealChannel) recordWait(err error) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if c.waitSet {
		return
	}
	c.waitErr = err
	c.waitSet = true
}

// WaitErr reports what session.Wait returned, and whether it has been
// captured. The session layer maps the error to an exit cause: nil or an
// *ssh.ExitError means the remote shell exited on its own (authoritative,
// with a status); anything else — and a not-yet-set outcome, which happens
// when the channel was closed before the watcher recorded — is a loss.
func (c *RealChannel) WaitErr() (error, bool) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr, c.waitSet
}

func (c *RealChannel) Done() <-chan struct{} {
	return c.done
}

func (c *RealChannel) ShellIntegrationReason() RefusalReason {
	c.reasonMu.Lock()
	defer c.reasonMu.Unlock()
	return c.shellIntegrationReason
}

// Resize sends a window-change request to the remote end.
//
// It checks the channel's done signal first: after disconnect (AD-7), Resize
// returns *ErrDisconnected immediately instead of blocking on the now-dead
// transport. The context is checked before the request and observed during
// the SendRequest call via a goroutine watchdog — if ctx cancels while
// SendRequest blocks (e.g. on a congested transport), Resize returns
// ctx.Err() promptly. The goroutine is drain-safe because the buffered
// channel guarantees the send always succeeds.
func (c *RealChannel) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	select {
	case <-c.done:
		return &ErrDisconnected{}
	default:
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	wcMsg := ptyWindowChangeMsg{
		Columns: uint32(cols),
		Rows:    uint32(rows),
		Width:   uint32(xpixel),
		Height:  uint32(ypixel),
	}

	type result struct {
		err error
	}
	ch := make(chan result, 1)
	go func() {
		_, err := c.session.SendRequest("window-change", false, gossh.Marshal(&wcMsg))
		ch <- result{err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return &ErrDisconnected{}
	case r := <-ch:
		return r.err
	}
}

// ---------------------------------------------------------------------------
// Wire-format message types for SSH protocol requests.
// ---------------------------------------------------------------------------

// ptyReqMsg matches RFC 4254 §6.2.
type ptyReqMsg struct {
	Term     string
	Columns  uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

// ptyWindowChangeMsg matches RFC 4254 §6.7.
type ptyWindowChangeMsg struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

// buildTerminalModes returns the SSH-encoded terminal modes string.
func buildTerminalModes() string {
	buf := make([]byte, 0, 64)
	for _, m := range []struct {
		opcode byte
		value  uint32
	}{
		{gossh.ECHO, 1},
		{gossh.TTY_OP_ISPEED, 14400},
		{gossh.TTY_OP_OSPEED, 14400},
	} {
		buf = append(buf, m.opcode)
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, m.value)
		buf = append(buf, b...)
	}
	buf = append(buf, 0) // TTY_OP_END
	return string(buf)
}

// setShellIntegrationReason records a refusal decided after the channel was
// built. Only design §6.4's substituted-exec row reaches it: everything else
// is known before the session is handed out.
func (c *RealChannel) setShellIntegrationReason(r RefusalReason) {
	c.reasonMu.Lock()
	defer c.reasonMu.Unlock()
	c.shellIntegrationReason = r
}
