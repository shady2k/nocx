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
//   - ssh: the bootstrap is a rewritten command line the parent executes —
//     ADR-0022, "the ssh command line is the carrier" — carrying the child's
//     forwarded lifecycle port as a -R reverse forward on that same ssh
//     connection plus the in-band install payload (wrapper, capability as
//     the first streamed line, payload, terminator) piped into `ssh -t`.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
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
func newChildGrantBuilder(lg log.Logger, pub func() *lifecyclepub.Publisher, transports *transportRegistry, sessions *sessionRegistry) lifecyclepub.GrantBuilder {
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
			return buildSSHChildBootstrap(lg, p, sessions, req, kind)
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

// buildSSHChildBootstrap composes the ssh child: a loopback listener
// transport (the local endpoint of the child's -R reverse forward), the
// child minted on it, and the rewritten command line the parent executes —
// the in-band install payload piped into `ssh -t` with the -R. The remote
// port is pre-picked because the payload must name it before ssh runs; a
// server that refuses the bind (PermitListen) fails the forward, the child
// never establishes, and the session is the honest conventional fallback.
func buildSSHChildBootstrap(lg log.Logger, pub *lifecyclepub.Publisher, sessions *sessionRegistry, req lifecyclepub.GrantRequest, parentKind transportKind) (lifecyclepub.GrantBootstrap, error) {
	if !parentKind.local {
		// A remote parent runs ssh on the far host: the -R forward would
		// terminate at that host, not at this backend's listener. The
		// mechanism does not preclude it — the far host's own remote
		// adapter listener is the natural endpoint — but it is not built
		// in this bead. Refuse honestly: the parent runs its command
		// conventionally.
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: ssh nested inside a remote parent is not implemented")
	}
	sid, ok := sessions.lookup(req.Lane)
	if !ok {
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: no session registered for lane %s", req.Lane)
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
	// The far shell is brought up by the SAME launcher the profile path
	// uses (AD-8: one owner for "what command makes a remote shell
	// integrated"), delivered as the command OpenSSH runs on the far side —
	// which is what ADR-0022 decided the carrier is.
	//
	// It used to be delivered in-band instead, piped into the client's
	// stdin ahead of the connection, and that is what nocx-beib was: the
	// authentication phase belongs to the ssh client, and a client whose
	// stdin is a pre-filled pipe cannot ask for a password, a passphrase, a
	// host key or a second factor — it reads our wrapper as the answer. A
	// key hides this completely, which is why every proof of this path
	// (ssh_child_assembly_test.go, ADR-0025) authenticated with one.
	// FullBootstrapCommand, not the launcher's StartCommand: the managed
	// path now emits the bounded carrier (shellintegration/carrier.go), and
	// the carrier is only half a delivery without the frame sender that
	// feeds it — which this path does not have. Design §12 gives the typed
	// `ssh` wrapper and its sender to P4; until then this line keeps the
	// pre-carrier self-installing launcher, deliberately and by name.
	cmd, reason, ok := shellintegration.FullBootstrapCommand(
		shellintegration.ShellAuto,
		shellintegration.LaunchOptions{
			SessionID:     sid,
			Enhanced:      true,
			Lane:          string(req.Lane),
			Domain:        string(h.Domain),
			Epoch:         h.Epoch,
			LifecyclePort: remotePort,
			Capability:    hex.EncodeToString(h.Capability[:]),
			Recovery:      hex.EncodeToString(h.Recovery[:]),
		})
	if !ok || cmd == "" {
		_ = ln.Close()
		if reason == shellintegration.ReasonNone {
			reason = shellintegration.ReasonUnsupportedShell
		}
		return lifecyclepub.GrantBootstrap{}, fmt.Errorf("child domain: launcher declined (%s)", reason)
	}
	line := composeSSHChildLine(cmd, remotePort, ln.Port(), req)
	return lifecyclepub.GrantBootstrap{Domain: h.Domain, Epoch: h.Epoch, Bootstrap: line}, nil
}

// composeSSHChildLine builds the rewritten command line the parent executes
// (ADR-0022: the ssh command line is the carrier). The shape:
//
//	ssh -t -R 127.0.0.1:CPORT:127.0.0.1:LPORT [-p N] dst '<launcher command>'
//
// Nothing wraps the client. Its stdin is the parent's terminal, so the whole
// authentication phase — password, passphrase, host-key confirmation, a
// second factor — is between the user and OpenSSH exactly as in a plain
// terminal, and the integration rides in the command sshd runs afterwards.
//
// This replaced an in-band delivery that piped the bootstrap into the
// client's stdin before it had connected (nocx-beib). It worked with a key,
// because the far side reaches a prompt immediately and consumes the staged
// bytes; with an interactive prompt the client read our wrapper as the
// user's password and the login could not succeed. The pipe was also why
// `-tt` was needed — a client whose stdin is not a terminal refuses to
// allocate one — so removing the pipe removes the reason for the second t
// along with the termios save/restore that guarded the raw-mode window.
//
// The far shell is therefore started by sshd from this command rather than
// as a bare login shell, which is the same shape the profile path already
// uses. A destination configured with its own RemoteCommand is refused by
// OpenSSH ("Cannot execute command-line and remote command") and falls back
// conventionally, which is the honest degrade — the same one the profile
// path names ReasonRemoteCommand.
func composeSSHChildLine(startCmd string, remotePort, localPort int, req lifecyclepub.GrantRequest) string {
	var b strings.Builder
	b.WriteString("ssh -t -R 127.0.0.1:")
	b.WriteString(fmt.Sprintf("%d:127.0.0.1:%d", remotePort, localPort))
	// The options the user typed, in their order, ahead of the destination
	// where ssh expects them (nocx-c6z0).
	//
	// This line is rebuilt rather than edited, so anything not carried here
	// is not merely reordered — it is gone. It used to carry host, user and
	// port and nothing else, while the shell's detector deliberately ACCEPTS
	// a line bearing -i, -o, -F, -J, -l, -e, -b and -m. So a user's
	// `ssh -i ~/.ssh/prod -J bastion host` connected with the wrong key and
	// no jump host at all, and the block went on showing the line they typed,
	// so nothing anywhere said otherwise.
	//
	// Quoted one token at a time, never joined and quoted once: each token is
	// a separate argv entry on the user's side and must stay one here.
	// -p and -t are absent by construction (the port is modelled above; a
	// second -t is -tt, which is not what this composes).
	for _, o := range req.Opts {
		b.WriteString(" ")
		b.WriteString(shellintegration.ShellQuote(o))
	}
	if req.Port != 0 {
		b.WriteString(fmt.Sprintf(" -p %d", req.Port))
	}
	b.WriteString(" ")
	dest := req.Host
	if req.User != "" {
		dest = req.User + "@" + dest
	}
	b.WriteString(shellintegration.ShellQuote(dest))
	b.WriteString(" ")
	b.WriteString(shellintegration.ShellQuote(startCmd))
	return b.String()
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
