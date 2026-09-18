package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/git"
)

// The worktree operations (brief nocx-xn63t.1.1) against real repositories in
// t.TempDir(): the linked worktree is a git fact that only git can create, so
// a fake would be testing the fake. Where the test needs to know what git
// thinks, it asks git itself (worktreeListing) rather than reading the
// implementation's own answer back.

// worktreeRepo is a repository with one commit on master and one directory
// beside it for the linked worktrees, so no worktree path is ever inside
// another.
func worktreeRepo(t *testing.T) (repoDir, wtHome string) {
	t.Helper()
	dir := newGitRepo(t)
	gitWrite(t, dir, "tracked.txt", "v1")
	gitCommit(t, dir, "one")
	return dir, t.TempDir()
}

// TestAddWorktreeCreatesTheBranchAtBase is criterion 1: a branch that does
// not exist is created at base and checked out at path, git agrees, and the
// result says the branch was created — the fact the caller's compensation
// turns on.
func TestAddWorktreeCreatesTheBranchAtBase(t *testing.T) {
	dir, home := worktreeRepo(t)
	path := filepath.Join(home, "worker-1")
	repo := openRepo(t, gitEnv(t), dir)

	res, err := repo.AddWorktree(context.Background(), "worker-1", "master", path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Error("Created = false, want true — this call created the branch, and the caller deletes only a branch it created")
	}

	listing := worktreeListing(t, dir)
	if !strings.Contains(listing, "worktree "+path) {
		t.Fatalf("git does not list the worktree at %s:\n%s", path, listing)
	}
	if !strings.Contains(listing, "branch refs/heads/worker-1") {
		t.Fatalf("the worktree is not on worker-1:\n%s", listing)
	}
	if _, err := os.Stat(filepath.Join(path, "tracked.txt")); err != nil {
		t.Fatalf("the worktree has no checkout of the base: %v", err)
	}
	if got, want := gitOut(t, path, "rev-parse", "HEAD"), gitOut(t, dir, "rev-parse", "master"); got != want {
		t.Errorf("worker-1 is at %s, want master's tip %s", got, want)
	}
}

// TestAddWorktreeChecksOutAnExistingBranchWithoutApplyingBase is criterion 2's
// first half: a branch that exists and is checked out nowhere is checked out
// as it is — which the test can see because base is deliberately a commit the
// branch is NOT at. If base were applied the tip would move; it does not.
func TestAddWorktreeChecksOutAnExistingBranchWithoutApplyingBase(t *testing.T) {
	dir, home := worktreeRepo(t)
	first := gitOut(t, dir, "rev-parse", "master")
	// A second commit on master, so "master" is no longer where idle is.
	gitWrite(t, dir, "second.txt", "v2")
	gitCommit(t, dir, "two")
	if err := commandIn(dir, "branch", "idle", first).Run(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "idle-wt")
	repo := openRepo(t, gitEnv(t), dir)

	res, err := repo.AddWorktree(context.Background(), "idle", "master", path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created {
		t.Error("Created = true, want false — the branch already existed")
	}
	if got := gitOut(t, path, "rev-parse", "HEAD"); got != first {
		t.Errorf("idle is at %s, want the branch's own commit %s — base is not applied to an existing branch", got, first)
	}
	if !strings.Contains(worktreeListing(t, dir), "branch refs/heads/idle") {
		t.Error("git does not report the new worktree on idle")
	}
}

// TestAddWorktreeRefusesABranchCheckedOutElsewhere is criterion 2's second
// half: the refusal NAMES the other path, and nothing is created. The
// interval, stated with both ends: from the call until it returns, the
// repository gains no worktree, no ref, and no .git/worktrees entry.
func TestAddWorktreeRefusesABranchCheckedOutElsewhere(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	first := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(context.Background(), "worker-1", "master", first); err != nil {
		t.Fatal(err)
	}
	refsBefore, adminBefore := refsOf(t, dir), worktreeAdminNames(t, dir)

	second := filepath.Join(home, "worker-1-again")
	_, err := repo.AddWorktree(context.Background(), "worker-1", "master", second)
	var held *git.ErrBranchCheckedOut
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want *git.ErrBranchCheckedOut", err)
	}
	if held.Branch != "worker-1" || held.Path != first {
		t.Errorf("ErrBranchCheckedOut = %+v, want the branch and the path %s that holds it", held, first)
	}
	assertNoRefOrWorktreeGained(t, dir, refsBefore, adminBefore)
	assertPathAbsent(t, second)
}

// TestAddWorktreeRefusesAnUnresolvableBase is criterion 3's first half. The
// interval, stated with both ends: from the call until it returns an error,
// nothing it made survives — no branch, no directory, no .git/worktrees
// entry. The base is resolved before the add because git creates the branch
// BEFORE it prepares the worktree (measured on 2.55), so a base git would
// refuse must not reach it.
func TestAddWorktreeRefusesAnUnresolvableBase(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	refsBefore, adminBefore := refsOf(t, dir), worktreeAdminNames(t, dir)

	path := filepath.Join(home, "worker-1")
	_, err := repo.AddWorktree(context.Background(), "worker-1", "nosuchbase", path)
	var unresolved *git.ErrBaseUnresolved
	if !errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want *git.ErrBaseUnresolved", err)
	}
	if unresolved.Base != "nosuchbase" {
		t.Errorf("ErrBaseUnresolved.Base = %q, want nosuchbase", unresolved.Base)
	}
	assertNoRefOrWorktreeGained(t, dir, refsBefore, adminBefore)
	assertPathAbsent(t, path)
}

// TestAddWorktreeRefusesAnOccupiedPath is criterion 3's second half, for the
// two ways a path can be occupied. The interval is the same one: nothing it
// made survives. This is also the case that makes the path check load-bearing
// rather than polite — measured on git 2.55, `worktree add -b` at an occupied
// path creates the branch and THEN fails to prepare the worktree, leaving the
// ref behind.
func TestAddWorktreeRefusesAnOccupiedPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, path string)
	}{
		{
			name: "a directory with a file in it",
			build: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a file",
			build: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := worktreeRepo(t)
			repo := openRepo(t, gitEnv(t), dir)
			refsBefore, adminBefore := refsOf(t, dir), worktreeAdminNames(t, dir)

			path := filepath.Join(home, "worker-1")
			tc.build(t, path)
			_, err := repo.AddWorktree(context.Background(), "worker-1", "master", path)
			var occupied *git.ErrWorktreePathNotEmpty
			if !errors.As(err, &occupied) {
				t.Fatalf("err = %v, want *git.ErrWorktreePathNotEmpty", err)
			}
			if occupied.Path != path {
				t.Errorf("ErrWorktreePathNotEmpty.Path = %q, want %q", occupied.Path, path)
			}
			assertNoRefOrWorktreeGained(t, dir, refsBefore, adminBefore)
			if _, statErr := os.Stat(path); statErr != nil {
				t.Errorf("the refused call removed what was already at %s: %v", path, statErr)
			}
		})
	}
}

// TestWorktreesReportsStatePathBranchAndAhead is criterion 4: every worktree
// of the repository with the four facts the caller decides on, and the main
// checkout told apart from the linked ones. The ignored-file case is here
// because "untracked does not include ignored" is the difference between a
// worktree a worker can remove and one it cannot.
func TestWorktreesReportsStatePathBranchAndAhead(t *testing.T) {
	dir, home := worktreeRepo(t)
	// The ignore rule is committed on master BEFORE the worktrees are made,
	// so each of them inherits it and the ignored-file case below is about
	// the ignored file rather than about a commit this test had to add.
	gitWrite(t, dir, ".gitignore", "ignored.txt\n")
	gitCommit(t, dir, "ignore")
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()

	clean := filepath.Join(home, "wt-clean")
	edited := filepath.Join(home, "wt-edit")
	untracked := filepath.Join(home, "wt-untracked")
	ignored := filepath.Join(home, "wt-ignored")
	ahead := filepath.Join(home, "wt-ahead")
	detached := filepath.Join(home, "wt-detached")
	for _, wt := range []struct{ branch, path string }{
		{"wt-clean", clean},
		{"wt-edit", edited},
		{"wt-untracked", untracked},
		{"wt-ignored", ignored},
		{"wt-ahead", ahead},
	} {
		if _, err := repo.AddWorktree(ctx, wt.branch, "master", wt.path); err != nil {
			t.Fatal(err)
		}
	}
	gitWrite(t, ahead, "ahead-1.txt", "v1")
	gitCommit(t, ahead, "ahead-1")
	gitWrite(t, ahead, "ahead-2.txt", "v2")
	gitCommit(t, ahead, "ahead-2")

	if err := os.WriteFile(filepath.Join(edited, "tracked.txt"), []byte("edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(untracked, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignored, "ignored.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "worktree", "add", "--detach", detached, "master").Run(); err != nil {
		t.Fatal(err)
	}

	list, err := repo.Worktrees(ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]git.Worktree{}
	for _, wt := range list {
		byPath[wt.Path] = wt
	}
	want := []string{dir, clean, edited, untracked, ignored, ahead, detached}
	if len(list) != len(want) {
		t.Fatalf("Worktrees = %d entries, want %d: %+v", len(list), len(want), list)
	}
	for _, p := range want {
		if _, ok := byPath[p]; !ok {
			t.Fatalf("no entry for %s in %+v", p, list)
		}
	}

	// The main checkout is the first record git prints, and exactly one.
	if !list[0].Main || list[0].Path != dir {
		t.Errorf("first entry = %+v, want the main checkout %s with Main set", list[0], dir)
	}
	for _, wt := range list[1:] {
		if wt.Main {
			t.Errorf("%s reported as the main checkout; only one worktree is", wt.Path)
		}
	}

	for _, tc := range []struct {
		name        string
		path        string
		branch      string
		uncommitted bool
		ahead       int
	}{
		{"main checkout", dir, "master", false, 0},
		{"a clean linked worktree", clean, "wt-clean", false, 0},
		{"an unstaged edit", edited, "wt-edit", true, 0},
		{"an untracked file", untracked, "wt-untracked", true, 0},
		{"an ignored file only", ignored, "wt-ignored", false, 0},
		{"two commits ahead", ahead, "wt-ahead", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt := byPath[tc.path]
			if wt.State != git.WorktreeReadable {
				t.Fatalf("State = %q (%s), want readable", wt.State, wt.Reason)
			}
			if wt.Branch != tc.branch || wt.Detached {
				t.Errorf("branch = %q detached=%v, want %q on a branch", wt.Branch, wt.Detached, tc.branch)
			}
			if wt.Uncommitted != tc.uncommitted {
				t.Errorf("Uncommitted = %v, want %v", wt.Uncommitted, tc.uncommitted)
			}
			if wt.Ahead != tc.ahead {
				t.Errorf("Ahead = %d, want %d", wt.Ahead, tc.ahead)
			}
		})
	}

	det := byPath[detached]
	if !det.Detached || det.Branch != "" {
		t.Errorf("detached worktree = %+v, want Detached with no branch", det)
	}
}

// TestWorktreesReportsAStateItCannotRead: a worktree whose directory is gone
// is still a worktree of the repository — git lists the record and calls it
// prunable — and its state cannot be read. The domain state says so, with the
// reason, rather than reporting it clean.
func TestWorktreesReportsAStateItCannotRead(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "wt-gone")
	if _, err := repo.AddWorktree(ctx, "wt-gone", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	list, err := repo.Worktrees(ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	var found *git.Worktree
	for i := range list {
		if list[i].Path == path {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("the prunable worktree is missing from %+v — git still lists it", list)
	}
	if found.State != git.WorktreeUnreadable {
		t.Fatalf("State = %q, want unreadable — a state that could not be read is never clean", found.State)
	}
	if found.Reason == "" {
		t.Error("Reason is empty: the caller cannot tell why the state could not be read")
	}
	if found.Uncommitted || found.Ahead != 0 {
		t.Errorf("unreadable entry carries facts it could not have read: %+v", found)
	}
}

// TestRemoveWorktreeLeavesTheBranch is criterion 5's first half, and the
// whole compensation flow end to end: create, remove, and delete the branch
// the create made.
func TestRemoveWorktreeLeavesTheBranch(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}

	if err := repo.RemoveWorktree(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worktree directory is still there (stat err = %v)", err)
	}
	if strings.Contains(worktreeListing(t, dir), "worktree "+path) {
		t.Error("git still lists the removed worktree")
	}
	if got := gitOut(t, dir, "rev-parse", "--verify", "refs/heads/worker-1"); got == "" {
		t.Error("the branch was removed with the worktree; removing a worktree leaves its branch")
	}

	if err := repo.DeleteWorktreeBranch(ctx, "worker-1", "master"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/worker-1"); err == nil {
		t.Error("the branch Add created is still there after the compensation deleted it")
	}
	if got := gitOut(t, dir, "rev-parse", "--verify", "refs/heads/master"); got == "" {
		t.Error("the delete took master with it")
	}
}

// TestRemoveWorktreeRefusesUncommittedWork is criterion 5's second half: a
// tracked change and an untracked file each refuse by name, and nothing is
// removed — not the directory, not the branch.
func TestRemoveWorktreeRefusesUncommittedWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dirty  func(t *testing.T, path string)
		reason string
	}{
		{
			name: "an unstaged edit",
			dirty: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("edited"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			reason: "a modified tracked file is uncommitted work",
		},
		{
			name: "an untracked file only",
			dirty: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			reason: "an untracked file is uncommitted work — nothing else would ever save it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := worktreeRepo(t)
			repo := openRepo(t, gitEnv(t), dir)
			ctx := context.Background()
			path := filepath.Join(home, "worker-1")
			if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
				t.Fatal(err)
			}
			tc.dirty(t, path)

			err := repo.RemoveWorktree(ctx, path)
			var dirty *git.ErrWorktreeUncommitted
			if !errors.As(err, &dirty) {
				t.Fatalf("err = %v, want *git.ErrWorktreeUncommitted", err)
			}
			if dirty.Path != path {
				t.Errorf("ErrWorktreeUncommitted.Path = %q, want %q", dirty.Path, path)
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Errorf("the worktree was removed anyway: %v", statErr)
			}
			if _, refErr := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/worker-1"); refErr != nil {
				t.Error("the branch was removed with the refused worktree")
			}
		})
	}
}

// TestRemoveWorktreeRefusesPathsItMayNotRemove is criterion 5's third part:
// the main checkout, a directory that is not a worktree of this repository,
// and a path that is not there at all each refuse by name, and the main
// checkout is still standing afterwards.
func TestRemoveWorktreeRefusesPathsItMayNotRemove(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()

	foreign := filepath.Join(home, "not-a-worktree")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}

	var mainErr *git.ErrMainWorktree
	if err := repo.RemoveWorktree(ctx, dir); !errors.As(err, &mainErr) {
		t.Fatalf("removing the main checkout: err = %v, want *git.ErrMainWorktree", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatalf("the main checkout was damaged: %v", err)
	}

	for _, path := range []string{foreign, filepath.Join(home, "never-existed")} {
		var notAWorktree *git.ErrNotAWorktree
		if err := repo.RemoveWorktree(ctx, path); !errors.As(err, &notAWorktree) {
			t.Errorf("removing %s: err = %v, want *git.ErrNotAWorktree", path, err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("the refused foreign directory was removed: %v", err)
	}
}

// TestRemoveWorktreeLeavesALockedWorktreeToGit: `git worktree lock` is a
// person's deliberate marker on a worktree, and the parser above consumes the
// listing's `locked` line without acting on it — because the decision belongs
// to git, whose refusal names the remedy ("use 'remove -f -f' to override or
// unlock first"). This pins that: the seam does not invent a force, and it
// does not swallow git's account of why it will not.
func TestRemoveWorktreeLeavesALockedWorktreeToGit(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "worktree", "lock", path).Run(); err != nil {
		t.Fatal(err)
	}

	err := repo.RemoveWorktree(ctx, path)
	if err == nil {
		t.Fatal("a locked worktree was removed")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("err = %v, want git's own account of the lock", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the locked worktree is gone: %v", statErr)
	}
	if !strings.Contains(worktreeListing(t, dir), "worktree "+path) {
		t.Error("the locked worktree is no longer listed")
	}
}

// TestRemoveWorktreeRefusesAStateItCannotRead: the one refusal that is not
// about the worktree being dirty but about nobody being able to say — a
// worktree whose directory is gone refuses rather than being treated as
// clean.
func TestRemoveWorktreeRefusesAStateItCannotRead(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "wt-gone")
	if _, err := repo.AddWorktree(ctx, "wt-gone", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	var unreadable *git.ErrWorktreeUnreadable
	if err := repo.RemoveWorktree(ctx, path); !errors.As(err, &unreadable) {
		t.Fatalf("err = %v, want *git.ErrWorktreeUnreadable", err)
	}
	if !strings.Contains(worktreeListing(t, dir), "worktree "+path) {
		t.Error("the record was removed even though the call refused")
	}
}

// TestDeleteWorktreeBranchIsNarrow: the compensation deletes only a branch
// with no commits beyond base. A branch ahead of base refuses by name and
// stays; a branch checked out in a worktree refuses by name and stays; a
// branch that is already gone is not an error, because the goal state holds.
func TestDeleteWorktreeBranchIsNarrow(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()

	// At base: deleted.
	if err := commandIn(dir, "branch", "at-base", "master").Run(); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteWorktreeBranch(ctx, "at-base", "master"); err != nil {
		t.Fatalf("deleting a branch with no commits beyond base: %v", err)
	}
	if _, err := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/at-base"); err == nil {
		t.Error("at-base is still there")
	}

	// Ahead of base: refused, ref intact.
	if err := commandIn(dir, "branch", "ahead", "master").Run(); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "checkout", "-q", "ahead").Run(); err != nil {
		t.Fatal(err)
	}
	gitWrite(t, dir, "more.txt", "v2")
	gitCommit(t, dir, "more")
	if err := commandIn(dir, "checkout", "-q", "master").Run(); err != nil {
		t.Fatal(err)
	}
	err := repo.DeleteWorktreeBranch(ctx, "ahead", "master")
	var beyond *git.ErrBranchHasCommits
	if !errors.As(err, &beyond) {
		t.Fatalf("err = %v, want *git.ErrBranchHasCommits", err)
	}
	if beyond.Ahead != 1 {
		t.Errorf("ErrBranchHasCommits.Ahead = %d, want 1", beyond.Ahead)
	}
	if _, err := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/ahead"); err != nil {
		t.Error("the refused branch was deleted anyway")
	}

	// Checked out in a worktree: refused, and the worktree's branch is intact.
	path := filepath.Join(home, "checked-out")
	if _, err := repo.AddWorktree(ctx, "checked-out", "master", path); err != nil {
		t.Fatal(err)
	}
	var held *git.ErrBranchCheckedOut
	if err := repo.DeleteWorktreeBranch(ctx, "checked-out", "master"); !errors.As(err, &held) {
		t.Fatalf("err = %v, want *git.ErrBranchCheckedOut", err)
	}
	if held.Path != path {
		t.Errorf("ErrBranchCheckedOut.Path = %q, want %q", held.Path, path)
	}
	if _, err := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/checked-out"); err != nil {
		t.Error("the branch of a live worktree was deleted")
	}

	// Already gone: the compensation's own goal state, so not an error.
	if err := repo.DeleteWorktreeBranch(ctx, "never-existed", "master"); err != nil {
		t.Errorf("deleting a branch that is not there: %v", err)
	}

	// Base that does not resolve: refused before anything is deleted.
	if err := commandIn(dir, "branch", "orphan-check", "master").Run(); err != nil {
		t.Fatal(err)
	}
	var unresolved *git.ErrBaseUnresolved
	if err := repo.DeleteWorktreeBranch(ctx, "orphan-check", "nosuchbase"); !errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want *git.ErrBaseUnresolved", err)
	}
	if _, err := gitTry(t, dir, "rev-parse", "--verify", "refs/heads/orphan-check"); err != nil {
		t.Error("a branch was deleted while its base did not resolve")
	}
}

// ── the test's own oracles ─────────────────────────────────────────────

// worktreeListing runs git worktree list --porcelain in dir: what git thinks,
// asked directly, so no assertion about the listing is answered by the code
// under test.
func worktreeListing(t *testing.T, dir string) string {
	t.Helper()
	return gitOut(t, dir, "worktree", "list", "--porcelain")
}

// worktreeAdminNames lists the worktree admin entries of dir's repository.
// The name git derives from a path is its own business, so the interval
// assertions compare this SET before and after rather than looking for a
// derived name.
func worktreeAdminNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, ".git", "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// refsOf lists the repository's branch refs.
func refsOf(t *testing.T, dir string) string {
	t.Helper()
	return gitOut(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
}

// assertNoRefOrWorktreeGained is the interval of criterion 3 stated as a
// check, for the half that holds whatever the path was: from the refused call
// until it returned, no branch appeared and no worktree admin entry was
// added. The path itself is asserted at each call site, because it means
// different things there — absent after a refusal that would have created it,
// and untouched after one where the test put something in the way.
func assertNoRefOrWorktreeGained(t *testing.T, dir string, refsBefore string, adminBefore []string) {
	t.Helper()
	if after := refsOf(t, dir); after != refsBefore {
		t.Errorf("refs changed across the refused call:\nbefore:\n%s\nafter:\n%s", refsBefore, after)
	}
	if after := worktreeAdminNames(t, dir); strings.Join(after, "\n") != strings.Join(adminBefore, "\n") {
		t.Errorf("worktree admin entries changed across the refused call: %v -> %v", adminBefore, after)
	}
}

// assertPathAbsent is the other half for the refusals that would have created
// the path: nothing it made survives.
func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists after the refused call (stat err = %v)", path, err)
	}
}
