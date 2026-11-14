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

func TestExecutableLookupAcceptsExecuteOnlyFirstCandidate(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	program := filepath.Join(first, "agent")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o111); err != nil { //nolint:gosec // execute-only fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "agent"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	if _, err := os.ReadFile(program); err == nil { //nolint:gosec // test TempDir executable fixture
		t.Skip("requires a user without read access to execute-only files")
	}
	for _, command := range []string{"agent", program, "./agent"} {
		got, err := resolveExecutable(command, []string{"PATH=" + first + string(os.PathListSeparator) + second}, first)
		want := program
		if command == "./agent" {
			want = first + "/./agent"
		}
		if err != nil || got != want {
			t.Fatalf("command %q resolved %q, %v; want execute-only %q", command, got, err, want)
		}
	}
}
