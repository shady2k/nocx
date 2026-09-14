package app

// The helper-backed git factory selection (the remote-helper design): the
// composition root's answer to transport.GitFactoryFor. An SSH session gets
// a helper-backed git.RepoFactory when the helper is INSTALLED for this
// machine; otherwise the selection answers the honest §6 refusal — which
// platform is unsupported, what failed to install, the exec the host
// refused, the consent ask — never the deleted remoteUnsupported
// (remote-helper design §6, D16). The factory's command and expected hash
// are never configuration: each consultation installs the artifact on the
// session's host (D7) and takes them from what was installed.

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
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
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

// helperGitFactory is the composition root's answer to

// transport.GitFactoryFor: for an SSH session it decides whether the
// helper may be used for that machine at all (D8) — the consent decision
// comes before any remote write — and when it may, installs the helper
// artifact on the session's host (D7) and returns a factory that serves
// git over one helper process on that session's pooled connection. The
// selection answers one of a factory, consentRequired, a §6 refusal
// (unsupportedPlatform, deployFailed, execForbidden — each with the
// message naming what to do), or the resolver's Refused (raw, a denied
// answer) as a reason with no earned state. git.open consults the
// selection twice (the refusal decision, then the open); each consultation
// installs idempotently, and an already-complete directory uploads nothing
// (D7), so both consultations converge on the same install. The dial
// happens inside the returned factory's Open, never here.

func helperGitFactory(lanes helperInstallProvider, source deploy.ArtifactSource, store *consent.Store, installs *consent.InstallStore, log *slog.Logger) (transport.GitFactoryFor, *helperRegistry) {
	reg := &helperRegistry{
		lanes: lanes, install: lanes, source: source, log: log, consent: store,
		hosts:   make(map[session.ID]*hostHelper),
		closing: make(map[string]struct{}),
		// farTools is made by its first holder rather than here, so a registry
		// nothing opens a far tool socket on holds no map at all.

	}
	return func(sess session.Session) transport.GitOpenSelection {
		// The platform probe is the one bounded remote exec the decision
		// runs before the user has accepted anything — it writes nothing.
		// The install, the prune and the footprint observation are reached
		// only when the machine resolves to helper (D8: consent is asked
		// when the user reaches for the feature, not when a connection is
		// made; nothing is written before the ask is answered).
		platform, available, perr := probeHelperPlatform(sess, lanes, source)
		r := newResolver(
			withStore(store),
			withHelperArtifactAvailable(available),
			withHelperRequested(true), // git.open is the surface reaching for the helper
		)
		switch r.Resolve(Machine{Fingerprint: sess.HostKeyFingerprint(), Mode: effectiveModeFor(sess)}) {
		case DesiredHelper:
			if perr != nil {
				// The probe's failure is a fact with a state (§6), never
				// a silent degrade: an artifact the matrix does not ship
				// (or was not built) is unsupportedPlatform; anything
				// else that stopped the probe is execForbidden.
				log.Info("helper unavailable: the platform probe failed",
					"host", sess.Host(), "error", perr)
				return helperProbeRefusal(platform, perr)
			}
			installed, err := installHelperFor(sess, lanes, source, platform)
			if err != nil {
				// The upload or install failed (D7). The failure is a
				// fact about the host or the build, carried by the
				// deployFailed state with what failed — the panel names
				// the recovery instead of a generic error (brief).
				log.Info("helper unavailable: install failed",
					"host", sess.Host(), "error", err)
				return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
					State:   git.OpenDeployFailed,
					Message: "installing the helper on " + sess.Host() + " failed: " + err.Error(),
				}}
			}
			// The footprint observation is recorded only after Ensure
			// succeeded — the never-connect footprint surface must never
			// list a footprint that was not written remotely (consent
			// design §3.3). A failed observation is a logged warning, not
			// an install failure: the helper is up and serving.
			// installs is always wired at the composition root; the guard
			// keeps a nil store (a test double) from panicking the helper
			// path it never exercises.
			if fp := sess.HostKeyFingerprint(); fp != "" && installs != nil {
				if rerr := installs.Record(consent.Install{
					Fingerprint: fp,
					Identity:    destinationIdentityFor(sess),
					Path:        path.Dir(installed.command),
					Hash:        installed.generation,
					InstalledAt: time.Now().UTC(),
				}); rerr != nil {
					log.Warn("helper installed but the footprint observation was not recorded",
						"host", sess.Host(), "error", rerr)
				}
			}
			return transport.GitOpenSelection{Factory: &sessionFactory{
				reg:     reg,
				sid:     sess.ID(),
				host:    sess.Host(),
				account: accountFromOptions(sess.SSHOptions()),
				fp:      sess.HostKeyFingerprint(),
				opts:    sess.SSHOptions(),
				install: installed,
			}}
		case ConsentRequired:
			// The ask is a RESULT state, never an install: nothing was
			// written to the host.
			return transport.GitOpenSelection{ConsentRequired: true}
		default:
			// Refused — raw, a denied answer, or nothing to offer. The
			// probe failure and the missing artifact are facts with
			// states; a machine that refused has no earned state and the
			// transport answers the not-available error with the reason.
			if !available && perr != nil {
				log.Info("helper unavailable: the platform probe failed",
					"host", sess.Host(), "error", perr)
				return helperProbeRefusal(platform, perr)
			}
			if !available {
				// No artifact for the probed platform (D20): the build
				// matrix does not ship this OS/arch. The message names
				// which platform — never a generic error (brief).
				log.Info("helper unavailable: no artifact for the platform",
					"host", sess.Host(), "goos", platform.GOOS, "goarch", platform.GOARCH)
				return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
					State:   git.OpenUnsupportedPlatform,
					Message: "we build no helper for " + platform.GOOS + "/" + platform.GOARCH,
				}}
			}
			return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
				Message: refusedHelperReason(sess, store),
			}}
		}
	}, reg
}

// helperProbeRefusal maps a probe failure onto the §6 refusal it is: an
// artifact the matrix does not ship (ErrUnsupportedPlatform) or that was
// not built (ErrArtifactsNotBuilt) is unsupportedPlatform with the message
// naming the platform and the recovery; anything else that stopped the
// probe — a refused exec, a dead lane — is execForbidden with what was
// seen. The error is the probe's own, never re-derived.
func helperProbeRefusal(platform deploy.Platform, err error) transport.GitOpenSelection {
	switch {
	case errors.Is(err, deploy.ErrUnsupportedPlatform), errors.Is(err, helperartifacts.ErrArtifactsNotBuilt):
		return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
			State:   git.OpenUnsupportedPlatform,
			Message: unsupportedPlatformMessage(platform, err),
		}}
	default:
		return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
			State:   git.OpenExecForbidden,
			Message: "the host refused the probe that would run the helper: " + err.Error(),
		}}
	}
}

// unsupportedPlatformMessage names which fact the refusal is: a platform
// the build matrix deliberately does not ship, or an artifact that was not
// built. artifactErr is the Artifact error the probe already saw (or nil
// when the probe did not reach the artifact decision).
func unsupportedPlatformMessage(p deploy.Platform, artifactErr error) string {
	if errors.Is(artifactErr, helperartifacts.ErrArtifactsNotBuilt) {
		return "the helper artifact for " + p.GOOS + "/" + p.GOARCH + " was not built — run `make helpers` to build it"
	}
	return "we build no helper for " + p.GOOS + "/" + p.GOARCH
}

// refusedHelperReason is the resolver's Refused account for a machine whose
// mode or stored answer forbids the helper: the actionable reason the
// not-available error carries, never a generic refusal.
func refusedHelperReason(sess session.Session, store *consent.Store) string {
	// Every message here names a mode the connection form actually offers.
	// Before ADR-0033 this text told the user to pick "Auto", which was a
	// value only this package knew — an instruction that could not be
	// followed.
	switch effectiveModeFor(sess) {
	case profile.DesiredRaw:
		return "this connection is set to Raw, which does not run the nocx helper — change its Delivery mode to Auto or Helper to open repositories here"
	case profile.DesiredScript:
		// An answer, not a gap: script asked for the shell tiers and not
		// the binary, so this is not a refusal to explain away but a
		// choice to name back.
		return "this connection is set to Script, which installs the shell integration but not the nocx helper — change its Delivery mode to Auto to be offered the helper, or to Helper to allow it outright"
	}
	if store != nil {
		if ans, ok := store.Lookup(sess.HostKeyFingerprint()); ok && ans == consent.Denied {
			return "this machine has declined to run the nocx helper — set the connection's Delivery mode to Helper, or change the answer in the footprint screen, to allow it"
		}
	}
	return "no helper available for this SSH session"
}

// probeHelperPlatform asks the session's host what platform it is (D20)
// and whether an artifact exists for it — the deployment side of the
// resolver's "a suitable binary exists for that platform" arm. The probe
// is one bounded exec and writes nothing; it is the only remote command
// the consent decision runs before the user has accepted.
func probeHelperPlatform(sess session.Session, lanes helperInstallProvider, source deploy.ArtifactSource) (deploy.Platform, bool, error) {
	_, platform, available, err := probeHelperPlatformAt(context.Background(), sess.Host(), sess.SSHOptions(), lanes, source)
	return platform, available, err
}

// probeHelperPlatformAt is the probe for every caller that has nothing to open
// on the connection afterwards: it takes the lease, asks, and releases. The
// open path is deliberately not one of those callers — it hands the reference
// back instead, so the pane it is about to open rides the authentication the
// probe already paid for (probeHelperPlatformHeld, nocx-k6p18.35).
func probeHelperPlatformAt(ctx context.Context, host string, opts []ssh.ConnectOption, lanes helperInstallProvider, source deploy.ArtifactSource) (string, deploy.Platform, bool, error) {
	hold, fingerprint, platform, available, err := probeHelperPlatformHeld(ctx, host, opts, lanes, source)
	hold.release()
	return fingerprint, platform, available, err
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

// installHelperFor installs the helper artifact on sess's host for the
// already-probed platform and returns the install (D7, D21): the deploy
// wiring, replacing the env-configuration the factory used to read. The
// context is background — the selection has no caller context — and the
// install lease's own hard timeout is what bounds the acquisition (the
// filesystemProviderFactory precedent).
func installHelperFor(sess session.Session, lanes helperInstallProvider, source deploy.ArtifactSource, platform deploy.Platform) (installedHelper, error) {
	return installHelperAt(context.Background(), sess.Host(), sess.SSHOptions(), lanes, source, platform)
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
	command, hash, err := deploy.Ensure(ctx, fsys, source, home, platform)
	if err != nil {
		return installedHelper{}, err
	}
	if err := deploy.Prune(ctx, fsys, home, path.Base(path.Dir(command))); err != nil {
		return installedHelper{}, fmt.Errorf("prune for %s: %w", host, err)
	}
	return installedHelper{dir: path.Dir(command), generation: hash, command: command}, nil
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

// effectiveModeFor re-derives the session's resolved desired mode from the
// connect options the session was opened with (session.Reg stamps the
// resolved cascade answer as WithDesiredMode). An absent answer means no
// profile spoke for this destination — a direct host or an ad-hoc open —
// and resolves to profile.DesiredAuto, the same value the cascade's
// hardcoded default carries (ADR-0033).
func effectiveModeFor(sess session.Session) profile.DesiredMode {
	cfg := &ssh.ConnectConfig{}
	for _, o := range sess.SSHOptions() {
		o(cfg)
	}
	return profile.DesiredMode(cfg.DesiredMode)
}

// destinationIdentityFor renders the display identity (user@host:port) the
// footprint surface shows for a helper installation — the same spelling a
// saved connection would resolve to, as far as the session's own options
// carry it.
func destinationIdentityFor(sess session.Session) string {
	cfg := &ssh.ConnectConfig{}
	for _, o := range sess.SSHOptions() {
		o(cfg)
	}
	host := sess.Host()
	if host == "" {
		return ""
	}
	if cfg.User != "" {
		host = cfg.User + "@" + host
	}
	if cfg.Port != 0 {
		host = host + ":" + strconv.Itoa(cfg.Port)
	}
	return host
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

// helperRegistry owns the helper processes the composition root started:
// one per session, shared by every binding that session opens. A session is
// one pooled connection — one host, one principal, one set of connect
// options — so sharing within it is the design's "one process per helper
// connection (D4) bounded by the binding registry", and the helper lives
// while any binding references it. Two sessions to the same host — even
// the same user — each get their own helper: the pool key that would prove
// they share one connection is resolved inside internal/ssh and not
// exposed, and sharing a helper across principals would be an
// authorization error. Cross-session sharing waits for that seam.
type helperRegistry struct {
	lanes     laneProvider
	install   helperInstallProvider
	source    deploy.ArtifactSource
	log       *slog.Logger
	consent   *consent.Store
	registry  *session.Reg
	lifecycle lifecyclechannel.Kernel
	// lifecycleLoss carries a helper-hosted adapter's loss cause to the
	// session integration axis, the same seam the local pty factory uses.
	// It is a separate seam from the published facts for the same reason it
	// is there: a channel that ends establishes nothing new, so no fact
	// moves and the product would otherwise learn nothing (nocx-dvql).
	// Nil (tests, or a server without the wiring) reports nowhere and the
	// loss is still logged by the adapter.
	lifecycleLoss func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause)
	mu            sync.Mutex
	hosts         map[session.ID]*hostHelper
	closing       map[string]struct{}
	// farTools are the far-side tool sockets this registry opened, keyed by the
	// session each belongs to (nocx-e2bws). They are held HERE rather than by
	// the hostHelper because the event that ends them is a session's end and not
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
func (r *helperRegistry) SessionEnded(sid session.ID) {
	r.mu.Lock()
	_, ok := r.farTools[sid]
	r.mu.Unlock()
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

// OpenHosted applies the same helper resolver used by git.open, then spawns
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
	hold, fingerprint, platform, available, err := probeHelperPlatformHeld(ctx, cfg.Host, opts, r.install, r.source)
	if err != nil && !available {
		return transport.HostedSessionOpen{}, hold, false, nil
	}
	resolver := newResolver(
		withStore(r.consent),
		withHelperArtifactAvailable(available),
		withHelperRequested(true),
	)
	if resolver.Resolve(Machine{Fingerprint: fingerprint, Mode: profile.DesiredMode(cfg.Remote.DesiredMode)}) != DesiredHelper {
		return transport.HostedSessionOpen{}, hold, false, nil
	}
	opened, selected, err := r.openFarHelper(ctx, cfg, claim, opts, platform, fingerprint)
	return opened, hold, selected, err
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
	installed, err := installHelperAt(ctx, cfg.Host, opts, r.install, r.source, platform)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	f := &sessionFactory{reg: r, sid: session.NewID(), host: cfg.Host, account: accountFromOptions(opts), opts: opts, install: installed}
	h := &hostHelper{f: f, lanes: r.lanes, log: r.log}
	h.mu.Lock()
	c, outcome, err := h.connectLocked(ctx)
	h.mu.Unlock()
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	if outcome.State != "" {
		return transport.HostedSessionOpen{}, true, errors.New(outcome.Message)
	}
	var subscriberRaw [16]byte
	if _, randErr := rand.Read(subscriberRaw[:]); randErr != nil {
		return transport.HostedSessionOpen{}, true, randErr
	}
	subscriber := proto.SubscriberID(hex.EncodeToString(subscriberRaw[:]))
	var lifecycleAdapter *lifecyclechannel.Adapter
	var lifecyclePeer net.Conn
	var lifecycleLaunch *proto.LifecycleLaunch
	if r.lifecycle != nil {
		coordinatorConn, peerConn := net.Pipe()
		var lifecycleErr error
		// The caller's exchange, for the reason helper_hosted.go gives.
		lifecycleAdapter, lifecycleErr = lifecyclechannel.NewStream(
			log.NewSlogAdapter(r.log).WithContext(ctx), r.lifecycle, coordinatorConn,
			lifecyclechannel.WithLossReporter(r.reportLifecycleLoss),
		)
		if lifecycleErr != nil {
			_ = peerConn.Close()
			_ = c.Close()
			return transport.HostedSessionOpen{}, true, lifecycleErr
		}
		lifecyclePeer = peerConn
		launch := lifecycleAdapter.Launch()
		lifecycleLaunch = &proto.LifecycleLaunch{
			Lane: string(launch.Lane), Domain: string(launch.Domain),
			Epoch: launch.Epoch, Capability: launch.Capability, Recovery: launch.Recovery,
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
		prepared, perr := r.tools.PrepareFarPaneToolSocket(ctx, cfg.Host, opts, claim)
		if perr != nil {
			r.log.Warn("far pane: no tool socket for this pane", "host", cfg.Host, "error", perr)
		} else {
			routes, toolToken = prepared, mintToolToken()
		}
	}
	entry, err := c.Spawn(ctx, proto.SpawnParams{
		Cwd: cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows, Lifecycle: lifecycleLaunch,
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
	if err != nil {
		if lifecycleAdapter != nil {
			_ = lifecycleAdapter.Close()
			_ = lifecyclePeer.Close()
		}
		_ = c.Close()
		return transport.HostedSessionOpen{}, true, err
	}
	attached, err := c.Attach(ctx, proto.AttachParams{
		Subscriber: subscriber,
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true,
		LifecycleOffset: 0, LifecycleFresh: true, RequestWrite: true,
	})
	if err != nil {
		if lifecycleAdapter != nil {
			_ = lifecycleAdapter.Close()
			_ = lifecyclePeer.Close()
		}
		_ = c.CloseSession(ctx, entry.HostSessionID)
		_ = c.Close()
		return transport.HostedSessionOpen{}, true, err
	}
	sid := session.ID(entry.HostSessionID.Session)
	f.sid = sid
	sess, err := r.registry.Adopt(ctx, cfg, sid, attached)
	if err != nil {
		_ = attached.Close()
		if lifecycleAdapter != nil {
			_ = lifecycleAdapter.Close()
			_ = lifecyclePeer.Close()
		}
		_ = c.CloseSession(ctx, entry.HostSessionID)
		_ = c.Close()
		return transport.HostedSessionOpen{}, true, err
	}
	r.mu.Lock()
	r.hosts[sid] = h
	r.mu.Unlock()
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
		id, oerr := r.tools.OpenFarPaneToolSocket(ctx, cfg.Host, opts, routes, string(sid))
		switch {
		case oerr != nil:
			// The launch already names the socket, so a listener that never came
			// up is a far agent that would fail on a path nothing serves. Said
			// out loud, and the directory goes with it: it is this open's own
			// scaffolding.
			r.log.Warn("far pane: the tool socket was not opened; the pane runs with no tools",
				"host", cfg.Host, "path", routes.Path, "error", oerr)
			if rerr := r.tools.RemoveFarPaneToolSocketDir(ctx, cfg.Host, opts, routes); rerr != nil {
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
	if lifecycleAdapter != nil {
		lifecycleLane = lifecycleAdapter.Lane()
		var startOnce sync.Once
		startLifecycle = func() {
			startOnce.Do(func() {
				bridgeLifecycle(log.NewSlogAdapter(h.log).WithContext(ctx),
					lifecycleAdapter.TransportID(), lifecyclePeer, attached.Lifecycle())
			})
		}
		var abortOnce sync.Once
		abortLifecycle = func() {
			abortOnce.Do(func() {
				_ = lifecycleAdapter.Close()
				_ = lifecyclePeer.Close()
			})
		}
	}
	return transport.HostedSessionOpen{
		Session: sess, Host: cfg.Host, Account: f.account, Generation: installed.generation,
		HelperCommand: installed.command, Fingerprint: fingerprint,
		LifecycleLane: lifecycleLane, StartLifecycle: startLifecycle,
		AbortLifecycle: abortLifecycle,
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
func bridgeLifecycle(lg log.Logger, transport lifecycle.TransportID, peer net.Conn, carrier io.ReadWriteCloser) {
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
		n, err := io.Copy(carrier, countingFirst(lg, "the adapter's first bytes reached the shell", peer))
		lg.Info("lifecycle bridge: the adapter's end closed",
			"to_shell_bytes", n, "carried_nothing", n == 0, "error", err)
		closeBoth()
	}()
	go func() {
		n, err := io.Copy(peer, countingFirst(lg, "the shell's first bytes reached the adapter", carrier))
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
// it without a sessionFactory: the pane screen read (nocx-ygxjv.3).
//
// It is the registry's own map and not a second one, because that map is
// already the record of which helper holds which session — the hosted open
// writes it, the readopt pass writes it — and a parallel map would be a second
// answer that agrees until one of the two is forgotten.
func (r *helperRegistry) hostFor(sid session.ID) (*hostHelper, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hosts[sid]
	return h, ok
}

func (r *helperRegistry) helper(f *sessionFactory) *hostHelper {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hosts[f.sid]; ok {
		return h
	}
	h := &hostHelper{f: f, lanes: r.lanes, log: r.log}
	if _, ok := r.closing[f.fp]; ok {
		h.closing = true
	}
	r.hosts[f.sid] = h
	return h
}

func (r *helperRegistry) forget(f *sessionFactory) {
	r.mu.Lock()
	delete(r.hosts, f.sid)
	r.mu.Unlock()
}

func (r *helperRegistry) isClosing(fp string) bool {
	r.mu.Lock()
	_, ok := r.closing[fp]
	r.mu.Unlock()
	return ok
}

// inventories returns one reconciliation inventory per helper generation
// currently held by this coordinator. A helper channel that was never opened
// has no client and therefore cannot claim any stored id space.
func (r *helperRegistry) inventories() []sessionInventory {
	r.mu.Lock()
	helpers := make([]*hostHelper, 0, len(r.hosts))
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
	helpers := make([]*hostHelper, 0, len(r.hosts))
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

// CloseHelpersFor first asks every helper daemon this coordinator knows about
// for its sessions and closes each one through the daemon's close-session
// operation. Only after all those acknowledgements does it close the bridge
// channels and forget the registry entries. A daemon that cannot enumerate
// sessions refuses the operation rather than allowing uninstall to delete a
// live session's executable.
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
	var victims []*hostHelper
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

func (h *hostHelper) cancelClosing() {
	h.mu.Lock()
	h.closing = false
	h.mu.Unlock()
}

func (h *hostHelper) closeSessions(ctx context.Context) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return errors.New("helper is already closing")
	}
	h.closing = true
	if h.client == nil && h.factory == nil && !h.dead {
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

// sessionFactory is a git.RepoFactory for one session: stateless (the state
// lives in the registry), so the two times git.open consults the selection
// both resolve to the same shared helper.
type sessionFactory struct {
	reg     *helperRegistry
	sid     session.ID
	host    string
	account string
	// fp is the machine's host public-key fingerprint — the consent key —
	// captured at selection time so the registry can close every live
	// helper channel on a machine without holding a session (D25).
	fp   string
	opts []ssh.ConnectOption
	// install is the completed install this session's git rides: the machine
	// identity and the generation the lane names, and the installed binary's
	// path for the two readers that need a path rather than a lane.
	install installedHelper
}

func (f *sessionFactory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	if f.reg.isClosing(f.fp) {
		return nil, git.OpenOutcome{}, errors.New("helper is closing for uninstall")
	}
	h := f.reg.helper(f)
	repo, outcome, err := h.open(ctx, cwd)
	if err != nil && errors.Is(err, client.ErrLost) {
		// The shared client died under us: real transport loss, not the
		// last-binding-close (that path sets dead and never reuses the
		// client). A fresh dial can heal the session, and open is
		// idempotent — exactly one retry; a mutation would never be retried
		// (D12).
		h.evict()
		if f.reg.isClosing(f.fp) {
			return nil, git.OpenOutcome{}, errors.New("helper is closing for uninstall")
		}
		repo, outcome, err = h.open(ctx, cwd)
	}
	return repo, outcome, err
}

// hostHelper is one session's shared helper process. opens are serialized
// per session (the transport's git-open lane already is; the mutex makes
// the refusal-close and the last-close exact rather than racy), so the
// reference count here mirrors the helper factory's own count event for
// event.
type hostHelper struct {
	f     *sessionFactory
	lanes laneProvider
	log   *slog.Logger

	mu      sync.Mutex
	client  *client.Client
	factory git.RepoFactory
	refs    int
	dead    bool // the shared client is closed; the next open must redial
	closing bool // uninstall has frozen this helper against new opens
}

// connectLocked returns the existing carrier or establishes a new bridge to
// the helper daemon. A lost carrier is disposable; the daemon's endpoint and
// session state live on the host and are reached again through a fresh lane.
// screenClient is the route a remote pane's screen is read through: this
// helper's carrier, and the handle it knows the session by.
//
// The generation is the one the daemon answers to — f.install.generation, the
// same
// value an inventory row carries and the same one the session-close path
// compares against — because a handle addressed to another generation names
// nothing there and the helper refuses it.
func (h *hostHelper) screenClient(ctx context.Context, sid string) (*client.Client, client.HostSessionID, error) {
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

func (h *hostHelper) connectLocked(ctx context.Context) (*client.Client, git.OpenOutcome, error) {
	if h.client != nil && !h.dead {
		select {
		case <-h.client.Done():
			h.client = nil
			h.factory = nil
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
	h.factory = helpergit.NewFactory(c)
	return c, git.OpenOutcome{}, nil
}

// inventoryClient preserves the hostHelper registry entry across carrier loss.
// It only redials an entry that had already opened a helper; an unstarted
// entry must not make inventory claim a helper exists.
func (h *hostHelper) inventoryClient(ctx context.Context) (*client.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil && h.factory == nil && !h.dead {
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
func (h *hostHelper) invalidate(c *client.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != c {
		return
	}
	h.client = nil
	h.factory = nil
	h.dead = true
}

func (h *hostHelper) open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return nil, git.OpenOutcome{}, errors.New("helper is closing for uninstall")
	}
	if h.dead || h.factory == nil {
		lane, err := h.lanes.LaneConn(ctx, h.f.host, h.f.install.machine(),
			proto.GenerationID(h.f.install.generation), h.f.opts...)
		if err != nil {
			return nil, git.OpenOutcome{}, fmt.Errorf("helper lane for %s: %w", h.f.host, err)
		}
		c, err := client.Dial(ctx, client.Config{
			Exec:       lane,
			ExpectHash: h.f.install.generation,
			Log:        h.log,
		})
		if err != nil {
			// Dial's contract: on failure the lane is left for the caller
			// to close.
			_ = lane.Close()
			// The dial refusals are the §6 states, not errors: the panel
			// renders a version-mismatched helper or a refused exec as an
			// honest state naming the recovery (remote-helper design §6).
			// ErrLost is not a refusal — it passes through so the caller's
			// one retry (sessionFactory.Open) can heal the session.
			if outcome, ok := dialFailure(err, h.f.host); ok {
				return nil, outcome, nil
			}
			return nil, git.OpenOutcome{}, err
		}
		h.client = c
		h.dead = false
		h.factory = helpergit.NewFactory(c)
	}
	repo, outcome, err := h.factory.Open(ctx, cwd)
	if err != nil {
		return nil, git.OpenOutcome{}, err
	}
	if outcome.State != git.OpenOK {
		// A refusing open carries no repo, so the helper factory never
		// counts it — if nothing else references the helper, close it
		// rather than leaving a process with no owner running on the far
		// host.
		if h.refs == 0 {
			h.closeLocked()
		}
		return repo, outcome, nil
	}
	h.refs++
	return &refRepo{Repo: repo, released: h.released}, outcome, nil
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

// released is called by the wrapping repo when a binding closes. The
// wrapped helper repo's own Close has already run the factory's release,
// which closes the shared client at zero; this half forgets the entry so
// the next open brings one helper up fresh instead of reusing a dead
// client.
func (h *hostHelper) released() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refs--
	if h.refs <= 0 {
		h.refs = 0
		h.closeLocked()
	}
}

// evict forgets the shared client after transport loss. The client is
// already dead (its Done closed); the entry must not be reused.
func (h *hostHelper) evict() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client = nil
	h.factory = nil
	h.dead = true
}

func (h *hostHelper) closeLocked() {
	if h.client != nil {
		_ = h.client.Close()
	}
	h.client = nil
	h.factory = nil
	h.dead = true
	h.f.reg.forget(h.f)
}

// refRepo wraps a helper-backed repo so the composition root can count the
// bindings referencing its shared helper. The wrapped repo's Close releases
// the helper factory's reference (which closes the shared client at zero);
// the wrapper then tells the hostHelper, which forgets the entry.
type refRepo struct {
	git.Repo
	released func()
	once     sync.Once
}

func (r *refRepo) Close() error {
	err := r.Repo.Close()
	r.once.Do(r.released)
	return err
}

// reportLifecycleLoss is the adapter's loss sink for every helper-hosted
// session, opened or taken back. A method rather than the field itself so a
// registry without the wiring is one nil check here instead of one at each
// construction site.
func (r *helperRegistry) reportLifecycleLoss(lane lifecycle.LaneID, cause lifecyclechannel.LossCause) {
	if r == nil || r.lifecycleLoss == nil {
		return
	}
	r.lifecycleLoss(lane, cause)
}
