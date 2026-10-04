package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestMeasureDaemonLaunchToFirstSpawn is an opt-in calibration harness. Run
// with RUN_DAEMON_LIFECYCLE_MEASUREMENT=1; it builds and starts the real helper
// repeatedly under isolated HOME directories, performs its authenticated hello,
// then measures until a real shell session is acknowledged. It emits each
// sample plus nearest-rank p99. This is intentionally not a normal unit test.
func TestMeasureDaemonLaunchToFirstSpawn(t *testing.T) {
	if os.Getenv("RUN_DAEMON_LIFECYCLE_MEASUREMENT") != "1" {
		t.Skip("set RUN_DAEMON_LIFECYCLE_MEASUREMENT=1 to calibrate D2 startup grace")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "nocx-helper")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nocx-helper") //nolint:gosec // Build the repository helper for this opt-in calibration.
	build.Dir = root
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build helper: %v\n%s", buildErr, out)
	}
	data, readErr := os.ReadFile(binary) //nolint:gosec // binary is the helper just built into t.TempDir.
	if readErr != nil {
		t.Fatal(readErr)
	}
	generation := fmt.Sprintf("%x", sha256.Sum256(data))
	const samples = 30
	durations := make([]time.Duration, 0, samples)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for i := 0; i < samples; i++ {
		home := t.TempDir()
		cmd := exec.Command(binary, endpoint.ServeCommand) //nolint:gosec // binary is the helper just built above.
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		started := time.Now()
		if startErr := cmd.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var c *client.Client
		for {
			c, err = helperlocal.Open(ctx, helperlocal.Config{Dir: endpoint.Dir(home), Generation: proto.GenerationID(generation), Log: logger})
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				cancel()
				_ = cmd.Process.Signal(syscall.SIGTERM)
				_ = cmd.Wait()
				t.Fatalf("sample %d hello: %v", i, err)
			}
			time.Sleep(5 * time.Millisecond)
		}
		var result proto.SpawnResult
		err = c.Call(ctx, proto.ServiceSession, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &result)
		elapsed := time.Since(started)
		_ = c.Close()
		cancel()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		if err != nil {
			t.Fatalf("sample %d spawn: %v", i, err)
		}
		durations = append(durations, elapsed)
		t.Logf("sample %02d: %s", i+1, elapsed)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(99*len(durations)+99)/100-1]
	t.Logf("launch-to-first-spawn p99 (nearest rank, n=%d): %s; min=%s max=%s", len(durations), p99, durations[0], durations[len(durations)-1])
}
