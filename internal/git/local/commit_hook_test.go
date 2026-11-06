package local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ADR-8eej3e66 stage 5: a commit's max is its HOOK deadline. The operation
// above passes it as the work context's deadline; this is the half that proves
// the process actually dies at it. A hook that sleeps past the deadline leaves
// no commit behind, and the call returns at its budget rather than at the
// hook's whim.
func TestCommitHookDiesAtTheCallersDeadline(t *testing.T) {
	dir := newGitRepo(t)
	env := gitEnv(t)
	gitWrite(t, dir, "f.txt", "hello\n")

	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil { //nolint:gosec // a git hook directory is git's own 0755 shape
		t.Fatal(err)
	}
	// The hook's shell dies with its process group; 30 s would outlast the
	// test on its own if the kill did not work.
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil { //nolint:gosec // a git hook must be executable
		t.Fatal(err)
	}

	repo := openRepo(t, env, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := repo.Commit(ctx, "hello", false)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Commit err = %v, want the deadline; a hook outlived its bound", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Commit returned after %v; the hook was not killed at its deadline", elapsed)
	}

	count := exec.Command(realGitPath(t), "rev-list", "--count", "HEAD") // #nosec G204 — realGitPath is LookPath-resolved; args are fixed
	count.Dir = dir
	count.Env = env
	if out, err := count.CombinedOutput(); err == nil {
		t.Fatalf("a commit landed despite the killed hook: %s", out)
	}
}
