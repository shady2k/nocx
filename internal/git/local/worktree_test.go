package local

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/waittest"
)

// The worktree operations (brief nocx-xn63t.1.1) against real repositories in
// t.TempDir(): the linked worktree is a git fact that only git can create, so
// a fake would be testing the fake. Where the test needs to know what git
// thinks, it asks git itself (worktreeListing) rather than reading the
// implementation's own answer back.

// worktreeRepo is a repository with one commit on master and one directory
// beside it for the linked worktrees, so no worktree path is ever inside
// another.
//
// The home is canonicalTempDir, for the reason that helper is for: git answers
// a worktree path in its RESOLVED spelling — `worktree list`, and the path a
// refusal names — while t.TempDir() hands back the unresolved one, which on
// macOS is /private/var against /var for every temporary directory. A test
// that asks for a path and then compares it with git's answer has to ask in the
// spelling git answers in, or the difference under test is the fixture's. The
// two tests that deliberately hand the seam a caller's alias spelling — the
// partial-add rollback and the same-path gate — take a home of their own.
func worktreeRepo(t *testing.T) (repoDir, wtHome string) {
	t.Helper()
	dir := newGitRepo(t)
	gitWrite(t, dir, "tracked.txt", "v1")
	gitCommit(t, dir, "one")
	return dir, canonicalTempDir(t)
}

// TestAddWorktreePreservesABranchCreatedAfterTheAbsenceRead fixes the race
// between the branch existence check and git's create-on-add operation. The
// wrapper creates the branch only after the real rev-parse has answered that
// it is absent, then returns that absence to this invocation. The old `add -b`
// path fails because the ref now exists and its cleanup incorrectly deletes it.
func TestAddWorktreePreservesABranchCreatedAfterTheAbsenceRead(t *testing.T) {
	dir, home := worktreeRepo(t)
	branch := "feat/race"
	marker := filepath.Join(t.TempDir(), "competitor-created-branch")
	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "git")
	zeroOID := strings.Repeat("0", len(gitOut(t, dir, "rev-parse", "HEAD")))
	realGit := realGitPath(t)
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2 $3 $4\" = \"rev-parse --verify --quiet refs/heads/" + branch + "\" ]; then\n" +
		"  \"$NOCX_REAL_GIT\" \"$@\"\n" +
		"  status=$?\n" +
		"  if [ \"$status\" -eq 1 ]; then\n" +
		"    \"$NOCX_REAL_GIT\" update-ref refs/heads/" + branch + " \"$NOCX_RACE_OID\" \"$NOCX_ZERO_OID\" || exit $?\n" +
		"    : > \"$NOCX_RACE_MARKER\"\n" +
		"  fi\n" +
		"  exit \"$status\"\n" +
		"fi\n" +
		"exec \"$NOCX_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // G306: controlled test executable.
		t.Fatal(err)
	}
	env := gitEnv(t)
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[i] = "PATH=" + wrapperDir + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
		}
	}
	env = append(env,
		"NOCX_REAL_GIT="+realGit,
		"NOCX_RACE_OID="+gitOut(t, dir, "rev-parse", "master"),
		"NOCX_ZERO_OID="+zeroOID,
		"NOCX_RACE_MARKER="+marker,
	)
	repo := openRepo(t, env, dir)

	path := filepath.Join(home, "worker")
	if _, err := repo.AddWorktree(context.Background(), branch, "master", path); err == nil {
		t.Fatal("AddWorktree succeeded after the branch appeared between its absence read and creation")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the competing branch was not created after the absence read: %v", err)
	}
	want := gitOut(t, dir, "rev-parse", "master")
	got, err := commandIn(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatalf("failed add removed the competing branch: %v", err)
	}
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("competing branch tip = %s, want master %s", strings.TrimSpace(string(got)), want)
	}
}

// TestAddWorktreeCleansAnAmbiguousClaimAfterContextCancellation covers the
// interval where update-ref commits the branch but the caller's context stops
// the command before its exit status reaches AddWorktree. The claim's unique
// reflog message proves ownership, so cleanup can remove only that ref.
func TestAddWorktreeCleansAnAmbiguousClaimAfterContextCancellation(t *testing.T) {
	dir, home := worktreeRepo(t)
	if err := commandIn(dir, "config", "core.logAllRefUpdates", "false").Run(); err != nil {
		t.Fatalf("disable automatic reflogs: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "ref-created")
	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "git")
	realGit := realGitPath(t)
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"update-ref\" ] && { [ \"$2\" = \"--create-reflog\" ] || [ \"$2\" = \"refs/heads/feat/context-cancelled\" ]; }; then\n" +
		"  \"$NOCX_REAL_GIT\" \"$@\" || exit $?\n" +
		"  : > \"$NOCX_REF_CREATED\"\n" +
		"  while [ ! -e \"$NOCX_REF_RELEASED\" ]; do sleep 0.01; done\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec \"$NOCX_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // G306: controlled test executable.
		t.Fatal(err)
	}
	env := gitEnv(t)
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[i] = "PATH=" + wrapperDir + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
		}
	}
	env = append(env,
		"NOCX_REAL_GIT="+realGit,
		"NOCX_REF_CREATED="+marker,
		"NOCX_REF_RELEASED="+filepath.Join(t.TempDir(), "never-released"),
	)
	repo := openRepo(t, env, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type addResult struct {
		added git.WorktreeAdded
		err   error
	}
	result := make(chan addResult, 1)
	path := filepath.Join(home, "context-cancelled")
	go func() {
		added, err := repo.AddWorktree(ctx, "feat/context-cancelled", "master", path)
		result <- addResult{added: added, err: err}
	}()
	waittest.WaitFor(t, "update-ref to commit the new branch before cancellation", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	outcome := <-result
	if outcome.err == nil {
		t.Fatal("AddWorktree succeeded after its ref command was cancelled")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the checkout at %q survived: %v", path, err)
	}
	if out, err := commandIn(dir, "rev-parse", "--verify", "--quiet", "refs/heads/feat/context-cancelled").CombinedOutput(); err == nil {
		t.Errorf("the claim created before cancellation survived: %s", strings.TrimSpace(string(out)))
	}
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

// TestAddWorktreeCleansARegisteredPartialCheckoutOnCancellation exercises a
// Git add that has created its checkout and branch but has not returned. The
// post-checkout hook signals this test only after Git registered and populated
// the path, then stays alive until cancellation kills Git's process group.
// This pins the AddWorktree error boundary: its caller has no undo value yet,
// so the Git seam must roll back its own partial work. The worktree home is
// under a TMPDIR symlink, matching the macOS /var -> /private/var path shape.
func TestAddWorktreeCleansARegisteredPartialCheckoutOnCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		branch      string
		preExisting bool
	}{
		{name: "new branch", branch: "feat/partial"},
		{name: "pre-existing branch", branch: "keep", preExisting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempParent, err := os.MkdirTemp("", "nocx-partial-worktree-")
			if err != nil {
				t.Fatalf("make temp parent: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tempParent) })
			tempTarget := filepath.Join(tempParent, "real")
			if err := os.Mkdir(tempTarget, 0o700); err != nil {
				t.Fatalf("make temp target: %v", err)
			}
			tempAlias := filepath.Join(tempParent, "alias")
			if err := os.Symlink(tempTarget, tempAlias); err != nil {
				t.Fatalf("symlink temp target: %v", err)
			}
			t.Setenv("TMPDIR", tempAlias)

			// The home is the caller's spelling on purpose: git answers the
			// resolved one, and the seam has to join the two.
			dir, _ := worktreeRepo(t)
			home := t.TempDir()
			if !strings.HasPrefix(home, tempAlias+string(os.PathSeparator)) {
				t.Fatalf("worktree home %q does not retain TMPDIR alias %q", home, tempAlias)
			}
			if tc.preExisting {
				if err := commandIn(dir, "branch", tc.branch, "master").Run(); err != nil {
					t.Fatalf("create pre-existing branch: %v", err)
				}
			}

			env := append(gitEnv(t), "NOCX_TEST_CANCEL_PID="+strconv.Itoa(os.Getpid()))
			hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
			hookBody := []byte("#!/bin/sh\nkill -USR1 \"$NOCX_TEST_CANCEL_PID\"\nexec tail -f /dev/null\n")
			if err := os.WriteFile(hook, hookBody, 0o700); err != nil { //nolint:gosec // G306: Git must execute this controlled test hook.
				t.Fatalf("install blocking post-checkout hook: %v", err)
			}

			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGUSR1)
			defer signal.Stop(signals)

			repo := openRepo(t, env, dir)
			path := filepath.Join(home, "partial")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			type addResult struct {
				added git.WorktreeAdded
				err   error
			}
			done := make(chan addResult, 1)
			go func() {
				added, addErr := repo.AddWorktree(ctx, tc.branch, "master", path)
				done <- addResult{added: added, err: addErr}
			}()

			select {
			case <-signals:
				// The hook signals only after Git has populated and registered the
				// checkout. Cancellation now exercises the partial-add rollback.
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("hook fired without the checkout at %q: %v", path, err)
				}
				cancel()
			case result := <-done:
				cancel()
				t.Fatalf("AddWorktree returned before the post-checkout event: added=%+v err=%v", result.added, result.err)
			}
			result := <-done
			if result.err == nil {
				t.Fatal("AddWorktree succeeded after its context was cancelled in post-checkout")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("partial checkout %q survived AddWorktree error %v: %v", path, result.err, err)
			}
			_, branchErr := commandIn(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+tc.branch).CombinedOutput()
			if tc.preExisting && branchErr != nil {
				t.Errorf("pre-existing branch %q was removed: %v", tc.branch, branchErr)
			}
			if !tc.preExisting && branchErr == nil {
				t.Errorf("branch %q created by the failed add survived", tc.branch)
			}
		})
	}
}

// TestAddWorktreeChecksOutAnExistingBranchWithoutApplyingBase is criterion 2's
// first half: a branch that exists and is checked out nowhere is checked out
// as it is — which the test can see because base is deliberately a commit the
// branch is NOT at. If base were applied the tip would move; it does not.
type observedDoneContext struct {
	context.Context
	doneCalled chan struct{}
	once       sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.doneCalled) })
	return c.Context.Done()
}

// TestAddWorktreeWaiterCannotRemoveTheSuccessfulCheckout verifies ownership
// when a second call targets the same physical path through a different
// symlink spelling. The first add is held in its post-checkout hook after Git
// registers the checkout. The second call must wait at the per-path gate, and
// cancellation there must leave the first call's checkout and branch intact.
func TestAddWorktreeWaiterCannotRemoveTheSuccessfulCheckout(t *testing.T) {
	tempParent, tempParentErr := os.MkdirTemp("", "nocx-worktree-gate-")
	if tempParentErr != nil {
		t.Fatalf("make temp parent: %v", tempParentErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempParent) })
	tempTarget := filepath.Join(tempParent, "real")
	if err := os.Mkdir(tempTarget, 0o700); err != nil {
		t.Fatalf("make temp target: %v", err)
	}
	tempAlias := filepath.Join(tempParent, "alias")
	if err := os.Symlink(tempTarget, tempAlias); err != nil {
		t.Fatalf("symlink temp target: %v", err)
	}
	t.Setenv("TMPDIR", tempAlias)

	// The home is the caller's spelling on purpose: the gate below has to see
	// one physical path through two of them.
	dir, _ := worktreeRepo(t)
	home := t.TempDir()
	linkedCwd := filepath.Join(home, "linked-caller")
	if output, err := commandIn(dir, "worktree", "add", "--detach", "--", linkedCwd, "master").CombinedOutput(); err != nil {
		t.Fatalf("create linked caller worktree: %v: %s", err, output)
	}
	pathAlias := filepath.Join(home, "winner")
	if !strings.HasPrefix(pathAlias, tempAlias+string(os.PathSeparator)) {
		t.Fatalf("worktree path %q does not retain TMPDIR alias %q", pathAlias, tempAlias)
	}
	pathReal := filepath.Join(tempTarget, strings.TrimPrefix(pathAlias, tempAlias+string(os.PathSeparator)))
	fifo := filepath.Join(tempParent, "release")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("make release FIFO: %v", err)
	}
	hookStarted := filepath.Join(tempParent, "hook-started")
	env := append(gitEnv(t),
		"NOCX_TEST_CANCEL_PID="+strconv.Itoa(os.Getpid()),
		"NOCX_HOOK_STARTED="+hookStarted,
		"NOCX_RELEASE_FIFO="+fifo,
	)
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	hookBody := []byte("#!/bin/sh\nif [ ! -e \"$NOCX_HOOK_STARTED\" ]; then\n  : > \"$NOCX_HOOK_STARTED\"\n  kill -USR1 \"$NOCX_TEST_CANCEL_PID\"\n  cat \"$NOCX_RELEASE_FIFO\" >/dev/null\nfi\n")
	if err := os.WriteFile(hook, hookBody, 0o700); err != nil { //nolint:gosec // G306: Git must execute this controlled test hook.
		t.Fatalf("install gated post-checkout hook: %v", err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)

	winnerFactory := NewFactory(WithEnv(env))
	waiterFactory := NewFactory(WithEnv(env))
	t.Cleanup(winnerFactory.Stop)
	t.Cleanup(waiterFactory.Stop)
	open := func(factory *Factory, cwd string) *Repo {
		t.Helper()
		repo, outcome, err := factory.Open(context.Background(), cwd)
		if err != nil {
			t.Fatalf("open Repo from shared Factory: %v", err)
		}
		if outcome.State != git.OpenOK {
			t.Fatalf("Factory.Open outcome = %s, want ok", outcome.State)
		}
		local, ok := repo.(*Repo)
		if !ok {
			t.Fatalf("Factory.Open returned %T, want *Repo", repo)
		}
		return local
	}
	winnerRepo := open(winnerFactory, dir)
	waiterRepo := open(waiterFactory, linkedCwd)
	if winnerRepo == waiterRepo || winnerRepo.gitDir == waiterRepo.gitDir {
		t.Fatal("test setup did not open distinct Repo values from the main and linked worktrees")
	}
	if !samePath(winnerRepo.commonGitDir, waiterRepo.commonGitDir) {
		t.Fatalf("common git directories differ: %q and %q", winnerRepo.commonGitDir, waiterRepo.commonGitDir)
	}
	if winnerRepo.worktreeAdds != waiterRepo.worktreeAdds {
		t.Fatal("Repos from separate Factories do not share the worktree-add gate")
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	t.Cleanup(cancelFirst)
	type addResult struct {
		added git.WorktreeAdded
		err   error
	}
	firstDone := make(chan addResult, 1)
	go func() {
		added, addErr := winnerRepo.AddWorktree(firstCtx, "feat/race", "master", pathAlias)
		firstDone <- addResult{added: added, err: addErr}
	}()
	select {
	case <-signals:
		if _, err := os.Stat(pathAlias); err != nil {
			t.Fatalf("hook fired without winner checkout %q: %v", pathAlias, err)
		}
	case result := <-firstDone:
		t.Fatalf("first AddWorktree returned before its checkout hook: added=%+v err=%v", result.added, result.err)
	}

	secondBase, cancelSecond := context.WithCancel(context.Background())
	t.Cleanup(cancelSecond)
	secondCtx := &observedDoneContext{Context: secondBase, doneCalled: make(chan struct{})}
	secondDone := make(chan error, 1)
	go func() {
		_, secondErr := waiterRepo.AddWorktree(secondCtx, "feat/race", "master", pathReal)
		secondDone <- secondErr
	}()
	// Done is first read by the gate after it has registered this same-path
	// waiter. Cancellation therefore tests the gate, not a later Git command.
	<-secondCtx.doneCalled
	otherUnlock, unlockErr := winnerRepo.worktreeAdds.lock(context.Background(), winnerRepo.commonGitDir, filepath.Join(home, "independent"))
	if unlockErr != nil {
		t.Fatalf("different target path was serialized: %v", unlockErr)
	}
	otherUnlock()
	cancelSecond()
	secondErr := <-secondDone
	if !errors.Is(secondErr, context.Canceled) || !strings.Contains(secondErr.Error(), "worktree path reservation") {
		t.Fatalf("second AddWorktree error = %v, want cancellation while waiting for the path reservation", secondErr)
	}
	if _, err := os.Lstat(pathAlias); err != nil {
		t.Fatalf("second call removed the first call's checkout: %v", err)
	}
	if err := commandIn(dir, "show-ref", "--verify", "--quiet", "refs/heads/feat/race").Run(); err != nil {
		t.Fatalf("second call removed the first call's branch: %v", err)
	}

	// Release the winner's hook without cancelling its context; its add must
	// return successfully and retain the checkout it owns.
	writer, openErr := os.OpenFile(fifo, os.O_WRONLY, 0) //nolint:gosec // G304: FIFO is created under this test's temp directory.
	if openErr != nil {
		t.Fatalf("open hook release FIFO: %v", openErr)
	}
	_, writeErr := writer.Write([]byte("continue\n"))
	closeErr := writer.Close()
	if writeErr != nil {
		t.Fatalf("release post-checkout hook: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close hook release FIFO: %v", closeErr)
	}
	first := <-firstDone
	if first.err != nil || !first.added.Created {
		t.Fatalf("first AddWorktree = %+v, %v; want successful created checkout", first.added, first.err)
	}
	if _, err := os.Lstat(pathAlias); err != nil {
		t.Fatalf("winner checkout did not survive both calls: %v", err)
	}
}

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

	if _, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{}); err != nil {
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

			_, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{})
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

// TestRemoveWorktreeDiscardsTheWorkOnlyWhenTheCallAsksForIt is the seam's
// half of nocx-xn63t.6.40 (ADR-0088), both ends on one checkout: the same
// dirty worktree refuses the call that did not ask, and goes — with its work
// counted — for the call that did.
//
// The ignored file is the third case the bead names, and it is here rather
// than assumed: it neither refuses the removal (git has never counted it as
// dirty) nor appears in the counts (git's status does not list it), and it
// leaves with the directory like everything else in it.
func TestRemoveWorktreeDiscardsTheWorkOnlyWhenTheCallAsksForIt(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}
	// The repository's own exclude file, so a file can be one git was told
	// not to look at without the checkout gaining a record of its own.
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.txt", "two.txt", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The call that did not ask: refused, and the answer counts nothing.
	discard, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{})
	var dirty *git.ErrWorktreeUncommitted
	if !errors.As(err, &dirty) {
		t.Fatalf("err = %v, want *git.ErrWorktreeUncommitted", err)
	}
	if discard != (git.WorktreeDiscard{}) {
		t.Errorf("a refused removal answered %+v, want nothing discarded", discard)
	}

	// The call that asked: the checkout and the work go, counted.
	discard, err = repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{Discard: true})
	if err != nil {
		t.Fatalf("remove with the discard: %v", err)
	}
	want := git.WorktreeDiscard{Modified: 1, Untracked: 2}
	if discard != want {
		t.Errorf("discarded = %+v, want %+v — the ignored file is in neither count", discard, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worktree directory is still there (stat err = %v)", err)
	}
	if got := gitOut(t, dir, "rev-parse", "--verify", "refs/heads/worker-1"); got == "" {
		t.Error("the discard took the branch with the worktree")
	}
}

// TestRemoveWorktreeDiscardIsStillRefusedByGitForALock: the discard is one
// --force, and one --force is not a key to somebody's lock. git's own account
// of the lock reaches the caller unchanged, and nothing on disk moves —
// because the alternative is a seam that quietly overrides a decision a
// person made about their own worktree.
func TestRemoveWorktreeDiscardIsStillRefusedByGitForALock(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	path := filepath.Join(home, "worker-1")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "worktree", "lock", path).Run(); err != nil {
		t.Fatal(err)
	}

	_, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{Discard: true})
	if err == nil {
		t.Fatal("a locked worktree was removed by a discard")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("err = %v, want git's own account of the lock", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the locked worktree is gone: %v", statErr)
	}

	// And the discard reaches no other refusal either: the main checkout is
	// still refused by name.
	var mainErr *git.ErrMainWorktree
	if _, err := repo.RemoveWorktree(ctx, dir, git.RemoveWorktreeOptions{Discard: true}); !errors.As(err, &mainErr) {
		t.Errorf("removing the main checkout with a discard: err = %v, want *git.ErrMainWorktree", err)
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
	if _, err := repo.RemoveWorktree(ctx, dir, git.RemoveWorktreeOptions{}); !errors.As(err, &mainErr) {
		t.Fatalf("removing the main checkout: err = %v, want *git.ErrMainWorktree", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatalf("the main checkout was damaged: %v", err)
	}

	for _, path := range []string{foreign, filepath.Join(home, "never-existed")} {
		var notAWorktree *git.ErrNotAWorktree
		if _, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{}); !errors.As(err, &notAWorktree) {
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

	_, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{})
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
	if _, err := repo.RemoveWorktree(ctx, path, git.RemoveWorktreeOptions{}); !errors.As(err, &unreadable) {
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
