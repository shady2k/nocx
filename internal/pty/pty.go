package pty

import (
	"context"
	"io"
	"os"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
)

type Pty interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Done() <-chan struct{}
}

// SandboxInfoProvider is implemented by local PTYs that were prepared as
// sandboxed sessions. The session layer reads the immutable metadata from it
// so the open result can carry {backend, workspace, writableRoots,
// readOnlyRoots} (design spec §8). Ordinary PTYs do not implement it.
type SandboxInfoProvider interface {
	SandboxInfo() *sandbox.SessionInfo
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
	// Sandbox is the wire opt-in: when non-nil, the shell is prepared by the
	// injected sandbox.Service and fails closed if enforcement cannot be
	// established. Ordinary sessions leave it nil and never touch the
	// sandbox path.
	Sandbox *sandbox.Request
	// SandboxPrepared runs after policy realization and before process start.
	// It is used to durably record the grant that caused enforcement.
	SandboxPrepared func(*sandbox.PreparedCommand) error
	// sandboxService is injected by the composition root via
	// WithSandboxService; it is never part of the wire contract.
	sandboxService sandbox.Service
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

// WithSandboxService injects the sandbox backend. It is applied by the
// composition-root PTY factory (internal/app/app.go) — the sandbox package
// is never a global — and is required exactly when Config.Sandbox is set.
func WithSandboxService(svc sandbox.Service) Option {
	return func(cfg *Config) {
		cfg.sandboxService = svc
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
