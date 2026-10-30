package main

import (
	"context"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

// prepareAgentRuntime does not inspect the feature flag and never starts Python.
// A present empty handoff is how a failed optional installation stays failed
// when selected later, rather than falling back to a user's interpreter.
func prepareAgentRuntime(ctx context.Context, executable, dataDir, goos, goarch string) serverbin.TreeInstall {
	install, err := serverbin.PrepareRuntime(ctx, executable, dataDir, goos, goarch)
	if err != nil {
		log.From(ctx).Warn("experimental assistant runtime unavailable; default engine remains available", "error", err)
		return serverbin.TreeInstall{}
	}
	return install
}
