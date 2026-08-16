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
	if info.Workspace != ws {
		t.Errorf("workspace = %q, want %q", info.Workspace, ws)
	}

	// The selected workspace is also the shell's real process cwd, not only
	// policy/result metadata. The command echo contains "$PWD" literally, so
	// only the shell-expanded response can satisfy this assertion.
	if err := writeAndAwait(lp, "printf 'sandbox-cwd=%s\\n' \"$PWD\"\n", "sandbox-cwd="+ws); err != nil {
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

	// Close tears down the process and the per-session runtime tree once.
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

// TestNewLocal_TrustedAgentEndToEnd proves the fixed-agent policy seam itself,
// not only an interactive shell: a backend-owned executable outside the
// workspace starts in the canonical workspace, can write there, and cannot
// write beside its own read-only executable.
func TestNewLocal_TrustedAgentEndToEnd(t *testing.T) {
	if runtime.GOOS == "darwin" && raceInstrumented() {
		t.Skip("Seatbelt agent smoke runs without TSan in the dedicated macOS gate")
	}
	cacheDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	agentDir := t.TempDir()
	agent := filepath.Join(agentDir, "opencode")
	outside := filepath.Join(agentDir, "outside")
	script := `#!/bin/sh
printf 'agent-cwd=%s\n' "$PWD"
printf allowed >"$PWD/allowed"
if printf denied >"$NOCX_AGENT_OUTSIDE"; then
  printf 'outside-writable\n'
else
  printf 'outside-denied\n'
fi
`
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatalf("write agent fixture: %v", err)
	}

	svc := sandbox.New(log.NewSlogAdapter(nil), cacheDir)
	if st := svc.Status(context.Background()); !st.Available {
		t.Skipf("native sandbox enforcement is unavailable: %+v", st)
	}
	lp, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Command: agent,
		Cwd:     workspace,
		Env:     []string{"NOCX_AGENT_OUTSIDE=" + outside},
		Cols:    80,
		Rows:    24,
		Sandbox: &sandbox.Request{Workspace: workspace},
	}, pty.WithSandboxService(svc), pty.WithTrustedSandboxExecutable(agent))
	if err != nil {
		t.Fatalf("NewLocal trusted agent: %v", err)
	}
	defer func() { _ = lp.Close() }()

	out, err := readUntilDone(lp, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agent-cwd="+workspace) {
		t.Errorf("agent cwd output = %q, want canonical workspace", out)
	}
	if !strings.Contains(out, "outside-denied") || strings.Contains(out, "outside-writable") {
		t.Errorf("agent escaped writable policy: %q", out)
	}
	if _, err := os.Stat(filepath.Join(workspace, "allowed")); err != nil {
		t.Errorf("agent could not write workspace: %v", err)
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("outside file exists or stat failed unexpectedly: %v", err)
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

func readUntilDone(lp pty.Pty, timeout time.Duration) (string, error) {
	output := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var out strings.Builder
		for {
			n, err := lp.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				output <- out.String()
				return
			}
		}
	}()
	select {
	case out := <-output:
		return out, nil
	case <-time.After(timeout):
		return "", errors.New("timeout waiting for agent exit")
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
