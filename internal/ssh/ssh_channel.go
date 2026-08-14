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
	shellIntegrationReason RefusalReason
	// hostKeyFingerprint is the SHA256 fingerprint of the target host's
	// public key, as presented and verified when the connection was dialed
	// (the consent design keys consent by it). Empty for channels not
	// created from a dial that captured it (stubs, tests).
	hostKeyFingerprint string

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

func (c *RealChannel) Read(p []byte) (int, error) {
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

func (c *RealChannel) Done() <-chan struct{} {
	return c.done
}

func (c *RealChannel) ShellIntegrationReason() RefusalReason {
	return c.shellIntegrationReason
}

// HostKeyFingerprint returns the target host's public-key fingerprint
// observed at dial time — the consent key (consent design §3.2). Empty
// when the channel was not created from a capturing dial.
func (c *RealChannel) HostKeyFingerprint() string { return c.hostKeyFingerprint }

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
