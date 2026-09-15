//go:build nocx_local_ssh

package sshsvc

// The INTERACTIVE-SHELL half of the `ssh` service: one session channel on a
// pooled connection, with a pty on it, handed to the caller as a stream
// (nocx-50w7p.4).
//
// # Why this is not `open`
//
// `open` (channel.go) hands the coordinator a stream keyed by a ChannelID and
// carries its bytes back over the helper protocol, because the consumer on the
// other end is the coordinator's own code (pkg/sftp, the publish). A shell
// channel is the opposite case in the one way that matters: the SESSION
// SERVICE is its consumer, in THIS process, because the process behind the
// channel is what a host session is (plan §4). So there is no ChannelID, no
// frame type and no routing — the caller gets the channel.
//
// What the two DO share is everything before that: the same destination type,
// the same single authentication method, the same host-key questions asked of
// the coordinator over the connection that asked for the channel, and the same
// pool (AD-4's one connection per host+identity, with the second channel
// multiplexing over it). That sharing is why this file lives beside channel.go
// rather than in the session package: a second dial path would be a second
// pool and a second place that decides how this helper authenticates.
//
// # The one thing it does NOT do
//
// It does not decide what to RUN. `Spec.Command` is either empty — a plain
// login shell — or the launch carrier the session package built from
// internal/shellintegration. This file never composes it and never sees a
// caller's string: the op that reaches it names a destination and a shape, and
// the command is built in the process that owns the session's semantics.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// DefaultTerm is the TERM requested on a shell channel's pty.
//
// It is the same value the coordinator's own ssh path requests
// (internal/ssh's openSessionWithPTY), and it is one constant here rather than
// a field of Spec because the terminal TYPE is not a caller's decision: the
// helper's emulator is the terminal, and it is an xterm-256color. A caller
// that could name it could ask for a terminal whose answers disagree with the
// runtime that will answer them, which is the defect ADR-0066 exists to
// prevent, one process further out.
const DefaultTerm = "xterm-256color"

// The pty modes this helper requests, matching internal/ssh's own hand-built
// request (ssh_channel.go's buildTerminalModes): ECHO on, and the two speeds
// set to the value that request has always carried. gossh's RequestPty encodes
// RFC 4254 §6.2 from exactly this map, so the encoding is the library's and the
// three values are the ones the product already speaks.
var shellPtyModes = gossh.TerminalModes{
	gossh.ECHO:          1,
	gossh.TTY_OP_ISPEED: 14400,
	gossh.TTY_OP_OSPEED: 14400,
}

// errBadShellSpec is a shell this helper will not open. It is refused before
// anything is dialed, for the reason validateProbe's errors are: a dial that
// cannot be completed would report a failure of the far side as though it were
// a statement about the request.
var errBadShellSpec = errors.New("shell spec is incomplete")

// ShellSpec is one interactive shell channel to open. Every field is either a
// resolved value or a decision the CALLER owns; nothing here is derived from
// the far host, because this helper cannot read one.
type ShellSpec struct {
	// Destination is the resolved address, account and identity, and
	// AcceptOnTrust is the caller's answer to "may a key this host has never
	// presented be recorded" — the same two fields a probe and a channel open
	// take, for the same reason.
	Destination   proto.SSHDestination
	AcceptOnTrust bool
	// HostKeyFingerprint pins the key the caller believes this host presents.
	// Empty means no expectation. When present it is enforced BEFORE the
	// coordinator is asked anything, so a handshake offering another key is
	// refused as the change it is (see pinnedHostKey).
	HostKeyFingerprint string
	// Command is the remote command, empty for a plain login shell. It is
	// built by the caller (the session package's ssh spawner) and never
	// composed here.
	Command string
	// Cols and Rows are the pty size requested at open. Zero means the
	// session service's own default has already been applied by the caller.
	Cols uint16
	Rows uint16
	// KeepaliveInterval and KeepaliveCountMax arm this channel's own pooled
	// connection prober (nocx-y6fh7 item 6), on the terms
	// ssh.PooledSpec's own fields already state. Zero disables it — the
	// caller's honest default, never this package's.
	KeepaliveInterval time.Duration
	KeepaliveCountMax int
	// OnLiveness receives every reachability change the prober reports for
	// as long as the pooled connection this channel opened (or joined) is
	// this channel's own — see acquirePooled's own note on what that means
	// under AD-4 sharing. Nil is ordinary.
	OnLiveness ssh.LivenessObserver
}

// ShellChannel is one interactive shell channel on a pooled connection, as an
// io.ReadWriteCloser with the four things a session needs of it.
//
// It owns its pooled REFERENCE: the connection stays open for as long as this
// channel lives and is released when it ends, whichever end ended it. That is
// AD-4's rule one level down — the reference is per CHANNEL, and a channel
// nobody closed keeps the transport alive, which is the caller's business and
// not a leak for this object to collect.
type ShellChannel struct {
	pool   *ssh.PooledConn
	sess   *gossh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	// exit is the far command's end. The wait runs on its own goroutine from
	// the moment the channel exists, because the alternative — waiting when
	// somebody asks — would either block a caller that only wants to know
	// whether it ended yet, or lose the status of a session nobody asked
	// about yet.
	exitMu   sync.Mutex
	exitErr  error
	exitSet  bool
	done     chan struct{}
	closeOne sync.Once
}

// ErrDetachedWriterCap is why a sibling channel's session ended when the
// helper's detached-writer cap was already spent (spec §5.7,
// nocx-6q1uh.3): its own connection was closed at once rather than at its
// last release, because ANOTHER channel on it had a write stuck behind a
// zero-sized window and the helper already held maxDetachedWriters such
// writers. wait recognises internal/ssh's ReasonDetachedWriterCap (read
// through the pooled connection's TaintReason) and wraps it with this
// sentinel so a caller can tell this apart from an ordinary transport
// failure.
var ErrDetachedWriterCap = errors.New("sshsvc: detached_writer_cap")

// ErrShellClosed is what a read or write on a channel that has ended returns.
// It is not ErrChannelClosed (that is the proxied plane's own sentinel, and
// the two are read by different callers); it is here so a session's failure
// path can tell "the stream ended" from "the helper could not reach the far
// side at all".
var ErrShellClosed = errors.New("ssh: the shell channel has ended")

// OpenShell opens one shell channel: it authenticates, acquires the pooled
// connection, opens a session channel, requests a pty, starts the command (or
// a plain login shell), and returns the stream.
//
// # The order of the two failure classes, and why it is the probe's
//
// Everything that fails BEFORE a channel exists is classified with
// classifyChannelError — the same function the proxied open uses — so an
// unreachable host, a credential the server refuses, a key this helper cannot
// use and a host key nobody recorded are the SAME facts here as there, in the
// caller's own vocabulary. Everything that fails AFTER is the far side
// declining this specific channel, and it is refused as such.
func (s *Service) OpenShell(ctx context.Context, spec ShellSpec) (*ShellChannel, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		// The re-dial case, named by the owner's plan §2: a helper with no
		// coordinator connection has nowhere to ask for the credential or the
		// host-key verdict, and the honest answer is the refusal rather than a
		// hang or a stored fallback.
		return nil, errNoAuthChannel
	}
	if err := validateShellSpec(spec); err != nil {
		return nil, err
	}

	pool, err := s.acquirePooled(ctx, conn, spec.Destination, spec.AcceptOnTrust, spec.HostKeyFingerprint,
		spec.KeepaliveInterval, spec.KeepaliveCountMax, spec.OnLiveness)
	if err != nil {
		return nil, err
	}

	sess, err := pool.Client().NewSession()
	if err != nil {
		_ = pool.Close()
		return nil, classifyChannelError(err)
	}
	// THE PIPES COME FIRST, before the command is started, and that ordering
	// is the library's requirement rather than a preference: x/crypto/ssh
	// refuses StdinPipe/StdoutPipe/StderrPipe once the session has started
	// ("…Pipe after process started"). Claiming them up front also means the
	// channel's readers exist before any byte can arrive on it.
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return nil, classifyChannelError(fmt.Errorf("stdin pipe: %w", err))
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return nil, classifyChannelError(fmt.Errorf("stdout pipe: %w", err))
	}
	// stderr is ATTACHED rather than left alone, and that is a correctness
	// point and not a nicety: x/crypto/ssh writes extended-data to io.Discard
	// when nobody claimed it, so a far side that sends anything on stderr
	// would have it silently dropped between this process and the pane. The
	// failure of the request itself is not fatal — a channel whose stderr
	// cannot be piped still carries the session — so it degrades to no stderr
	// rather than to no shell.
	stderr, serr := sess.StderrPipe()
	if serr != nil {
		s.log.Warn("ssh: the shell channel's stderr could not be piped", "error", serr)
	}

	// The terminal-then-command order is RFC 4254's: `pty-req` must be
	// answered before the shell or exec request is sent, and a server that
	// refuses the pty refuses it there rather than silently running the
	// session without one.
	if err := sess.RequestPty(DefaultTerm, int(spec.Rows), int(spec.Cols), shellPtyModes); err != nil {
		_ = sess.Close()
		_ = pool.Close()
		return nil, classifyChannelError(fmt.Errorf("pty-req: %w", err))
	}
	if spec.Command == "" {
		if err := sess.Shell(); err != nil {
			_ = sess.Close()
			_ = pool.Close()
			return nil, classifyChannelError(fmt.Errorf("shell: %w", err))
		}
	} else if err := sess.Start(spec.Command); err != nil {
		// A refused exec is THIS channel's failure and only this channel's:
		// the coordinator's own path answers it with a replacement channel
		// running a plain shell (internal/ssh's recoverFromRefusedExec),
		// which is a product decision about a pane's prompt and belongs to
		// the layer that owns the pane's semantics rather than here. What
		// this side must not do is report it as a lost connection, which
		// would send somebody to look at the host.
		_ = sess.Close()
		_ = pool.Close()
		return nil, &proto.Refusal{Code: proto.ErrCodeChannelRefused, Message: err.Error()}
	}

	ch := &ShellChannel{
		pool: pool, sess: sess,
		stdin: stdin, stdout: stdout, stderr: stderr,
		done: make(chan struct{}),
	}
	go ch.wait()
	s.log.Info("ssh: shell channel opened",
		"host", spec.Destination.Host, "port", spec.Destination.Port,
		"user", spec.Destination.User, "command", spec.Command != "",
		"cols", spec.Cols, "rows", spec.Rows)
	return ch, nil
}

// validateShellSpec refuses a shell that cannot be opened before anything is
// dialed. It mirrors validateDestination field for field, for the same reason
// that one mirrors validateProbe: a difference between two validators for one
// question would be two answers to it.
func validateShellSpec(spec ShellSpec) error {
	switch {
	case spec.Destination.Host == "":
		return fmt.Errorf("%w: no host", errBadShellSpec)
	case spec.Destination.Port <= 0 || spec.Destination.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadShellSpec, spec.Destination.Port)
	case spec.Destination.User == "":
		return fmt.Errorf("%w: no user", errBadShellSpec)
	}
	return validateIdentity(spec.Destination.Identity)
}

// pinnedHostKey enforces the caller's own expectation about a host key, in
// front of the coordinator's verdict.
//
// # Why both, and why this one runs first
//
// The verdict answers "is this key the one RECORDED for this host", and the
// coordinator is the only party that can answer it — it holds known_hosts and
// the person's accept decision. The pinned fingerprint answers a different
// question: "is this the key the CALLER told me to expect", a value that
// travelled with the request on a different connection. A host that changed
// between the caller's own check and this dial satisfies the second question
// and not the first, and refusing on the pinned value is what keeps the two
// from having to be trusted as one.
//
// It returns internal/ssh's own mismatch type on purpose: classifyChannelError
// rebuilds the coordinator's typed error from it, so the caller sees
// `host-key-changed` with the two fingerprints — the same answer a changed key
// from known_hosts produces, because from the caller's side it is the same
// fact.
func pinnedHostKey(inner gossh.HostKeyCallback, want string) gossh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		if got := gossh.FingerprintSHA256(key); got != want {
			return &ssh.ErrHostKeyMismatch{
				Addr:           hostname,
				KnownHostsAddr: hostname,
				KeyAlgo:        key.Type(),
				Fingerprint:    got,
				Expected:       want,
				Key:            key.Marshal(),
			}
		}
		return inner(hostname, remote, key)
	}
}

// Read hands over the channel's output bytes.
func (c *ShellChannel) Read(p []byte) (int, error) { return c.stdout.Read(p) }

// Stderr is the channel's standard-error stream, or nil when the far side's
// server would not pipe one. It is a READER rather than a merged stream
// because the caller decides where it goes — the session's feed merges it into
// the same window the pty's output travels in, which is what a terminal does.
func (c *ShellChannel) Stderr() io.Reader { return c.stderr }

// Write sends bytes to the far shell's input.
func (c *ShellChannel) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// Resize sends RFC 4254 §6.7's window-change request. Pixel dimensions are not
// carried: nothing on the helper protocol has cell metrics today, which is the
// same state the local pty path is in (session/ptyTerminal.Resize).
func (c *ShellChannel) Resize(cols, rows uint16) error {
	if err := c.sess.WindowChange(int(rows), int(cols)); err != nil {
		return fmt.Errorf("ssh: window-change: %w", err)
	}
	return nil
}

// Signal sends one signal to the far shell's process, which is what a session
// signalling "its own group" means for a channel.
//
// It takes no pgid, and that is the design rather than a simplification: a
// process group number is the FAR kernel's namespace, this helper cannot see
// that kernel, and a request naming one could not be honoured or refused
// honestly. The ssh protocol's signal request addresses the channel's command
// and nothing else, which is exactly the reach this session has.
func (c *ShellChannel) Signal(sig syscall.Signal) error {
	name, ok := sshSignalName(sig)
	if !ok {
		// Refused by name rather than sent as something else. RFC 4254 §6.9
		// carries the signal as a NAME without its SIG prefix, so a number
		// this table does not know has no spelling on this wire — and the
		// alternative, sending a name the far side would interpret as a
		// different signal, is a stop ladder that kills the wrong thing.
		return fmt.Errorf("%w: signal %d has no name on this wire", ErrShellSignal, int(sig))
	}
	if err := c.sess.Signal(gossh.Signal(name)); err != nil {
		return fmt.Errorf("ssh: signal %s: %w", name, err)
	}
	return nil
}

// ErrShellSignal is a signal this helper cannot name for the far side. It is
// its own sentinel because the two callers (a stop ladder and a session's own
// close) act on it differently from a transport failure: the first asks for a
// different signal, the second has nothing to retry.
var ErrShellSignal = errors.New("ssh: this helper cannot name that signal")

// sshSignalName spells a POSIX signal the way RFC 4254 §6.9 carries one —
// the name without its SIG prefix.
//
// The set is the signals this product actually sends: the stop ladder's three
// (INT, TERM, KILL) and the four job-control signals a person's keyboard
// produces (HUP for a dropped connection's hangup, QUIT, and the STOP/CONT
// pair). It is a TABLE rather than a derivation because the wire has names and
// the kernel has numbers, and the mapping between them is a fixed list on both
// sides rather than arithmetic.
var sshSignalNames = map[syscall.Signal]string{
	syscall.SIGHUP:   "HUP",
	syscall.SIGINT:   "INT",
	syscall.SIGQUIT:  "QUIT",
	syscall.SIGKILL:  "KILL",
	syscall.SIGTERM:  "TERM",
	syscall.SIGSTOP:  "STOP",
	syscall.SIGCONT:  "CONT",
	syscall.SIGTSTP:  "TSTP",
	syscall.SIGUSR1:  "USR1",
	syscall.SIGUSR2:  "USR2",
	syscall.SIGWINCH: "WINCH",
}

func sshSignalName(sig syscall.Signal) (string, bool) {
	name, ok := sshSignalNames[sig]
	return name, ok
}

// Taint marks this channel's pooled connection so the pool hands it to no
// new channel again (spec §5.7's first detach, nocx-6q1uh.3): existing
// siblings — including this one — run to their own end, and it closes when
// the last of them releases, or at once past the helper's detached-writer
// cap (internal/ssh's ConnPool.Taint).
func (c *ShellChannel) Taint() {
	c.pool.Taint()
}

// Done closes when the far command has ended and WaitErr has been recorded.
// The ordering is internal/pty's — status first, then the close — so observing
// Done is enough to read it.
func (c *ShellChannel) Done() <-chan struct{} { return c.done }

// WaitErr is how the far command ended, and whether that has been captured
// yet.
func (c *ShellChannel) WaitErr() (error, bool) {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitErr, c.exitSet
}

// wait records the far command's end once.
//
// A sibling whose OWN write never stuck can still have its channel ended by
// another channel's detach exceeding the helper's cap (spec §5.7): its
// connection closes out from under it, sess.Wait returns whatever the
// mux teardown produces, and this is where that gets renamed to the reason
// it actually was, read back from the pooled connection every handle
// sharing it can see.
func (c *ShellChannel) wait() {
	err := c.sess.Wait()
	if c.pool.TaintReason() == ssh.ReasonDetachedWriterCap {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrDetachedWriterCap, err)
		} else {
			err = fmt.Errorf("%w: the connection closed clean but under the cap", ErrDetachedWriterCap)
		}
	}
	c.exitMu.Lock()
	c.exitErr = shellExit(err)
	c.exitSet = true
	c.exitMu.Unlock()
	close(c.done)
}

// Close ends this channel and releases the pooled reference it holds. It is
// idempotent: a session's stop and a close racing each other must not release
// the same reference twice, which would close a connection another channel is
// still using.
func (c *ShellChannel) Close() error {
	var err error
	c.closeOne.Do(func() {
		err = c.sess.Close()
		_ = c.pool.Close()
	})
	return err
}

// ShellExit is what a shell channel's wait returned, in the vocabulary the
// session layer already consumes: an error carrying an exit CODE.
//
// It exists because x/crypto/ssh spells its status differently —
// *gossh.ExitError answers ExitStatus() and Signal() (a NAME), while every
// consumer in this repository reads ExitCode() int, which is os/exec's
// spelling and the one client.ExitStatus mirrors — and translating it here
// keeps x/crypto's types inside the two packages allowed to see them.
//
// A channel that ended without a status (the connection died, the server
// closed it, we closed it) is code -1, which is the same value
// SessionExitStatus documents for "killed by a signal or no status could be
// collected". The distinction that matters to a reader — did the far command
// say how it ended, or did the stream simply stop — is carried by the error
// being a *gossh.ExitError or not, and the signal NAME is dropped rather than
// translated into a number: a name-to-number table would be this package
// inventing a far kernel's numbering.
type ShellExit struct {
	// Err is what the wait returned, nil for a clean zero exit.
	Err error
}

func (e *ShellExit) Error() string {
	if e.Err == nil {
		return "ssh: the shell channel exited 0"
	}
	return e.Err.Error()
}

// Unwrap keeps the library's error reachable for a caller that wants to know
// WHY the channel ended.
func (e *ShellExit) Unwrap() error { return e.Err }

// ExitCode is the far command's status, or -1 when the stream ended without
// one.
func (e *ShellExit) ExitCode() int {
	var exit *gossh.ExitError
	if errors.As(e.Err, &exit) {
		return exit.ExitStatus()
	}
	if e.Err == nil {
		return 0
	}
	return -1
}

// shellExit wraps a wait result in the shape the session layer reads. A nil
// error stays a non-nil *ShellExit with code 0: "the command exited zero" and
// "the wait has not returned" must not be the same value.
func shellExit(err error) error { return &ShellExit{Err: err} }
