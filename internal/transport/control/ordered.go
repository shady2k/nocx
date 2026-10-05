package control

import (
	"context"
	"sync"
)

// orderedSubmission runs tasks strictly in submission order on one worker
// goroutine, under a capacity bound. It is the submission for work whose
// ARRIVAL ORDER is load-bearing but that must still run off the read loop:
// the read loop submits in order, and this submission preserves that order
// by construction — the single worker drains a FIFO channel, so task N+1
// never starts before task N has finished.
//
// The canonical example is resize: the per-session coalescing lane replaces
// its pending op, so two resizes racing off-loop can land on stale
// dimensions. close shares this submission with resize so a close admitted
// after a resize on the same socket observes the resize's enqueue first —
// the same-socket ordering the read loop used to provide by running
// everything inline.
//
// The bound is admission-backed like any other: a full queue refuses with a
// *Rejection (never blocks, never grows without limit), exactly the
// saturation contract. The worker is started lazily on first submit and
// stopped when its owning server closes.
//
// A panicking task crashes the process, exactly like boundedSubmission's
// runAndRelease (control.go: a panic is deliberately not swallowed — it
// propagates, but never leaks a permit). The queue's already-admitted tasks
// are lost with the crash, which is the same policy as a permit leaked by a
// crashing worker.
type orderedSubmission struct {
	name     string
	capacity int
	ch       chan orderedTask
	done     chan struct{}
	mu       sync.Mutex
	started  bool
	closed   bool
}

type orderedTask struct {
	ctx  context.Context
	task Task
}

// NewOrderedSubmission returns a Submission that runs each task on a single
// worker in submission order. Capacity bounds the queue; a full queue
// refuses (capacity 0 refuses every submit, negative is a programming error).
func NewOrderedSubmission(name string, capacity int) Submission {
	if capacity < 0 {
		panic("control: negative capacity for ordered submission " + name)
	}
	return &orderedSubmission{
		name:     name,
		capacity: capacity,
		ch:       make(chan orderedTask, capacity),
		done:     make(chan struct{}),
	}
}

// Name identifies the resource for metrics only.
func (s *orderedSubmission) Name() string { return s.name }

// TrySubmit enqueues the task in arrival order. A full queue refuses with a
// *Rejection — the caller answers the saturation error/notification.
func (s *orderedSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &Rejection{Reason: "submission closed", Scope: s.name}
	}
	select {
	case s.ch <- orderedTask{ctx: ctx, task: task}:
		if !s.started {
			s.started = true
			go s.worker()
		}
		return nil
	default:
		return &Rejection{
			Reason: "capacity exhausted",
			Scope:  s.name,
		}
	}
}

// Shutdown stops admission and lets the worker finish its admitted work.
// It is safe to call more than once.
func (s *orderedSubmission) Shutdown() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.ch)
		if !s.started {
			close(s.done)
		}
	}
	s.mu.Unlock()
}

// worker drains the FIFO in submission order, then exits after Shutdown.
func (s *orderedSubmission) worker() {
	defer close(s.done)
	for t := range s.ch {
		t.task.Run(t.ctx)
	}
}
