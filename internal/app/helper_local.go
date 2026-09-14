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
	"io/fs"
	"log/slog"
	"sync"
	"syscall"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/profile"
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

// refuseLocalHelperUnreachable classifies a failure of REACHING this machine's
// helper into the refusal a person is shown (nocx-ie23r.4, design L4).
//
// THE SENTINELS ARE THE CLASSIFICATION, never the message text. Each one is a
// distinct fact the client already decided — a peer that is not our helper, a
// peer that never answered, a version the helper refused — and re-reading them
// out of a string is how two answers to "what went wrong" start disagreeing.
//
// What is left after the handshake's own sentinels is the start boundary: the
// dial found nothing serving and the binary did not come up (or came up and
// ended before it served, or is not executable at all), which is the reason
// helperlocal.Open's own doc calls "an error from the start".
func refuseLocalHelperUnreachable(cause error) error {
	switch {
	case errors.Is(cause, helperclient.ErrHashMismatch),
		errors.Is(cause, helperclient.ErrNotOurHelper):
		// Something answered on this machine's endpoint and it is not the
		// helper this build installed. One helper serves the endpoint, so
		// the thing answering is another copy of nocx — and quitting it is
		// the only remedy a person has that is not a lie.
		return transport.RefuseLocalHelper(transport.HelperRefusal{
			Reason: transport.HelperHandshakeFailed,
			Action: transport.HelperActionQuitOtherNocx,
			Cause:  cause,
		})
	case errors.Is(cause, helperclient.ErrVersionMismatch):
		// The peer refused the protocol version (D5's exit 42), and its own
		// sentinel says what that means: non-retryable until reinstall. The
		// installed helper IS a different build from the one reaching for
		// it, which no amount of quitting and reopening fixes.
		return transport.RefuseLocalHelper(transport.HelperRefusal{
			Reason: transport.HelperHandshakeFailed,
			Action: transport.HelperActionReinstallNocx,
			Cause:  cause,
		})
	case errors.Is(cause, helperclient.ErrSentinelTimeout),
		errors.Is(cause, helperclient.ErrLost),
		errors.Is(cause, syscall.EPIPE):
		// The endpoint accepted and then said nothing, died mid-handshake, or
		// hung up on the hello write. That last one is the SAME event seen by
		// a different observer: internal/helper/local's own note says a peer
		// closing mid-handshake produces three errors — ErrNotOurHelper (the
		// pump saw what was said), ErrLost (the carrier calls a peer close
		// transport loss) and a bare EPIPE from the hello write — so the bare
		// EPIPE is classified here rather than left for a message match. The
		// remaining non-EPIPE forms stay the carrier's documented gap.
		// Nothing here entitles us to name it as another build, so the remedy
		// is the mild one — and it is a real one: the socket is being served,
		// so a second attempt costs a person one gesture.
		return transport.RefuseLocalHelper(transport.HelperRefusal{
			Reason: transport.HelperHandshakeFailed,
			Action: transport.HelperActionRetryOpen,
			Cause:  cause,
		})
	case errors.Is(cause, endpoint.ErrForeignDir):
		// The endpoint directory belongs to another account. That directory is
		// <home>/.nocx/run (endpoint.Dir), NOT the install directory — the
		// install writes <home>/.nocx/helper/<gen>, and this arm is reached
		// only after a SUCCESSFUL install, when the dial then finds a run
		// directory somebody else owns. So the reason is START, not install:
		// the install did succeed and nothing of ours is serving. The cause
		// text names the real problem, and the remedy is the directory the
		// person can actually fix.
		return transport.RefuseLocalHelper(transport.HelperRefusal{
			Reason: transport.HelperStartFailed,
			Action: transport.HelperActionFixPermissions,
			Cause:  cause,
		})
	default:
		// Everything left is the start boundary: the binary did not come up.
		// ctx.Err() lands here because a cancelled start is a fact about
		// shutdown, not about the helper, and ErrPathTooLong because the
		// socket path is derived from the generation rather than chosen by
		// the product.
		return transport.RefuseLocalHelper(transport.HelperRefusal{
			Reason: transport.HelperStartFailed,
			Cause:  cause,
		})
	}
}

// refuseLocalHelperNotInstalled classifies the state an open reaches when
// Start put no generation on disk.
//
// cause is the install's own error when there was one, and errNoLocalGeneration
// when nothing tried to install (a build with no artifact source wired, which
// production cannot reach — helperartifacts.DefaultSource is what the
// composition root passes). The two are different facts with the same
// consequence, and the refusal says which one it has rather than averaging
// them into one sentence about a missing helper.
func refuseLocalHelperNotInstalled(cause error) error {
	action := transport.HelperRefusalAction("")
	switch {
	case errors.Is(cause, syscall.ENOSPC), errors.Is(cause, syscall.EDQUOT):
		// The disk, not the copy: the artifact was there and the bytes would
		// not fit. A quota is the same fact — an allocation limit rather than
		// a missing copy — so it names the same remedy. Repairing the
		// application would not have helped, and telling somebody to reinstall
		// is how a refusal becomes a lie.
		action = transport.HelperActionFreeSpace
	case errors.Is(cause, fs.ErrPermission), errors.Is(cause, syscall.EROFS):
		// The directory, not the copy: nocx could not write where it installs
		// — the directory is not ours to write, or the filesystem is
		// read-only. A reinstall writes the SAME bytes to the SAME place and
		// fails the same way, so it is the wrong remedy; the person has to
		// make the install directory writable first.
		action = transport.HelperActionFixPermissions
	}
	return transport.RefuseLocalHelper(transport.HelperRefusal{
		Reason: transport.HelperInstallFailed,
		Action: action,
		Cause:  cause,
	})
}

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
// spawnTokens is the bearer each pane this opener spawned was launched with,
// kept until the interval that admits it takes it (nocx-50w7p.16).
//
// It exists because of an ORDER, and the order is the whole reason this type is
// not a field on the session: the launch has to carry a bearer BEFORE the
// coordinator knows which session it is for — the helper mints the session id
// and reports it in the spawn's result — while the interval that gives the
// bearer its meaning is created later still, when the pane's agent enrols and a
// person answers. So the bearer is minted with the launch and BOUND when the
// interval opens, and this is where it waits in between.
//
// An entry lives from the successful spawn until the session ends: a pane that
// is never enrolled keeps one 64-byte string until then, and a pane whose
// session ends drops it with the interval that was holding it — which is why
// SpawnToken peeks rather than consumes (see its comment) and why forget exists
// at all.
type spawnTokens struct {
	mu   sync.Mutex
	byID map[session.ID]string
}

func (b *spawnTokens) record(sid session.ID, token string) {
	if b == nil || sid == "" || token == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byID == nil {
		b.byID = make(map[session.ID]string)
	}
	b.byID[sid] = token
}

// SpawnToken is the seam the approval service reads: the bearer this opener
// launched the pane with.
//
// IT IS A PEEK AND NOT A TAKE, and re-approval is why. A session can hold two
// intervals with identical approval — withdrawn and enrolled again — and both
// belong to the SAME launch: the far shell staged one bearer and cannot learn a
// second, so an interval that rotated it would refuse the pane's own agent for
// presenting the value that was correct when it was written. What retires a
// bearer is the interval ending (session end, withdrawal), which is where the
// entry is dropped, not the first reader.
func (b *spawnTokens) SpawnToken(sid session.ID) (string, bool) {
	if b == nil || sid == "" {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	token, ok := b.byID[sid]
	return token, ok && token != ""
}

// Forget drops what a launch left, when the session it was for ends. Called by
// the approval service, which is the party that sees that event and is also the
// party that stopped holding the bearer.
func (b *spawnTokens) Forget(sid session.ID) {
	if b == nil || sid == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.byID, sid)
}

type localHelperOpener struct {
	log      *slog.Logger
	registry *session.Reg
	// spawnTokens holds the bearer of each pane this opener launched, until the
	// interval that admits it takes it. Nil is a legitimate wiring for a test
	// that builds an opener without one, and then the approval mints its own.
	spawnTokens *spawnTokens
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
	// installFailure is WHY Start could not put this machine's generation on
	// disk, when it could not. It is recorded BESIDE the empty install rather
	// than instead of it, and that is the whole of nocx-ie23r.4's install
	// half: the refusal a person meets at the act has to name the concrete
	// error — no space, a directory we may not write, an artifact this build
	// does not carry — and Start is the only place that error exists. A
	// refusal that could only say "not installed" would be naming the
	// category, which the reason field already does.
	installFailure error
	// toolSocketPath is THIS backend's tool endpoint socket, when it is
	// running one — internal/toolendpoint.Endpoint.SocketPath(), handed
	// down by cmd/nocx-server's composition root (nocx-2tesu). Empty is a
	// real, deliberate state (startToolEndpoint answered nil, nil for no
	// authorizer/dispatcher) and not merely "not set yet": a local pane
	// opened while it is empty carries no NOCX_TOOL_SOCKET, which is the
	// soft degrade staying soft — the shell's own refusal text is what a
	// user sees, never a pane pointed at an empty path.
	//
	// It travels on each SPAWN REQUEST and never in the daemon's environment
	// (nocx-50w7p.18). The daemon's endpoint socket is keyed by the
	// generation rather than by a coordinator, so one account's daemon serves
	// several coordinators (D12), and a value handed to it once at its start
	// describes only whichever coordinator started it — a pane opened by
	// another one would reach a backend that never asked for that pane. This
	// process's OWN endpoint is a fact this process knows and the daemon
	// cannot, so it says it per pane.
	toolSocketPath string
	// client is the one connection to the local daemon. Held across panes,
	// dropped when it is lost so the next open redials.
	client *helperclient.Client
	// reverse is the closed set of ops this coordinator answers when the
	// helper asks it something (helper_reverse.go). It is bound by the
	// composition root once the vault and the ssh client exist, and it is
	// carried into every connection this opener builds — a helper that dials
	// has nobody else to ask.
	reverse *helperclient.ReverseRegistry
	// sshTargets resolves a resolved ssh destination into the value the helper
	// dials (nocx-50w7p.5).
	//
	// IT IS THE COORDINATOR'S HALF AND IT STAYS HERE. Resolution is an alias
	// through ~/.ssh/config, the merging of its defaults, and the credential's
	// own authorization against the endpoint its profile names — and the
	// authorization check in particular belongs to the party that reads the
	// config and holds the binding, which is this process. The helper is handed
	// an address, an account and a REFERENCE to material, and it decides
	// nothing (ssh.WireDestination's own comment).
	//
	// Nil is a legitimate wiring for a server that never opens an ssh pane, and
	// an ssh open on it is a refusal that names this line rather than a dial.
	sshTargets sshTargetResolver

	// reattached is one connection per RE-ATTACHED session (nocx-ie23r.5),
	// keyed by the session it was attached for and carrying the generation its
	// endpoint is named for. It is the local analogue of helperRegistry.hosts:
	// a taken-back session's attachment lives on a connection that cannot be
	// moved, so the pane's screen read has to reach THAT connection — and its
	// daemon, which after an update is not the one this build installed. Held
	// until the coordinator closes (see close), because the connection is what
	// the session's own detach travels over.
	reattached map[session.ID]localSessionConn
	// held is this machine's daemon's own set of sessions — see its doc below
	// for the interval and for why the carrier is not read off the kind.
	held map[session.ID]struct{}
}

// held is the set of session ids THIS MACHINE'S DAEMON holds — the answer to
// "which helper holds this session" for the one carrier that the remote
// registry cannot answer for (nocx-50w7p.5).
//
// THE INTERVAL, BOTH ENDS NAMED. A session enters when this opener SPAWNS it
// (OpenHosted, for either destination: a remote destination carried by this
// daemon is still this daemon's) or when it RE-ATTACHES it after a restart
// (readoptLocal), and it leaves when the session is RELEASED (Release) — the
// same seam the re-attached connection leaves by, because that is where this
// process stops being the party that holds it.
//
// IT IS DELIBERATELY NOT CLEARED WHEN A CONNECTION DROPS (dropIfLost). A lost
// socket does not end the sessions behind it — the daemon owning them through
// its holder's process is the whole point — so a pane whose coordinator lost
// its socket is still this daemon's, and saying otherwise would turn a session
// that is still running into "no helper holds this".
//
// noteHeld records that this machine's daemon holds a session.
//
// It takes the opener's OWN lock rather than a second one: `held` is the same
// kind of state as `reattached` — which sessions this process has a claim on —
// and two locks over one object is how a deadlock is written.
func (o *localHelperOpener) noteHeld(sid session.ID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held == nil {
		o.held = make(map[session.ID]struct{})
	}
	o.held[sid] = struct{}{}
}

// forgetHeld drops a session this opener no longer holds, for the one case
// Release cannot cover: the daemon ANSWERED and the session was not in its
// answer, so there is no connection of ours to give up. A stale entry is worse
// than a missing one — the owner would route the pane here for a terminal that
// is gone, and the refusal a person reads would name the wrong helper.
func (o *localHelperOpener) forgetHeld(sid session.ID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.held, sid)
}

// holds answers whether this machine's daemon holds a session. It is what
// paneScreen.owner asks before it asks the remote registry, and it is the only
// question that routes an ssh pane whose destination is remote.
func (o *localHelperOpener) holds(sid session.ID) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.held[sid]
	return ok
}

// sshTargetResolver turns a host and its connect options into the destination
// this machine's helper dials, or refuses.
//
// It is NARROWER THAN the client that satisfies it, on purpose: the client also
// holds a dialer, and this opener needs the half of it that decides — not the
// half that connects. A seam that named the concrete client would let a later
// edit reach the dialer from here without either side noticing, which is the
// state nocx-50w7p.5 exists to end.
type sshTargetResolver interface {
	ResolveTarget(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DialTarget, error)
}

// setSSHTargets records the destination resolver an ssh open on this machine's
// helper is built from.
//
// It is a SETTER beside installedLocalGeneration for the same reason that one
// is: the composition root wires the opener at New, and the ssh client it
// resolves through is built later in the same function — at a point where the
// vault, the credential resolver and the profile store all exist. A constructor
// argument would force the earlier value to wait on the later one, which is the
// brain method this file keeps trying not to grow.
func (o *localHelperOpener) setSSHTargets(resolver sshTargetResolver) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sshTargets = resolver
}

// resolveTarget is the read side of the setter above, under the same lock, so a
// caller never holds a nil interface it just checked.
func (o *localHelperOpener) resolveTarget(ctx context.Context, host string, opts []ssh.ConnectOption) (ssh.DialTarget, error) {
	o.mu.Lock()
	resolver := o.sshTargets
	o.mu.Unlock()
	if resolver == nil {
		return ssh.DialTarget{}, errors.New(
			"open an ssh pane on this machine's helper: no destination resolver is wired")
	}
	return resolver.ResolveTarget(ctx, host, opts...)
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

// installFailed records why this machine's generation is NOT on disk, so the
// refusal raised when a person opens a pane can name the concrete error
// instead of only its absence (nocx-ie23r.4). It is called by the same start
// step that calls installedLocalGeneration, and the two are mutually
// exclusive by construction: an install either produced a generation or a
// reason there is none.
func (o *localHelperOpener) installFailed(cause error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.installFailure = cause
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

// installedHelperBinary answers the executable of the generation Start put on
// THIS machine, or empty when nothing was installed.
//
// It is the second fact a nested child's launch needs, one field beside
// toolEndpoint and for the same reason (nocx-1n56d): a sudo/su child runs on
// this machine, so the MCP adapter its shell execs is this machine's installed
// helper — the SAME path every local pane this machine opens already carries
// (helper/session's LocalSpawner takes it from the daemon's own
// os.Executable). Reading it from this process's environment, which is what
// the builder used to do, names a path that belongs to whoever launched the
// backend rather than to the child being composed: a backend started inside a
// pane inherits that pane's LOCAL path, which is another generation's binary
// (nocx-e2bws).
//
// Read under the opener's own lock, per grant, for the reason the endpoint is:
// the install happens at Start and this builder is wired at New.
func (o *localHelperOpener) installedHelperBinary() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.installed.Binary
}

// toolEndpoint answers the tool socket this backend runs, for the pane about
// to be spawned on it — or empty, which is the state a backend with no tool
// surface is in and not "not configured yet".
//
// It is read under the opener's own lock, and it is read PER PANE on purpose
// (nocx-50w7p.18): the endpoint belongs to the coordinator that opens the
// pane, and this opener serves several panes at once. Handing it to the daemon
// once — which is what a `NOCX_TOOL_SOCKET` in the daemon's own environment
// was — made it a fact about whichever coordinator started that daemon.
func (o *localHelperOpener) toolEndpoint() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.toolSocketPath
}

// OpenHosted opens a pane on this machine's helper.
//
// TWO KINDS OF DESTINATION ARE THIS OPENER'S, and since nocx-50w7p.5 they are
// the two a session can have. A LOCAL destination is a shell the daemon forks on
// this machine. A REMOTE one is a shell channel on a connection the daemon
// DIALS — `spawn-ssh`, a second op beside `spawn` because every shape on this
// wire is closed, so a destination folded into `spawn` would be a payload an
// older generation REJECTS while a new op is one it answers `unknown_op` to.
//
// That second arm is the owner's invariant made true: there is no ssh connection
// without a helper, so an ssh pane is a pane THIS machine's helper hosts, and the
// coordinator's own dial — the `svc.Open` fallback that used to answer a
// destination no helper claimed — is gone with it. ADR-0057 is why there is no
// third arm: locally there is no Tier A fallback, so a helper that cannot be
// reached is a refusal naming what failed rather than a second way to connect.
//
// It answers selected=false only for a destination that is not this machine's,
// which is now the empty Kind alone. A destination this opener owns is ALWAYS
// its own, including when it cannot be served — see the file header: answering
// "not mine" for a broken helper is exactly the fallback ADR-0057 refuses.
func (o *localHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	if o == nil || (cfg.Kind != session.KindLocal && cfg.Kind != session.KindRemote) {
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
	var res hostedSpawnResult
	if cfg.Kind == session.KindRemote {
		res, err = o.openSSH(ctx, spawn, cfg, claim)
	} else {
		// The shell, the argv and the environment are all the daemon's: D3
		// refuses any op whose params carry a free-form []string, and the helper
		// resolves the login shell through the same internal/loginshell this
		// coordinator used to ask. The shell-integration activation environment
		// travels with it: LocalSpawner renders NOCX_SHELL_INTEGRATION,
		// NOCX_PROMPT_MODE and NOCX_SESSION_ID into the script it hands the
		// shell on an inherited descriptor, which is where an integrated shell
		// has always exported them from.
		res, err = spawn.run(ctx, cfg, func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error) {
			return c.Spawn(ctx, proto.SpawnParams{
				Cwd: cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows,
				Lifecycle:      life,
				IdempotencyKey: claim,
				// THIS backend's own tool endpoint, carried per pane
				// (nocx-50w7p.18): the pane's tools belong to the coordinator
				// that opened it, and the daemon cannot know which of its
				// callers that is. Empty when this backend runs none, and the
				// pane then renders no NOCX_TOOL_SOCKET at all — the soft
				// degrade, stated above.
				AgentToolEndpoint: o.toolEndpoint(),
			})
		})
	}
	if err != nil {
		o.dropIfLost(c)
		return transport.HostedSessionOpen{}, true, fmt.Errorf("open a pane on this machine's helper: %w", err)
	}
	sid := res.Session.ID()
	if o.registry == nil {
		_ = res.Session.Close()
		return transport.HostedSessionOpen{}, true, errors.New("local helper opener has no session registry")
	}
	// THE THREE THINGS THAT ARE ABOUT A PROCESS ON THIS MACHINE ARE LOCAL ONLY,
	// and the launch record is what says so: a remote session's branch has no
	// pid and no pgid key AT ALL, because pid 0 — the only value left if a record
	// insisted on carrying one — is the kernel scheduler, and a reader that
	// trusted it would ask the OS about a process this machine never started
	// (helper/proto's launch union; D10's "the launch record is the authority").
	//
	// So the pid is recorded, the shell replacement is watched, and the
	// integration axis is filled in only for a launch that has one. The
	// integration axis is not merely skipped for a remote pane, it is left EMPTY,
	// which on that axis is "do not register" and is exactly right: a remote
	// session's launch-time refusal is the ssh channel's own answer, and
	// registerRemoteIntegration reads it there rather than from here
	// (transport.HostedSessionOpen's own comment).
	remote := res.Entry.RemoteLaunch
	shell := ""
	var (
		status string
		reason ssh.RefusalReason
	)
	if remote == nil {
		shell = res.Entry.Launch.Shell
		if err := o.registry.RecordOwnedProcessPID(sid, res.Entry.Launch.Pid); err != nil {
			_ = res.Session.Close()
			return transport.HostedSessionOpen{}, true, fmt.Errorf("recording local helper launch pid: %w", err)
		}
		status, reason = localIntegrationStatus(shell, res.LifecycleLane)
		o.watchForReplacement(res.Session, res.Entry.Launch.Pid, shell)
	}
	if res.LifecycleLane != "" && o.noteChildDomainParent != nil {
		o.noteChildDomainParent(res.LifecycleTransport, res.LifecycleLane, string(sid))
	}
	out := transport.HostedSessionOpen{
		Session: res.Session,
		// Host and Account are the DESTINATION, and their emptiness is the
		// distinction the readopt pass reads (nocx-ie23r.2, nocx-50w7p.5).
		//
		// A LOCAL launch leaves them empty: Generation says which id space the
		// session belongs to, which is true and is what a verdict needs, and the
		// rest of the route back is a socket this machine dials rather than a
		// saved connection. Filling them in with this machine's name would be
		// inventing a route readopt would then try to resolve as a profile.
		//
		// A REMOTE launch names them, because for that session they are the
		// destination and not this machine — the far host the helper's own launch
		// record echoed, and the account it authenticated as. They are taken from
		// THE HELPER'S RECORD rather than from cfg, which is the difference
		// between what this coordinator asked for and what the daemon actually
		// dialed.
		Generation:         generation,
		LifecycleLane:      res.LifecycleLane,
		StartLifecycle:     res.StartLifecycle,
		AbortLifecycle:     res.AbortLifecycle,
		ObserveOutputHoles: res.ObserveOutputHoles,
		IntegrationShell:   shell,
		IntegrationStatus:  status,
		IntegrationReason:  reason,
	}
	if remote != nil {
		out.Host = remote.Host
		out.Account = remote.User
	}
	// THE SESSION ENTERS THIS DAEMON'S SET (nocx-50w7p.5). It is noted for
	// BOTH destinations, because the set answers "who holds this terminal" and
	// this daemon does — whether the process inside is a PTY it forked here or
	// a shell channel it dialed. That is exactly the fact paneScreen.owner
	// cannot read off the session's kind.
	o.noteHeld(sid)
	// THE OTHER END OF THE SET, for a session that ENDS (nocx-50w7p.5). The
	// daemon reports the process's own end on the session, and an owner that
	// went on routing to it would be pointing at a terminal that is gone. It is
	// the shape watchForReplacement already uses on the local half — one
	// goroutine that ends with the session it watches, which is why a pane
	// costs one of these whether it is local or remote — and it is started for
	// BOTH destinations, because the remote one has no shell replacement to
	// watch and would otherwise have nothing observing its end at all.
	//
	// A LOST CONNECTION IS NOT THIS, and nothing is cleared when the socket
	// drops (dropIfLost): the daemon still holds the sessions behind it, which
	// is the whole reason a replacement can take them back.
	//
	// It deliberately does not select on the caller's context: an open's
	// context ends when the request that asked for the pane does, and a watcher
	// that stopped there would forget a session that is still running.
	go func() {
		<-res.Session.Done()
		o.forgetHeld(sid)
	}()
	return out, true, nil
}

// openSSH hosts an ssh pane on this machine's helper: the daemon dials the far
// host and the session's process is the shell channel it opens (nocx-50w7p.5).
//
// # The division this function is
//
// The DESTINATION IS RESOLVED HERE AND DIALED THERE. This half contributes the
// two things a helper may not decide for itself: the ADDRESS — an alias through
// ~/.ssh/config, merged with that config's own defaults — and the
// AUTHORIZATION, because a linked credential may only be spent on the endpoint
// its profile names and this process is the party holding that binding. What
// crosses is an address, an account and a REFERENCE to material, so the helper
// holds no secret at rest and cannot dial what this coordinator has not
// authorised (ssh.WireDestination's own comment, which is one conversion rather
// than a copy per consumer).
//
// # Why the claim rides this spawn
//
// L7's rule is that a pane's claim is durable BEFORE the first irreversible
// effect, and the effect here is a shell on somebody else's machine. A
// coordinator that dies between this spawn and the binding it writes leaves a
// claim naming a session nothing recorded, and the repeat passes the SAME key
// so the helper answers with the session the first attempt made instead of
// forking a second far shell (proto.SSHSpawnParams' comment on the field). That
// is why the key is a parameter rather than minted below, and why the empty
// string is not passed: an open with no claim is an open owed no promise.
func (o *localHelperOpener) openSSH(ctx context.Context, spawn hostedSpawn, cfg session.Config, claim string) (hostedSpawnResult, error) {
	if cfg.Remote == nil {
		// Unreachable from the wire — resolveRemote builds this before Kind is
		// set to Remote — and named rather than panicked on, because the two
		// facts are written by different statements in different files.
		return hostedSpawnResult{}, errors.New("open an ssh pane on this machine's helper: the config carries no destination")
	}
	target, err := o.resolveTarget(ctx, cfg.Host, session.SSHOptionsFromConfig(cfg.Remote))
	if err != nil {
		return hostedSpawnResult{}, fmt.Errorf("resolve the ssh pane's destination: %w", err)
	}
	// THE GENERATION THE LAUNCH NAMES HAS TO BE ON THE FAR SIDE BEFORE THE
	// LAUNCH IS ASKED FOR (nocx-50w7p.21).
	//
	// A helper-hosted ssh pane runs the SCRIPT carrier: the daemon builds
	// stage-1 and a start command that execs `$HOME/.nocx/launch`, and the far
	// shell's own first check is `[ -x "$HOME/.nocx/launch" ]` — which is a
	// fact only a PUBLISH creates. Until this line nothing published for this
	// route: the trigger lived in RealClient.Connect's startPublish, and that
	// whole dial half is compiled out of a coordinator built without
	// nocx_local_ssh (ssh_real_dial.go). So the bundle was never written, the
	// far side refused the generation, and the pane degraded to a conventional
	// terminal with a hello-timeout — the defect measured by the epic's e2e
	// (nocx-50w7p.21).
	//
	// IT IS SEQUENCED BEFORE THE SPAWN, NOT CONCURRENT WITH IT, and the
	// ordering is the design's rather than a preference: §6.1's barrier (steps
	// 4-5) exists so stage-1 cannot re-prove a generation while the write is
	// still in flight, and the party that would wait here is the DAEMON, which
	// has no gate to wait on — spawn_ssh.go says exactly that where it builds
	// its bootstrap plan with `Ordered` nil. What this call does is make that
	// honest: by the time the spawn op is sent, the publish has reached its
	// terminal outcome. The cost is on a wall-clock that only starts when the
	// shell exists, so it takes nothing from the far side's bootstrap budget.
	//
	// The FAILURE IS NOT A REFUSAL (design §6.1 step 5, §6.4): a publish that
	// could not commit leaves any generation already there byte-identical, so
	// the pane still opens and the far side decides for itself. It is logged
	// with its own cause rather than reported as this open's error, because
	// ADR-0004 makes an ordinary usable terminal the one thing no failure path
	// may suppress.
	if perr := o.publishForPane(ctx, cfg); perr != nil {
		// THE LOGGER IS OPTIONAL and the failure is not: a test that wires an
		// opener without one must still get the fail-open behaviour below
		// rather than a panic, so the log line is guarded rather than assumed.
		if o.log != nil {
			o.log.Warn("ssh pane: the shell integration bundle could not be published; the far side may find no generation",
				"host", cfg.Host, "error", perr)
		}
	}
	params := proto.SSHSpawnParams{
		Destination: ssh.WireDestination(target),
		// The two facts the far launcher is built for, read off the config the
		// registry accepted rather than off the caller's spec: the shell pin a
		// profile may carry (nocx-pu4.1) and the integration mode the resolver
		// concluded. Their vocabularies are the same closed sets, and an EMPTY
		// mode is not a gap to fill in here — it means "this destination was
		// never asked", which profile.DesiredMode's own gate answers as the
		// default does (AD-8, one owner for that question).
		Shell:       proto.SSHShellKind(cfg.Remote.Shell),
		DesiredMode: proto.SSHMode(cfg.Remote.DesiredMode),
		Cols:        cfg.Cols,
		Rows:        cfg.Rows,
		// NO Cwd, and the absence is the wire's rule rather than an omission:
		// this generation cannot move a far login shell, so `spawn-ssh` refuses a
		// non-empty cwd by name instead of accepting a value nothing acts on. The
		// launch record keeps the `cwd` key and reports it empty for the same
		// reason.
		IdempotencyKey: claim,
		// THIS backend's endpoint, per spawn, for the reason the local arm gives:
		// the pane's tools belong to the coordinator that opened it, and one
		// account's daemon serves several coordinators (D12). The two FAR-HOST
		// paths are deliberately left empty — a far socket path with no endpoint
		// here is refused rather than degraded, and this opener arranges no
		// far-side forward yet, so the honest request is the one that asks for no
		// tool surface at all.
		AgentToolEndpoint: o.toolEndpoint(),
		// THE PANE'S BEARER, MINTED WITH THE LAUNCH (nocx-50w7p.16). It has to
		// travel with the spawn because the far launcher renders it into the
		// frame the shell reads — and the shell is running before a person has
		// answered anything about this pane, which is why the interval that
		// gives it meaning cannot be what mints it.
		AgentToolToken: mintToolToken(),
	}
	res, err := spawn.run(ctx, cfg, func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error) {
		params.Lifecycle = life
		return spawn.client.SpawnSSH(ctx, params)
	})
	if err != nil {
		return res, err
	}
	// BOUND TO THE SESSION THE HELPER REPORTED, which is the id the interval
	// will be created under when this pane's agent enrols and a person answers.
	// The launch carried the bearer; this is the only place that learns which
	// session it was for.
	o.spawnTokens.record(res.Session.ID(), params.AgentToolToken)
	return res, nil
}

// publishForPane writes the shell-integration bundle onto the destination an
// ssh open is about to dial, through THIS MACHINE'S HELPER, when this pane's
// mode asks for the script carrier (nocx-50w7p.21).
//
// # It asks the value that already knows, and it asks twice for nothing
//
// `cfg.Remote.RemoteInstaller` is the installer the open was built with —
// stamped by the connection resolver for a saved profile and by the transport
// for a direct host (nocx-mlm7 P8) — and it is the SAME value
// internal/ssh's startPublish handed to this publish before this route
// existed. So there is no second publisher and no new seam: this file calls
// the carrier the config already carries, which is also what keeps one
// destination one authentication (helper_publish.go's pool key is the
// resolved destination).
//
// The MODE GATE is asked of the axis (`profile.DesiredMode.DeliversScripts`),
// the one owner of "does this destination integrate" (AD-8), and it is asked
// with the same input the daemon asks it with — the mode that is about to
// travel in the spawn params. A `raw` pane publishes nothing, and a nil
// installer publishes nothing, which is not a refusal: a build wiring no
// installer is a build that integrates nothing, and the open proceeds to the
// same plain shell either way.
func (o *localHelperOpener) publishForPane(ctx context.Context, cfg session.Config) error {
	remote := cfg.Remote
	if remote == nil || remote.RemoteInstaller == nil {
		return nil
	}
	if !profile.DesiredMode(remote.DesiredMode).DeliversScripts() {
		return nil
	}
	// The destination is named the way the pane names it — the host and the
	// options this open is about to resolve with — so the publish's lease and
	// the pane's channel land on ONE pooled connection rather than costing two
	// authentications for one machine.
	if err := remote.RemoteInstaller.EnsureInstalledRemote(
		ctx, cfg.Host, session.SSHOptionsFromConfig(remote)...); err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: %w", cfg.Host, err)
	}
	return nil
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
	// THE SESSION LEAVES THIS DAEMON'S SET HERE (nocx-50w7p.5). Release is the
	// seam at which this process stops being the party that holds the pane, so
	// it is the same seam the re-attached connection leaves by — one act, one
	// place, rather than a second concept with its own lifetime.
	delete(o.held, key)
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
	installed, dir, existing, reverse := o.installed, o.dir, o.client, o.reverse
	installFailure := o.installFailure
	o.mu.Unlock()

	if installed.Binary == "" || installed.Generation == "" || dir == "" {
		// WHY, not only THAT: a generation that was never installed carries
		// the install's own error with it when there was one, and the
		// sentinel's sentence when there was not (nocx-ie23r.4).
		if installFailure == nil {
			installFailure = errNoLocalGeneration
		}
		return nil, "", refuseLocalHelperNotInstalled(installFailure)
	}
	if existing != nil {
		return existing, string(installed.Generation), nil
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir: dir, Generation: installed.Generation, Binary: installed.Binary,
		Log: o.log,
		// The answers this coordinator gives the helper when it dials
		// (helper_reverse.go). Read under the lock with everything else the
		// connection is built from, so a connection is opened with ONE view of
		// what this coordinator can answer.
		Reverse: reverse,
	})
	if err != nil {
		return nil, "", refuseLocalHelperUnreachable(err)
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

// OpenHosted asks the far host's own helper first when the destination is
// remote, and THIS machine's helper when it is not — or when the far host's
// helper declined.
//
// The order is the epic's shape rather than a preference. A destination whose
// own machine runs a helper needs no channel from here at all: that pane is the
// far host's local shell, and the install that made it so already rides this
// machine's helper rather than a dial of the coordinator's. Everything else —
// no helper there, or none the consent decision allows — is a pane THIS
// machine's helper dials and hosts, which is what replaced the coordinator's own
// fallback dial (nocx-50w7p.5).
//
// The claim travels BOTH ways, because both spawns are irreversible: a repeat
// that reaches either helper must answer with the session the first attempt made
// rather than fork a second shell.
func (h *hostedOpeners) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	if cfg.Kind == session.KindLocal {
		return h.local.OpenHosted(ctx, cfg, claim)
	}
	if opened, selected, err := h.remote.OpenHosted(ctx, cfg, claim); selected {
		return opened, true, err
	}
	return h.local.OpenHosted(ctx, cfg, claim)
}

var _ transport.HelperSessionOpener = (*hostedOpeners)(nil)
