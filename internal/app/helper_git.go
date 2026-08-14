package app

// The helper-backed git factory selection (the remote-helper design): the
// composition root's answer to transport.GitFactoryFor. An SSH session gets
// a helper-backed git.RepoFactory when a helper is configured for this
// machine; otherwise nil, and git.open keeps its OpenRemoteUnsupported
// refusal — the zero-install fallback (design D3 as amended 2026-08-13,
// D16).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
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

// helperCommandEnvVar and helperHashEnvVar are the composition-root knobs
// that say a remote helper is available: the command to run on a remote
// host and the content hash to expect from it (D21). They are env vars, not
// settings, for the same reason the log level is — the value is owned by
// another module (the deploy bead, nocx-jwye, which writes the D7 install
// layout the command names and whose hash is the directory key), and a
// setting would drag the store and the renderer into a knob only the
// installer reads. Unset — either one — means no helper is available and
// the OpenRemoteUnsupported refusal stands.
const (
	helperCommandEnvVar = "NOCX_HELPER_COMMAND"
	helperHashEnvVar    = "NOCX_HELPER_HASH"
)

// helperGitFactory is the composition root's answer to
// transport.GitFactoryFor: for an SSH session it resolves the helper
// configuration and returns a factory that serves git over one helper
// process on that session's pooled connection, or nil when no helper is
// configured — nil is what keeps git.open's OpenRemoteUnsupported refusal
// standing. The function is side-effect-free: git.open consults it twice
// (the refusal decision, then the open), and the dial happens inside the
// returned factory's Open, never here.
func helperGitFactory(lanes helperLaneProvider, log *slog.Logger) transport.GitFactoryFor {
	reg := &helperRegistry{lanes: lanes, log: log, hosts: make(map[session.ID]*hostHelper)}
	return func(sess session.Session) git.RepoFactory {
		command, hash, ok := helperConfigFromEnv()
		if !ok {
			return nil
		}
		return &sessionFactory{reg: reg, sid: sess.ID(), host: sess.Host(), opts: sess.SSHOptions(), command: command, expectHash: hash}
	}
}

func helperConfigFromEnv() (command, expectHash string, ok bool) {
	command = os.Getenv(helperCommandEnvVar)
	expectHash = os.Getenv(helperHashEnvVar)
	if command == "" || expectHash == "" {
		return "", "", false
	}
	return command, expectHash, true
}

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
