package coordinator_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
)

// Acceptance: the coordinator does not inherit the working directory of the
// window that raised it.
//
// The daemon exists to outlive that window, so anything the window's cwd is
// attached to may be gone while the daemon is still serving. Under an
// AppImage it is exactly that: AppRun chdirs into the FUSE mount before
// exec, and the mount is unmounted when the window exits. CI run 33320751321
// caught the consequence — a session shell reported '/work/squashfs-root'
// rather than $HOME.
//
// These tests assert on what the kernel gave the child, not on a field: the
// question is where the process actually stands.

// writeCwdReportingServer puts a fake nocx-server in dir. It waits for the
// trigger file to appear, then resolves its own working directory into
// report — which a process whose directory has been unlinked cannot do.
// With relativeWrite it also creates a file by relative name first, the
// other thing that needs a directory that is still there; that is asked for
// only where the chosen directory is one the test can write to.
//
// It waits on a state change rather than a duration: the test controls when
// the trigger appears, and the bound is only so a broken run ends.
func writeCwdReportingServer(t *testing.T, dir, trigger, report string, relativeWrite bool) string {
	t.Helper()
	p := filepath.Join(dir, "nocx-server")
	script := "#!/bin/sh\n" +
		"i=0\n" +
		"while [ ! -e " + trigger + " ] && [ \"$i\" -lt 600 ]; do sleep 0.05; i=$((i+1)); done\n"
	if relativeWrite {
		script += "echo ok > child-artifact || exit 3\n"
	}
	script += "pwd -P > " + report + " || exit 4\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	return p
}

// spawnAndWait runs the fake server through the real spawner with the given
// HOME and returns what the child reported, having released it by creating
// the trigger only after release() has run.
func spawnAndWait(t *testing.T, home string, relativeWrite bool, release func()) (string, error) {
	t.Helper()
	ctx := context.Background()
	bin := t.TempDir()
	work := t.TempDir()
	trigger := filepath.Join(work, "go")
	report := filepath.Join(work, "cwd")
	path := writeCwdReportingServer(t, bin, trigger, report, relativeWrite)

	spawned, err := coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{
		Path: path,
		Environ: func() []string {
			return []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		},
	}).Spawn(ctx)
	if err != nil {
		t.Fatalf("spawn %s: %v", path, err)
	}

	release()

	if err := os.WriteFile(trigger, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var waitErr error
	select {
	case waitErr = <-spawned.Exit:
	case <-time.After(60 * time.Second):
		t.Fatal("the spawned process never exited")
	}
	reported, readErr := os.ReadFile(report) //nolint:gosec // test-controlled path
	if readErr != nil {
		return "", waitErr
	}
	return string(reported[:len(reported)-1]), waitErr // strip pwd's newline
}

// sameDir compares two paths after resolving symlinks, because on macOS
// t.TempDir() hands back /var/folders/… while a process that chdir'd there
// reports /private/var/folders/… — the same directory through the /var
// symlink. A string comparison is green on Linux and red on the runner
// this project ships from (CI run 33321753715).
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

func TestSpawnDoesNotInheritTheLaunchersWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is a POSIX shell script")
	}
	launchDir := t.TempDir() // stands in for the AppImage's mounted AppDir
	home := t.TempDir()
	t.Chdir(launchDir)

	reported, waitErr := spawnAndWait(t, home, false, func() {})
	if waitErr != nil {
		t.Fatalf("the spawned process failed: %v", waitErr)
	}
	if sameDir(t, reported, launchDir) {
		t.Errorf("the daemon stands in the launcher's directory %q; that directory belongs to "+
			"the window, and the daemon outlives the window", launchDir)
	}
	if !sameDir(t, reported, home) {
		t.Errorf("the daemon reported cwd %q, want the chosen directory %q", reported, home)
	}
}

func TestSpawnSurvivesItsLaunchDirectoryBeingRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is a POSIX shell script")
	}
	// The AppImage case in miniature: the directory the launcher stood in
	// is gone while the daemon is still working. A vanished cwd is exactly
	// what an unmounted AppDir is.
	launchDir := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(launchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Chdir(launchDir)

	reported, waitErr := spawnAndWait(t, home, true, func() {
		t.Chdir(t.TempDir()) // this process must not hold the mount open either
		if err := os.RemoveAll(launchDir); err != nil {
			t.Fatal(err)
		}
	})
	if waitErr != nil {
		t.Fatalf("the daemon failed after its launch directory was removed: %v", waitErr)
	}
	if !sameDir(t, reported, home) {
		t.Errorf("the daemon reported cwd %q, want %q", reported, home)
	}
	if _, err := os.Stat(filepath.Join(home, "child-artifact")); err != nil {
		t.Errorf("the daemon could not create a file by relative name: %v", err)
	}
}

func TestSpawnFallsBackWhenTheChosenDirectoryIsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is a POSIX shell script")
	}
	// A spawn must not start failing on a machine where it works today:
	// an unresolvable home is a fallback, never a refusal.
	absent := filepath.Join(t.TempDir(), "no-such-home")
	t.Chdir(t.TempDir())

	reported, waitErr := spawnAndWait(t, absent, false, func() {})
	if waitErr != nil {
		t.Fatalf("the spawn failed on an absent home: %v", waitErr)
	}
	if reported != "/" {
		t.Errorf("with %q absent the daemon reported cwd %q, want the fallback %q", absent, reported, "/")
	}
}

func TestSpawnDirectory(t *testing.T) {
	home := t.TempDir()
	absent := filepath.Join(t.TempDir(), "gone")
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		environ []string
		want    string
	}{
		{"the home the daemon will resolve", []string{"PATH=/bin", "HOME=" + home}, home},
		{"the last HOME wins, as it does for the daemon", []string{"HOME=" + absent, "HOME=" + home}, home},
		{"no HOME at all", []string{"PATH=/bin"}, "/"},
		{"an empty HOME", []string{"HOME="}, "/"},
		{"a HOME that does not exist", []string{"HOME=" + absent}, "/"},
		{"a HOME that is not a directory", []string{"HOME=" + file}, "/"},
	} {
		if got := coordinator.SpawnDirectory(tc.environ); got != tc.want {
			t.Errorf("%s: SpawnDirectory = %q, want %q", tc.name, got, tc.want)
		}
	}
}
