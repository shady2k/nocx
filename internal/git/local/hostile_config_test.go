package local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
)

// The panel reads a repository whose git config belongs to the user, not to
// us, and D6 runs git under the user's resolved environment. So "what git
// prints" is not a constant: two ordinary configurations change it in ways
// that would break the panel silently rather than loudly. Both were measured
// on git 2.55 before these tests were written.

// TestDiffIgnoresExternalDiffDriver: a user with diff.external set — anyone
// using difftastic or delta as a diff DRIVER — makes plain `git diff` return
// that program's output instead of a unified diff. The panel renders the
// text AS a unified diff, so without --no-ext-diff it would decorate
// arbitrary prose and show the user nonsense with no error anywhere.
//
// Measured: with diff.external pointing at a script that echoes one line,
// `git diff` returns exactly that line; --no-ext-diff returns the real diff.
func TestDiffIgnoresExternalDiffDriver(t *testing.T) {
	dir := diffRepo(t)

	script := filepath.Join(t.TempDir(), "extdiff.sh")
	if err := writeFile(script, "#!/bin/sh\necho 'TOTALLY NOT A UNIFIED DIFF'\n", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := exec.Command(realGitPath(t), "config", "diff.external", script) // #nosec G204 — realGitPath is LookPath-resolved; script is the test's own t.TempDir() path
	cfg.Dir = dir
	cfg.Env = gitEnv(t)
	if out, err := cfg.CombinedOutput(); err != nil {
		t.Fatalf("git config diff.external: %v: %s", err, out)
	}

	repo := openRepo(t, gitEnv(t), dir)
	d, err := repo.Diff(context.Background(), "tracked.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffOK {
		t.Fatalf("State = %s", d.State)
	}
	if strings.Contains(d.Text, "TOTALLY NOT A UNIFIED DIFF") {
		t.Fatalf("the external driver replaced the diff: %q", d.Text)
	}
	if !strings.Contains(d.Text, "@@") || !strings.Contains(d.Text, "+changed") {
		t.Fatalf("not a unified diff: %q", d.Text)
	}
}

// TestWorktreeStateIgnoresStatusShowUntrackedFiles: status.showUntrackedFiles
// is an ordinary user setting, and its DEFAULT effect is to make `git status`
// print nothing about an untracked file. A worktree holding only such a file
// would then look clean — and "looks clean" is the answer RemoveWorktree acts
// on, so a setting the user chose for their shell would decide whether a
// worker's uncommitted work survives.
//
// Measured on git 2.55, and asserted here before the operation is: with the
// setting at "no" a plain `git status --porcelain` prints NOTHING while the
// file sits in the worktree. The explicit --untracked-files=all that
// StatusArgs carries is what overrides it, and this is the test that says so —
// remove the flag and the second half of this test fails by removing the
// worktree, not by a mismatch.
func TestWorktreeStateIgnoresStatusShowUntrackedFiles(t *testing.T) {
	dir, home := worktreeRepo(t)
	if err := commandIn(dir, "config", "status.showUntrackedFiles", "no").Run(); err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The hostile half: the setting really does hide the file from the plain
	// invocation.
	if out := gitOut(t, path, "status", "--porcelain"); out != "" {
		t.Fatalf("the fixture is not hostile: plain `git status --porcelain` reported %q", out)
	}

	list, err := repo.Worktrees(ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	wt, ok := worktreeAt(list, path)
	if !ok {
		t.Fatalf("no entry for %s in %+v", path, list)
	}
	if !wt.Uncommitted {
		t.Fatalf("entry = %+v, want Uncommitted — the setting must not hide a worktree's work from this seam", wt)
	}

	var uncommitted *git.ErrWorktreeUncommitted
	if err := repo.RemoveWorktree(ctx, path); !errors.As(err, &uncommitted) {
		t.Fatalf("RemoveWorktree err = %v, want *git.ErrWorktreeUncommitted", err)
	}
	if _, err := os.Stat(filepath.Join(path, "untracked.txt")); err != nil {
		t.Fatalf("the untracked file is gone: %v", err)
	}
}

// TestWorktreeReadsRunNoConfiguredDiffProgram: diff.external makes a plain
// `git diff` return a program's output instead of a diff, which is why the
// panel's diff carries --no-ext-diff. This pins the same fact for the
// worktree operations: on a repository where the configured program DOES run
// (asserted first, so the operation's answer cannot be the same one by
// accident), driving all four operations leaves its marker absent.
//
// What this case does NOT pin, stated because a probe measured it rather than
// because it reads better: it stays green on a worktree read that grew the
// panel's line counts, because `diff --numstat` never invokes diff.external
// (measured on git 2.55). The invariant that the state read takes no counts
// is asserted where it is observable instead — no `diff` invocation in the
// recorded argv, in worktree_failure_test.go's TestWorktreeInvocationArgv,
// which fails the moment a counts read is added.
//
// The boundary is stated as measured: a repository CAN make one program run
// during AddWorktree, git's own post-checkout hook, because `worktree add`
// checks the base out. That is git's semantics and this project's standing
// decision (hooks always run — CommitArgs carries the same rule), so nothing
// here suppresses it. What a configuration may not do is change the ANSWER,
// and that is what the other case in this file is about.
func TestWorktreeReadsRunNoConfiguredDiffProgram(t *testing.T) {
	dir, home := worktreeRepo(t)
	marker := filepath.Join(t.TempDir(), "diff-external-ran")
	script := filepath.Join(t.TempDir(), "extdiff.sh")
	if err := writeFile(script, "#!/bin/sh\ntouch "+marker+"\necho 'not a diff'\n", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := exec.Command(realGitPath(t), "config", "diff.external", script) // #nosec G204 — realGitPath is LookPath-resolved; script is the test's own t.TempDir() path
	cfg.Dir = dir
	cfg.Env = gitEnv(t)
	if out, err := cfg.CombinedOutput(); err != nil {
		t.Fatalf("git config diff.external: %v: %s", err, out)
	}

	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The hostile half, BEFORE the operations that are the subject: in the
	// worktree, where there is now something to diff, the program really is
	// what a plain `git diff` produces its output with. Its marker is removed
	// afterwards, so the assertion at the end is about those operations alone
	// — and the worktree is left dirty on purpose, because a clean one would
	// have nothing for a counts read to count either.
	if out := gitOut(t, path, "diff"); !strings.Contains(out, "not a diff") {
		t.Fatalf("the fixture is not hostile: a plain `git diff` returned %q", out)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Worktrees(ctx, "master"); err != nil {
		t.Fatal(err)
	}
	// The dirty state read is the one that would take line counts if anything
	// did, so the removal is driven against it and refused before the worktree
	// is cleaned and removed for real.
	if err := repo.RemoveWorktree(ctx, path); err == nil {
		t.Fatal("the edited worktree was removed; this case needs it dirty to exercise the state read")
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveWorktree(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteWorktreeBranch(ctx, "worker-1", "master"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a configured diff.external program ran during the worktree operations")
	}
}

// TestStatusDoesNotRewriteTheIndex: the panel polls this question every few
// seconds while an agent runs git in the terminal beside it. A plain
// `git status` opportunistically refreshes and REWRITES .git/index, so a
// reader would be mutating the repository twelve times a minute — which is
// interference, not observation, and is what --no-optional-locks exists for.
//
// Measured: after `touch` on a tracked file, a plain status moves the index
// mtime; the same status with the flag leaves it alone.
func TestStatusDoesNotRewriteTheIndex(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	index := filepath.Join(dir, ".git", "index")

	// Make the stat cache stale, which is what tempts git to refresh it.
	if err := os.Chtimes(filepath.Join(dir, "tracked.txt"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}

	if _, statusErr := repo.Status(context.Background()); statusErr != nil {
		t.Fatal(statusErr)
	}

	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("status rewrote .git/index (%s -> %s): the panel must not mutate the repository it is reading",
			before.ModTime(), after.ModTime())
	}
}
