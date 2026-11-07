package control

import (
	"context"
	"sync"
)

// keyedOrderedSubmission owns one worker per active key, not per request.
// A key's queue and worker disappear as soon as its last task finishes.
// All admission and worker handoff decisions share mu: a submit racing an
// idle worker either joins its FIFO or creates the next worker, never both.
type keyedOrderedSubmission struct {
	name     string
	mu       sync.Mutex
	admitted int
	keys     map[string]*keyedQueue
	closed   bool
	done     chan struct{}
}

type keyedQueue struct {
	head *keyedTask
	tail *keyedTask
}

type keyedTask struct {
	orderedTask
	next *keyedTask
}

// NewKeyedOrderedSubmission preserves submission order within each Task.Key,
// shared across every caller of this submission. Different keys run
// independently, and an idle key keeps neither a queue nor a worker.
//
// THERE IS NO COUNT BOUND (ADR-8eej3e66 stage 8): the queue grows with what
// clients actually sent, which the credit window bounds, and a queued task
// that its owner withdrew or gave up on is dropped by the one-step start
// decision before it runs. A capacity refusal here was one of the capacity
// answers stage 8 removes.
//
// Empty keys are refused without allocating per-key state. Shutdown fences
// further admission and lets admitted tasks finish in order, including
// cancelled tasks (their Run receives the original context).
func NewKeyedOrderedSubmission(name string) Submission {
	return &keyedOrderedSubmission{name: name, done: make(chan struct{})}
}

func (s *keyedOrderedSubmission) Name() string { return s.name }

func (s *keyedOrderedSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &Rejection{Reason: "submission closed", Scope: s.name}
	}
	if task.Key == "" {
		return &Rejection{Reason: "ordering key required", Scope: s.name}
	}
	next := &keyedTask{orderedTask: orderedTask{ctx: ctx, task: task}}
	if queue := s.keys[task.Key]; queue != nil {
		queue.tail.next = next
		queue.tail = next
		s.admitted++
		return nil
	}
	if s.keys == nil {
		s.keys = make(map[string]*keyedQueue)
	}
	queue := &keyedQueue{head: next, tail: next}
	s.keys[task.Key] = queue
	s.admitted++
	go s.worker(task.Key, queue)
	return nil
}

// Shutdown is idempotent and nonblocking. The owner tracks admitted tasks to
// cancel and join them under its shutdown policy; done closes after all tasks
// have returned and every per-key queue has been reclaimed.
func (s *keyedOrderedSubmission) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.admitted == 0 {
		close(s.done)
	}
}

func (s *keyedOrderedSubmission) worker(key string, queue *keyedQueue) {
	for {
		// Only this worker advances head. Submitters only append to tail,
		// under mu, so reading the current task needs no lock.
		next := queue.head
		if startAllowed(next.task) {
			next.task.Run(next.ctx)
		}

		s.mu.Lock()
		s.admitted--
		queue.head = next.next
		if queue.head == nil {
			delete(s.keys, key)
			if len(s.keys) == 0 {
				s.keys = nil
			}
			if s.closed && s.admitted == 0 {
				close(s.done)
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}
