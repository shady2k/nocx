package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
)

func TestPrepareAgentRuntimeMissingBundleIsExplicitlyUnavailable(t *testing.T) {
	for _, target := range []string{"linux/amd64", "darwin/amd64", "darwin/arm64"} {
		parts := strings.Split(target, "/")
		install := prepareAgentRuntime(context.Background(), filepath.Join(t.TempDir(), "bin/nocx"), t.TempDir(), parts[0], parts[1])
		if install.Path != "" || install.Hash != "" || install.Lease != nil {
			t.Fatalf("missing bundle should pin unavailability: %#v", install)
		}
	}
}

func TestPrepareAgentRuntimeStagesEveryPlatformBeforePackageDisappears(t *testing.T) {
	for _, target := range []string{"linux/amd64", "darwin/amd64", "darwin/arm64"} {
		t.Run(target, func(t *testing.T) {
			parts := strings.Split(target, "/")
			mount := t.TempDir()
			executable := filepath.Join(mount, "usr/bin/nocx")
			if parts[0] == "darwin" {
				executable = filepath.Join(mount, "App.app/Contents/MacOS/nocx")
			}
			source, err := runtimebundle.PackagedRoot(executable, parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			digest := writePackagedRuntimeFixture(t, source, target)
			data := t.TempDir()
			install := prepareAgentRuntime(context.Background(), executable, data, parts[0], parts[1])
			t.Cleanup(func() { _ = install.Close() })
			if install.Hash != digest || install.Path != filepath.Join(data, "agent-runtime", digest) || install.Lease.File() == nil {
				t.Fatalf("unbound staged runtime: %#v", install)
			}
			locator := runtimebundle.New(install.Path, install.Hash, target)
			if err = locator.Adopt(install.Lease); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = locator.Close() })
			if err = os.RemoveAll(mount); err != nil {
				t.Fatal(err)
			}
			if _, err = locator.Locate(context.Background()); err != nil {
				t.Fatalf("launcher handed off mutable package paths: %v", err)
			}
		})
	}
}

func writePackagedRuntimeFixture(t *testing.T, source, target string) string {
	t.Helper()
	var err error
	files := []any{}
	for _, file := range []struct {
		name, body string
		executable bool
	}{{runtimebundle.InterpreterPath, "#!/bin/sh\nexit 99\n", true}, {runtimebundle.EntrypointPath, "raise RuntimeError('must not start during install')\n", false}} {
		name := filepath.Join(source, file.name)
		if err = os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if file.executable {
			mode = 0o700
		}
		if err = os.WriteFile(name, []byte(file.body), mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(file.body))
		files = append(files, map[string]any{"path": file.name, "sha256": hex.EncodeToString(sum[:]), "size": len(file.body), "executable": file.executable})
	}
	manifest, err := json.Marshal(map[string]any{"formatVersion": 1, "metadata": map[string]any{"bridgeVersion": 1, "target": target, "interpreter": runtimebundle.InterpreterPath, "entrypoint": runtimebundle.EntrypointPath, "pythonVersion": "3.13.16", "lockSha256": strings.Repeat("a", 64), "packages": []any{map[string]any{"name": "pydantic-ai-slim", "version": "1.100.0"}}, "provenance": map[string]any{}}, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:])
}
