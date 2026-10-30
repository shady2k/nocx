package coordinator_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/coordinator"
)

func TestSpawnPinsRuntimeAndReusedCoordinatorKeepsItsPin(t *testing.T) {
	for _, explicitEmpty := range []bool{false, true} {
		t.Run(map[bool]string{true: "explicit unavailable", false: "immutable tree"}[explicitEmpty], func(t *testing.T) {
			dir := shortDir(t)
			report := filepath.Join(t.TempDir(), "runtime-environment")
			root, digest := filepath.Join(t.TempDir(), "immutable-tree"), strings.Repeat("a", 64)
			if explicitEmpty {
				root, digest = "", ""
			}
			environ := append(os.Environ(), childMarkerEnv+"=1", childDirEnv+"="+dir, childReportEnv+"="+report,
				coordinator.AgentRuntimeRootEnv+"=/inherited/first", coordinator.AgentRuntimeRootEnv+"=/inherited/second", coordinator.AgentRuntimeDigestEnv+"=inherited-digest", coordinator.AgentRuntimeLeaseFDEnv+"=777")
			spawner := &trackingSpawner{t: t, inner: coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{Path: os.Args[0], AgentRuntimeRoot: root, AgentRuntimeDigest: digest, Environ: func() []string { return environ }})}
			launcher := newLauncher(t, coordinator.LauncherConfig{Dir: dir, Client: newRealClient(t, dir), Spawner: spawner, Stopper: &recordingStopper{}})
			if _, err := launcher.Launch(context.Background()); err != nil {
				t.Fatal(err)
			}
			seen, err := os.ReadFile(report) //nolint:gosec // report is a fixed filename in this test's private temporary directory
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{fmt.Sprintf("NOCX_AGENT_RUNTIME_ROOT=%q\n", root), fmt.Sprintf("NOCX_AGENT_RUNTIME_DIGEST=%q\n", digest), "NOCX_AGENT_RUNTIME_ROOT_PRESENT=true\n", "NOCX_AGENT_RUNTIME_DIGEST_PRESENT=true\n"} {
				if !strings.Contains(string(seen), want) {
					t.Fatalf("child did not receive explicit pin %q:\n%s", want, seen)
				}
			}
			if strings.Contains(string(seen), "inherited") {
				t.Fatalf("inherited runtime survived:\n%s", seen)
			}
			// A second desktop launcher may have another build installed. Reuse
			// must keep the already-running coordinator's immutable selection.
			second := &trackingSpawner{t: t, inner: coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{Path: os.Args[0], AgentRuntimeRoot: "/new/tree", AgentRuntimeDigest: strings.Repeat("b", 64), Environ: func() []string { return environ }})}
			reuse := newLauncher(t, coordinator.LauncherConfig{Dir: dir, Client: newRealClient(t, dir), Spawner: second, Stopper: &recordingStopper{}})
			if _, err = reuse.Launch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if second.spawns() != 0 || spawner.spawns() != 1 {
				t.Fatal("reuse replaced the runtime-owning process")
			}
			after, err := os.ReadFile(report) //nolint:gosec // reread the same test-owned report to detect unwanted process replacement
			if err != nil || !bytes.Equal(after, seen) {
				t.Fatalf("reuse mutated runtime handoff: %v", err)
			}
		})
	}
}

func TestSpawnedCoordinatorKeepsLeaseAfterLauncherCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parent := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("c", 64)
	root := filepath.Join(parent, digest)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := runtimebundle.AcquireShared(ctx, parent, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	dir := shortDir(t)
	environ := append(os.Environ(), childMarkerEnv+"=1", childDirEnv+"="+dir, coordinator.AgentRuntimeLeaseFDEnv+"=777")
	spawner := &trackingSpawner{t: t, inner: coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{
		Path: os.Args[0], AgentRuntimeRoot: root, AgentRuntimeDigest: digest, AgentRuntimeLease: lease.File(), Environ: func() []string { return environ },
	})}
	client := newRealClient(t, dir)
	launcher := newLauncher(t, coordinator.LauncherConfig{Dir: dir, Client: client, Spawner: spawner, Stopper: &recordingStopper{}})
	if _, err = launcher.Launch(ctx); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if exclusive, lockErr := runtimebundle.TryExclusive(parent, digest); !errors.Is(lockErr, runtimebundle.ErrLeaseBusy) {
		if exclusive != nil {
			_ = exclusive.Close()
		}
		t.Fatalf("coordinator lost the inherited shared lease: %v", lockErr)
	}
	sighting, err := client.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.NewSignalStopper(coordinator.SignalStopperConfig{Grace: time.Second}).Stop(ctx, sighting); err != nil {
		t.Fatal(err)
	}
	exclusive, err := runtimebundle.TryExclusive(parent, digest)
	if err != nil {
		t.Fatalf("dead coordinator retained lease: %v", err)
	}
	_ = exclusive.Close()
}
