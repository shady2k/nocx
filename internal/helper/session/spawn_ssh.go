//go:build nocx_local_ssh

package session

// The ssh spawner: the helper's second Spawner, whose process is a shell
// channel on a connection THIS helper dialed (nocx-50w7p.4, plan §4-§6).
//
// # Why the build tag, and why it is the same tag the ssh service carries
//
// A helper that can open an ssh pane links an ssh client, and the artifact
// written to a host nobody here controls must not (plan §1). So this file
// exists only in the build that passes nocx_local_ssh, exactly as
// internal/helper/sshsvc and internal/helper/sshdial do, and a build without
// it has no sshProcess, no feed and no launcher: the session service's
// SSHSpawner is nil, and `spawn-ssh` is refused by name (ErrNoSSHSpawner)
// rather than answered with a session that could never carry a byte.
//
// # What is here, and what is deliberately not
//
// Here: the process adapter (Read/Write/Resize/Signal/Done/WaitErr over a
// channel), the launch carrier and its stage-1 bootstrap — both built from
// internal/shellintegration, which is legal in the helper and verified to be
// (`go list -deps ./cmd/nocx-helper` reaches it untagged) — and the bounded
// output feed that lets the bootstrap read the far side's tokens and then
// hands the same stream, minus what the bootstrap consumed, to the session's
// pump.
//
// NOT here, and each for a named reason:
//
//   - the SHELL-INTEGRATION GENERATION the far side installs. An integrated
//     pane whose rcfile refuses still has its lifecycle channel — the shell
//     speaks the protocol on a port that exists — but the bundle that would
//     make a far host's prompt integrate is published by the coordinator's own
//     sftp path (nocx-50w7p.15), not here. Until it lands, a far host with no
//     generation execs a native login shell after naming that outcome, which
//     is the fail-open this design promises rather than a silent degrade.
//
// The lifecycle TUNNEL and the agent tool socket's REACHABILITY are here now
// (nocx-50w7p.14). Both are the helper's own forward on the connection this
// process dialed, and both are created BEFORE the shell channel: the launcher
// names them, and neither the port nor the path exists until the far side has
// been asked for a listener (sshsvc.PaneSpec). The port reaches the far shell
// in frame 2 — the frame protocol's own place for it, because the port is
// allocated by the listening side and can never be in the command — and the
// tool socket path reaches it through the launcher's environment, as the local
// spawner does for a local pane.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/bootstrapstream"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// ShellOpener is this spawner's seam on the ssh service: open one pane's
// far-side listeners, and open the interactive shell channel that follows them.
//
// It is declared here, over *sshsvc.Service, rather than in the ssh service:
// the service knows how to open a channel and a listener and nothing about
// sessions, and this file knows about sessions and nothing about dialing. The
// things they share are the ShellChannel, the PaneListeners and the one
// connection both are on.
type ShellOpener interface {
	OpenPaneListeners(ctx context.Context, spec sshsvc.PaneSpec) (*sshsvc.PaneListeners, error)
	OpenShell(ctx context.Context, spec sshsvc.ShellSpec) (*sshsvc.ShellChannel, error)
}

// sshSpawner is the SSHSpawner this build wires into the session service.
//
// It holds no tool endpoint, and that absence is a decision rather than a gap
// (nocx-50w7p.18): the socket a pane's far tool connections are forwarded into
// belongs to the coordinator that opened that pane, so it arrives on each
// SSHSpawnRequest. A value held here would have been the one read from this
// daemon's own start environment, which is a fact about whichever coordinator
// started the daemon — and the daemon's socket is keyed by the generation, so
// several coordinators ride one of them (D12).
type sshSpawner struct {
	opener ShellOpener
	log    *slog.Logger
}

// NewSSHSpawner builds the ssh spawner over this daemon's ssh service. It is
// called by the composition root in the build that has one.
func NewSSHSpawner(opener ShellOpener, logger *slog.Logger) SSHSpawner {
	if logger == nil {
		logger = slog.Default()
	}
	return &sshSpawner{opener: opener, log: logger}
}

// SpawnSSH opens one shell channel, brings the launcher and its stage-1 up on
// it, and answers the process the session service will own.
//
// The order is the whole of the failure story:
//
//  1. decide whether this session integrates, from the mode axis's own
//     predicate (profile.DesiredMode.DeliversScripts) — `raw` and an
//     unrecognised mode both answer no, and a session that does not integrate
//     gets a plain login shell rather than a launcher that would block on a
//     frame nobody sends;
//  2. build stage-1 and the carrier TOGETHER, because the command commits to
//     the digest of exactly those bytes — a command whose digest names bytes
//     nobody will send is a far side that waits out its whole budget;
//  3. open the channel, which is the first irreversible act (it dials);
//  4. start the feed and the bootstrap, and answer.
//
// Step 4 does not WAIT for the bootstrap: the session exists as soon as its
// process does, and the first byte a person can want — the prompt — is behind
// the bootstrap anyway, which is what the process's Read blocks on. A spawn
// that waited would hold the op for the whole bootstrap budget, and a
// coordinator's pane would appear fifteen seconds after it was asked for.
func (p *sshSpawner) SpawnSSH(ctx context.Context, req SSHSpawnRequest) (Process, error) {
	kind := shellintegration.ShellKind(req.Shell)
	if kind == "" {
		kind = shellintegration.ShellAuto
	}

	var (
		stage   []byte
		command string
		plan    shellintegration.BootstrapPlan
		pane    *sshsvc.PaneListeners
	)
	// integrate is the mode axis's own answer, asked of the one predicate that
	// owns it. `raw` refuses integration outright; an unrecognised mode fails
	// closed there too, so a caller cannot invent its way into a launcher.
	if profile.DesiredMode(req.Mode).DeliversScripts() {
		// THE FAR SIDE'S CARRIERS COME FIRST, and that ordering is the
		// contract rather than tidiness: the launcher names the lifecycle port
		// and the tool socket path, and neither exists until the far side has
		// been asked for a listener. A command built before them would name a
		// port nobody listens on and a path nobody answers.
		listeners, err := p.openPaneListeners(ctx, req)
		if err != nil {
			// Refused, never degraded: the caller asked for an authenticated
			// channel, and a launch that carries a port or a path nothing
			// answers is a shell that blocks until its hello budget runs out
			// (or an agent that fails for a reason that is not about it). A
			// caller that wants the pane without the channel opens it without
			// asking for one.
			return nil, err
		}
		pane = listeners

		opts := shellintegration.LaunchOptions{
			SessionID:           req.SessionID,
			Enhanced:            true,
			AgentHelperPath:     req.AgentHelperPath,
			AgentToolSocketPath: req.AgentToolSocketPath,
			// WHY THERE IS NO TOOL SURFACE, when the caller said (nocx-e2bws):
			// a code, rendered into the shell's environment so the stage can
			// name the reason to a person rather than reporting a path no
			// launch gave it.
			AgentToolsAbsent: shellintegration.AgentToolsAbsent(req.AgentToolsAbsent),
			// The bearer, on the frame-2 road the lifecycle capability takes
			// rather than in the agent env block (nocx-50w7p.16).
			AgentToolToken: req.AgentToolToken,
		}
		// The authenticated lifecycle channel's addressing. The CAPABILITY is
		// not rendered anywhere by the launcher: it reaches the far shell as
		// frame 2 (the Secret source below), and the port travels with it —
		// the transport is a loopback listener on the far side, and the frame
		// protocol's own note says why the port cannot be in the command.
		if req.Lifecycle != nil && pane != nil && pane.Lifecycle() != nil {
			opts.Lane = req.Lifecycle.Lane
			opts.Domain = req.Lifecycle.Domain
			opts.Epoch = req.Lifecycle.Epoch
			opts.Capability = req.Lifecycle.Capability
			opts.Recovery = req.Lifecycle.Recovery
			opts.LifecyclePort = pane.LifecyclePort()
		}
		built, err := shellintegration.Stage1Frame(kind, opts)
		if err != nil {
			// Fail closed and say so: no stage-1 means no carrier, and the
			// honest answer is a plain shell rather than one that blocks on a
			// frame nobody will send.
			p.log.Error("ssh pane: stage-1 could not be rendered; a plain shell will run",
				"session", req.SessionID, "shell", string(kind), "error", err)
		} else {
			opts.StageDigest = shellintegration.StageDigest(built)
			cmd, reason, ok := shellintegration.NewRemoteLauncher().StartCommand(kind, opts)
			if !ok {
				p.log.Warn("ssh pane: the launcher declined this far shell; a plain shell will run",
					"session", req.SessionID, "shell", string(kind), "reason", string(reason))
			} else {
				stage, command = built, cmd
				// Frame 2 is the ONE place a bearer travels. A pane with no
				// lifecycle channel gets no Secret source, which the frame
				// protocol answers with a NON-SECRET refusal — the far shell
				// is told no channel was opened instead of waiting for one.
				//
				// Ordered is nil, and that is the plan's own documented meaning
				// for "nothing to wait for": §6.1's barrier exists to order
				// frame 2 behind the lifecycle RECEIVER and the publish, and
				// the receiver here is a listener this process created before
				// the shell existed. The publish is not a fact this process
				// could be handed a gate for: the COORDINATOR publishes before
				// it asks for this spawn (nocx-50w7p.21, internal/app's
				// publishForPane), so by the time this plan exists the write
				// has already reached its terminal outcome and there is no
				// second fact to wait for.
				plan = shellintegration.BootstrapPlan{Stage1: built}
				if opts.Capability != "" {
					plan.Secret = shellintegration.SecretFunc(func(context.Context) ([]byte, error) {
						return shellintegration.SecretFrame(opts)
					})
				}
			}
		}
	}
	if stage == nil && pane != nil {
		// The launcher declined, so the far shell is never told about the
		// carriers and nothing will ever dial them: end them here rather than
		// hold a listener and a port on somebody else's machine for the life
		// of a session that cannot use them.
		_ = pane.Close()
		pane = nil
	}

	ch, err := p.opener.OpenShell(ctx, sshsvc.ShellSpec{
		Destination:        req.Destination,
		AcceptOnTrust:      req.AcceptOnTrust,
		HostKeyFingerprint: req.HostKeyFingerprint,
		Command:            command,
		Cols:               req.Cols,
		Rows:               req.Rows,
	})
	if err != nil {
		if pane != nil {
			_ = pane.Close()
		}
		return nil, err
	}

	proc := &sshProcess{
		ch:            ch,
		shell:         string(kind),
		feed:          newChannelFeed(),
		pane:          pane,
		done:          make(chan struct{}),
		bootstrapDone: make(chan struct{}),
		log:           p.log.With("session", req.SessionID),
	}
	// The stream is TAKEN OVER here and read from exactly one place from now
	// on: the feed the bootstrap and, afterwards, the session's pump both read
	// from. Starting it before the bootstrap is what makes a deadline able to
	// give up on a read without losing it.
	go proc.feed.pump(ch)
	if stderr := ch.Stderr(); stderr != nil {
		// A pty session's stderr is the pty itself on the far side, so this
		// carries nothing in the ordinary case; it exists because a server
		// that DOES send extended data would otherwise have it discarded by
		// x/crypto/ssh with nobody able to see that it happened.
		go proc.feed.pump(stderr)
	}
	go proc.watch()
	if stage != nil {
		go proc.bootstrap(ctx, plan)
	} else {
		close(proc.bootstrapDone)
	}
	if req.Lifecycle != nil && (pane == nil || pane.Lifecycle() == nil) {
		// The caller asked for an authenticated channel and this session has
		// none: the far shell could not be integrated (an unsupported shell, a
		// stage-1 that would not render), so no command carried the channel and
		// no bearer was minted for nobody. It is said out loud because the
		// caller's alternative reading of the same state is a hello that times
		// out twenty seconds later, one process away.
		p.log.Warn("ssh pane: the enhanced lifecycle channel was asked for and this pane could not integrate the far shell; the pane runs without it",
			"session", req.SessionID, "bead", "nocx-50w7p.14")
	}
	return proc, nil
}

// openPaneListeners asks the far side for the carriers this request needs, or
// answers nil when it needs none.
//
// A request with neither a lifecycle channel nor a far tool socket path asks
// for no listener and costs no dial: a `raw` pane, or an integrated pane whose
// coordinator runs no tool endpoint and wants no channel, is exactly today's
// session. Answering an empty set is not a failure — it is the request.
func (p *sshSpawner) openPaneListeners(ctx context.Context, req SSHSpawnRequest) (*sshsvc.PaneListeners, error) {
	spec := sshsvc.PaneSpec{
		Destination:        req.Destination,
		AcceptOnTrust:      req.AcceptOnTrust,
		HostKeyFingerprint: req.HostKeyFingerprint,
		Lifecycle:          req.Lifecycle != nil,
		ToolSocketPath:     req.AgentToolSocketPath,
		// The session this pane IS. Every connection on the far-side tool
		// socket announces it before its own bytes, which is how the
		// coordinator learns which pane a far agent arrived on
		// (nocx-50w7p.16) — the helper's listener is per pane, so this is a
		// fact of the acceptance rather than a claim the far side makes.
		Session: req.SessionID,
		// The REQUEST's endpoint, and never a value this daemon holds: the
		// far side's bytes are FOR the coordinator that opened this pane, so
		// the target is that coordinator's own socket on this machine
		// (nocx-50w7p.18). Empty is refused by name in validatePaneSpec,
		// before anything is dialed.
		ToolSocketTarget: req.AgentToolEndpoint,
	}
	if !spec.Lifecycle && spec.ToolSocketPath == "" {
		return nil, nil
	}
	return p.opener.OpenPaneListeners(ctx, spec)
}

// sshProcess is a helper host session whose process runs on the FAR side of an
// ssh connection: internal/helper/session's Process, over a shell channel.
//
// # It reports NO OS evidence, and that is a property of the type
//
// Pid is zero because there IS no pid: the shell belongs to another machine's
// namespace, and 0 is the kernel scheduler. The session's inventory therefore
// carries `observed: null` for every ssh session — "nobody could be asked" —
// and the seam that would ask is never reached (`session.entry` gates it on
// the launch being local). A session that reported a pid here would have the
// helper describe the scheduler under this session's authority.
type sshProcess struct {
	ch    *sshsvc.ShellChannel
	shell string
	feed  *channelFeed
	log   *slog.Logger

	// pane is the session's far-side listeners and, through them, the
	// lifecycle carrier: the far shell's connection to this process's own
	// loopback listener, which finished the machine's teardown when the
	// session ends. Nil for a pane that asked for none.
	pane *sshsvc.PaneListeners

	// bootstrapDone closes when the launcher's bootstrap has finished with the
	// stream, whatever the outcome. Read waits on it, which is what makes the
	// handover from the bootstrap driver to the session's pump a sequence
	// rather than a race.
	bootstrapDone chan struct{}
	done          chan struct{}

	mu      sync.Mutex
	waitErr error
	waitSet bool
}

// The assertions that make this type's obligations the COMPILER's: a Process
// the session service can own, the group signaller its signal path reaches
// for, and — since nocx-50w7p.14 — the lifecycle carrier the session service
// asks for when a session requested an authenticated channel.
//
// LifecycleProcess is implemented UNCONDITIONALLY and answers nil for a pane
// with no channel, which is not a capability advertised and withdrawn: the
// interface's question is "what is your lifecycle stream" and nil is this
// type's honest answer for a pane that has none (session.finishSpawn reads it
// exactly that way, and the session keeps no launch without a window).
var (
	_ Process               = (*sshProcess)(nil)
	_ ProcessGroupSignaller = (*sshProcess)(nil)
	_ LifecycleProcess      = (*sshProcess)(nil)
)

// Lifecycle is the far shell's end of the authenticated channel: the stream
// the session service reads the shell's frames from and writes the
// coordinator's frames to.
//
// It is the ACCEPTED connection of this pane's own remote listener, carried
// across a socketpair so a session owns one stable stream from its first
// moment; nil when this pane has no channel.
func (p *sshProcess) Lifecycle() io.ReadWriteCloser {
	if p.pane == nil {
		return nil
	}
	return p.pane.Lifecycle()
}

// errBootstrapInput is a write that arrived while the launcher's bootstrap
// still owns the far side's input. It is REFUSED, never buffered: a buffered
// keystroke is a command the user did not knowingly run, executed later, at a
// prompt they were not looking at — the coordinator's own input quarantine
// states the same rule (internal/ssh, design §5.3).
var errBootstrapInput = errors.New("session: the remote shell is still bootstrapping: input refused")

// Read hands over the far side's output — after the bootstrap has finished
// with it.
//
// The wait is not a delay imposed on the reader: during the bootstrap the far
// side emits protocol tokens on a terminal it holds in raw mode, and those
// tokens are precisely what must NOT reach a pane. The first byte anybody
// could want is the prompt, which comes after the bootstrap's outcome.
func (p *sshProcess) Read(b []byte) (int, error) {
	select {
	case <-p.bootstrapDone:
	case <-p.done:
		// The channel ended during the bootstrap. The feed still holds
		// whatever arrived, so the read below drains it and then reports the
		// end; blocking here would lose the far side's last words.
	}
	return p.feed.Read(b)
}

// Write sends bytes to the far shell's input.
func (p *sshProcess) Write(b []byte) (int, error) {
	select {
	case <-p.bootstrapDone:
	case <-p.done:
		return 0, fmt.Errorf("session: the remote shell channel ended: %w", sshsvc.ErrShellClosed)
	default:
		return 0, errBootstrapInput
	}
	// The bytes the runtime decided — a reply to a question the far program
	// asked, or the user's own input — and a failure is REPORTED rather than
	// swallowed: a terminal that cannot deliver an answer leaves the program
	// waiting forever, and the only trace of it would otherwise be a pane that
	// stopped responding.
	n, werr := p.ch.Write(b)
	if werr != nil {
		p.log.Warn("ssh pane: a write to the far side's input failed", "bytes", len(b), "error", werr)
	}
	return n, werr
}

// Resize applies one committed size to the far pty.
//
// Pixel dimensions are ignored, and that is the honest value rather than a
// placeholder: nothing on the wire between the coordinator and this helper
// carries cell metrics today, which is the same state the local pty path is in
// (ptyTerminal.Resize).
func (p *sshProcess) Resize(_ context.Context, cols, rows, _, _ uint16) error {
	return p.ch.Resize(cols, rows)
}

// Close ends the channel. The session's established end for a caller's request
// is close-session; this is the shutdown and failure path, and both reach the
// same object.
func (p *sshProcess) Close() error {
	err := p.ch.Close()
	p.feed.close(err)
	if p.pane != nil {
		// The listeners end with the session, and their end is a fact about
		// the FAR HOST: closing them cancels the port and the path there, so a
		// session that is over leaves nothing listening on somebody else's
		// machine.
		_ = p.pane.Close()
	}
	return err
}

// Done closes when the far command has ended and WaitErr has been recorded.
// The ordering is internal/pty's — status first, then the close — so observing
// Done is enough to read it.
func (p *sshProcess) Done() <-chan struct{} { return p.done }

// WaitErr is how the far command ended, and whether it has been captured.
func (p *sshProcess) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr, p.waitSet
}

// Pid is ZERO, always, and that is the answer rather than a gap: this session
// has no process on this machine. See the type's own comment.
func (p *sshProcess) Pid() int { return 0 }

// Shell is the far shell this session was launched for.
func (p *sshProcess) Shell() string { return p.shell }

// ForegroundProcessGroup is UNAVAILABLE, and it says so rather than answering
// zero. A foreground group is the far kernel's fact; the session's inventory
// does not ask for one (its evidence seaval is gated on a local process), and a
// caller that asked anyway must be told it cannot be had rather than handed a
// number that means "the shell is in the foreground" on this machine.
func (p *sshProcess) ForegroundProcessGroup() (int, error) {
	return 0, fmt.Errorf("session: a remote session has no foreground process group on this host")
}

// SignalProcessGroup sends one signal to the far shell's process.
//
// Zero means the session's own process — the channel's command — and that is
// the only reachable addressee, because the ssh protocol's signal request
// addresses the channel's command. A NON-ZERO pgid is refused by name: it is a
// number in the far kernel's namespace, this helper cannot see that kernel,
// and sending the request anyway would signal whatever the channel's command
// happens to be rather than the group the caller named (ErrRemotePgid).
func (p *sshProcess) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid > 0 {
		return fmt.Errorf("%w: pgid %d", ErrRemotePgid, pgid)
	}
	err := p.ch.Signal(sig)
	if err != nil {
		// The ssh service's own sentinel is preserved through the wrap so a
		// caller can tell "this signal has no name on the wire" from "the
		// channel refused it".
		return err
	}
	return nil
}

// watch records the far command's end once, and ends the feed so a reader that
// is parked on it wakes with the status.
func (p *sshProcess) watch() {
	<-p.ch.Done()
	err, _ := p.ch.WaitErr()
	p.mu.Lock()
	p.waitErr = err
	p.waitSet = true
	p.mu.Unlock()
	p.feed.close(err)
	close(p.done)
}

// bootstrap drives the launcher's frames to exactly one terminal outcome.
//
// Its context is deliberately NOT the spawn request's: a cancelled RPC must
// not abort a bootstrap that is already running on a live channel, and the
// only thing that should stop it is the session ending — which is what the
// watcher below cancels on. The deadline inside DeliverBootstrap is what
// bounds it otherwise (the same budgets the coordinator's own path uses).
func (p *sshProcess) bootstrap(ctx context.Context, plan shellintegration.BootstrapPlan) {
	defer close(p.bootstrapDone)
	bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	go func() {
		select {
		case <-p.done:
			cancel()
		case <-bctx.Done():
		}
	}()

	stream := &bootstrapStream{feed: p.feed, in: p.ch}
	outcome := shellintegration.DeliverBootstrap(bctx, log.NewSlogAdapter(p.log), stream, plan)
	// SAID OUT LOUD, because it is the difference between a pane whose blocks
	// will work and one that is only a terminal: the outcome names which of the
	// two the far side reached, and until this line the only sign of a failed
	// bootstrap was the absence of blocks much later. A session with no
	// lifecycle channel — a `raw` one, or an integrated one whose far shell
	// declined — reports `channel-unavailable` after a successful integration
	// and a native shell otherwise: both are answers, and neither is a failure
	// of the pane.
	p.log.Info("ssh pane: the bootstrap reached its outcome",
		"outcome", string(outcome), "integrated", outcome == shellintegration.OutcomeBootstrapAccepted)
}

// bootstrapStream is the far side as shellintegration's driver sees it: the
// lines it wrote, and a writer that bypasses the input gate.
type bootstrapStream struct {
	feed *channelFeed
	in   io.Writer
}

func (s *bootstrapStream) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	return s.feed.ReadLine(ctx, timeout)
}

func (s *bootstrapStream) Write(p []byte) (int, error) { return s.in.Write(p) }

// feedBufferLimit bounds what the feed holds while nobody is reading it.
//
// It is reached only while the bootstrap owns the stream, and the far side is
// a loader in raw mode emitting handshake tokens, so it is a safety valve
// rather than a working bound — which is why it can be generous. What it must
// not be is UNBOUNDED: a far side that floods during a bootstrap that is
// waiting out its deadline would otherwise be accumulated in the helper's
// memory, and the helper's memory is spent on somebody else's machine (D8).
//
// On overflow the pump STOPS READING rather than dropping: the channel applies
// flow control and the far side blocks, exactly as it would against a pty
// nobody is draining. Dropping the oldest bytes — the shape the coordinator's
// own output tap uses — is right there and wrong here: that tap is a copy of a
// stream the renderer also receives, while this feed is the ONLY reader, so a
// byte dropped here is a byte nobody ever sees.
const feedBufferLimit = 256 << 10

// feedLineLimit bounds one line, for the reason every other bootstrap reader
// bounds one: a binary stream has no newline in it, and a reader looking for
// one would grow without end.
const feedLineLimit = 4096

// channelFeed is the shell channel's output, read once and handed to two
// consumers in sequence: the launcher's bootstrap first, and the session's
// pump afterwards.
//
// # Why a feed and not just the pipe
//
// Because a deadline is not a property an io.Reader can have. The bootstrap
// must give up waiting for a token after a bound, and the only way to give up
// on a blocked Read is to not be the one blocked — so a pump owns the Read and
// the two consumers take turns on the buffer it fills. The alternative shape,
// letting the bootstrap read the pipe directly and then handing it over, loses
// bytes on exactly the path that matters: the read the deadline abandoned
// still arrives later, and the line it carried would be dropped.
//
// The pump is the SAME goroutine that would otherwise be the session's, which
// is why the handover is a change of consumer and not a change of reader.
type channelFeed struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error

	// sig wakes a ReadLine that is waiting for a line. It is a one-slot
	// channel rather than a condition variable because ReadLine waits with a
	// DEADLINE, and a cond.Wait cannot be told one.
	sig chan struct{}
}

func newChannelFeed() *channelFeed {
	f := &channelFeed{sig: make(chan struct{}, 1)}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// pump moves one of the channel's two output streams into the feed. It runs
// once per stream — the pty's output and its stderr, when the far side sends
// any — and it is what applies the backpressure above.
//
// It does NOT close the feed when its own stream ends, and that is deliberate:
// the end of the far command is the WAIT's fact (the channel's Done), and a
// feed closed by whichever stream happened to end first would report a live
// shell as over. What it does on a read error is nothing at all — the wait
// reports the same end a moment later, with the status attached, and that is
// the message worth logging.
func (f *channelFeed) pump(r io.Reader) {
	buf := make([]byte, pageSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f.feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// feed appends bytes, waiting for room rather than dropping any.
//
// It wakes BOTH waiters, and they are two because they wait differently: the
// session's Read parks on the condition variable (it has no deadline and no
// context), and ReadLine parks on sig (it does have one, and a cond.Wait cannot
// be told a duration). Signalling only one of them is the defect this comment
// exists for — with the condition variable left unsignalled, the session's pump
// slept until the STREAM ENDED and then took every byte at once, after the
// runtime had already been failed. A program's question was therefore never
// answered, and the only trace of it was a session that produced nothing.
func (f *channelFeed) feed(p []byte) {
	f.mu.Lock()
	for len(f.buf) >= feedBufferLimit && !f.closed {
		f.cond.Wait()
	}
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.buf = append(f.buf, p...)
	f.cond.Broadcast()
	f.mu.Unlock()
	f.wake()
}

// close ends the feed. Everything waiting on it wakes and reports the end.
func (f *channelFeed) close(err error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.err = err
	f.mu.Unlock()
	f.wake()
	f.cond.Broadcast()
}

func (f *channelFeed) wake() {
	select {
	case f.sig <- struct{}{}:
	default:
	}
}

// Read hands over buffered bytes, and blocks while there are none and the
// stream is open.
func (f *channelFeed) Read(p []byte) (int, error) {
	f.mu.Lock()
	for len(f.buf) == 0 && !f.closed {
		f.cond.Wait()
	}
	if len(f.buf) == 0 {
		err := f.err
		f.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	f.mu.Unlock()
	f.cond.Broadcast()
	return n, nil
}

// ReadLine returns the next line with its ending removed, or
// bootstrapstream.ErrDeadline when the bound passed first. A deadline consumes
// NOTHING: bytes read so far stay where the next read will find them, which is
// what makes a timeout a refusal rather than a hole in the stream.
func (f *channelFeed) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		line, ok, err := f.takeLine()
		if err != nil {
			return "", err
		}
		if ok {
			return line, nil
		}
		select {
		case <-f.sig:
		case <-deadline.C:
			return "", bootstrapstream.ErrDeadline
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// takeLine removes one complete line, or reports why there is not one yet.
func (f *channelFeed) takeLine() (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := bytes.IndexByte(f.buf, '\n'); i >= 0 {
		line := string(bytes.TrimRight(f.buf[:i], "\r"))
		f.buf = f.buf[i+1:]
		f.cond.Broadcast()
		return line, true, nil
	}
	if len(f.buf) > feedLineLimit {
		// Not this protocol. The bytes are the far side's output and they stay
		// in the buffer for the session's pump — what this refuses is the
		// WAIT, not the data.
		return "", false, fmt.Errorf("%w: %d bytes with no line ending (bound %d)",
			bootstrapstream.ErrLineTooLong, len(f.buf), feedLineLimit)
	}
	if f.closed {
		return "", false, io.EOF
	}
	return "", false, nil
}
