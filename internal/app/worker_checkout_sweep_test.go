package app

// The sweep that removes the checkouts nobody has used for a period
// (nocx-xn63t.1.6), over the REAL git binary, the REAL local factory and a
// REAL content store — the same stand the holdings answer is proved on
// (worker_checkouts_test.go), because the sweep removes through exactly the
// removal path that answer's refusals guard.
//
// The brief's acceptance criteria, each with its test:
//
//   - with the clock injected, a checkout last used 31 days ago is removed
//     at the sweep and its branch remains; one used 29 days ago stays; with
//     the setting at 0 nothing is removed (TestTheSweepRemovesACheckout...,
//     TestTheSweepLeavesACheckoutUsedWithinThePeriod,
//     TestTheSweepWithThePeriodAtZeroRemovesNothing);
//   - an expired checkout with uncommitted work is not removed, and
//     holdings says it is expired and why it is still there
//     (TestAnExpiredCheckoutWithUncommittedWorkIsKeptAndNamedInHoldings);
//   - opening a pane in an old checkout moves its last-used time, and the
//     next sweep leaves it (TestOpeningAPaneInAnOldCheckoutMovesItsStamp...);
//   - a person changes the period in Settings and the next sweep uses it,
//     through the settings seam a user reaches (TestTheSweepReadsThePeriod...);
//   - a checkout a live pane stands in is never removed
//     (TestTheSweepNeverRemovesACheckoutAPaneIsStandingIn);
//   - wired at the composition root (TestTheCheckoutSweepIsWiredAtTheCompositionRoot).
//
// No test here waits on a duration: the daily ticker is production
// scheduling, and every test drives RunOnce directly with the clock
// injected.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// errPaneCwdBroken is the pane-inventory read failure the unresolved-pass
// test stands on.
var errPaneCwdBroken = errors.New("pane cwd broken")

var sweepNow = time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)

// newSweeper builds the sweep over the stand with the idle period and the
// clock injected — the way the composition root builds it, with the
// period closure standing in for the settings read.
func newSweeper(stand *checkoutStand, period time.Duration) *checkoutSweeper {
	return &checkoutSweeper{
		checkouts: stand.checkouts,
		sessions:  stand.reg,
		period:    func() time.Duration { return period },
		now:       func() time.Time { return sweepNow },
	}
}

// ageCheckout rewrites the checkout's row with an older last-used stamp —
// the only way a test can age one, because Touch never moves the stamp
// backward and must never learn how.
func ageCheckout(t *testing.T, stand *checkoutStand, key, path string, at time.Time) {
	t.Helper()
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; want the one checkout", rows, err)
	}
	row := rows[0]
	row.LastUsedAt = at.UnixMilli()
	if err := stand.rows.Put(context.Background(), row); err != nil {
		t.Fatalf("age the checkout's row: %v", err)
	}
}

// checkoutGone reports whether the checkout directory has left the disk.
func checkoutGone(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	t.Fatalf("stat the checkout: %v", err)
	return false
}

func TestTheSweepRemovesACheckoutUnusedForThePeriodAndKeepsItsBranch(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))
	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if !checkoutGone(t, checkout) {
		t.Fatalf("the checkout at %q survived a sweep with the period at 30 days and nothing used it for 31", checkout)
	}
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, %v; want the removed checkout's row dropped", rows, err)
	}
	// THE BRANCH REMAINS: the sweep is the removal path, and that path never
	// deletes a branch.
	gitRun(t, repoDir, "rev-parse", "--verify", "refs/heads/feat/one")
}

func TestTheSweepLeavesACheckoutUsedWithinThePeriod(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-29*24*time.Hour))
	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the checkout at %q was removed with its last use 29 days back, inside the period", checkout)
	}
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 1 || rows[0].LastUsedAt != sweepNow.Add(-29*24*time.Hour).UnixMilli() {
		t.Fatalf("rows = %+v, %v; want the row kept with its own stamp", rows, err)
	}
}

func TestTheSweepWithThePeriodAtZeroRemovesNothing(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-400*24*time.Hour))
	newSweeper(stand, 0).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the checkout at %q was removed with the period at zero, which means never", checkout)
	}
}

func TestAnExpiredCheckoutWithUncommittedWorkIsKeptAndNamedInHoldings(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))
	// Uncommitted work: an untracked file nobody's commit keeps.
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("half a thought\n"), 0o600); err != nil {
		t.Fatalf("write uncommitted work into the checkout: %v", err)
	}
	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the sweep removed the checkout at %q past uncommitted work, which the removal path refuses", checkout)
	}
	survey := stand.checkouts.Leftovers(context.Background(), string(coordA))
	if !survey.Complete || len(survey.Leftovers) != 1 {
		t.Fatalf("holdings = %+v; want the one expired checkout, complete", survey)
	}
	leftover := survey.Leftovers[0]
	if leftover.Path != checkout || !leftover.Expired {
		t.Fatalf("leftover = %+v; want the checkout named expired", leftover)
	}
	if leftover.HoldReason != string(workers.CheckoutRefusalUncommitted) || leftover.HoldDetail == "" {
		t.Fatalf("hold = %q / %q; want the uncommitted refusal and what is true on disk", leftover.HoldReason, leftover.HoldDetail)
	}
}

func TestOpeningAPaneInAnOldCheckoutMovesItsStampAndTheNextSweepLeavesIt(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	// A person opens a pane from the UI in the old checkout: the spec the
	// wire builds carries the pane and no directory, and the note reads the
	// layout row's. The clock at the sweep's now — the stamp moves to NOW.
	stand.checkouts.now = func() time.Time { return sweepNow }
	stand.tabs.cwdOf["pane-person"] = checkout
	stand.checkouts.notePaneOpened(transport.OpenSpec{Kind: "", PaneID: "pane-person"}, "sess-x")

	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())
	if checkoutGone(t, checkout) {
		t.Fatalf("the sweep removed the checkout at %q after a pane opened in it, which moved its last-used to now", checkout)
	}
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 1 || rows[0].LastUsedAt != sweepNow.UnixMilli() {
		t.Fatalf("rows = %+v, %v; want the stamp at the pane-open time", rows, err)
	}
}

func TestTheSweepNeverRemovesACheckoutAPaneIsStandingIn(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	// A LIVE pane stands in the checkout: a session whose pane's recorded
	// directory is the checkout. Its open stamp aged out with the row, so
	// only the inventory of live panes can stop the sweep — and it must.
	stand.tabs.cwdOf["pane-live"] = checkout
	if _, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-live", Cwd: checkout,
	}); err != nil {
		t.Fatalf("open the live pane's session: %v", err)
	}
	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the sweep removed the checkout at %q out from under a live pane", checkout)
	}
	survey := stand.checkouts.Leftovers(context.Background(), string(coordA))
	if !survey.Complete || len(survey.Leftovers) != 1 || !survey.Leftovers[0].Expired ||
		survey.Leftovers[0].HoldReason != "pane-open" {
		t.Fatalf("holdings = %+v; want the checkout expired because a pane is open in it", survey)
	}
}

// paneAppearsFactory wraps the real factory: the FIRST ground read of a
// pass — the listing the removal judges against, read after the sweep has
// already sampled its pane inventory — is where the pane opens. A wrap and
// not a second stand, because the window the re-check closes is between
// real git reads, and everything under the wrap stays the real binary.
type paneAppearsFactory struct {
	inner    git.RepoFactory
	t        *testing.T
	stand    *checkoutStand
	checkout string
	appeared bool
}

func (f *paneAppearsFactory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	repo, outcome, err := f.inner.Open(ctx, cwd)
	if err != nil {
		return repo, outcome, err
	}
	return &paneAppearsRepo{Repo: repo, f: f}, outcome, nil
}

type paneAppearsRepo struct {
	git.Repo
	f *paneAppearsFactory
}

func (r *paneAppearsRepo) Log(ctx context.Context, limit int) (git.Log, error) {
	out, logErr := r.Repo.Log(ctx, limit)
	if !r.f.appeared {
		r.f.appeared = true
		r.f.stand.tabs.cwdOf["pane-mid"] = r.f.checkout
		if _, openErr := r.f.stand.reg.Open(context.Background(), session.Config{
			Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-mid", Cwd: r.f.checkout,
		}); openErr != nil {
			r.f.t.Fatalf("open the mid-sweep pane: %v", openErr)
		}
	}
	return out, logErr
}

// Criterion (nocx-xn63t.1 review, blocker 3): the pane inventory is a
// snapshot, and a pane can open in an expired checkout AFTER it is taken and
// BEFORE the removal runs — the checkout would come down under a running
// shell. The removal step re-checks the live panes immediately before each
// removal; this test opens the pane at exactly that moment — during the
// ground read, after the snapshot — and holds the sweep to the pane-open
// hold instead of the removal.
func TestTheSweepRechecksAPaneThatOpensAfterItsSnapshot(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	stand.checkouts.repos = &paneAppearsFactory{inner: stand.factory, t: t, stand: stand, checkout: checkout}

	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the sweep removed %q after a pane opened in it mid-sweep", checkout)
	}
	survey := stand.checkouts.Leftovers(context.Background(), string(coordA))
	if !survey.Complete || len(survey.Leftovers) != 1 || survey.Leftovers[0].HoldReason != "pane-open" {
		t.Fatalf("holdings = %+v; want the checkout held because a pane opened in it after the snapshot", survey)
	}
}

func TestTheSweepReadsThePeriodThroughTheSettingsRegistry(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	reg := settings.New(&appFakeDoc{}, nil)
	period := checkoutIdlePeriod(reg)
	sweeper := &checkoutSweeper{
		checkouts: stand.checkouts,
		sessions:  stand.reg,
		period:    period,
		now:       func() time.Time { return sweepNow },
	}
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	// The person sets the period to 0 in Settings — through the registry,
	// the seam the settings.set RPC writes — and the sweep obeys.
	if err := reg.SetNumber(settings.WorktreeIdleDays, 0); err != nil {
		t.Fatalf("set the period to 0: %v", err)
	}
	sweeper.RunOnce(context.Background())
	if checkoutGone(t, checkout) {
		t.Fatal("the sweep removed with the period at 0 set through the registry")
	}

	// The person sets it back to a day: the next sweep removes.
	if err := reg.SetNumber(settings.WorktreeIdleDays, 1); err != nil {
		t.Fatalf("set the period to 1: %v", err)
	}
	sweeper.RunOnce(context.Background())
	if !checkoutGone(t, checkout) {
		t.Fatal("the sweep left the checkout with the period at 1 day set through the registry")
	}
}

// Criterion (nocx-xn63t.1 review, finding 5): the setting is a number, and a
// fractional day is a FRACTION of a day — 0.5 is twelve hours, honoured
// rather than truncated. The old arithmetic converted days to a Duration
// before multiplying, which turned 0.5 into zero, and zero is the "never"
// setting: a person who asked for half a day of retention got none. The
// period read here is the production closure (checkoutIdlePeriod), not a
// second conversion beside it — a second conversion is exactly what let the
// truncation hide while this file's own test passed.
func TestTheSweepHonoursAFractionalDay(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	reg := settings.New(&appFakeDoc{}, nil)
	period := checkoutIdlePeriod(reg)
	sweeper := &checkoutSweeper{
		checkouts: stand.checkouts,
		sessions:  stand.reg,
		period:    period,
		now:       func() time.Time { return sweepNow },
	}

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-11*time.Hour))
	if err := reg.SetNumber(settings.WorktreeIdleDays, 0.5); err != nil {
		t.Fatalf("set the period to half a day: %v", err)
	}
	sweeper.RunOnce(context.Background())
	if checkoutGone(t, checkout) {
		t.Fatal("the sweep removed a checkout 11 hours old under a half-day period")
	}

	ageCheckout(t, stand, key, checkout, sweepNow.Add(-13*time.Hour))
	sweeper.RunOnce(context.Background())
	if !checkoutGone(t, checkout) {
		t.Fatal("the sweep left a checkout 13 hours old under a half-day period")
	}
}

func TestTheCheckoutSweepIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	if a.checkoutSweeper == nil {
		t.Fatal("New built no checkout sweeper")
	}
	if a.checkoutSweeper.checkouts != a.workerCheckouts {
		t.Fatal("the sweep removes through a different service than the one holdings reads")
	}
	if a.checkoutSweeper.sessions == nil {
		t.Fatal("the sweep was built without the live-session inventory")
	}
	if a.checkoutSweeper.period == nil {
		t.Fatal("the sweep was built without the settings-backed period")
	}
	if got := a.checkoutSweeper.period(); got != 30*24*time.Hour {
		t.Fatalf("period() = %v with no stored override, want the declared default 30 days", got)
	}
	if a.checkoutSweeper.period() <= 0 {
		t.Fatal("the default period is not positive")
	}
}

// Criterion: the sweep's nil-field contract — a partially wired service is
// the struct's honest absence, answered by removing nothing, never by a
// panic and never by an inventory that happens to read as empty.
func TestAPartiallyWiredSweepRemovesNothing(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	// No live-session inventory wired: a pane-held checkout cannot be told
	// from an abandoned one, so nothing is judged.
	broken := &workerCheckouts{
		repos:        stand.checkouts.repos,
		worktreeRoot: stand.checkouts.worktreeRoot,
		rows:         stand.checkouts.rows,
		held:         stand.record,
		layout:       stand.tabs,
	}
	sweeper := &checkoutSweeper{
		checkouts: broken,
		period:    func() time.Duration { return 30 * 24 * time.Hour },
		now:       func() time.Time { return sweepNow },
	}
	sweeper.RunOnce(context.Background())
	if checkoutGone(t, checkout) {
		t.Fatal("the sweep removed with the live-session inventory unwired")
	}
}

// Criterion: a pane directory that cannot be READ is unresolved, not
// "no panes" — the pass judges nothing and the checkout stays.
func TestAnUnreadablePaneInventoryJudgesNothing(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	// A live pane whose recorded directory cannot be read this pass.
	stand.tabs.cwdOf["pane-live"] = checkout
	live, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: "pane-live", Cwd: checkout,
	})
	if err != nil {
		t.Fatalf("open the live pane's session: %v", err)
	}
	defer func() { _ = stand.reg.Close(live.ID()) }()
	stand.tabs.paneCwdErrFor = map[string]error{"pane-live": errPaneCwdBroken}
	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatal("the sweep removed while the pane inventory was unreadable, which is unresolved, never safe")
	}
	survey := stand.checkouts.Leftovers(context.Background(), string(coordA))
	if !survey.Complete || len(survey.Leftovers) != 1 || survey.Leftovers[0].Expired {
		t.Fatalf("holdings = %+v; want the checkout unjudged, not marked expired", survey)
	}
}

// Criterion: the sweep runs at backend start — the first pass is
// synchronous in start, so the effect is visible the moment it returns —
// and the returned stop ends the cadence; calling it twice is nothing.
// The daily ticker itself is never waited on: it is production cadence.
func TestStartRunsTheFirstPassAtStartAndStopsCleanly(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	sweeper := newSweeper(stand, 30*24*time.Hour)
	stop := sweeper.start(log.NewSlogAdapter(nil))
	stop()
	if !checkoutGone(t, checkout) {
		t.Fatalf("the first pass at start did not remove the expired checkout at %q", checkout)
	}
}
