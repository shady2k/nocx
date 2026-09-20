package app

// The worktree stage's happy path, watched end to end (nocx-xn63t.1.7).
//
// The criterion, in the order the test walks it: a coordinator standing in a
// repository spawns a worker with a worktree ask; the worker's pane lives in
// the new checkout, on the asked branch; the worker commits once and leaves
// one unstaged edit; its close answers what the checkout holds (path,
// uncommitted, ahead) and leaves the checkout standing; a SECOND coordinator
// session's holdings is told about the leftover; removing it refuses while
// the uncommitted work is there; once the edit is reverted the removal
// succeeds; and the branch — with the worker's commit on it — survives the
// removal.
//
// This test's author did not write the implementation it watches (AGENTS.md
// testing rule 4): every expectation below comes from the criterion, and the
// code was read only to learn how to drive the real seams — the record's
// Register and Close behind workers.spawn and workers.close, the checkout
// service the composition root hands the assistant for workers.holdings and
// workers.removeCheckout (workerRecordWithCheckouts), and the real local git
// factory over a real repository in a temp dir. What git says about the
// checkout is the repository's own answer, read with the git binary.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// happyLiveSpawner delegates to the real workerSpawner and remembers each
// Spawned it got back. The pane enrolment the real launcher performs is the
// one part of the journey this stand does not run — there is no agent process
// behind the stub PTY — so the enrolment answer below reads what the spawn
// itself opened rather than inventing a liveness of its own.
type happyLiveSpawner struct {
	*workerSpawner

	mu   sync.Mutex
	live map[workers.ParticipantID]workers.Liveness
}

func (s *happyLiveSpawner) Spawn(ctx context.Context, req workers.SpawnRequest) (workers.Spawned, error) {
	spawned, err := s.workerSpawner.Spawn(ctx, req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.live == nil {
		s.live = make(map[workers.ParticipantID]workers.Liveness)
	}
	s.live[req.Participant] = spawned.Liveness()
	s.mu.Unlock()
	return spawned, nil
}

// happyLiveEnrolments is the record's enrolment seam over happyLiveSpawner:
// Await answers the liveness of the session the spawn actually opened, which
// is what the real enrolment is — the participant's own session, arriving.
type happyLiveEnrolments struct{ spawner *happyLiveSpawner }

func (e happyLiveEnrolments) Await(_ context.Context, p workers.ParticipantID) (workers.Liveness, error) {
	e.spawner.mu.Lock()
	defer e.spawner.mu.Unlock()
	live, ok := e.spawner.live[p]
	if !ok {
		return workers.Liveness{}, errors.New("no launcher enrolled")
	}
	return live, nil
}

func (happyLiveEnrolments) Withdraw(context.Context, workers.ParticipantID) error { return nil }

// worktreeHappyStand is the stage's journey over real material: the real
// spawner, the real record (Register and Close), the real local git factory,
// a real content store for the checkout rows, and the checkout service wired
// exactly as the composition root wires it — the value behind
// workers.holdings' leftover half and workers.removeCheckout's execution.
// What is doubled is only what the journey does not assert: the layout rows
// (worktreeTabs) and the pane enrolment (happyLiveEnrolments).
type worktreeHappyStand struct {
	reg     *session.Reg
	tabs    *worktreeTabs
	opener  *fakeAxisOpener
	factory *gitlocal.Factory
	record  *workers.Registrar
	store   *workers.MemoryStore
	// worktreeRoot is where nocx-made checkouts of this stand live — the
	// spawner's root, read back through the stand for the location formula.
	worktreeRoot string
	// coordinatorRecord is the value the composition root hands the
	// assistant's tool surface — spawn, close, holdings, removal all reach
	// the record through it, which is what makes calls through it calls
	// through the tools' own seam.
	coordinatorRecord *workerRecordWithCheckouts
}

func newWorktreeHappyStand(t *testing.T) *worktreeHappyStand {
	t.Helper()
	_, lg := logtest.New(t)
	ptys := &workerTestPTYFactory{log: lg}
	reg := session.New(lg, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	tabs := newWorktreeTabs()
	opener := &fakeAxisOpener{reg: reg}
	awaiter := &fakeAxisAwaiter{}
	awaiter.outcome = transport.IntegrationOutcome{Registered: true, Status: transport.IntegrationIntegrated}
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	// A REAL store over a real file, so the rows the spawn writes are rows a
	// holdings survey reads back — not a stub that answers whatever the test
	// would like.
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: lg,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The production tab record, shared by the spawn that writes it and the
	// close that reads it — the one record a close needs to take the
	// participant's tab out of the window.
	partTabs := newWorkerTabs()
	// The durable checkout record, held first without its `held` answer: the
	// registrar below is what answers it, and the registrar needs the closer,
	// which needs nothing here. The pointer is filled in once the record
	// exists.
	checkouts := &workerCheckouts{
		repos:        factory,
		worktreeRoot: filepath.Join(dir, "worktrees"),
		rows:         db.WorkerCheckouts(),
		sessions:     reg,
		layout:       tabs,
	}
	spawner := &happyLiveSpawner{workerSpawner: &workerSpawner{
		layout: tabs, opener: opener, sessions: reg,
		integration: awaiter, enrolments: newWorkerEnrolments(lg, reg),
		workspace: "ws-test", repos: factory, tabs: partTabs,
		worktreeRoot: checkouts.worktreeRoot,
		checkouts:    checkouts, log: lg,
	}}
	closer := &workerCloser{
		sessions: reg, layout: tabs, tabs: partTabs,
		repos: factory, log: lg,
	}
	memStore := workers.NewMemoryStore()
	record := workers.NewRegistrar(
		memStore, spawner, happyLiveEnrolments{spawner: spawner}, noopSupervisor{},
		workers.WithCloser(closer),
		workers.WithEnrolmentDeadline(5*time.Second),
	)
	checkouts.held = record
	return &worktreeHappyStand{
		reg: reg, tabs: tabs, opener: opener, factory: factory,
		record:            record,
		store:             memStore,
		worktreeRoot:      checkouts.worktreeRoot,
		coordinatorRecord: &workerRecordWithCheckouts{Registrar: record, checkouts: checkouts},
	}
}

// openWorktreeHappyCoordinator stands a coordinator session in a pane whose
// recorded directory is cwd — the layout row the session→pane→directory walk
// reads. Each coordinator gets its own tab, the way two sessions of one
// window would hold two.
func (s *worktreeHappyStand) openWorktreeHappyCoordinator(t *testing.T, paneID, tabID, cwd string) session.ID {
	t.Helper()
	s.tabs.cwdOf[paneID] = cwd
	s.tabs.tabOf[paneID] = tabID
	s.tabs.panesOf[tabID] = []content.Pane{{ID: paneID, TabID: tabID, Cwd: cwd, Kind: content.PaneLocal}}
	sess, err := s.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: paneID, Cwd: cwd,
	})
	if err != nil {
		t.Fatalf("open coordinator %q: %v", paneID, err)
	}
	return sess.ID()
}

// Criterion: the whole journey — spawn with a worktree ask, the worker's own
// work, the close's answer, the second session's holdings, the removal's
// refusal and success, and the branch that outlives the checkout. Each step
// names itself in its failure message, with what was expected and what came
// back.
func TestAWorkerGetsItsOwnWorktreeAndTheCheckoutOutlivesIt(t *testing.T) {
	repoDir, head := initRealRepo(t)
	stand := newWorktreeHappyStand(t)
	const branch = "feat/happy"

	// Step 1 — the coordinator's spawn creates the checkout and a live
	// worker in it.
	coord1 := stand.openWorktreeHappyCoordinator(t, "pane-coord-1", "tab-coord-1", repoDir)
	reg, err := stand.record.Register(context.Background(), workers.RegisterRequest{
		Group:              workers.ID("wt-happy-1"),
		CoordinatorSession: string(coord1),
		Role:               workers.RoleWorker,
		Task:               "do the work",
		Command:            "run-agent",
		Environment:        "env-local",
		Worktree:           anAsk(branch),
	})
	if err != nil {
		t.Fatalf("step 1 (spawn with a worktree ask): want a live registered worker, got error %v", err)
	}
	if reg.State != workers.StateLive {
		t.Fatalf("step 1 (spawn with a worktree ask): want state %q, got %q (%+v)", workers.StateLive, reg.State, reg.Participant)
	}
	wantPath := expectedWorktreePath(stand.worktreeRoot, repoDir, branch)

	// Step 2 — the checkout exists at the documented place, ON the asked
	// branch, the record carries the resolved facts, and the worker's pane
	// (the minted row and the session both) lives in it.
	if out := gitIn(t, wantPath, "rev-parse", "--abbrev-ref", "HEAD"); out != branch {
		t.Fatalf("step 2 (the checkout is on the asked branch): want HEAD %q, got %q", branch, out)
	}
	stored, err := stand.store.Participant(context.Background(), reg.ID)
	if err != nil {
		t.Fatalf("step 2 (the record carries the checkout): want the worker's row, got error %v", err)
	}
	wantLoc := workers.Worktree{Path: wantPath, Branch: branch, Base: head}
	if stored.Worktree != wantLoc {
		t.Fatalf("step 2 (the record carries the checkout): want %+v, got %+v", wantLoc, stored.Worktree)
	}
	if got := stand.tabs.firstPane.Cwd; got != wantPath {
		t.Fatalf("step 2 (the worker's pane row lives in the checkout): want cwd %q, got %q", wantPath, got)
	}
	if got := stand.opener.lastSpec().Cwd; got != wantPath {
		t.Fatalf("step 2 (the worker's session lives in the checkout): want cwd %q, got %q", wantPath, got)
	}

	// Step 3 — the worker does its work in the checkout: one commit, and one
	// edit it leaves unstaged.
	if writeErr := os.WriteFile(filepath.Join(wantPath, "worker-note.txt"), []byte("worker work\n"), 0o600); writeErr != nil {
		t.Fatalf("step 3 (the worker's commit): write the file: %v", writeErr)
	}
	gitIn(t, wantPath, "add", "worker-note.txt")
	gitIn(t, wantPath, "commit", "-m", "worker work")
	workerCommit := gitIn(t, wantPath, "rev-parse", "HEAD")
	readme := filepath.Join(wantPath, "README.md")
	seed, err := os.ReadFile(readme) //nolint:gosec // a tracked file of this test's own checkout under t.TempDir()
	if err != nil {
		t.Fatalf("step 3 (the worker's edit): read the tracked file: %v", err)
	}
	if writeErr := os.WriteFile(readme, append(seed, []byte("an edit no commit keeps\n")...), 0o600); writeErr != nil {
		t.Fatalf("step 3 (the worker's edit): write the tracked file: %v", writeErr)
	}

	// Step 4 — the coordinator's close answers what the checkout holds: the
	// path, the work no commit keeps, and the commit the branch grew.
	res, err := stand.record.Close(context.Background(), string(coord1), reg.ID)
	if err != nil {
		t.Fatalf("step 4 (the close answers): want a close result, got error %v", err)
	}
	wantLeftover := workers.Leftover{
		Path: wantPath, Branch: branch,
		State: workers.CheckoutRead, Uncommitted: true, Ahead: 1,
	}
	if res.Worktree != wantLeftover {
		t.Fatalf("step 4 (the close answers): want %+v, got %+v", wantLeftover, res.Worktree)
	}

	// Step 5 — the close left the checkout on disk.
	if info, statErr := os.Lstat(wantPath); statErr != nil || !info.IsDir() {
		t.Fatalf("step 5 (the close left the checkout): want a directory at %q, got stat error %v", wantPath, statErr)
	}

	// Step 6 — a SECOND coordinator session, standing in the same
	// repository, is told about the leftover by its own holdings.
	coord2 := stand.openWorktreeHappyCoordinator(t, "pane-coord-2", "tab-coord-2", repoDir)
	survey := stand.coordinatorRecord.LeftoverCheckouts(context.Background(), string(coord2))
	if !survey.Complete {
		t.Fatalf("step 6 (the second session's holdings): want a complete survey, got an incomplete one (%+v)", survey)
	}
	if len(survey.Leftovers) != 1 {
		t.Fatalf("step 6 (the second session's holdings): want exactly one leftover, got %d (%+v)", len(survey.Leftovers), survey.Leftovers)
	}
	got := survey.Leftovers[0]
	wantRow := workers.LeftoverCheckout{
		Path: wantPath, Branch: branch,
		Readable: true, Uncommitted: true, Ahead: 1,
	}
	if got.Path != wantRow.Path || got.Branch != wantRow.Branch || !got.Readable || !got.Uncommitted || got.Ahead != wantRow.Ahead {
		t.Fatalf("step 6 (the second session's holdings): want %+v, got %+v", wantRow, got)
	}

	// Step 7 — removing the checkout refuses while the uncommitted work is
	// in it, names the refusal, and leaves everything exactly as it was.
	removal := stand.coordinatorRecord.RemoveCheckouts(context.Background(), string(coord2), []workers.CheckoutRef{{Path: wantPath}})
	if len(removal.Items) != 1 {
		t.Fatalf("step 7 (the removal refuses uncommitted work): want one answer, got %d (%+v)", len(removal.Items), removal.Items)
	}
	refused := removal.Items[0]
	if refused.Removed {
		t.Fatalf("step 7 (the removal refuses uncommitted work): want removed=false, got %+v", refused)
	}
	if refused.Refusal != workers.CheckoutRefusalUncommitted {
		t.Fatalf("step 7 (the removal refuses uncommitted work): want refusal %q, got %q (detail %q)",
			workers.CheckoutRefusalUncommitted, refused.Refusal, refused.Detail)
	}
	if refused.Path != wantPath || refused.Branch != branch {
		t.Fatalf("step 7 (the removal refuses uncommitted work): want the checkout resolved as %q/%q, got %q/%q",
			wantPath, branch, refused.Path, refused.Branch)
	}
	if info, statErr := os.Lstat(wantPath); statErr != nil || !info.IsDir() {
		t.Fatalf("step 7 (the refusal left the checkout): want a directory at %q, got stat error %v", wantPath, statErr)
	}

	// Step 8 — the worker's edit is reverted, and the same removal now
	// succeeds: the checkout directory is gone.
	gitIn(t, wantPath, "checkout", "--", "README.md")
	removal = stand.coordinatorRecord.RemoveCheckouts(context.Background(), string(coord2), []workers.CheckoutRef{{Path: wantPath}})
	if len(removal.Items) != 1 {
		t.Fatalf("step 8 (the removal succeeds once clean): want one answer, got %d (%+v)", len(removal.Items), removal.Items)
	}
	removed := removal.Items[0]
	if !removed.Removed || removed.Refusal != "" {
		t.Fatalf("step 8 (the removal succeeds once clean): want removed=true refusal \"\", got %+v", removed)
	}
	if _, statErr := os.Lstat(wantPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("step 8 (the removal took the checkout): want %q gone, got stat %v", wantPath, statErr)
	}

	// Step 9 — the branch is still there, still at the worker's commit.
	if out := gitIn(t, repoDir, "rev-parse", "--verify", branch); out != workerCommit {
		t.Fatalf("step 9 (the branch outlives the checkout): want %q at %q, got %q", branch, workerCommit, out)
	}
}
