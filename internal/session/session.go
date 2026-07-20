package session

import (
	"context"
	"fmt"
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
	Host   string
	Local  *pty.Config
	Remote *ssh.ConnectConfig
	Cols   uint16
	Rows   uint16
	XPixel uint16
	YPixel uint16
}

type Session interface {
	ID() ID
	Kind() Kind
	Write(p []byte) (int, error)
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Close() error
}

type Registry interface {
	Open(ctx context.Context, cfg Config) (Session, error)
	Get(id ID) (Session, error)
	Close(id ID) error
	List() []Session
}

type Stub struct {
	log      log.Logger
	mu       sync.Mutex
	sessions map[ID]*stubSession
	nextID   int
}

func NewStub(logger log.Logger, _ *pty.Stub, _ *ssh.Stub) *Stub {
	return &Stub{
		log:      logger,
		sessions: make(map[ID]*stubSession),
	}
}

func (r *Stub) Open(ctx context.Context, cfg Config) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := ID(fmt.Sprintf("ses-%d", r.nextID))
	s := &stubSession{
		id:   id,
		kind: cfg.Kind,
		log:  r.log.With("session_id", string(id)),
	}
	r.sessions[id] = s
	r.log.Info("session stub: opened", "id", string(id), "kind", cfg.Kind)
	return s, nil
}

func (r *Stub) Get(id ID) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return s, nil
}

func (r *Stub) Close(id ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	delete(r.sessions, id)
	r.log.Info("session stub: closed", "id", string(id))
	return s.Close()
}

func (r *Stub) List() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()

	sessions := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

type stubSession struct {
	id   ID
	kind Kind
	log  log.Logger
}

func (s *stubSession) ID() ID     { return s.id }
func (s *stubSession) Kind() Kind { return s.kind }
func (s *stubSession) Write(p []byte) (int, error) {
	s.log.Debug("session stub: Write", "len", len(p))
	return len(p), nil
}

func (s *stubSession) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	s.log.Debug("session stub: Resize", "cols", cols, "rows", rows)
	return nil
}

func (s *stubSession) Close() error {
	s.log.Debug("session stub: session closed")
	return nil
}
