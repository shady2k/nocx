package app

// Child-domain bootstrap composition (nocx-u7uh.11): the composition-root
// half of the domain_request/domain_grant flow (docs/lifecycle-protocol.md
// §9). The kernel validates the request and answers a grant echo; this file
// mints the child (through the publisher's kernel.RequestDomain — the
// kernel stays the sole minter of capabilities), picks the child's
// transport, and composes the opaque, already-substituted bootstrap the
// parent shell executes verbatim.
//
// Delivery is per environment, exactly as the protocol doc §9 records:
//
//   - sudo/su (same machine): the bootstrap is the child's bash rcfile; the
//     parent stages it into a preserved descriptor and launches
//     `sudo --preserve-fds=3,N -i env -u BASH_ENV bash --rcfile /dev/fd/N
//     -i` — ADR-0024's own preferred answer (recorded in its open-questions
//     section): the per-epoch capability never enters a filesystem object.
//     su has no --preserve-fds flag, so its launch
//     (`su -l -c 'env -u BASH_ENV bash --rcfile /dev/fd/N -i'`) rests on
//     the descriptor surviving su's own exec. Verified 2026-08-09
//     (nocx-u7uh.30): util-linux su (v2.42.2, su-common.c run_shell),
//     shadow su (4.19.4, execve_shell — the binary measured on this host,
//     which preserved fd 7 through the exact launcher line) and BSD/macOS
//     su (FreeBSD lineage) all end in a plain exec with no fd sweep — but
//     none promises preservation in a man page. The fallback when one does
//     not: the child starts conventional, never establishes, and the parent
//     stillborn-activates (§9) — asserted by the fd-closed su test. The
//     full reasoning lives at the launcher site in nocx.bash/nocx.zsh.
//   - ssh: the bootstrap is the user's OWN `ssh` invocation with two
//     multiplex options added — ADR-0035, "the channel we own is the
//     carrier" — carrying the child's forwarded lifecycle port as a -R
//     reverse forward on that same connection and, as its remote command,
//     the bounded loader. Nothing of variable size and no secret travels in
//     the line: the bundle is published over an auxiliary channel of the
//     master, and stage-1 and the secret travel as frames on the pty the
//     parent shell is already using. typed_line.go is that delivery.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// transportKind describes how a lifecycle transport's domains reach the
// kernel, so the grant builder can compose the child's launch: a local
// adapter's domains ride the inherited descriptor (fd 3); a remote
// adapter's ride the forwarded loopback port; a listener transport's ride a
// loopback TCP listener (the ssh child's -R endpoint).
type transportKind struct {
	local bool // domains ride the inherited descriptor (fd 3)
	port  int  // remote: the forwarded loopback port; listener: the local listener port
}

type transportRegistry struct {
	mu    sync.Mutex
	kinds map[lifecycle.TransportID]transportKind
}

func newTransportRegistry() *transportRegistry {
	return &transportRegistry{kinds: make(map[lifecycle.TransportID]transportKind)}
}

func (r *transportRegistry) register(t lifecycle.TransportID, k transportKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[t] = k
}

func (r *transportRegistry) lookup(t lifecycle.TransportID) (transportKind, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.kinds[t]
	return k, ok
}

// sessionRegistry maps a lifecycle lane to the session that owns it, so the
// grant builder can anchor the child's bootstrap (NOCX_SESSION_ID, AD-7) to
// the same session the parent reports into. It is fed by the same
// registerLane closure that binds lanes to the transport's session registry.
type sessionRegistry struct {
	mu     sync.Mutex
	byLane map[lifecycle.LaneID]string
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{byLane: make(map[lifecycle.LaneID]string)}
}

func (r *sessionRegistry) register(lane lifecycle.LaneID, sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byLane[lane] = sid
}

func (r *sessionRegistry) lookup(lane lifecycle.LaneID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sid, ok := r.byLane[lane]
	return sid, ok
}

// newChildGrantBuilder wires the child-domain bootstrap builder behind the
// domain_grant outbound. It is the single owner of "how do we reach a
// host" (ADR-0022): the composition root decides the transport and the
// launch, and the shell never parses the bootstrap.
//
// pub is a LAZY accessor, not the publisher: the composition root builds
// the publisher around this builder (app.go declares the variable and
// assigns it only after lifecyclepub.New evaluates the WithGrantBuilder
// option), so a captured *Publisher value would be nil for the lifetime of
// the closure — the first real domain_request would dereference nil
// (nocx-u7uh.29). The accessor resolves the variable at grant time.
func newChildGrantBuilder(lg log.Logger, pub func() *lifecyclepub.Publisher, transports *transportRegistry, sessions *sessionRegistry, typed *typedRunner) lifecyclepub.GrantBuilder {
	return func(req lifecyclepub.GrantRequest) (boot lifecyclepub.GrantBootstrap, err error) {
		// Every outcome is logged, refusals loudest. A refusal here is
		// invisible by construction — the publisher answers it with an
		// empty-bootstrap echo, so the parent silently runs its command
		// conventionally — and the comment on that path claimed "the
		// builder's log line carries the reason" while no builder path
		// logged anything at all. Two separate defects then had to be
		// diagnosed by instrumenting the binary by hand (nocx-beib).
		defer func() {
			switch {
			case err != nil:
				lg.Warn("child domain refused; the command runs conventionally",
					"env", req.Env, "host", req.Host, "lane", req.Lane, "error", err)
			default:
				// The options are named, not just counted: they are the
				// difference between the command the user asked for and the
				// one that runs, and when they went missing the only visible
				// symptom was an ssh sitting at a host-key prompt the user
				// had passed an option to suppress (nocx-c6z0). The launcher
				// command is deliberately not logged — it carries the
				// per-epoch capability.
				lg.Info("child domain granted",
					"env", req.Env, "host", req.Host, "lane", req.Lane,
					"domain", boot.Domain, "epoch", boot.Epoch,
					"opts", req.Opts, "bootstrapBytes", len(boot.Bootstrap))
			}
		}()

		p := pub()
		parent, ok := p.Domain(req.Parent)
		if !ok {
			return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: unknown parent %s", req.Parent)
		}
		kind, ok := transports.lookup(parent.Transport)
		if !ok {
			return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: transport %s has no recorded kind", parent.Transport)
		}
		switch req.Env {
		case lifecycle.EnvSudo, lifecycle.EnvSu:
			return buildLocalChildBootstrap(p, sessions, req, parent.Transport, kind)
		case lifecycle.EnvSSH:
			return buildSSHChildBootstrap(lg, p, sessions, req, kind, typed)
		default:
			return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: unsupported environment %q", req.Env)
		}
	}
}

// buildLocalChildBootstrap composes the sudo/su child: the child's bash
// rcfile, minted on the parent's own transport (locally the child inherits
// the preserved descriptor fd 3; remotely it connects to the parent's
// forwarded port). The rcfile is the opaque bootstrap the parent stages
// into the preserved fd; its final line closes the descriptor once bash has
// read it, so the per-epoch capability it carries cannot be re-read by a
// descendant.
func buildLocalChildBootstrap(pub *lifecyclepub.Publisher, sessions *sessionRegistry, req lifecyclepub.GrantRequest, parentTransport lifecycle.TransportID, kind transportKind) (lifecyclepub.GrantBootstrap, error) {
	sid, ok := sessions.lookup(req.Lane)
	if !ok {
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: no session registered for lane %s", req.Lane)
	}
	h, err := pub.RequestDomain(req.Lane, &req.Parent, parentTransport)
	if err != nil {
		return lifecyclepub.GrantBootstrap{}, err
	}
	opts := shellintegration.LaunchOptions{
		SessionID:  sid,
		Enhanced:   true,
		Capability: hex.EncodeToString(h.Capability[:]),
		Recovery:   hex.EncodeToString(h.Recovery[:]),
		Lane:       string(req.Lane),
		Domain:     string(h.Domain),
		Epoch:      h.Epoch,
	}
	if kind.local {
		opts.LifecycleFD = 3 // the inherited socketpair descriptor
	} else {
		opts.LifecyclePort = kind.port
	}
	rc, err := shellintegration.LocalBashRcfile(opts)
	if err != nil {
		return lifecyclepub.GrantBootstrap{}, err
	}
	// The child reads the rcfile from the preserved bootstrap descriptor
	// (sudo --preserve-fds=3,N ... --rcfile /dev/fd/N, ADR-0024's preferred
	// answer: the per-epoch capability never enters a filesystem object).
	// The descriptor NUMBER is chosen by the parent at launch from the free
	// single-digit range (4-9, the POSIX-sh guarantee — a busy user fd is
	// never clobbered), so the rcfile closes the descriptor it was READ
	// FROM — BASH_SOURCE[0] is /dev/fd/N inside the rcfile — once bash has
	// finished with it: its contents must not stay reachable to the child's
	// descendants. The eval is bash-3.2-safe (no {var} close in 3.2); the
	// suffix is validated by the fd the shell itself opened.
	rc += "\neval \"exec ${BASH_SOURCE[0]##*/}<&-\" 2>/dev/null\n"
	return lifecyclepub.GrantBootstrap{Domain: h.Domain, Epoch: h.Epoch, Bootstrap: rc}, nil
}

// buildSSHChildBootstrap composes the ssh child: the user's own `ssh`
// invocation with our two multiplex options added, a loopback listener
// transport (the local endpoint of the child's -R reverse forward), the child
// minted on it, and the bounded loader as the remote command.
//
// It is the typed path's entry point, and the order in it is the order design
// §4.4 and §6.1 fix. The WRAPPER DECIDES FIRST, before anything is minted or
// opened: a configured RemoteCommand, a multiplex policy the user expressed,
// a control socket path that cannot be built safely — each of those runs the
// user's own line with no nocx effect at all, and the line returned IS their
// line, because an empty bootstrap on this path is a command the parent shell
// would eval to nothing.
//
// Everything after the decision is local: a listener, a mint, a stage-1 frame
// and a command under 1 KiB. Nothing reaches the far host until the multiplex
// handshake has proven ownership of that specific socket, which happens in
// typed_line.go after the user has finished authenticating to their own
// client.
func buildSSHChildBootstrap(lg log.Logger, pub *lifecyclepub.Publisher, sessions *sessionRegistry, req lifecyclepub.GrantRequest, parentKind transportKind, typed *typedRunner) (lifecyclepub.GrantBootstrap, error) {
	if !parentKind.local {
		// A remote parent runs ssh on the far host: the -R forward would
		// terminate at that host, not at this backend's listener, and the
		// multiplex socket would be created on the far machine where this
		// backend cannot reach it. The mechanism does not preclude it —
		// the far host's own remote adapter listener is the natural
		// endpoint — but it is not built in this bead. Refuse honestly:
		// the parent runs its command conventionally.
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: ssh nested inside a remote parent is not implemented")
	}
	sid, ok := sessions.lookup(req.Lane)
	if !ok {
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: no session registered for lane %s", req.Lane)
	}
	inv := ssh.TypedInvocation{Opts: req.Opts, Host: req.Host, User: req.User, Port: req.Port}

	if typed == nil {
		// Not wired. Say so loudly and hand back the user's own line: a
		// missing composition is a bug in this file's caller, and the user
		// still gets the ssh they asked for.
		lg.Error("child domain: the typed-ssh delivery is not wired; the line runs as plain ssh",
			"host", req.Host)
		return lifecyclepub.GrantBootstrap{Bootstrap: composeSSHLine(ssh.TypedWrap{}, nil, inv, "")}, nil
	}

	// §4.4, decided before anything happens.
	wrap, reason, accepted := typed.wrapper.Wrap(context.Background(), inv)
	if !accepted {
		lg.Info("child domain: nocx does not interpose on this line; it runs as plain ssh",
			"host", req.Host, "user", req.User, "port", req.Port, "reason", string(reason))
		return lifecyclepub.GrantBootstrap{Bootstrap: composeSSHLine(ssh.TypedWrap{}, nil, inv, "")}, nil
	}

	ln, err := lifecyclechannel.NewListener(lg, pub)
	if err != nil {
		return lifecyclepub.GrantBootstrap{}, err
	}
	h, err := pub.RequestDomain(req.Lane, &req.Parent, ln.TransportID())
	if err != nil {
		_ = ln.Close()
		return lifecyclepub.GrantBootstrap{}, err
	}
	remotePort, err := randomPort()
	if err != nil {
		_ = ln.Close()
		return lifecyclepub.GrantBootstrap{}, err
	}

	// The far shell is brought up by the SAME launcher the profile path uses
	// (AD-8: one owner for "what command makes a remote shell integrated"),
	// and it is now the bounded carrier for both — the pre-carrier
	// self-installing launcher, which carried the whole bundle and both
	// secrets in its text, is gone from this path and from the repository
	// with it.
	//
	// The two halves are minted together or not at all: the command commits
	// to the digest of the stage-1 frame, so a command whose digest names
	// bytes nobody will send is a far side blocking on a frame that never
	// arrives.
	opts := shellintegration.LaunchOptions{
		SessionID:     sid,
		Enhanced:      true,
		Lane:          string(req.Lane),
		Domain:        string(h.Domain),
		Epoch:         h.Epoch,
		LifecyclePort: remotePort,
		Capability:    hex.EncodeToString(h.Capability[:]),
		Recovery:      hex.EncodeToString(h.Recovery[:]),
	}
	stage, err := shellintegration.Stage1Frame(shellintegration.ShellAuto, opts)
	if err != nil {
		_ = ln.Close()
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: stage-1 could not be rendered: %w", err)
	}
	opts.StageDigest = shellintegration.StageDigest(stage)
	carrier, creason, cok := shellintegration.NewRemoteLauncher().StartCommand(shellintegration.ShellAuto, opts)
	if !cok || carrier == "" {
		_ = ln.Close()
		if creason == shellintegration.ReasonNone {
			creason = shellintegration.ReasonUnsupportedShell
		}
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: launcher declined (%s)", creason)
	}

	// The delivery is armed BEFORE the grant is handed over: once the parent
	// shell has the line it can start `ssh` at once, and a bootstrap window
	// opened afterwards could miss the loader's readiness token and leave
	// the far side blocked on a frame nobody would send.
	plan := shellintegration.BootstrapPlan{Stage1: stage}
	// The lane travels with the delivery because it is the addressing the
	// session integration axis already uses to route this session's facts
	// (RegisterLifecycleLane), so the bootstrap's terminal outcome needs no
	// second registry to reach the product.
	delivery, err := typed.arm(sid, string(req.Lane), wrap.ControlPath, plan)
	if err != nil {
		_ = ln.Close()
		return lifecyclepub.GrantBootstrap{}, err
	}
	// Frame 2 is delivered only after the publish has reached a terminal
	// outcome (design §6.1 step 5), and never before stage-1 has verified
	// itself — DeliverBootstrap runs the barrier at exactly that point. The
	// barrier is the plan's rather than the secret source's for the reason
	// BootstrapPlan.Ordered gives: step 5 orders the DELIVERY, and step 8
	// follows frame 2 whether it carried a bearer or a refusal.
	//
	// The context is DeliverBootstrap's own, bounded by the bootstrap
	// deadline, and the wait honours it: once the bootstrap has given up
	// there is nothing left to hand a bearer to, and minting one anyway
	// would put a live per-epoch secret into backend memory for a session
	// that has already ended. It does not shorten the ordering — the
	// publish still settles before anything is minted; it only stops the
	// wait when the frame it would fill can no longer be written.
	plan.Ordered = func(ctx context.Context) error {
		select {
		case <-delivery.publishSettled:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("child domain: the bootstrap ended before the publish settled: %w", ctx.Err())
		case <-time.After(typedPublishDeadline):
			return fmt.Errorf("child domain: the publish did not settle inside %s", typedPublishDeadline)
		}
	}
	plan.Secret = shellintegration.SecretFunc(func(context.Context) ([]byte, error) {
		return shellintegration.SecretFrame(opts)
	})
	delivery.plan = plan

	extra := []string{"-t", "-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", remotePort, ln.Port())}
	line := composeSSHLine(wrap, extra, inv, carrier)

	go delivery.run(context.Background())

	return lifecyclepub.GrantBootstrap{Domain: h.Domain, Epoch: h.Epoch, Bootstrap: line}, nil
}

// randomPort picks a high loopback port for the -R bind. A collision with
// an occupied remote port makes the forward fail and the child fall back
// conventionally — the honest degrade, never a silent one.
func randomPort() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(20000))
	if err != nil {
		return 0, err
	}
	return 40000 + int(n.Int64()), nil
}
