package local

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/spawn"
)

// Criterion 6: for every git invocation these operations make, a test where
// that invocation fails. The fake git resolves the question the failures
// share — "who noticed" — because each case has to fail the invocation it is
// about WHILE the ones before it succeed; a real repository cannot be made to
// refuse its sixth git call and not its first.
//
// The paired success is the acceptance file beside this one, which runs the
// same calls against real git in t.TempDir(). The pair matters: a fake that
// always failed would prove these operations report SOME error, never that
// they report the right one.
//
// Every assertion has the same two halves, and they are the point: the call
// must not succeed (a failure that looks like a success is how a caller ends
// up believing in a worktree nobody made), and it must not answer a DOMAIN
// refusal (ErrBaseUnresolved, ErrBranchHasCommits …), because those are read
// as facts about the repository — "your base is not a commit" — when the
// truth is that git could not be run.

// withFakeWorktree is the environment every case below shares: a listing that
// names the main checkout and a linked worktree, BOTH real directories. The
// state read runs with the worktree's own path as the git child's cwd, so a
// fictional path would fail every read with "the directory is gone" — the
// reason one test below chooses on purpose and the others must not inherit by
// accident. It returns the linked worktree's path for the case to act on.
func withFakeWorktree(t *testing.T, behaviors map[string]string) (env []string, wtPath string) {
	t.Helper()
	wtPath = t.TempDir()
	return fakeGitEnv(t, mergeBehaviors(map[string]string{
		"FAKE_WORKTREE_MAIN": t.TempDir(),
		"FAKE_WORKTREE_PATH": wtPath,
	}, behaviors)), wtPath
}

// TestAddWorktreeReportsAFailedInvocation drives each invocation AddWorktree
// makes to failure in turn.
func TestAddWorktreeReportsAFailedInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake map[string]string
		what string
	}{
		{
			name: "the worktree listing",
			fake: map[string]string{"FAKE_FAIL": "worktree list"},
			what: "AddWorktree reads the listing first: the branch may already be checked out",
		},
		{
			name: "the branch tip read",
			fake: map[string]string{"FAKE_FAIL": "rev-parse --verify --quiet refs/heads/worker-1"},
			what: "the branch's existence decides which form of the add runs",
		},
		{
			name: "the base read, on the path that creates the branch",
			fake: map[string]string{
				"FAKE_BRANCH_TIP": "fail",
				"FAKE_FAIL":       "rev-parse --verify --quiet master^{commit}",
			},
			what: "a base that does not RESOLVE is a refusal; a base that could not be READ is not",
		},
		{
			name: "the worktree add, checking out an existing branch",
			fake: map[string]string{"FAKE_FAIL": "worktree add"},
			what: "the mutation fails, so nothing may be reported as placed",
		},
		{
			name: "the worktree add, creating the branch",
			fake: map[string]string{
				"FAKE_BRANCH_TIP": "fail",
				"FAKE_FAIL":       "worktree add",
			},
			what: "the same mutation on the creating path, where git may have written the ref first",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, wtPath := withFakeWorktree(t, tc.fake)
			repo := openRepo(t, env, t.TempDir())

			res, err := repo.AddWorktree(context.Background(), "worker-1", "master", wtPath)
			if err == nil {
				t.Fatalf("AddWorktree succeeded (Created=%v) with a failed invocation: %s", res.Created, tc.what)
			}
			if refusal := domainRefusal(err); refusal != "" {
				t.Fatalf("err = %v, a %s — a failed invocation must not be reported as a fact about the repository: %s", err, refusal, tc.what)
			}
		})
	}
}

// TestAddWorktreeRemovesTheBranchItCreatedWhenTheAddFails: the interval's
// fallback. git can create the branch and then fail to prepare the worktree,
// so the branch is removed by the same narrow rule the caller's compensation
// uses. The fake makes the tip appear between the two reads
// ("absent_then_present"), which is exactly the state git leaves behind; the
// count then says the branch cannot be deleted, and BOTH failures must reach
// the caller — the primary one, and the branch that is still there.
func TestAddWorktreeRemovesTheBranchItCreatedWhenTheAddFails(t *testing.T) {
	env, wtPath := withFakeWorktree(t, map[string]string{
		"FAKE_BRANCH_TIP":    "absent_then_present",
		"FAKE_FAIL":          "worktree add",
		"FAKE_REVLIST_COUNT": "1",
	})
	repo := openRepo(t, env, t.TempDir())

	_, err := repo.AddWorktree(context.Background(), "worker-1", "master", wtPath)
	if err == nil {
		t.Fatal("AddWorktree succeeded with the add failed and a branch left behind")
	}
	var beyond *git.ErrBranchHasCommits
	if !errors.As(err, &beyond) {
		t.Fatalf("err = %v, want the cleanup failure (ErrBranchHasCommits) among its causes", err)
	}
	if !strings.Contains(err.Error(), "worktree add") {
		t.Errorf("err = %v, want the failed invocation named as well: the caller must see both facts", err)
	}
}

// TestAddWorktreeSucceedsOnTheFakeWhereTheInvocationSucceeds is the other half
// of the pair inside the fake's own territory: with nothing made to fail, the
// creating form answers Created. It is here because every case above asserts
// an error, and an operation that failed unconditionally would satisfy all of
// them.
func TestAddWorktreeSucceedsOnTheFakeWhereTheInvocationSucceeds(t *testing.T) {
	env, wtPath := withFakeWorktree(t, map[string]string{"FAKE_BRANCH_TIP": "fail"})
	repo := openRepo(t, env, t.TempDir())

	res, err := repo.AddWorktree(context.Background(), "worker-1", "master", wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Error("Created = false, want true — the fake reports no such branch, so this call created it")
	}
}

// TestWorktreesReportsAFailedInvocation: the listing and the base read are
// hard failures — the call cannot answer at all — while a per-worktree read
// that fails is the domain state WorktreeUnreadable, because the listing
// still names the worktrees and the caller must be able to see them.
func TestWorktreesReportsAFailedInvocation(t *testing.T) {
	t.Run("the worktree listing", func(t *testing.T) {
		env, _ := withFakeWorktree(t, map[string]string{"FAKE_FAIL": "worktree list"})
		repo := openRepo(t, env, t.TempDir())
		list, err := repo.Worktrees(context.Background(), "master")
		if err == nil {
			t.Fatalf("Worktrees answered %+v with the listing failed", list)
		}
	})

	t.Run("the base read", func(t *testing.T) {
		env, _ := withFakeWorktree(t, map[string]string{"FAKE_FAIL": "rev-parse --verify --quiet master^{commit}"})
		repo := openRepo(t, env, t.TempDir())
		list, err := repo.Worktrees(context.Background(), "master")
		if err == nil {
			t.Fatalf("Worktrees answered %+v with the base read failed", list)
		}
		if refusal := domainRefusal(err); refusal != "" {
			t.Fatalf("err = %v, a %s — a base that could not be READ is not a base that does not exist", err, refusal)
		}
	})

	t.Run("the commit count of one worktree", func(t *testing.T) {
		env, wtPath := withFakeWorktree(t, map[string]string{"FAKE_FAIL": "rev-list --count"})
		repo := openRepo(t, env, t.TempDir())
		list, err := repo.Worktrees(context.Background(), "master")
		if err != nil {
			t.Fatalf("Worktrees failed outright: %v — the listing was readable, so its entries are still the answer", err)
		}
		wt, ok := worktreeAt(list, wtPath)
		if !ok {
			t.Fatalf("no entry for %s in %+v", wtPath, list)
		}
		if wt.State != git.WorktreeUnreadable || wt.Reason == "" {
			t.Fatalf("entry = %+v, want WorktreeUnreadable with a reason", wt)
		}
	})

	t.Run("the status of one worktree", func(t *testing.T) {
		env, wtPath := withFakeWorktree(t, map[string]string{"FAKE_FAIL": "status --porcelain=v2"})
		repo := openRepo(t, env, t.TempDir())
		list, err := repo.Worktrees(context.Background(), "master")
		if err != nil {
			t.Fatalf("Worktrees failed outright: %v", err)
		}
		wt, ok := worktreeAt(list, wtPath)
		if !ok {
			t.Fatalf("no entry for %s in %+v", wtPath, list)
		}
		if wt.State != git.WorktreeUnreadable {
			t.Fatalf("entry = %+v, want WorktreeUnreadable — an unread state is never clean", wt)
		}
		if wt.Uncommitted || wt.Ahead != 0 {
			t.Errorf("an unreadable entry carries facts it could not have read: %+v", wt)
		}
	})
}

// TestRemoveWorktreeReportsAFailedInvocation: the listing, the state read and
// the removal itself.
func TestRemoveWorktreeReportsAFailedInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake map[string]string
		// wantUnreadable is the one case whose refusal is a domain state
		// rather than an invocation error: a state that could not be read is
		// not a state that is clean, and the caller must be told which.
		wantUnreadable bool
	}{
		{name: "the worktree listing", fake: map[string]string{"FAKE_FAIL": "worktree list"}},
		{
			name: "the state read",
			fake: map[string]string{"FAKE_FAIL": "status --porcelain=v2"},
			// The state could not be read, so the worktree is unreadable —
			// and that refusal comes from the same place Worktrees' does.
			wantUnreadable: true,
		},
		{name: "the removal itself", fake: map[string]string{"FAKE_FAIL": "worktree remove"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, wtPath := withFakeWorktree(t, tc.fake)
			repo := openRepo(t, env, t.TempDir())
			err := repo.RemoveWorktree(context.Background(), wtPath)
			if err == nil {
				t.Fatal("RemoveWorktree succeeded with a failed invocation")
			}
			var unreadable *git.ErrWorktreeUnreadable
			if got := errors.As(err, &unreadable); got != tc.wantUnreadable {
				t.Fatalf("err = %v (unreadable=%v), want unreadable=%v", err, got, tc.wantUnreadable)
			}
			if refusal := domainRefusal(err); refusal != "" && !tc.wantUnreadable {
				t.Fatalf("err = %v, a %s — a failed invocation must not read as a fact about the repository", err, refusal)
			}
		})
	}
}

// TestDeleteWorktreeBranchReportsAFailedInvocation: the listing, both
// rev-parse reads (which the prefix form of FAKE_FAIL can tell apart), the
// count and the guarded delete.
func TestDeleteWorktreeBranchReportsAFailedInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake map[string]string
	}{
		{
			name: "the worktree listing",
			fake: map[string]string{"FAKE_FAIL": "worktree list"},
		},
		{
			name: "the base read",
			fake: map[string]string{"FAKE_FAIL": "rev-parse --verify --quiet master^{commit}"},
		},
		{
			name: "the branch tip read",
			fake: map[string]string{"FAKE_FAIL": "rev-parse --verify --quiet refs/heads/worker-1"},
		},
		{
			name: "the commit count",
			fake: map[string]string{"FAKE_FAIL": "rev-list --count"},
		},
		{
			name: "the guarded delete",
			fake: map[string]string{"FAKE_FAIL": "update-ref -d"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := withFakeWorktree(t, tc.fake)
			repo := openRepo(t, env, t.TempDir())

			err := repo.DeleteWorktreeBranch(context.Background(), "worker-1", "master")
			if err == nil {
				t.Fatal("DeleteWorktreeBranch succeeded with a failed invocation")
			}
			if refusal := domainRefusal(err); refusal != "" {
				t.Fatalf("err = %v, a %s — a failed invocation must not read as a fact about the repository", err, refusal)
			}
		})
	}
}

// TestWorktreeOperationsReportAVanishedGit is the cheap shape the rest of this
// suite uses for "the invocation could not be made at all": the binary is
// gone, so EVERY one of these operations must fail — including the ones whose
// first act is a read. It is the companion to the per-invocation cases above,
// which can only fail an invocation the fake can recognize.
func TestWorktreeOperationsReportAVanishedGit(t *testing.T) {
	env, wtPath := withFakeWorktree(t, nil)
	repo := openRepo(t, env, t.TempDir())
	r, ok := repo.(*Repo)
	if !ok {
		t.Fatalf("openRepo returned %T, want *Repo", repo)
	}
	r.gitPath = "/nonexistent/git"

	ctx := context.Background()
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", "/tmp/fake/wt2"); err == nil {
		t.Error("AddWorktree succeeded with no git to run")
	}
	if _, err := repo.Worktrees(ctx, "master"); err == nil {
		t.Error("Worktrees succeeded with no git to run")
	}
	if err := repo.RemoveWorktree(ctx, wtPath); err == nil {
		t.Error("RemoveWorktree succeeded with no git to run")
	}
	if err := repo.DeleteWorktreeBranch(ctx, "worker-1", "master"); err == nil {
		t.Error("DeleteWorktreeBranch succeeded with no git to run")
	}
}

// TestWorktreeInvocationArgv pins the two things about these invocations a
// real repository cannot show: the reads take no optional locks, and the path
// is placed after -- so that a path beginning with '-' is a path and not an
// option. The fake records argv, which is the only place either is visible.
func TestWorktreeInvocationArgv(t *testing.T) {
	const oid = "8f987d98fcd910d9aaa66d5769bda44bab3db702"
	env, wtPath := withFakeWorktree(t, map[string]string{
		// The branch is absent for Add's read — which is what drives the
		// branch-creating form — and present for the delete's read, which is
		// the only way one fake serves both operations in one test.
		"FAKE_BRANCH_TIP": "absent_then_present",
		"FAKE_OID":        oid,
	})
	repo := openRepo(t, env, t.TempDir())
	ctx := context.Background()
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", wtPath); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveWorktree(ctx, wtPath); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteWorktreeBranch(ctx, "worker-1", "master"); err != nil {
		t.Fatal(err)
	}

	calls := fakeGitLog(t, env)
	assertArgv := func(want ...string) {
		t.Helper()
		joined := strings.Join(want, " ")
		for _, call := range calls {
			if strings.Join(call, " ") == joined {
				return
			}
		}
		t.Errorf("no invocation %q among %v", joined, calls)
	}
	assertArgv("--no-optional-locks", "worktree", "list", "--porcelain", "-z")
	assertArgv("worktree", "add", "-b", "worker-1", "--", wtPath, "master")
	assertArgv("worktree", "remove", "--", wtPath)
	assertArgv("--no-optional-locks", "rev-list", "--count", oid+".."+oid)
	assertArgv("update-ref", "-d", "refs/heads/worker-1", oid)

	// And the operations make no DIFF invocation at all — the claim the
	// hostile_config_test.go case about a configured external diff program
	// rests on, and one argv can prove and a passing config case cannot:
	// measured on git 2.55, `diff --numstat` never invokes diff.external, so
	// that case stays green even if the worktree state read grew the panel's
	// line counts. This assertion is what fails when it does, because the
	// counts are two `git diff` invocations per worktree (attachCounts).
	for _, call := range calls {
		if subcommand(call) == "diff" {
			t.Errorf("a worktree read ran %v: the state read deliberately takes no line counts", call)
		}
	}
}

// subcommand is the git subcommand of a recorded invocation: the first
// argument that is not a git-level option, which is where the fake's own
// dispatch looks for it too.
func subcommand(call []string) string {
	for _, arg := range call {
		if !strings.HasPrefix(arg, "--") {
			return arg
		}
	}
	return ""
}

// TestWorktreeListingEncodingFollowsTheGitVersion: -z is what makes a
// worktree path unambiguous, and it arrived in git 2.36 while this package's
// floor is 2.25 — so WHICH form is asked for is a fact about the git that is
// running, decided once at open where the version is known. Both directions
// are asserted on the argv, because on a repository whose paths are ordinary
// the two encodings produce the same answer and only the argv tells them
// apart. The fake answers each form the way git does, so the read is exercised
// in both too.
func TestWorktreeListingEncodingFollowsTheGitVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		nul     bool
	}{
		{version: "2.55.0", nul: true},
		{version: "2.36.0", nul: true}, // the version that added -z
		{version: "2.35.3", nul: false},
		{version: "2.25.0", nul: false}, // the floor itself
	} {
		t.Run(tc.version, func(t *testing.T) {
			env, wtPath := withFakeWorktree(t, map[string]string{"FAKE_GIT_VERSION": tc.version})
			repo := openRepo(t, env, t.TempDir())

			list, err := repo.Worktrees(context.Background(), "master")
			if err != nil {
				t.Fatalf("Worktrees: %v", err)
			}
			if wt, ok := worktreeAt(list, wtPath); !ok || wt.Branch != "wt" {
				t.Fatalf("the listing was not read in the encoding that was asked for: %+v", list)
			}

			var listing []string
			for _, call := range fakeGitLog(t, env) {
				if len(call) > 2 && call[1] == "worktree" && call[2] == "list" {
					listing = call
					break
				}
			}
			if listing == nil {
				t.Fatal("no worktree listing invocation was recorded")
			}
			if hasZ := slices.Contains(listing, "-z"); hasZ != tc.nul {
				t.Errorf("listing argv = %v (contains -z: %v), want -z: %v for git %s", listing, hasZ, tc.nul, tc.version)
			}
		})
	}
}

// TestWorktreeListHasNULIsTheVersionBoundary pins the decision itself, at the
// version that introduced -z. The case above drives the same answer through a
// repository; this one is here for the two edges a repository cannot show: the
// version immediately below 2.36, and a version that cannot be read at all —
// which the factory refuses before a Repo exists (belowFloor), so the helper's
// conservative answer is what keeps this total if that ever changes.
func TestWorktreeListHasNULIsTheVersionBoundary(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"git version 2.55.0", true},
		{"2.36.0", true},
		{"2.36", true},
		{"2.35.3", false},
		{"2.25.0", false},
		{"1.9.0", false},
		{"3.0.0", true},
		{"not a version", false},
		{"", false},
	} {
		if got := worktreeListHasNUL(tc.version); got != tc.want {
			t.Errorf("worktreeListHasNUL(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestWorktreeListingEncodingsAgreeOnRealGit reads one real repository BOTH
// ways and requires the two encodings to describe it identically — record for
// record, including a worktree whose PATH CONTAINS A NEWLINE, which is the
// case the fallback encoding has to work around.
//
// This is the half of the fallback the fake cannot give: the fake answers a
// fixture, and this answers real git. Which encoding the seam ASKS FOR is a
// separate fact, asserted on argv in TestWorktreeListingEncodingFollowsTheGitVersion
// (the fake records it), so the two tests together cover the version gate and
// the fallback parser rather than one of them covering both vacuously.
func TestWorktreeListingEncodingsAgreeOnRealGit(t *testing.T) {
	dir, home := worktreeRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	ctx := context.Background()
	linked := filepath.Join(home, "wt-linked")
	detached := filepath.Join(home, "wt-detached")
	newline := filepath.Join(home, "wt\nnl")
	if _, err := repo.AddWorktree(ctx, "worker-1", "master", linked); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "worktree", "add", "--detach", detached, "master").Run(); err != nil {
		t.Fatal(err)
	}
	if err := commandIn(dir, "worktree", "add", "-b", "nl", newline, "master").Run(); err != nil {
		t.Fatal(err)
	}

	nul := worktreeListingBytes(t, dir, spawn.WorktreeListArgs())
	lines := worktreeListingBytes(t, dir, spawn.WorktreeListLinesArgs())
	fromNUL, err := spawn.ParseWorktreeList(nul)
	if err != nil {
		t.Fatalf("the NUL listing: %v", err)
	}
	fromLines, err := spawn.ParseWorktreeListLines(lines)
	if err != nil {
		t.Fatalf("the line listing: %v", err)
	}

	if len(fromNUL) != 4 {
		t.Fatalf("records = %d, want 4 (main, linked, detached, newline): %+v", len(fromNUL), fromNUL)
	}
	if len(fromLines) != len(fromNUL) {
		t.Fatalf("the line encoding read %d records and the NUL encoding %d:\nnul:   %+v\nlines: %+v",
			len(fromLines), len(fromNUL), fromNUL, fromLines)
	}
	for i := range fromNUL {
		if fromLines[i] != fromNUL[i] {
			t.Errorf("record %d differs between the encodings:\nnul:   %+v\nlines: %+v", i, fromNUL[i], fromLines[i])
		}
	}
	if fromNUL[0].Path != dir || fromNUL[0].Branch != "master" {
		t.Errorf("first record = %+v, want the main checkout on master", fromNUL[0])
	}
	var foundNewline bool
	for _, rec := range fromNUL {
		switch rec.Path {
		case newline:
			foundNewline = true
			if rec.Branch != "nl" {
				t.Errorf("the newline worktree = %+v, want branch nl", rec)
			}
		case detached:
			if !rec.Detached || rec.Branch != "" {
				t.Errorf("the detached worktree = %+v, want Detached with no branch", rec)
			}
		}
	}
	if !foundNewline {
		t.Errorf("no record for %q in %+v — the path's newline was not preserved", newline, fromNUL)
	}
}

// worktreeListingBytes runs one listing invocation against real git and returns
// its stdout VERBATIM: the encodings differ in their terminators, so trimming
// the bytes before parsing them would test neither.
func worktreeListingBytes(t *testing.T, dir string, argv []string) []byte {
	t.Helper()
	cmd := commandIn(dir, argv...)
	cmd.Env = gitEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", argv, err)
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────────

// mergeBehaviors layers one behaviour map over another, the specific one
// winning: the call site sets what the fake must be, and each case overrides
// the knob it is about.
func mergeBehaviors(base, over map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range over {
		merged[k] = v
	}
	return merged
}

// worktreeAt finds one entry of a listing by path.
func worktreeAt(list []git.Worktree, path string) (git.Worktree, bool) {
	for _, wt := range list {
		if wt.Path == path {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

// domainRefusal names the git DOMAIN refusal an error is, or "" when it is not
// one: those are answers about the repository, and a failure to run git must
// never be reported as one.
func domainRefusal(err error) string {
	switch {
	case errors.As(err, new(*git.ErrBaseUnresolved)):
		return "ErrBaseUnresolved"
	case errors.As(err, new(*git.ErrBranchCheckedOut)):
		return "ErrBranchCheckedOut"
	case errors.As(err, new(*git.ErrBranchHasCommits)):
		return "ErrBranchHasCommits"
	case errors.As(err, new(*git.ErrWorktreePathNotEmpty)):
		return "ErrWorktreePathNotEmpty"
	case errors.As(err, new(*git.ErrWorktreeUncommitted)):
		return "ErrWorktreeUncommitted"
	case errors.As(err, new(*git.ErrMainWorktree)):
		return "ErrMainWorktree"
	case errors.As(err, new(*git.ErrNotAWorktree)):
		return "ErrNotAWorktree"
	}
	return ""
}

// TestAddWorktreeRemovesTheBranchItCreatedWhenTheCallersDeadlineIsSpent is the
// failure this pair was missing, and it is the one that reached a user's
// repository (nocx-tlfoj).
//
// The cleanup above runs on the CALLER'S context, and the most common reason
// the add failed at all is that that context ran out: the registrar gives the
// spawn and the enrolment one budget together (internal/workers/registrar.go,
// "covers spawn and enrolment"), and on a machine where `git worktree add`
// does not fit inside it, git creates the branch and then the add is killed.
// Every call discardBranch makes is then refused by the same spent context, so
// the branch the call is returning an error about stays in the repository.
//
// It is asserted through the ERROR's text rather than through git, because the
// fake is where a spent deadline can be arranged without a race: what the
// caller must never see is the cleanup reporting that it could not run.
func TestAddWorktreeRemovesTheBranchItCreatedWhenTheCallersDeadlineIsSpent(t *testing.T) {
	env, wtPath := withFakeWorktree(t, map[string]string{
		"FAKE_BRANCH_TIP": "absent_then_present",
		"FAKE_WORKTREE":   "slow",
	})
	repo := openRepo(t, env, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := repo.AddWorktree(ctx, "worker-1", "master", wtPath)
	if err == nil {
		t.Fatal("AddWorktree succeeded with an add that outlived the caller's budget")
	}
	if strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("err = %v\nwant the branch removed anyway: a cleanup bounded by the deadline that killed the work it cleans up can never run, and leaves the branch in the repository", err)
	}
}
