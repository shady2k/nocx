package control

import (
	"context"
	"sync"
	"time"
)

// waitingSemaphore is a semaphore whose TryAcquire WAITS for capacity instead
// of refusing instantly. It is the admission behind the store keys, the
// native picker, the agent probe and the session gate: two operations on one
// resource are not an overload, they are a queue of length two — the second
// proceeds once the first releases.
//
// THE WAIT IS THE INVOCATION'S DECLARED TURN (ADR-8eej3e66 §3). The
// dispatcher binds the declaration's turn to the request context
// (OrderRequest.Turn), so a person's call waits as long as its catalogue entry
// says it may and not one instant longer; a call with no catalogue scope (an
// internal call, a test) falls back to the timeout the composition root named.
// There is no waiter-count bound: a caller beyond it is not refused, because
// no capacity answer exists any more. What bounds the queue is what bounded
// the work in the first place — a connection may have only its credit window
// outstanding, and every waiter leaves at its turn.
//
// TryAcquire may block (up to the turn). That is why NewWaitingSemaphore
// deliberately does NOT satisfy a non-blocking admission: the composition
// root places this admission on the task goroutine (the operation's Run,
// never a submission's TrySubmit), so the read loop never blocks on it.
type waitingSemaphore struct {
	name    string
	ch      chan struct{} // capacity tokens; one buffered token per permit
	mu      sync.Mutex
	waiters int // registered waiters, under mu
	timeout time.Duration
	book    *holderBook
}

// NewWaitingSemaphore returns an Admission with the given capacity. timeout is
// the wait bound for a caller whose context carries no declared turn.
func NewWaitingSemaphore(name string, capacity int, timeout time.Duration) Admission {
	if capacity < 0 {
		panic("control: negative capacity for waiting semaphore " + name)
	}
	if timeout < 0 {
		panic("control: negative waitTimeout for waiting semaphore " + name)
	}
	return &waitingSemaphore{
		name:    name,
		ch:      make(chan struct{}, capacity),
		timeout: timeout,
		book:    newHolderBook(),
	}
}

func (s *waitingSemaphore) Name() string { return s.name }

// waitBound is the turn the invocation declared, or the fallback this gate was
// built with when the context carries none.
func (s *waitingSemaphore) waitBound(ctx context.Context) time.Duration {
	if req, ok := OrderFrom(ctx); ok && req.Turn > 0 {
		return req.Turn
	}
	return s.timeout
}

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
	s.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiters--
		s.mu.Unlock()
	}()

	// Capacity exhausted: wait, bounded by the turn and the context. The
	// send is the only permit-acquisition path here; the runtime hands the
	// token to exactly one blocked sender per release, and a select that
	// picks the timeout/cancellation case instead never completed the send,
	// so no token is stranded.
	bound := s.waitBound(ctx)
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case s.ch <- struct{}{}:
		return &waitingPermit{ch: s.ch, book: s.book, id: s.book.add(ctx, time.Now())}, nil
	case <-ctx.Done():
		return nil, &Rejection{
			Kind:    RejectionCancelled,
			Reason:  "cancelled",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	case <-timer.C:
		return nil, &Rejection{
			Kind:    RejectionWaitExceeded,
			Reason:  "wait exceeded",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	}
}

// waitingPermit returns its token exactly once, like every permit here: a
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
