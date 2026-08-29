package serverbin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/update/serverbin"
)

// target builds a [serverbin.Target] over a bundle directory holding a
// source binary, so each test states only what it is varying.
func target(goos, exePath, dataDir, version string) serverbin.Target {
	return serverbin.Target{GOOS: goos, ExePath: exePath, DataDir: dataDir, Version: version}
}

// ---------------------------------------------------------------------------
// The platform split
// ---------------------------------------------------------------------------

// On darwin the bundle is an ordinary directory that outlives the window,
// so the daemon is spawned where it ships. A copy here would be a second
// binary under the user's home for the updater to keep in step, and the
// day it fell behind nothing would say so.
func TestResolve_Darwin_SpawnsTheBinaryInsideTheBundle(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	src := writeSource(t, bundle, "the coordinator")

	got, err := newInstaller(serverbin.NewOSFS()).
		Resolve(context.Background(), target("darwin", filepath.Join(bundle, "nocx"), data, "0.2.0"))
	if err != nil {
		t.Fatalf("darwin resolve: %v", err)
	}
	if got != src {
		t.Errorf("darwin spawns %s, want the sibling %s", got, src)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, serverbin.DirName)); len(entries) != 0 {
		t.Errorf("darwin installed %d copies under the data directory; it must install none", len(entries))
	}
}

// On Linux the AppImage's FUSE mount dies with this process, so the path
// handed back must be OUTSIDE the bundle — under the profile's data
// directory, named for the version and the content hash.
func TestResolve_Linux_SpawnsAVersionedCopyOutsideTheBundle(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	sibling := writeSource(t, bundle, "the coordinator")

	got, err := newInstaller(serverbin.NewOSFS()).
		Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0"))
	if err != nil {
		t.Fatalf("linux resolve: %v", err)
	}
	if got == sibling {
		t.Fatal("linux resolved to the binary inside the bundle, which vanishes with this process")
	}
	binDir := filepath.Join(data, serverbin.DirName)
	if filepath.Dir(got) != binDir {
		t.Errorf("resolved %s, want a copy under %s", got, binDir)
	}
	if !strings.HasPrefix(filepath.Base(got), serverbin.BinaryName+"-0.2.0-") {
		t.Errorf("the copy %s is not named for its version", filepath.Base(got))
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("the resolved path does not exist: %v", err)
	}
}

// The second launch of an unchanged build returns the same path and
// writes nothing — the ordinary case, and the one that must not cost a
// copy of a 60 MB binary every time a window opens.
func TestResolve_Linux_SecondLaunchReturnsTheSamePath(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	writeSource(t, bundle, "the coordinator")
	tgt := target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0")
	inst := newInstaller(serverbin.NewOSFS())

	first, err := inst.Resolve(context.Background(), tgt)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := inst.Resolve(context.Background(), tgt)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if first != second {
		t.Errorf("two launches of one build resolved to %s and %s", first, second)
	}
}

// An update changed the binary inside the bundle: the next launch resolves
// to a NEW name and the superseded copy is gone. Both halves matter — a
// new name alone would accumulate a daemon per release.
func TestResolve_Linux_AnUpdateSupersedesAndPrunesTheOldCopy(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	writeSource(t, bundle, "coordinator 0.1.0")
	inst := newInstaller(serverbin.NewOSFS())
	binDir := filepath.Join(data, serverbin.DirName)

	old, err := inst.Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.1.0"))
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	writeSource(t, bundle, "coordinator 0.2.0")
	updated, err := inst.Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0"))
	if err != nil {
		t.Fatalf("resolve after the update: %v", err)
	}
	if updated == old {
		t.Fatal("the updated build resolved to the old copy's name")
	}
	if _, statErr := os.Stat(old); !os.IsNotExist(statErr) {
		t.Errorf("the superseded copy %s survived the prune (%v)", old, statErr)
	}
	if _, statErr := os.Stat(updated); statErr != nil {
		t.Errorf("the copy in use was pruned: %v", statErr)
	}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%s holds %d copies after an update, want 1", binDir, len(entries))
	}
}

// ---------------------------------------------------------------------------
// The failures, each paired with the success above
// ---------------------------------------------------------------------------

// An install that cannot be made is an error, never the sibling path.
// Degrading to the binary inside the image would produce a daemon that
// serves and then dies with the first window that closes — the product
// looking like it works right up until somebody's session is gone.
func TestResolve_Linux_AFailedInstallIsRefusedNotDegraded(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	sibling := writeSource(t, bundle, "the coordinator")

	inst := newInstaller(newFault("mkdirall", serverbin.DirName, errInjected))
	got, err := inst.Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0"))
	if !errors.Is(err, errInjected) {
		t.Fatalf("a failed install must be reported, got path %q err %v", got, err)
	}
	if got == sibling {
		t.Error("a failed install fell back to the binary inside the bundle")
	}
	if got != "" {
		t.Errorf("a failed install returned a path to spawn: %q", got)
	}
}

// The paired failure for the copy itself, not the directory: the source
// cannot be read.
func TestResolve_Linux_AnUnreadableSourceIsReported(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	writeSource(t, bundle, "the coordinator")

	inst := newInstaller(newFault("open", "nocx-server", errInjected))
	if _, err := inst.Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0")); !errors.Is(err, errInjected) {
		t.Fatalf("an unreadable source must be reported, got %v", err)
	}
}

// Pruning is housekeeping. A prune that fails leaves a superseded copy on
// disk and nothing else: the window still gets the path it needs, because
// refusing to start over a stale file would trade a footprint for an
// outage.
func TestResolve_Linux_AFailedPruneStillYieldsAPathToSpawn(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	writeSource(t, bundle, "the coordinator")

	inst := newInstaller(newFault("readdir", serverbin.DirName, errInjected))
	got, err := inst.Resolve(context.Background(), target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0"))
	if err != nil {
		t.Fatalf("a prune failure must not stop a launch: %v", err)
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Errorf("resolved %s, which is not there: %v", got, statErr)
	}
}

// A cancelled context stops the install rather than writing through it.
func TestResolve_Linux_HonoursACancelledContext(t *testing.T) {
	bundle := t.TempDir()
	data := t.TempDir()
	writeSource(t, bundle, "the coordinator")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newInstaller(serverbin.NewOSFS()).
		Resolve(ctx, target("linux", filepath.Join(bundle, "nocx"), data, "0.2.0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled resolve returned %v", err)
	}
}

// With no executable path to resolve against, the sibling is the bare
// name — so the exec failure names something a person can act on rather
// than an empty string. Asserted on darwin, where the sibling IS the
// answer.
func TestResolve_NoExecutablePathLeavesABareName(t *testing.T) {
	got, err := newInstaller(serverbin.NewOSFS()).
		Resolve(context.Background(), target("darwin", "", t.TempDir(), "0.2.0"))
	if err != nil {
		t.Fatalf("resolve with no exe path: %v", err)
	}
	if got != serverbin.BinaryName {
		t.Errorf("got %q, want the bare %q", got, serverbin.BinaryName)
	}
}
