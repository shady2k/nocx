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
	"github.com/shady2k/nocx/internal/waittest"
)

// diffRepo builds a repo with one committed file, then one staged change and
// one unstaged change and one untracked file.
func diffRepo(t *testing.T) (dir string) {
	t.Helper()
	dir = newGitRepo(t)
	gitWrite(t, dir, "tracked.txt", "one\n")
	gitCommit(t, dir, "one")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("brand new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDiffUnstagedRealGit(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)

	d, err := repo.Diff(context.Background(), "tracked.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffOK {
		t.Fatalf("State = %s", d.State)
	}
	if !strings.Contains(d.Text, "+changed") {
		t.Fatalf("diff text missing the change: %q", d.Text)
	}
	if d.Truncated {
		t.Fatal("unbounded diff reported truncated")
	}
}

func TestDiffStagedRealGit(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	if _, err := repo.Stage(context.Background(), []string{"tracked.txt"}); err != nil {
		t.Fatal(err)
	}

	d, err := repo.Diff(context.Background(), "tracked.txt", git.SideStaged, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffOK {
		t.Fatalf("State = %s", d.State)
	}
	if !strings.Contains(d.Text, "+changed") {
		t.Fatalf("diff text missing the change: %q", d.Text)
	}
}

// TestDiffUntrackedRealGit: --no-index against /dev/null is git's own answer
// for a file with nothing to diff against — and it exits 1 when there are
// differences, which is data, not an error.
func TestDiffUntrackedRealGit(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)

	d, err := repo.Diff(context.Background(), "new.txt", git.SideUntracked, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffOK {
		t.Fatalf("State = %s", d.State)
	}
	if !strings.Contains(d.Text, "+brand new") {
		t.Fatalf("diff text missing the content: %q", d.Text)
	}
}

func TestDiffEmptyRealGit(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "same")
	gitCommit(t, dir, "one")
	repo := openRepo(t, gitEnv(t), dir)

	// No changes: the diff is empty, and "empty" is a state, not an error.
	d, err := repo.Diff(context.Background(), "f.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffEmpty {
		t.Fatalf("State = %s, want empty", d.State)
	}
}

func TestDiffBinaryRealGit(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "v1")
	gitCommit(t, dir, "one")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte{0x00, 0x01, 0x02, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t, gitEnv(t), dir)

	d, err := repo.Diff(context.Background(), "f.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffBinary {
		t.Fatalf("State = %s, want binary", d.State)
	}
}

// TestDiffTooLarge: at maxBytes the child is killed and reaped, the result
// is tooLarge with a prefix — and it carries no cut flag of its own; the cut
// stays in local's private execution record.
func TestDiffTooLarge(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)

	d, err := repo.Diff(context.Background(), "tracked.txt", git.SideUnstaged, 16)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffTooLarge {
		t.Fatalf("State = %s, want tooLarge", d.State)
	}
	if !d.Truncated {
		t.Fatal("Truncated not set for a bounded diff")
	}
	if len(d.Text) > 16 {
		t.Fatalf("retained %d bytes, over the 16-byte bound", len(d.Text))
	}
}

func TestDiffGoneUntrackedRealGit(t *testing.T) {
	dir := diffRepo(t)
	repo := openRepo(t, gitEnv(t), dir)

	// The untracked file vanishes between the status and the click.
	if err := os.Remove(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	d, err := repo.Diff(context.Background(), "new.txt", git.SideUntracked, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != git.DiffGone {
		t.Fatalf("State = %s, want gone", d.State)
	}
}

func TestDiffContextCancelled(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	env := fakeGitEnv(t, map[string]string{"FAKE_DIFF": "sleep", "FAKE_STARTED_FILE": marker})
	repo := openRepo(t, env, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := repo.Diff(ctx, "f.txt", git.SideUnstaged, 1<<20)
		done <- err
	}()
	waittest.WaitFor(t, "fake diff to start", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Diff returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Diff did not return after cancellation")
	}
}

// TestDiffChildIgnoresTERM: the escalation reaches KILL and the read sees
// EOF — the diff child ignores INT and TERM, and only KILL ends it.
func TestDiffChildIgnoresTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	env := fakeGitEnv(t, map[string]string{"FAKE_DIFF": "sleep_stubborn", "FAKE_STARTED_FILE": marker})
	repo := openRepo(t, env, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := repo.Diff(ctx, "f.txt", git.SideUnstaged, 1<<20)
		done <- err
	}()
	waittest.WaitFor(t, "fake diff to start", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Diff returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Diff did not return after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("escalation took %s, KILL never landed", elapsed)
	}
}

func TestDiffBadSide(t *testing.T) {
	dir := newGitRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	if _, err := repo.Diff(context.Background(), "f.txt", git.Side("bogus"), 100); err == nil {
		t.Fatal("unknown side accepted")
	}
}

func TestDiffZeroBound(t *testing.T) {
	dir := newGitRepo(t)
	repo := openRepo(t, gitEnv(t), dir)
	if _, err := repo.Diff(context.Background(), "f.txt", git.SideUnstaged, 0); err == nil {
		t.Fatal("zero byte bound accepted")
	}
}

// TestDiffFormsFail: each of the three invocations failing is an error, not
// a domain state — the diff state table has no "failed" row, so the failure
// must surface as an error the transport can toast.
func TestDiffFormsFail(t *testing.T) {
	for _, side := range []git.Side{git.SideStaged, git.SideUnstaged, git.SideUntracked} {
		t.Run(string(side), func(t *testing.T) {
			env := fakeGitEnv(t, map[string]string{"FAKE_DIFF": "fail"})
			repo := openRepo(t, env, t.TempDir())
			_, err := repo.Diff(context.Background(), "f.txt", side, 1<<20)
			if err == nil || !strings.Contains(err.Error(), "bad config") {
				t.Fatalf("Diff(%s) returned %v, want the git error", side, err)
			}
		})
	}
}
