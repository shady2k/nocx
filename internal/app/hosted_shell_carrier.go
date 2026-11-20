package app

// The separate hosted-shell carrier remains here until stages .7/.8.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/rows"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// helperInstallProvider is the full composition-root surface the factory
// needs to bring a helper up on a host: the exec lane the helper rides
// (nocx-50w7p.10), the write-capable install lease the deploy package installs
// through (D7), and the bounded one-shot exec the platform probe uses (D20).
// The interface exists so the factory is testable against doubles without a
// live connection — and since nocx-50w7p.9 the composition root wires
// installLeaseRoutes, where ALL THREE are this machine's helper's: the lane as
// a channel it opened, the install lease as the sftp channel it opened, and the
// platform probe as a named op on a probe lease. No field of that dispatch is
// the coordinator's own dial any more.
//
// The registry itself keeps the narrow laneProvider — install is a
// selection-time concern, not a per-session one.
type helperInstallProvider interface {
	laneProvider
	HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error)
	DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error)
	// HelperInflate runs one fixed command on the host — its own gzip, over the
	// file the install just wrote — and is the install's ONE optimisation seam
	// (nocx-evj4b.1.3). It reports every failure, including a host without
	// gzip, and the install falls back to the plain upload on each.
	HelperInflate(ctx context.Context, host, path string, opts ...ssh.ConnectOption) error
}

// farPaneToolSocketter is the far pane's tool surface as the helper registry
// needs it (nocx-e2bws): the three routes resolved and the directory made before
// the spawn, the listener opened after it, and the teardown that ends both when
// the session does.
//
// NARROW ON PURPOSE, one consumer wide. It is the rule that already keeps
// laneProvider and installLeaseProvider apart, applied to the fourth thing this
// coordinator asks of a machine: an implementer of "bring a helper up on a host"
// must not grow "bind a socket on it", and a test double for the first must not
// fake the second. The composition root wires both from ONE value — they are the
// same owner — and that is a wiring decision rather than an interface one.
type farPaneToolSocketter interface {
	PrepareFarPaneToolSocket(ctx context.Context, host string, opts []ssh.ConnectOption, name string) (farPaneToolRoutes, error)
	OpenFarPaneToolSocket(ctx context.Context, host string, opts []ssh.ConnectOption, routes farPaneToolRoutes, session string) (proto.ForwardID, error)
	CloseFarPaneToolSocket(ctx context.Context, id proto.ForwardID) error
	RemoveFarPaneToolSocketDir(ctx context.Context, host string, opts []ssh.ConnectOption, routes farPaneToolRoutes) error
	// RecordPaneBearer binds a far pane's launch bearer to the session the far
	// helper reported, in the book the approval service reads.
	RecordPaneBearer(sid session.ID, token string)
}

func accountFromOptions(opts []ssh.ConnectOption) string {
	var cfg ssh.ConnectConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg.User
}

// newHostedShellRegistry owns the separate carrier for helper-hosted shells.
// Git never uses these carriers; its client belongs to farhelper.
func newHostedShellRegistry(lanes helperInstallProvider, source deploy.ArtifactSource, store *consent.Store, logger *slog.Logger) *helperRegistry {
	return &helperRegistry{
		lanes: lanes, install: lanes, source: source, log: logger, consent: store,
		hosts:           make(map[session.ID]*hostedShellCarrier),
		pendingReadopts: make(map[session.ID]*hostedShellCarrier),
		closing:         make(map[string]struct{}),
	}
}

// probeHold is the pooled reference a platform probe ran on, held past the
// probe itself.
//
// # The interval it is, with both ends named
//
// It OPENS where the lease is taken in probeHelperPlatformHeld below: that is
// the dial, and on a destination whose helper cannot be used it is also the
// far host's only authentication for the whole open. It CLOSES at release,
// which the open path defers until the pane it opened holds a reference of its
// own — hostedOpeners.OpenHosted runs it after the local `spawn-ssh` (or the
// far helper's lane) has come up, and helperRegistry.OpenHosted runs it
// immediately, because a caller with nothing to open has nothing to hold it
// for.
//
// WHY IT IS HELD AT ALL (nocx-k6p18.35). The selection probe runs BEFORE the
// session exists, so when it declines — the ordinary answer for a destination
// that ships no far-side helper — the connection it authenticated is the one
// the pane's own `spawn-ssh` is about to ask for. Released there, the helper's
// unlease drops the pool's last reference, the connection closes, and the spawn
// dials and authenticates again a few milliseconds later: two logins on
// somebody else's host for one pane, which is what the epic's e2e counted
// (`cmd/e2e-sshd` saw 2). Held across the open, the spawn's acquisition is a
// cache hit on the same ref-counted entry and the host authenticates once.
//
// release is nil-safe and idempotent, and both properties are load-bearing: a
// destination that never reached a lease holds nil, and the decline, the
// failure and the success paths all release exactly once.
type probeHold struct {
	lease ssh.DiscoveryConn
	once  sync.Once
}

func (h *probeHold) release() {
	if h == nil {
		return
	}
	h.once.Do(func() { _ = h.lease.Close() })
}

// probeHelperPlatformHeld is the same probe with its lease LEFT OPEN, and it is
// the held half of the interval probeHold documents: what it returns is a
// reference the CALLER must release.
//
// A non-nil hold is returned whenever a lease was taken, INCLUDING on the two
// failures below — a probe whose command failed, and a platform no artifact
// exists for. Neither means the connection is unusable, and the open path keeps
// it for exactly that reason; a hold whose connection died is evicted by the
// pool on the next acquisition, so holding a dead one costs nothing.
func probeHelperPlatformHeld(ctx context.Context, host string, opts []ssh.ConnectOption, lanes helperInstallProvider, source deploy.ArtifactSource) (*probeHold, string, deploy.Platform, bool, error) {
	// The probe dials with the interactive rung removed, and the reason is
	// the open path rather than the git one. Selection runs BEFORE the
	// session exists (OpenHosted), so this dial is the reference the whole
	// open is built on: it authenticates, and the pane's own spawn a moment
	// later rides that same pooled connection rather than paying for a second
	// login (probeHold). A dial that had to STOP AND ASK a person, on the
	// other hand, would raise a prompt for a question the product asked
	// itself, in front of a user who then has to answer it again for the real
	// session — the ask belongs to the pane, not to a probe (nocx-bzac4).
	//
	// Suppressing the ask is the choice because it is a rule this codebase
	// already has: a probe answers a question the product asked itself and may
	// not stop a person to do it (ssh.WithoutPasswordPrompt, which is what
	// un-wires the rung; a prompt credential reaches this probe as
	// ssh.ErrNoAuthMethod instead, refused before any dial). Every
	// silent credential still applies, so a key, an agent or a remembered
	// password probes exactly as before; only the destination that would have
	// to interrupt someone declines — and declining degrades to the plain
	// terminal, which is the direction §4.2 requires.
	//
	// The option list is COPIED before the suppression is appended: the
	// caller's slice is the destination's own options and is used again for
	// the install lease and the helper channel, both of which are the user's
	// chosen action rather than a probe.
	probeOpts := make([]ssh.ConnectOption, 0, len(opts)+1)
	probeOpts = append(probeOpts, opts...)
	probeOpts = append(probeOpts, ssh.WithoutPasswordPrompt())
	probe, err := lanes.DiscoveryConn(ctx, host, probeOpts...)
	if err != nil {
		// No lease, so no hold: the reference this function would hand back was
		// never taken, and the caller's release is a no-op on nil.
		return nil, "", deploy.Platform{}, false, fmt.Errorf("probe lease for %s: %w", host, err)
	}
	// NO deferred Close here, and that absence IS this function: the reference
	// travels back to the caller, which releases it once the interval it
	// belongs to is over (probeHold's own comment).
	hold := &probeHold{lease: probe}
	fingerprint := ""
	if fp, ok := probe.(interface{ HostKeyFingerprint() string }); ok {
		fingerprint = fp.HostKeyFingerprint()
	}
	platform, err := deploy.Probe(ctx, probeExec{probe})
	if err != nil {
		return hold, fingerprint, deploy.Platform{}, false, err
	}
	if _, _, aerr := source.Artifact(platform); aerr != nil {
		return hold, fingerprint, platform, false, aerr
	}
	return hold, fingerprint, platform, true, nil
}

func installHelperAt(ctx context.Context, host string, opts []ssh.ConnectOption, lanes helperInstallProvider, source deploy.ArtifactSource, platform deploy.Platform) (installedHelper, error) {
	conn, err := lanes.HelperInstallConn(ctx, host, opts...)
	if err != nil {
		return installedHelper{}, fmt.Errorf("install lease for %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()
	home, err := conn.Home()
	if err != nil {
		return installedHelper{}, fmt.Errorf("remote home for %s: %w", host, err)
	}
	fsys := installFS{conn}
	progress, endRow := installRowReporter(ctx, installMachine(host, opts))
	// The row is about the INSTALL and nothing after it: a completed install
	// has no duration left to show, and the prune below is cleanup rather
	// than the work the person was watching. Both ends are deferred, so a
	// failed or cancelled install takes its row with it.
	defer endRow()
	// THE INSTALL'S ONE OPTIMISATION: the artifact's own gzip bytes crossing the
	// link, and the host's gzip writing the file (nocx-evj4b.1.3). Every failure
	// of this seam — no gzip there, an exec the server refuses — is answered by
	// the plain upload inside Ensure, so nothing here is a requirement.
	// AND THE TOOL IS CONFIRMED BEFORE ANYTHING IS SENT: one fixed command, so a
	// host without gzip installs the plain way instead of paying the compressed
	// upload first and the decompressed one after it (nocx-evj4b.1.3).
	var installOpts []deploy.Option
	if probeErr := lanes.HelperInflate(ctx, host, "", opts...); probeErr == nil {
		inflate := func(ctx context.Context, path string) error {
			return lanes.HelperInflate(ctx, host, path, opts...)
		}
		installOpts = append(installOpts, deploy.WithInflate(inflate))
	} else {
		log.From(ctx).Debug("helper install: the host cannot decompress, sending the artifact plain", "host", host, "error", probeErr)
	}
	command, hash, err := deploy.Ensure(ctx, fsys, source, home, platform, progress, installOpts...)
	if err != nil {
		return installedHelper{}, err
	}
	if err := deploy.Prune(ctx, fsys, home, path.Base(path.Dir(command))); err != nil {
		return installedHelper{}, fmt.Errorf("prune for %s: %w", host, err)
	}
	return installedHelper{dir: path.Dir(command), generation: hash, command: command}, nil
}

// installRowReporter binds the install's progress to the Operations row the
// caller's invocation carries (ADR-8eej3e66 §8), and answers a Progress that
// is nil when nobody is watching. This reporter observes hosted installation;
// a Git consultation does not install through this registry.
//
// The row appears on the FIRST progress the install reports, which is after
// the completeness check and the removal of an unfinished tree. That is the
// point of the requirement rather than an accident: an install that is not
// needed reports nothing, so no row exists for it — and a repeat open of a
// host whose helper is already complete is therefore silent.
func installRowReporter(ctx context.Context, machine string) (deploy.Progress, func()) {
	reporter, ok := rows.ReporterFrom(ctx)
	if !ok {
		return nil, func() {}
	}
	return func(stage deploy.Stage, written, total int64) {
		reporter.Show(rows.Row{
			Kind:    "install",
			Title:   installRowTitle(stage),
			Machine: machine,
			Done:    written,
			Total:   total,
		})
	}, reporter.End
}

// installRowTitle is what the install is doing, in the person's terms. The
// stages are the deploy package's closed set; an unknown one says the least it
// can rather than claiming a stage it is not in.
func installRowTitle(stage deploy.Stage) string {
	switch stage {
	case deploy.StageCopying:
		return "Copying the nocx helper"
	case deploy.StageChecking:
		return "Checking the nocx helper"
	default:
		return "Installing the nocx helper"
	}
}

// installMachine names the destination the way the rest of the product does
// (frontend/src/machine-name.ts): user@host, or the bare host when no account
// is known. The port is not part of that name — the resolver hands this
// package "host:port" and the tab strip would say "host" — so it is stripped
// here rather than left for a row to disagree with the tab it came from. The
// two are one spelling of one machine, and the one the renderer already owns
// is the authority.
func installMachine(host string, opts []ssh.ConnectOption) string {
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	if account := accountFromOptions(opts); account != "" {
		return account + "@" + host
	}
	return host
}

// installedHelper is a COMPLETED install of the helper artifact on one host, in
// the facts a lane names it by (D7, D21): WHERE it is (the install directory,
// which is the machine's identity as this level records it — consent.Install
// stores the same directory) and WHICH build it is (the generation, i.e. the
// content hash).
//
// Those are the two things the coordinator sends in proto.LaneParams, and the
// reason they travel as facts rather than as the command they determine is D3:
// the helper builds the invocation from its own install layout
// (deploy.InstalledBinary), so no caller ever reaches an argv on somebody
// else's machine (nocx-50w7p.10).
type installedHelper struct {
	dir        string
	generation string
	// command is the installed binary's absolute path — dir joined with the
	// install layout's binary name. It is NOT sent to the helper (the lane
	// names the directory and the helper appends the name and the subcommand)
	// and it is kept for the two readers that need a file rather than a
	// directory: the footprint record's Path (its parent) and the hosted-open
	// answer that reports which binary serves a session.
	command string
}

// machine is the typed identity a lane is opened with.
func (i installedHelper) machine() proto.Machine { return proto.Machine{Dir: i.dir} }

// hostModeFor is the integration method the HOST keeps (ADR-727g63tr decision
// 1), read from the record by the host-key fingerprint a dial observed. It is
// the ONE answer to "which method does this host use", so two connections to
// one machine — from two profiles, or from a profile and a typed line — resolve
// the same mode.
//
// A host with no record, and a caller with no store (a test's shape, never the
// composition root's), is auto: "not yet answered" (ADR-0033), which is the
// connect-time ask. The store's own Lookup is nil-safe, so the nil case needs
// no test of its own here.
func hostModeFor(modes *hostmode.Store, fingerprint string) hostmode.Mode {
	if mode, ok := modes.Lookup(fingerprint); ok {
		return mode
	}
	return hostmode.Auto
}

// probeExec adapts a DiscoveryConn to the deploy package's ExecOnce: the
// platform probe's one bounded command. DiscoveryConn.Exec is the existing
// bounded exec capability (output cap, cancellation, session-refusal
// classification); the adapter exists only because deploy declares its own
// narrow seam and must not import internal/ssh.
type probeExec struct{ conn ssh.DiscoveryConn }

func (e probeExec) Exec(ctx context.Context, cmd string) ([]byte, error) {
	res, err := e.conn.Exec(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitStatus != 0 {
		return nil, fmt.Errorf("deploy probe: command exited %d", res.ExitStatus)
	}
	return res.Stdout, nil
}

// installFS adapts an ssh.HelperInstallConn to the deploy package's
// RemoteFS seam: the same shape, with each package's own File type name.
// Create is the one method whose return type differs; the rest pass
// through.
type installFS struct{ conn ssh.HelperInstallConn }

func (a installFS) Lstat(p string) (fs.FileInfo, error) { return a.conn.Lstat(p) }
func (a installFS) Mkdir(p string, m fs.FileMode) error { return a.conn.Mkdir(p, m) }

func (a installFS) Create(p string, m fs.FileMode) (deploy.File, error) {
	f, err := a.conn.Create(p, m)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (a installFS) SyncDir(p string) error                  { return a.conn.SyncDir(p) }
func (a installFS) Rename(s, d string) error                { return a.conn.Rename(s, d) }
func (a installFS) Remove(p string) error                   { return a.conn.Remove(p) }
func (a installFS) ReadDir(p string) ([]fs.FileInfo, error) { return a.conn.ReadDir(p) }
func (a installFS) ReadFile(p string) ([]byte, error)       { return a.conn.ReadFile(p) }
func (a installFS) Chmod(p string, m fs.FileMode) error     { return a.conn.Chmod(p, m) }
func (a installFS) Sync(p string) error                     { return a.conn.Sync(p) }

// helperRegistry owns the separate carriers used by hosted shells, keyed by
// their session ids. Git bindings consume farhelper leases and do not acquire
// or release these entries. A successfully opened carrier belongs to this
// registry from openFarHelper's registration until SessionEnded, uninstall, or
// coordinator shutdown closes it; a failed open closes its unregistered carrier
// in its own rollback. Readoption stages a carrier outside hosts until adoption
// transfers it here, so an unfinished readopt cannot claim a live inventory.
// Carrier loss alone does not end a far session: inventory can reach the same
// daemon again through a fresh lane without forgetting the hosted entry (D1).
type helperRegistry struct {
	lifetime *sessionLifetimeSetting
	lanes    laneProvider
	install  helperInstallProvider
	source   deploy.ArtifactSource
	// publishScreen is the coordinator transport's owner of session.frame
	// metadata; the far-helper attachment forwards each reassembled snapshot
	// to that existing screen plane rather than deriving CWD a second time.
	publishScreen func(sid session.ID, revision uint64, doc []byte) bool
	// publishEffect and blockRows are the same two transport seams the local
	// hosted opener binds (helper_hosted.go): a far pane's runtime effects
	// (notifications, OSC 52) and its streamed block output. The far route
	// bound neither (nocx-xn63t.7), so the coordinator had no rows source for
	// a far pane, never sent block.closed, and every block on an ssh pane
	// stayed "Running" after its command ended — a Stop that had stopped the
	// command looked as though it had stopped nothing.
	publishEffect func(sid session.ID, effect proto.EffectFrame) bool
	blockRows     blockRowsSink
	log           *slog.Logger
	consent       *consent.Store
	// modes is the HOST record: the integration method each machine keeps,
	// keyed by host-key fingerprint (ADR-727g63tr decision 1). It is the one
	// answer to "which method does this host use" for every connection to it,
	// and it is a different question from consent beside it. Nil (a test
	// double, or a build that wired none) reads every host as auto — not yet
	// answered — which is the ask.
	modes     *hostmode.Store
	registry  *session.Reg
	lifecycle lifecyclechannel.Kernel
	// lifecycleLoss carries a helper-hosted adapter's loss cause to the
	// session integration axis, the same seam the local pty factory uses.
	// It is a separate seam from the published facts for the same reason it
	// is there: a channel that ends establishes nothing new, so no fact
	// moves and the product would otherwise learn nothing (nocx-dvql).
	// Nil (tests, or a server without the wiring) reports nowhere and the
	// loss is still logged by the adapter.
	lifecycleLoss func(lane lifecycle.LaneID, loss lifecyclechannel.Loss)
	// environmentEntries is the lane -> downlink registry
	// (environment_entry.go, nocx-2v80t.3.21), the same one the local route
	// uses. Nil wires nothing.
	environmentEntries *environmentEntryRegistry
	// lifecycleCursors keeps every pane's lifecycle cursor with its binding
	// (lifecycle_cursor.go, ADR-0077): this route's opens write it, and the
	// re-adopt pass reads the one the store carried over. Nil keeps nothing.
	lifecycleCursors lifecycleCursorStore
	// lifecycleStopping is raised when the coordinator begins stopping: a
	// lifecycle frame arriving after it is left for the next coordinator
	// (lifecycleCursor.applyFrame). Nil never stops.
	lifecycleStopping *atomic.Bool
	// transports is the composition root's record of what each lifecycle
	// transport is (internal/app's transportRegistry), and this route is ONE OF
	// ITS WRITERS (nocx-8gg8y). A far pane's channel is an adapter this
	// registry builds, so it is the only party that knows the transport exists
	// — and while nothing registered it, every question keyed on it answered
	// "unknown": the agent launch resolver read a launch on an ssh pane as an
	// unregistered local pane, refused it with a sentence naming none of its
	// causes, and the pane could not start the agent a person had typed. Nil
	// leaves the same silence as before, which is a test's shape and not the
	// composition root's.
	transports *transportRegistry
	// registerLane binds a far pane's lane to the session that will speak for
	// it. The transport registers the same pair for the FACTS; this seam exists
	// because the coordinator's own lane→session map (childSessions) is what
	// the agent enroller reads, and a far pane's enrolment would otherwise be
	// refused as a lane nobody owns. It is the same closure the local opener
	// and the remote lifecycle provider are handed.
	registerLane    func(lane lifecycle.LaneID, sid string)
	mu              sync.Mutex
	hosts           map[session.ID]*hostedShellCarrier
	pendingReadopts map[session.ID]*hostedShellCarrier
	closing         map[string]struct{}
	// farTools are the far-side tool sockets this registry opened, keyed by the
	// session each belongs to (nocx-e2bws). They are held HERE rather than by
	// the hostedShellCarrier because the event that ends them is a session's end and not
	// a helper's: the transport tells this coordinator a session is over, and the
	// map from a session to what that session was given is exactly what the
	// teardown needs to find the listener and its directory.
	farTools map[session.ID]farToolSocket
	// tools is this coordinator's far tool surface, or nil when this build wires
	// none: a nil surface is a pane opened conventionally and truthfully (no
	// socket, and the far host named as the reason), never a failure.
	tools farPaneToolSocketter
}

// farToolSocket is one far pane's tool socket as this coordinator holds it: the
// three routes it was resolved from, the connection they belong to, and the
// listener id `unforward` ends. It is a value and not a pointer because a
// teardown that took it out of the map owns it completely.
type farToolSocket struct {
	host   string
	opts   []ssh.ConnectOption
	routes farPaneToolRoutes
	id     proto.ForwardID
}

// farToolTeardownTimeout bounds the two acts a session's end owes the far host.
// They are ACTS and not a loop — one unforward, one sftp removal — and the bound
// exists so a far side that has stopped answering cannot hold the transport's
// own teardown behind them.
const farToolTeardownTimeout = 10 * time.Second

// farHelperAnswerTimeout bounds ONE answer the far helper owes an open: its
// spawn, and the attach that makes the session a pane (nocx-398ca).
//
// It is the same budget the lifecycle adapter already grants the shell after
// the spawn (remoteShellSetupTimeout): a spawn that has not answered in the
// time the product allows the shell it started to prove itself is not slow,
// it is stopped. The context an open carries is the WS request's, which lives
// as long as the renderer's promise — so a helper that accepted the request
// and went silent held the open, and with it one of the execution lane's
// eight permits, until the server died. The owner met exactly that: eight
// stuck opens, then "Control plane busy" on every other action until the
// process was killed (nocx-3an1s.1.1). The lane is gone now (ADR-8eej3e66
// stage 8), and the bound stays because an open that never answers is still a
// call that never settles.
//
// A timed-out spawn may still have forked a shell on the far host; the client
// is closed with the failure, the helper keeps that session (D1) and the
// unclaimed-session sweep retires it. The pane gets an error rather than a
// cursor, which is the trade this bound makes.
//
// A var, not a const: the silent-helper test shrinks it rather than waiting
// out the production budget.
var farHelperAnswerTimeout = remoteShellSetupTimeout

// farHelperAnswerError says WHICH answer never came when the bound above is
// what ended the wait. Every other failure keeps its own error: a refusal, a
// lost connection and a bad request are the far side's answers and already
// name themselves.
func farHelperAnswerError(what string, err error) error {
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("the far helper did not answer the %s within %s: %w", what, farHelperAnswerTimeout, err)
}

// beginFarToolSocket registers one far pane's socket against its session BEFORE
// the listener exists, and answers false when the session is ALREADY over.
//
// # Why before, and what the false means
//
// Opening a listener on somebody else's host is a round trip, and a session can
// end inside it — a pane whose program exited, a tab closed, a transport that
// withdrew the watch. A registration made after the open would miss that end and
// the listener would outlive the session it was opened for, which is exactly the
// invariant this whole path exists to keep (ADR-0058). So the ENTRY comes first,
// with no listener id in it, and the id is filled in by completeFarToolSocket.
//
// false means the end ran while the listener was being opened: the entry is gone
// (and the directory with it), so the CALLER owns the listener it just created
// and must close it itself. That is the only case where a far tool socket is
// closed by the opener rather than by the session's end.
func (r *helperRegistry) beginFarToolSocket(sid session.ID, ts farToolSocket) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.farTools == nil {
		r.farTools = make(map[session.ID]farToolSocket)
	}
	if _, ended := r.farTools[sid]; ended {
		// Unreachable in production — one open per session — but a second
		// open for a session already registered must not overwrite the entry
		// the teardown is about to read.
		return false
	}
	r.farTools[sid] = ts
	return true
}

// completeFarToolSocket fills in the listener id of a registration made before
// the open, and answers whether the SESSION IS STILL LIVE: false means its end
// ran during the open, the entry went with it, and the caller owns the teardown.
func (r *helperRegistry) completeFarToolSocket(sid session.ID, id proto.ForwardID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	held, live := r.farTools[sid]
	if !live {
		return false
	}
	held.id = id
	r.farTools[sid] = held
	return true
}

// SessionEnded ends the far-side tool socket of a session whose output is over:
// the listener first, then the directory it was bound in (nocx-e2bws).
//
// IT IS THE CLOSING EDGE OF AN INTERVAL THIS FILE OPENS in openFarHelper, and
// the event is the transport's rather than this registry's: a session's output
// ending is the fact that makes the far pane unable to produce another agent
// call, and it is the same fact that retires the pane's bearer — so the socket
// and the credential it carried end together, which is what "no listener
// outlives its session" means in practice (ADR-0058).
//
// A failure is LOGGED and not returned: the session is already over, the caller
// is the transport's teardown, and a far host that refused to remove a directory
// is not a state anybody can act on from here — but it is not silence either,
// because a directory left behind is a socket path a later pane's bind could
// fail on, and whoever reads this line is the one who can see that.
//
// It is idempotent: the entry is taken out of the map before anything is done,
// so a second end for one session finds nothing and does nothing.
//
// The hosted shell owns its carrier until this session ends.
func (r *helperRegistry) SessionEnded(sid session.ID) {
	r.mu.Lock()
	_, ok := r.farTools[sid]
	h, hostedOK := r.hosts[sid]
	r.mu.Unlock()
	if hostedOK {
		h.sessionEnded()
	}
	if !ok {
		return
	}
	r.tearDownFarToolSocket(sid)
}

// tearDownFarToolSocket takes the registration out of the map and ends what it
// names — the directory FIRST and the listener second — and it is one function
// because two callers own it: a session's end (SessionEnded), and the opener
// that finds the session already over when its listener comes up.
//
// THE ORDER IS THE POINT. The directory goes while the listener still holds the
// pooled reference that keeps the connection to that host up, so the removal is
// an operation on a connection that is already there rather than a dial of its
// own; the listener is ended last, and with it the reference. A socket whose
// file is gone answers nobody in between, so no agent can dial into the gap.
func (r *helperRegistry) tearDownFarToolSocket(sid session.ID) {
	r.mu.Lock()
	held, ok := r.farTools[sid]
	delete(r.farTools, sid)
	r.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), farToolTeardownTimeout)
	defer cancel()
	// A ZERO ID IS A LISTENER STILL BEING OPENED: the session ended inside that
	// round trip, and completeFarToolSocket is about to answer false — so the
	// OPENER closes the listener, and what is owed here is the directory (which
	// the open may also have failed on). Nothing is skipped: this removal takes
	// the socket file with it, so a listener that comes up microseconds later is
	// closed by its opener into a directory that is already gone.
	if held.id.IsZero() {
		if err := r.tools.RemoveFarPaneToolSocketDir(ctx, held.host, held.opts, held.routes); err != nil {
			r.log.Warn("far pane: the tool socket's directory was not removed",
				"session", sid, "host", held.host, "dir", held.routes.Dir, "error", err)
		}
		return
	}
	if err := r.tools.RemoveFarPaneToolSocketDir(ctx, held.host, held.opts, held.routes); err != nil {
		r.log.Warn("far pane: the tool socket's directory was not removed",
			"session", sid, "host", held.host, "dir", held.routes.Dir, "error", err)
	}
	if err := r.tools.CloseFarPaneToolSocket(ctx, held.id); err != nil {
		r.log.Warn("far pane: the tool socket was not closed; its session is over",
			"session", sid, "host", held.host, "path", held.routes.Path, "error", err)
	}
}

// OpenHosted resolves the hosted shell delivery mode, then spawns
// and attaches through the helper ABI. The returned session id is the helper's
// id; the coordinator never mints a replacement.
//
// THE HOLD IS RELEASED HERE, and for this caller that is the whole of it: a
// caller with nothing to open on the connection has nothing to hold it for. The
// open PATH is the caller that has something — the pane — and it releases the
// hold after the pane exists instead (hostedOpeners.OpenHosted).
//
// claim is L7's idempotency key, and it is the caller's rather than this
// function's because the DURABLE part of it — the row written before the first
// irreversible effect — belongs to the session-open path above both openers.
// Carrying it here is what makes a repeat after a coordinator died between the
// spawn and that row answer with the session the first attempt made instead of
// forking a second shell on somebody else's machine (nocx-50w7p.5). Empty means
// no claim was written and this spawn is owed no such promise.
func (r *helperRegistry) OpenHosted(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, bool, error) {
	opened, hold, selected, err := r.openHoldingLease(ctx, cfg, claim)
	hold.release()
	return opened, selected, err
}

// openHoldingLease is OpenHosted with the selection probe's pooled reference
// HANDED BACK rather than released, and the hold is the caller's to release.
//
// # Why the distinction exists at all (nocx-k6p18.35)
//
// The platform probe an open begins with is a DIAL: it authenticates against
// the far host to ask one question. On a destination whose own helper is
// declined — the ordinary answer for a host that ships no helper, and the whole
// of the epic's e2e — the pane is opened a moment later by THIS machine's helper
// through `spawn-ssh`, on the SAME pooled connection under the same key. If the
// probe's reference is dropped in between, the helper's unlease closes that
// connection as its last reference and the spawn authenticates a second time:
// two logins on somebody else's host for one pane.
//
// So the reference travels out of here instead, and its interval has two named
// ends, both in code: it OPENS in probeHelperPlatformHeld, which is where the
// lease is taken, and it CLOSES at the caller's release — after the arm that
// opened the pane has taken a reference of its own. For a destination the far
// host's own helper serves, that arm is openFarHelper below and the hold is
// released by the caller the moment it returns; for a destination it declines,
// it is the local opener's `spawn-ssh` (hostedOpeners.OpenHosted).
//
// A HOLD IS RETURNED WHENEVER A LEASE WAS TAKEN, including on the two decline
// arms, and nil when the destination never reached one — a kind this route does
// not serve, or a lease the helper refused to hand out. probeHold.release is
// nil-safe and idempotent, so no arm has to test for either.
func (r *helperRegistry) openHoldingLease(ctx context.Context, cfg session.Config, claim string) (transport.HostedSessionOpen, *probeHold, bool, error) {
	if cfg.Kind != session.KindRemote || cfg.Remote == nil || r.install == nil || r.registry == nil {
		return transport.HostedSessionOpen{}, nil, false, nil
	}
	opts := session.SSHOptionsFromConfig(cfg.Remote)
	endProbe := diagnostics.Stage(ctx, diagnostics.StageProbe)
	hold, fingerprint, platform, available, err := probeHelperPlatformHeld(ctx, cfg.Host, opts, r.install, r.source)
	endProbe(err)
	// THE MODE ARRIVES WITH THE FINGERPRINT, and that order is the design
	// rather than an accident: the record is keyed by the machine's host key,
	// so which method this host uses is answerable only once a dial has seen
	// one. Before that — a host nobody has connected to, or a probe that got
	// far enough to offer a key but no further — the answer is auto, "not yet
	// answered" (ADR-0033), which is the connect-time ask. `mode` below is
	// therefore read from the record and never from the connection's stored
	// delivery mode (ADR-727g63tr decision 1 supersedes that place).
	mode := hostModeFor(r.modes, fingerprint)
	if err != nil {
		// The connect-time helper ask (ADR-0068, owner's decision
		// 2026-09-16). An unknown or changed host key is the ONE probe
		// failure that also carries a fingerprint (deterministic from the
		// offered key bytes, independent of trust — ssh.ErrUnknownHostKey /
		// ErrHostKeyMismatch), so it is the one case that can need this ask
		// before the key itself is even trusted. Every other probe failure
		// (unreachable, no auth material, uname exec failed after the dial
		// succeeded) is answered exactly as before: swallowed here, so the
		// local opener's own dial a moment later produces the real,
		// definitive refusal.
		if ask := helperConsentAskForProbeFailure(cfg.ProfileID, cfg.Host, offeredHostKeyFingerprint(err), err, r.consent, r.modes); ask != nil {
			return transport.HostedSessionOpen{}, hold, true, ask
		}
		if fingerprint == "" {
			fingerprint = offeredHostKeyFingerprint(err)
		}
		mode = hostModeFor(r.modes, fingerprint)
		if fingerprint == "" {
			// known_hosts lists acceptable keys, not the key this connection
			// authenticated. Defer delivery until the local opener has a lease.
			mode = hostmode.Raw
		}
		cfg.Remote.IntegrationMode = string(mode)
		return transport.HostedSessionOpen{}, hold, false, nil
	}
	// THE OPEN'S OWN CONFIG CARRIES THE HOST'S ANSWER. Every later reader of
	// this config takes the mode from it — the local opener's spawn (which
	// decisions the shell scripts from the mode), the open ack's
	// desiredMode, and the integration axis that reads the ack's gate — so
	// stamping it here is what makes one host resolve one mode for the whole
	// open rather than only for the helper selection. cfg.Remote is a pointer
	// and this is deliberate: the transport built this config and reads it
	// back after Open returns (ws_session_handlers.go's openResult).
	cfg.Remote.IntegrationMode = string(mode)
	resolver := newResolver(
		withStore(r.consent),
		withHelperArtifactAvailable(available),
	)
	switch resolver.Resolve(Machine{Fingerprint: fingerprint, Mode: mode}) {
	case DesiredHelper:
		opened, selected, oerr := r.openFarHelper(ctx, cfg, claim, opts, platform, fingerprint)
		return opened, hold, selected, oerr
	case ConsentRequired:
		// The key is ALREADY trusted (the probe above just dialed it
		// successfully) — nothing about the host key rides this refusal,
		// and the one dialog the renderer raises asks about the method
		// alone. Answered here, `selected: true`, rather than swallowed
		// like an ordinary Refused: swallowing this outcome is exactly the
		// bug ADR-0068 fixes — an auto connection whose fingerprint has
		// never been answered fell through to a silent script-tier open
		// and was never asked, on every connect, forever.
		//
		// A CONNECTION SET TO `helper` ARRIVES HERE TOO (ADR-0105,
		// nocx-1kejz.3). It used to answer DesiredHelper from its own mode
		// alone, so the machine was never asked and the pane ran whatever
		// the deployment produced — with nothing on screen saying which
		// delivery was in force. The method a connection keeps and the
		// permission a machine gives are two answers to two questions, and
		// this arm is where the second one is obtained.
		return transport.HostedSessionOpen{}, hold, true, transport.NewHelperConsentNeeded(cfg.ProfileID, cfg.Host, fingerprint, nil)
	default:
		// Refused: raw, script, an explicit helper that failed elsewhere,
		// or auto with a recorded decline. Falls through to the local
		// opener exactly as before — declining leaves the connection
		// usable without the helper.
		return transport.HostedSessionOpen{}, hold, false, nil
	}
}

// helperConsentAskForProbeFailure decides whether a failed platform probe
// is the moment to raise the connect-time helper ask (ADR-0068), when the
// failure is a host-key trust decision the SAME dialog can carry it beside
// (owner's decision 2026-09-16: never two dialogs in sequence).
//
// THE MODES THAT MAY ASK ARE THE MODES THAT MAY HAVE THE HELPER, and since
// ADR-727g63tr decision 1 they are read from the HOST record by the very
// fingerprint the probe failure offered: auto (which is "not yet answered")
// and helper (ADR-0105 — naming the method is not the machine's permission).
// A host recorded script is an answer and asks nothing, even for a key this
// route's known_hosts has not seen yet; a host recorded raw never reaches here
// at all.
//
// The fingerprint is the argument this question is asked BY, not something
// derived after the mode: without one the host has no record at all, and there
// is nothing to consult.
//
// Returns nil for every case that must NOT ask here: a host recorded script,
// a probe failure that is not a host-key one, or a fingerprint ADR-0034 has
// already answered through some OTHER route to the same machine ("one machine,
// one answer" — TestOneMachineOneAnswerAcrossConnections). In every nil case
// the existing mechanism answers on its own: the local opener's own dial
// produces the ordinary host-key-only refusal a moment later (or, for an
// already-answered fingerprint, the key gets trusted and the very next probe
// resolves the machine's already-recorded answer without asking again).
func helperConsentAskForProbeFailure(profileID, host, fingerprint string, probeErr error, store *consent.Store, modes *hostmode.Store) error {
	mode := hostModeFor(modes, fingerprint)
	if mode != hostmode.Auto && mode != hostmode.Helper {
		return nil
	}
	if fingerprint == "" || store == nil {
		return nil
	}
	if _, answered := store.Lookup(fingerprint); answered {
		return nil
	}
	return transport.NewHelperConsentNeeded(profileID, host, fingerprint, probeErr)
}

// offeredHostKeyFingerprint reads the fingerprint an unknown-or-changed
// host-key error carries, deterministic from the offered key bytes alone —
// the one probe failure that names a machine before the dial has trusted
// anything. Empty for every other error, including a nil one.
func offeredHostKeyFingerprint(err error) string {
	var unknown *ssh.ErrUnknownHostKey
	if errors.As(err, &unknown) {
		return unknown.Fingerprint
	}
	var changed *ssh.ErrHostKeyMismatch
	if errors.As(err, &changed) {
		return changed.Fingerprint
	}
	return ""
}

// openFarHelper is the SELECTED arm of the remote route: the far host's own
// helper is installed if it is not there, reached, and the pane spawned on it.
//
// It is a function of its own rather than the rest of openHoldingLease for the
// reason every extraction in this package is — the interval the hold exists for
// is then two statements in one small function, rather than one that scrolls
// past ninety lines of spawn-and-attach. What it takes from the selection is
// what the selection already decided: the connect options the whole open uses,
// the platform the probe answered, and the host-key fingerprint it observed
// (which the open reports back, ADR-0023). Re-deriving any of the three here
// would be a second answer to "what did the probe say", and on a host whose
// answer is expensive to obtain that second answer is another login.
func (r *helperRegistry) openFarHelper(ctx context.Context, cfg session.Config, claim string, opts []ssh.ConnectOption, platform deploy.Platform, fingerprint string) (transport.HostedSessionOpen, bool, error) {
	opts = append(append([]ssh.ConnectOption(nil), opts...), ssh.WithHostKeyPin(fingerprint))
	endInstall := diagnostics.Stage(ctx, diagnostics.StageInstall)
	installed, err := installHelperAt(ctx, cfg.Host, opts, r.install, r.source, platform)
	endInstall(err)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	f := &hostedShellBinding{reg: r, sid: session.NewID(), host: cfg.Host, account: accountFromOptions(opts), opts: opts, install: installed}
	h := &hostedShellCarrier{f: f, lanes: r.lanes, log: r.log}
	endHold := diagnostics.Stage(ctx, diagnostics.StageHold)
	h.mu.Lock()
	c, outcome, err := h.connectLocked(ctx)
	h.mu.Unlock()
	endHold(err)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	if outcome.State != "" {
		return transport.HostedSessionOpen{}, true, errors.New(outcome.Message)
	}
	// THE CHANNEL THIS OPEN DIALED IS THE OPEN'S UNTIL THE PANE IS THE
	// REGISTRY'S (nocx-ouru5.1.8). Every return below used to remember its own
	// `_ = c.Close()` — four of them — and a helper channel is the one thing on
	// this path that outlives a failed open: the client is not in `hosts` yet,
	// so no teardown closes it, and what stays behind is a lane nobody can
	// reach, a helper on somebody else's machine serving a pane that never
	// came, and (in a stand) the fake process behind that lane still running
	// after every test ended. That is the leak ci-linux reported, and the
	// client it named was dialled exactly here (hostedShellCarrier.connectLocked, from
	// this function, from a `session.open` over the socket). One defer, armed
	// by the dial and disarmed by the registration that makes the registry the
	// owner, is that rule stated once instead of remembered at each return.
	committed := false
	defer func() {
		if committed {
			return
		}
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
	}()
	var subscriberRaw [16]byte
	if _, randErr := rand.Read(subscriberRaw[:]); randErr != nil {
		return transport.HostedSessionOpen{}, true, randErr
	}
	subscriber := proto.SubscriberID(hex.EncodeToString(subscriberRaw[:]))
	var lifecycleAdapter *lifecyclechannel.Adapter
	var lifecyclePeer net.Conn
	var lifecycleLaunch *proto.LifecycleLaunch
	// THE COMPLETION DOWNLINK, on the remote route (helper_hosted.go carries
	// the full reasoning). The lifecycle channel is authenticated HERE, in
	// this coordinator's kernel, while the emulator and the rendezvous live
	// in the helper — and on this route that helper is on ANOTHER MACHINE,
	// which changes nothing about the obligation: each already-authenticated
	// completion is carried down to the session that owns the pane, or the
	// far runtime waits forever for an authentication that already happened
	// here. The wrapper is built before the spawn and bound when the spawn
	// RPC answers, because a shell that completes a command inside that
	// window is accepted while the far session's identity is still unknown.
	var downlink *client.CompletionDownlink
	// stopDownlink ends the delivery context; nil with the downlink. Its two
	// ends are endLifecycleLeg's rollback and the pane's own end.
	var stopDownlink context.CancelFunc
	// cursor is the leg's applied cursor (lifecycle_cursor.go, ADR-0077).
	var cursor *lifecycleCursor
	if r.lifecycle != nil {
		// THE DELIVERY CONTEXT IS THE PANE'S LIFETIME, not this request's:
		// the request context dies with the renderer's socket while the PTY
		// deliberately lives on (AD-9). WithoutCancel keeps its values and
		// drops its cancellation.
		sessionCtx, cancelSession := context.WithCancel(context.WithoutCancel(ctx))
		stopDownlink = cancelSession
		// A delivery that fails is retried, and one that is lost is logged
		// by the downlink itself, through log.From on this same context.
		// This route streams no block rows, so a lost boundary has no block
		// to settle here.
		downlink = client.NewCompletionDownlink(c, sessionCtx, nil)
		driveKernel := client.NewCompletionObservingKernel(r.lifecycle, downlink)

		coordinatorConn, peerConn := net.Pipe()
		var lifecycleErr error
		// The caller's exchange, for the reason helper_hosted.go gives.
		cursor = newLifecycleCursor(ctx, r.lifecycleCursors, r.lifecycleStopping)
		endChannel := diagnostics.Stage(ctx, diagnostics.StageLifecycle)
		lifecycleAdapter, lifecycleErr = lifecyclechannel.NewStream(
			log.NewSlogAdapter(r.log).WithContext(ctx), driveKernel, coordinatorConn,
			lifecyclechannel.WithLossReporter(r.reportLifecycleLoss),
			lifecyclechannel.WithFrameScope(cursor.applyFrame),
			lifecyclechannel.WithSetupTimeout(remoteShellSetupTimeout),
		)
		endChannel(lifecycleErr)
		if lifecycleErr != nil {
			_ = peerConn.Close()
			stopDownlink()
			return transport.HostedSessionOpen{}, true, lifecycleErr
		}
		lifecyclePeer = peerConn
		launch := lifecycleAdapter.Launch()
		lifecycleLaunch = &proto.LifecycleLaunch{
			Lane: string(launch.Lane), Domain: string(launch.Domain),
			Epoch: launch.Epoch, Capability: launch.Capability, Recovery: launch.Recovery, RecoveryEpisodeID: launch.RecoveryEpisodeID,
		}
		// A FAR PANE'S CHANNEL IS NOT THIS MACHINE'S, AND THE COMPOSITION ROOT
		// IS TOLD SO THE MOMENT IT EXISTS (nocx-8gg8y). The adapter's own
		// TransportID says "register me in the kind registry" in its comment;
		// nothing did on this route, so every question keyed on the transport —
		// the agent launch resolver's first among them — answered "unknown" and
		// took its refusal branch. local:false is what this transport is: the
		// domains ride the helper's stream to another machine, so no record of
		// ours and no tool surface of ours applies to a launch on it.
		if r.transports != nil {
			r.transports.register(lifecycleAdapter.TransportID(), transportKind{local: false})
		}
		if r.environmentEntries != nil {
			r.environmentEntries.register(sessionCtx, launch.Lane, downlink)
		}
	}
	// endLifecycleLeg rolls back everything this open built for the lifecycle
	// plane. The downlink ends WITH the adapter and never after it: an open
	// that never became a pane has no session to deliver to, and a delivery
	// context outliving its pane is the lifetime this package refuses to
	// keep. One function, so no arm below can roll back half of it.
	endLifecycleLeg := func(reason string) {
		if lifecycleAdapter != nil {
			diagnostics.NoteLifecycleClosed(ctx, reason)
			_ = lifecycleAdapter.CloseWith(reason)
			_ = lifecyclePeer.Close()
		}
		if stopDownlink != nil {
			stopDownlink()
		}
	}
	// THE PANE'S TOOL SURFACE IS RESOLVED BEFORE ITS SPAWN, because the launch
	// this spawn renders is what names the socket on the far host (nocx-e2bws):
	// the far agent dials NOCX_TOOL_SOCKET, and a path that only existed after
	// the shell started would be a path no launch ever named.
	//
	// The NAME is the claim — the caller's own name for this spawn, which the
	// wire already carries to the far helper — and never a session id: that id
	// is minted on the far side DURING this call (AD-7) and is what the
	// listener's pane record is stamped with, one step below.
	//
	// A FAILURE HERE IS NOT A REFUSAL (ADR-0004): a pane whose far directory
	// could not be made still opens, conventionally, with no tool surface — and
	// that is the same soft degrade as a coordinator that runs no endpoint.
	var routes farPaneToolRoutes
	var toolToken string
	if claim != "" && r.tools != nil {
		endTools := diagnostics.Stage(ctx, diagnostics.StageToolSocket)
		prepared, perr := r.tools.PrepareFarPaneToolSocket(ctx, cfg.Host, opts, claim)
		endTools(perr)
		if perr != nil {
			r.log.Warn("far pane: no tool socket for this pane", "host", cfg.Host, "error", perr)
		} else {
			routes, toolToken = prepared, mintToolToken()
		}
	}
	if lifecycleAdapter != nil {
		lifecycleAdapter.BeginSetup()
	}
	// THE ANSWER IS BOUNDED, THE OPEN IS NOT (nocx-398ca): see
	// farHelperAnswerTimeout's own note. The spawn and the attach are one
	// round trip each on a connection that is already up, and each gets the
	// budget the adapter above gives the shell that follows them.
	spawnCtx, cancelSpawn := context.WithTimeout(ctx, farHelperAnswerTimeout)
	endSpawn := diagnostics.Stage(ctx, diagnostics.StageSpawn)
	entry, err := c.Spawn(spawnCtx, proto.SpawnParams{
		SessionIdleDays: r.lifetime.daysPtr(),
		Cwd:             cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows, Lifecycle: lifecycleLaunch,
		// THE CLAIM RIDES THE SPAWN, and it is the L7 interval's opening half
		// rather than a duplicate of the row the open path already wrote
		// (nocx-50w7p.5). Without it, a coordinator that died between this
		// spawn and that row leaves a far shell nothing recorded, and the
		// repeat forks a SECOND one on somebody else's machine. Empty is the
		// honest value when the caller wrote no claim.
		IdempotencyKey: claim,
		// THE FAR PATH, when this pane has a tool surface at all. The field's
		// meaning is "the tool endpoint on the helper's own machine", and this
		// pane's helper is on the FAR host — so what travels is the socket path
		// there, which the far launcher renders as the shell's
		// NOCX_TOOL_SOCKET and the far sshd binds at this machine's helper's
		// request (nocx-e2bws).
		AgentToolEndpoint: routes.Path,
		// AND THE BEARER ITS AGENT WILL PRESENT: a pane on another host cannot
		// be admitted by process ownership, so its interval is opened by this
		// value, which the far launcher stages into the frame its shell reads
		// (nocx-50w7p.16).
		AgentToolToken: toolToken,
	})
	cancelSpawn()
	if err != nil {
		endSpawn(err)
		endLifecycleLeg("open-rollback:spawn")
		return transport.HostedSessionOpen{}, true, farHelperAnswerError("spawn", err)
	}
	// THE SPAWN NAMED THE FAR SESSION: everything the kernel accepted while
	// it was unknown is delivered now, in acceptance order.
	if downlink != nil {
		downlink.Bind(entry.HostSessionID)
	}
	// The rows plane is held from the first frame and bound below, as the
	// local hosted opener does (helper_hosted.go).
	holdOpt, held := holdRowsBeforeAttach(r.blockRows)
	attachCtx, cancelAttach := context.WithTimeout(ctx, farHelperAnswerTimeout)
	attached, err := c.Attach(attachCtx, proto.AttachParams{
		Subscriber: subscriber,
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true,
		LifecycleOffset: 0, LifecycleFresh: true, RequestWrite: true,
	}, holdOpt)
	cancelAttach()
	endSpawn(err)
	if err != nil {
		endLifecycleLeg("open-rollback:attach")
		// A HELPER THAT DID NOT ANSWER THE ATTACH WILL NOT ANSWER THE CLOSE.
		// Asking anyway would hold the failure for another budget; the
		// connection ending is what releases the session to the helper's own
		// unclaimed-session sweep (D1).
		if !errors.Is(err, context.DeadlineExceeded) {
			_ = c.CloseSession(ctx, entry.HostSessionID)
		}
		return transport.HostedSessionOpen{}, true, farHelperAnswerError("attach", err)
	}
	sid := session.ID(entry.HostSessionID.Session)
	f.sid = sid
	// THE FAR ATTACHMENT'S SCREEN DRAIN (nocx-zg3k3.2.2): forward the
	// helper's reassembled runtime snapshot through the same coordinator
	// publisher used by the local hosted opener. The helper's OSC 7 state is
	// already in this document; no second CWD parser belongs here.
	if r.publishScreen != nil {
		attached.OnScreenFrame(func(revision uint64, doc []byte) {
			r.publishScreen(sid, revision, doc)
		})
		attached.OnScreenLost(func(reason string) {
			log.From(ctx).Warn("screen assembly lost on the far-helper attachment",
				"session", string(sid), "reason", reason)
		})
	}
	if r.publishEffect != nil {
		attached.OnEffect(func(effect proto.EffectFrame) { r.publishEffect(sid, effect) })
	}
	if cursor != nil {
		cursor.bind(entry.HostSessionID.Session, attached.LifecycleIngested())
	}
	// THE STREAMED BLOCK OUTPUT, registered before the adopt like the screen
	// drain, so no row is dropped at a door nobody opened yet.
	stopBlockRows := bindHeldBlockRows(ctx, r.blockRows, sid, attached, held)
	// Observe before Adopt can consume its first post-bootstrap PTY bytes.
	if lifecycleAdapter != nil {
		attached.OnFirstOutput(lifecycleAdapter.ObserveShellOutput)
	}
	endAdopt := diagnostics.Stage(ctx, diagnostics.StageAdoption)
	sess, err := r.registry.Adopt(ctx, cfg, sid, attached)
	endAdopt(err)
	if err != nil {
		stopBlockRows()
		_ = attached.Close()
		endLifecycleLeg("open-rollback:adopt")
		_ = c.CloseSession(ctx, entry.HostSessionID)
		return transport.HostedSessionOpen{}, true, err
	}
	bindBlockEndToSession(sess, attached, r.blockRows, sid, stopBlockRows)
	// AND THE LANE NOW HAS A SESSION, which is the other fact the agent
	// enroller reads (nocx-8gg8y). The transport records the same pair for the
	// published facts; this map is the coordinator's own, and a far pane's lane
	// missing from it made an enrolment on somebody else's machine answer "nocx
	// does not know which pane this shell is".
	if lifecycleAdapter != nil && r.registerLane != nil {
		r.registerLane(lifecycleAdapter.Lane(), string(sid))
	}
	// Record the verified host identity on the adopted session.
	if fingerprint != "" {
		if rerr := r.registry.RecordHostKeyFingerprint(sid, fingerprint); rerr != nil {
			r.log.Warn("far helper pane: the verified host key could not be recorded on its session",
				"host", cfg.Host, "error", rerr)
		}
	}
	// The registry holds this carrier until the hosted session ends.
	r.mu.Lock()
	r.hosts[sid] = h
	r.mu.Unlock()
	// THE REGISTRY IS THE OWNER FROM HERE, and the open's own defer above stands
	// down: closing the channel now is the stand's end, the helper's retire or
	// the session's own end — the three things that already close every entry
	// in this map.
	committed = true
	// THE LISTENER IS OPENED NOW, with the session id the FAR helper minted, and
	// it is registered against that session so its end is not this process's to
	// remember (nocx-e2bws). The interval, both ends named: it opens here, after
	// the spawn answered, and closes in helperRegistry.SessionEnded, which the
	// transport calls when the session's output is over — the same event that
	// retires the pane's bearer. A listener that outlived its session would be a
	// socket on somebody else's host accepting agents for a pane this
	// coordinator has forgotten (ADR-0058).
	if routes.Path != "" {
		// REGISTERED BEFORE THE LISTENER EXISTS, so an end that arrives during
		// the open is not lost (beginFarToolSocket's own note).
		live := r.beginFarToolSocket(sid, farToolSocket{host: cfg.Host, opts: opts, routes: routes})
		// ONE MORE ANSWER THE HELPER OWES THIS OPEN, and it is bounded for the
		// same reason the spawn and the attach are (nocx-398ca): this call is
		// on the open's path, before its result, and a silent helper must not
		// hold it.
		toolCtx, cancelTool := context.WithTimeout(ctx, farHelperAnswerTimeout)
		id, oerr := r.tools.OpenFarPaneToolSocket(toolCtx, cfg.Host, opts, routes, string(sid))
		cancelTool()
		switch {
		case oerr != nil:
			// The launch already names the socket, so a listener that never came
			// up is a far agent that would fail on a path nothing serves. Said
			// out loud, and the directory goes with it: it is this open's own
			// scaffolding.
			r.log.Warn("far pane: the tool socket was not opened; the pane runs with no tools",
				"host", cfg.Host, "path", routes.Path, "error", oerr)
			cleanupCtx, cancelCleanup := context.WithTimeout(ctx, farHelperAnswerTimeout)
			rerr := r.tools.RemoveFarPaneToolSocketDir(cleanupCtx, cfg.Host, opts, routes)
			cancelCleanup()
			if rerr != nil {
				r.log.Warn("far pane: the tool socket's directory was not removed", "host", cfg.Host, "error", rerr)
			}
		case !live || !r.completeFarToolSocket(sid, id):
			// THE SESSION ENDED WHILE THE LISTENER WAS BEING OPENED: its end
			// took the registration (and removed the directory), so this open
			// owns the listener it just made and ends it here rather than
			// leaving a socket on somebody else's host for a session this
			// coordinator has forgotten.
			r.log.Info("far pane: the session ended while its tool socket was opening; closing it",
				"session", sid, "host", cfg.Host, "path", routes.Path)
			if cerr := r.tools.CloseFarPaneToolSocket(ctx, id); cerr != nil {
				r.log.Warn("far pane: the tool socket was not closed", "session", sid, "host", cfg.Host, "error", cerr)
			}
		default:
			r.tools.RecordPaneBearer(sid, toolToken)
			// AND THE SESSION MAY HAVE ENDED BEFORE THE REGISTRATION EXISTED:
			// between Adopt and beginFarToolSocket there is the same gap one
			// step earlier, and a session that ended in it would leave an entry
			// nothing will ever come back for. The session's own lifetime is the
			// authority for that question — so it is asked, and the opener owns
			// the teardown when it is already over.
			select {
			case <-sess.Done():
				r.log.Info("far pane: the session ended before its tool socket was registered; closing it",
					"session", sid, "host", cfg.Host, "path", routes.Path)
				r.tearDownFarToolSocket(sid)
			default:
			}
		}
	}
	var lifecycleLane lifecycle.LaneID
	var startLifecycle func()
	var abortLifecycle func()
	var detachLifecycle func()
	if lifecycleAdapter != nil {
		lifecycleLane = lifecycleAdapter.Lane()
		var startOnce sync.Once
		startLifecycle = func() {
			startOnce.Do(func() {
				bridgeLifecycle(log.NewSlogAdapter(h.log).WithContext(ctx),
					lifecycleAdapter.TransportID(), lifecyclePeer, attached.Lifecycle(), cursor, attached)
			})
		}
		var abortOnce sync.Once
		abortLifecycle = func() {
			abortOnce.Do(func() { endLifecycleLeg("open-aborted:transport") })
		}
		// The detach mirrors the rollback but ends the adapter with Detach,
		// not Close: the leg's adapter learns an orderly handover from this
		// side instead of reading its own carrier's EOF as the loss it is
		// not (ADR-0076).
		var detachOnce sync.Once
		detachLifecycle = func() {
			detachOnce.Do(func() {
				if lifecycleAdapter != nil {
					_ = lifecycleAdapter.Detach()
					_ = lifecyclePeer.Close()
				}
				if stopDownlink != nil {
					stopDownlink()
				}
			})
		}
	}
	// THE DOWNLINK'S OTHER END: the pane's own. sess.Done() is the edge the
	// transport's teardown owner already waits on, so the delivery context
	// hangs off it rather than owning a lifetime of its own.
	if stopDownlink != nil {
		bindDownlinkToSession(sess, stopDownlink)
	}
	return transport.HostedSessionOpen{
		Session: sess, Host: cfg.Host, Account: f.account, Generation: installed.generation,
		HelperCommand: installed.command, Fingerprint: fingerprint,
		LifecycleLane: lifecycleLane, StartLifecycle: startLifecycle,
		AbortLifecycle: abortLifecycle, DetachLifecycle: detachLifecycle,
		// The two ends of one fact meet here and nowhere else: the
		// attachment knows a stretch of output never crossed the wire, and
		// the transport's ring is the only thing that can place it at an
		// offset. Neither package learns the other's job — this passes a
		// function, and the content store stays where it is (nocx-k6p18.25).
		ObserveOutputHoles: attached.OnOutputHole,
	}, true, nil
}

// bridgeLifecycle carries the shell's lifecycle bytes between the helper
// attachment and the adapter's end of the channel, and SAYS SO (nocx-n14oo.7).
//
// This hop was silent in both directions, and the cost was a diagnosis that
// could not be made: on 2026-09-10 a worker pane's shell wrote 219 bytes of
// hello — the helper logged it — and the coordinator's adapter timed out ten
// seconds later having seen no envelope at all, accepted or rejected. Three
// hops lie between those two facts and this is the last of them, so a bridge
// that reported nothing could neither be blamed nor cleared.
//
// The three lines are chosen to make exactly that reading. Started names the
// transport, which is the key the adapter's own "established" and "lost" lines
// carry, so the bridge joins the channel it feeds rather than sitting beside
// it. The FIRST bytes in each direction are said once, because the answer
// needed is whether anything arrived at all and a per-frame line would bury
// it. And each half says what it carried when it ends, with carried_nothing
// stated as its own fact: a bridge that ran and moved nothing and a bridge
// that never ran look identical in a byte count and must not read alike.
//
// IT ALSO FEEDS THE APPLIED CURSOR (ADR-0077). cursor, when not nil, is told
// every stretch this bridge hands the adapter and where that stretch ends in
// the helper's stream (offsets is the attachment's own ingest cursor) —
// before the write, since the adapter can apply a frame from those bytes
// before the write returns.
//
// AND IT NEVER DROPS WHAT THE SHELL SAID. The two directions used to end each
// other: the moment the adapter-to-shell copy failed to write — which is
// exactly what happens when the attachment is done because the shell has
// exited — it closed the pipe the shell-to-adapter copy was still writing
// into, and whatever the attachment still held, a replayed command end or
// the domain's close, raced that close to the adapter (nocx-zg3k3.5.11: a
// returned coordinator meets an exited shell with its last frames queued).
// A write that fails toward the shell now ends only that direction; the other
// drains what the attachment holds, and its own end closes both.
func bridgeLifecycle(lg log.Logger, transport lifecycle.TransportID, peer net.Conn, carrier io.ReadWriteCloser,
	cursor *lifecycleCursor, offsets lifecycleOffsets,
) {
	if lg == nil {
		lg = log.NewSlogAdapter(nil)
	}
	lg = lg.With("transport", string(transport))
	lg.Debug("lifecycle bridge started")

	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = peer.Close()
			_ = carrier.Close()
		})
	}
	go func() {
		from := countingFirst(lg, "the adapter's first bytes reached the shell", peer)
		buf := make([]byte, 32*1024)
		var n int64
		var err error
		towardShellFailed := false
		for {
			m, rerr := from.Read(buf)
			if m > 0 {
				if _, werr := carrier.Write(buf[:m]); werr != nil {
					err, towardShellFailed = werr, true
					break
				}
				n += int64(m)
			}
			if rerr != nil {
				if rerr != io.EOF {
					err = rerr
				}
				break
			}
		}
		lg.Info("lifecycle bridge: the adapter's end closed",
			"to_shell_bytes", n, "carried_nothing", n == 0, "error", err)
		if towardShellFailed {
			// The shell's side refuses writes; what it already said is
			// still owed to the adapter, and the other direction's end
			// closes both once it is delivered.
			return
		}
		closeBoth()
	}()
	go func() {
		from := countingFirst(lg, "the shell's first bytes reached the adapter", carrier)
		buf := make([]byte, 32*1024)
		var n int64
		var err error
		for {
			m, rerr := from.Read(buf)
			if m > 0 {
				if cursor != nil && offsets != nil {
					cursor.relayed(m, offsets.LifecycleIngested())
				}
				if _, werr := peer.Write(buf[:m]); werr != nil {
					err = werr
					break
				}
				n += int64(m)
			}
			if rerr != nil {
				if rerr != io.EOF {
					err = rerr
				}
				break
			}
		}
		lg.Info("lifecycle bridge: the shell's end closed",
			"to_adapter_bytes", n, "carried_nothing", n == 0, "error", err)
		closeBoth()
	}()
}

// countingFirst wraps a reader so the FIRST read that yields anything is said
// once, with its size. It is a reader rather than a counter inside the copy
// because io.Copy is what moves the bytes and the arrival has to be reported
// at the moment it happens, not when the direction ends — the whole failure
// this exists for ends ten seconds after the byte that mattered.
func countingFirst(lg log.Logger, what string, r io.Reader) io.Reader {
	return &firstByteReader{lg: lg, what: what, r: r}
}

type firstByteReader struct {
	lg   log.Logger
	what string
	r    io.Reader
	said bool
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && !f.said {
		f.said = true
		f.lg.Info("lifecycle bridge: "+f.what, "bytes", n)
	}
	return n, err
}

// hostFor answers which helper holds a session, for the ONE caller that needs
// it without a hostedShellBinding: the pane screen read (nocx-ygxjv.3).
//
// It is the registry's own map and not a second one, because that map is
// already the record of which helper holds which session — the hosted open
// writes it, the readopt pass writes it — and a parallel map would be a second
// answer that agrees until one of the two is forgotten.
func (r *helperRegistry) hostFor(sid session.ID) (*hostedShellCarrier, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hosts[sid]
	return h, ok
}

func (r *helperRegistry) forget(h *hostedShellCarrier) {
	r.mu.Lock()
	if r.hosts[h.f.sid] == h {
		delete(r.hosts, h.f.sid)
	}
	r.mu.Unlock()
}

// stageReadoptHelper brings up a helper for a carried-over session without
// making it an inventory or adopting the session. A successful return is the
// helper client's completed handshake; Readopt consumes the same client.
// The pending interval starts at registration in pendingReadopts and ends at
// takeReadoptHelper's transfer to one adoption attempt, or discard on failure
// or shutdown. Until adoption registers it in hosts, inventory must not see it.
func (r *helperRegistry) stageReadoptHelper(ctx context.Context, f *hostedShellBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h := &hostedShellCarrier{f: f, lanes: r.lanes, log: r.log}
	h.mu.Lock()
	_, outcome, err := h.connectLocked(ctx)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil || outcome.State != "" {
		closeStagedReadoptHelper(h)
	}
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if outcome.State != "" {
		return errors.New(outcome.Message)
	}
	r.mu.Lock()
	if r.lifecycleStopping != nil && r.lifecycleStopping.Load() {
		r.mu.Unlock()
		h.mu.Lock()
		closeStagedReadoptHelper(h)
		h.mu.Unlock()
		return errors.New("application is shutting down")
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		h.mu.Lock()
		closeStagedReadoptHelper(h)
		h.mu.Unlock()
		return err
	}
	if r.pendingReadopts == nil {
		r.pendingReadopts = make(map[session.ID]*hostedShellCarrier)
	}
	if r.pendingReadopts[f.sid] != nil || r.hosts[f.sid] != nil {
		r.mu.Unlock()
		h.mu.Lock()
		closeStagedReadoptHelper(h)
		h.mu.Unlock()
		return fmt.Errorf("helper already held for readopt session %s", f.sid)
	}
	r.pendingReadopts[f.sid] = h
	r.mu.Unlock()
	return nil
}

// closeStagedReadoptHelper closes a pending channel without touching hosts:
// pending helpers are not registered there, and closeLocked's forget could
// otherwise remove a concurrently registered live host for the same session.
func closeStagedReadoptHelper(h *hostedShellCarrier) {
	if h.client != nil {
		_ = h.client.Close()
	}
	h.client = nil
	h.dead = true
}

// discardReadoptHelper closes a prepared helper that will not reach Readopt.
func (r *helperRegistry) discardReadoptHelper(sid session.ID) {
	h := r.takeReadoptHelper(sid)
	if h == nil {
		return
	}
	h.mu.Lock()
	closeStagedReadoptHelper(h)
	h.mu.Unlock()
}

// discardAllReadoptHelpers closes every staged channel when the app shuts down.
func (r *helperRegistry) discardAllReadoptHelpers() {
	r.mu.Lock()
	pending := r.pendingReadopts
	r.pendingReadopts = make(map[session.ID]*hostedShellCarrier)
	r.mu.Unlock()
	for _, h := range pending {
		h.mu.Lock()
		closeStagedReadoptHelper(h)
		h.mu.Unlock()
	}
}

// takeReadoptHelper transfers the staged helper into one Readopt attempt.
// It is not in hosts until that attempt has an answer and performs adoption.
func (r *helperRegistry) takeReadoptHelper(sid session.ID) *hostedShellCarrier {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.pendingReadopts[sid]
	delete(r.pendingReadopts, sid)
	return h
}

// inventories returns one reconciliation inventory per helper generation
// currently held by this coordinator. A helper channel that was never opened
// has no client and therefore cannot claim any stored id space.
func (r *helperRegistry) inventories() []sessionInventory {
	r.mu.Lock()
	helpers := make([]*hostedShellCarrier, 0, len(r.hosts))
	for _, h := range r.hosts {
		helpers = append(helpers, h)
	}
	r.mu.Unlock()

	out := make([]sessionInventory, 0, len(helpers))
	for _, h := range helpers {
		h.mu.Lock()
		c, generation := h.client, h.f.install.generation
		host, account := h.f.host, h.f.account
		h.mu.Unlock()
		if c != nil && generation != "" {
			out = append(out, &helperSessionInventory{
				client: c, generation: generation, host: host, account: account,
			})
		}
	}
	return out
}

// sessions asks each active helper once and combines the coordinator DTOs for
// the read-only inventory RPC. A failed helper fails the aggregate answer;
// callers must not interpret a partial list as a complete inventory.
func (r *helperRegistry) sessions(ctx context.Context) ([]client.SessionEntry, error) {
	r.mu.Lock()
	helpers := make([]*hostedShellCarrier, 0, len(r.hosts))
	for _, h := range r.hosts {
		helpers = append(helpers, h)
	}
	r.mu.Unlock()
	if len(helpers) == 0 {
		return nil, errors.New("helper session inventory unavailable: no active helper")
	}
	out := make([]client.SessionEntry, 0)
	for _, h := range helpers {
		for attempt := range 2 {
			c, err := h.inventoryClient(ctx)
			if err != nil {
				return nil, err
			}
			entries, err := c.Sessions(ctx)
			if err == nil {
				out = append(out, entries...)
				break
			}
			if attempt == 0 && errors.Is(err, client.ErrLost) {
				h.invalidate(c)
				continue
			}
			return nil, err
		}
	}
	return out, nil
}

// CloseHelpersFor first asks every hosted daemon on the named machine
// for its sessions and closes each one through the daemon's close-session
// operation. Only after all those acknowledgements does it close the bridge
// channels and forget the registry entries. A daemon that cannot enumerate
// sessions refuses the operation rather than allowing uninstall to delete a
// live session's executable. The closing interval begins with the fingerprint
// reservation below and ends at FinishHelpersFor after the caller's uninstall
// attempt; on a session-close failure this function releases it before returning.
func (r *helperRegistry) CloseHelpersFor(ctx context.Context, fp string) error {
	if fp == "" {
		return nil
	}
	r.mu.Lock()
	if _, alreadyClosing := r.closing[fp]; alreadyClosing {
		r.mu.Unlock()
		return errors.New("helper uninstall already in progress")
	}
	r.closing[fp] = struct{}{}
	var victims []*hostedShellCarrier
	for _, h := range r.hosts {
		if h.f.fp == fp {
			victims = append(victims, h)
		}
	}
	r.mu.Unlock()
	for _, h := range victims {
		if err := h.closeSessions(ctx); err != nil {
			for _, victim := range victims {
				victim.cancelClosing()
			}
			r.FinishHelpersFor(fp)
			return err
		}
	}

	// closeLocked takes h.mu and calls reg.forget, which re-takes r.mu; the
	// registry lock is therefore released before closing each helper.
	for _, h := range victims {
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
	}
	return nil
}

func (r *helperRegistry) FinishHelpersFor(fp string) {
	r.mu.Lock()
	delete(r.closing, fp)
	r.mu.Unlock()
}

func (h *hostedShellCarrier) cancelClosing() {
	h.mu.Lock()
	h.closing = false
	h.mu.Unlock()
}

func (h *hostedShellCarrier) closeSessions(ctx context.Context) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return errors.New("helper is already closing")
	}
	h.closing = true
	if h.client == nil && !h.dead {
		h.closing = false
		h.mu.Unlock()
		return nil
	}
	c, outcome, err := h.connectLocked(ctx)
	generation := h.f.install.generation
	h.mu.Unlock()
	ok := false
	defer func() {
		if !ok {
			h.cancelClosing()
		}
	}()
	if err != nil {
		return fmt.Errorf("connect helper for session close: %w", err)
	}
	if outcome.State != "" {
		return fmt.Errorf("connect helper for session close: %s", outcome.Message)
	}
	entries, err := c.Sessions(ctx)
	if err != nil {
		var refusal *client.RefusalError
		if errors.As(err, &refusal) &&
			(refusal.Code == proto.ErrCodeUnknownService || refusal.Code == proto.ErrCodeUnknownOp) {
			return fmt.Errorf("helper session service unavailable: %w", err)
		}
		return fmt.Errorf("list helper sessions: %w", err)
	}
	for _, entry := range entries {
		if entry.HostSessionID.Generation != generation {
			continue
		}
		if err := c.CloseSession(ctx, entry.HostSessionID); err != nil {
			return fmt.Errorf("close helper session %s: %w", entry.HostSessionID.Session, err)
		}
	}
	ok = true
	return nil
}

// hostedShellBinding records the route and install of one hosted shell.
type hostedShellBinding struct {
	reg     *helperRegistry
	sid     session.ID
	host    string
	account string
	// fp is the machine's host public-key fingerprint — the consent key —
	// captured at selection time so the registry can close every live
	// helper channel on a machine without holding a session (D25).
	fp   string
	opts []ssh.ConnectOption
	// install is the completed install this hosted shell uses: the machine
	// identity and the generation the lane names, and the installed binary's
	// path for the two readers that need a path rather than a lane.
	install installedHelper
}

// hostedShellCarrier is the carrier of one hosted shell, not a Git client.
// The shell's attachment and lifecycle leg need it even when no Git repository
// is open. There is no Git reference count here: only the hosted lifetime owns
// closing this carrier, and carrier loss does not erase the daemon's session.
type hostedShellCarrier struct {
	f       *hostedShellBinding
	lanes   laneProvider
	log     *slog.Logger
	mu      sync.Mutex
	client  *client.Client
	dead    bool
	closing bool
}

// screenClient is the route a remote pane's screen is read through: this
// helper's carrier, and the handle it knows the session by.
//
// The generation is the one the daemon answers to — f.install.generation, the
// same value an inventory row carries and the same one the session-close path
// compares against — because a handle addressed to another generation names
// nothing there and the helper refuses it.
func (h *hostedShellCarrier) screenClient(ctx context.Context, sid string) (*client.Client, client.HostSessionID, error) {
	h.mu.Lock()
	c, outcome, err := h.connectLocked(ctx)
	generation := h.f.install.generation
	h.mu.Unlock()
	switch {
	case err != nil:
		return nil, client.HostSessionID{}, err
	case outcome.State != "":
		return nil, client.HostSessionID{}, fmt.Errorf("helper for this pane: %s", outcome.Message)
	}
	return c, client.HostSessionID{Generation: generation, Session: sid}, nil
}

// connectLocked returns the existing carrier or establishes a new bridge to
// the helper daemon. A lost carrier is disposable; the daemon's endpoint and
// session state live on the host and are reached again through a fresh lane.
func (h *hostedShellCarrier) connectLocked(ctx context.Context) (*client.Client, git.OpenOutcome, error) {
	if h.client != nil && !h.dead {
		select {
		case <-h.client.Done():
			h.client = nil
			h.dead = true
		default:
			return h.client, git.OpenOutcome{}, nil
		}
	}
	lane, err := h.lanes.LaneConn(ctx, h.f.host, h.f.install.machine(),
		proto.GenerationID(h.f.install.generation), h.f.opts...)
	if err != nil {
		return nil, git.OpenOutcome{}, fmt.Errorf("helper lane for %s: %w", h.f.host, err)
	}
	// No Command: the lane's carrier refuses one (client.ErrNoCommandOnALane),
	// because the helper already started the bridge from the machine and the
	// generation above. What is left for this process to state is the
	// generation it EXPECTS the far helper to be, which the handshake verifies
	// (D21).
	c, err := client.Dial(ctx, client.Config{
		Exec:       lane,
		ExpectHash: h.f.install.generation,
		Log:        h.log,
	})
	if err != nil {
		// Dial's contract: on failure the lane is left for the caller to
		// close. Refusals are returned as an open outcome; other failures
		// remain errors so callers can apply their retry policy.
		_ = lane.Close()
		if outcome, ok := dialFailure(err, h.f.host); ok {
			return nil, outcome, nil
		}
		return nil, git.OpenOutcome{}, err
	}
	h.client = c
	h.dead = false
	return c, git.OpenOutcome{}, nil
}

// inventoryClient preserves the hostedShellCarrier registry entry across carrier loss.
// It only redials an entry that had already opened a helper; an unstarted
// entry must not make inventory claim a helper exists.
func (h *hostedShellCarrier) inventoryClient(ctx context.Context) (*client.Client, error) {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalHelperGitOpen)
	defer scope.Complete()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil && !h.dead {
		return nil, errors.New("helper session inventory unavailable: no active helper")
	}
	c, outcome, err := h.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	if outcome.State != "" {
		return nil, errors.New(outcome.Message)
	}
	return c, nil
}

// invalidate drops only the carrier used by a failed operation. Comparing
// pointers prevents an old loss from evicting a replacement installed by a
// concurrent retry.
func (h *hostedShellCarrier) invalidate(c *client.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != c {
		return
	}
	h.client = nil
	h.dead = true
}

// dialFailure maps a helper dial error onto the §6 open outcome it is,
// and reports whether the error is a refusal at all. A protocol version
// or content-hash mismatch is helperVersionMismatch — the file at the
// install path is not the binary nocx installed (D6); the one automatic
// reinstall of D6 is not implemented in this bead, so the state's own
// "non-retryable until reinstall" is exactly what happens. A refused exec,
// a peer that never answered within the sentinel deadline, or something
// else that answered is execForbidden (D5). ErrLost — the transport died
// during the handshake — is not a refusal: the caller's one retry heals
// it, and a mutation never would be retried (D12).
func dialFailure(err error, host string) (git.OpenOutcome, bool) {
	outcome := git.OpenOutcome{Message: err.Error()}
	switch {
	case errors.Is(err, client.ErrVersionMismatch), errors.Is(err, client.ErrHashMismatch):
		outcome.State = git.OpenHelperVersionMismatch
		outcome.Message = "the helper installed on " + host + " answered with a different protocol version or content than nocx installed — reinstall it to recover (" + err.Error() + ")"
	case errors.Is(err, client.ErrHelperNotServing):
		// The bridge reached the host and found no helper serving that
		// generation, and could not start one. It is its own sentence: "no
		// helper is running there" is not "the host refused the exec", and the
		// recovery is different.
		outcome.State = git.OpenExecForbidden
		outcome.Message = "no nocx helper is running on " + host + " for the generation nocx installed, and it could not be started: " + err.Error()
	case errors.Is(err, client.ErrExecForbidden), errors.Is(err, client.ErrNotOurHelper), errors.Is(err, client.ErrSentinelTimeout):
		outcome.State = git.OpenExecForbidden
		outcome.Message = "the host did not answer with the nocx helper: " + err.Error()
	default:
		return git.OpenOutcome{}, false
	}
	return outcome, true
}

func (h *hostedShellCarrier) sessionEnded() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeLocked()
}

func (h *hostedShellCarrier) closeLocked() {
	if h.client != nil {
		_ = h.client.Close()
	}
	h.client = nil
	h.dead = true
	h.f.reg.forget(h)
}

// reportLifecycleLoss is the adapter's loss sink for every helper-hosted
// session, opened or taken back. A method rather than the field itself so a
// registry without the wiring is one nil check here instead of one at each
// construction site.
func (r *helperRegistry) reportLifecycleLoss(lane lifecycle.LaneID, loss lifecyclechannel.Loss) {
	if r == nil || r.lifecycleLoss == nil {
		return
	}
	r.lifecycleLoss(lane, loss)
}
