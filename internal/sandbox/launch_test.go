package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestResolveOpenCodeCanonicalExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "opencode-real")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatalf("write executable: %v", err)
	}
	link := filepath.Join(dir, OpenCodeIntentName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := ResolveOpenCode()
	if err != nil {
		t.Fatalf("ResolveOpenCode: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveOpenCode = %q, want %q", got, want)
	}
	if status := OpenCodeStatus(); status != (IntentStatus{Name: OpenCodeIntentName, Available: true}) {
		t.Fatalf("OpenCodeStatus = %#v", status)
	}
}

func TestResolveOpenCodeRejectsMissingAndNonExecutable(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := ResolveOpenCode()
		var launchErr *LaunchError
		if !errors.As(err, &launchErr) || launchErr.Reason != ReasonOpenCodeNotFound {
			t.Fatalf("err = %v, want LaunchError(%q)", err, ReasonOpenCodeNotFound)
		}
		if status := OpenCodeStatus(); status != (IntentStatus{Name: OpenCodeIntentName, Available: false, Reason: ReasonOpenCodeNotFound}) {
			t.Fatalf("OpenCodeStatus = %#v", status)
		}
	})

	t.Run("not executable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, OpenCodeIntentName), []byte("not executable"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		t.Setenv("PATH", dir)
		_, err := ResolveOpenCode()
		var launchErr *LaunchError
		if !errors.As(err, &launchErr) || launchErr.Reason != ReasonOpenCodeNotFound {
			t.Fatalf("err = %v, want LaunchError(%q)", err, ReasonOpenCodeNotFound)
		}
	})
}

func TestAddTrustedExecutablesGrantsCanonicalFileAndRuntimeRoots(t *testing.T) {
	workspace, runtimeRoot, _ := fixture(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-real")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatalf("write executable: %v", err)
	}
	link := filepath.Join(dir, "agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	policy, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if err := addTrustedExecutables(policy, []string{link, link}); err != nil {
		t.Fatalf("addTrustedExecutables: %v", err)
	}
	canonical := canonicalPath(t, target)
	count := 0
	for _, file := range policy.ReadOnlyFiles {
		if file == canonical {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("canonical executable count = %d in %v, want 1", count, policy.ReadOnlyFiles)
	}
	if err := ValidatePolicy(policy); err != nil {
		t.Fatalf("ValidatePolicy: %v", err)
	}
}

func TestServiceNewRuntimeRootRedactsPrivatePath(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "private-cache-file")
	if err := os.WriteFile(privatePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	svc := New(log.NewSlogAdapter(nil), privatePath)
	_, err := svc.NewRuntimeRoot()
	var setupErr *SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("err = %v, want SetupError", err)
	}
	if strings.Contains(err.Error(), privatePath) {
		t.Fatalf("runtime-root error leaked private path: %v", err)
	}
}
