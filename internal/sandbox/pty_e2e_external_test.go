//go:build linux || darwin

package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
)

// TestNewLocal_SandboxedEndToEnd exercises the real platform backend through
// the real pty.NewLocal path: the ordinary shell command is prepared by the
// Service, enforcement is applied before readiness, and the interactive shell
// runs inside the cage. On Linux the internal test binary also registers the
// helper argv dispatch; macOS launches sandbox-exec directly.
func TestNewLocal_SandboxedEndToEnd(t *testing.T) {
	if runtime.GOOS == "darwin" && raceInstrumented() {
		t.Skip("Seatbelt PTY smoke runs without TSan in the dedicated macOS gate")
	}
	cacheDir := t.TempDir()
	ws := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	canonicalWS := canonicalExisting(t, ws)

	svc := sandbox.New(log.NewSlogAdapter(nil), cacheDir)
	if st := svc.Status(context.Background()); !st.Available {
		t.Skipf("native sandbox enforcement is unavailable: %+v", st)
	}

	lp, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Cwd:     ws,
		Cols:    80,
		Rows:    24,
		Sandbox: &sandbox.Request{Workspace: ws},
	}, pty.WithSandboxService(svc))
	if err != nil {
		t.Fatalf("NewLocal sandboxed: %v", err)
	}
	defer func() { _ = lp.Close() }()

	info := lp.SandboxInfo()
	if info == nil {
		t.Fatal("SandboxInfo() = nil, want metadata")
	}
	wantBackend := sandbox.BackendLandlock
	if runtime.GOOS == "darwin" {
		wantBackend = sandbox.BackendSeatbelt
	}
	if info.Backend != wantBackend {
		t.Errorf("backend = %q, want %q", info.Backend, wantBackend)
	}
	if info.Workspace != canonicalWS {
		t.Errorf("workspace = %q, want %q", info.Workspace, canonicalWS)
	}

	// The selected workspace is also the shell's real process cwd, not only
	// policy/result metadata. The command echo contains "$PWD" literally, so
	// only the shell-expanded response can satisfy this assertion.
	if err := writeAndAwait(lp, "printf 'sandbox-cwd=%s\\n' \"$PWD\"\n", "sandbox-cwd="+canonicalWS); err != nil {
		t.Fatalf("sandbox cwd: %v", err)
	}

	// The shell runs inside the cage and answers on the PTY. The expected result
	// is computed by the shell, so terminal echo cannot satisfy this assertion.
	const shellReady = "34055"
	const shellReadyCommand = "printf '%s\\n' \"$((31337 + 2718))\"\n"
	if err := writeAndAwait(lp, shellReadyCommand, shellReady); err != nil {
		t.Fatalf("shell interaction: %v", err)
	}

	// /dev stays read-only except for the finite interactive-terminal allowlist.
	// This proves a real shell can redirect output and reopen its controlling
	// terminal without granting write access to arbitrary device hierarchies.
	const deviceReady = "sandbox-device-ok"
	const deviceReadyCommand = "printf ignored >/dev/null && exec 3</dev/tty && exec 3>&- && printf 'sandbox-device-ok\\n'\n"
	if err := writeAndAwait(lp, deviceReadyCommand, deviceReady); err != nil {
		t.Fatalf("sandbox device interaction: %v", err)
	}

	// Close tears down the process; the runtime tree is removed by the
	// waiter goroutine once the process exits. Wait for it (bounded).
	_ = lp.Close()
	select {
	case <-lp.Done():
	case <-time.After(10 * time.Second):
		t.Error("sandboxed session did not exit after Close")
	}
	entries, rErr := os.ReadDir(filepath.Join(cacheDir, "sandbox-sessions"))
	if rErr != nil {
		t.Fatalf("read sandbox-sessions: %v", rErr)
	}
	if len(entries) != 0 {
		t.Errorf("runtime trees not cleaned up after Close: %v", entries)
	}
}

// TestNewLocal_SandboxedFailClosed asserts that a sandbox request that cannot
// be validated never yields a PTY.
func TestNewLocal_SandboxedFailClosed(t *testing.T) {
	svc := sandbox.New(log.NewSlogAdapter(nil), t.TempDir())
	_, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Cwd:     t.TempDir(),
		Cols:    80,
		Rows:    24,
		Sandbox: &sandbox.Request{Workspace: "relative/not-absolute"},
	}, pty.WithSandboxService(svc))
	if !errors.Is(err, sandbox.ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions", err)
	}
}

// writeAndAwait writes to the PTY and reads until needle appears, proving the
// sandboxed shell is alive and interactive. On timeout the reader goroutine
// stays blocked until the caller's deferred Close unblocks it.
func writeAndAwait(lp pty.Pty, payload, needle string) error {
	done := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 4096)
		var out strings.Builder
		for {
			n, err := lp.Read(buf)
			if err != nil {
				return
			}
			out.Write(buf[:n])
			if strings.Contains(out.String(), needle) {
				done <- struct{}{}
				return
			}
		}
	}()
	if _, err := lp.Write([]byte(payload)); err != nil {
		return err
	}
	select {
	case <-time.After(15 * time.Second):
		return errors.New("timeout waiting for " + needle)
	case <-done:
		return nil
	}
}

func canonicalExisting(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize %q: %v", path, err)
	}
	return canonical
}

func raceInstrumented() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			return true
		}
	}
	return false
}
