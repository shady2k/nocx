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
	"sync"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
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
// transport.GitFactoryFor: for an SSH session it installs the helper
// artifact on that session's host (D7) and returns a factory that serves
// git over one helper process on that session's pooled connection, or nil
// when no helper is available — nil is what keeps git.open's
// OpenRemoteUnsupported refusal standing. git.open consults the selection
// twice (the refusal decision, then the open); each consultation installs
// idempotently, and an already-complete directory uploads nothing (D7), so
// both consultations converge on the same install. The dial happens inside
// the returned factory's Open, never here.
func helperGitFactory(lanes helperInstallProvider, log *slog.Logger) transport.GitFactoryFor {
	reg := &helperRegistry{lanes: lanes, log: log, hosts: make(map[session.ID]*hostHelper)}
	return func(sess session.Session) git.RepoFactory {
		command, hash, err := installHelperFor(sess, lanes)
		if err != nil {
			// Fail-open (D16): a host with no helper keeps the refusal. The
			// failure is a fact about the host or the build — an exec that
			// cannot be probed, a platform we do not build for, artifacts
			// that were never built — and it is logged, never silent, so a
			// soft degrade stays visible (AGENTS.md).
			log.Info("helper unavailable; the zero-install refusal stands",
				"host", sess.Host(), "error", err)
			return nil
		}
		return &sessionFactory{
			reg:        reg,
			sid:        sess.ID(),
			host:       sess.Host(),
			opts:       sess.SSHOptions(),
			command:    command,
			expectHash: hash,
		}
	}
}

// installHelperFor installs the helper artifact on sess's host and returns
// the absolute command path and the content hash to expect from it (D7,
// D21): the deploy wiring, replacing the env-configuration the factory used
// to read. The context is background — the selection has no caller context
// — and the install lease's own hard timeout is what bounds the acquisition
// (the filesystemProviderFactory precedent).
func installHelperFor(sess session.Session, lanes helperInstallProvider) (command, hash string, err error) {
	ctx := context.Background()

	probe, err := lanes.DiscoveryConn(ctx, sess.Host(), sess.SSHOptions()...)
	if err != nil {
		return "", "", fmt.Errorf("probe lease for %s: %w", sess.Host(), err)
	}
	defer func() { _ = probe.Close() }()
	platform, err := deploy.Probe(ctx, probeExec{probe})
	if err != nil {
		return "", "", err
	}

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
