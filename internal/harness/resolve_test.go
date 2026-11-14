package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/shellenv"
)

func TestExecutableUsesResolvedEnvironment(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "login-agent")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	got, err := resolveExecutable("login-agent", []string{"PATH=" + dir}, dir)
	if err != nil || got != program {
		t.Fatalf("resolved %q, %v; want %q", got, err, program)
	}
	if _, missingErr := resolveExecutable("login-agent", []string{"PATH=" + t.TempDir()}, dir); missingErr == nil {
		t.Fatal("missing login PATH executable succeeded")
	}
	got, err = resolveExecutable("./login-agent", nil, dir)
	if err != nil || got != dir+"/./login-agent" {
		t.Fatalf("relative command resolved %q, %v", got, err)
	}
}

func TestEnvironmentOverridesMatchExecutableLookup(t *testing.T) {
	env := mergeEnvironment([]string{"PATH=/backend", "KEEP=yes", "NAME=old"}, []string{"PATH=/login", "NAME=new"})
	path, _ := shellenv.Path(env)
	if path != "/login" {
		t.Fatalf("lookup PATH = %q", path)
	}
	if strings.Join(env, ";") != "KEEP=yes;PATH=/login;NAME=new" {
		t.Fatalf("environment = %v", env)
	}
}
