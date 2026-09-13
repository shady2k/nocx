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

// errLocalEndpointUnreachable is the state a carried-over LOCAL session is
// judged `unknown` out of: this machine's daemon did not answer the one
// question reconciliation asks it (nocx-ie23r.2).
//
// It is a SENTINEL rather than prose because the cause the renderer picks its
// sentence from is decided in ONE place (causeFor), and that function can only
// tell a local failure from a remote one by what the error carries. Every
// failure of the dial, the handshake and the ask is wrapped in it, because
// locally there is no second thing that could have gone wrong: there is no
// credential to seal, no host key to consent to and no ssh route to resolve,
// so "nobody answered" is the whole of what the ask can fail with.
var errLocalEndpointUnreachable = errors.New(
	"this machine's helper could not be asked")

// localInventoryRoute asks THIS machine's own daemon what it holds, for one
// generation (nocx-ie23r.2 — L5's missing local inventory).
//
// # What it is for
//
// A local pane used to be a PTY the backend forked, so it died with the
// backend and a carried-over local session was certainly gone. It is a session
// on this machine's daemon now (helper_local.go's header), which means it can
// OUTLIVE the coordinator — and the one thing reconciliation must then do is
// the thing it does for a remote host: ask. Without this, a local session is
// `noInventory` for ever, and the notice a person reads tells them the session
// "may still be running", which for a command that is certainly over is the
// kind of falsehood that makes the whole third state untrustworthy.
//
// # It dials the generation the BINDING names, not the one that is installed
//
// The socket's name carries the generation (endpoint.Path), and the binding
// carries the generation its session was spawned by. Those two are the same
// only while the build has not changed — and the case that matters is exactly
// the one where it has: a person updates nocx, the OLD daemon is still holding
// their shells, and the generation to ask about is the old one. Asking the
// installed generation instead would report every surviving session as
// unreachable the first time somebody updated, which is the same lie this file
// exists to end, told about the other half of the users.
//
// # It never STARTS a daemon, and that is not an optimisation
//
// helperlocal.Open starts the installed binary when nothing is serving — which
// is right for an open, where a pane has been asked for, and wrong here. A
// probe that spawned a helper would turn "was this session still there" into a
// process somebody did not ask for, and would make the answer to the question
// depend on the act of asking it. So the Binary is EMPTY: this caller may not
// start one, which is a state helperlocal.Config names in its own words, and a
// machine with nothing serving answers with endpoint.ErrNoEndpoint instead. A
// failure, and therefore `unknown` — never `absent`, because nothing said the
// session was gone.
//
// # The connection the re-attachment rides is the coordinator's OWN one
//
// A session this machine's daemon still holds is ATTACHED over the same
// connection every other pane and every frame read uses (nocx-ie23r.5) — see
// Attach below. The ask itself is made on a PROBE connection that is
// released the moment the answer is in, and the two are deliberately not the
// same object: a probe may not start a daemon (a question that spawns answers
// itself), while the pane's attachment is an open-shaped act on this machine's
// daemon, which is the connection `connect` owns and `close` ends.
//
// The bead before this one asked over a probe connection and closed it when
// the answer came back, which was right while nothing attached to what it
// found. An attachment lives on the connection it was made over and cannot be
// moved to another, so that release would detach the session it had just
// recovered — silently, because the attach result is a value and the
// connection's end is not.

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
	// (nocx-u7uh.11, and the worker record's pane enroller reads the second).
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
	// toolSocketPath is THIS backend's tool endpoint socket, when it is
	// running one — internal/toolendpoint.Endpoint.SocketPath(), handed
	// down by cmd/nocx-server's composition root (nocx-2tesu). Empty is a
	// real, deliberate state (startToolEndpoint answered nil, nil for no
	// authorizer/dispatcher) and not merely "not set yet": a local pane
	// opened while it is empty carries no NOCX_TOOL_SOCKET, which is the
	// soft degrade staying soft — the shell's own refusal text is what a
	// user sees, never a pane pointed at an empty path.
	toolSocketPath string
	// client is the one connection to the local daemon. Held across panes,
	// dropped when it is lost so the next open redials.
	client *helperclient.Client
	// reattached is one connection per RE-ATTACHED session (nocx-ie23r.5),
	// keyed by the session it was attached for and carrying the generation its
	// endpoint is named for. It is the local analogue of helperRegistry.hosts:
	// a taken-back session's attachment lives on a connection that cannot be
	// moved, so the pane's screen read has to reach THAT connection — and its
	// daemon, which after an update is not the one this build installed. Held
	// until the coordinator closes (see close), because the connection is what
	// the session's own detach travels over.
	reattached map[session.ID]localSessionConn
}

// routeDir records the endpoint directory this machine's daemon is reached
// through — endpoint.Dir(home), derived from the same home the rest of the
// composition root uses.
//
// IT IS ITS OWN SETTER, CALLED AT New, and that is a fact about WHEN the two
// halves of the local route become knowable rather than a style choice
// (nocx-ie23r.5). The directory is a fact of the HOME and is known before
// anything is built; the INSTALL is a fact of Start, and the generation it
// produces is what the open route and the pane's own connection need. The ask
// needs only the directory — it dials the generation a binding names, on a
// probe connection that may not start one — and the ask RUNS AT New, where
// reconciliation is. A route whose directory arrived with the install would
// answer `noInventory` for every carried-over local session, which is exactly
// the silence nocx-ie23r.2 exists to end, arriving through the door this bead
// opened.
func (o *localHelperOpener) routeDir(home string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dir = endpoint.Dir(home)
}

// installedLocalGeneration records what Start installed, so the open route can
// reach it. It is a SETTER rather than a constructor argument because the
// install happens at Start and the composition root wires the opener at New:
// the alternative is installing inside New, which is the brain method this
// change is trying not to feed.
func (o *localHelperOpener) installedLocalGeneration(installed helperlocal.Installed) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.installed = installed
}

// setToolSocketPath records this backend's tool endpoint socket, so a pane
// this opener starts can find it (nocx-2tesu). It is a THIRD setter beside
// installedLocalGeneration for the same reason that one is a setter rather
// than a constructor argument, one step further: cmd/nocx-server does not
// know whether it is running a tool endpoint at all until AFTER Start
// returns (starting it needs the app's ToolAuthorizer/ToolDispatcher, which
// Start is what populates), so this fact becomes known later than
// installedLocalGeneration's ever does, and forcing the two through one
// call would make the earlier one wait on the later.
//
// path is empty when this backend is not running an endpoint — see the
// field's own doc for why that is a real state and not "not configured yet".
func (o *localHelperOpener) setToolSocketPath(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.toolSocketPath = path
}

// toolSocketEnv turns this backend's tool socket path into the one extra
// environment entry a freshly spawned local helper needs to find it — or
// into nothing, when there is none, which is what keeps the soft degrade
// soft: an empty path adds no entry rather than exporting an empty one.
func toolSocketEnv(path string) []string {
	if path == "" {
		return nil
	}
	return []string{shellintegration.ToolSocketEnvVar + "=" + path}
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
	if o.registry == nil {
		_ = res.Session.Close()
		return transport.HostedSessionOpen{}, true, errors.New("local helper opener has no session registry")
	}
	if err := o.registry.RecordOwnedProcessPID(sid, res.Entry.Launch.Pid); err != nil {
		_ = res.Session.Close()
		return transport.HostedSessionOpen{}, true, fmt.Errorf("recording local helper launch pid: %w", err)
	}
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
// screenClient is the route a pane's screen is read through: this machine's
// daemon, and the handle it knows the session by.
//
// It reuses connect, so a coordinator that already holds the connection pays
// nothing for the answer, and one that does not dials the daemon exactly as an
// open would — the same endpoint, the same generation, no second route.
func (o *localHelperOpener) screenClient(ctx context.Context, sid string) (*helperclient.Client, helperclient.HostSessionID, error) {
	// A RE-ATTACHED SESSION'S OWN CONNECTION FIRST (nocx-ie23r.5). It is not an
	// optimisation: that connection is named for the generation the session
	// actually lives on, which after an update is not the one this build
	// installed, and a frame asked for from the wrong generation's daemon is a
	// refusal — a pane whose output flows and whose screen cannot be read.
	if conn, ok := o.reattachedConn(session.ID(sid)); ok {
		return conn.client, helperclient.HostSessionID{
			Generation: string(conn.generation), Session: sid,
		}, nil
	}
	c, generation, err := o.connect(ctx)
	if err != nil {
		return nil, helperclient.HostSessionID{}, err
	}
	return c, helperclient.HostSessionID{Generation: generation, Session: sid}, nil
}

// Release gives up the connection opened for a session that did not come back
// (the local route's own method, called on the failure arm). It closes the
// connection and forgets it, so a refused re-attachment — another coordinator
// holding the keyboard, a transport that would not take the session — costs
// nothing that outlives the attempt. Safe on a session that has no connection,
// which is the ordinary case here.
func (o *localHelperOpener) Release(sid string) {
	key := session.ID(sid)
	o.mu.Lock()
	conn, ok := o.reattached[key]
	delete(o.reattached, key)
	o.mu.Unlock()
	if ok {
		_ = conn.client.Close()
	}
}

// reattachedConn answers the connection a re-attached session rides, if it has
// one. Read under the opener's own lock, and by the same key sessionConn writes.
func (o *localHelperOpener) reattachedConn(sid session.ID) (localSessionConn, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	conn, ok := o.reattached[sid]
	return conn, ok
}

func (o *localHelperOpener) connect(ctx context.Context) (*helperclient.Client, string, error) {
	o.mu.Lock()
	installed, dir, existing, toolSocketPath := o.installed, o.dir, o.client, o.toolSocketPath
	o.mu.Unlock()

	if installed.Binary == "" || installed.Generation == "" || dir == "" {
		return nil, "", errNoLocalGeneration
	}
	if existing != nil {
		return existing, string(installed.Generation), nil
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir: dir, Generation: installed.Generation, Binary: installed.Binary,
		Log: o.log, Env: toolSocketEnv(toolSocketPath),
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

// LocalSessions reaches this machine's daemon for the generation a carried-over
// binding names, and answers with the sessions it holds.
//
// IT DIALS THE GENERATION THE BINDING NAMES, NOT THE ONE THAT IS INSTALLED. The
// socket's name carries the generation (endpoint.Path), and the binding carries
// the generation its session was spawned by. Those two are the same only while
// the build has not changed — and the case that matters is exactly the one
// where it has: a person updates nocx, the OLD daemon is still holding their
// shells, and the generation to ask about is the old one. Asking the installed
// generation instead would report every surviving session as unreachable the
// first time somebody updated, which is a falsehood about half the users.
//
// IT NEVER STARTS A DAEMON, and that is not an optimisation: a probe that
// spawned a helper would turn "was this session still there" into a process
// nobody asked for, and would make the answer to the question depend on the
// act of asking it. So the Binary is EMPTY — this caller may not start one,
// which is a state helperlocal.Config names in its own words — and a machine
// with nothing serving answers endpoint.ErrNoEndpoint. A failure, and therefore
// `unknown`: never `absent`, because nothing said the session was gone.
//
// A NIL ANSWER MEANS NOBODY MAY BE ASKED: no endpoint directory, or no
// generation to name. The caller's own `noInventory` stands.
//
// THE CONNECTION IS RELEASED BEFORE THIS RETURNS, whatever the answer, and
// that is stated here because it is the one thing an ask's caller must know:
// nothing of the ask outlives the question. What carries a re-attached pane's
// attachment is the coordinator's own connection to this machine's daemon,
// which Attach takes and whose generation is the one the binding named.
func (o *localHelperOpener) LocalSessions(ctx context.Context, generation string) ([]helperclient.SessionEntry, error) {
	o.mu.Lock()
	dir := o.dir
	o.mu.Unlock()
	if dir == "" || generation == "" {
		return nil, nil
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir:        dir,
		Generation: proto.GenerationID(generation),
		// EMPTY: this caller may not start a helper. See this method's own
		// doc — a probe is not a spawn.
		Binary: "",
		Log:    o.log,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLocalEndpointUnreachable, err)
	}
	defer func() { _ = c.Close() }()
	entries, err := c.Sessions(ctx)
	if err != nil {
		// WRAPPED IN THE SAME SENTINEL, and this half is the one that is easy
		// to miss: a handshake that completed and a connection that then went
		// away reports a loss, not a dial failure, and an unwrapped one would
		// fall through to causeFor's generic branches and be described as a
		// host that refused or timed out. It is still this machine's helper
		// that did not answer the question.
		return nil, fmt.Errorf("%w: %w", errLocalEndpointUnreachable, err)
	}
	return entries, nil
}

// Attach takes this machine's daemon attachment for a session that is still
// running, over the connection THIS SESSION is re-attached on (hostedCarrier).
//
// THE CONNECTION IS PER SESSION AND NAMED BY THE BINDING'S GENERATION, not by
// what this build installed, and the reason is the one the ask already gives: a
// person updates nocx, the OLD daemon is holding their shells, and the generation
// to attach to is the old one. What that costs is stated where the pane's two
// halves meet — screenClient resolves the same connection, so the frame read and
// the attachment are always the same daemon.
//
// IT MAY NOT START A DAEMON, for the same reason the ask may not: a session is
// only there if something is already serving it, and a re-attachment that could
// start a helper would answering its own question with a process nobody asked
// for. Binary is therefore EMPTY, and nothing serving answers ErrNoEndpoint.
//
// THE CONNECTION OUTLIVES THIS CALL and is owned by the opener, closed when the
// coordinator closes (see sessionConn). It cannot be closed when the session
// ends, tempting as that is: the attachment lives ON the connection, and the
// session's own teardown sends its detach over it — a close raced against that
// would detach nothing and report the session's close as a failure. The
// attachment's own Close is what releases the daemon's subscriber; this
// connection then sits idle until the coordinator goes, which is exactly what
// the ordinary open's shared connection does between panes.
func (o *localHelperOpener) Attach(ctx context.Context, params proto.AttachParams) (*helperclient.AttachedSession, error) {
	c, err := o.sessionConn(ctx, params.Session.Generation, params.Session.Session)
	if err != nil {
		return nil, err
	}
	return c.Attach(ctx, params)
}

// AdoptLifecycle asks this machine's daemon for the identity a taken-back
// session's shell is still speaking with (hostedCarrier, nocx-k6p18.31), over
// the SAME connection the attachment will be made on.
//
// The same connection, and not a new one, for a reason specific to this call:
// what it answers is the capability the shell has been stamping its frames
// with, handed to the holder the daemon considers the session's own
// coordinator. Asking over a connection that is not about to hold the session
// would be a second caller asking to be handed somebody else's authority.
// sessionConn is what makes "same" true — it opens the connection on the first
// of these two calls and answers with it on the second.
func (o *localHelperOpener) AdoptLifecycle(ctx context.Context, id helperclient.HostSessionID) (*proto.LifecycleLaunch, error) {
	c, err := o.sessionConn(ctx, proto.GenerationID(id.Generation), id.Session)
	if err != nil {
		return nil, err
	}
	return c.AdoptLifecycle(ctx, id)
}

// localSessionConn is one re-attached session's connection: the client, and the
// generation its endpoint is named for — carried together because the screen
// read needs both, and a handle addressed to the wrong generation names nothing.
type localSessionConn struct {
	client     *helperclient.Client
	generation proto.GenerationID
}

// sessionConn answers with the connection one re-attached session rides,
// opening it on the first call for that session and reusing it afterwards.
//
// IT IS THE LOCAL ANALOGUE OF helperRegistry.hosts, and for the same reason:
// a pane's screen read has to reach the daemon that holds the session, and
// after an update that daemon is not the one this build installed. The remote
// route answers that with the helper channel it keeps per session (hostFor);
// this is the same map one carrier over, and it is deliberately the opener's
// rather than a second map anywhere else — `hosts` and this are the two
// halves of one fact, "which daemon holds this session", told per carrier.
//
// The daemon is DIALED AND NEVER STARTED: a session a binding names is held by
// a helper that is already serving, and this is only ever reached after the ask
// proved exactly that.
func (o *localHelperOpener) sessionConn(ctx context.Context, generation proto.GenerationID, sid string) (*helperclient.Client, error) {
	key := session.ID(sid)
	o.mu.Lock()
	if conn, ok := o.reattached[key]; ok {
		o.mu.Unlock()
		return conn.client, nil
	}
	dir := o.dir
	o.mu.Unlock()
	if dir == "" || generation == "" {
		return nil, errNoLocalGeneration
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir: dir, Generation: generation,
		// EMPTY: a re-attachment reaches a daemon that is serving, and never
		// starts one. See Attach's own doc.
		Binary: "",
		Log:    o.log,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLocalEndpointUnreachable, err)
	}
	o.mu.Lock()
	if existing, ok := o.reattached[key]; ok {
		o.mu.Unlock()
		_ = c.Close()
		return existing.client, nil
	}
	if o.reattached == nil {
		o.reattached = map[session.ID]localSessionConn{}
	}
	o.reattached[key] = localSessionConn{client: c, generation: generation}
	o.mu.Unlock()
	return c, nil
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
	kept := make([]*helperclient.Client, 0, len(o.reattached))
	for _, conn := range o.reattached {
		kept = append(kept, conn.client)
	}
	o.reattached = nil
	o.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	// The re-attached panes' connections go with the coordinator, which is the
	// closing event they were given rather than the session's own end: the
	// daemon's subscriber was already released by the attachment's detach, and
	// what is left here is an idle socket per re-attached pane.
	for _, conn := range kept {
		_ = conn.Close()
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
