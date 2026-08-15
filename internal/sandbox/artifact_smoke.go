//go:build !windows

package sandbox

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ArtifactSmokeArg selects the release-artifact enforcement probe. It is an
// internal packaging contract, not a user-facing command: cmd/sandboxprobe
// invokes the built nocx executable with this marker and supplies the probe
// executable as argv[2].
const ArtifactSmokeArg = "__sandbox-artifact-smoke"

// ArtifactSmokeCacheEnv carries the disposable cache root created by the
// external artifact probe.
const ArtifactSmokeCacheEnv = "NOCX_SANDBOX_SMOKE_CACHE"

// MaybeArtifactSmoke runs the release-artifact enforcement probe when this
// process was launched by cmd/sandboxprobe. Like MaybeHelper, it must run
// before Wails or the backend starts. A true return is unreachable in normal
// execution because the probe exits the process; the bool keeps main's startup
// dispatch explicit and testable.
func MaybeArtifactSmoke() bool {
	if len(os.Args) != 3 || os.Args[1] != ArtifactSmokeArg {
		return false
	}
	os.Exit(runArtifactSmoke(os.Args[2]))
	return true
}

func runArtifactSmoke(probePath string) int {
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	workspace := os.Getenv("NOCX_SB_WORKSPACE")
	cacheDir := os.Getenv(ArtifactSmokeCacheEnv)
	if workspace == "" || cacheDir == "" || !filepath.IsAbs(probePath) {
		logger.Error("invalid sandbox artifact smoke invocation")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	svc := New(logger, cacheDir)
	status := svc.Status(ctx)
	if !status.Available {
		logger.Error("sandbox backend unavailable for artifact smoke",
			"backend", status.Backend,
			"reason", status.Reason,
			"abi", status.ABI,
		)
		return 1
	}

	prepared, err := svc.Prepare(ctx, Request{Workspace: workspace}, CommandSpec{
		Path: probePath,
		Args: []string{"--child"},
		Dir:  workspace,
		Env:  os.Environ(),
	})
	if err != nil {
		logger.Error("sandbox artifact smoke prepare failed", "error", err)
		return 1
	}
	defer prepared.Close()

	prepared.Cmd.Stdout = os.Stdout
	prepared.Cmd.Stderr = os.Stderr
	if err := prepared.Cmd.Start(); err != nil {
		logger.Error("sandbox artifact smoke start failed", "error", err)
		return 1
	}
	if err := prepared.WaitReady(ctx); err != nil {
		logger.Error("sandbox artifact smoke readiness failed", "error", err)
		return 1
	}
	if err := prepared.Cmd.Wait(); err != nil {
		logger.Error("sandbox artifact smoke probe failed", "error", err)
		return 1
	}
	return 0
}
