package serverbin_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/update/serverbin"
)

// The payload is a real executable rather than a fixture file, because the
// claim under test is about what RUNS, not about what exists. A path check
// is exactly the check that cannot tell the versioned scheme from the
// stable-name one it replaces: both leave a file where the launcher looks.
func writeExecutableReporting(t *testing.T, dir, version string) string {
	t.Helper()
	p := filepath.Join(dir, "nocx-server")
	script := "#!/bin/sh\necho \"nocx-server " + version + "\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	return p
}

func reportedVersion(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path).CombinedOutput() //nolint:gosec // path is produced by the code under test
	if err != nil {
		t.Fatalf("spawning %s failed: %v (%s)", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSpawnedDaemonIsTheNewVersionAfterAnUpdate is acceptance 3: after an
// update the daemon spawned from the installed path REPORTS the new
// version. It is driven by executing the binary the installer hands back,
// so the assertion is on running code and not on a file existing.
//
// The stable mutable name this replaces would pass every check but this
// one: the file is there, its mtime is recent, and the process coming out
// of it is last month's.
func TestSpawnedDaemonIsTheNewVersionAfterAnUpdate(t *testing.T) {
	ctx := context.Background()
	binDir := filepath.Join(t.TempDir(), "bin")
	inst := serverbin.New(serverbin.NewOSFS(), nil)

	// Before the update: the image carries 0.1.0.
	before, err := inst.Ensure(ctx, writeExecutableReporting(t, t.TempDir(), "0.1.0"), binDir, "0.1.0")
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if got := reportedVersion(t, before.Path); got != "nocx-server 0.1.0" {
		t.Fatalf("before the update the daemon reports %q", got)
	}

	// The update replaced the application, so the binary inside it is now
	// 0.2.0. This is the launcher's next start.
	after, err := inst.Ensure(ctx, writeExecutableReporting(t, t.TempDir(), "0.2.0"), binDir, "0.2.0")
	if err != nil {
		t.Fatalf("install after the update: %v", err)
	}

	if got := reportedVersion(t, after.Path); got != "nocx-server 0.2.0" {
		t.Fatalf("after the update the spawned daemon reports %q, want %q", got, "nocx-server 0.2.0")
	}
	if after.Path == before.Path {
		t.Error("the new version was installed over the old path — a daemon already running from it would keep its old code, and a launcher holding that path would spawn it")
	}

	// The old copy is still there and still runnable: a daemon spawned
	// before the update is executing from it, and on Linux it has no
	// other path to its own executable.
	if got := reportedVersion(t, before.Path); got != "nocx-server 0.1.0" {
		t.Errorf("the superseded copy was altered: %q", got)
	}
}

// Pruning happens once the new copy is the one in use, and the copy that
// survives must still be spawnable — a prune that leaves an unusable
// directory has taken the daemon with it.
func TestPrunedDirectoryStillSpawnsTheCurrentVersion(t *testing.T) {
	ctx := context.Background()
	binDir := filepath.Join(t.TempDir(), "bin")
	inst := serverbin.New(serverbin.NewOSFS(), nil)

	old, err := inst.Ensure(ctx, writeExecutableReporting(t, t.TempDir(), "0.1.0"), binDir, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	current, err := inst.Ensure(ctx, writeExecutableReporting(t, t.TempDir(), "0.2.0"), binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Prune(ctx, binDir, current.Name); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if got := reportedVersion(t, current.Path); got != "nocx-server 0.2.0" {
		t.Errorf("after pruning the current copy reports %q", got)
	}
	if _, err := os.Stat(old.Path); !os.IsNotExist(err) {
		t.Errorf("the superseded copy survived pruning")
	}
}
