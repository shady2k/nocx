//go:build nocx_local_ssh

package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/shady2k/nocx/internal/helper/runner"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/storage"
)

func nativeSandboxEnvironment(endpointDir string) session.SandboxEnvironment {
	home, err := os.UserHomeDir()
	if err != nil {
		return session.SandboxEnvironment{}
	}
	paths, err := storage.NewAppPaths()
	if err != nil {
		return session.SandboxEnvironment{}
	}
	runnerPath, err := runner.Install(filepath.Join(endpointDir, "sandbox-runners"))
	if err != nil {
		return session.SandboxEnvironment{}
	}
	reserved := []string{filepath.Join(home, ".nocx"), endpointDir}
	for _, location := range []string{paths.ConfigDir(), paths.DataDir(), paths.CacheDir()} {
		reserved = append(reserved, location)
		for _, profile := range []string{"nocx", "nocx-dev"} {
			reserved = append(reserved, filepath.Join(filepath.Dir(location), profile))
		}
	}
	switch runtime.GOOS {
	case "linux":
		reserved = append(reserved, filepath.Join(home, ".local", "share", "keyrings"))
	case "darwin":
		reserved = append(reserved, filepath.Join(home, "Library", "Keychains"))
	}
	return session.SandboxEnvironment{HostHome: home, RunnerPath: runnerPath, RuntimeBase: filepath.Join(endpointDir, "sandbox-runtimes"), ReservedRoots: reserved}
}
