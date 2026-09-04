package app

// This machine as an entry in the helper inventory (L1 of the local-helper
// design, D11 of level 1) — the composition root's open route for it.
//
// A local pane used to be a PTY the BACKEND forked, through internal/app's
// localPTYFactory, so it died with the backend and nothing else could ever
// hold it. It is now a session on this machine's helper generation, reached
// over the endpoint socket, spawned by the daemon and adopted here under the
// id the helper minted — the same three acts a remote pane goes through, over
// the same client, against the same session service (hostedSpawn.run).
//
// # There is no fallback, and that is a decision rather than an omission
//
// ADR-0057: when the local helper cannot be installed, started or reached,
// nocx does not open the pane by another route. So this opener always SELECTS
// a local destination — it never answers "not mine" for one — and a failure
// comes back as an error the person is shown. The alternative, keeping Tier A
// behind the helper, is a path that runs only when the daemon is broken, which
// is never during development and never in CI: the one path nobody exercises,
// diverging silently. The refusal's own vocabulary — a reason and an action
// from closed sets — is nocx-ie23r.4's; what this file owes is that the
// failure arrives AT THE ACT, with the concrete error in it.
//
// # One connection, every pane
//
// The remote route holds one helper process per session (D4, bounded by the
// binding registry) because each is an ssh exec lane to a different principal.
// Locally there is one daemon, one account and one socket, and the protocol
// multiplexes attachments over a connection by construction — so this holds
// ONE client for the generation and every pane rides it. A connection per pane
// would be a second answer to "which daemon serves this machine", and closing
// one because a pane failed would take every other pane's attachment with it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// errNoLocalGeneration is the state a refusal is raised out of: Start has not
// installed this machine's generation, so there is nothing to reach and — per
// ADR-0057 — nothing to fall back to.
var errNoLocalGeneration = errors.New(
	"this machine's nocx helper is not installed, so there is nothing to open the pane on")

// localHelperOpener opens a pane on this machine's helper.
type localHelperOpener struct {
	log      *slog.Logger
	registry *session.Reg
	// kernel and lifecycleLoss are the authenticated-channel seams, the same
	// two the remote hosted route uses. Nil is a legitimate wiring and makes
	// a conventional session, never a failure.
	kernel        lifecyclechannel.Kernel
	lifecycleLoss func(lifecycle.LaneID, lifecyclechannel.LossCause)
	// procs and reportShellReplaced are the shell-replacement observation
	// (nocx-cgzc) at its new address. The observation survived the move
	// because it is made from OUTSIDE the process: the daemon forks the
	// shell, and this coordinator watches the pid the daemon reports on the
	// same machine. What is watched changed owner; who watches did not.
	procs               procwatch.Watcher
	reportShellReplaced func(sid, observed string)
	// noteChildDomainParent records the two facts a nested sudo/su needs
	// about the pane it is opened inside: which transport its parent's
	// lifecycle lane rides, and which session that lane speaks for
	// (nocx-u7uh.11, and the wave record's pane enroller reads the second).
	// It is a SEPARATE seam from the transport's own lane registration
	// because the transport already owns that half — the hosted open path
	// binds lane to session through laneRegistrar — and one closure doing
	// both would put two owners on one statement.
	noteChildDomainParent func(t lifecycle.TransportID, lane lifecycle.LaneID, sid string)

	mu sync.Mutex
	// installed is what App.Start put on this machine, and dir is the
	// endpoint directory derived from the same home. Both are empty until
	// the install has run; an open before that is the refusal above.
	installed helperlocal.Installed
	dir       string
	// client is the one connection to the local daemon. Held across panes,
	// dropped when it is lost so the next open redials.
	client *helperclient.Client
}

// installedLocalGeneration records what Start installed, so the open route can
// reach it. It is a SETTER rather than a constructor argument because the
// install happens at Start and the composition root wires the opener at New:
// the alternative is installing inside New, which is the brain method this
// change is trying not to feed.
func (o *localHelperOpener) installedLocalGeneration(installed helperlocal.Installed, home string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.installed = installed
	o.dir = endpoint.Dir(home)
}

// OpenHosted opens a local pane on this machine's helper.
//
// It answers selected=false only for a destination that is not this machine's.
// A LOCAL destination is always this opener's, including when it cannot be
// served — see the file header: answering "not mine" for a broken helper is
// exactly the fallback ADR-0057 refuses.
func (o *localHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	if o == nil || cfg.Kind != session.KindLocal {
		return transport.HostedSessionOpen{}, false, nil
	}
	c, generation, err := o.connect(ctx)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	spawn := hostedSpawn{
		client: c, registry: o.registry,
		lifecycle: o.kernel, loss: o.lifecycleLoss,
		// The handshake bound, stated here rather than left to the adapter:
		// how long a shell may take to prove itself before the pane falls
		// back to a conventional terminal is a product decision, and this is
		// the composition root.
		helloTimeout: lifecycle.HelloTimeout,
		log:          o.log,
	}
	// The shell, the argv and the environment are all the daemon's: D3
	// refuses any op whose params carry a free-form []string, and the helper
	// resolves the login shell through the same internal/loginshell this
	// coordinator used to ask. The shell-integration activation environment
	// travels with it: LocalSpawner renders NOCX_SHELL_INTEGRATION,
	// NOCX_PROMPT_MODE and NOCX_SESSION_ID into the script it hands the shell
	// on an inherited descriptor, which is where an integrated shell has
	// always exported them from.
	res, err := spawn.run(ctx, cfg, proto.SpawnParams{
		Cwd: cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows,
		IdempotencyKey: claim,
	})
	if err != nil {
		o.dropIfLost(c)
		return transport.HostedSessionOpen{}, true, fmt.Errorf("open a pane on this machine's helper: %w", err)
	}
	sid := res.Session.ID()
	shell := res.Entry.Launch.Shell
	status, reason := localIntegrationStatus(shell, res.LifecycleLane)
	o.watchForReplacement(res.Session, res.Entry.Launch.Pid, shell)
	if res.LifecycleLane != "" && o.noteChildDomainParent != nil {
		o.noteChildDomainParent(res.LifecycleTransport, res.LifecycleLane, string(sid))
	}
	return transport.HostedSessionOpen{
		Session: res.Session,
		// Host and Account are EMPTY, and Generation is not. Generation says
		// which id space this session belongs to, which is true and is what a
		// verdict needs; the rest of the route back — which pane, which
		// connection, where the binary lives — is what a REMOTE session needs
		// to be re-adopted over ssh, and a local one is re-adopted by dialling
		// a socket instead. Filling them in with this machine's name would be
		// inventing a route the readopt pass would then try to resolve as a
		// saved connection. The local inventory that reads this is
		// nocx-ie23r.2's.
		Generation:         generation,
		LifecycleLane:      res.LifecycleLane,
		StartLifecycle:     res.StartLifecycle,
		AbortLifecycle:     res.AbortLifecycle,
		ObserveOutputHoles: res.ObserveOutputHoles,
		IntegrationShell:   shell,
		IntegrationStatus:  status,
		IntegrationReason:  reason,
	}, true, nil
}

// localIntegrationStatus is what this open already knows about the pane's
// shell integration, in the axis's own vocabulary.
//
// The local pty factory reported this because it was the only thing that knew
// which binary it had exec'd. That is still the rule; the knower has changed.
// The helper's launch record names the shell it actually started, and whether
// a lifecycle lane was established says whether anything is expected to
// answer — so the two facts the axis needs are both in hand at this point and
// nowhere else.
//
// An empty status means "do not register", which is how "conventional by
// design" is expressed. A shell nocx has no local tier for is NOT that: it is
// a session that asked to be integrated and will not be, and saying so out
// loud is the whole of nocx-wwz0.
func localIntegrationStatus(shell string, lane lifecycle.LaneID) (string, ssh.RefusalReason) {
	if shell == "" {
		return "", ssh.ReasonNone
	}
	if shellintegration.LocalShellKind(shell) == shellintegration.ShellUnknown {
		return transport.IntegrationConventional, ssh.ReasonUnsupportedShell
	}
	if lane == "" {
		return "", ssh.ReasonNone
	}
	return transport.IntegrationStarting, ssh.ReasonNone
}

// watchForReplacement asks the observer to say when the shell the DAEMON
// started stops being the process running under its pid — the takeover
// nocx-cgzc measured, where a wrapper execs out of the user's own startup file
// milliseconds after the fork and the product finds out ten seconds later.
//
// The pid comes off the helper's launch record now rather than off a child
// this process forked, and that is the whole of what moved: the observation is
// a kernel question about a pid on THIS machine, the daemon runs under the
// same account (D12), and nothing about it needed the watcher to be the
// parent. It stays a SECOND detector — the handshake bound is still the first
// — so a platform that cannot observe an exec degrades to exactly the product
// that shipped before it, which is why the failure is a Debug line.
//
// THE WATCH ENDS WITH THE SESSION, and that end is what the session's own Done
// is read for. The pid is the OS's to reuse the moment the shell is reaped, so
// a registration that outlived its session would be a watch on somebody else's
// process. It used to be released by the pty wrapper's Close, which is a
// wrapper this coordinator no longer holds; the session is the thing it holds
// now, and it ends at exactly the same moment.
func (o *localHelperOpener) watchForReplacement(sess session.Session, pid int, shell string) {
	sid := sess.ID()
	if sid == "" || pid <= 0 || shell == "" || o.procs == nil || o.reportShellReplaced == nil {
		return
	}
	stop, err := o.procs.Started(pid, shell, func(obs procwatch.Observation) {
		o.log.Info("the shell this session started was replaced before it answered",
			"session", string(sid), "pid", obs.PID, "started", shell, "observed", obs.Name)
		o.reportShellReplaced(string(sid), obs.Name)
	})
	if err != nil {
		o.log.Debug("this session's shell is not watched for replacement",
			"session", string(sid), "error", err)
		return
	}
	go func() {
		<-sess.Done()
		stop()
	}()
}

// connect answers with the connection to this machine's daemon, opening one if
// there is none, and with the generation behind it.
//
// The handshake is performed on every fresh connection and is NOT skipped
// locally: the socket's name carries 64 bits of the generation, the hello-ok
// carries the whole content hash, and a stale binary under ~/.nocx is likelier
// on the machine where builds land than on a server (D21).
func (o *localHelperOpener) connect(ctx context.Context) (*helperclient.Client, string, error) {
	o.mu.Lock()
	installed, dir, existing := o.installed, o.dir, o.client
	o.mu.Unlock()

	if installed.Binary == "" || installed.Generation == "" || dir == "" {
		return nil, "", errNoLocalGeneration
	}
	if existing != nil {
		return existing, string(installed.Generation), nil
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir: dir, Generation: installed.Generation, Binary: installed.Binary,
		Log: o.log,
	})
	if err != nil {
		return nil, "", err
	}
	o.mu.Lock()
	if o.client == nil {
		o.client = c
		o.mu.Unlock()
		return c, string(installed.Generation), nil
	}
	// A second open raced this one and stored its connection first. The loser
	// closes its OWN rather than replacing a client other panes are already
	// attached through — and it closes it outside the lock, because closing a
	// carrier is a syscall and holding the opener's mutex across one would
	// stall every other pane's open.
	winner := o.client
	o.mu.Unlock()
	_ = c.Close()
	return winner, string(installed.Generation), nil
}

// dropIfLost forgets a connection that has ended, so the next open dials
// again. A connection that is merely refusing one spawn — a budget, a bad
// key — is KEPT: every other pane on this machine is attached through it, and
// tearing it down because one open failed would end their sessions to tidy up
// this one's failure.
func (o *localHelperOpener) dropIfLost(c *helperclient.Client) {
	select {
	case <-c.Done():
	default:
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client == c {
		o.client = nil
	}
}

// close releases the connection to the local daemon. It does NOT end the
// sessions behind it, and that is the point of the whole epic: the daemon goes
// on holding them, and the next coordinator asks it what it holds.
func (o *localHelperOpener) close() {
	o.mu.Lock()
	c := o.client
	o.client = nil
	o.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// hostedOpeners is the one seam the transport asks, and the only thing in the
// composition root that knows a destination has two possible helpers.
//
// It is a DISPATCH and never a policy: the two openers each answer for what
// they own, and this only decides which one is asked first. Putting the branch
// here rather than inside either opener is what keeps `helperRegistry.
// OpenHosted` — already the most conditional function in its file — from
// growing another arm.
type hostedOpeners struct {
	local  *localHelperOpener
	remote *helperRegistry
}

func (h *hostedOpeners) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	if cfg.Kind == session.KindLocal {
		return h.local.OpenHosted(ctx, cfg, claim)
	}
	return h.remote.OpenHosted(ctx, cfg)
}

var _ transport.HelperSessionOpener = (*hostedOpeners)(nil)
