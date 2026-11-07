package control

import (
	"context"
	"sync"
)

// orderedSubmission runs tasks strictly in submission order on one worker
// goroutine. It is the submission for work whose ARRIVAL ORDER is load-bearing
// but that must still run off the read loop: the read loop submits in order,
// and this submission preserves that order by construction — the single
// worker runs task N+1 only after task N has finished.
//
// The canonical example is a pane's destruction sharing a queue with a save:
// a save submitted after the pane closed must observe the destruction, where
// two goroutines would race.
//
// THERE IS NO COUNT BOUND (ADR-8eej3e66 stage 8). The queue holds what
// clients actually sent — the credit window bounds that for renderer calls,
// and lifecycle facts arrive one per command — and no submission answers a
// person's request with a capacity error. The worker is started lazily on
// first submit and stopped when its owning server closes.
//
// A panicking task crashes the process (control.go: a panic is deliberately
// not swallowed). The queue's already-admitted tasks are lost with the crash.
type orderedSubmission struct {
	name string
	done chan struct{}

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []orderedTask
	started bool
	closed  bool
}

type orderedTask struct {
	ctx  context.Context
	task Task
}

// NewOrderedSubmission returns a Submission that runs each task on a single
// worker in submission order, and never refuses.
func NewOrderedSubmission(name string) Submission {
	s := &orderedSubmission{name: name, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Name identifies the resource for metrics only.
func (s *orderedSubmission) Name() string { return s.name }

// TrySubmit appends the task to the queue. It never blocks and never refuses
// while the submission is open, so the read loop hands the work over and
// moves on.
func (s *orderedSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &Rejection{Reason: "submission closed", Scope: s.name}
	}
	s.queue = append(s.queue, orderedTask{ctx: ctx, task: task})
	if !s.started {
		s.started = true
		go s.worker()
	}
	s.cond.Signal()
	return nil
}

// Shutdown stops admission and lets the worker finish its admitted work.
// It is idempotent.
func (s *orderedSubmission) Shutdown() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if !s.started {
			close(s.done)
		}
		s.cond.Signal()
	}
	s.mu.Unlock()
}

// worker drains the queue in submission order, then exits after Shutdown.
func (s *orderedSubmission) worker() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		if startAllowed(next.task) {
			next.task.Run(next.ctx)
		}
	}
}
