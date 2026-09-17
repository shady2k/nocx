//go:build nocx_local_ssh

package ssh

// This file is the DIAL half of what used to be one ssh_real.go, and the build
// tag is the whole reason it is a file of its own.
//
// The repository builds the ssh client twice — once into the artifact this
// machine's helper runs and once into nothing at all — and only a build tag
// tells the two apart (the coordinator's half is ssh_real.go; the invariant is
// plan 2026-09-13 §1, §3: every ssh connection is made by the LOCAL helper).
// What is here is the part that makes a connection: the pool AD-4 requires,
// the resolve-then-authorize-then-acquire path a tab took, the interactive
// shell channel with its pty and its bootstrap, and the recovery from a server
// that refuses the exec request. What is NOT here is the resolution and the
// host-key DECISION both halves need, which stayed in ssh_real.go because a
// build without a client still answers for them.
//
// The tag is `nocx_local_ssh`, passed by `make helper-local` alone: `make
// helpers` builds the four deployable targets without it, and cmd/nocx-server
// is never built with it. So the copy of this file that ships to a host nobody
// here controls is the one that was not compiled.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	gossh "golang.org/x/crypto/ssh"
	agent "golang.org/x/crypto/ssh/agent"
)

// probeHostKeyCallback returns a HostKeyCallback that captures the observed
// host key fingerprint on every call (success or failure). The capture is set
// before the callback returns, so reading *capture after dialDirect returns
// gives the fingerprint that was presented by the server — even when the key
// was rejected (unknown host key, key mismatch). For unreachable hosts the
// callback is never invoked and *capture is empty.
//
// The closure captures a stack-allocated string by reference; escape analysis
// promotes it to the heap. The returned func owns the only reference, so
// *capture is safe to read after the dial completes and before the closure is
// collected.
func (rc *RealClient) probeHostKeyCallback(knownHostsAddr string) (gossh.HostKeyCallback, *string, error) {
	var captured string
	cb, err := rc.hostKeyCallbackFor(knownHostsAddr)
	if err != nil {
		return nil, nil, err
	}
	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		captured = gossh.FingerprintSHA256(key)
		return cb(addr, remote, key)
	}, &captured, nil
}

// acquiredPooled bundles the result of resolving and acquiring a pooled
// connection: the raw client, the pooled wrapper (for per-connection
// features like agent forwarding), the handle owning this reference, and the
// caller's effective config. Connect and TunnelConn share this path so a
// forward is authorized and keyed exactly like a tab (AD-4) — a tunnel to a
// host the credential is not bound to is the same authorization violation as
// a tab to it.
type acquiredPooled struct {
	handle   *poolHandle
	pconn    *pooledSSHConn
	client   *gossh.Client
	resolved *resolvedConfig
	cfg      *ConnectConfig
}

// acquirePooled resolves the connection config, enforces credential
// authorization, and acquires a pooled connection. On success the caller
// owns the returned handle and MUST release it exactly once.
//
// Authorization is enforced BEFORE any dial. Only a linked credential
// (Secrets != nil) carries an authorized endpoint to check; inline auth has
// no stored secret to redirect.
//
// Resolve the authorized endpoint through ~/.ssh/config separately from the
// dial target. The resolver stores the canonical (resolved) hostname, and
// this pass re-resolves it through the current SSH config. If ~/.ssh/config
// changed since the resolver ran (drift), the two resolutions yield different
// results and the check fails. For binding tests that bypass the resolver,
// this pass also resolves aliases set directly as AuthorizedEndpoint.
//
// The host parameter and cfg.AuthorizedEndpoint are separate inputs, so
// comparing their resolved forms is not self-authorizing.
func (rc *RealClient) acquirePooled(ctx context.Context, host string, opts []ConnectOption) (*acquiredPooled, error) {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve config for %s: %w", host, err)
	}
	if cfg.Secrets != nil {
		resolvedAuthz := rc.resolveAuthzEndpoint(ctx, cfg.AuthorizedEndpoint)
		if authErr := checkAuthorization(resolvedAuthz, resolved, string(cfg.SecretID), false); authErr != nil {
			return nil, authErr
		}
	}
	key := rc.poolKeyFor(ctx, resolved, cfg)
	handle, err := rc.dial.pool.AcquireDial(ctx, key, rc.dialForConnect(ctx, host, resolved, cfg))
	if err != nil {
		return nil, err
	}

	pconn, ok := handle.conn.(*pooledSSHConn)
	if !ok {
		// dialForConnect always returns a *pooledSSHConn; a different type
		// means the pool's dial factory was overridden (tests). Release and bail.
		rc.dial.pool.Release(handle)
		return nil, fmt.Errorf("internal: pooled connection is not a *pooledSSHConn (%T)", handle.conn)
	}
	gclient, ok := pconn.client.(*gossh.Client)
	if !ok {
		rc.dial.pool.Release(handle)
		return nil, fmt.Errorf("internal: pooled client is not *gossh.Client (%T)", pconn.client)
	}
	return &acquiredPooled{handle: handle, pconn: pconn, client: gclient, resolved: resolved, cfg: cfg}, nil
}

// Connect implements SSH.Connect.
func (rc *RealClient) Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}

	// Agent forwarding: register the per-connection channel handler once
	// (initAgentForward is guarded by agentForwardOnce), then request it
	// per-session inside openShell. Fail early if the user asked for
	// forwarding but no agent is reachable.
	if acq.cfg.AgentForward {
		if !rc.agentAvailable() {
			rc.dial.pool.Release(acq.handle)
			return nil, fmt.Errorf("agent forwarding requested but no SSH agent available (SSH_AUTH_SOCK not set)")
		}
		if fwdErr := acq.pconn.initAgentForward(acq.client, os.Getenv("SSH_AUTH_SOCK")); fwdErr != nil {
			rc.dial.pool.Release(acq.handle)
			return nil, fmt.Errorf("agent-forward setup: %w", fwdErr)
		}
	}
	// The authenticated lifecycle channel (ADR-0024 decision 2 "Over SSH"):
	// establish it before the start command is built, so the allocated
	// loopback port and the per-epoch capability are substituted into the
	// launch text. Refusal — the remote sshd will not forward — is
	// detectable synchronously and NOT distinguishable; the session then
	// opens as a conventional terminal with a visible native prompt, and
	// no diagnostic names a policy. Established only for enhanced sessions
	// with a launcher: without the channel the shell keeps its native
	// prompt (ADR-0024 decision 9).
	//
	// The desired mode is part of the gate for the same reason it gates
	// shellStartCommand (nocx-tr2n): raw and helper integrate nothing, so
	// their start command is a plain shell that will never dial the
	// forwarded port. Establishing anyway allocated a remote listener and
	// a domain that openShell closed unused on the next statement — free
	// while no ssh session was ever enhanced, and a round trip and a
	// remote listener per raw tab now that every session asks.
	var lc *lifecycleHandle
	if acq.cfg.Enhanced && modeAllowsIntegration(acq.cfg.DesiredMode) &&
		acq.cfg.RemoteLifecycle != nil && acq.cfg.RemoteLauncher != nil {
		launch, closer, lerr := acq.cfg.RemoteLifecycle.Establish(ctx, host, opts...)
		if lerr != nil {
			rc.log.Warn("ssh: lifecycle channel refused; session stays conventional",
				"host", host, "error", lerr)
		} else {
			lc = &lifecycleHandle{launch: launch, closer: closer}
		}
	}

	ch, err := rc.openShell(ctx, acq.pconn, acq.resolved, acq.cfg, publishTarget{host: host, opts: opts}, func() { rc.dial.pool.Release(acq.handle) }, lc, acq.pconn.fingerprint)
	if err != nil {
		// Failed to open the shell — release our reference so the
		// connection can close if we were the only tab. Without this the
		// failed Connect path leaks a pooled ref (and a jump transport)
		// for the process life.
		lc.close()
		rc.dial.pool.Release(acq.handle)
		return nil, err
	}

	// openShell wired the channel's close to release our pool reference.
	// RealChannel.Close runs closeCb exactly once (sync.Once), so the handle
	// is released exactly once even if the session errors and the tab then
	// closes. Releasing the handle drops the target refcount; when it hits
	// zero the pooledSSHConn closes the gossh.Client AND releases the jump
	// handle, which closes the bastion when its own refcount hits zero. One
	// Close per channel, one Release per handle, no leak.
	return ch, nil
}

// Close implements SSH.Close. It closes every pooled connection regardless
// of refcount — used during shutdown. Ordinary tab closure releases a
// single handle via the channel's closeCb and never reaches here.
func (rc *RealClient) Close() error {
	rc.dial.pool.CloseAll()
	return nil
}

func (rc *RealClient) shellStartCommand(ctx context.Context, resolved *resolvedConfig, cfg *ConnectConfig, pub publishTarget, lc *lifecycleHandle) (string, RefusalReason, BootstrapRun) {
	if resolved.remoteCommand != "" {
		return resolved.remoteCommand, ReasonRemoteCommand, nil
	}

	// raw publishes nothing and integrates nothing (N1, §3.1). helper is
	// NOT inert: the tiers are additive, so allowing the deployed binary
	// never withholds the scripts (§5.2, nocx-7k8ma). Unknown modes fail
	// closed.
	if !modeAllowsIntegration(cfg.DesiredMode) {
		return "", ReasonNone, nil
	}

	// No launcher wired: the publish still runs — it is what puts a
	// generation on the far host — and the session opens a plain shell. The
	// installer supplies no command any more; a session either runs the
	// carrier or runs nothing at all.
	if cfg.RemoteLauncher == nil {
		rc.startPublish(ctx, pub, resolved, cfg, nil)
		return "", ReasonNone, nil
	}

	shell := cfg.Shell
	if shell == "" {
		shell = ShellAuto
	}
	opts := LaunchOptions{
		SessionID: cfg.SessionID,
		Enhanced:  cfg.Enhanced,
	}
	// The lifecycle channel config addresses the carrier: lane, domain and
	// epoch are NAMES and travel in the command, while the PORT — allocated
	// by the listening side and unknowable here — travels in frame 2
	// (carrier.go: "the lifecycle port is allocated by the proven master and
	// travels in frame 2 instead"). The capability and the recovery fence are
	// carried so the launcher can prove it does not put them anywhere: both
	// reach the far shell as a frame on the channel, never as command text.
	// lc is nil when establishment was refused; the launch then carries no
	// channel config and the shell stays conventional.
	if lc != nil {
		opts.Lane = lc.launch.Lane
		opts.Domain = lc.launch.Domain
		opts.Epoch = lc.launch.Epoch
		opts.LifecyclePort = lc.launch.Port
		opts.Capability = lc.launch.Capability
		opts.Recovery = lc.launch.Recovery
	}
	// The bootstrap is prepared first: the carrier commits to the
	// digest of the stage-1 frame, so the frame exists before the
	// command that names it does. Without a bootstrapper there is
	// nothing to feed the loader, and a loader with no sender blocks
	// on a frame that never arrives — so the session runs a plain
	// shell instead, with a named reason.
	digest, run, gate, prepared := cfg.RemoteLauncher.Prepare(shell, opts)
	if !prepared {
		rc.log.Warn("ssh: the shell bootstrap could not be prepared; the session runs a plain shell",
			"host", resolved.hostName, "shell", shell)
		// The publish still runs. It is what puts a generation on the
		// far host for the NEXT connection, and a session that cannot
		// bootstrap today is exactly the one that most needs the next
		// one to work.
		rc.startPublish(ctx, pub, resolved, cfg, nil)
		return "", ReasonUnsupportedShell, nil
	}
	opts.StageDigest = digest

	// §6.1 step 2: the publish and the loader start CONCURRENTLY, and
	// §7 requires it — 3 + 3 + 10 is 16 against a 15 s integration
	// deadline, so a sequential publish cannot close. The publish runs
	// on its own auxiliary channel from here while the loader takes the
	// terminal and quarantines input; the mint waits for both through
	// the gate, and the longest path is 13 s rather than 16 — asserted on
	// the schedule graph rather than on a stopwatch, by
	// shellintegration's TestBootstrapSchedule_* assertions.
	//
	// Concurrent with the loader, and never with the CLAIM on the user's
	// session channel: openShell has already opened it, so this auxiliary
	// channel competes with nothing the user needs. See openShell.
	//
	// Step 4 is answered synchronously, because it already is: the
	// transport was established before the session was opened, so by
	// here the receiver either exists or never will.
	rc.startPublish(ctx, pub, resolved, cfg, gate)
	if lc != nil {
		gate.ReceiverReady()
	} else {
		gate.ReceiverUnavailable(errors.New("ssh: no lifecycle channel for this session"))
	}
	cmd, reason, ok := cfg.RemoteLauncher.StartCommand(shell, opts)
	if ok && cmd != "" {
		// The bound, at the point of no return (nocx-e4ir3). A launcher is
		// a seam somebody else implements next, and the producer that put
		// 92 KiB and two bearers in a command was policing itself against
		// a cap beside it. Refused here, the command is never sent and the
		// session still fails open to a plain login shell — the reason is
		// named so the degrade is visible in the product, not only in a log.
		if len(cmd) >= MaxRemoteCommandLen {
			rc.log.Warn("ssh: the remote command is longer than the bound; the session runs a plain shell",
				"bytes", len(cmd), "bound", MaxRemoteCommandLen, "shell", string(shell))
			return "", ReasonCommandTooLong, nil
		}
		return cmd, ReasonNone, run
	}
	// Decline or degenerate result: fall back to a plain shell. Normalize
	// a missing reason so the degrade stays visible in the product
	// (AGENTS.md: a soft degrade must never be log-only). The lifecycle
	// channel is not used by a plain shell; openShell closes lc.
	if reason == "" {
		reason = ReasonUnsupportedShell
	}
	return "", reason, nil
}

// publishTarget is where a publish goes, named the way the caller named it:
// the address it dialed and the options it resolved with.
//
// It is the WHOLE of what the publish needs to name the same destination the
// pane did, and it is deliberately not a `*gossh.Client`: the publish no longer
// rides this connection. It rides this machine's helper's — a home probe and an
// sftp channel on the pooled connection AD-4 keys by (host, port, user,
// identity) — and the helper is handed the same triple the pane's open hands it
// (nocx-50w7p.15). Passing a different spelling of one destination would be a
// different pool key, and therefore a second authentication for one machine.
type publishTarget struct {
	host string
	opts []ConnectOption
}

// startPublish runs the bundle publish on its own schedule and tells the gate
// when it has settled (design §6.1 step 5, §7).
//
// It used to run inline, ahead of everything, and that is what made the
// deadline arithmetic fail to close: the publish is bounded at T = 10 s and
// the receiver and the frame at 3 s each, so a sequential schedule costs 16 s
// against a 15 s deadline. Concurrent, the longest path is the publish plus
// the terminal outcome after frame 2 — 13 s.
//
// Its result stays a LOG and never an input to which command is emitted: the
// fail-open contract says a failed publish leaves the previous activation
// byte-identical and the session still starts, and the carrier is what starts
// it either way. What the result DOES decide is one thing — whether the gate
// opens with an error to name, so that a far side reporting
// generation-unavailable can be told that the reason there is no generation is
// that nocx could not write one.
//
// The context is deliberately not the caller's: Connect returns as soon as the
// session is started, and a cancelled connect context must not abort a publish
// that is already writing to the far host. The bound is the publisher's own T,
// which it enforces against its own clock; adding a second timer here would be
// a second, unsynchronised deadline for one budget.
func (rc *RealClient) startPublish(ctx context.Context, pub publishTarget, resolved *resolvedConfig, cfg *ConnectConfig, gate BootstrapGate) {
	if cfg.RemoteInstaller == nil {
		// A terminal outcome all the same, and it must be: a gate waiting
		// for a fact nobody will ever supply is a session that never leaves
		// `starting`, which §7 forbids outright.
		if gate != nil {
			gate.PublishSettled(nil)
		}
		return
	}
	pctx := context.WithoutCancel(ctx)
	go func() {
		err := rc.publishBundle(pctx, pub, resolved, cfg)
		if gate != nil {
			gate.PublishSettled(err)
		}
	}()
}

// publishBundle is the publish itself, separated so startPublish is about the
// schedule and this is about the work.
//
// The HOME QUESTION is no longer asked here, and that is the whole of
// nocx-50w7p.15's defect: it used to be asked over this connection's exec
// channel, and the answer a session's shell activates with is not the one
// every other path into the account reports. It is asked by the party that
// owns the carrier now, on the destination's pooled connection, and the
// account's own `$HOME` is what the bundle is written under.
func (rc *RealClient) publishBundle(ctx context.Context, pub publishTarget, resolved *resolvedConfig, cfg *ConnectConfig) error {
	if err := cfg.RemoteInstaller.EnsureInstalledRemote(ctx, pub.host, pub.opts...); err != nil {
		rc.log.Warn("ssh: shell integration publish failed",
			"host", resolved.hostName, "error", err)
		return err
	}
	return nil
}

// openShell opens a session, requests a PTY, optionally requests agent
// forwarding, and starts a shell. releaseRef drops the caller's pooled
// reference when the remote session ends; it is assigned to the channel
// BEFORE the session watcher starts, so the watcher can never observe the
// field unset (a race the old assign-after-openShell shape had: a session
// dying in the window left Close reading a nil callback or racing the
// write). lc is the established lifecycle channel, or nil (refused or not
// wired); it is closed on every path that does not hand the shell a
// channel-using start command, and otherwise transferred to the channel.
//
// # The user's session channel is claimed FIRST, and that ordering is the
// product's promise rather than the scheduler's
//
// The session channel is opened before shellStartCommand runs, and that
// ordering was BOUGHT: the publish used to open an auxiliary channel on this
// same connection, a server bounds the sessions it grants one connection
// (OpenSSH's MaxSessions, and a subsystem counts), and with one slot the two
// competed for it — in opposite directions. Whichever asked first won, and
// when the publish won, gclient.NewSession() for the INTERACTIVE session was
// refused and Connect returned an error, so the user got no terminal at all.
// Measured before this line moved: 4 of 10 attempts reached the working
// un-integrated prompt §0 promises and 6 reached nothing.
//
// ADR-0004 makes an ordinary usable terminal with a visible native prompt the
// one thing no failure path may suppress, and losing it to nocx's OWN
// auxiliary work is the single way to lose it that is nocx's fault. The typed
// path never had the defect — there the user's own `ssh` is the interactive
// session and it authenticates before nocx interposes — and one spelling of
// the rule is better than two (AD-8), so this is the saved path saying the
// same thing: the user's session exists before any channel of ours does.
//
// # Since nocx-50w7p.15 the claim stands, and the race it answered is gone
//
// The publish no longer opens anything here: it is this machine's helper's, on
// the helper's own pooled connection (pub is the destination, not a client).
// So this connection now carries exactly ONE session channel — the user's —
// and the ordering above is no longer load-bearing for it; it is kept because
// the assert it encodes is the product's promise and is cheaper to keep than
// to re-derive, and because the pane itself moves onto the helper under
// nocx-50w7p.5, where the same bound will be about the helper's connection.
//
// It does NOT make the publish sequential with the loader, and it must not:
// design §6.1 step 2 and §7's arithmetic need those concurrent (3 + 3 + 10 is
// 16 against a 15 s deadline; only the concurrent schedule closes at 13).
// Opening the session CHANNEL is not finishing the bootstrap — the loader has
// not been sent, the frames have not started, nothing has been minted. What
// happens between these two statements is one round trip for a channel and its
// pty, and the publish runs beside the loader on a connection of its own.
func (rc *RealClient) openShell(ctx context.Context, pconn *pooledSSHConn, resolved *resolvedConfig, cfg *ConnectConfig, pub publishTarget, releaseRef func(), lc *lifecycleHandle, hostKeyFingerprint string) (*RealChannel, error) {
	sess, err := rc.openSessionWithPTY(pconn, resolved, cfg)
	if err != nil {
		lc.close()
		return nil, err
	}
	session, stdin, stdout := sess.session, sess.stdin, sess.stdout

	startCmd, reason, bootstrap := rc.shellStartCommand(ctx, resolved, cfg, pub, lc)

	// A start command that does not use the lifecycle channel — the
	// launcher declined, the destination ran a configured remote command,
	// or no integration happened at all — leaves the channel unclaimed: a
	// plain shell (or a configured command) never connects to the
	// forwarded port, and holding the lease open would keep the pooled
	// connection (and its remote listener) alive for the process life.
	// Close it here, before the shell starts.
	if startCmd == "" || reason == ReasonRemoteCommand || !modeAllowsIntegration(cfg.DesiredMode) {
		lc.close()
	}

	// execAccepted records the REQUEST RESULT, which is what §6.4 says to
	// branch on. It is the discriminator for the sixth row: an accepted
	// request whose loader never announces itself is a substituted command,
	// not a refused one, and the two leave the user in opposite places.
	execAccepted := false
	if startCmd != "" {
		if startErr := session.Start(startCmd); startErr != nil {
			// §6.4 amendment A. The exec request was refused; whether the
			// channel survived it is a property of the server, so it is
			// OBSERVED rather than assumed — a shell on the same channel,
			// and a replacement session channel on the same connection if
			// that channel is gone. Neither costs a second authentication.
			recovered, rerr := rc.recoverFromRefusedExec(pconn, resolved, cfg, session, startErr)
			if rerr != nil {
				lc.close()
				_ = session.Close()
				return nil, rerr
			}
			session, stdin, stdout = recovered.session, recovered.stdin, recovered.stdout
			// What is on the far side now is a NATIVE login shell: our
			// loader never ran, so there is nothing to send frames to and
			// no channel for it to use. Sending them anyway would type them
			// into the user's shell.
			reason, bootstrap = ReasonExecRefused, nil
			lc.close()
		} else {
			execAccepted = true
		}
	} else {
		if err := session.Shell(); err != nil {
			lc.close()
			_ = session.Close()
			return nil, fmt.Errorf("shell: %w", err)
		}
	}

	// The input quarantine opens BEFORE the command is sent (design §5.3):
	// the session is bootstrapping and the user's keystrokes are refused,
	// not buffered — a buffered keystroke is a command the user did not
	// knowingly run, executed later. It closes at exactly one terminal
	// outcome, never at READY.
	//
	// The output side is a feed rather than the raw pipe for the same
	// interval: the bootstrap reads the far side's tokens, and the same
	// reader hands the remainder to the terminal afterwards, so no byte is
	// consumed by a wait that gave up.
	var out io.Reader = stdout
	gate := newInputGate(true)
	bootstrapDone := make(chan struct{})
	var feed *sessionFeed
	if bootstrap != nil {
		feed = newSessionFeed(stdout)
		out = feed
		gate = newInputGate(false)
	} else {
		close(bootstrapDone)
	}

	ch := &RealChannel{
		log:                    rc.log.With("remote", resolved.hostName),
		session:                session,
		stdin:                  stdin,
		stdout:                 out,
		done:                   make(chan struct{}),
		inputGate:              gate,
		bootstrapDone:          bootstrapDone,
		shellIntegrationReason: reason,
		hostKeyFingerprint:     hostKeyFingerprint,
		closeCb: func() {
			_ = session.Close()
		},
		releasePoolRef: releaseRef,
		lifecycleClose: lc.close,
	}

	if bootstrap != nil {
		// The context is deliberately NOT the caller's: Connect returns
		// as soon as the session is started, and a cancelled connect
		// context must not abort a bootstrap that is already running on
		// a live session. The session's own end is what stops it, which
		// the stream reports as an error.
		bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		go func() {
			<-ch.done
			cancel()
		}()
		go func() {
			defer cancel()
			// Whatever the outcome, the gate opens: the interval
			// closes at the TERMINAL OUTCOME, and every one of them
			// leaves the far side on its way to a usable prompt.
			bootReason := bootstrap(bctx, bootstrapStream{sessionFeed: feed, w: stdin})
			if bootReason != ReasonNone {
				// §6.4's sixth row, named at the only moment it is
				// observable. The exec request was ACCEPTED, so the far
				// side agreed to run our loader; the loader never spoke
				// and the channel is already gone. Something else ran on
				// it, it reported its status, and no native prompt exists
				// on any channel of that connection — which is why this is
				// session-failed and not a refusal that a prompt survives.
				//
				// It does not claim to diagnose the server's
				// configuration, and it cannot: a far side that died
				// before the loader spoke reaches the same state. What it
				// names is the OUTCOME, which is the same either way — the
				// user has no prompt here.
				//
				// "The channel is already gone" is asked through
				// farSideEnded rather than of the feed alone: the far
				// side's end reaches this goroutine down two unordered
				// chains, and reading only the pump's reported the row
				// as ReasonUnknown whenever the watcher's chain won a
				// race the pump had not already finished.
				_, remoteSessionEnded := ch.WaitErr()
				if execAccepted && farSideEnded(feed, remoteSessionEnded) {
					bootReason = ReasonExecSubstituted
				}
				ch.setShellIntegrationReason(bootReason)
				ch.log.Warn("ssh: shell bootstrap did not integrate the session",
					"reason", bootReason)
				// THE HARD INVALIDATION of design §5.3's validity
				// interval, named where it does its work.
				//
				// The capability's validity opens at minting and is
				// closed hard by backend invalidation — a bootstrap
				// refusal or timeout among them — after which a frame
				// of that epoch is rejected. This is that close, and
				// it is what bounds the one exposure §6.1 admits it
				// cannot remove: a forged STAGE_READY that outruns an
				// honest refusal produces a bearer, and this is the
				// event that kills it. Nothing else on this path did
				// it before — the transport stayed live until the
				// session ended, so a refusal left a valid epoch
				// behind it for as long as the tab was open.
				//
				// It is also the ONLY thing that moves a refused
				// remote session out of `starting`: with no domain
				// ever established the kernel publishes nothing, so
				// without this the axis would wait for a fact that is
				// never coming (§7: `starting` can never be
				// permanent).
				//
				// Closing the handle is the whole invalidation: it
				// ends the domain through the kernel's TransportLost,
				// releases the tunnel lease and removes the remote
				// listener. It is idempotent, so the channel's own
				// Close still runs exactly once.
				lc.close()
			}
			gate.release()
			close(bootstrapDone)
		}()
	}

	// The watcher starts AFTER releasePoolRef is set (above), so a session
	// that ends during the assignment window cannot race the field.
	go func() {
		waitErr := session.Wait()
		// Record BEFORE Close: the exit monitor wakes on done (which Close
		// closes) and reads WaitErr to classify how the session ended — the
		// remote shell's own exit (authoritative, with a status, via nil or
		// *ExitError) versus a loss (nocx-ictcq). Close is idempotent
		// (closeOnce), so if the tab already called Close this is a no-op;
		// if not, it closes the session, drops the ref, and (for a
		// jump-backed conn) releases the bastion handle.
		ch.recordWait(waitErr)
		_ = ch.Close()
	}()

	return ch, nil
}

// ptySession is one session channel with a pty and its pipes: everything
// needed to carry an interactive shell.
type ptySession struct {
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// openSessionWithPTY opens a session channel on an EXISTING connection,
// requests a pty and (when asked) agent forwarding, and returns its pipes.
//
// It is a function rather than four inline statements because §6.4's fourth
// row needs it twice: a server that tears the channel down as it refuses the
// exec request leaves the CONNECTION intact, and a replacement channel on it
// reaches a prompt at the cost of a second session and no second
// authentication.
//
// # The session is opened through the CONNECTION, not through its client
//
// pconn.newSession rather than gclient.NewSession, and the difference is not
// cosmetic: this connection is shared (AD-4), so the channel it is about to ask
// for may not be the first one it has carried, and a far side that bounds the
// sessions it grants one connection refuses the second when the first has not
// been released yet (nocx-xn63t.4.10 — measured on the live-sshd fixture, and
// the reason the ordering this function's caller relies on was bought in the
// first place). internal/ssh's pool owns that rule for every session this
// repository opens on a pooled connection, so no caller may spell it a second
// time and get it wrong.
func (rc *RealClient) openSessionWithPTY(pconn *pooledSSHConn, resolved *resolvedConfig, cfg *ConnectConfig) (ptySession, error) {
	session, err := pconn.newSession()
	if err != nil {
		return ptySession{}, fmt.Errorf("new session: %w", err)
	}
	ptyReq := ptyReqMsg{
		Term:     "xterm-256color",
		Columns:  uint32(resolved.cols),
		Rows:     uint32(resolved.rows),
		Width:    uint32(resolved.xpixel),
		Height:   uint32(resolved.ypixel),
		Modelist: buildTerminalModes(),
	}
	if _, err = session.SendRequest("pty-req", true, gossh.Marshal(&ptyReq)); err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("pty-req: %w", err)
	}
	if cfg.AgentForward {
		// Per-connection handler already registered in Connect (initAgentForward).
		// Per-session: request agent forwarding on this session so the remote
		// side can open auth-agent@openssh.com channels. agent.RequestAgentForwarding
		// uses wantReply=true, so a server refusal surfaces as an error.
		if reqErr := agent.RequestAgentForwarding(session); reqErr != nil {
			_ = session.Close()
			return ptySession{}, fmt.Errorf("agent-forward request: %w", reqErr)
		}
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("stdout pipe: %w", err)
	}
	return ptySession{session: session, stdin: stdin, stdout: stdout}, nil
}

// recoverFromRefusedExec is §6.4's third and fourth rows, in code.
//
// The refusal is CONDITIONAL and the condition is observable at the moment it
// matters, so nothing here has to guess: a request refused with the channel
// intact leaves a pty already granted on it and a `shell` succeeds there; a
// request refused with the channel torn down does not, and the answer is a
// replacement session channel on the SAME connection. Neither opens a
// connection and neither costs a second authentication — the whole point of
// the row, and the difference between conventional(exec-refused) and
// session-failed(exec-refused).
//
// The recovery is attempted in that order rather than branched on the error
// value on purpose. The discriminator §6.4 names — (false, nil) against
// (false, io.EOF) — is the client's view of the REQUEST, and gossh's
// Session.Start folds the first into an error of its own while passing the
// second through; asking the channel whether it still works answers the same
// question at the seam an implementer actually holds, and it never reads an
// error's text.
func (rc *RealClient) recoverFromRefusedExec(pconn *pooledSSHConn, resolved *resolvedConfig, cfg *ConnectConfig, refused *gossh.Session, startErr error) (ptySession, error) {
	rc.log.Warn("ssh: the server refused the exec request; recovering to a native prompt without a second authentication",
		"host", resolved.hostName, "error", startErr)

	// The channel may have survived the refusal with its pty. A Start whose
	// exec request was refused leaves the Session unstarted, so a Shell on
	// that same Session is a Shell on that same channel.
	stdin, inErr := refused.StdinPipe()
	stdout, outErr := refused.StdoutPipe()
	if inErr == nil && outErr == nil {
		if shellErr := refused.Shell(); shellErr == nil {
			rc.log.Info("ssh: the refused exec left the channel usable; the session is a native prompt on it",
				"host", resolved.hostName, "reason", string(ReasonExecRefused))
			return ptySession{session: refused, stdin: stdin, stdout: stdout}, nil
		}
	}

	// It did not. A replacement channel on the same connection is the whole
	// of the recovery — never a new connection, never a second
	// authentication.
	_ = refused.Close()
	replacement, err := rc.openSessionWithPTY(pconn, resolved, cfg)
	if err != nil {
		return ptySession{}, fmt.Errorf("shell start: %s: the exec request was refused and no replacement session channel could be opened on the same connection: %w",
			ReasonExecRefused, err)
	}
	if shellErr := replacement.session.Shell(); shellErr != nil {
		_ = replacement.session.Close()
		return ptySession{}, fmt.Errorf("shell start: %s: the exec request was refused and the replacement session channel reached no prompt: %w",
			ReasonExecRefused, shellErr)
	}
	rc.log.Info("ssh: the refused exec took the channel; a replacement session channel on the same connection reached a native prompt",
		"host", resolved.hostName, "reason", string(ReasonExecRefused))
	return replacement, nil
}

// isAuthError returns true if the error likely comes from a failed SSH authentication.
func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "ssh: handshake failed") ||
		strings.Contains(msg, "no common algorithms")
}
