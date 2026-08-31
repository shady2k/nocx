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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// helperLaneProvider acquires the pty-less exec lane a helper rides on one
// host (design D19). *ssh.RealClient satisfies it; the interface exists so
// the factory is testable against a double without a live connection — the
// same reason internal/filesystem/sftp declares its own narrow fsConn seam.
type helperLaneProvider interface {
	HelperConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperConn, error)
}

// helperInstallProvider is the full composition-root surface the factory
// needs to bring a helper up on a host: the exec lane the helper rides
// (D19), the write-capable install lease the deploy package installs
// through (D7), and the bounded one-shot exec the platform probe uses
// (D20). *ssh.RealClient satisfies all three; the interface exists so the
// factory is testable against doubles without a live connection. The
// registry itself keeps the narrow helperLaneProvider — install is a
// selection-time concern, not a per-session one.
type helperInstallProvider interface {
	helperLaneProvider
	HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error)
	DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error)
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
func helperGitFactory(lanes helperInstallProvider, store *consent.Store, installs *consent.InstallStore, log *slog.Logger) (transport.GitFactoryFor, *helperRegistry) {
	reg := &helperRegistry{lanes: lanes, log: log, hosts: make(map[session.ID]*hostHelper)}
	return func(sess session.Session) transport.GitOpenSelection {
		// The platform probe is the one bounded remote exec the decision
		// runs before the user has accepted anything — it writes nothing.
		// The install, the prune and the footprint observation are reached
		// only when the machine resolves to relay (D8: consent is asked
		// when the user reaches for the feature, not when a connection is
		// made; nothing is written before the ask is answered).
		platform, available, perr := probeHelperPlatform(sess, lanes)
		r := newResolver(
			withStore(store),
			withHelperArtifactAvailable(available),
			withHelperRequested(true), // git.open is the surface reaching for the helper
		)
		switch r.Resolve(Machine{Fingerprint: sess.HostKeyFingerprint(), Mode: effectiveModeFor(sess)}) {
		case DesiredRelay:
			if perr != nil {
				// The probe's failure is a fact with a state (§6), never
				// a silent degrade: an artifact the matrix does not ship
				// (or was not built) is unsupportedPlatform; anything
				// else that stopped the probe is execForbidden.
				log.Info("helper unavailable: the platform probe failed",
					"host", sess.Host(), "error", perr)
				return helperProbeRefusal(platform, perr)
			}
			command, hash, err := installHelperFor(sess, lanes, platform)
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
			// keeps a nil store (a test double) from panicking the relay
			// path it never exercises.
			if fp := sess.HostKeyFingerprint(); fp != "" && installs != nil {
				if rerr := installs.Record(consent.Install{
					Fingerprint: fp,
					Identity:    destinationIdentityFor(sess),
					Path:        path.Dir(command),
					Hash:        hash,
					InstalledAt: time.Now().UTC(),
				}); rerr != nil {
					log.Warn("helper installed but the footprint observation was not recorded",
						"host", sess.Host(), "error", rerr)
				}
			}
			return transport.GitOpenSelection{Factory: &sessionFactory{
				reg:        reg,
				sid:        sess.ID(),
				host:       sess.Host(),
				fp:         sess.HostKeyFingerprint(),
				opts:       sess.SSHOptions(),
				command:    command,
				expectHash: hash,
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
	case errors.Is(err, deploy.ErrUnsupportedPlatform), errors.Is(err, deploy.ErrArtifactsNotBuilt):
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
	if errors.Is(artifactErr, deploy.ErrArtifactsNotBuilt) {
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
		return "this connection is set to Raw, which does not run the nocx helper — change its Delivery mode to Auto or Relay to open repositories here"
	case profile.DesiredScript:
		// An answer, not a gap: script asked for the shell tiers and not
		// the binary, so this is not a refusal to explain away but a
		// choice to name back.
		return "this connection is set to Script, which installs the shell integration but not the nocx helper — change its Delivery mode to Auto to be offered the helper, or Relay to allow it outright"
	}
	if store != nil {
		if ans, ok := store.Lookup(sess.HostKeyFingerprint()); ok && ans == consent.Denied {
			return "this machine has declined to run the nocx helper — set the connection's Delivery mode to Relay, or change the answer in the footprint screen, to allow it"
		}
	}
	return "no helper available for this SSH session"
}

// probeHelperPlatform asks the session's host what platform it is (D20)
// and whether an artifact exists for it — the deployment side of the
// resolver's "a suitable binary exists for that platform" arm. The probe
// is one bounded exec and writes nothing; it is the only remote command
// the consent decision runs before the user has accepted.
func probeHelperPlatform(sess session.Session, lanes helperInstallProvider) (deploy.Platform, bool, error) {
	ctx := context.Background()
	probe, err := lanes.DiscoveryConn(ctx, sess.Host(), sess.SSHOptions()...)
	if err != nil {
		return deploy.Platform{}, false, fmt.Errorf("probe lease for %s: %w", sess.Host(), err)
	}
	defer func() { _ = probe.Close() }()
	platform, err := deploy.Probe(ctx, probeExec{probe})
	if err != nil {
		return deploy.Platform{}, false, err
	}
	if _, _, aerr := deploy.DefaultSource.Artifact(platform); aerr != nil {
		return platform, false, aerr
	}
	return platform, true, nil
}

// installHelperFor installs the helper artifact on sess's host for the
// already-probed platform and returns the absolute command path and the
// content hash to expect from it (D7, D21): the deploy wiring, replacing
// the env-configuration the factory used to read. The context is
// background — the selection has no caller context — and the install
// lease's own hard timeout is what bounds the acquisition (the
// filesystemProviderFactory precedent).
func installHelperFor(sess session.Session, lanes helperInstallProvider, platform deploy.Platform) (command, hash string, err error) {
	ctx := context.Background()

	conn, err := lanes.HelperInstallConn(ctx, sess.Host(), sess.SSHOptions()...)
	if err != nil {
		return "", "", fmt.Errorf("install lease for %s: %w", sess.Host(), err)
	}
	defer func() { _ = conn.Close() }()
	home, err := conn.Home()
	if err != nil {
		return "", "", fmt.Errorf("remote home for %s: %w", sess.Host(), err)
	}
	fsys := installFS{conn}
	command, hash, err = deploy.Ensure(ctx, fsys, deploy.DefaultSource, home, platform)
	if err != nil {
		return "", "", err
	}
	// Bound the footprint: every superseded install on the host goes, the
	// one just installed never (D25).
	if err := deploy.Prune(ctx, fsys, home, path.Base(path.Dir(command))); err != nil {
		return "", "", fmt.Errorf("prune for %s: %w", sess.Host(), err)
	}
	return command, hash, nil
}

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
	lanes helperLaneProvider
	log   *slog.Logger

	mu    sync.Mutex
	hosts map[session.ID]*hostHelper
}

func (r *helperRegistry) helper(f *sessionFactory) *hostHelper {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hosts[f.sid]; ok {
		return h
	}
	h := &hostHelper{f: f, lanes: r.lanes, log: r.log}
	r.hosts[f.sid] = h
	return h
}

func (r *helperRegistry) forget(f *sessionFactory) {
	r.mu.Lock()
	delete(r.hosts, f.sid)
	r.mu.Unlock()
}

// CloseHelpersFor closes every live helper channel on the machine whose
// host public-key fingerprint is fp — the D25 order an uninstall is bound
// by: no helper may be running out of a directory being deleted, so the
// channels the backend KNOWS about (its own, per session) are closed
// before the install directory is removed. The registry's channels are
// the backend's whole knowledge: a helper running from a DIFFERENT nocx
// instance sharing the same $HOME is out of reach, which the design
// accepts because the backend can only account for its own channels.
// A closed helper is forgotten; the next open redials it, and a machine
// whose install directory is gone answers the honest dial refusal (D6).
func (r *helperRegistry) CloseHelpersFor(fp string) {
	if fp == "" {
		return
	}
	r.mu.Lock()
	var victims []*hostHelper
	for _, h := range r.hosts {
		if h.f.fp == fp {
			victims = append(victims, h)
		}
	}
	r.mu.Unlock()
	// closeLocked takes h.mu (an open in flight finishes first, so the
	// close is exact) and calls reg.forget, which re-takes r.mu — the
	// iteration therefore releases r.mu before closing.
	for _, h := range victims {
		h.closeLocked()
	}
}

// sessionFactory is a git.RepoFactory for one session: stateless (the state
// lives in the registry), so the two times git.open consults the selection
// both resolve to the same shared helper.
type sessionFactory struct {
	reg  *helperRegistry
	sid  session.ID
	host string
	// fp is the machine's host public-key fingerprint — the consent key —
	// captured at selection time so the registry can close every live
	// helper channel on a machine without holding a session (D25).
	fp         string
	opts       []ssh.ConnectOption
	command    string
	expectHash string
}

func (f *sessionFactory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	h := f.reg.helper(f)
	repo, outcome, err := h.open(ctx, cwd)
	if err != nil && errors.Is(err, client.ErrLost) {
		// The shared client died under us: real transport loss, not the
		// last-binding-close (that path sets dead and never reuses the
		// client). A fresh dial can heal the session, and open is
		// idempotent — exactly one retry; a mutation would never be
		// retried (D12).
		h.evict()
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
	lanes helperLaneProvider
	log   *slog.Logger

	mu      sync.Mutex
	client  *client.Client
	factory git.RepoFactory
	refs    int
	dead    bool // the shared client is closed; the next open must redial
}

func (h *hostHelper) open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead || h.factory == nil {
		lane, err := h.lanes.HelperConn(ctx, h.f.host, h.f.opts...)
		if err != nil {
			return nil, git.OpenOutcome{}, fmt.Errorf("helper lane for %s: %w", h.f.host, err)
		}
		c, err := client.Dial(ctx, client.Config{
			Exec:       lane,
			Command:    bridgeCommand(h.f.command, h.f.expectHash),
			ExpectHash: h.f.expectHash,
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

// bridgeCommand is what the exec lane runs on the remote host: the installed
// helper, asked to BRIDGE to the endpoint of the generation we installed
// (level-1 design §5, D11).
//
// It is not the helper serving over this channel's stdin and stdout any more,
// and that is the point. The authoritative endpoint is a private Unix socket
// on the host; the bridge connects to it and copies bytes, holding no session,
// no window and no lock. So the sessions live in a process that outlives this
// channel, this coordinator and this nocx — which is what makes a session
// survive a coordinator being replaced (D1) — while what rides the ssh channel
// is exactly what rode it before: the frame protocol, unchanged.
//
// The generation is the content hash the installer wrote (D7, D21), because a
// helper install is content-addressed and the generation IS the build: naming
// it here is what stops a bridge from reaching a DIFFERENT generation's
// sessions, and what lets two generations coexist on one host while an old one
// still holds somebody's shell (D4).
//
// No port forwarding is configured and none is required: nothing is forwarded.
func bridgeCommand(command, generation string) string {
	return command + " " + endpoint.BridgeCommand + " " + generation
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
