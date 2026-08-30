package coordinator_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
)

type launchFailureDiscoverer struct {
	err error
}

func (d launchFailureDiscoverer) Hello(context.Context) (coordinator.Sighting, error) {
	return coordinator.Sighting{}, d.err
}

type launchFailureSpawner struct {
	err error
}

func (s launchFailureSpawner) Spawn(context.Context) (coordinator.Spawned, error) {
	return coordinator.Spawned{}, s.err
}

type launchFailureStopper struct{}

func (launchFailureStopper) Stop(context.Context, coordinator.Sighting) error { return nil }

type launchFailureAnnouncer struct{}

func (launchFailureAnnouncer) Announce(coordinator.Notice) {}

func newLaunchFailureTestLauncher(t *testing.T, dir string, discoverer coordinator.Discoverer, spawner coordinator.Spawner) *coordinator.Launcher {
	t.Helper()
	launcher, err := coordinator.NewLauncher(coordinator.LauncherConfig{
		Dir:          dir,
		Client:       discoverer,
		Spawner:      spawner,
		Stopper:      launchFailureStopper{},
		Announcer:    launchFailureAnnouncer{},
		ReadyTimeout: 20 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Self: coordinator.ClientIdentity{
			Version:  "test",
			Commit:   "test",
			Protocol: coordinator.ProtocolVersion,
		},
	})
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	return launcher
}

func requireLaunchFailure(t *testing.T, err error, want coordinator.FailureKind) *coordinator.LaunchFailure {
	t.Helper()
	if err == nil {
		t.Fatal("Launch succeeded, want classified failure")
	}
	failure, ok := coordinator.AsLaunchFailure(err)
	if !ok {
		t.Fatalf("AsLaunchFailure(%v) = false", err)
	}
	if failure.Kind != want {
		t.Fatalf("failure kind = %q, want %q", failure.Kind, want)
	}
	if failure.Message == "" || failure.Remedy == "" {
		t.Fatalf("failure lacks person-readable fields: %+v", failure)
	}
	return failure
}

func TestLaunchFailureKindsSurviveWrapping(t *testing.T) {
	cases := []struct {
		name string
		kind coordinator.FailureKind
	}{
		{name: "profile", kind: coordinator.FailureProfileUnusable},
		{name: "server binary", kind: coordinator.FailureServerBinaryUnusable},
		{name: "incompatible", kind: coordinator.FailureIncompatible},
		{name: "not ready", kind: coordinator.FailureNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("underlying cause")
			failure := coordinator.NewLaunchFailure(tc.kind, "The launch could not continue.", "Fix the problem and retry.", cause)
			if failure.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", failure.Kind, tc.kind)
			}
			if !strings.HasPrefix(failure.Error(), failure.Message) {
				t.Fatalf("Error() = %q, want prefix Message %q", failure.Error(), failure.Message)
			}
			if failure.Unwrap() != cause {
				t.Fatalf("Unwrap() = %v, want %v", failure.Unwrap(), cause)
			}
			if strings.Contains(failure.Message, ": underlying cause") || strings.Contains(failure.Remedy, ": underlying cause") {
				t.Fatalf("person-readable text contains a wrapped error chain: %+v", failure)
			}

			wrapped := fmt.Errorf("launch attempt failed: %w", failure)
			got, ok := coordinator.AsLaunchFailure(wrapped)
			if !ok || got != failure {
				t.Fatalf("AsLaunchFailure(%v) = (%p, %t), want (%p, true)", wrapped, got, ok, failure)
			}
		})
	}
}

func TestLaunchClassifiesAnIncompatibleDiscoveryAnswer(t *testing.T) {
	dir := t.TempDir()
	socket := coordinator.SocketPathIn(dir)
	raw := errors.New("foreign coordinator answered")
	launcher := newLaunchFailureTestLauncher(t, dir, launchFailureDiscoverer{err: raw}, launchFailureSpawner{err: errors.New("must not spawn")})

	failure := requireLaunchFailure(t, func() error {
		_, err := launcher.Launch(context.Background())
		return err
	}(), coordinator.FailureIncompatible)
	if !strings.Contains(failure.Message, socket) {
		t.Fatalf("message %q does not name socket %q", failure.Message, socket)
	}
	if failure.Cause != raw {
		t.Fatalf("cause = %v, want %v", failure.Cause, raw)
	}
}

func TestLaunchClassifiesAReadinessFailureAndNamesTheDaemonLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(filepath.Dir(dir), "nocx.log")
	launcher := newLaunchFailureTestLauncher(t, dir, launchFailureDiscoverer{err: coordinator.ErrNoCoordinator}, launchFailureSpawner{})

	failure := requireLaunchFailure(t, func() error {
		_, err := launcher.Launch(context.Background())
		return err
	}(), coordinator.FailureNotReady)
	if !strings.Contains(failure.Remedy, logPath) {
		t.Fatalf("remedy %q does not name daemon log %q", failure.Remedy, logPath)
	}
}

func TestLaunchClassifiesAServerBinaryFailure(t *testing.T) {
	dir := t.TempDir()
	raw := errors.New("exec: permission denied")
	launcher := newLaunchFailureTestLauncher(t, dir, launchFailureDiscoverer{err: coordinator.ErrNoCoordinator}, launchFailureSpawner{err: raw})

	failure := requireLaunchFailure(t, func() error {
		_, err := launcher.Launch(context.Background())
		return err
	}(), coordinator.FailureServerBinaryUnusable)
	if failure.Cause != raw {
		t.Fatalf("cause = %v, want %v", failure.Cause, raw)
	}
	remedy := strings.ToLower(failure.Remedy)
	if runtime.GOOS == "darwin" {
		if !strings.Contains(remedy, "reinstall") {
			t.Fatalf("darwin remedy %q does not say reinstall", failure.Remedy)
		}
	} else if runtime.GOOS == "linux" && !strings.Contains(failure.Remedy, "~/.local/share/nocx/bin") {
		t.Fatalf("linux remedy %q does not name the server binary directory", failure.Remedy)
	}
}

func TestLaunchClassifiesAnUnusableRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dir := filepath.Join(blocked, "run")
	launcher := newLaunchFailureTestLauncher(t, dir, launchFailureDiscoverer{err: coordinator.ErrNoCoordinator}, launchFailureSpawner{err: errors.New("must not spawn")})

	failure := requireLaunchFailure(t, func() error {
		_, err := launcher.Launch(context.Background())
		return err
	}(), coordinator.FailureProfileUnusable)
	if !strings.Contains(failure.Message, dir) {
		t.Fatalf("message %q does not name runtime directory %q", failure.Message, dir)
	}
}
