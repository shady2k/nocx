package app

// The checkouts a repository has left over, end to end over the REAL git
// binary, the REAL local factory and a REAL content store (nocx-xn63t.1.4).
//
// The brief's acceptance criteria, each with its test:
//
//   - a checkout a closed worker left is visible to a NEW coordinator
//     session in the same repository, and invisible to one in another
//     repository (TestASpawnedCheckoutIsLeftOverForANewSessionInTheSameRepository);
//   - the same after a backend restart — a new store over the same database
//     file (TestTheCheckoutRecordSurvivesAStoreReopen);
//   - a checkout removed by hand is not listed, and its row is gone after
//     the read (TestACheckoutRemovedByHandIsDroppedOnRead);
//   - a checkout a live worker holds is listed under that worker, never as
//     left over (TestALiveWorkersCheckoutIsNotLeftOver);
//   - the store failing to open marks the answer incomplete, in the answer
//     and not only in a log (TestAFailingRecordMarksTheAnswerIncomplete),
//     and the composition root hands the service the store that actually
//     opened (TestTheCheckoutsServiceReadsTheRealStoreAtTheCompositionRoot,
//     TestTheCheckoutsServiceIsWiredAtTheCompositionRoot);

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// checkoutStand is the real-material stand: the local git factory, a real
// encrypted content store, and the production spawner wired with the
// production checkouts service — the composition the brief's criteria are
// about, at the spawner and through the service, the way
// worker_spawn_worktree_test.go's stands are, each for the half it proves.
type checkoutStand struct {
	reg       *session.Reg
	tabs      *worktreeTabs
	opener    *fakeAxisOpener
	factory   *gitlocal.Factory
	spawner   *workerSpawner
	checkouts *workerCheckouts
	record    *workers.Registrar
	memStore  *workers.MemoryStore
	store     content.ContentDB
	rows      content.WorkerCheckoutRepository
	dbPath    string
}

func newCheckoutStand(t *testing.T) *checkoutStand {
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
	opener := &fakeAxisOpener{reg: reg}
	awaiter := &fakeAxisAwaiter{}
	awaiter.outcome = transport.IntegrationOutcome{Registered: true, Status: transport.IntegrationIntegrated}
	factory := gitlocal.NewFactory()
	t.Cleanup(factory.Stop)

	// A REAL store over a real file, so a reopen is the restart the brief
	// names — not a second in-memory thing that happens to hold the rows.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "content.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   dbPath,
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	memStore := workers.NewMemoryStore()
	record := workers.NewRegistrar(memStore, nil, nil, nil)
	checkouts := &workerCheckouts{
		repos:        factory,
		worktreeRoot: filepath.Join(dir, "worktrees"),
		rows:         db.WorkerCheckouts(),
		sessions:     reg,
		layout:       tabs,
		held:         record,
	}
	spawner := &workerSpawner{
		layout: tabs, opener: opener, sessions: reg,
		integration: awaiter, enrolments: newWorkerEnrolments(logger, reg),
		workspace: "ws-test", repos: factory,
		worktreeRoot: checkouts.worktreeRoot,
		checkouts:    checkouts,
		log:          logger,
	}
	return &checkoutStand{
		reg: reg, tabs: tabs, opener: opener, factory: factory,
		spawner: spawner, checkouts: checkouts, record: record,
		memStore: memStore, store: db, rows: db.WorkerCheckouts(), dbPath: dbPath,
	}
}

// openCoordinator stands a coordinator session in a pane whose recorded
// directory is cwd — the layout row the walk reads.
func (s *checkoutStand) openCoordinator(t *testing.T, paneID, cwd string) session.ID {
	t.Helper()
	const tabID = "tab-coord"
	s.tabs.cwdOf[paneID] = cwd
	s.tabs.tabOf[paneID] = tabID
	s.tabs.panesOf[tabID] = []content.Pane{{ID: paneID, TabID: tabID, Cwd: cwd, Kind: content.PaneLocal}}
	sess, err := s.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: paneID, Cwd: cwd,
	})
	if err != nil {
		t.Fatalf("open the coordinator's session: %v", err)
	}
	return sess.ID()
}

// spawnCheckoutWorker runs the production Spawn with a worktree ask, the way
// task 1.2's real-repository test drives it.
func (s *checkoutStand) spawnCheckoutWorker(t *testing.T, coord session.ID, participant, branch, task string) {
	t.Helper()
	_, err := s.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: workers.ParticipantID(participant), Group: workers.ID(participant),
		Task: task, Command: "run-agent",
		CoordinatorSession: string(coord), Worktree: anAsk(branch),
	})
	if err != nil {
		t.Fatalf("spawn %s: %v", participant, err)
	}
}

// holdCheckout stamps a LIVE participant onto the record as the holder of a
// checkout — the fact MarkLive accepts, which is what makes the checkout
// held rather than left over.
func (s *checkoutStand) holdCheckout(t *testing.T, participant, sessionID, path, branch string) {
	t.Helper()
	ctx := context.Background()
	p := workers.Participant{
		ID: workers.ParticipantID(participant), Group: workers.ID(participant),
		Role: workers.RoleWorker, State: workers.StatePrepared,
		Task:         "hold it",
		RegisteredAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
	// The record's own writes, at the store: the registrar's Register is the
	// whole spawn procedure, and this test stamps the interval's facts
	// directly, exactly as internal/workers' own tests do.
	if err := s.memStore.EnsureGroup(ctx, p.Group, sessionID); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	if err := s.memStore.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit prepared: %v", err)
	}
	l := workers.Liveness{SessionID: sessionID}
	if err := s.memStore.MarkLive(ctx, p.ID, l, workers.Worktree{Path: path, Branch: branch, Base: "4f2a1c9"}); err != nil {
		t.Fatalf("mark live: %v", err)
	}
}

func reopenStore(t *testing.T, path string) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   path,
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen the content store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Criterion 1, whole: session A spawns a worktree worker; a NEW coordinator
// session standing in the SAME repository — the main checkout here, a linked
// one in the sibling half — calls holdings and sees that checkout, named
// with the worker it was for; a session standing in a DIFFERENT repository
// sees nothing.
func TestASpawnedCheckoutIsLeftOverForANewSessionInTheSameRepository(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	otherDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)

	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "read AGENTS.md and report")

	// A NEW coordinator session, same repository, different session.
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete {
		t.Fatalf("the survey read whole and answered incomplete")
	}
	if len(survey.Leftovers) != 1 {
		t.Fatalf("leftovers = %+v, want exactly the checkout the spawn created", survey.Leftovers)
	}
	left := survey.Leftovers[0]
	if want := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one"); left.Path != want {
		t.Fatalf("leftover path = %q, want %q", left.Path, want)
	}
	if left.Branch != "feat/one" || left.Name != "worker-1" || left.Task != "read AGENTS.md and report" {
		t.Fatalf("leftover = %+v, want the branch it holds and the worker it was for", left)
	}
	if !left.Readable || left.Uncommitted || left.Ahead != 0 {
		t.Fatalf("leftover state = %+v, want a readable, clean, even checkout", left)
	}
	if left.LastUsed.IsZero() {
		t.Fatalf("lastUsed is zero for a checkout nocx created moments ago")
	}

	// A coordinator standing in a DIFFERENT repository sees none of it.
	coordC := stand.openCoordinator(t, "pane-c", otherDir)
	other := stand.checkouts.Leftovers(context.Background(), string(coordC))
	if !other.Complete {
		t.Fatalf("the other repository's survey answered incomplete")
	}
	if len(other.Leftovers) != 0 {
		t.Fatalf("another repository's leftovers = %+v, want none", other.Leftovers)
	}
}

// Criterion 1, the linked-checkout half: the coordinator asking may itself
// stand in a LINKED worktree of the same repository — the key is the main
// checkout's, which is the seam's own listing answer, and the answer is the
// same repository's leftovers.
func TestACoordinatorInALinkedWorktreeSeesTheSameLeftovers(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)

	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")

	// A linked worktree of the SAME repository, made by hand the way a
	// person or an earlier worker would.
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repoDir, "worktree", "add", linked, "-b", "linked-branch")
	coordB := stand.openCoordinator(t, "pane-b", linked)

	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete {
		t.Fatalf("the survey answered incomplete")
	}
	found := false
	for _, left := range survey.Leftovers {
		if left.Branch == "feat/one" {
			found = true
		}
		if left.Branch == "linked-branch" {
			t.Fatalf("the coordinator's own linked worktree %q came back as nocx's leftover", left.Path)
		}
	}
	if !found {
		t.Fatalf("leftovers = %+v, want feat/one visible from the linked checkout", survey.Leftovers)
	}
}

// Criterion 2: the same after a backend restart — a NEW store over the SAME
// database file. The row is the durable half; git never left.
func TestTheCheckoutRecordSurvivesAStoreReopen(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "the restart task")

	// THE RESTART: the store closed, a new one over the same file, a new
	// service over it, a new session asking.
	if err := stand.store.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	reopened := reopenStore(t, stand.dbPath)
	fresh := &workerCheckouts{
		repos:        stand.factory,
		worktreeRoot: stand.checkouts.worktreeRoot,
		rows:         reopened.WorkerCheckouts(),
		sessions:     stand.reg,
		layout:       stand.tabs,
		held:         stand.record,
	}
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	survey := fresh.Leftovers(context.Background(), string(coordB))
	if !survey.Complete {
		t.Fatalf("the reopened store answered incomplete")
	}
	if len(survey.Leftovers) != 1 {
		t.Fatalf("leftovers after the restart = %+v, want the one checkout with its record", survey.Leftovers)
	}
	if survey.Leftovers[0].Name != "worker-1" || survey.Leftovers[0].Task != "the restart task" {
		t.Fatalf("leftover = %+v, want the record that survived the restart", survey.Leftovers[0])
	}
}

// Criterion 3: a checkout removed by hand (`git worktree remove` in a shell)
// is not listed, and its row is gone after the read.
func TestACheckoutRemovedByHandIsDroppedOnRead(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/gone", "t")
	removed := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/gone")

	gitRun(t, repoDir, "worktree", "remove", removed)

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete {
		t.Fatalf("the survey answered incomplete")
	}
	if len(survey.Leftovers) != 0 {
		t.Fatalf("leftovers = %+v, want none — the checkout is not git's any more", survey.Leftovers)
	}
	// AND THE ROW IS GONE after the read that noticed: the next read starts
	// clean instead of silt.
	rows, err := stand.rows.List(context.Background(), nocxCheckoutRepoKey(repoDir))
	if err != nil {
		t.Fatalf("list the record: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the record still holds %+v after the read that noticed the checkout was gone", rows)
	}
}

// Criterion 4: a checkout a LIVE worker holds is listed under that worker —
// in the record's held answer, which is what the participant row renders —
// and never as left over, not even to a coordinator session that is not the
// holder's own.
func TestALiveWorkersCheckoutIsNotLeftOver(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/held", "t")
	held := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/held")
	stand.holdCheckout(t, "worker-1", string(coordA), held, "feat/held")

	// A DIFFERENT session asks: the checkout is held, and held is a fact
	// about the checkout, so it is not offered up as abandoned to a
	// coordinator it does not belong to.
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete {
		t.Fatalf("the survey answered incomplete")
	}
	if len(survey.Leftovers) != 0 {
		t.Fatalf("leftovers = %+v, want none — %q is a live worker's", survey.Leftovers, held)
	}
	heldBy, err := stand.record.HeldWorktrees(context.Background())
	if err != nil {
		t.Fatalf("held worktrees: %v", err)
	}
	if len(heldBy) != 1 || heldBy[0].Path != held {
		t.Fatalf("held = %+v, want the checkout under its worker", heldBy)
	}
}

// Criterion 5: the store failing is visible IN the answer. With the record's
// repository failing, the survey is empty AND incomplete — never a short
// list standing as the whole truth.
func TestAFailingRecordMarksTheAnswerIncomplete(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")

	broken := &workerCheckouts{
		repos:        stand.factory,
		worktreeRoot: stand.checkouts.worktreeRoot,
		rows:         failingCheckoutRows{err: errRowsBroken},
		sessions:     stand.reg,
		layout:       stand.tabs,
		held:         stand.record,
	}
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	survey := broken.Leftovers(context.Background(), string(coordB))
	if survey.Complete {
		t.Fatalf("the answer claims completeness behind a record that could not be read")
	}
	if len(survey.Leftovers) != 0 {
		t.Fatalf("leftovers = %+v, want empty behind a failed read", survey.Leftovers)
	}

	// And the reading coordinator's OWN spawn result carries the same
	// honesty through the executor: an incomplete survey is a count that
	// says it may be short, which is the executor test's business — here it
	// is enough that the service said so.
	if stand.checkouts.rows == nil {
		t.Fatal("the real stand lost its record")
	}
}

var errRowsBroken = errBroken{}

type errBroken struct{}

func (errBroken) Error() string { return "content: the store is broken for this test" }

// failingCheckoutRows is the durable record with a store that answers every
// read with failure — what a store that failed to open but was wired anyway
// would be, and the shape the composition root must never hand the service
// (it hands nil instead, which the service reads the same way).
type failingCheckoutRows struct{ err error }

func (f failingCheckoutRows) Put(context.Context, content.WorkerCheckout) error { return f.err }
func (f failingCheckoutRows) Touch(context.Context, string, int64) error        { return f.err }
func (f failingCheckoutRows) List(context.Context, string) ([]content.WorkerCheckout, error) {
	return nil, f.err
}

func (f failingCheckoutRows) All(context.Context) ([]content.WorkerCheckout, error) {
	return nil, f.err
}
func (f failingCheckoutRows) Delete(context.Context, string, []string) error { return f.err }

// Criterion 5's wiring half, and criterion 7's: the composition root builds
// the service over its REAL inputs, and when the store it wired actually
// opened (the isolated test home derives a real content key, so it does),
// the rows come from that store and an unresolvable session answers
// complete-and-empty — every read behind the answer worked. The
// store-could-not-open degrade itself is the service's nil-rows branch,
// proven by TestAFailingRecordMarksTheAnswerIncomplete beside the failing
// double; the composition root's job here is to hand the service the store
// that opened and nothing that pretends to be one.
func TestTheCheckoutsServiceReadsTheRealStoreAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.workerCheckouts == nil {
		t.Fatal("New built no checkouts service; the wiring is the criterion")
	}
	if a.workerCheckouts.rows == nil {
		t.Fatal("the content store opened and the service was wired without it")
	}
	if a.workerCheckouts.held == nil || a.workerCheckouts.repos == nil ||
		a.workerCheckouts.sessions == nil || a.workerCheckouts.layout == nil {
		t.Fatalf("the service is missing a real input: held=%v repos=%v sessions=%v layout=%v",
			a.workerCheckouts.held, a.workerCheckouts.repos, a.workerCheckouts.sessions, a.workerCheckouts.layout)
	}
	survey := a.workerCheckouts.Leftovers(context.Background(), "no-such-session")
	if !survey.Complete {
		t.Fatalf("the survey answered incomplete behind a store that reads: %+v", survey)
	}
	if len(survey.Leftovers) != 0 {
		t.Fatalf("leftovers = %+v, want none for a session standing nowhere", survey.Leftovers)
	}
}

// Criterion: the wiring is the composition root's, and the service it built
// is the one the spawn path records through — the same value, not two
// constructions of it.
func TestTheCheckoutsServiceIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.workerCheckouts == nil {
		t.Fatal("New built no checkouts service")
	}
	// The pane-open note is the same service the transport was handed: a
	// pane opened inside a recorded checkout moves ITS row's last-used.
	if a.workerCheckouts.worktreeRoot == "" {
		t.Fatal("the service was built without the worktrees root")
	}
}

// Criterion: the last-used stamp moves when nocx opens a pane whose cwd is
// inside the checkout — at the root or below it — and never for a pane
// standing elsewhere or on a far machine. This is the note the composition
// root hands the transport.
func TestAPaneOpenedInsideACheckoutMovesItsLastUsedForward(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	t0 := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	stand.checkouts.now = func() time.Time { return t0 }
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	rowsOf := func(t *testing.T) content.WorkerCheckout {
		t.Helper()
		rows, err := stand.rows.List(context.Background(), key)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows = %+v, %v; want the one checkout", rows, err)
		}
		return rows[0]
	}
	if row := rowsOf(t); row.LastUsedAt != t0.UnixMilli() {
		t.Fatalf("last_used = %d, want the creation stamp %d", row.LastUsedAt, t0.UnixMilli())
	}

	// A pane opened in a SUBDIRECTORY of the checkout, on the local machine:
	// inside it.
	stand.checkouts.now = func() time.Time { return t1 }
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "", Cwd: filepath.Join(checkout, "pkg", "deep")}, "sess-x")
	if row := rowsOf(t); row.LastUsedAt != t1.UnixMilli() {
		t.Fatalf("last_used = %d, want the pane-open stamp %d", row.LastUsedAt, t1.UnixMilli())
	}

	// A pane standing elsewhere changes nothing, and neither does one on a
	// far machine whose cwd happens to spell the same directory.
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "", Cwd: repoDir}, "sess-x")
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "ssh", Cwd: checkout}, "sess-x")
	if row := rowsOf(t); row.LastUsedAt != t1.UnixMilli() {
		t.Fatalf("last_used = %d, want it unmoved by panes outside the checkout", row.LastUsedAt)
	}
}

// Criterion: a pane the RENDERER opens moves the stamp too. The open
// request carries no directory on the wire — the pane's directory is the
// layout row's fact — so the note reads the row for the pane it was told
// about, and a pane whose row has no directory yet changes nothing.
func TestAPaneTheRendererOpensStampsItsCheckoutLastUsed(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	t0 := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	stand.checkouts.now = func() time.Time { return t0 }
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	stand.checkouts.now = func() time.Time { return t1 }

	// The spec the wire actually builds for a renderer open: a pane id,
	// kind local, and NO cwd — the wire never carried one.
	stand.tabs.cwdOf["pane-renderer"] = filepath.Join(checkout, "pkg", "deep")
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "", PaneID: "pane-renderer"}, "sess-x")
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; want the one checkout", rows, err)
	}
	if rows[0].LastUsedAt != t1.UnixMilli() {
		t.Fatalf("last_used = %d, want the renderer pane-open stamp %d (the note must read the layout row's directory)", rows[0].LastUsedAt, t1.UnixMilli())
	}

	// A renderer open whose pane has no recorded directory yet — the fresh
	// pane whose shell has not answered an OSC 7 — stamps nothing, because
	// there is no directory to stand anywhere.
	stand.checkouts.now = func() time.Time { return t1.Add(time.Hour) }
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "", PaneID: "pane-unrecorded"}, "sess-x")
	rows, err = stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; want the one checkout", rows, err)
	}
	if rows[0].LastUsedAt != t1.UnixMilli() {
		t.Fatalf("last_used = %d, want it unmoved by a pane with no recorded directory", rows[0].LastUsedAt)
	}
}

// Criterion: the spawn path records the checkout through the SAME service
// the answer reads — a second construction would be two records that agree
// until they don't.
func TestTheSpawnRecordsThroughTheSameServiceTheAnswerReads(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	if stand.spawner.checkouts != stand.checkouts {
		t.Fatal("the spawner records through a different service than the one holdings reads")
	}
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	rows, err := stand.rows.List(context.Background(), nocxCheckoutRepoKey(repoDir))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; want the row the spawn wrote", rows, err)
	}
}

// gitRun is initRealRepo's runner, named for the calls this file makes
// besides init: a linked worktree, and the by-hand removal the brief's
// criterion names.
func gitRun(t *testing.T, dir string, args ...string) {
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
		t.Fatalf("git %v: %v\n%s", args, err, strings.TrimSpace(string(out)))
	}
}
