package app

import (
	"context"
	"os"
	"runtime"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

// newAgentRuntimeLocator pins package bytes for the coordinator's lifetime,
// independently of the preference controlling new model operations. Installation
// failure disables only the experimental engine; it never prevents Eino startup.
func newAgentRuntimeLocator(ctx context.Context, executable, dataDir string) *runtimebundle.Locator {
	if _, explicit := os.LookupEnv(coordsock.AgentRuntimeRootEnv); explicit {
		locator := runtimebundle.LocatorForExecutable(executable)
		if err := locator.Pin(ctx); err != nil {
			log.From(ctx).Warn("experimental agent runtime handoff unavailable", "error", err)
		}
		return locator
	}

	target := runtime.GOOS + "/" + runtime.GOARCH
	installed, err := serverbin.PrepareRuntime(ctx, executable, dataDir, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		log.From(ctx).Warn("experimental agent runtime installation unavailable", "error", err)
		return runtimebundle.New("", "", target)
	}
	locator := runtimebundle.New(installed.Path, installed.Hash, target)
	if err := locator.Adopt(installed.Lease); err != nil {
		_ = installed.Close()
		log.From(ctx).Warn("experimental agent runtime lease unavailable", "error", err)
		return runtimebundle.New("", "", target)
	}
	return locator
}
