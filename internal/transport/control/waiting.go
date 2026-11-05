package control

import (
	"context"
	"sync"
	"time"
)

// waitingSemaphore is a semaphore whose TryAcquire WAITS (bounded) for
// capacity instead of refusing instantly. It is the admission behind the
// domain conflict gates: two operations on the config domain are not an
// overload, they are a queue of length two — the second may proceed once the
// first releases, and refusing it would tell a sequential client that did
// nothing wrong that the control plane is busy (the response to its previous
// request is enqueued before the permit is released, so the very next
// request can arrive while the gate is still held).
//
// The wait is bounded on BOTH ends, per the executor design review:
//
//   - waitTimeout: a waiter gives up after this long and returns a Rejection.
//     The bound keeps a gate held by a hung operation (an abandoned task at
//     shutdown, say) from making every conflicting request wait forever.
//   - maxQueue: at most this many callers may be registered as waiters;
//     a caller beyond the bound is refused INSTANTLY, never queued. The bound
//     keeps a flood of conflicting requests from piling up without limit.
//
// Only exhausting a bound is a refusal. Within the bounds a conflicting
// request waits for the gate, exactly like a queue of length two.
//
// TryAcquire may block (up to waitTimeout). That is why
// NewWaitingSemaphore deliberately does NOT satisfy NonblockingAdmission: a
// waiting admission can never be wired into a bounded Submission, whose
// TrySubmit the read loop calls. Callers that must never block (the read
// loop's synchronous path) must not call it; the composition root places this
// admission inside the task goroutine's acquisition (operation Run), never in
// a submission's TrySubmit. The two admission classes and the reason they
// differ are ADR-0026 item 4.

type waitingSemaphore struct {
	name     string
	ch       chan struct{} // capacity tokens; one buffered token per permit
	mu       sync.Mutex
	waiters  int // registered waiters, under mu
	maxQueue int
	timeout  time.Duration
	book     *holderBook
}

func NewWaitingSemaphore(name string, capacity, maxQueue int, waitTimeout time.Duration) Admission {
	if capacity < 0 {
		panic("control: negative capacity for waiting semaphore " + name)
	}
	if maxQueue < 0 {
		panic("control: negative maxQueue for waiting semaphore " + name)
	}
	if waitTimeout < 0 {
		panic("control: negative waitTimeout for waiting semaphore " + name)
	}
	return &waitingSemaphore{
		name:     name,
		ch:       make(chan struct{}, capacity),
		maxQueue: maxQueue,
		timeout:  waitTimeout,
		book:     newHolderBook(),
	}
}

func (s *waitingSemaphore) Name() string { return s.name }

func (s *waitingSemaphore) TryAcquire(ctx context.Context) (Permit, *Rejection) {
	// Fast path: capacity available and nobody waiting. The waiter-count
	// check under the lock keeps a fresh caller from stealing a token from a
	// registered waiter: once someone is queued, everyone else goes through
	// the queue too, so the wait is FIFO-ish and bounded rather than
	// starvation-prone.
	s.mu.Lock()
	if s.waiters == 0 {
		s.mu.Unlock()
		select {
		case s.ch <- struct{}{}:
			return &waitingPermit{ch: s.ch, book: s.book, id: s.book.add(ctx, time.Now())}, nil
		default:
		}
		s.mu.Lock()
	}
	if s.waiters >= s.maxQueue {
		s.mu.Unlock()
		return nil, &Rejection{
			Reason:  "conflict queue full",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	}
	s.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiters--
		s.mu.Unlock()
	}()

	// Capacity exhausted: wait, bounded by the timeout and the context. The
	// send is the only permit-acquisition path here; the runtime hands the
	// token to exactly one blocked sender per release, and a select that
	// picks the timeout/cancellation case instead never completed the send,
	// so no token is stranded.
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case s.ch <- struct{}{}:
		return &waitingPermit{ch: s.ch, book: s.book, id: s.book.add(ctx, time.Now())}, nil
	case <-ctx.Done():
		return nil, &Rejection{
			Reason:  "cancelled",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	case <-timer.C:
		return nil, &Rejection{
			Reason:  "wait exceeded",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	}
}

// waitingPermit returns its token exactly once, like semaphorePermit: a
// double release would hand back a token belonging to another held permit
// and the bound could be silently exceeded.
type waitingPermit struct {
	ch   chan struct{}
	once sync.Once
	book *holderBook
	id   uint64
}

func (p *waitingPermit) Release() {
	p.once.Do(func() {
		<-p.ch
		if p.book != nil {
			p.book.release(p.id)
		}
	})
}

// Holders names the requests currently holding this gate.
func (s *waitingSemaphore) Holders() []Holder { return s.book.snapshot() }
