package control

import (
	"context"
	"sync"
	"time"
)

// semaphore is a counting Admission backed by a buffered channel. A token in
// the channel is one held Permit; the channel is full exactly when every
// Permit is out.
type semaphore struct {
	name string
	ch   chan struct{}
	book *holderBook
}

// NewSemaphore returns a NonblockingAdmission with the given capacity. Its
// TryAcquire never blocks: a full channel is a refusal, which is exactly the
// class of admission a bounded Submission may hold (NonblockingAdmission). A
// capacity of 0 refuses every acquire; a negative capacity is a programming
// error.
func NewSemaphore(name string, capacity int) NonblockingAdmission {
	if capacity < 0 {
		panic("control: negative capacity for semaphore " + name)
	}
	return &semaphore{name: name, ch: make(chan struct{}, capacity), book: newHolderBook()}
}

// nonblocking seals the interface: only this package's types satisfy it.
func (s *semaphore) nonblocking() {}

func (s *semaphore) Name() string { return s.name }

func (s *semaphore) TryAcquire(ctx context.Context) (Permit, *Rejection) {
	select {
	case s.ch <- struct{}{}:
		return &semaphorePermit{ch: s.ch, book: s.book, id: s.book.add(ctx, time.Now())}, nil
	default:
		return nil, &Rejection{
			Reason:  "capacity exhausted",
			Scope:   s.name,
			Holders: s.book.snapshot(),
		}
	}
}

// semaphorePermit returns its token exactly once. The once is not defensive
// tidiness: without it, a caller that releases twice takes a second token out
// of the channel — one belonging to a permit still held — and the semaphore
// then admits past its capacity. A bound a double release can silently exceed
// is not a bound, so single use is enforced by construction rather than asked
// for in a comment.
type semaphorePermit struct {
	ch   chan struct{}
	once sync.Once
	book *holderBook
	id   uint64
}

func (p *semaphorePermit) Release() {
	p.once.Do(func() {
		<-p.ch
		if p.book != nil {
			p.book.release(p.id)
		}
	})
}

// Holders names the requests currently holding this semaphore.
func (s *semaphore) Holders() []Holder { return s.book.snapshot() }
