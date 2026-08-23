package shellintegration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the P3 implementation gate: the five bounds of design §7 and
// the assertions of §11 that measure them (27, 28, 29, 30, 31, 33). It
// extends the measurement harness in publisher_measure_test.go rather than
// building a second one — countingFS, faultFS and the scenario fixtures all
// come from there.
//
// No test here waits on a duration. Every deadline is driven by fakeClock,
// and every contention outcome is reached through an observable state
// change (a lock directory that disappears, a manifest that names a new
// generation), never through a sleep that is hoped to be long enough.

// fakeClock is the injected clock. Time moves only when the code under test
// asks to wait, and every wait it asked for is recorded — which is what
// makes "at most 1.55 s of injected time" an assertion rather than a
// stopwatch reading.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.waits = append(c.waits, d)
	now := c.t
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

// advance moves the clock without any wait having been asked for: the way a
// slow remote host consumes T while the publisher is inside one operation.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) waited() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.waits {
		total += d
	}
	return total
}

func (c *fakeClock) waitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waits)
}

// install replaces a publisher's clock seam. attempt() copies now/after
// when the attempt starts, so this must happen before the call under test.
func (c *fakeClock) install(p *Publisher) *fakeClock {
	p.now, p.after = c.Now, c.After
	return c
}

// fakeClockPublisher is newMeasuredPublisher with the clock injected: the
// stand every bound in this file is measured on.
func fakeClockPublisher(t *testing.T) (*Publisher, string, *countingFS, *faultFS, *fakeClock) {
	t.Helper()
	pub, root, cfs, ffs := newMeasuredPublisher(t)
	return pub, root, cfs, ffs, newFakeClock().install(pub)
}

// countOps counts the calls of one kind whose path is exactly path.
func countOps(m measuredTrace, kind, path string) int {
	n := 0
	for _, op := range m.ops {
		k, p, _ := strings.Cut(op, ":")
		if k == kind && p == path {
			n++
		}
	}
	return n
}

// plantLock plants a lock directory with a nonce naming a holder that never
// releases it: the crashed publisher every contention case starts from.
func plantLock(t *testing.T, root string) string {
	t.Helper()
	lockDir := filepath.Join(root, lockName)
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	// #nosec G306 — test fixture, deliberately restricted permissions.
	if err := os.WriteFile(filepath.Join(lockDir, lockNonceFile), []byte("another-writer"), 0o600); err != nil {
		t.Fatalf("plant nonce: %v", err)
	}
	return lockDir
}

// TestBoundedWorkConstantsAgree: N is derived from the bundle rather than
// typed in, and the probe schedule is the budget it claims to be. A fourth
// generation script must move N loudly — 101, not "still under 90".
func TestBoundedWorkConstantsAgree(t *testing.T) {
	if got := publishFSOpBudget(3); got != maxPublishFSOps {
		t.Errorf("publishFSOpBudget(3) = %d, want the source constant %d", got, maxPublishFSOps)
	}
	if got := publishFSOpBudget(4); got != 101 {
		t.Errorf("publishFSOpBudget(4) = %d, want 101 (a fourth script costs 5+2+2+2)", got)
	}
	if got := generationFileCount(launchBundle()); got != 3 {
		t.Fatalf("the shipped bundle has %d generation files; N was fixed at F=3", got)
	}
	var sum time.Duration
	for _, d := range lockProbeSchedule {
		sum += d
	}
	if sum != lockProbeBudget {
		t.Errorf("lockProbeSchedule sums to %v, but lockProbeBudget says %v", sum, lockProbeBudget)
	}
}

// TestMeasuredMaximumEqualsTheSourceConstant is §11 assertion 30, both
// halves. The first half — the measured maximum is at or below the ceiling
// — is what a ceiling with slack in it would also satisfy; the second half
// — the constant EQUALS the measured maximum — is what makes it a ratchet.
//
// The path measured is the worst one the five bounds still permit: one
// staging slot of three files plus its manifest temp to clear, an
// uncommitted generation at the target version to remove, a stale
// generation to sweep, the launch carrier to reinstall, a manifest to read,
// and a stale lock to probe K times and break.
func TestMeasuredMaximumEqualsTheSourceConstant(t *testing.T) {
	pub, root, cfs, _, clock := fakeClockPublisher(t)
	for _, v := range []string{"39", "40"} {
		if _, err := pub.Publish(prodBundle(v)); err != nil {
			t.Fatalf("stage %s: %v", v, err)
		}
	}
	if err := os.Remove(filepath.Join(root, launchName)); err != nil {
		t.Fatalf("remove launch: %v", err)
	}
	plantOrphans(t, filepath.Join(root, tmpName), 1, 3)
	// #nosec G306 — test fixture, deliberately restricted permissions.
	if err := os.WriteFile(filepath.Join(root, tmpName, "manifest-dead.tmp"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("plant manifest temp: %v", err)
	}
	plantGeneration(t, filepath.Join(root, integrationDir), "v41")
	plantLock(t, root)

	cfs.reset()
	res, err := pub.Publish(prodBundle("41"))
	if err != nil {
		t.Fatalf("worst permitted attempt: %v", err)
	}
	if !res.Published {
		t.Fatalf("worst permitted attempt did not publish: %+v", res)
	}
	m := cfs.snapshot()
	t.Logf("worst permitted attempt: %s", m)

	if m.total > maxPublishFSOps {
		t.Errorf("worst attempt = %d FS-seam calls, over the ceiling N = %d", m.total, maxPublishFSOps)
	}
	if m.total != maxPublishFSOps {
		t.Errorf("worst attempt = %d FS-seam calls; the constant in the source says %d — the two must move together",
			m.total, maxPublishFSOps)
	}
	if m.bytes > maxPublishBytes {
		t.Errorf("worst attempt wrote %d bytes, over B = %d", m.bytes, maxPublishBytes)
	}
	// The probes and the stale break are 7 of those 90, and they are
	// injected time, not elapsed time.
	if got := clock.waited(); got > lockProbeBudget {
		t.Errorf("lock probing waited %v of injected time, over the %v budget", got, lockProbeBudget)
	}
}

// TestEveryMeasuredPathIsUnderTheCeiling is the other half of assertion 30:
// no path measured by the gate — happy, faulted or contended — exceeds N.
func TestEveryMeasuredPathIsUnderTheCeiling(t *testing.T) {
	rows := append(measureHappyPaths(t), measureFaultPaths(t)...)
	for _, r := range rows {
		if r.m.total > maxPublishFSOps {
			t.Errorf("%s cost %d FS-seam calls, over N = %d", r.path, r.m.total, maxPublishFSOps)
		}
		if r.m.bytes > maxPublishBytes {
			t.Errorf("%s wrote %d bytes, over B = %d", r.path, r.m.bytes, maxPublishBytes)
		}
	}
}

// TestLockProbesAreBoundedAndInjected is §11 assertion 31. The retired loop
// cost one FS call per 25 ms poll — 200 metadata operations to break a
// stale lock and 400 for a waiter that is re-contended and publishes
// nothing. The bound is now structural: K probes, and 1.55 s of injected
// time, whatever the machine.
func TestLockProbesAreBoundedAndInjected(t *testing.T) {
	pub, root, cfs, _, clock := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	lockDir := plantLock(t, root)

	cfs.reset()
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("publish under a stale lock: %v", err)
	}
	m := cfs.snapshot()

	probes := countOps(m, "lstat", lockDir)
	// K probes, then the acquisition after the stale break lstats once more.
	if probes != lockProbes+1 {
		t.Errorf("lock was probed %d times; K = %d plus one acquisition after the break", probes, lockProbes)
	}
	if got := clock.waitCount(); got != lockProbes {
		t.Errorf("%d waits for %d probes", got, lockProbes)
	}
	if got := clock.waited(); got != lockProbeBudget {
		t.Errorf("probing waited %v of injected time, want exactly the %v budget", got, lockProbeBudget)
	}
	// The break is two removes and nothing else.
	if got := countOps(m, "remove", lockDir); got != 2 {
		t.Errorf("lock directory removed %d times (break + release), want 2", got)
	}
}

// TestSingleflightJoinsConcurrentCalls is §11 assertion 29's local half and
// the second clause of 31: a hundred concurrent calls for one destination
// produce ONE remote publish, and the joined waiters perform no remote
// operation at all — proven by the total FS-seam cost being the cost of a
// single publish, not of a hundred.
//
// The callers are made concurrent by construction rather than by hope: the
// leader is held inside its first seam call until every waiter has joined
// its flight, which is an observable count, and only then released. A test
// that merely started a hundred goroutines would measure the scheduler —
// on a loaded machine the leader finishes first and the stragglers become
// leaders of their own, which is correct behaviour and no evidence at all.
func TestSingleflightJoinsConcurrentCalls(t *testing.T) {
	// What one publish costs, alone.
	solo, _, scfs, _, _ := fakeClockPublisher(t)
	if _, err := solo.Publish(prodBundle("39")); err != nil {
		t.Fatalf("solo publish: %v", err)
	}
	want := scfs.snapshot()

	pub, root, cfs, _, _ := fakeClockPublisher(t)
	// The waiters block on the leader — an observable state change — with
	// T as a real safety net. An injected clock that grants every wait
	// instantly would expire T for a waiter before the leader had done
	// anything: a correct reading of that clock and the wrong stand here.
	pub.after = time.After

	const callers = 100
	results := make([]PublishResult, callers)
	errs := make([]error, callers)
	entered := make(chan struct{})
	gate := make(chan struct{})
	cfs.onCall = func(n int) {
		if n == 1 {
			close(entered)
			<-gate
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = pub.Publish(prodBundle("39"))
	}()
	<-entered // the leader is inside its attempt: the flight exists

	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = pub.Publish(prodBundle("39"))
		}(i)
	}
	// Join as one more caller to observe the count, then release the
	// leader once every waiter is in.
	flight, leader := joinPublishFlight(pub.fs, pub.root, prodBundle("39"))
	if leader {
		t.Fatal("the observer became the leader: no flight was in progress")
	}
	for flight.joined() < callers {
		runtime.Gosched()
	}
	close(gate)
	wg.Wait()
	cfs.onCall = nil

	got := cfs.snapshot()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if !results[i].Published || results[i].Generation != "v39" {
			t.Fatalf("caller %d did not receive the leader's result: %+v", i, results[i])
		}
	}
	if got.total != want.total {
		t.Errorf("%d concurrent calls cost %d FS-seam calls; one publish costs %d — the waiters probed",
			callers, got.total, want.total)
	}
	if got.calls["rename"] != 2 {
		t.Errorf("%d renames; exactly one generation and one manifest rename means one remote publish",
			got.calls["rename"])
	}
	if got.bytes != want.bytes {
		t.Errorf("%d concurrent calls wrote %d bytes; one publish writes %d", callers, got.bytes, want.bytes)
	}
	assertBoundedFootprint(t, root, "v39")
}

// TestSingleflightDoesNotJoinDifferentDestinations: the key is the seam
// value, the root AND the content digest. Joining two roots, two hosts or
// two bundles would report to one caller a publish that only ever reached
// the other.
func TestSingleflightDoesNotJoinDifferentDestinations(t *testing.T) {
	pub, _, _, _, _ := fakeClockPublisher(t)
	a, b := prodBundle("39"), prodBundle("40")
	if bundleDigest(a) == bundleDigest(b) {
		t.Fatal("two versions hashed to one digest")
	}
	fa, leaderA := joinPublishFlight(pub.fs, pub.root, a)
	if !leaderA {
		t.Fatal("the first call must lead")
	}
	if _, leader := joinPublishFlight(pub.fs, pub.root, b); !leader {
		t.Error("a different content digest was joined to another bundle's flight")
	}
	if _, leader := joinPublishFlight(pub.fs, pub.root+"-other", a); !leader {
		t.Error("a different root was joined to another destination's flight")
	}
	other := newCountingFS(NewOSFS())
	if _, leader := joinPublishFlight(other, pub.root, a); !leader {
		t.Error("a different seam value was joined to another destination's flight")
	}
	_ = fa.finish(PublishResult{}, nil)
	// And the same call joins.
	fb, leaderB := joinPublishFlight(pub.fs, pub.root, a)
	if !leaderB {
		t.Fatal("after the leader finished, the next call must lead")
	}
	if _, leader := joinPublishFlight(pub.fs, pub.root, a); leader {
		t.Error("an identical call was not joined to the flight in progress")
	}
	_ = fb.finish(PublishResult{}, nil)
}

// TestCrossProcessAtMostOneCommitPerDigest is §11 assertion 29's other
// half. Singleflight is per process and is not the boundary: these two
// publishers share no state but the filesystem, exactly as two instances of
// the application do. The mechanism under test is the version check
// REPEATED UNDER THE LOCK — the lock is released between attempts, so
// holding it is not by itself a guarantee of a single commit.
//
// The waiter is driven by an observable state change: its injected wait
// returns when the lock directory is gone, never after a duration.
func TestCrossProcessAtMostOneCommitPerDigest(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	lockDir := filepath.Join(root, lockName)

	leaderFS := newCountingFS(NewOSFS())
	waiterFS := newCountingFS(NewOSFS()) // a distinct seam value: no local join
	leader := NewPublisher(testLogger(), leaderFS, root)
	waiter := NewPublisher(testLogger(), waiterFS, root)

	leaderDone := make(chan struct{})
	// The waiter's wait ends when the lock is observably free, so the two
	// attempts serialise on the remote arbiter and on nothing else.
	waiter.after = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		go func() {
			for {
				if _, err := os.Lstat(lockDir); errors.Is(err, fs.ErrNotExist) {
					break
				}
				select {
				case <-leaderDone:
					ch <- time.Now()
					return
				default:
				}
			}
			ch <- time.Now()
		}()
		return ch
	}

	var wg sync.WaitGroup
	var leaderRes, waiterRes PublishResult
	var leaderErr, waiterErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(leaderDone)
		leaderRes, leaderErr = leader.Publish(prodBundle("39"))
	}()
	go func() {
		defer wg.Done()
		waiterRes, waiterErr = waiter.Publish(prodBundle("39"))
	}()
	wg.Wait()

	if leaderErr != nil {
		t.Fatalf("first publisher: %v", leaderErr)
	}
	if waiterErr != nil {
		t.Fatalf("second publisher: %v", waiterErr)
	}
	commits := 0
	for _, res := range []PublishResult{leaderRes, waiterRes} {
		if res.Published {
			commits++
		}
		if res.Generation != "v39" {
			t.Errorf("a publisher reported generation %q, want v39: %+v", res.Generation, res)
		}
	}
	if commits != 1 {
		t.Errorf("%d commits for one content digest across two processes, want exactly 1", commits)
	}
	// No torn state: the committed generation verifies against the bundle.
	assertBoundedFootprint(t, root, "v39")
	for _, f := range prodBundle("39").Files {
		if f.Name == launchName {
			continue
		}
		got := readFileT(t, filepath.Join(root, integrationDir, "v39", f.Name))
		if string(got) != string(f.Data) {
			t.Errorf("%s lost or corrupted bytes across the race", f.Name)
		}
	}
}

// TestVersionCheckIsRepeatedUnderTheLock pins the mechanism the test above
// relies on: the manifest is read AFTER the lock directory is created, not
// before. A check that precedes the acquisition proves nothing — another
// publisher may commit in between.
func TestVersionCheckIsRepeatedUnderTheLock(t *testing.T) {
	pub, root, cfs, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cfs.reset()
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("replacement: %v", err)
	}
	m := cfs.snapshot()
	lockTaken, manifestRead := -1, -1
	for i, op := range m.ops {
		kind, path, _ := strings.Cut(op, ":")
		if kind == "mkdir" && path == filepath.Join(root, lockName) && lockTaken < 0 {
			lockTaken = i
		}
		if kind == "readfile" && path == filepath.Join(root, manifestName) && manifestRead < 0 {
			manifestRead = i
		}
	}
	if lockTaken < 0 || manifestRead < 0 {
		t.Fatalf("trace has no lock acquisition (%d) or manifest read (%d)", lockTaken, manifestRead)
	}
	if manifestRead < lockTaken {
		t.Errorf("the version check at op %d precedes the lock acquisition at op %d", manifestRead, lockTaken)
	}
}

// TestContendedWithNothingCommittedIsNamed is §11 assertion 33. A lock that
// is re-taken the instant the stale rule frees it leaves the loser with
// nothing to fall back on: no generation was ever committed, so it may not
// simply proceed — the far side would find nothing and nobody would move
// the session out of `starting`. The outcome must be NAMED.
func TestContendedWithNothingCommittedIsNamed(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	if err := os.MkdirAll(filepath.Join(root, tmpName), 0o700); err != nil {
		t.Fatalf("plant root: %v", err)
	}
	squatter := &squatterFS{FS: NewOSFS(), lockDir: filepath.Join(root, lockName)}
	pub := NewPublisher(testLogger(), squatter, root)
	newFakeClock().install(pub)
	plantLock(t, root)

	res, err := pub.Publish(prodBundle("39"))
	if err == nil {
		t.Fatalf("a permanently contended lock must not report success: %+v", res)
	}
	var ce *ContendedError
	if !errors.As(err, &ce) {
		t.Fatalf("contention produced %T: %v", err, err)
	}
	if res.Reason != ReasonContended {
		t.Errorf("outcome reason = %q, want the named %q", res.Reason, ReasonContended)
	}
	if !strings.Contains(err.Error(), ReasonContended) {
		t.Errorf("the error does not name the outcome: %v", err)
	}
	if res.Published {
		t.Error("a contended attempt reported Published")
	}
	if _, statErr := os.Stat(filepath.Join(root, manifestName)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a contended attempt with nothing committed wrote a manifest: %v", statErr)
	}
}

// TestContendedWithACommittedGenerationUsesIt is the other branch of §6.3:
// the loser uses a verified older generation when one exists, so the
// session integrates instead of degrading.
func TestContendedWithACommittedGenerationUsesIt(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	first := NewPublisher(testLogger(), NewOSFS(), root)
	if _, err := first.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	squatter := &squatterFS{FS: NewOSFS(), lockDir: filepath.Join(root, lockName)}
	pub := NewPublisher(testLogger(), squatter, root)
	newFakeClock().install(pub)
	plantLock(t, root)

	res, err := pub.Publish(prodBundle("40"))
	if err != nil {
		t.Fatalf("a contended attempt over a committed generation must not fail: %v", err)
	}
	if res.Published {
		t.Errorf("a contended attempt reported a publish: %+v", res)
	}
	if res.Reason != ReasonContendedExisting {
		t.Errorf("reason = %q, want %q", res.Reason, ReasonContendedExisting)
	}
	if res.Generation != "v39" || res.Version != "39" {
		t.Errorf("the fallback names %+v, want the committed v39", res)
	}
	if got := readManifestT(t, root).Generation; got != "v39" {
		t.Errorf("the contended attempt moved the activation to %s", got)
	}
}

// squatterFS re-takes the lock directory the instant it is removed. It is
// the deterministic form of "another process is holding this and will not
// let go" — no goroutine racing, no sleep: the re-take happens inside the
// Remove that frees it, so the outcome is the same on every machine.
type squatterFS struct {
	FS
	lockDir string
}

func (s *squatterFS) Remove(path string) error {
	err := s.FS.Remove(path)
	if err == nil && path == s.lockDir {
		_ = s.FS.Mkdir(s.lockDir, 0o700)
	}
	return err
}

// TestSecondAttemptAgainstUnclearableResidueCreatesNoSecondSlot is §11
// assertion 28 and bound 1. Residue that cannot be removed refuses the
// write: the alternative — staging beside it — is how one bounded attempt
// becomes an unbounded total.
func TestSecondAttemptAgainstUnclearableResidueCreatesNoSecondSlot(t *testing.T) {
	pub, root, _, ffs, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	before := readFileT(t, filepath.Join(root, manifestName))

	// Residue from an attempt that died before commit, and a seam that
	// refuses to remove it.
	plantOrphans(t, filepath.Join(root, tmpName), 1, 2)
	ffs.setFaultPath("remove", filepath.Join(root, tmpName, "orphan00"), errInjected)

	for attempt := 1; attempt <= 2; attempt++ {
		res, err := pub.Publish(prodBundle("40"))
		if err == nil {
			t.Fatalf("attempt %d wrote against residue it could not clear: %+v", attempt, res)
		}
		var re *ResidueError
		if !errors.As(err, &re) {
			t.Fatalf("attempt %d: want a ResidueError, got %T: %v", attempt, err, err)
		}
		if res.Reason != "" && res.Reason != ReasonResidue {
			t.Errorf("attempt %d reason = %q", attempt, res.Reason)
		}
		entries, rerr := os.ReadDir(filepath.Join(root, tmpName))
		if rerr != nil {
			t.Fatalf("readdir tmp: %v", rerr)
		}
		if len(entries) != 1 || entries[0].Name() != "orphan00" {
			t.Fatalf("attempt %d created a second staging slot: %v", attempt, names(entries))
		}
		if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
			t.Fatalf("attempt %d changed the committed manifest", attempt)
		}
	}

	// And it converges the moment the residue can be cleared.
	ffs.setFaultPath("remove", "", nil)
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("retry after the residue became clearable: %v", err)
	}
	assertBoundedFootprint(t, root, "v40")
}

// TestStagingResidueBeyondOneSlotIsBounded: whatever tmp/ holds, one
// attempt clears at most one staging slot's worth of it — and converges
// over attempts rather than paying for all of it at once or refusing
// forever. tmp/ is also the sh publisher's staging area, so residue this
// process never wrote is a real state a host can be in.
func TestStagingResidueBeyondOneSlotIsBounded(t *testing.T) {
	pub, root, cfs, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	plantOrphans(t, filepath.Join(root, tmpName), 6, 2)

	cfs.reset()
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("publish over six orphans: %v", err)
	}
	m := cfs.snapshot()
	if m.total > maxPublishFSOps {
		t.Errorf("an attempt over six orphans cost %d FS-seam calls, over N = %d", m.total, maxPublishFSOps)
	}
	left, err := os.ReadDir(filepath.Join(root, tmpName))
	if err != nil {
		t.Fatalf("readdir tmp: %v", err)
	}
	if len(left) != 6-maxStagingSlotEntries {
		t.Fatalf("cleared %d entries, want exactly %d per attempt: %v",
			6-len(left), maxStagingSlotEntries, names(left))
	}

	// Convergence: each further attempt clears its slot's worth, and no
	// attempt pays for more.
	for i := range 3 {
		if _, perr := pub.Publish(prodBundle(fmt.Sprintf("4%d", i+1))); perr != nil {
			t.Fatalf("converging publish %d: %v", i, perr)
		}
	}
	left, err = os.ReadDir(filepath.Join(root, tmpName))
	if err != nil {
		t.Fatalf("readdir tmp: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("tmp/ still holds %v after four attempts", names(left))
	}
}

// TestRemoveTreeRefusesResidueTheLayoutCannotProduce is bound 4. A
// directory tree planted under tmp/ or integration/ — by a previous
// protocol, by a user, by another program — was traversed to whatever depth
// it had, on the publish path, under the lock. Now it is refused, and the
// refusal is a named residue outcome rather than an unbounded walk.
func TestRemoveTreeRefusesResidueTheLayoutCannotProduce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "nested deeper than the layout produces",
			plant: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(dir, "deep", "deeper"), 0o700); err != nil {
					t.Fatalf("plant: %v", err)
				}
			},
			want: "nested deeper",
		},
		{
			name: "wider than the layout produces",
			plant: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "wide"), 0o700); err != nil {
					t.Fatalf("plant: %v", err)
				}
				for i := range maxResidueEntries + 1 {
					// #nosec G306 — test fixture, deliberately restricted permissions.
					if err := os.WriteFile(filepath.Join(dir, "wide", fmt.Sprintf("f%02d", i)), []byte("x"), 0o600); err != nil {
						t.Fatalf("plant: %v", err)
					}
				}
			},
			want: "holds 9 entries",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, root, cfs, _, _ := fakeClockPublisher(t)
			if _, err := pub.Publish(prodBundle("39")); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			before := readFileT(t, filepath.Join(root, manifestName))
			tc.plant(t, filepath.Join(root, tmpName))

			cfs.reset()
			_, err := pub.Publish(prodBundle("40"))
			var re *ResidueError
			if !errors.As(err, &re) {
				t.Fatalf("want a ResidueError, got %T: %v", err, err)
			}
			if re.Reason == "" {
				t.Error("the residue refusal named no reason")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name the bound (%q): %v", tc.want, err)
			}
			m := cfs.snapshot()
			if m.total > maxPublishFSOps {
				t.Errorf("the refusal itself cost %d FS-seam calls", m.total)
			}
			if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
				t.Error("a refused attempt changed the committed manifest")
			}
		})
	}
}

// TestAtMostOneStaleGenerationSweptPerAttempt is bound 3. The keep-two
// policy implied it and did not enforce it: nine generations swept seven in
// one attempt, under the lock. Bounded, the same host converges one per
// attempt — including on the no-op path, so convergence does not wait for
// the next version bump.
func TestAtMostOneStaleGenerationSweptPerAttempt(t *testing.T) {
	pub, root, cfs, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	integration := filepath.Join(root, integrationDir)
	for _, v := range []string{"v10", "v11", "v12", "v13"} {
		plantGeneration(t, integration, v)
	}

	cfs.reset()
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("replacement: %v", err)
	}
	m := cfs.snapshot()
	if m.total > maxPublishFSOps {
		t.Errorf("an attempt over five stale generations cost %d FS-seam calls, over N = %d", m.total, maxPublishFSOps)
	}
	// v39, v10..v13 were there; v40 is committed; exactly one goes.
	gens := readDirNamesT(t, integration)
	if len(gens) != 5 {
		t.Fatalf("after one attempt integration/ holds %v, want exactly one entry swept", gens)
	}
	if containsName(gens, "v10") {
		t.Errorf("the oldest generation was not the one swept: %v", gens)
	}

	// Convergence, on the no-op path too: the same version publishes
	// nothing and still sweeps one.
	for range 3 {
		if _, err := pub.Publish(prodBundle("40")); err != nil {
			t.Fatalf("no-op attempt: %v", err)
		}
	}
	gens = readDirNamesT(t, integration)
	if len(gens) != 2 {
		t.Errorf("integration/ holds %v after four attempts, want the keep-two footprint", gens)
	}
}

func readDirNamesT(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	return names(entries)
}

func containsName(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestPublishDeadlineInitiatesNoFurtherRemoteOperation is T. Expiring means
// exactly this and nothing more: no new remote operation is initiated, the
// attempt fails with a named reason, the previous activation is untouched —
// and the shell still starts, which is the carrier's half. It is not a
// promise to destroy kernel I/O in an uninterruptible state: the operation
// in flight when the deadline passes runs to its own completion.
func TestPublishDeadlineInitiatesNoFurtherRemoteOperation(t *testing.T) {
	pub, root, cfs, _, clock := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	before := readFileT(t, filepath.Join(root, manifestName))

	// A remote host that consumes the whole budget inside one operation:
	// the clock is advanced from the seam itself, after the tenth call.
	const consumeAfter = 10
	cfs.reset()
	cfs.onCall = func(n int) {
		if n == consumeAfter {
			clock.advance(publishDeadline + time.Second)
		}
	}
	res, err := pub.Publish(prodBundle("40"))
	cfs.onCall = nil

	var de *DeadlineError
	if !errors.As(err, &de) {
		t.Fatalf("want a DeadlineError once T expired, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), ReasonTimeout) {
		t.Errorf("the error does not name the outcome: %v", err)
	}
	if res.Published {
		t.Errorf("an expired attempt reported a publish: %+v", res)
	}
	if got := cfs.snapshot().total; got != consumeAfter {
		t.Errorf("%d FS-seam calls were issued; the %dth consumed T, so no further one may be initiated", got, consumeAfter)
	}
	if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
		t.Error("an expired attempt changed the committed manifest")
	}
	// The next attempt, with time again, converges with no manual cleanup.
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("retry after the deadline expired: %v", err)
	}
	assertBoundedFootprint(t, root, "v40")
}

// TestUncommittedGenerationRemovalIsBoundedToOne is bound 2 at its guard.
// Only one uncommitted generation — the one at the target version — may be
// removed per attempt; a second means residue is accumulating rather than
// converging, and the attempt refuses rather than clearing it. The bound is
// asserted at the counter because the call graph currently reaches it once:
// the guard exists so that a future second caller fails loudly instead of
// quietly doubling the work an attempt does under the lock.
func TestUncommittedGenerationRemovalIsBoundedToOne(t *testing.T) {
	pub, root, _, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	plantGeneration(t, filepath.Join(root, integrationDir), "v40")

	ap := pub.attempt()
	ap.at.uncommit = maxUncommittedPerAttempt
	err := ap.commitGeneration(prodBundle("40"), "nonce-that-is-never-renamed")
	var re *ResidueError
	if !errors.As(err, &re) {
		t.Fatalf("the second uncommitted removal must refuse, got %T: %v", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, integrationDir, "v40")); statErr != nil {
		t.Errorf("the refused attempt removed the generation anyway: %v", statErr)
	}
}

// TestResidueBudgetsAreCountedSeparately: §7 asks inspected and removed
// entries to be bounded apart. They are equal in the good case and diverge
// exactly where a bound bites — a refusal inspects an entry and removes
// nothing — so a test that only watched one of them would not see it.
func TestResidueBudgetsAreCountedSeparately(t *testing.T) {
	pub, root, _, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	plantOrphans(t, filepath.Join(root, tmpName), 1, 3)

	ap := pub.attempt()
	if err := ap.clearStagingSlot(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if ap.at.inspected != 4 || ap.at.removed != 4 {
		t.Fatalf("clearing one slot of three files inspected %d and removed %d, want 4 and 4",
			ap.at.inspected, ap.at.removed)
	}

	// A refusal inspects and does not remove.
	deep := filepath.Join(root, tmpName, "deep")
	if err := os.MkdirAll(filepath.Join(deep, "deeper"), 0o700); err != nil {
		t.Fatalf("plant: %v", err)
	}
	ap2 := pub.attempt()
	if err := ap2.clearStagingSlot(); err == nil {
		t.Fatal("a nested tree must refuse")
	}
	if ap2.at.removed != 0 {
		t.Errorf("a refusal removed %d entries", ap2.at.removed)
	}
	if ap2.at.inspected == 0 {
		t.Error("a refusal inspected nothing")
	}
	if ap2.at.inspected > maxResidueInspected || ap2.at.removed > maxResidueRemoved {
		t.Errorf("budgets exceeded: inspected %d, removed %d", ap2.at.inspected, ap2.at.removed)
	}
}

// TestPanickingLeaderReleasesItsWaiters: a leader that dies without
// publishing a result would leave every caller joined to its flight blocked
// until T — one failure turned into as many stalled sessions as happened to
// be joined. newNonce panics when the machine has no entropy, so this is a
// path the product has, not an invented one.
//
// The waiter's wait never fires here on purpose: if the flight were not
// finished, this test hangs rather than passing by accident on a timeout
// that happens to name the right outcome.
func TestPanickingLeaderReleasesItsWaiters(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	fsys := &panicOnceFS{FS: NewOSFS(), gate: make(chan struct{}), entered: make(chan struct{})}

	leader := NewPublisher(testLogger(), fsys, root)
	waiter := NewPublisher(testLogger(), fsys, root)
	waiter.after = func(time.Duration) <-chan time.Time { return make(chan time.Time) }

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r == nil {
				t.Error("the leader's panic did not reach its caller")
			}
		}()
		_, _ = leader.Publish(prodBundle("39"))
	}()
	<-fsys.entered

	var waiterErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, waiterErr = waiter.Publish(prodBundle("39"))
	}()
	flight, isLeader := joinPublishFlight(fsys, root, prodBundle("39"))
	if isLeader {
		t.Fatal("the observer became the leader: no flight was in progress")
	}
	for flight.joined() < 2 { // the waiter and this observer
		runtime.Gosched()
	}
	close(fsys.gate)
	wg.Wait()

	if waiterErr == nil {
		t.Fatal("the waiter of a panicking leader reported success")
	}
	if !strings.Contains(waiterErr.Error(), "panicked") {
		t.Errorf("the waiter's error does not say what happened: %v", waiterErr)
	}
}

// panicOnceFS panics on its first call, after letting the test see that the
// call was reached.
type panicOnceFS struct {
	FS
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (f *panicOnceFS) Lstat(path string) (fs.FileInfo, error) {
	f.once.Do(func() {
		close(f.entered)
		<-f.gate
		panic("shellintegration: no entropy for publish nonce")
	})
	return f.FS.Lstat(path)
}

// TestShippedBundleFitsTheByteCeiling is B. The margin is 4.6x only
// because the bundle is stripped: the sources are 145,726 bytes and
// stripShellComments takes them to 56,916, so a change that disabled
// stripping would leave the payload at 1.8x rather than 4.6x — still under
// B, and no longer the number anyone reasoned about. Both facts are
// asserted, because the second is what makes the first stable.
func TestShippedBundleFitsTheByteCeiling(t *testing.T) {
	pub, _, cfs, _, _ := fakeClockPublisher(t)
	if _, err := pub.Publish(launchBundle()); err != nil {
		t.Fatalf("first contact: %v", err)
	}
	m := cfs.snapshot()
	if m.bytes > maxPublishBytes {
		t.Errorf("a first-contact publish issued %d bytes, over B = %d", m.bytes, maxPublishBytes)
	}
	t.Logf("first contact issued %d bytes in %d writes (B = %d, %.1fx headroom)",
		m.bytes, m.calls["write"], maxPublishBytes, float64(maxPublishBytes)/float64(m.bytes))

	raw := len(bashScriptRaw) + len(zshScriptRaw) + len(posixScriptRaw)
	stripped := len(bashScript) + len(zshScript) + len(posixScript)
	if stripped >= raw {
		t.Errorf("the shipped generation files are not stripped: %d bytes of %d raw", stripped, raw)
	}
	// A publish also reads. If B is a wire budget it must cover both.
	vcfs := newCountingFS(NewOSFS())
	vpub := NewPublisher(testLogger(), vcfs, pub.root)
	if vr, err := vpub.Verify(); err != nil || !vr.Installed {
		t.Fatalf("Verify = %+v, %v", vr, err)
	}
	v := vcfs.snapshot()
	if total := m.bytes + m.read + v.read; total > maxPublishBytes {
		t.Errorf("a verify-then-publish attempt moves %d bytes, over B = %d", total, maxPublishBytes)
	} else {
		t.Logf("verify-then-publish moves %d bytes in total (%.1fx headroom)",
			total, float64(maxPublishBytes)/float64(total))
	}
}

// joined reports how many callers are waiting on this flight, read under the
// lock that guards the counter.
//
// It is an OBSERVATION and lives with the tests that make it: the publisher
// itself never reads the live count — finish returns it, once, for the log —
// so a production accessor would be a function with no production caller, and
// the dead-code ratchet is configured to say so.
//
// The tests below spin on it rather than sleeping, which is the point: "the
// leader is held until every waiter has joined" is an event, and waiting for
// an event is what AGENTS.md requires of a test that must not depend on
// timing.
func (f *publishFlight) joined() int {
	publishFlights.mu.Lock()
	defer publishFlights.mu.Unlock()
	return f.waiters
}
