package pty

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/shady2k/nocx/internal/log"
)

// ErrNoForeground is returned by SignalForeground when there is no
// foreground execution to signal: the pty's foreground process group is the
// shell's own (it is sitting at a prompt), or the process is gone. The
// lease's escalation treats it as "nothing running to cancel" — never an
// error worth failing the terminalization for.
var ErrNoForeground = errors.New("pty: no foreground process to signal")

type Pty interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Done() <-chan struct{}
}

type Config struct {
	Command string
	Args    []string
	Env     []string
	// Cwd is where the shell starts. Empty means inherit the process's
	// directory — which for a GUI launched from Finder is "/", so callers
	// that care should pass something.
	Cwd string
	// SessionID is the backend-assigned session id (AD-7) the shell will
	// run under. The pty factory uses it to bind a lifecycle lane to its
	// session (RegisterLifecycleLane), so published lifecycle facts route
	// to the right subscriber; empty on paths that predate the session id.
	SessionID string
	Cols      uint16
	Rows      uint16
	XPixel    uint16
	YPixel    uint16
	// Enhanced requests the marker-only prompt env (ADR-0006) for this session.
	Enhanced bool
	// ExtraFiles are inherited by the shell as fds 3, 4, … via
	// exec.Cmd.ExtraFiles. The lifecycle channel descriptor is the first
	// one, so it is fd 3 in the shell.
	ExtraFiles []*os.File
}

// Option configures a Config before PTY creation.
type Option func(*Config)

// WithExtraEnv appends extra environment variables to the PTY process.
func WithExtraEnv(env []string) Option {
	return func(cfg *Config) {
		cfg.Env = append(cfg.Env, env...)
	}
}

// WithExtraFiles appends open files the child shell inherits as fds 3, 4, …
// via exec.Cmd.ExtraFiles — the lifecycle channel descriptor is the first
// one, so it is fd 3 in the shell.
func WithExtraFiles(files ...*os.File) Option {
	return func(cfg *Config) {
		cfg.ExtraFiles = append(cfg.ExtraFiles, files...)
	}
}

type Stub struct {
	log  log.Logger
	done chan struct{}
}

func NewStub(logger log.Logger) *Stub {
	return &Stub{log: logger, done: make(chan struct{})}
}

func (s *Stub) Read(p []byte) (int, error) {
	s.log.Debug("pty stub: Read called (no-op)")
	return 0, io.EOF
}

func (s *Stub) Write(p []byte) (int, error) {
	s.log.Debug("pty stub: Write called", "len", len(p))
	return len(p), nil
}

func (s *Stub) Close() error {
	s.log.Debug("pty stub: Close called")
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func (s *Stub) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	s.log.Debug("pty stub: Resize called", "cols", cols, "rows", rows, "xpixel", xpixel, "ypixel", ypixel)
	return nil
}

func (s *Stub) Done() <-chan struct{} {
	return s.done
}
