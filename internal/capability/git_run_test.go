package capability

import (
	"context"
	"errors"
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
