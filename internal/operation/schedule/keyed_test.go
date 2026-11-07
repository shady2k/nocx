package schedule

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// acquired is one Acquire result delivered from a goroutine.
type acquired struct {
	release func()
	err     error
}

func acquireAsync(k *Keyed, ctx context.Context, key string) <-chan acquired {
	ch := make(chan acquired, 1)
	go func() {
		release, err := k.Acquire(ctx, key)
		ch <- acquired{release: release, err: err}
	}()
	return ch
}

func await(t *testing.T, ch <-chan acquired) acquired {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return")
		return acquired{}
	}
}

// A mutation on one checkout never starts while another holds it; the same
// work on a different checkout is not held behind it.
func TestKeyedOrdersOneCheckoutAndNotAnother(t *testing.T) {
	k := NewKeyed("test")
	releaseA, err := k.Acquire(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	same := acquireAsync(k, context.Background(), "/repo")
	other := acquireAsync(k, context.Background(), "/other")

	got := await(t, other)
	if got.err != nil {
		t.Fatalf("other checkout acquire: %v", got.err)
	}
	got.release()

	select {
	case a := <-same:
		t.Fatalf("a second holder of the same checkout started while the first was held: %+v", a)
	case <-time.After(100 * time.Millisecond):
	}

	releaseA()
	second := await(t, same)
	if second.err != nil {
		t.Fatalf("second acquire after release: %v", second.err)
	}
	second.release()
}

// A cancelled waiter is removed from its queue before it starts: it never
// receives a release, and the key is free for the next caller.
func TestKeyedCancelledWaiterNeverStarts(t *testing.T) {
	k := NewKeyed("test")
	release, err := k.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiter := acquireAsync(k, ctx, "repo")
	time.Sleep(50 * time.Millisecond) // let it join the queue
	cancel()

	got := await(t, waiter)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", got.err)
	}
	if got.release != nil {
		t.Fatal("cancelled waiter received a release: it was granted after cancellation")
	}

	release()
	// The queue state must be clean: a fresh acquire runs immediately.
	next, err := k.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatalf("acquire after a cancelled waiter: %v", err)
	}
	next()
}

// The start-against-cancel order the other way round: the grant happened, the
// caller's context then fired, and the caller must give the key back rather
// than run or strand it.
func TestKeyedGrantThenCancelReleasesTheKey(t *testing.T) {
	k := NewKeyed("test")
	first, err := k.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiter := acquireAsync(k, ctx, "repo")
	time.Sleep(50 * time.Millisecond)

	// Release and cancel in the same instant; whichever wins, the queue must
	// not strand the key. Release is idempotent, so the two orders can share
	// one call site.
	done := make(chan struct{})
	go func() { first(); close(done) }()
	cancel()
	<-done

	got := await(t, waiter)
	if got.err == nil {
		got.release()
	}
	next, err := k.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatalf("acquire after grant/cancel race: %v", err)
	}
	next()
}

// There is no count bound at all (ADR-8eej3e66 stage 8): many keys are served
// at once while each key keeps its own order. What ends a wait on ONE key is
// the caller's context, not an admission.
func TestKeyedHasNoCountBound(t *testing.T) {
	k := NewKeyed("test")
	release, err := k.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	const keys = 64
	for i := range keys {
		key := "repo-" + strconv.Itoa(i)
		r, err := k.Acquire(context.Background(), key)
		if err != nil {
			t.Fatalf("acquire %s: %v", key, err)
		}
		r()
	}
	// A waiter on the held key ends at its context, not at a queue bound.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := k.Acquire(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter on the held key err = %v, want the turn deadline", err)
	}
	release()
}

func TestKeyedDropsAnIdleKey(t *testing.T) {
	k := NewKeyed("test")
	release, err := k.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := k.Keys(); got != 1 {
		t.Fatalf("keys while held = %d, want 1", got)
	}
	release()
	if got := k.Keys(); got != 0 {
		t.Fatalf("keys after release = %d, want 0", got)
	}
}

// ── bulkhead ──────────────────────────────────────────────────────────────

func bulkheadLimits() Limits {
	return Limits{Capacity: 3, PersonReserved: 1, AbandonedPerDomain: 2, AbandonedGlobal: 4}
}

func acquireLease(t *testing.T, b *Bulkhead, domain string, class Class) *Lease {
	t.Helper()
	lease, err := b.Acquire(context.Background(), domain, class)
	if err != nil {
		t.Fatalf("acquire %s/%s: %v", domain, class, err)
	}
	return lease
}

func TestBulkheadBoundsOneDomainAndNotAnother(t *testing.T) {
	b := NewBulkhead("git", bulkheadLimits())
	l1 := acquireLease(t, b, "mount:1", ClassPerson)
	l2 := acquireLease(t, b, "mount:1", ClassPerson)
	l3 := acquireLease(t, b, "mount:1", ClassPerson)

	// The fourth waits at capacity.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, "mount:1", ClassPerson); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fourth acquire err = %v, want the turn deadline", err)
	}

	// A different mount is unaffected.
	other := acquireLease(t, b, "mount:2", ClassPerson)

	l1.Release()
	l2.Release()
	l3.Release()
	other.Release()
}

func TestBulkheadBackgroundCannotTakeThePersonReserve(t *testing.T) {
	b := NewBulkhead("git", bulkheadLimits())
	// Capacity 3, reserve 1: background may hold 2.
	bg1 := acquireLease(t, b, "mount:1", ClassBackground)
	bg2 := acquireLease(t, b, "mount:1", ClassBackground)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, "mount:1", ClassBackground); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third background acquire err = %v, want to wait for the reserve", err)
	}

	// The reserved permit is there for a person.
	person := acquireLease(t, b, "mount:1", ClassPerson)
	person.Release()
	bg1.Release()
	bg2.Release()
}

func TestBulkheadPersonWaiterIsServedBeforeBackground(t *testing.T) {
	b := NewBulkhead("git", bulkheadLimits())
	l1 := acquireLease(t, b, "mount:1", ClassPerson)
	l2 := acquireLease(t, b, "mount:1", ClassPerson)
	l3 := acquireLease(t, b, "mount:1", ClassPerson)

	// Background first, person second; both wait. A released permit is the
	// person's.
	ctxBg, cancelBg := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelBg()
	bgCh := make(chan error, 1)
	go func() {
		lease, err := b.Acquire(ctxBg, "mount:1", ClassBackground)
		if lease != nil {
			lease.Release()
		}
		bgCh <- err
	}()
	time.Sleep(30 * time.Millisecond)

	ctxPerson, cancelPerson := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelPerson()
	personCh := make(chan error, 1)
	go func() {
		lease, err := b.Acquire(ctxPerson, "mount:1", ClassPerson)
		if lease != nil {
			lease.Release()
		}
		personCh <- err
	}()
	time.Sleep(30 * time.Millisecond)

	l3.Release()
	select {
	case err := <-personCh:
		if err != nil {
			t.Fatalf("person waiter: %v", err)
		}
	case err := <-bgCh:
		t.Fatalf("background waiter was served before the person: %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("neither waiter was served")
	}
	l1.Release()
	l2.Release()
	// Let the background waiter finish so nothing leaks into the next test.
	<-bgCh
}

// A worker whose deadline expired keeps its permit until it really returns;
// at the abandoned bound its domain is suspect, and other domains go on.
func TestBulkheadAbandonedWorkMakesItsDomainSuspectOnly(t *testing.T) {
	// A worker that abandoned its request still holds its permit: two of
	// two permits at capacity, neither reclaimed, so the next caller waits.
	held := NewBulkhead("git", Limits{Capacity: 2, PersonReserved: 1, AbandonedPerDomain: 3, AbandonedGlobal: 4})
	l1 := acquireLease(t, held, "mount:1", ClassPerson)
	l2 := acquireLease(t, held, "mount:1", ClassPerson)
	l1.Abandon()
	l2.Abandon()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := held.Acquire(ctx, "mount:1", ClassPerson); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire on a domain holding abandoned work = %v, want a wait", err)
	}
	l1.Release()
	l2.Release()

	// At the abandoned bound the domain is suspect: no new worker starts,
	// while another domain is unaffected.
	suspect := NewBulkhead("git", Limits{Capacity: 4, PersonReserved: 1, AbandonedPerDomain: 1, AbandonedGlobal: 4})
	a := acquireLease(t, suspect, "mount:1", ClassPerson)
	a.Abandon()
	_, err := suspect.Acquire(context.Background(), "mount:1", ClassPerson)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonDomainSuspect {
		t.Fatalf("acquire on a suspect domain err = %v, want a domain-suspect refusal", err)
	}
	healthy := acquireLease(t, suspect, "mount:2", ClassPerson)
	healthy.Release()

	// The worker returning clears the suspicion.
	a.Release()
	again := acquireLease(t, suspect, "mount:1", ClassPerson)
	again.Release()
}

func TestBulkheadGlobalAbandonedBoundRefusesOnlyDomainsWithAbandonedWork(t *testing.T) {
	b := NewBulkhead("git", Limits{Capacity: 4, PersonReserved: 1, AbandonedPerDomain: 4, AbandonedGlobal: 1})
	a := acquireLease(t, b, "mount:1", ClassPerson)
	a.Abandon()

	if _, err := b.Acquire(context.Background(), "mount:1", ClassPerson); err == nil {
		t.Fatal("a domain holding abandoned work was admitted past the global abandoned bound")
	}
	healthy := acquireLease(t, b, "mount:2", ClassPerson)
	healthy.Release()
	a.Release()
}

func TestBulkheadDropsAnIdleDomain(t *testing.T) {
	b := NewBulkhead("git", bulkheadLimits())
	lease := acquireLease(t, b, "mount:1", ClassPerson)
	if got := b.Domains(); got != 1 {
		t.Fatalf("domains while held = %d, want 1", got)
	}
	lease.Release()
	if got := b.Domains(); got != 0 {
		t.Fatalf("domains after release = %d, want 0 (idle state is reclaimed)", got)
	}
}
