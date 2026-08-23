package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
)

// ErrNoForeground is returned by SignalForeground when there is no
// foreground execution to signal: the pty's foreground process group is the
// shell's own (it is sitting at a prompt), or the process is gone. The
// lease's escalation treats it as "nothing running to cancel" — never an
// error worth failing the terminalization for.
var ErrNoForeground = errors.New("pty: no foreground process to signal")

// ErrProtectedForeground is the SPECIFIC half of ErrNoForeground: the
// foreground process group is the launcher shell's own, so this seam refuses
// to signal it — the shell is not part of the job it is waiting on.
//
// It wraps ErrNoForeground rather than replacing it because "there is
// nothing here to cancel" remains true and is what the run lease still reads
// (internal/transport/run_lease.go). What it adds is the fact a caller
// deciding on a FALLBACK needs and could not previously ask for: a program
// may be running inside that protected group. Job control off — `set +m`,
// which ADR-0024 already names — puts a foreground command in the shell's
// own group, and so does `exec`; the kernel's answer is then identical to
// the one it gives at an idle prompt (nocx-7l4ex.10). Distinguishing the two
// is the lifecycle's job, never the byte stream's (AD-6).
//
// The two states it must NOT be confused with, and both are why the guard
// branch is the only place this may be returned: an ESRCH from kill(2) means
// the group is gone, and any other kill failure means the signal did not
// arrive and nothing may claim otherwise.
var ErrProtectedForeground = fmt.Errorf("%w: the foreground group is the launcher shell's own", ErrNoForeground)

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
	// SandboxStartFailed rolls back the recorded grant when the child cannot
	// establish enforcement. It is never called after readiness succeeds.
	SandboxStartFailed func() error
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
