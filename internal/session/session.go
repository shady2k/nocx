package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/ssh"
)

type ID string

type Kind int

const (
	KindLocal Kind = iota
	KindRemote
)

type Config struct {
	Kind   Kind
	Cwd    string
	Host   string
	Local  *pty.Config
	Remote *ssh.ConnectConfig
	Cols   uint16
	Rows   uint16
	XPixel uint16
	YPixel uint16
	// Enhanced requests the marker-only prompt env (ADR-0006) for this session.
	Enhanced bool
	// ProfileID records the profile this session was opened from, enabling
	// the connection list to report which rows are live and when they were
	// last used (nocx-uxs5.4). Empty for ad-hoc/local sessions.
	ProfileID string
	// CredentialID records the credential this session was opened with.
	// Used by revocation to find sessions running that credential.
	// Empty for sessions with no linked credential (inline auth).
	CredentialID string
	// Sandbox is the wire opt-in for a filesystem-isolated local tab
	// (ADR-0019). nil for ordinary local and all SSH sessions.
	Sandbox *sandbox.Request
}

type PTYFactory interface {
	NewPTY(ctx context.Context, cfg pty.Config) (pty.Pty, error)
}

type OutputHandler func(data []byte) error

type Session interface {
	ID() ID
	Kind() Kind
	// Cwd is where the session's shell was started. It is the tab's name
	// until a program sets a title; it does NOT follow `cd`, which needs the
	// OSC 7 events in nocx-5mn.2.
	Cwd() string
	// ProfileID returns the profile ID this session was opened from.
	// Empty for ad-hoc/local sessions (nocx-uxs5.4).
	ProfileID() string

	// CredentialID returns the credential ID this session was opened with.
	// Empty for sessions with no linked credential (inline auth) and for
	// local/ad-hoc sessions.
	CredentialID() string

	// SandboxInfo returns the immutable sandbox metadata for a sandboxed
	// local tab, or nil for ordinary/SSH sessions (design spec §3.3).
	SandboxInfo() *sandbox.SessionInfo

	Write(p []byte) (int, error)
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Close() error
	Done() <-chan struct{}
	StartOutput(ctx context.Context, onOutput OutputHandler) error
}

// ProfileUsageTracker records profile session activity (nocx-uxs5.4).
// Implementations may persist last-used timestamps; a nil tracker is a no-op.
type ProfileUsageTracker interface {
	// SessionOpened is called when a session is created for a profile.
	SessionOpened(profileID string)
	// SessionClosed is called when a session for a profile ends.
	SessionClosed(profileID string)
	// LastUsedForProfiles returns the last-used time for each requested
	// profile ID. Profiles with no recorded usage are absent from the map.
	LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error)
}

type Registry interface {
	Open(ctx context.Context, cfg Config) (Session, error)
	Get(id ID) (Session, error)
	Close(id ID) error
	List() []Session
}

func NewID() ID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return IDFromBytes(b)
}

func IDFromBytes(b [16]byte) ID {
	buf := make([]byte, 32)
	hex.Encode(buf, b[:])
	return ID(buf)
}

func IDToBytes(id ID) ([16]byte, error) {
	if len(id) != 32 {
		return [16]byte{}, fmt.Errorf("session id must be 32 hex chars, got %d", len(id))
	}
	var b [16]byte
	_, err := hex.Decode(b[:], []byte(id))
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid session id hex: %w", err)
	}
	return b, nil
}

type Reg struct {
	log          log.Logger
	ptf          PTYFactory
	ssh          SSHFactory
	mu           sync.Mutex
	sessions     map[ID]*realSession
	usageTracker ProfileUsageTracker
}

// SSHFactory creates SSH connections (AD-4). Injected at the composition
// root so tests can stub it.
type SSHFactory interface {
	Connect(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error)
}

func New(logger log.Logger, ptf PTYFactory) *Reg {
	return &Reg{
		log:      logger,
		ptf:      ptf,
		sessions: make(map[ID]*realSession),
	}
}

// WithSSHFactory injects an SSH factory, enabling KindRemote sessions.
func (r *Reg) WithSSHFactory(f SSHFactory) *Reg {
	r.ssh = f
	return r
}

// WithProfileUsageTracker injects a ProfileUsageTracker, enabling last-used
// persistence and the sessions.status RPC (nocx-uxs5.4). A nil tracker is a
// no-op — Open and Close work without it.
func (r *Reg) WithProfileUsageTracker(t ProfileUsageTracker) *Reg {
	r.usageTracker = t
	return r
}

func (r *Reg) Open(ctx context.Context, cfg Config) (Session, error) {
	var ch Channel
	var err error

	var pt pty.Pty
	if cfg.Kind == KindRemote {
		if r.ssh == nil {
			return nil, fmt.Errorf("SSH sessions not available (no SSH factory wired)")
		}
		if cfg.Remote == nil {
			return nil, fmt.Errorf("remote session requires ConnectConfig")
		}
		ch, err = r.ssh.Connect(ctx, cfg.Host, sshOptionsFromConfig(cfg.Remote)...)
		if err != nil {
			return nil, fmt.Errorf("ssh connect: %w", err)
		}
	} else {
		var perr error
		pt, perr = r.ptf.NewPTY(ctx, pty.Config{
			Cwd:      cfg.Cwd,
			Cols:     cfg.Cols,
			Rows:     cfg.Rows,
			XPixel:   cfg.XPixel,
			YPixel:   cfg.YPixel,
			Enhanced: cfg.Enhanced,
			Sandbox:  cfg.Sandbox,
		})
		if perr != nil {
			return nil, fmt.Errorf("open session: %w", perr)
		}
		ch = pt
	}

	id := NewID()

	s := &realSession{
		id:           id,
		kind:         cfg.Kind,
		cwd:          resolveSessionCwd(cfg.Cwd),
		profileID:    cfg.ProfileID,
		credentialID: cfg.CredentialID,
		ch:           ch,
		log:          r.log.With("session_id", string(id)),
	}
	// Sandbox metadata rides up from the PTY so the open result can carry
	// {backend, workspace, writableRoots} without the transport owning any
	// policy (design spec §4.5). Only local PTYs can be sandboxed.
	if pi, ok := pt.(pty.SandboxInfoProvider); ok {
		s.sandboxInfo = pi.SandboxInfo()
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.log.Info("session opened", "id", string(id), "kind", kindName(cfg.Kind), "profile_id", cfg.ProfileID)
	if r.usageTracker != nil && cfg.ProfileID != "" {
		r.usageTracker.SessionOpened(cfg.ProfileID)
	}
	return s, nil
}

func (r *Reg) Get(id ID) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return s, nil
}

func (r *Reg) Close(id ID) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	r.log.Info("session closed", "id", string(id))
	err := s.Close()
	if r.usageTracker != nil && s.profileID != "" {
		r.usageTracker.SessionClosed(s.profileID)
	}
	return err
}

func (r *Reg) List() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()

	sessions := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// resolveSessionCwd mirrors what the PTY layer will actually do, so the value
// the client is told matches the directory the shell really starts in, and
// renders it the way a terminal user expects to read it. Only this side knows
// the home directory, so the ~ abbreviation happens here rather than in the UI.
func resolveSessionCwd(cwd string) string {
	dir := cwd
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = home
	}
	return abbreviateHome(dir)
}

func abbreviateHome(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		return "~" + dir[len(home):]
	}
	return dir
}

func kindName(k Kind) string {
	switch k {
	case KindLocal:
		return "local"
	case KindRemote:
		return "ssh"
	default:
		return "unknown"
	}
}

// sshOptionsFromConfig converts a ssh.ConnectConfig into ConnectOptions.
func sshOptionsFromConfig(cfg *ssh.ConnectConfig) []ssh.ConnectOption {
	var opts []ssh.ConnectOption
	if cfg.User != "" {
		opts = append(opts, ssh.WithUser(cfg.User))
	}
	if cfg.Port > 0 {
		opts = append(opts, ssh.WithPort(cfg.Port))
	}
	if cfg.KeyFile != "" {
		opts = append(opts, ssh.WithKeyFile(cfg.KeyFile))
	}
	if cfg.UseAgent {
		opts = append(opts, ssh.WithAgent())
	}
	if cfg.Cols > 0 || cfg.Rows > 0 {
		opts = append(opts, ssh.WithPTYSize(cfg.Cols, cfg.Rows, cfg.XPixel, cfg.YPixel))
	}
	if cfg.AuthMode != "" {
		opts = append(opts, ssh.WithAuthMode(cfg.AuthMode))
	}
	if cfg.JumpHost != "" {
		opts = append(opts, ssh.WithJumpHost(cfg.JumpHost, cfg.JumpPort, cfg.JumpUser, cfg.JumpAuthMode))
	}
	if cfg.JumpSecrets != nil {
		opts = append(opts, ssh.WithJumpCredentials(cfg.JumpSecrets, cfg.JumpSecretID))
	}
	if cfg.JumpPassphraseSecretID != "" {
		opts = append(opts, ssh.WithJumpPassphraseSecretID(cfg.JumpPassphraseSecretID))
	}
	if cfg.Secrets != nil {
		opts = append(opts, ssh.WithCredentials(cfg.Secrets, cfg.SecretID))
	}
	// The vault-stored key (and its passphrase) ride the same store: without
	// them the dial sees publicKey auth with nothing to offer — the probe
	// works because it uses the resolved config directly, the session loses
	// the binding here. ADR-0017: a connection references a secret.
	if cfg.KeySecretID != "" {
		opts = append(opts, ssh.WithKeySecretID(cfg.KeySecretID))
	}
	if cfg.PassphraseSecretID != "" {
		opts = append(opts, ssh.WithPassphraseSecretID(cfg.PassphraseSecretID))
	}

	if cfg.AuthorizedEndpoint != "" {
		opts = append(opts, ssh.WithAuthorizedEndpoint(cfg.AuthorizedEndpoint))
	}
	if cfg.JumpAuthorizedEndpoint != "" {
		opts = append(opts, ssh.WithJumpAuthorizedEndpoint(cfg.JumpAuthorizedEndpoint))
	}
	if cfg.RemoteInstaller != nil {
		opts = append(opts, ssh.WithRemoteInstaller(cfg.RemoteInstaller))
	}
	return opts
}

// realSession is the concrete Session implementation.
//
// Deleted profile with open session: the session holds its own Channel
// (SSH connection or PTY) and does not reference the profile store at
type realSession struct {
	id           ID
	kind         Kind
	cwd          string
	profileID    string
	credentialID string

	ch        Channel
	log       log.Logger
	handler   OutputHandler
	handlerMu sync.Mutex
	closeOnce sync.Once
	// sandboxInfo is the immutable sandbox metadata for a sandboxed local
	// tab, nil otherwise (design spec §3.3).
	sandboxInfo *sandbox.SessionInfo
}

func (s *realSession) ID() ID               { return s.id }
func (s *realSession) Kind() Kind           { return s.kind }
func (s *realSession) Cwd() string          { return s.cwd }
func (s *realSession) ProfileID() string    { return s.profileID }
func (s *realSession) CredentialID() string { return s.credentialID }
func (s *realSession) SandboxInfo() *sandbox.SessionInfo {
	return s.sandboxInfo
}

func (s *realSession) Write(p []byte) (int, error) {
	return s.ch.Write(p)
}

func (s *realSession) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	return s.ch.Resize(ctx, cols, rows, xpixel, ypixel)
}

func (s *realSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.log.Debug("closing session")
		err = s.ch.Close()
	})
	return err
}

func (s *realSession) Done() <-chan struct{} {
	return s.ch.Done()
}

func (s *realSession) StartOutput(ctx context.Context, onOutput OutputHandler) error {
	s.handlerMu.Lock()
	if s.handler != nil {
		s.handlerMu.Unlock()
		return fmt.Errorf("output already started for session %s", s.id)
	}
	s.handler = onOutput
	s.handlerMu.Unlock()

	go s.readPump(ctx)
	return nil
}

func (s *realSession) readPump(ctx context.Context) {
	buf := make([]byte, 32768)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := s.ch.Read(buf)
		if err != nil {
			if err != io.EOF {
				s.log.Debug("pty read error", "error", err)
			}
			return
		}

		s.handlerMu.Lock()
		h := s.handler
		s.handlerMu.Unlock()

		if h == nil {
			return
		}

		if err := h(buf[:n]); err != nil {
			s.log.Debug("output handler error", "error", err)
			return
		}
	}
}
