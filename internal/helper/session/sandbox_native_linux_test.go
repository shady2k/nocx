//go:build linux

package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/runner"
	"github.com/shady2k/nocx/internal/sandbox"
	"golang.org/x/sys/unix"
)

// Native acceptance must never skip a missing backend. This additional
// regression exercises the real candidate when this test build includes its
// runner and the host kernel supports the fixed policy ABI.
func TestNativeTerminalCreationFailureKillsHUPIgnoringCandidateBeforeRuntimeCleanup(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runnerPath, err := runner.Install(filepath.Join(root, "runner"))
	if errors.Is(err, runner.ErrNotBuilt) {
		t.Skip("runner artifact is not embedded in this test build")
	}
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pidFile := filepath.Join(work, "native-pid")
	// The PID marker is written only after HUP is ignored. Waiting for it in the
	// failing terminal factory makes the leaked-child regression deterministic.
	script := "trap '' HUP; printf '%s' \"$$\" > \"$PWD/native-pid\"; while :; do sleep 1; done"
	spawner := NewLocalSpawner(log, Shell{Path: "/bin/sh", Args: []string{"-c", script}}, "")
	pid, calls := 0, 0
	t.Cleanup(func() {
		if pid > 0 {
			if group, groupErr := syscall.Getpgid(pid); groupErr == nil && group == pid {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	service := New(Options{Generation: "native-cleanup-regression", Spawner: spawner, Log: log, Sandbox: SandboxEnvironment{HostHome: home, RunnerPath: runnerPath, RuntimeBase: filepath.Join(root, "runtime")}, Screen: func(emulator.Geometry) (emulator.Terminal, error) {
		calls++
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			data, readErr := os.ReadFile(pidFile) // #nosec G304 -- own temporary marker, written by the native regression fixture.
			if readErr == nil {
				parsedPID, parseErr := strconv.Atoi(string(data))
				if parseErr != nil {
					return nil, parseErr
				}
				pid = parsedPID
				return nil, errors.New("terminal allocation failed")
			}
			if !errors.Is(readErr, os.ErrNotExist) {
				return nil, readErr
			}
			time.Sleep(time.Millisecond)
		}
		return nil, errors.New("native shell did not establish HUP ignore")
	}})
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	call := func(op string, params any) (any, error) {
		encoded, encodeErr := json.Marshal(params)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return service.Call(ctx, op, encoded)
	}
	raw, err := call(proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "failed-terminal", LaunchID: "failed-launch", Mode: proto.SandboxEnforce, Workspace: "work", Cwd: work, Enforce: &proto.SandboxEnforceIntent{StandardRevision: 1, ProfileProvenance: sandbox.StandardRoot}})
	var unsupported *sandbox.BuildError
	if errors.As(err, &unsupported) && unsupported.Code == "backend_unsupported" {
		t.Skip("host does not support the mandatory Linux policy ABI")
	}
	if err != nil {
		t.Fatal(err)
	}
	prepared, ok := raw.(proto.SandboxPrepareResult)
	if !ok || prepared.Enforce == nil {
		t.Fatalf("prepare returned an invalid Enforce result: %T", raw)
	}
	params := proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: prepared.OperationID, LaunchID: prepared.LaunchID, Mode: proto.SandboxEnforce, Grant: &proto.SandboxGrantBinding{ID: 1, Digest: prepared.Enforce.Digest, Version: 1}, Shape: proto.SandboxLaunchShape{Cols: 80, Rows: 24}}
	if _, err := call(proto.OpSandboxLaunch, params); !errors.Is(err, ErrSpawn) {
		t.Fatalf("terminal creation failure = %v, want ErrSpawn", err)
	}
	if pid <= 0 {
		t.Fatal("terminal factory did not observe a started HUP-ignoring native shell")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("candidate still exists after refusal: %v", err)
	}
	if _, err := os.Stat(prepared.Enforce.Policy.Runtime.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime was not removed after confirmed death: %v", err)
	}
	if _, err := call(proto.OpSandboxLaunch, params); !errors.Is(err, ErrSpawn) {
		t.Fatalf("consumed failure replay = %v, want original terminal failure", err)
	}
	if calls != 1 {
		t.Fatalf("consumed failure created %d terminal candidates, want one", calls)
	}
	if spawner.NativeCleanupPending() {
		t.Fatal("confirmed candidate death retained unknown-cleanup launch guard")
	}
}

func TestNativeDiagnosticListenerFailureOnlyKillsItsOwnShell(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runnerPath, err := runner.Install(filepath.Join(root, "runner"))
	if errors.Is(err, runner.ErrNotBuilt) && os.Getenv("NOCX_SANDBOX_SMOKE_MANDATORY") != "1" {
		t.Skip("runner artifact is not embedded in this test build")
	}
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := sandbox.Prepare(sandbox.BuildRequest{
		WorkspaceID: "listener-failure", Cwd: work, HostHome: home, Shell: "/bin/sh", Runner: runnerPath,
		StandardRevision: 1, ProfileProvenance: sandbox.StandardRoot, RuntimeBase: filepath.Join(root, "runtime"),
	})
	var unsupported *sandbox.BuildError
	if errors.As(err, &unsupported) && unsupported.Code == "backend_unsupported" && os.Getenv("NOCX_SANDBOX_SMOKE_MANDATORY") != "1" {
		t.Skip("host does not support the mandatory Linux policy ABI")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Cleanup() }()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawner := NewLocalSpawner(logger, Shell{Path: "/bin/sh", Args: []string{"-c", "while :; do sleep 1; done"}}, "")
	ordinary, err := spawner.Spawn(SpawnRequest{SessionID: "ordinary-listener-neighbor", Cwd: work, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ordinary.Close() }()
	restricted, err := spawner.Spawn(SpawnRequest{SessionID: "native-listener-failure", Native: prepared, Cwd: work, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restricted.Close() }()
	native, ok := restricted.(*localProcess)
	if !ok {
		t.Fatalf("native spawner returned unexpected process type %T", restricted)
	}
	observer, ok := native.observer.(*linuxNativeObserver)
	if !ok || observer.file == nil {
		if os.Getenv("NOCX_SANDBOX_SMOKE_MANDATORY") != "1" {
			t.Skip("seccomp user notification observer is unavailable")
		}
		t.Fatal("mandatory native listener was not installed")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// Hold the os.File descriptor reference through the atomic replacement.
	// The observer may close it immediately after seeing the injected HUP.
	rawListener, err := observer.file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var replacementErr error
	if err := rawListener.Control(func(fd uintptr) {
		replacementErr = unix.Dup3(int(reader.Fd()), int(fd), unix.O_CLOEXEC)
	}); err != nil {
		t.Fatal(err)
	}
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	select {
	case <-restricted.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("installed listener death did not terminate its sandbox shell")
	}
	if err := syscall.Kill(restricted.Pid(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("affected sandbox shell still exists: %v", err)
	}
	select {
	case <-ordinary.Done():
		t.Fatal("listener failure terminated the unrelated ordinary shell")
	default:
	}
	if err := syscall.Kill(ordinary.Pid(), 0); err != nil {
		t.Fatalf("unrelated ordinary shell did not survive: %v", err)
	}
	t.Log("NATIVE_INSTALLED_LISTENER_FAILURE_AFFECTED_ONLY_PROOF_COMPLETE")
}
