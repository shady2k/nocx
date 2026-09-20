package app

// The worktree half of a worker spawn, asserted at the spawner and through
// the real registrar (nocx-xn63t.1.2).
//
// Two stands are used, each for the half it can actually prove:
//
//   - A scripted git.RepoFactory/repo, which answers every seam call a
//     worktree spawn makes and can fail each one on demand. This is where
//     the refusal table lives — no recorded directory, a remote coordinator
//     pane, no repository there, an occupied path, a branch held elsewhere,
//     and one failing case per seam call, paired with the success that runs
//     the same road.
//
//   - The REAL gitlocal factory against real repositories (git init in a
//     temp dir), for the happy path's end state and for the compensation
//     the brief words as an interval: the checkout exists from before the
//     pane opens until either the participant is live or the compensation
//     has removed it — asserted through the real Registrar with a deadline,
//     a launcher whose enrolment never arrives, and `git branch` afterwards.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// expectedWorktreePath is the documented location formula, spelled in the
// test that pins it: <root>/<repo key>/<branch slug>, where <repo key> is
// the main checkout's basename plus "-" and the first 8 hex chars of
// sha256 over the common git dir (the main checkout's .git). The root and
// the key go through the product's own canonical derivations
// (nocxCanonicalPath, nocxCheckoutRepoKey) — what this helper pins is the
// join, not a second derivation of the parts.
func expectedWorktreePath(root, mainCheckout, branch string) string {
	return filepath.Join(nocxCanonicalPath(root),
		nocxCheckoutRepoKey(mainCheckout),
		strings.ReplaceAll(branch, "/", "-"))
}

// symlinkedWorktreeRoot builds the macOS shape on any host: a worktrees
// root whose ancestor is a symlink (on macOS /var → /private/var), so the
// spelling nocx holds — the link — differs from the spelling git answers —
// the resolved target. The link spelling is what a stand hands nocx.
func symlinkedWorktreeRoot(t *testing.T) string {
	t.Helper()
	real := filepath.Join(t.TempDir(), "storage-real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatalf("make the storage root: %v", err)
	}
	link := filepath.Join(t.TempDir(), "storage-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink the storage root: %v", err)
	}
	return filepath.Join(link, "worktrees")
}

// symlinkedDir answers a symlink to dir, for handing nocx the spelling of
// a repository's ancestor a coordinator's pane records on macOS.
func symlinkedDir(t *testing.T, dir string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink %q: %v", dir, err)
	}
	return link
}

// ── the scripted git seam ────────────────────────────────────────────────

// scriptedGitRepo answers the five calls a worktree spawn makes, failing
// each on demand. addCalls records "branch|base|path" in order, which is
// both the what and the WHEN — the checkout must exist before the pane row
// that opens in it.
type scriptedGitRepo struct {
	git.Repo
	mu              sync.Mutex
	trees           []git.Worktree
	treesErr        error
	head            git.Log
	headErr         error
	addCreated      bool
	addErr          error
	addCalls        []string
	removed         []string
	removeErr       error
	deletedBranches [][2]string
	deleteErr       error
	// makeDir has AddWorktree create the directory, the way git would, so a
	// second spawn meets the occupied-path refusal for real.
	makeDir bool
	closed  bool
}

func (r *scriptedGitRepo) Worktrees(context.Context, string) ([]git.Worktree, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trees, r.treesErr
}

func (r *scriptedGitRepo) Log(context.Context, int) (git.Log, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head, r.headErr
}

func (r *scriptedGitRepo) AddWorktree(_ context.Context, branch, base, path string) (git.WorktreeAdded, error) {
	r.mu.Lock()
	r.addCalls = append(r.addCalls, branch+"|"+base+"|"+path)
	makeDir := r.makeDir
	r.mu.Unlock()
	if makeDir {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return git.WorktreeAdded{}, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return git.WorktreeAdded{Created: r.addCreated}, r.addErr
}

func (r *scriptedGitRepo) RemoveWorktree(_ context.Context, path string) error {
	r.mu.Lock()
	r.removed = append(r.removed, path)
	r.mu.Unlock()
	_ = os.RemoveAll(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeErr
}

func (r *scriptedGitRepo) DeleteWorktreeBranch(_ context.Context, branch, base string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedBranches = append(r.deletedBranches, [2]string{branch, base})
	return r.deleteErr
}

func (r *scriptedGitRepo) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *scriptedGitRepo) snapshot() (addCalls, removed []string, deleted [][2]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.addCalls...), append([]string(nil), r.removed...),
		append([][2]string(nil), r.deletedBranches...)
}

// scriptedGitFactory answers Open with its scripted repo and outcome.
type scriptedGitFactory struct {
	repo    *scriptedGitRepo
	state   git.OpenState
	openErr error

	mu    sync.Mutex
	opens []string
}

func (f *scriptedGitFactory) Open(_ context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	f.mu.Lock()
	f.opens = append(f.opens, cwd)
	f.mu.Unlock()
	if f.openErr != nil {
		return nil, git.OpenOutcome{}, f.openErr
	}
	return f.repo, git.OpenOutcome{State: f.state, Toplevel: cwd, GitDir: filepath.Join(cwd, ".git")}, nil
}

func (f *scriptedGitFactory) opensSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

// ── the stand ────────────────────────────────────────────────────────────

// worktreeTabs is the paneMinter double for these tests: pane rows carry
// KINDS (which is the fact the local-only refusal reads), and the first
// CreateTabAfter records what os.Stat said about the pane's own directory
// at the moment the row was minted — the layout's side of the interval.
type worktreeTabs struct {
	mu            sync.Mutex
	cwdOf         map[string]string
	panesOf       map[string][]content.Pane
	tabOf         map[string]string
	tabErr        error
	createErr     error
	paneCwdErrFor map[string]error

	created    []string
	deleted    []string
	firstPane  content.Pane
	dirAtStart *bool
}

func newWorktreeTabs() *worktreeTabs {
	return &worktreeTabs{cwdOf: map[string]string{}, panesOf: map[string][]content.Pane{}, tabOf: map[string]string{}}
}

func (f *worktreeTabs) CreateTabAfter(_ context.Context, tab content.Tab, firstPane content.Pane, _ string) (content.Created[content.NewTab], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return content.Created[content.NewTab]{}, f.createErr
	}
	f.created = append(f.created, tab.ID)
	f.firstPane = firstPane
	if f.dirAtStart == nil {
		_, statErr := os.Lstat(firstPane.Cwd)
		exists := statErr == nil
		f.dirAtStart = &exists
	}
	return content.Created[content.NewTab]{}, nil
}

func (f *worktreeTabs) DeleteTab(_ context.Context, id string, _ content.Replacement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *worktreeTabs) PaneCwd(_ context.Context, paneID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paneCwdErrFor != nil {
		if err, named := f.paneCwdErrFor[paneID]; named {
			return "", err
		}
	}
	return f.cwdOf[paneID], nil
}

func (f *worktreeTabs) TabForPane(_ context.Context, paneID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tabErr != nil {
		return "", f.tabErr
	}
	return f.tabOf[paneID], nil
}

func (f *worktreeTabs) Panes(_ context.Context, tabID string) ([]content.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panesOf[tabID], nil
}

// worktreeStand is a spawner over the scripted seam, wired the way
// production wires it (one factory, one worktree root under the test's own
// temp dir).
type worktreeStand struct {
	reg     *session.Reg
	tabs    *worktreeTabs
	opener  *fakeAxisOpener
	awaiter *fakeAxisAwaiter
	repo    *scriptedGitRepo
	factory *scriptedGitFactory
	spawner *workerSpawner
}

func newWorktreeStand(t *testing.T, repo *scriptedGitRepo) *worktreeStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	tabs := newWorktreeTabs()
	factory := &scriptedGitFactory{repo: repo, state: git.OpenOK}
	awaiter := &fakeAxisAwaiter{}
	awaiter.outcome = transport.IntegrationOutcome{Registered: true, Status: transport.IntegrationIntegrated}
	opener := &fakeAxisOpener{reg: reg}
	spawner := &workerSpawner{
		layout: tabs, opener: opener, sessions: reg,
		integration: awaiter, enrolments: newWorkerEnrolments(logger, reg),
		workspace: "ws-test", repos: factory,
		worktreeRoot: t.TempDir(), log: logger,
	}
	return &worktreeStand{
		reg: reg, tabs: tabs, opener: opener,
		awaiter: awaiter, repo: repo, factory: factory, spawner: spawner,
	}
}

// openWorktreeCoordinator stands a coordinator session in a pane with the
// given kind and recorded directory, the way the layout row would hold it.
func (s *worktreeStand) openWorktreeCoordinator(t *testing.T, paneID, cwd string, kind content.PaneKind) session.ID {
	t.Helper()
	const tabID = "tab-coord"
	s.tabs.cwdOf[paneID] = cwd
	s.tabs.tabOf[paneID] = tabID
	s.tabs.panesOf[tabID] = []content.Pane{{ID: paneID, TabID: tabID, Cwd: cwd, Kind: kind}}
	sess, err := s.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: paneID, Cwd: cwd,
	})
	if err != nil {
		t.Fatalf("open the coordinator's session: %v", err)
	}
	return sess.ID()
}

// scriptedRepoWithMain is the scripted repo's answer for a repository whose
// main checkout is mainPath and whose HEAD is hash.
func scriptedRepoWithMain(mainPath, headHash string, created bool) *scriptedGitRepo {
	return &scriptedGitRepo{
		trees:      []git.Worktree{{Path: mainPath, Main: true, State: git.WorktreeReadable}},
		head:       git.Log{Entries: []git.LogEntry{{Hash: headHash}}},
		addCreated: created,
	}
}

// worktreeOf reads the location off a Spawned the way the record does: the
// optional WorktreeSource, asserted because a Spawned that could not answer
// would silently read as zero here and prove nothing.
func worktreeOf(t *testing.T, s workers.Spawned) workers.Worktree {
	t.Helper()
	src, ok := s.(workers.WorktreeSource)
	if !ok {
		t.Fatalf("%T is not a workers.WorktreeSource", s)
	}
	return src.WorktreeLocation()
}

// anAsk is the ordinary worktree ask: one branch, default base.
func anAsk(branch string) *workers.WorktreeAsk {
	return &workers.WorktreeAsk{Branch: branch}
}

// ── the happy path, scripted ─────────────────────────────────────────────

// Criterion: a worktree spawn creates the checkout BEFORE the pane row that
// opens in it, puts the pane row and the session both in the new checkout,
// and answers the resolved facts — path, branch, and HEAD's own hash as the
// base.
func TestAWorktreeSpawnOpensThePaneInTheNewCheckout(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "head-hash-1", true))
	// The double must behave like git at the one fact this test observes:
	// a successful AddWorktree leaves the checkout ON DISK, so the pane row
	// minted afterwards opens in something that exists.
	stand.repo.makeDir = true
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-wt", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	wantPath := expectedWorktreePath(stand.spawner.worktreeRoot, "/repo", "feat/one")
	if got := stand.tabs.firstPane.Cwd; got != wantPath {
		t.Fatalf("pane row cwd = %q, want the new checkout %q", got, wantPath)
	}
	if got := stand.opener.lastSpec().Cwd; got != wantPath {
		t.Fatalf("session cwd = %q, want the new checkout %q", got, wantPath)
	}
	if stand.tabs.dirAtStart == nil || !*stand.tabs.dirAtStart {
		t.Fatalf("the pane row was minted before the checkout existed (saw %v)",
			stand.tabs.dirAtStart)
	}
	want := workers.Worktree{Path: wantPath, Branch: "feat/one", Base: "head-hash-1"}
	if got := worktreeOf(t, spawned); got != want {
		t.Fatalf("location = %+v, want %+v", got, want)
	}
	addCalls, _, _ := stand.repo.snapshot()
	if len(addCalls) != 1 || addCalls[0] != "feat/one|head-hash-1|"+wantPath {
		t.Fatalf("AddWorktree calls = %v, want exactly [%q]", addCalls, "feat/one|head-hash-1|"+wantPath)
	}
}

// Criterion: a spawn that asks for no worktree is today's spawn — the pane
// opens where the coordinator stood, the git seam is never consulted, and
// nothing claims a checkout.
func TestASpawnWithoutAWorktreeAskOpensWhereItsCoordinatorStood(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "head-hash-1", true))
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-plain", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := stand.opener.lastSpec().Cwd; got != "/repo" {
		t.Fatalf("session cwd = %q, want the coordinator's own", got)
	}
	if got := worktreeOf(t, spawned); got != (workers.Worktree{}) {
		t.Fatalf("location = %+v, want zero", got)
	}
	if opens := stand.factory.opensSeen(); len(opens) != 0 {
		t.Fatalf("the git seam was opened %v", opens)
	}
}

// ── the refusal table (each paired with the success above) ───────────────

// Criterion: no recorded directory on the coordinator pane → named refusal,
// and nothing minted: no pane, no tab, no git.
func TestAWorktreeSpawnWithNoRecordedDirectoryIsRefused(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-nocwd", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, errSpawnWorktreeNoCwd) {
		t.Fatalf("err = %v, want errSpawnWorktreeNoCwd", err)
	}
	if len(stand.tabs.created) != 0 || stand.opener.lastSessionID() != "" {
		t.Fatalf("a refusal minted a pane or tab: %v / %q", stand.tabs.created, stand.opener.lastSessionID())
	}
	if opens := stand.factory.opensSeen(); len(opens) != 0 {
		t.Fatalf("the git seam was opened %v", opens)
	}
}

// Criterion: a coordinator on an ssh pane → named refusal, nothing minted,
// the far machine's path never mistaken for a local repository.
func TestAWorktreeSpawnFromARemotePaneIsRefused(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/srv/deploy", content.PaneSSH)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-ssh", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, errSpawnWorktreeNotLocal) {
		t.Fatalf("err = %v, want errSpawnWorktreeNotLocal", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
	if opens := stand.factory.opensSeen(); len(opens) != 0 {
		t.Fatalf("the git seam was opened %v", opens)
	}
}

// Criterion: a pane whose row cannot be read (in no tab this window knows)
// is refused — local-only is asserted from the row, never guessed.
func TestAWorktreeSpawnWithAnUnreadablePaneRowIsRefused(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)
	stand.tabs.tabErr = errors.New("the row is gone")

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-norow", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, errSpawnWorktreeNoPaneRow) {
		t.Fatalf("err = %v, want errSpawnWorktreeNoPaneRow", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

// Criterion: the cwd is not a repository → named refusal naming the state.
func TestAWorktreeSpawnOutsideARepositoryIsRefused(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.factory.state = git.OpenNotARepository
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/home/nobody", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-norepo", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err == nil || !strings.Contains(err.Error(), "not a repository") {
		t.Fatalf("err = %v, want the not-a-repository refusal", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

// Criterion: a path already occupied — the slug collision — is a named
// refusal with no retry under another name. The first spawn created the
// directory the way git would; the second must stop at the existence check.
func TestAWorktreeSpawnRefusesAnOccupiedPath(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.makeDir = true
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)
	ask := anAsk("feat/one")

	if _, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-first", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: ask,
	}); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	wantPath := expectedWorktreePath(stand.spawner.worktreeRoot, "/repo", "feat/one")

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-second", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: ask,
	})
	if !errors.Is(err, errSpawnWorktreePathExists) || !strings.Contains(err.Error(), wantPath) {
		t.Fatalf("err = %v, want errSpawnWorktreePathExists naming %q", err, wantPath)
	}
	addCalls, _, _ := stand.repo.snapshot()
	if len(addCalls) != 1 {
		t.Fatalf("the second spawn reached AddWorktree: %v", addCalls)
	}
}

// Criterion: the branch is checked out elsewhere → the seam's own named
// refusal, carrying WHERE, and nothing minted.
func TestAWorktreeSpawnRefusesABranchCheckedOutElsewhere(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.addErr = &git.ErrBranchCheckedOut{Branch: "feat/one", Path: "/elsewhere"}
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-held", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	var held *git.ErrBranchCheckedOut
	if !errors.As(err, &held) || held.Path != "/elsewhere" {
		t.Fatalf("err = %v, want git.ErrBranchCheckedOut naming /elsewhere", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

// ── one failing case per seam call, each paired with the success ─────────

func TestAWorktreeSpawnRefusesWhenTheFactoryCannotOpen(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.factory.openErr = errors.New("git is not runnable here")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-open", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, stand.factory.openErr) {
		t.Fatalf("err = %v, want the factory's error", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

func TestAWorktreeSpawnRefusesWhenHEADCannotBeRead(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.headErr = errors.New("the log read was stopped")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-head", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, stand.repo.headErr) {
		t.Fatalf("err = %v, want the HEAD read's error", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

func TestAWorktreeSpawnRefusesWhenTheListingFails(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.treesErr = errors.New("the listing was stopped")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-list", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, stand.repo.treesErr) {
		t.Fatalf("err = %v, want the listing's error", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

func TestAWorktreeSpawnRefusesWhenAddWorktreeFails(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.addErr = errors.New("git refused the checkout")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-add", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if !errors.Is(err, stand.repo.addErr) {
		t.Fatalf("err = %v, want the AddWorktree error", err)
	}
	if len(stand.tabs.created) != 0 {
		t.Fatalf("a refusal minted a tab: %v", stand.tabs.created)
	}
}

// Criterion: the coordinator standing INSIDE a linked worktree does not
// move the key — the main checkout is the seam's Worktrees answer, and the
// common git dir is the MAIN checkout's .git, never the linked worktree the
// coordinator happens to be standing in.
func TestAWorktreeSpawnFromALinkedWorktreeKeysOffTheMainCheckout(t *testing.T) {
	repo := scriptedRepoWithMain("/main-repo", "head-hash-1", true)
	// The linked worktree the coordinator stands in FOLLOWS the main one:
	// git lists the main checkout first whatever this Repo is bound to,
	// which is the whole reason the seam answers Main from position. The
	// previous draft of this fixture put the linked entry first, which real
	// git never does, and the product rightly refused the key rather than
	// guess from a listing that broke its contract.
	repo.trees = append(repo.trees, git.Worktree{
		Path: "/main-repo-linked", Branch: "feat/linked", State: git.WorktreeReadable,
	})
	stand := newWorktreeStand(t, repo)
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/main-repo-linked", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-linked", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	wantPath := expectedWorktreePath(stand.spawner.worktreeRoot, "/main-repo", "feat/one")
	addCalls, _, _ := stand.repo.snapshot()
	if len(addCalls) != 1 || addCalls[0] != "feat/one|head-hash-1|"+wantPath {
		t.Fatalf("AddWorktree calls = %v, want the checkout keyed off the main checkout at %q",
			addCalls, wantPath)
	}
}

// Criterion: a repository with no commits has nothing to branch from.
func TestAWorktreeSpawnRefusesARepositoryWithNoCommits(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.head = git.Log{}
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-empty", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("err = %v, want the no-commits refusal", err)
	}
}

// ── the compensation ─────────────────────────────────────────────────────

// Criterion: a failure AFTER the checkout was created — here, the tab mint
// refusing — removes the checkout and the branch the spawn created, and
// mints no pane for a checkout that no longer exists.
func TestAFailedTabMintRemovesTheCheckoutAndItsBranch(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "head-hash-1", true))
	stand.repo.makeDir = true
	stand.tabs.createErr = errors.New("the layout refused the tab")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-comp", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err == nil {
		t.Fatal("the refused tab mint was accepted")
	}
	wantPath := expectedWorktreePath(stand.spawner.worktreeRoot, "/repo", "feat/one")
	if _, statErr := os.Lstat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the checkout at %q survived the compensation: %v", wantPath, statErr)
	}
	_, removed, deleted := stand.repo.snapshot()
	if len(removed) != 1 || removed[0] != wantPath {
		t.Fatalf("removed = %v, want exactly [%q]", removed, wantPath)
	}
	if len(deleted) != 1 || deleted[0] != [2]string{"feat/one", "head-hash-1"} {
		t.Fatalf("deleted = %v, want exactly [feat/one head-hash-1]", deleted)
	}
}

// Criterion: the same failure over a branch the spawn merely checked out
// removes the checkout but keeps the branch — the branch is somebody
// else's.
func TestAFailedTabMintKeepsAPreexistingBranch(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "head-hash-1", false))
	stand.repo.makeDir = true
	stand.tabs.createErr = errors.New("the layout refused the tab")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-keep", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("keep"),
	})
	if err == nil {
		t.Fatal("the refused tab mint was accepted")
	}
	_, _, deleted := stand.repo.snapshot()
	if len(deleted) != 0 {
		t.Fatalf("a branch the spawn found was deleted: %v", deleted)
	}
}

// Criterion: a compensation that cannot remove the checkout is an ERROR,
// never silence — the record's own discipline treats a failed compensation
// as non-terminal and retries.
func TestAKillReportsAFailedCheckoutRemoval(t *testing.T) {
	stand := newWorktreeStand(t, scriptedRepoWithMain("/repo", "h", true))
	stand.repo.removeErr = errors.New("the worktree holds work")
	coord := stand.openWorktreeCoordinator(t, "pane-coord", "/repo", content.PaneLocal)

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-stuck", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/one"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	killErr := spawned.Kill(context.Background())
	if killErr == nil || !strings.Contains(killErr.Error(), "remove the worktree") {
		t.Fatalf("Kill err = %v, want the failed removal named", killErr)
	}
	_, removed, deleted := stand.repo.snapshot()
	if len(removed) != 1 || len(deleted) != 0 {
		t.Fatalf("removed = %v deleted = %v, want the removal attempted and the branch untouched", removed, deleted)
	}
}

// ── the real repository ──────────────────────────────────────────────────

// initRealRepo makes a real repository with one commit and answers its HEAD.
func initRealRepo(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	gitRun := func(args ...string) string {
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
	gitRun("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	gitRun("add", ".")
	gitRun("commit", "-m", "seed")
	return dir, gitRun("rev-parse", "HEAD")
}

// Criterion, end to end over the REAL git binary and the REAL local factory:
// a worktree spawn creates a checkout that IS on the asked branch, at the
// documented path, with HEAD's hash as the base.
func TestAWorktreeSpawnOnARealRepositoryCreatesTheCheckout(t *testing.T) {
	repoDir, head := initRealRepo(t)
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	stand := newWorktreeStand(t, scriptedRepoWithMain(repoDir, head, true))
	stand.spawner.repos = factory
	coord := stand.openWorktreeCoordinator(t, "pane-coord", repoDir, content.PaneLocal)

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-real", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/x"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	wantPath := expectedWorktreePath(stand.spawner.worktreeRoot, repoDir, "feat/x")
	info, statErr := os.Lstat(wantPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the checkout at %q: %v", wantPath, statErr)
	}
	out, err := exec.Command("git", "-C", wantPath, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput() //nolint:gosec // the path is this test's own temp checkout
	if err != nil || strings.TrimSpace(string(out)) != "feat/x" {
		t.Fatalf("checkout HEAD = %q (%v), want feat/x", strings.TrimSpace(string(out)), err)
	}
	want := workers.Worktree{Path: wantPath, Branch: "feat/x", Base: head}
	if got := worktreeOf(t, spawned); got != want {
		t.Fatalf("location = %+v, want %+v", got, want)
	}
	if got := stand.opener.lastSpec().Cwd; got != wantPath {
		t.Fatalf("session cwd = %q, want the new checkout", got)
	}
}

// Criterion, the macOS shape (nocx-xn63t.1.5 evidence): the worktrees root
// sits behind a symlinked ancestor, so git answers the RESOLVED spelling of
// every path it reports while nocx holds the link spelling — and the
// coordinator's pane records the repository through its own symlinked
// ancestor. The checkout must still land at the documented place, the spawn
// must carry that place, and the record row must be readable under the key
// the location formula derives from the spellings nocx was handed.
func TestASymlinkedWorktreesRootSpawnsTheCheckoutAtTheDocumentedPlace(t *testing.T) {
	repoDir, head := initRealRepo(t)
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	stand := newCheckoutStand(t)
	root := symlinkedWorktreeRoot(t)
	stand.checkouts.worktreeRoot = root
	stand.spawner.worktreeRoot = root

	// The coordinator stands in the repository through the link spelling:
	// that is what its pane records, and git answers the resolved one.
	repoLink := symlinkedDir(t, repoDir)
	coord := stand.openCoordinator(t, "pane-a", repoLink)

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-sym", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk("feat/sym"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	wantPath := expectedWorktreePath(root, repoLink, "feat/sym")
	info, statErr := os.Lstat(wantPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the checkout at %q: %v", wantPath, statErr)
	}
	if got := worktreeOf(t, spawned); got.Path != wantPath {
		t.Fatalf("location path = %q, want the documented place %q", got.Path, wantPath)
	}
	rows, err := stand.rows.List(context.Background(), nocxCheckoutRepoKey(repoLink))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; want the row the spawn wrote", rows, err)
	}
	if rows[0].Path != wantPath || rows[0].Branch != "feat/sym" || rows[0].Base != head {
		t.Fatalf("row = %+v, want the checkout at %q on feat/sym from %q", rows[0], wantPath, head)
	}
}

// noopSupervisor stands in for the watch: the never-enrols case below never
// reaches Attach, and this test is not about supervision.
type noopSupervisor struct{}

func (noopSupervisor) Attach(context.Context, workers.Participant) error { return nil }

// Criterion, the brief's own interval through the REAL registrar: a launcher
// whose enrolment never arrives is compensated, and the compensation leaves
// neither the checkout nor the branch the spawn created — while a branch the
// spawn merely checked out survives, and the record names no checkout.
//
// The pane DOES open in the checkout first (the stand is the real spawner
// over a real registry), so the checkout existed from before the pane opened
// — the interval's start — and `git branch` afterwards settles its end.
func TestANeverEnrolledWorkerLeavesNeitherCheckoutNorBranchBehind(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	enrol := newWorkerEnrolments(logger, reg)
	awaiter := &fakeAxisAwaiter{}
	awaiter.outcome = transport.IntegrationOutcome{Registered: true, Status: transport.IntegrationIntegrated}

	tabs := newWorktreeTabs()
	spawner := &workerSpawner{
		layout: tabs, opener: &fakeAxisOpener{reg: reg}, sessions: reg,
		integration: awaiter, enrolments: enrol,
		workspace: "ws-test", repos: factory, worktreeRoot: t.TempDir(), log: logger,
	}
	registrar := workers.NewRegistrar(
		workers.NewMemoryStore(), spawner, enrol, noopSupervisor{},
		workers.WithEnrolmentDeadline(200*time.Millisecond),
	)

	seedCoordinator := func(t *testing.T) session.ID {
		t.Helper()
		tabs.cwdOf["pane-coord"] = repoDir
		tabs.tabOf["pane-coord"] = "tab-coord"
		tabs.panesOf["tab-coord"] = []content.Pane{{ID: "pane-coord", TabID: "tab-coord", Cwd: repoDir, Kind: content.PaneLocal}}
		sess, err := reg.Open(context.Background(), session.Config{
			Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-coord", Cwd: repoDir,
		})
		if err != nil {
			t.Fatalf("open coordinator: %v", err)
		}
		return sess.ID()
	}

	t.Run("a created branch is removed with the checkout", func(t *testing.T) {
		coord := seedCoordinator(t)

		wantPath := expectedWorktreePath(spawner.worktreeRoot, repoDir, "feat/x")
		_, err := registrar.Register(context.Background(), workers.RegisterRequest{
			Group: workers.ID("worker-1"), CoordinatorSession: string(coord),
			Role: workers.RoleWorker, Task: "t", Command: "run-agent",
			Environment: "env-local", Worktree: anAsk("feat/x"),
		})
		if err == nil {
			t.Fatal("a launcher that never enrols registered anyway")
		}
		if _, statErr := os.Lstat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the checkout at %q survived: %v", wantPath, statErr)
		}
		if out, branchErr := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "feat/x").CombinedOutput(); branchErr == nil { //nolint:gosec // repoDir is this test's temp repository
			t.Fatalf("the branch the spawn created survived: %s", strings.TrimSpace(string(out)))
		}
	})

	t.Run("a pre-existing branch is still there", func(t *testing.T) {
		run := func(args ...string) {
			t.Helper()
			out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput() //nolint:gosec // literal git verbs over this test's own temp repository
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("branch", "keep", "main")

		coord := seedCoordinator(t)

		wantPath := expectedWorktreePath(spawner.worktreeRoot, repoDir, "keep")
		_, err := registrar.Register(context.Background(), workers.RegisterRequest{
			Group: workers.ID("worker-1"), CoordinatorSession: string(coord),
			Role: workers.RoleWorker, Task: "t", Command: "run-agent",
			Environment: "env-local", Worktree: anAsk("keep"),
		})
		if err == nil {
			t.Fatal("a launcher that never enrols registered anyway")
		}
		if _, statErr := os.Lstat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the checkout at %q survived: %v", wantPath, statErr)
		}
		run("rev-parse", "--verify", "keep")
	})
}

// deadlineTabs reproduces the reviewed defect's own trigger: the spawn's
// deadline fires WHILE the tab mint is in flight, so the mint fails on a
// context that is already cancelled. The double cancels the spawn's context
// — an event, not a sleep — and then refuses the mint.
type deadlineTabs struct {
	*worktreeTabs
	cancel    context.CancelFunc
	createErr error
}

func (f *deadlineTabs) CreateTabAfter(_ context.Context, _ content.Tab, _ content.Pane, _ string) (content.Created[content.NewTab], error) {
	f.cancel()
	return content.Created[content.NewTab]{}, f.createErr
}

// Criterion (nocx-xn63t.1 review, blocker 2), over the REAL git binary and
// the REAL local factory: when the tab mint fails because the spawn's
// deadline was cancelled, the early rollback must still remove the checkout
// and the branch it created. The undo used to run on the spawn's own
// context — already cancelled, so the removal failed with it and the
// checkout survived a spawn no worker can ever join. The later compensation
// (Kill) derives its own detached, bounded context for exactly this reason;
// the early rollback now derives the same answer, so the assertion below is
// the interval's end — the checkout is gone — reached on real git.
func TestARefusedTabMintRemovesTheCheckoutEvenOnACancelledDeadline(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tabs := &deadlineTabs{
		worktreeTabs: newWorktreeTabs(),
		cancel:       cancel,
		createErr:    errors.New("the deadline fired while the mint was in flight"),
	}
	spawner := &workerSpawner{
		layout: tabs, opener: &fakeAxisOpener{reg: reg}, sessions: reg,
		integration: &fakeAxisAwaiter{}, enrolments: newWorkerEnrolments(logger, reg),
		workspace: "ws-test", repos: factory, worktreeRoot: t.TempDir(), log: logger,
	}
	tabs.cwdOf["pane-coord"] = repoDir
	tabs.tabOf["pane-coord"] = "tab-coord"
	tabs.panesOf["tab-coord"] = []content.Pane{{ID: "pane-coord", TabID: "tab-coord", Cwd: repoDir, Kind: content.PaneLocal}}
	sess, err := reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-coord", Cwd: repoDir,
	})
	if err != nil {
		t.Fatalf("open coordinator: %v", err)
	}

	_, spawnErr := spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-cancel", Group: "worker-1", Task: "t", Command: "run-agent",
		CoordinatorSession: string(sess.ID()), Worktree: anAsk("feat/deadline"),
	})
	if spawnErr == nil {
		t.Fatal("a refused tab mint returned a Spawned")
	}
	wantPath := expectedWorktreePath(spawner.worktreeRoot, repoDir, "feat/deadline")
	if _, statErr := os.Lstat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the checkout at %q survived a refused mint on a cancelled context: %v", wantPath, statErr)
	}
	if out, branchErr := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "feat/deadline").CombinedOutput(); branchErr == nil { //nolint:gosec // repoDir is this test's temp repository
		t.Fatalf("the branch the spawn created survived: %s", strings.TrimSpace(string(out)))
	}
}
