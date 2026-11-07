package capability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/schedule"
)

func testGitBulkhead() *schedule.Bulkhead {
	return schedule.NewBulkhead("git", schedule.Limits{
		Capacity: 8, PersonReserved: 2, AbandonedPerDomain: 8, AbandonedGlobal: 32,
	})
}

func testAllowance(turn, max time.Duration) gitAllowance {
	return gitAllowance{Operation: "git.stage", Turn: turn, Max: max, Expired: catalog.ExpiredOutcomeUnknown}
}

// A call that gets its turn starts, and its work's ctx is live until it
// completes.
func TestGitRunStartsAfterItsTurnAndCompletes(t *testing.T) {
	var ran atomic.Bool
	var finished atomic.Int32
	allow := testAllowance(time.Second, time.Second)
	err := gitRun(context.Background(), allow, func(context.Context) (func(), func(), error) {
		return func() { finished.Add(1) }, func() { t.Error("abandoned a completed call") }, nil
	}, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Errorf("work ctx already dead: %v", err)
		}
		ran.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("gitRun: %v", err)
	}
	if !ran.Load() {
		t.Fatal("the work never ran")
	}
	if got := finished.Load(); got != 1 {
		t.Fatalf("finish called %d times, want 1", got)
	}
}

// A turn that expires while the worker waits for the checkout means the call
// never starts: no work, no permit, and the scope records not-started.
func TestGitRunTurnExpiryNeverStarts(t *testing.T) {
	scope := catalog.Begin(context.Background(), catalog.OpGitStage, catalog.Invocation{})
	ctx := catalog.WithScope(context.Background(), scope)
	var ran atomic.Bool
	allow := testAllowance(30*time.Millisecond, time.Second)

	start := make(chan struct{})
	err := gitRun(ctx, allow, func(actx context.Context) (func(), func(), error) {
		close(start)
		<-actx.Done() // the queue: the turn deadline arrives here
		return nil, nil, actx.Err()
	}, func(context.Context) error {
		ran.Store(true)
		return nil
	})
	var notStarted *NotStartedError
	if !errors.As(err, &notStarted) {
		t.Fatalf("gitRun err = %v, want a NotStartedError", err)
	}
	select {
	case <-start:
	default:
		t.Fatal("acquire was never called")
	}
	if ran.Load() {
		t.Fatal("work ran after its turn expired")
	}
	if out, settled := scope.Outcome(); !settled || out != catalog.OutcomeNotStarted {
		t.Fatalf("scope outcome = %v (settled %v), want not-started", out, settled)
	}
}

// A worker whose request settled at its max keeps its permit until the work
// returns; the answer is the declaration's expiry, and the worker is told to
// abandon when the caller answers.
func TestGitRunMaxExpiryAbandonsTheWorker(t *testing.T) {
	var abandoned atomic.Int32
	var finished atomic.Int32
	workDone := make(chan error, 1)
	allow := testAllowance(time.Second, 60*time.Millisecond)

	err := gitRun(context.Background(), allow, func(context.Context) (func(), func(), error) {
		return func() { finished.Add(1) }, func() { abandoned.Add(1) }, nil
	}, func(ctx context.Context) error {
		<-ctx.Done() // a hung provider that honours cancellation
		workDone <- ctx.Err()
		return ctx.Err()
	})
	var expired *ExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("gitRun err = %v, want an ExpiredError", err)
	}
	if expired.Outcome != catalog.ExpiredOutcomeUnknown {
		t.Fatalf("expired outcome = %q, want outcome-unknown", expired.Outcome)
	}
	if got := abandoned.Load(); got != 1 {
		t.Fatalf("abandon called %d times, want 1", got)
	}
	if werr := <-workDone; !errors.Is(werr, context.DeadlineExceeded) {
		t.Fatalf("work ctx err = %v, want the work deadline", werr)
	}
	// The permit returns only when the worker returns; give the worker its
	// moment and assert the finish happened once.
	deadline := time.Now().Add(2 * time.Second)
	for finished.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := finished.Load(); got != 1 {
		t.Fatalf("finish called %d times after the worker returned, want 1", got)
	}
}

// A cancelled request is not an expiry: the backend reports the cancellation
// and does not pretend the deadline passed.
func TestGitRunCallerCancellationIsNotAnExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	allow := testAllowance(time.Second, time.Second)
	started := make(chan struct{})
	var abandons atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- gitRun(ctx, allow, func(context.Context) (func(), func(), error) {
			return func() {}, func() { abandons.Add(1) }, nil
		}, func(wctx context.Context) error {
			close(started)
			<-wctx.Done()
			return wctx.Err()
		})
	}()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gitRun err = %v, want context.Canceled", err)
	}
	if abandons.Load() != 0 {
		t.Fatal("a cancelled call was recorded as an abandoned worker")
	}
}

// Acquire is the acquisition's own failure — a suspect domain is an expiry,
// and the turn budget is measured against the wait.
func TestGitRunAcquireFailurePassesThrough(t *testing.T) {
	want := errors.New("domain suspect")
	err := gitRun(context.Background(), testAllowance(time.Second, time.Second),
		func(context.Context) (func(), func(), error) { return nil, nil, want },
		func(context.Context) error { t.Fatal("work ran after a failed acquire"); return nil })
	if !errors.Is(err, want) {
		t.Fatalf("gitRun err = %v, want the acquire failure", err)
	}
}

// ── who decides that a call started ──────────────────────────────────────

// A start the worker claimed is never overridden by the caller's turn: this
// is the race the two selects lost. The worker acquired, looked at the turn,
// published its start and entered the mutation; then the caller's select had
// BOTH arms ready and could take its deadline arm, answering "did not start —
// nothing changed" while the mutation ran (ADR-8eej3e66 §4).
func TestAStartTheWorkerClaimedIsNeverAnsweredNotStarted(t *testing.T) {
	d := newStartDecision()
	want := gitStart{expired: make(chan struct{})}
	if !d.begin(want) {
		t.Fatal("the worker's claim was refused with nobody else settled")
	}
	if d.expire(context.DeadlineExceeded) {
		t.Fatal("the caller's turn overrode a start the worker had already claimed")
	}
	got, err := d.outcome()
	if err != nil {
		t.Fatalf("outcome after a claimed start = %v, want the start", err)
	}
	if got.expired != want.expired {
		t.Fatal("the caller did not read the start the worker published")
	}
}

// The other order: a caller whose turn ran out first owns the answer, and the
// worker that arrives afterwards must not run anything.
func TestATurnThatExpiredFirstRefusesTheLateWorker(t *testing.T) {
	d := newStartDecision()
	if !d.expire(context.DeadlineExceeded) {
		t.Fatal("the expiry did not settle a decision nobody else had taken")
	}
	if d.begin(gitStart{expired: make(chan struct{})}) {
		t.Fatal("a worker started after the caller had answered that nothing did")
	}
	if _, err := d.outcome(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("outcome = %v, want the turn's deadline", err)
	}
	if d.expire(errors.New("second")) {
		t.Fatal("a settled decision was settled a second time")
	}
}

// A worker whose acquisition failed answers with its own reason, and a caller
// whose turn ends after that reads the worker's reason rather than its own.
func TestAFailedAcquisitionSettlesTheDecisionOnce(t *testing.T) {
	d := newStartDecision()
	want := errors.New("domain suspect")
	d.fail(want)
	if d.expire(context.DeadlineExceeded) {
		t.Fatal("the caller's turn overrode the worker's own failure")
	}
	if _, err := d.outcome(); !errors.Is(err, want) {
		t.Fatalf("outcome = %v, want the acquire failure", err)
	}
	if d.begin(gitStart{expired: make(chan struct{})}) {
		t.Fatal("the work started after its acquisition failed")
	}
}

// Exactly one side settles, however the two arrive: a worker claiming its
// start and a caller whose turn ran out, raced a hundred times under -race.
// Nothing here can be true of two independent selects — that shape let both
// of them answer.
func TestTheStartDecisionSettlesExactlyOnceUnderContention(t *testing.T) {
	for round := 0; round < 100; round++ {
		d := newStartDecision()
		claims := make(chan bool, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			claims <- d.begin(gitStart{expired: make(chan struct{})})
		}()
		go func() {
			defer wg.Done()
			claims <- d.expire(context.DeadlineExceeded)
		}()
		wg.Wait()
		close(claims)
		wins := 0
		for ok := range claims {
			if ok {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d sides settled one decision, want exactly 1", round, wins)
		}
		<-d.ready // settled, so the caller may read it
		start, err := d.outcome()
		if (err == nil) != (start.expired != nil) {
			t.Fatalf("round %d: the decision carries a start and an error, or neither: %v", round, err)
		}
	}
}
