package app

// What a workers.close answers about the checkout it leaves (nocx-xn63t.1.3).
//
// THE OWNER'S DECISION, 2026-09-18: closing a worker never touches its
// checkout — a red merged-tree gate sends work back to the worker, and an
// agent's conversation is keyed to its directory. The close answers what is
// left instead: path, branch, uncommitted, ahead.
//
// The checks run against REAL repositories (git init in a temp dir) through
// the REAL local factory, because what they assert is git's own answer: a
// branch that is still there, a working tree that holds an edit, a count of
// commits. Everything beside git is a double — the session is already gone
// (a finished worker, the ordinary case a close of a worker with a checkout
// takes) and the tab half is a recording map — because those halves have
// their own tests and are not what this bead is about.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

// gitIn runs one git command in dir, hermetically, and answers its output.
// It is initRealRepo's runner lifted to a helper, because the checks here
// need git AFTER the setup too — a branch that is still listed, commits
// made in the checkout.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // literal git verbs over this test's own temp repository
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// goneSessions is every finished worker's registry: the session is already
// out of it, which is the branch of Close a close of a worker with a
// checkout ordinarily takes (the worker finished, then the coordinator
// tidied up).
type goneSessions struct{}

func (goneSessions) Get(session.ID) (session.Session, error) { return nil, errors.New("gone") }

func (goneSessions) EndSession(session.ID) error { return nil }

// closingTabs records the one tab write a close makes, so a test can see the
// close's own half ran before the answer was read.
type closingTabs struct{ deleted []string }

func (c *closingTabs) DeleteTab(_ context.Context, id string, _ content.Replacement) error {
	c.deleted = append(c.deleted, id)
	return nil
}

// closeWorktreeStand is a workerCloser whose git seam is the REAL local
// factory, over a real linked checkout of the real repository at repoDir.
// It answers the checkout path it created, the way a spawn would have.
type closeWorktreeStand struct {
	closer *workerCloser
	layout *closingTabs
}

func newCloseWorktreeStand(t *testing.T, repoDir, head string) (*closeWorktreeStand, string) {
	t.Helper()
	return newCloseWorktreeStandAt(t, repoDir, head, filepath.Join(t.TempDir(), "wt-feat"))
}

// newCloseWorktreeStandAt is the stand at a checkout path the test picks —
// the symlink stand hands a path through a symlinked ancestor, the macOS
// shape: the record holds that spelling, and git answers the resolved one.
func newCloseWorktreeStandAt(t *testing.T, repoDir, head, wtPath string) (*closeWorktreeStand, string) {
	t.Helper()
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)
	gitIn(t, repoDir, "worktree", "add", "-b", "feat/x", wtPath, head)
	tabs := newWorkerTabs()
	tabs.record("p-1", "tab-1")
	layout := &closingTabs{}
	_, lg := logtest.New(t)
	return &closeWorktreeStand{
		closer: &workerCloser{
			sessions: goneSessions{}, layout: layout, tabs: tabs,
			repos: factory, log: lg,
		},
		layout: layout,
	}, wtPath
}

// participant is the record's row for the worker whose checkout these tests
// close over: live, its session already gone, its checkout where the stand
// made it.
func (s *closeWorktreeStand) participant(wtPath, head string) workers.Participant {
	return workers.Participant{
		ID: "p-1", Group: "worker-1", State: workers.StateLive,
		Liveness: workers.Liveness{SessionID: "sess-gone"},
		Worktree: workers.Worktree{Path: wtPath, Branch: "feat/x", Base: head},
	}
}

// Criterion: closing a worktree worker leaves the checkout AND the branch on
// disk, and the answer names path, branch, and — for a tree the worker left
// clean — uncommitted:false and ahead:0 as READ values, not as an absence.
func TestClosingAWorktreeWorkerLeavesTheCheckoutAndSaysWhatItHolds(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)

	res, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if info, statErr := os.Lstat(wtPath); statErr != nil || !info.IsDir() {
		t.Fatalf("the checkout at %q did not survive the close: %v", wtPath, statErr)
	}
	if out := gitIn(t, repoDir, "branch", "--list", "feat/x"); !strings.Contains(out, "feat/x") {
		t.Fatalf("the branch did not survive the close: %q", out)
	}
	want := workers.Leftover{Path: wtPath, Branch: "feat/x", State: workers.CheckoutRead}
	if res.Worktree != want {
		t.Fatalf("worktree = %+v, want %+v", res.Worktree, want)
	}
	if len(stand.layout.deleted) != 1 || stand.layout.deleted[0] != "tab-1" {
		t.Fatalf("the close's tab half = %v, want exactly [tab-1]: the answer rides a close that did its own work",
			stand.layout.deleted)
	}
}

// Criterion: uncommitted is TRUE after the worker left an unstaged edit —
// work no commit keeps, still on disk, named as such.
func TestClosingAWorktreeWorkerSaysTheCheckoutHoldsUncommittedWork(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("seed\nedited\n"), 0o600); err != nil {
		t.Fatalf("leave an edit: %v", err)
	}

	res, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	want := workers.Leftover{Path: wtPath, Branch: "feat/x", State: workers.CheckoutRead, Uncommitted: true}
	if res.Worktree != want {
		t.Fatalf("worktree = %+v, want %+v", res.Worktree, want)
	}
}

// Criterion: ahead is N after N commits — the worker's committed work,
// counted beyond the base the branch started from.
func TestClosingAWorktreeWorkerCountsTheCommitsAheadOfBase(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)
	gitIn(t, wtPath, "commit", "--allow-empty", "-m", "one")
	gitIn(t, wtPath, "commit", "--allow-empty", "-m", "two")

	res, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	want := workers.Leftover{Path: wtPath, Branch: "feat/x", State: workers.CheckoutRead, Ahead: 2}
	if res.Worktree != want {
		t.Fatalf("worktree = %+v, want %+v", res.Worktree, want)
	}
}

// Criterion: git fails while reading the state — here, the checkout is gone
// from disk — and the close STILL SUCCEEDS, answering unknown. Unknown is
// the point of the answer: uncommitted:false over a read that failed would
// read as safe, and nothing about this checkout has been read at all.
func TestClosingAWorktreeWorkerSaysUnknownWhenTheStateCannotBeRead(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatalf("remove the checkout: %v", err)
	}

	res, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head))
	if err != nil {
		t.Fatalf("a checkout whose state git cannot read must not fail the close: %v", err)
	}
	want := workers.Leftover{Path: wtPath, Branch: "feat/x", State: workers.CheckoutUnknown}
	if res.Worktree != want {
		t.Fatalf("worktree = %+v, want %+v", res.Worktree, want)
	}
}

// Criterion: closing a worker spawned WITHOUT a worktree answers nothing
// about one — the zero result, which is what keeps the field off the wire —
// and the git seam is never consulted.
func TestClosingAWorkerWithoutAWorktreeAsksGitNothing(t *testing.T) {
	factory := &scriptedGitFactory{openErr: errors.New("git must not be opened")}
	tabs := newWorkerTabs()
	tabs.record("p-1", "tab-1")
	_, lg := logtest.New(t)
	closer := &workerCloser{
		sessions: goneSessions{}, layout: &closingTabs{}, tabs: tabs,
		repos: factory, log: lg,
	}

	res, err := closer.Close(context.Background(), workers.Participant{
		ID: "p-1", Group: "worker-1", State: workers.StateLive,
		Liveness: workers.Liveness{SessionID: "sess-gone"},
	})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if res != (workers.CloseResult{}) {
		t.Fatalf("result = %+v, want zero: a worker with no checkout answers nothing about one", res)
	}
	if opens := factory.opensSeen(); len(opens) != 0 {
		t.Fatalf("the close opened git at %v, want nothing", opens)
	}
}

// recordingToucher is the last-used record a close stamps: it keeps every
// path it was touched with, and fails every touch when err is set.
type recordingToucher struct {
	touched []string
	err     error
}

func (r *recordingToucher) Touch(_ context.Context, path string, _ time.Time) error {
	r.touched = append(r.touched, path)
	return r.err
}

// Criterion of nocx-xn63t.1.4 carried by the close: closing a worker moves
// its checkout's last-used time, because the sweep of nocx-xn63t.1.6 reads
// that time and a checkout a worker just left is not an unused one.
func TestClosingAWorktreeWorkerMovesItsLastUsedTime(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)
	toucher := &recordingToucher{}
	stand.closer.checkouts = toucher

	if _, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head)); err != nil {
		t.Fatalf("close: %v", err)
	}
	// THE CANONICAL SPELLING, not the one this test happened to hold: the
	// close stamps through the same one owner every checkout comparison uses
	// (nocx-xn63t.1.5's audit), so on a platform whose temp directory is a
	// symlink — macOS — the recorded row and the stamp meet in one form. This
	// assertion said wtPath and was red on macOS for exactly that reason.
	wantTouched := nocxCanonicalPath(wtPath)
	if len(toucher.touched) != 1 || toucher.touched[0] != wantTouched {
		t.Fatalf("touched = %v, want exactly [%s]", toucher.touched, wantTouched)
	}
}

// Paired failure: the record refusing the stamp does not fail a close whose
// real work — the session and the tab — is already done, and the answer
// about the checkout is still git's.
func TestAFailedLastUsedStampDoesNotFailTheClose(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand, wtPath := newCloseWorktreeStand(t, repoDir, head)
	stand.closer.checkouts = &recordingToucher{err: errors.New("database is locked")}

	res, err := stand.closer.Close(context.Background(), stand.participant(wtPath, head))
	if err != nil {
		t.Fatalf("a failed stamp must not fail the close: %v", err)
	}
	if res.Worktree.State != workers.CheckoutRead {
		t.Fatalf("worktree = %+v, want git's reading despite the failed stamp", res.Worktree)
	}
}

// A worker with no checkout stamps nothing.
func TestClosingAWorkerWithoutAWorktreeStampsNothing(t *testing.T) {
	tabs := newWorkerTabs()
	tabs.record("p-1", "tab-1")
	_, lg := logtest.New(t)
	toucher := &recordingToucher{}
	closer := &workerCloser{sessions: goneSessions{}, layout: &closingTabs{}, tabs: tabs, checkouts: toucher, log: lg}

	if _, err := closer.Close(context.Background(), workers.Participant{
		ID: "p-1", Group: "worker-1", State: workers.StateLive,
		Liveness: workers.Liveness{SessionID: "sess-gone"},
	}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(toucher.touched) != 0 {
		t.Fatalf("touched = %v, want nothing", toucher.touched)
	}
}

// Criterion, the macOS shape (nocx-xn63t.1.3 evidence): the checkout sits
// behind a symlinked ancestor, so git answers the RESOLVED spelling of its
// path while the record holds the spelling the spawn was handed. The close
// must still find the checkout in git's listing and answer what it holds —
// read, with its branch — never unknown for a checkout that is right there
// and perfectly readable.
func TestClosingAWorktreeBehindASymlinkedAncestorStillReadsTheCheckout(t *testing.T) {
	repoDir, head := initRealRepo(t)
	real := filepath.Join(t.TempDir(), "checkout-real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatalf("make the checkout's parent: %v", err)
	}
	link := symlinkedDir(t, real)
	checkout := filepath.Join(link, "wt-feat")
	stand, _ := newCloseWorktreeStandAt(t, repoDir, head, checkout)

	// The worker's leftovers, spelled through the link like everything
	// else: an unstaged edit no commit keeps, and two commits beyond the
	// base — the close must read all three answers through the listing.
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("seed\nedited\n"), 0o600); err != nil {
		t.Fatalf("leave an edit: %v", err)
	}
	gitIn(t, checkout, "commit", "--allow-empty", "-m", "one")
	gitIn(t, checkout, "commit", "--allow-empty", "-m", "two")

	res, err := stand.closer.Close(context.Background(), stand.participant(checkout, head))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	want := workers.Leftover{
		Path: checkout, Branch: "feat/x", State: workers.CheckoutRead,
		Uncommitted: true, Ahead: 2,
	}
	if res.Worktree != want {
		t.Fatalf("worktree = %+v, want %+v: the close must read a checkout the record names by its symlinked spelling",
			res.Worktree, want)
	}
}
