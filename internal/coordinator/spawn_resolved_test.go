package coordinator_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

// Acceptance: the launcher spawns the coordinator from the path
// [serverbin.Installer.Resolve] handed back — asserted on what the kernel
// actually executed, not on a file being where somebody expected it.
//
// The distinction is the whole reason the versioned scheme exists. Every
// arrangement this replaces also leaves a file called nocx-server
// somewhere plausible; what separates them is which inode the process
// comes out of, and only running it can say.

// writeReportingServer puts a fake nocx-server in dir that records the
// path it was invoked as and exits. $0 is the path exec was given, which
// is exactly the fact under test.
func writeReportingServer(t *testing.T, dir, report string) string {
	t.Helper()
	p := filepath.Join(dir, serverbin.BinaryName)
	script := "#!/bin/sh\nprintf '%s' \"$0\" > " + report + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	return p
}

func TestLauncherSpawnsTheBinaryServerbinResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is a POSIX shell script")
	}
	ctx := context.Background()

	bundle := t.TempDir() // stands in for the AppImage's usr/bin
	data := t.TempDir()   // stands in for ~/.local/share/nocx
	report := filepath.Join(t.TempDir(), "argv0")
	sibling := writeReportingServer(t, bundle, report)

	// What the composition root does: resolve, then spawn what it got.
	// GOOS is passed rather than read, so the Linux answer is exercised
	// on the darwin runner too — the mount that vanishes is Linux's, and
	// a check that only runs on Linux is a check the macOS job cannot
	// tell has broken.
	resolved, err := serverbin.New(serverbin.NewOSFS(), nil).Resolve(ctx, serverbin.Target{
		GOOS:    "linux",
		ExePath: filepath.Join(bundle, "nocx"),
		DataDir: data,
		Version: "0.2.0",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	spawned, err := coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{Path: resolved}).Spawn(ctx)
	if err != nil {
		t.Fatalf("spawn %s: %v", resolved, err)
	}
	select {
	case <-spawned.Exit:
	case <-time.After(10 * time.Second):
		t.Fatal("the spawned process never exited")
	}

	executed, err := os.ReadFile(report) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("the spawned process recorded nothing: %v", err)
	}
	if string(executed) != resolved {
		t.Errorf("the launcher executed %q, but serverbin resolved %q", executed, resolved)
	}
	if string(executed) == sibling {
		t.Error("the launcher executed the binary inside the bundle, whose mount does not outlive this process")
	}
	if !strings.HasPrefix(filepath.Base(string(executed)), serverbin.BinaryName+"-0.2.0-") {
		t.Errorf("the process came out of %q, which is not a versioned copy", executed)
	}
	if spawned.Command != resolved {
		t.Errorf("the spawn handle names %q, want %q — a failure report that names the wrong file "+
			"sends the reader to a binary that was never run", spawned.Command, resolved)
	}
}
