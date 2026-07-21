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
	mu       sync.Mutex
	sessions map[ID]*realSession
}

func New(logger log.Logger, ptf PTYFactory) *Reg {
	return &Reg{
		log:      logger,
		ptf:      ptf,
		sessions: make(map[ID]*realSession),
	}
}

func (r *Reg) Open(ctx context.Context, cfg Config) (Session, error) {
	if cfg.Kind == KindRemote {
		return nil, fmt.Errorf("remote (SSH) sessions are not implemented")
	}

	pt, err := r.ptf.NewPTY(ctx, pty.Config{
		Cwd:    cfg.Cwd,
		Cols:   cfg.Cols,
		Rows:   cfg.Rows,
		XPixel: cfg.XPixel,
		YPixel: cfg.YPixel,
	})
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}

	id := NewID()
	s := &realSession{
		id:   id,
		kind: cfg.Kind,
		cwd:  resolveSessionCwd(cfg.Cwd),
		pty:  pt,
		log:  r.log.With("session_id", string(id)),
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.log.Info("session opened", "id", string(id))
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

type realSession struct {
	id        ID
	kind      Kind
	cwd       string
	pty       pty.Pty
	log       log.Logger
	handler   OutputHandler
	handlerMu sync.Mutex
	closeOnce sync.Once
}

func (s *realSession) ID() ID      { return s.id }
func (s *realSession) Kind() Kind  { return s.kind }
func (s *realSession) Cwd() string { return s.cwd }

func (s *realSession) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

func (s *realSession) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	return s.pty.Resize(ctx, cols, rows, xpixel, ypixel)
}

func (s *realSession) Done() <-chan struct{} {
	return s.pty.Done()
}

func (s *realSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.log.Debug("closing session")
		err = s.pty.Close()
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

		n, err := s.pty.Read(buf)
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
