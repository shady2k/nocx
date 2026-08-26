package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/testwait"
)

// TestStatusUnbornRealGit: an unborn branch with a staged file, through the
// real git.
func TestStatusUnbornRealGit(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "hi")
	repo := openRepo(t, gitEnv(t), dir)

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Unborn {
		t.Fatalf("Unborn = false: %s", summary(st))
	}
	if st.Head != "" {
		t.Fatalf("Head = %q on unborn", st.Head)
	}
	if len(st.Staged) != 1 || st.Staged[0].Path != "f.txt" {
		t.Fatalf("Staged = %+v", st.Staged)
	}
	if st.Completeness != git.CompletenessComplete {
		t.Fatalf("Completeness = %s", st.Completeness)
	}
}

func TestStatusBothListsRealGit(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "v1")
	gitCommit(t, dir, "one")
	gitWrite(t, dir, "f.txt", "v2")                              // staged
	if err := writeFile(dir+"/f.txt", "v3", 0o644); err != nil { // then modified again
		t.Fatal(err)
	}
	repo := openRepo(t, gitEnv(t), dir)

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Staged) != 1 || len(st.Unstaged) != 1 {
		t.Fatalf("one file must land in both lists: %s", summary(st))
	}
	if st.Staged[0].Path != "f.txt" || st.Unstaged[0].Path != "f.txt" {
		t.Fatalf("paths = %+v / %+v", st.Staged, st.Unstaged)
	}
	if st.Total != 1 {
		t.Fatalf("Total = %d, want 1 record", st.Total)
	}
}

func TestStatusUpstreamRealGit(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "hi")
	gitCommit(t, dir, "one")
	// Give the branch an upstream by cloning and pushing.
	bare := t.TempDir()
	cmd := commandIn(dir, "clone", "--bare", dir, bare)
	cmd.Env = gitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	cmd = commandIn(dir, "remote", "add", "origin", bare)
	cmd.Env = gitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v: %s", err, out)
	}
	cmd = commandIn(dir, "push", "-u", "origin", "master")
	cmd.Env = gitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("push: %v: %s", err, out)
	}
	// One local commit ahead.
	gitWrite(t, dir, "g.txt", "more")
	gitCommit(t, dir, "two")

	repo := openRepo(t, gitEnv(t), dir)
	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Upstream != "origin/master" {
		t.Fatalf("Upstream = %q", st.Upstream)
	}
	if st.Ahead != 1 || st.Behind != 0 {
		t.Fatalf("Ahead=%d Behind=%d, want 1, 0", st.Ahead, st.Behind)
	}
	if len(st.Staged) != 0 || len(st.Unstaged) != 0 {
		t.Fatalf("clean tree must be empty: %s", summary(st))
	}
}

// TestStatusCapped: more records than the retention cap — the lists hold a
// prefix, Total is exact, and the discriminator says capped, not cut.
func TestStatusCapped(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "finite"})
	repo := openRepo(t, env, t.TempDir(),
		WithStatusEntries(2), WithStatusCeilings(1<<20, time.Minute))

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Completeness != git.CompletenessCapped {
		t.Fatalf("Completeness = %s, want capped", st.Completeness)
	}
	if len(st.Staged) != 2 {
		t.Fatalf("retained %d records, want 2", len(st.Staged))
	}
	if st.Total <= 2 {
		t.Fatalf("Total = %d, must exceed the cap", st.Total)
	}
}

// TestStatusCutBelowRecordCap: the byte ceiling stops the traversal after a
// few records — below the retention cap. The lists hold every record that
// was observed, Total is a lower bound, and the ONE discriminator says cut;
// nothing about the list length says the status is incomplete.
func TestStatusCutBelowRecordCap(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "stream"})
	repo := openRepo(t, env, t.TempDir(),
		WithStatusEntries(5000), WithStatusCeilings(300, time.Minute))

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Completeness != git.CompletenessCut {
		t.Fatalf("Completeness = %s, want cut", st.Completeness)
	}
	if st.Total >= 5000 {
		t.Fatalf("cut must happen below the record cap, Total=%d", st.Total)
	}
	if len(st.Staged) != st.Total {
		t.Fatalf("all observed records must be retained: %d staged, %d total", len(st.Staged), st.Total)
	}
}

// TestStatusCutByWallClock: the wall-clock half of the ceiling — a traversal
// that produces no output at all still ends.
func TestStatusCutByWallClock(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "sleep"})
	repo := openRepo(t, env, t.TempDir(),
		WithStatusCeilings(1<<20, 100*time.Millisecond))

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Completeness != git.CompletenessCut {
		t.Fatalf("Completeness = %s, want cut", st.Completeness)
	}
	if st.Total != 0 {
		t.Fatalf("Total = %d, want 0", st.Total)
	}
}

// TestStatusContextCancelled: a cancelled context is an error, never a
// cut-shaped result.
func TestStatusContextCancelled(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "sleep", "FAKE_STARTED_FILE": marker})
	repo := openRepo(t, env, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := repo.Status(ctx)
		done <- err
	}()
	testwait.WaitFor(t, "fake status to start", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Status returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Status did not return after cancellation")
	}
}

// TestStatusChildIgnoresTERM: the escalation reaches KILL and the read sees
// EOF — the child ignores INT and TERM, so only KILL ends it, and Status
// returns rather than hanging.
func TestStatusChildIgnoresTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "sleep_stubborn", "FAKE_STARTED_FILE": marker})
	repo := openRepo(t, env, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := repo.Status(ctx)
		done <- err
	}()
	testwait.WaitFor(t, "fake status to start", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Status returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Status did not return after cancellation")
	}
	// INT+TERM were ignored (200ms grace each), so KILL must have done the
	// work; the whole run should complete well under 3s and the fake git
	// must be gone.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("escalation took %s, KILL never landed", elapsed)
	}
}

// TestStatusFails: a non-zero exit from git status is a failure (unlike
// diff, status has no data-carrying exit codes).
func TestStatusFails(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "fail"})
	repo := openRepo(t, env, t.TempDir())
	_, err := repo.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "index corrupt") {
		t.Fatalf("Status returned %v, want the git error", err)
	}
}

// TestStatusEmptyListsNeverNil: an empty status marshals as [], never null.
func TestStatusEmptyListsNeverNil(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "hi")
	gitCommit(t, dir, "one")
	repo := openRepo(t, gitEnv(t), dir)

	st, err := repo.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Staged == nil || st.Unstaged == nil || st.Conflicted == nil {
		t.Fatal("empty lists must be [] not nil")
	}
}
