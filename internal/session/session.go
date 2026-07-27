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

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
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
	Write(p []byte) (int, error)
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Close() error
	Done() <-chan struct{}
	StartOutput(ctx context.Context, onOutput OutputHandler) error
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
	log      log.Logger
	ptf      PTYFactory
	ssh      SSHFactory
	mu       sync.Mutex
	sessions map[ID]*realSession
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

func (r *Reg) Open(ctx context.Context, cfg Config) (Session, error) {
	var ch Channel
	var err error

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
		pt, perr := r.ptf.NewPTY(ctx, pty.Config{
			Cwd:      cfg.Cwd,
			Cols:     cfg.Cols,
			Rows:     cfg.Rows,
			XPixel:   cfg.XPixel,
			YPixel:   cfg.YPixel,
			Enhanced: cfg.Enhanced,
		})
		if perr != nil {
			return nil, fmt.Errorf("open session: %w", perr)
		}
		ch = pt
	}

	id := NewID()
	s := &realSession{
		id:   id,
		kind: cfg.Kind,
		cwd:  resolveSessionCwd(cfg.Cwd),
		ch:   ch,
		log:  r.log.With("session_id", string(id)),
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.log.Info("session opened", "id", string(id), "kind", kindName(cfg.Kind))
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
	return s.Close()
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
	if cfg.Secrets != nil {
		opts = append(opts, ssh.WithCredentials(cfg.Secrets, cfg.SecretID))
	}
	if cfg.RemoteInstaller != nil {
		opts = append(opts, ssh.WithRemoteInstaller(cfg.RemoteInstaller))
	}
	// ADR-0013: Pass BoundHost/BoundPort for grant enforcement
	if cfg.BoundHost != "" {
		opts = append(opts, ssh.WithBinding(cfg.BoundHost, cfg.BoundPort))
	}
	if cfg.JumpBoundHost != "" {
		opts = append(opts, ssh.WithJumpBinding(cfg.JumpBoundHost, cfg.JumpBoundPort))
	}
	return opts
}

type realSession struct {
	id        ID
	kind      Kind
	cwd       string
	ch        Channel
	log       log.Logger
	handler   OutputHandler
	handlerMu sync.Mutex
	closeOnce sync.Once
}

func (s *realSession) ID() ID      { return s.id }
func (s *realSession) Kind() Kind  { return s.kind }
func (s *realSession) Cwd() string { return s.cwd }

func (s *realSession) Write(p []byte) (int, error) {
	return s.ch.Write(p)
}

func (s *realSession) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	return s.ch.Resize(ctx, cols, rows, xpixel, ypixel)
}

func (s *realSession) Done() <-chan struct{} {
	return s.ch.Done()
}

func (s *realSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.log.Debug("closing session")
		err = s.ch.Close()
	})
	return err
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
