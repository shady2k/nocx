package domains

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

// testOptions is the smallest registry that shows each rule: two permits per
// domain with one reserved for a person, two abandoned workers per domain, a
// global abandoned bound of four with one of them reserved for healthy
// domains.
func testOptions() Options {
	return Options{
		PerDomain:          2,
		PersonReserve:      1,
		AbandonedPerDomain: 2,
		AbandonedGlobal:    4,
		HealthyReserve:     1,
	}
}

func mount(name string) Key { return Key{Kind: "mount", ID: name} }

// blockingRun returns a Run that blocks until release is closed, and reports
// whether it was ever called.
func blockingRun(started chan<- struct{}, release <-chan struct{}) func(context.Context) error {
	return func(context.Context) error {
		if started != nil {
			started <- struct{}{}
		}
		<-release
		return nil
	}
}

// workBound is the Request.Work factory for a test that is not about the
// grant: a fresh work context with its own deadline, made when the permit is
// taken — which is the moment the real caller makes it too.
func workBound(d time.Duration) func() (context.Context, context.CancelFunc) {
	return func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), d)
	}
}

// settledWork hands Do a work context the test itself cancels, which is how a
// call settles while its worker keeps running and is counted abandoned.
func settledWork(ctx context.Context) func() (context.Context, context.CancelFunc) {
	return func() (context.Context, context.CancelFunc) { return ctx, func() {} }
}

func turnCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// TestAHealthyDomainCompletesBesideABlockedOne is the failure-domain rule at
// its smallest: one mount's worker never returns, and a second mount's call
// still completes. Without bulkheads the blocked call would occupy the one
// shared permit.
func TestAHealthyDomainCompletesBesideABlockedOne(t *testing.T) {
	r := New(testOptions())
	release := make(chan struct{})
	blocked := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = r.Do(Request{
			Key: mount("hung"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second), Work: workBound(time.Minute),
			Run: blockingRun(blocked, release),
		})
	}()
	<-blocked

	done := make(chan error, 1)
	go func() {
		done <- r.Do(Request{
			Key: mount("healthy"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second),
			Work: workBound(time.Second),
			Run: func(context.Context) error {
				return nil
			},
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("healthy mount: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a healthy mount's call was blocked by another mount's hung worker")
	}
	close(release)
	wg.Wait()
}

// TestBackgroundCannotTakeTheLastPermitFromAPerson proves the class
// reservation: background work fills its ceiling below the whole and a
// person's call still gets a permit.
func TestBackgroundCannotTakeTheLastPermitFromAPerson(t *testing.T) {
	r := New(testOptions())
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	for range 2 {
		go func() {
			_ = r.Do(Request{
				Key: mount("m"), Origin: catalog.OriginBackground,
				Turn: turnCtx(t, time.Second), Work: workBound(time.Minute),
				Run: func(context.Context) error {
					started <- struct{}{}
					<-release
					return nil
				},
			})
		}()
	}
	// The second background worker must wait: the ceiling is one.
	<-started
	select {
	case <-started:
		t.Fatal("background work took a second permit; the ceiling is one")
	case <-time.After(100 * time.Millisecond):
	}
	if got := r.Stats().Running; got != 1 {
		t.Fatalf("background running workers = %d, want 1", got)
	}
	person := make(chan error, 1)
	go func() {
		person <- r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second),
			Work: workBound(time.Second),
			Run:  func(context.Context) error { return nil },
		})
	}()
	select {
	case err := <-person:
		if err != nil {
			t.Fatalf("person's call: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background work took the permit reserved for a person")
	}
	close(release)
}

// TestADomainAtItsAbandonedBoundRefusesAtOnce is the breaker: the call that
// settles at its deadline leaves its worker running, and once the domain
// holds its abandoned bound a further call settles without starting work.
func TestADomainAtItsAbandonedBoundRefusesAtOnce(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 4
	r := New(opts)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	for i := range opts.AbandonedPerDomain {
		workCtx, cancel := context.WithCancel(context.Background())
		go func() {
			_ = r.Do(Request{
				Key: mount("hung"), Origin: catalog.OriginPerson,
				Turn: turnCtx(t, time.Second), Work: settledWork(workCtx),
				Run: blockingRun(nil, release),
			})
		}()
		// Wait for the worker to take its permit, then let the work expiry
		// settle the call while the worker keeps running.
		if err := waitFor(t, "worker to start", func() bool { return r.Stats().Running == i+1 }); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := waitFor(t, "worker to be counted abandoned", func() bool { return r.Stats().Abandoned == i+1 }); err != nil {
			t.Fatal(err)
		}
	}

	called := atomic.Bool{}
	start := time.Now()
	err := r.Do(Request{
		Key: mount("hung"), Origin: catalog.OriginPerson,
		Turn: turnCtx(t, time.Second), Work: workBound(time.Second),
		Run: func(context.Context) error {
			called.Store(true)
			return nil
		},
	})
	if err == nil {
		t.Fatal("a call on a suspect domain succeeded")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || !errors.Is(err, ErrDomainSuspect) {
		t.Fatalf("error = %v, want a *Refusal for a suspect domain", err)
	}
	if refusal.Domain != mount("hung") {
		t.Errorf("refusal names %q, want %q", refusal.Domain, mount("hung"))
	}
	if called.Load() {
		t.Error("a refused call started its work")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("a suspect domain's call took %v; it must settle at once", elapsed)
	}
}

// TestTheGlobalAbandonedBoundStopsWorkersEverywhere is the second half of the
// breaker: with the global bound reached, a call on a domain that has
// abandoned nothing is refused too, so changing host ids cannot grow the
// abandoned set without limit.
func TestTheGlobalAbandonedBoundStopsWorkersEverywhere(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 4
	opts.AbandonedPerDomain = 4
	opts.AbandonedGlobal = 2
	opts.HealthyReserve = 1
	r := New(opts)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	abandon := func(key Key, want int) {
		workCtx, cancel := context.WithCancel(context.Background())
		go func() {
			_ = r.Do(Request{
				Key: key, Origin: catalog.OriginPerson,
				Turn: turnCtx(t, time.Second), Work: settledWork(workCtx),
				Run: blockingRun(nil, release),
			})
		}()
		if err := waitFor(t, "worker start", func() bool { return r.Stats().Running == want }); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if err := waitFor(t, "abandonment", func() bool { return r.Stats().Abandoned == want }); err != nil {
			t.Fatal(err)
		}
	}
	abandon(mount("a"), 1)
	abandon(mount("b"), 2)
	if got := r.Stats().Abandoned; got != 2 {
		t.Fatalf("abandoned workers = %d, want 2", got)
	}
	err := r.Do(Request{
		Key: mount("fresh"), Origin: catalog.OriginPerson,
		Turn: turnCtx(t, time.Second), Work: workBound(time.Second),
		Run: func(context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("a fresh domain started work while the global abandoned bound was reached")
	}
	if !errors.Is(err, ErrDomainSuspect) {
		t.Fatalf("error = %v, want ErrDomainSuspect", err)
	}
}

// TestAnIdleDomainIsReclaimed is the reclaiming rule: a domain's state is
// dropped when nothing holds or waits on it, so changing host ids cannot grow
// the map.
func TestAnIdleDomainIsReclaimed(t *testing.T) {
	r := New(testOptions())
	for i := range 10_000 {
		key := Key{Kind: "host", ID: fmt.Sprintf("host-%05d", i)}
		err := r.Do(Request{
			Key: key, Origin: catalog.OriginBackground,
			Turn: turnCtx(t, time.Second), Work: workBound(time.Second),
			Run: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := r.Stats().Domains; got != 0 {
		t.Fatalf("domains retained after 10,000 idle calls = %d, want 0", got)
	}
	if got := r.Stats().Running + r.Stats().Abandoned; got != 0 {
		t.Fatalf("running+abandoned = %d, want 0", got)
	}
}

// TestATurnDeadlineThatExpiresNeverStartsTheWork proves the turn bound: the
// wait for capacity ends the call, names the domain, and the closure never
// runs.
func TestATurnDeadlineThatExpiresNeverStartsTheWork(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 1
	opts.PersonReserve = 0
	opts.AbandonedPerDomain = 8
	opts.AbandonedGlobal = 16
	opts.HealthyReserve = 0
	r := New(opts)
	release := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		_ = r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second), Work: workBound(time.Minute),
			Run: blockingRun(blocked, release),
		})
	}()
	<-blocked
	called := atomic.Bool{}
	err := r.Do(Request{
		Key: mount("m"), Origin: catalog.OriginPerson,
		Turn: turnCtx(t, 20*time.Millisecond), Work: workBound(time.Second),
		Run: func(context.Context) error {
			called.Store(true)
			return nil
		},
	})
	if err == nil {
		t.Fatal("a call whose turn expired succeeded")
	}
	if !errors.Is(err, ErrTurnExpired) {
		t.Fatalf("error = %v, want ErrTurnExpired", err)
	}
	if called.Load() {
		t.Error("the closure ran even though the turn deadline expired first")
	}
	close(release)
}

// TestAnAbandonedWorkerKeepsItsPermitUntilItReturns is the interval the
// abandoned bound exists for: from settlement until the worker really
// returns, the permit is still held.
func TestAnAbandonedWorkerKeepsItsPermitUntilItReturns(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 1
	opts.PersonReserve = 0
	opts.AbandonedPerDomain = 8
	r := New(opts)
	release := make(chan struct{})
	started := make(chan struct{})
	workDone := make(chan struct{})
	workCtx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second), Work: settledWork(workCtx),
			Run: func(context.Context) error {
				close(started)
				<-release
				close(workDone)
				return nil
			},
		})
	}()
	<-started
	cancel()
	if err := waitFor(t, "abandonment", func() bool { return r.Stats().Abandoned == 1 }); err != nil {
		t.Fatal(err)
	}
	if got := r.Stats().Running; got != 1 {
		t.Fatalf("running workers after settlement = %d, want 1 (the permit is held until the worker returns)", got)
	}
	close(release)
	<-workDone
	if err := waitFor(t, "release", func() bool { return r.Stats().Running == 0 && r.Stats().Abandoned == 0 }); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) error {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

// TestMeasuredBulkheadSizes is the measurement ADR-8eej3e66 §6 asks for before
// a size is declared ("sizes are measured in the stage that adopts them").
// It is not an acceptance test: it reports the wait one domain's capacity
// imposes, so PerDomain is a number read off a run rather than inherited.
//
//	go test ./internal/operation/domains -run TestMeasuredBulkheadSizes -v
func TestMeasuredBulkheadSizes(t *testing.T) {
	const (
		calls    = 200
		workTime = 2 * time.Millisecond
	)
	for _, capacity := range []int{1, 2, 4, 8, 16} {
		r := New(Options{
			PerDomain: capacity, PersonReserve: 0,
			AbandonedPerDomain: 1000, AbandonedGlobal: 1000,
		})
		var wg sync.WaitGroup
		waits := make([]time.Duration, calls)
		start := time.Now()
		for i := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				issued := time.Now()
				_ = r.Do(Request{
					Key: mount("one"), Origin: catalog.OriginPerson,
					Turn: turnCtx(t, time.Minute), Work: workBound(time.Minute),
					Run: func(context.Context) error {
						waits[i] = time.Since(issued)
						time.Sleep(workTime)
						return nil
					},
				})
			}()
		}
		wg.Wait()
		sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
		t.Logf("capacity %2d: %d calls of %v over one domain finished in %v; p50 wait %v, p100 wait %v",
			capacity, calls, workTime, time.Since(start).Round(time.Millisecond),
			waits[calls/2].Round(time.Millisecond), waits[calls-1].Round(time.Millisecond))
	}
}

// TestTenThousandTimeoutsOnChangingHostsStayBounded is the constraint row
// "repeated timeouts cannot grow resources" (ADR-8eej3e66 §6), at the layer
// that owns the resources: 10,000 calls that each time out on a hung
// dependency, each naming a fresh host, and the goroutines and domain state
// stay inside the declared bounds.
//
// The declared bounds are the registry's own: at most AbandonedGlobal workers
// are abandoned, no more work starts while that cap holds, and an idle
// domain's state is reclaimed — so the map cannot grow with the host ids and
// the goroutines cannot grow with the calls.
func TestTenThousandTimeoutsOnChangingHostsStayBounded(t *testing.T) {
	opts := FilesystemDefaults()
	r := New(opts)
	release := make(chan struct{})
	baseline := runtime.NumGoroutine()

	var refused int
	for i := range 10_000 {
		key := Key{Kind: "host", ID: fmt.Sprintf("host-%05d", i)}
		work, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := r.Do(Request{
			Key: key, Origin: catalog.OriginBackground,
			Turn: turnCtx(t, time.Second), Work: settledWork(work),
			Run: blockingRun(nil, release),
		})
		cancel()
		if err == nil {
			t.Fatalf("call %d: a hung dependency returned no error", i)
		}
		if errors.Is(err, ErrDomainSuspect) {
			refused++
		}
	}

	stats := r.Stats()
	if stats.Abandoned > opts.AbandonedGlobal {
		t.Errorf("abandoned workers = %d, above the declared global bound of %d", stats.Abandoned, opts.AbandonedGlobal)
	}
	if stats.Running > opts.AbandonedGlobal {
		t.Errorf("running workers = %d, above the declared global bound of %d", stats.Running, opts.AbandonedGlobal)
	}
	if stats.Domains > opts.AbandonedGlobal+opts.PerDomain {
		t.Errorf("domain state retained = %d, above the declared bound of %d", stats.Domains, opts.AbandonedGlobal+opts.PerDomain)
	}
	if grown := runtime.NumGoroutine() - baseline; grown > opts.AbandonedGlobal+opts.PerDomain {
		t.Errorf("goroutines grew by %d over 10,000 timed-out calls, above the declared bound of %d",
			grown, opts.AbandonedGlobal+opts.PerDomain)
	}
	if refused == 0 {
		t.Error("no call was refused by the breaker; the bound was never the reason")
	}
	t.Logf("10,000 timed-out calls over changing hosts: %d abandoned, %d domains retained, %d goroutines over baseline, %d refused by the breaker",
		stats.Abandoned, stats.Domains, runtime.NumGoroutine()-baseline, refused)

	// When the dependency finally answers, everything the bound held is
	// released: no worker, no domain, no goroutine survives the episode.
	close(release)
	if err := waitFor(t, "the abandoned set to drain", func() bool {
		s := r.Stats()
		return s.Abandoned == 0 && s.Running == 0 && s.Domains == 0
	}); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, "goroutines to return to baseline", func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}); err != nil {
		t.Fatalf("%v (baseline %d, now %d)", err, baseline, runtime.NumGoroutine())
	}
}

// TestAWaitThatIsGrantedLeavesTheWaitingCount is the accounting half of the
// same wait: a call that waited and then took a permit must leave the waiting
// count. The grant arm used to return without taking it back out, so a domain
// reported a waiter for ever and would not be reclaimed — invisible while
// every waiter expired at its one-second turn, and ordinary once a person's
// turn is thirty seconds and a wait is granted rather than refused.
func TestAWaitThatIsGrantedLeavesTheWaitingCount(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 1
	opts.PersonReserve = 0
	opts.AbandonedPerDomain = 8
	opts.AbandonedGlobal = 16
	opts.HealthyReserve = 0
	r := New(opts)
	release := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		_ = r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Minute), Work: workBound(time.Minute),
			Run: blockingRun(blocked, release),
		})
	}()
	<-blocked

	done := make(chan error, 1)
	go func() {
		done <- r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Minute), Work: workBound(time.Minute),
			Run: func(context.Context) error { return nil },
		})
	}()
	if err := waitFor(t, "the second call to wait", func() bool { return r.Stats().Waiting == 1 }); err != nil {
		t.Fatal(err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the call granted the permit: %v", err)
	}
	if err := waitFor(t, "the granted waiter to leave the waiting count", func() bool {
		s := r.Stats()
		return s.Waiting == 0 && s.Running == 0
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTheWorkBoundIsMadeWhenThePermitIsTaken is this package's half of finding
// 2 of nocx-3an1s.2.14. Request.Work is a factory — not a context — so that a
// wait for capacity cannot spend the work's own budget: the factory must not
// run while the call is queued, and must run once the permit is free.
func TestTheWorkBoundIsMadeWhenThePermitIsTaken(t *testing.T) {
	opts := testOptions()
	opts.PerDomain = 1
	opts.PersonReserve = 0
	r := New(opts)
	release := make(chan struct{})
	held := make(chan struct{})

	var holder sync.WaitGroup
	holder.Add(1)
	go func() {
		defer holder.Done()
		_ = r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second), Work: workBound(time.Minute),
			Run: func(context.Context) error {
				close(held)
				<-release
				return nil
			},
		})
	}()
	<-held

	var made atomic.Int32
	queued := make(chan error, 1)
	go func() {
		queued <- r.Do(Request{
			Key: mount("m"), Origin: catalog.OriginPerson,
			Turn: turnCtx(t, time.Second),
			Work: func() (context.Context, context.CancelFunc) {
				made.Add(1)
				return context.WithCancel(context.Background())
			},
			Run: func(context.Context) error { return nil },
		})
	}()

	select {
	case <-time.After(100 * time.Millisecond):
	case <-queued:
		t.Fatal("the queued call ran while the only permit was held")
	}
	if got := made.Load(); got != 0 {
		t.Fatalf("the work bound was made %d time(s) while the call was still waiting for a permit", got)
	}

	close(release)
	if err := <-queued; err != nil {
		t.Fatalf("the granted call: %v", err)
	}
	holder.Wait()
	if got := made.Load(); got != 1 {
		t.Fatalf("the work bound was made %d time(s); want exactly once, at the grant", got)
	}
}
