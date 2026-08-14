package app

// The helper-backed git factory selection (the remote-helper design): the
// composition root's answer to transport.GitFactoryFor. An SSH session gets
// a helper-backed git.RepoFactory when the helper is INSTALLED for this
// machine; otherwise nil, and git.open keeps its OpenRemoteUnsupported
// refusal — the zero-install fallback (design D3 as amended 2026-08-13,
// D16). The factory's command and expected hash are never configuration:
// each consultation installs the artifact on the session's host (D7) and
// takes them from what was installed.

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
// tri-state selection answers consentRequired when the machine has no
// relay-tier answer, and the zero-install refusal (empty selection) when
// it is denied, raw, or nothing to offer. git.open consults the selection
// twice (the refusal decision, then the open); each consultation installs
// idempotently, and an already-complete directory uploads nothing (D7), so
// both consultations converge on the same install. The dial happens inside
// the returned factory's Open, never here.
func helperGitFactory(lanes helperInstallProvider, store *consent.Store, installs *consent.InstallStore, log *slog.Logger) transport.GitFactoryFor {
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
				log.Info("helper unavailable; the zero-install refusal stands",
					"host", sess.Host(), "error", perr)
				return transport.GitOpenSelection{}
			}
			command, hash, err := installHelperFor(sess, lanes, platform)
			if err != nil {
				// Fail-open (D16): a host whose install failed keeps the
				// refusal. The failure is a fact about the host or the
				// build, and it is logged, never silent, so a soft degrade
				// stays visible (AGENTS.md).
				log.Info("helper unavailable; the zero-install refusal stands",
					"host", sess.Host(), "error", err)
				return transport.GitOpenSelection{}
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
				opts:       sess.SSHOptions(),
				command:    command,
				expectHash: hash,
			}}
		case ConsentRequired:
			// The ask is a RESULT state, never an install: nothing was
			// written to the host.
			return transport.GitOpenSelection{ConsentRequired: true}
		default:
			if perr != nil {
				// A host that could not be probed, or a platform we do not
				// build for, is a fact the operator should see — never a
				// silent degrade (AGENTS.md).
				log.Info("helper unavailable; the zero-install refusal stands",
					"host", sess.Host(), "error", perr)
			}
			return transport.GitOpenSelection{}
		}
	}
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
// resolved cascade answer as WithDesiredMode). An absent answer is the
// hardcoded auto default — the resolver treats "" as auto.
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

// sessionFactory is a git.RepoFactory for one session: stateless (the state
// lives in the registry), so the two times git.open consults the selection
// both resolve to the same shared helper.
type sessionFactory struct {
	reg        *helperRegistry
	sid        session.ID
	host       string
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
			Command:    h.f.command,
			ExpectHash: h.f.expectHash,
			Log:        h.log,
		})
		if err != nil {
			// Dial's contract: on failure the lane is left for the caller
			// to close.
			_ = lane.Close()
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
