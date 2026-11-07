package schedule

import (
	"context"
	"fmt"
	"sync"
)

// Reasons a Refusal carries. They are fixed server vocabulary: a handler maps
// them to the declaration's outcome, and nothing a caller sends can reach
// them.
const (
	ReasonQueueFull     = "queue full"
	ReasonDomainSuspect = "domain suspect"
)

// Refusal is a bound that refused work without waiting: the queue is full, or
// the failure domain has exhausted its abandoned workers and is suspect.
type Refusal struct {
	Scope  string
	Domain string
	Reason string
}

func (r *Refusal) Error() string {
	if r.Domain == "" {
		return fmt.Sprintf("schedule %s: %s", r.Scope, r.Reason)
	}
	return fmt.Sprintf("schedule %s: %s (%s)", r.Scope, r.Reason, r.Domain)
}

// Keyed serialises work per resource key: one holder per key, in arrival
// order, and different keys run independently.
//
// A queued caller that is cancelled or expires is REMOVED before it starts,
// and the decision to start against a cancellation is taken in one step under
// the gate's lock (ADR-8eej3e66 §5). A caller that is granted the key and then
// finds its context cancelled gives the key back without running; neither
// order can start a cancelled mutation or strand the key.
//
// Capacity bounds running and queued callers across every key, so a burst of
// new checkouts cannot allocate one worker each without limit.
type Keyed struct {
	name     string
	capacity int
	mu       sync.Mutex
	admitted int // running + queued, across every key
	queues   map[string]*keyQueue
}

type keyQueue struct {
	// holder is the caller that owns the key. Queue entries are waiters in
	// arrival order; nothing runs from this slice except through grant,
	// which hands ownership over.
	held    bool
	waiters []*keyWaiter
}

type keyWaiter struct {
	granted bool
	ready   chan struct{}
}

// NewKeyed returns a per-key gate. Capacity bounds every admitted caller,
// running and queued; a negative capacity is a programming error.
func NewKeyed(name string, capacity int) *Keyed {
	if capacity < 0 {
		panic("schedule: negative keyed capacity for " + name)
	}
	return &Keyed{name: name, capacity: capacity, queues: make(map[string]*keyQueue)}
}

// Name identifies the gate for diagnostics.
func (k *Keyed) Name() string { return k.name }

// Acquire takes the key, waiting behind its current holder for as long as ctx
// allows. It returns a release function, which is idempotent.
func (k *Keyed) Acquire(ctx context.Context, key string) (func(), error) {
	if key == "" {
		return nil, &Refusal{Scope: k.name, Reason: "ordering key required"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The cancellation check and the decision to join or create the queue
	// are one critical section: a context already cancelled here never
	// queues, and a queued waiter's cancellation below either removes it or
	// finds it granted.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	k.mu.Lock()
	q := k.queues[key]
	if q == nil {
		if k.admitted >= k.capacity {
			k.mu.Unlock()
			return nil, &Refusal{Scope: k.name, Reason: ReasonQueueFull}
		}
		k.queues[key] = &keyQueue{held: true}
		k.admitted++
		k.mu.Unlock()
		return k.releaseFunc(key), nil
	}
	if k.admitted >= k.capacity {
		k.mu.Unlock()
		return nil, &Refusal{Scope: k.name, Reason: ReasonQueueFull}
	}
	w := &keyWaiter{ready: make(chan struct{})}
	q.waiters = append(q.waiters, w)
	k.admitted++
	k.mu.Unlock()

	select {
	case <-w.ready:
		// Granted. If the call was cancelled in the same instant, the
		// caller has not run anything yet, so give the key straight back.
		if err := ctx.Err(); err != nil {
			k.release(key)
			return nil, err
		}
		return k.releaseFunc(key), nil
	case <-ctx.Done():
		k.mu.Lock()
		if w.granted {
			// Grant and cancellation crossed: ownership moved here, and it
			// must move on. Release outside the lock is not required — the
			// grant path is under the same lock — but release takes it, so
			// drop the lock first.
			k.mu.Unlock()
			k.release(key)
			return nil, ctx.Err()
		}
		k.removeWaiter(q, w)
		k.admitted--
		k.cleanup(key, q)
		k.mu.Unlock()
		return nil, ctx.Err()
	}
}

// removeWaiter drops w from q's waiters. Called with the lock held.
func (k *Keyed) removeWaiter(q *keyQueue, w *keyWaiter) {
	for i, candidate := range q.waiters {
		if candidate == w {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			return
		}
	}
}

// cleanup drops an empty queue. Called with the lock held.
func (k *Keyed) cleanup(key string, q *keyQueue) {
	if !q.held && len(q.waiters) == 0 {
		delete(k.queues, key)
	}
}

func (k *Keyed) releaseFunc(key string) func() {
	var once sync.Once
	return func() { once.Do(func() { k.release(key) }) }
}

// release hands the key to the next waiter, or frees it.
func (k *Keyed) release(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	q := k.queues[key]
	if q == nil || !q.held {
		return
	}
	for len(q.waiters) > 0 {
		w := q.waiters[0]
		q.waiters = q.waiters[1:]
		// The grant and the start decision are one step: a waiter whose
		// context has already fired is dropped here, before it can run, and
		// the next waiter is considered in the same pass.
		if w.granted {
			continue
		}
		w.granted = true
		close(w.ready)
		return
	}
	q.held = false
	k.admitted--
	k.cleanup(key, q)
}

// Waiting reports how many callers are queued behind key's holder. It is a
// read for diagnostics and tests: no decision uses it.
func (k *Keyed) Waiting(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	if q := k.queues[key]; q != nil {
		return len(q.waiters)
	}
	return 0
}

// Domains reports how many keys are active; it exists for diagnostics and
// tests (an idle key must leave nothing behind).
func (k *Keyed) Keys() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.queues)
}

// ── bulkhead ──────────────────────────────────────────────────────────────

// Class is who the work is for. A bulkhead reserves part of its capacity for
// people, so background work cannot take every permit before a person
// arrives (ADR-8eej3e66 §6).
type Class string

const (
	ClassPerson     Class = "person"
	ClassBackground Class = "background"
)

// Limits are one bulkhead's bounds. Every field is explicit: zero means zero
// capacity or zero queue, never "unlimited".
type Limits struct {
	// Capacity is the permits a failure domain may hold at once.
	Capacity int
	// PersonReserved is the part of Capacity background work may never hold:
	// the permits left for a person while background saturates a domain.
	PersonReserved int
	// Queue bounds waiters per domain; a caller beyond it is refused rather
	// than queued without limit.
	Queue int
	// AbandonedPerDomain is how many workers whose deadline expired may still
	// be running on one domain before the domain is suspect.
	AbandonedPerDomain int
	// AbandonedGlobal is the same bound across every domain.
	AbandonedGlobal int
}

func (l Limits) validate(name string) {
	if l.Capacity <= 0 {
		panic("schedule: bulkhead " + name + " has no capacity")
	}
	if l.PersonReserved < 0 || l.PersonReserved > l.Capacity {
		panic("schedule: bulkhead " + name + " reserves more than it holds")
	}
	if l.Queue < 0 || l.AbandonedPerDomain <= 0 || l.AbandonedGlobal <= 0 {
		panic("schedule: bulkhead " + name + " has an invalid bound")
	}
}

// Bulkhead bounds execution per failure domain — a checkout's mount for git
// processes — so a hung domain holds its own permits and a healthy domain
// goes on.
//
// The abandoned bound is the circuit breaker: at a deadline the request
// settles and the worker keeps its permit until it really returns. A domain
// at its abandoned bound is suspect: calls settle at once with the entry's
// expired outcome and no new worker starts. Once a worker returns and the
// count falls below the bound, the domain is admitted again.
type Bulkhead struct {
	name         string
	limits       Limits
	mu           sync.Mutex
	domains      map[string]*domainState
	abandonedAll int
}

type domainState struct {
	held       int // includes abandoned workers still running
	heldBg     int
	abandoned  int
	personQ    []*bulkWaiter
	background []*bulkWaiter
}

func (d *domainState) waiting() int { return len(d.personQ) + len(d.background) }

type bulkWaiter struct {
	granted bool
	ready   chan struct{}
}

// NewBulkhead returns a bulkhead over the given limits.
func NewBulkhead(name string, limits Limits) *Bulkhead {
	limits.validate(name)
	return &Bulkhead{name: name, limits: limits, domains: make(map[string]*domainState)}
}

// Name identifies the bulkhead for diagnostics.
func (b *Bulkhead) Name() string { return b.name }

// Acquire takes one permit on domain for class, waiting for capacity up to
// ctx's deadline. A suspect domain and a full queue refuse immediately.
func (b *Bulkhead) Acquire(ctx context.Context, domain string, class Class) (*Lease, error) {
	if domain == "" {
		return nil, &Refusal{Scope: b.name, Reason: "failure domain required"}
	}
	if class != ClassPerson && class != ClassBackground {
		return nil, &Refusal{Scope: b.name, Reason: "unknown class"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	st := b.domains[domain]
	if st == nil {
		st = &domainState{}
		b.domains[domain] = st
	}
	if reason := b.refusalLocked(st); reason != "" {
		b.dropIfIdleLocked(domain, st)
		b.mu.Unlock()
		return nil, &Refusal{Scope: b.name, Domain: domain, Reason: reason}
	}
	if b.roomLocked(st, class) {
		b.takeLocked(st, class)
		b.mu.Unlock()
		return &Lease{bulkhead: b, domain: domain, class: class}, nil
	}
	if st.waiting() >= b.limits.Queue {
		b.dropIfIdleLocked(domain, st)
		b.mu.Unlock()
		return nil, &Refusal{Scope: b.name, Domain: domain, Reason: ReasonQueueFull}
	}
	w := &bulkWaiter{ready: make(chan struct{})}
	if class == ClassPerson {
		st.personQ = append(st.personQ, w)
	} else {
		st.background = append(st.background, w)
	}
	b.mu.Unlock()

	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			// Granted and abandoned in the same instant: give it back.
			b.release(domain, class)
			return nil, err
		}
		return &Lease{bulkhead: b, domain: domain, class: class}, nil
	case <-ctx.Done():
		b.mu.Lock()
		if w.granted {
			b.mu.Unlock()
			b.release(domain, class)
			return nil, ctx.Err()
		}
		st.removeWaiter(w)
		b.dropIfIdleLocked(domain, st)
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (st *domainState) removeWaiter(w *bulkWaiter) {
	for i, candidate := range st.personQ {
		if candidate == w {
			st.personQ = append(st.personQ[:i], st.personQ[i+1:]...)
			return
		}
	}
	for i, candidate := range st.background {
		if candidate == w {
			st.background = append(st.background[:i], st.background[i+1:]...)
			return
		}
	}
}

// refusalLocked reports why no worker may start, or "".
func (b *Bulkhead) refusalLocked(st *domainState) string {
	if st.abandoned >= b.limits.AbandonedPerDomain {
		return ReasonDomainSuspect
	}
	if b.abandonedAll >= b.limits.AbandonedGlobal && st.abandoned > 0 {
		// The global abandoned bound is reached: the domains already holding
		// work nobody can join are the ones refused. A healthy domain keeps
		// its reserved capacity.
		return ReasonDomainSuspect
	}
	return ""
}

// roomLocked reports whether class may take a permit now. Background may
// never take the reserved part of Capacity, and a waiting person keeps a
// freed permit from a fresh background caller.
func (b *Bulkhead) roomLocked(st *domainState, class Class) bool {
	if class == ClassBackground {
		return st.held < b.limits.Capacity-b.limits.PersonReserved && len(st.personQ) == 0 &&
			st.heldBg < b.limits.Capacity-b.limits.PersonReserved
	}
	return st.held < b.limits.Capacity && len(st.personQ) == 0
}

func (b *Bulkhead) takeLocked(st *domainState, class Class) {
	st.held++
	if class == ClassBackground {
		st.heldBg++
	}
}

// release returns one permit of class and serves the waiters.
func (b *Bulkhead) release(domain string, class Class) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.domains[domain]
	if st == nil || st.held == 0 {
		return
	}
	st.held--
	if class == ClassBackground && st.heldBg > 0 {
		st.heldBg--
	}
	b.serveLocked(domain, st)
}

// serveLocked hands free permits to waiters, people first.
func (b *Bulkhead) serveLocked(domain string, st *domainState) {
	for {
		if len(st.personQ) > 0 && st.held < b.limits.Capacity {
			w := st.personQ[0]
			st.personQ = st.personQ[1:]
			if w.granted {
				continue
			}
			w.granted = true
			b.takeLocked(st, ClassPerson)
			close(w.ready)
			continue
		}
		if len(st.background) > 0 &&
			st.held < b.limits.Capacity-b.limits.PersonReserved &&
			st.heldBg < b.limits.Capacity-b.limits.PersonReserved {
			w := st.background[0]
			st.background = st.background[1:]
			if w.granted {
				continue
			}
			w.granted = true
			b.takeLocked(st, ClassBackground)
			close(w.ready)
			continue
		}
		b.dropIfIdleLocked(domain, st)
		return
	}
}

// dropIfIdleLocked reclaims an empty domain's state, so changing host ids
// cannot grow the map without limit.
func (b *Bulkhead) dropIfIdleLocked(domain string, st *domainState) {
	if st.held == 0 && st.waiting() == 0 && st.abandoned == 0 {
		delete(b.domains, domain)
	}
}

// Domains reports how many failure domains hold state; diagnostics and tests
// use it to prove an idle domain is reclaimed.
func (b *Bulkhead) Domains() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.domains)
}

// Lease is one held permit. Release must be called when the work returns;
// Abandon marks that the request settled at its deadline while the worker
// still runs. Both are idempotent and safe to call after each other.
type Lease struct {
	bulkhead  *Bulkhead
	domain    string
	class     Class
	abandoned bool
	released  bool
}

// Abandon records that this worker outlived its request's deadline. Its
// permit stays held until Release.
func (l *Lease) Abandon() {
	if l == nil || l.bulkhead == nil {
		return
	}
	l.bulkhead.mu.Lock()
	defer l.bulkhead.mu.Unlock()
	if l.released || l.abandoned {
		return
	}
	if st := l.bulkhead.domains[l.domain]; st != nil {
		st.abandoned++
		l.bulkhead.abandonedAll++
		l.abandoned = true
	}
}

// Release returns the permit exactly once.
func (l *Lease) Release() {
	if l == nil || l.bulkhead == nil {
		return
	}
	b := l.bulkhead
	b.mu.Lock()
	if l.released {
		b.mu.Unlock()
		return
	}
	l.released = true
	st := b.domains[l.domain]
	if st == nil || st.held == 0 {
		b.mu.Unlock()
		return
	}
	st.held--
	if l.class == ClassBackground && st.heldBg > 0 {
		st.heldBg--
	}
	if l.abandoned && st.abandoned > 0 {
		st.abandoned--
		if b.abandonedAll > 0 {
			b.abandonedAll--
		}
	}
	b.serveLocked(l.domain, st)
	b.mu.Unlock()
}
