package control

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// A RESOURCE, NOT A DOMAIN (ADR-8eej3e66 section 5).
//
// The domain gates admitted one operation per domain and made the next wait a
// second. Read off the code, no method needs whole-domain exclusion: reads have
// no order to keep, and a write needs order on the resource it changes — one
// session, one collection. This admission is that: a keyed FIFO per resource,
// and a waiter on a different resource is not behind anybody.
//
// It is still an Admission, so it is placed exactly where a domain gate was —
// inside the operation's Run, on the task goroutine — and no handler learns
// that the gate is gone. What changes is the grain and the wait bound: the
// bound is the invocation's declared turn, from the catalogue, instead of a
// server-wide constant.
//
// # What it holds
//
// The keys of one invocation are acquired TOGETHER, under one lock, in one
// step. A partial hold while waiting for the rest is impossible by
// construction, which is what retires CanonicalOrder as a convention: there is
// no second acquisition to order.
//
// A waiter starts when it is first in the queue of every key it names. A
// cancelled or expired waiter that has not started is dropped before it can
// start, and start against cancel is decided under the same lock — if the
// promotion won, the keys are released without running anything, because no
// caller has entered the work yet.
//
// # What it deliberately does not do
//
//   - Shared and exclusive barriers. `store:<name>:shared` is held
//     exclusively here; the read/write barrier arrives with config and vault,
//     which are the stores that need it (restore, reset).
//   - Coalescing (latest-value) and preemption. Both are already realised
//     where they matter: a session's resize is coalesced by its own lane and
//     cancelled by close (sessionLane), so a queued stale resize never reaches
//     this admission and a close never waits behind one.
//   - Failure domains, bulkheads and abandoned-worker bounds. Those bound
//     EXECUTION (ADR section 6) and are a different admission.

// FallbackOrderTurn bounds the wait when the invocation carries no catalogue
// scope — an internal call or a test, which has no declaration to read. It is
// the value the session, content and API declarations carry as their turn
// (DefaultDomainConflictWaitTimeout, internal/transport/ws.go), so a call
// without a scope waits exactly as long as one with a declaration waits.
const FallbackOrderTurn = time.Second

// OrderRequest is what the dispatcher knows and the operation does not: the
// canonical resource keys this invocation orders on, and the turn it may wait
// for them. It travels in the context because the admission is acquired by the
// capability layer, which is never handed a catalogue handle.
type OrderRequest struct {
	// Keys are canonical resource names: "<kind>:<name>", for example
	// "session:3f2a…", "store:content", "collection:/home/u/api".
	Keys []string
	// Turn is the invocation's declared wait bound. Zero means the scope
	// carried none, and FallbackOrderTurn applies.
	Turn time.Duration
}

type orderRequestKey struct{}

// WithOrder binds the invocation's ordering request to its context.
func WithOrder(ctx context.Context, req OrderRequest) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, orderRequestKey{}, req)
}

// OrderFrom returns the ordering request the dispatcher bound, if any.
func OrderFrom(ctx context.Context) (OrderRequest, bool) {
	if ctx == nil {
		return OrderRequest{}, false
	}
	req, ok := ctx.Value(orderRequestKey{}).(OrderRequest)
	return req, ok
}

type orderHeldKey struct{}

// WithOrderHeld marks a context as inside a held ordered scope. Acquiring
// another ordered resource through such a context is the nesting ADR section 5
// forbids, and TryAcquire panics on it: the alternative is a deadlock that
// looks like a hung server.
func WithOrderHeld(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, orderHeldKey{}, true)
}

// OrderHeld reports whether the context is inside a held ordered scope.
func OrderHeld(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(orderHeldKey{}).(bool)
	return held
}

// HoldsOrder reports whether an admission (possibly a composite) contains a
// ResourceOrder. The operation core asks once per acquisition to decide
// whether the callback runs inside an ordered scope.
func HoldsOrder(a Admission) bool {
	for _, part := range Parts(a) {
		if _, ok := part.(*ResourceOrder); ok {
			return true
		}
	}
	return false
}

// ResourceOrder is one domain's per-resource FIFO. Construct one per domain at
// the composition root and pass it where the domain gate was; the prefixes say
// which canonical keys belong to this domain, so a session key is never
// ordered by the content FIFO.
type ResourceOrder struct {
	name     string
	prefixes []string
	book     *holderBook

	mu     sync.Mutex
	queues map[string]*orderQueue
}

type orderQueue struct {
	waiters []*orderWaiter
	head    int
}

type orderWaiter struct {
	keys    []string
	ready   chan struct{}
	holder  Holder
	started bool
	dropped bool
	permit  *orderPermit
}

// NewResourceOrder builds the FIFO for one domain. An empty name or an empty
// prefix is a programming error, not a runtime surprise.
//
// THERE IS NO COUNT BOUND (ADR-8eej3e66 stage 8): a waiter that cannot start
// waits under its declared turn and answers "did not start" when the turn
// ends, so the queue is bounded by what a connection may have outstanding and
// by time, not by an admission that would refuse a person's call.
func NewResourceOrder(name string, prefixes ...string) *ResourceOrder {
	if name == "" {
		panic("control: resource order without a name")
	}
	for _, p := range prefixes {
		if p == "" {
			panic("control: resource order " + name + " has an empty key prefix")
		}
	}
	return &ResourceOrder{name: name, prefixes: prefixes, book: newHolderBook()}
}

func (r *ResourceOrder) Name() string { return r.name }

// Holders names the invocations currently holding this order.
func (r *ResourceOrder) Holders() []Holder { return r.book.snapshot() }

// TryAcquire takes every key this domain owns for the invocation in ctx. The
// keys are acquired together or not at all, and the wait is bounded by the
// invocation's declared turn.
func (r *ResourceOrder) TryAcquire(ctx context.Context) (Permit, *Rejection) {
	if OrderHeld(ctx) {
		panic("control: nested resource acquisition — an ordered resource is already held; " +
			"end the phase, release it, and revalidate after the next acquisition (ADR-8eej3e66 section 5)")
	}
	keys := r.keysFrom(ctx)
	if len(keys) == 0 {
		return noPermit{}, nil
	}
	turn := FallbackOrderTurn
	if req, ok := OrderFrom(ctx); ok && req.Turn > 0 {
		turn = req.Turn
	}

	holder, _ := HolderFrom(ctx)
	w := &orderWaiter{keys: keys, ready: make(chan struct{}), holder: holder}
	r.mu.Lock()
	for _, k := range keys {
		q := r.queues[k]
		if q == nil {
			if r.queues == nil {
				r.queues = make(map[string]*orderQueue)
			}
			q = &orderQueue{}
			r.queues[k] = q
		}
		q.waiters = append(q.waiters, w)
	}
	if r.runnableLocked(w) {
		r.startLocked(w)
	}
	r.mu.Unlock()

	timer := time.NewTimer(turn)
	defer timer.Stop()
	select {
	case <-w.ready:
		// The promotion and a cancellation can race; whichever took the
		// lock first decided. A cancellation that arrived after the
		// promotion releases the keys without running anything: no caller
		// has entered the work.
		if err := ctx.Err(); err != nil {
			w.permit.Release()
			return nil, &Rejection{Reason: "cancelled", Scope: r.name, Holders: r.book.snapshot()}
		}
		return w.permit, nil
	case <-ctx.Done():
		return nil, r.abandon(w, "cancelled")
	case <-timer.C:
		return nil, r.abandon(w, "wait exceeded")
	}
}

// keysFrom keeps the invocation's canonical keys that belong to this domain,
// deduplicated and sorted so two invocations naming one set enqueue in one
// order.
func (r *ResourceOrder) keysFrom(ctx context.Context) []string {
	req, ok := OrderFrom(ctx)
	if !ok || len(req.Keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(req.Keys))
	out := make([]string, 0, len(req.Keys))
	for _, key := range req.Keys {
		if key == "" || seen[key] || !r.owns(key) {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) > 1 {
		sort.Strings(out)
	}
	return out
}

func (r *ResourceOrder) owns(key string) bool {
	for _, p := range r.prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// runnableLocked reports whether w is first in the queue of every key it
// names. Callers hold mu.
func (r *ResourceOrder) runnableLocked(w *orderWaiter) bool {
	for _, k := range w.keys {
		q := r.queues[k]
		if q == nil || !q.firstIs(w) {
			return false
		}
	}
	return true
}

// firstIs reports whether w is the first waiter not yet dropped or released.
func (q *orderQueue) firstIs(w *orderWaiter) bool {
	for i := q.head; i < len(q.waiters); i++ {
		candidate := q.waiters[i]
		if candidate == nil || candidate.dropped {
			continue
		}
		return candidate == w
	}
	return false
}

// startLocked hands w its permit. Callers hold mu.
func (r *ResourceOrder) startLocked(w *orderWaiter) {
	w.started = true
	w.permit = &orderPermit{order: r, waiter: w, id: r.book.addHolder(w.holder, time.Now()), book: r.book}
	close(w.ready)
}

// abandon settles a waiter that gave up before it started. A waiter promoted
// a moment earlier holds the keys and is released here.
func (r *ResourceOrder) abandon(w *orderWaiter, reason string) *Rejection {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w.started {
		// The promotion won the race. Nothing ran, so nothing is rolled
		// back; the keys go straight to the next waiter.
		r.book.release(w.permit.id)
		r.releaseLocked(w)
	} else {
		w.dropped = true
		r.forgetLocked(w)
	}
	return &Rejection{Reason: reason, Scope: r.name, Holders: r.book.snapshot()}
}

// releaseLocked returns the keys. Callers hold mu.
func (r *ResourceOrder) releaseLocked(w *orderWaiter) {
	if w.dropped {
		return
	}
	w.dropped = true
	r.forgetLocked(w)
}

// forgetLocked removes w from every queue it names and promotes whatever the
// removal unblocked. Callers hold mu.
func (r *ResourceOrder) forgetLocked(w *orderWaiter) {
	for _, k := range w.keys {
		q := r.queues[k]
		if q == nil {
			continue
		}
		for i := q.head; i < len(q.waiters); i++ {
			if q.waiters[i] == w {
				q.waiters[i] = nil
				break
			}
		}
		r.trimLocked(k)
	}
}

// trimLocked advances a queue's head past released slots and drops the queue
// when nothing is left in it, so an idle resource keeps no state.
func (r *ResourceOrder) trimLocked(k string) {
	q := r.queues[k]
	if q == nil {
		return
	}
	for q.head < len(q.waiters) && q.waiters[q.head] == nil {
		q.head++
	}
	if q.head == len(q.waiters) {
		delete(r.queues, k)
		return
	}
	r.promoteLocked(k)
}

// promoteLocked starts the first waiter of a key's queue when it is also first
// in every other key it names. Only the head may start: order within a key is
// FIFO.
func (r *ResourceOrder) promoteLocked(k string) {
	q := r.queues[k]
	if q == nil {
		return
	}
	for i := q.head; i < len(q.waiters); i++ {
		w := q.waiters[i]
		if w == nil || w.dropped {
			continue
		}
		if !w.started && r.runnableLocked(w) {
			r.startLocked(w)
		}
		return
	}
}

// orderPermit is a held set of resource keys. The holder identity travels with
// it so app.diagnostics can name what is holding a resource.
type orderPermit struct {
	order  *ResourceOrder
	waiter *orderWaiter
	id     uint64
	book   *holderBook
	once   sync.Once
}

func (p *orderPermit) Release() {
	p.once.Do(func() {
		p.book.release(p.id)
		p.order.mu.Lock()
		defer p.order.mu.Unlock()
		p.order.releaseLocked(p.waiter)
	})
}

// noPermit is the permit of an invocation that names no resource this domain
// orders on: nothing is held, and Release does nothing.
type noPermit struct{}

func (noPermit) Release() {}
