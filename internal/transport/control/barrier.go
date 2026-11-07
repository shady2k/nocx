package control

import (
	"context"
	"sync"
	"time"
)

// Barrier is a shared/exclusive admission with a waiting bound, and it is the
// one primitive a set of stores uses to keep a generation coherent across
// them.
//
// The shape it exists for: restore rewrites the config and content stores from
// one file, and reset destroys the vault and the config references pointing at
// it. Those are the only operations that must see every other writer of those
// stores STOPPED, and the only readers that may proceed beside them are the
// ones whose answer does not depend on which generation is on disk. A plain
// serially-executing gate gives neither half: it orders two writers but cannot
// say "nobody at all, readers included", and it has no way to say "this
// operation was already holding the store when the exclusive holder arrived".
//
// # Writers are not starved, and readers are not admitted behind them
//
// A shared acquisition is granted only when no exclusive holder exists AND no
// exclusive holder is WAITING. That second clause is the whole of the
// fairness rule: without it, continuous reads are granted one after another
// and the restore queued among them never sees the set empty. With it, the
// first exclusive waiter closes the door behind it.
//
// # The bound
//
// waitTimeout bounds the whole acquisition (not one wake) for a caller whose
// context carries no declared turn; a call that carries one waits exactly the
// turn its catalogue entry declares (ADR-8eej3e66 §3). There is no waiter
// count: a caller beyond any such count is not refused, because no capacity
// answer exists any more, and every waiter leaves when its turn ends.
//
// A refusal carries the holders it found, exactly like the semaphores: a
// person is owed "what is holding this", and the answer travels in the
// rejection (holder.go).
type Barrier struct {
	name        string
	waitTimeout time.Duration
	book        *holderBook

	mu         sync.Mutex
	shared     int
	exclusive  bool
	sharedWait int
	exclWait   int
	wake       chan struct{} // closed and replaced on every release
}

// NewBarrier returns a shared/exclusive barrier. A negative waitTimeout is a
// programming error, like every other admission here. The returned barrier is
// the container; Shared and Exclusive return the admissions an operation
// composes.
func NewBarrier(name string, waitTimeout time.Duration) *Barrier {
	if waitTimeout < 0 {
		panic("control: negative waitTimeout for barrier " + name)
	}
	return &Barrier{
		name:        name,
		waitTimeout: waitTimeout,
		book:        newHolderBook(),
		wake:        make(chan struct{}),
	}
}

// Name identifies the barrier for metrics only.
func (b *Barrier) Name() string { return b.name }

// Shared returns the admission a reader or a writer of one of the barrier's
// stores takes. Several shared holders may hold it at once.
func (b *Barrier) Shared() Admission { return barrierAdmission{b: b, exclusive: false} }

// Exclusive returns the admission an operation that rewrites the stores takes.
// It waits for every shared holder to release and admits no new one while it
// waits.
func (b *Barrier) Exclusive() Admission { return barrierAdmission{b: b, exclusive: true} }

// Holders names the requests currently holding the barrier.
func (b *Barrier) Holders() []Holder { return b.book.snapshot() }

// barrierAdmission is one mode of a barrier, and satisfies Admission.
type barrierAdmission struct {
	b         *Barrier
	exclusive bool
}

func (a barrierAdmission) Name() string { return a.b.name }

func (a barrierAdmission) TryAcquire(ctx context.Context) (Permit, *Rejection) {
	return a.b.acquire(ctx, a.exclusive)
}

func (b *Barrier) grantableLocked(exclusive bool) bool {
	if b.exclusive {
		return false
	}
	if exclusive {
		return b.shared == 0
	}
	return b.exclWait == 0
}

// waitBound is the turn the invocation declared, or the fallback this barrier
// was built with when the context carries none.
func (b *Barrier) waitBound(ctx context.Context) time.Duration {
	if req, ok := OrderFrom(ctx); ok && req.Turn > 0 {
		return req.Turn
	}
	return b.waitTimeout
}

func (b *Barrier) acquire(ctx context.Context, exclusive bool) (Permit, *Rejection) {
	deadline := time.NewTimer(b.waitBound(ctx))
	defer deadline.Stop()

	b.mu.Lock()
	registered := false
	for {
		if b.grantableLocked(exclusive) {
			if exclusive {
				b.exclusive = true
			} else {
				b.shared++
			}
			id := b.book.add(ctx, time.Now())
			b.mu.Unlock()
			return &barrierPermit{b: b, exclusive: exclusive, id: id}, nil
		}
		if !registered {
			registered = true
		}
		if exclusive {
			b.exclWait++
		} else {
			b.sharedWait++
		}
		wake := b.wake
		b.mu.Unlock()

		// WHICH SIGNAL WOKE THE WAITER IS REMEMBERED, not re-discovered. A
		// timer channel yields its one value to the first receive and is
		// empty afterwards, so asking "did the deadline fire?" a second time
		// answers no for ever and the waiter loops without a bound — which is
		// the exact failure a bounded wait exists to prevent.
		const (
			byWake = iota
			byCancel
			byDeadline
		)
		outcome := byWake
		select {
		case <-wake:
		case <-ctx.Done():
			outcome = byCancel
		case <-deadline.C:
			outcome = byDeadline
		}

		b.mu.Lock()
		if exclusive {
			b.exclWait--
		} else {
			b.sharedWait--
		}
		switch outcome {
		case byCancel:
			b.mu.Unlock()
			return nil, &Rejection{
				Kind:    RejectionCancelled,
				Reason:  "cancelled",
				Scope:   b.name,
				Holders: b.book.snapshot(),
			}
		case byDeadline:
			b.mu.Unlock()
			return nil, &Rejection{
				Kind:    RejectionWaitExceeded,
				Reason:  "wait exceeded",
				Scope:   b.name,
				Holders: b.book.snapshot(),
			}
		}
	}
}

type barrierPermit struct {
	b         *Barrier
	exclusive bool
	id        uint64
	once      sync.Once
}

func (p *barrierPermit) Release() {
	p.once.Do(func() {
		p.b.mu.Lock()
		if p.exclusive {
			p.b.exclusive = false
		} else {
			p.b.shared--
		}
		close(p.b.wake)
		p.b.wake = make(chan struct{})
		p.b.mu.Unlock()
		p.b.book.release(p.id)
	})
}
