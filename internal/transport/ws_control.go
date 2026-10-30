package transport

// The bounded off-loop executor for control work that must not freeze the
// read loop: connections.test probes and dialog.openFile.
//
// Both run on their own goroutine under a capacity-one admission (the
// internal/transport/control seam, nocx-sfv6.3): the read loop submits and
// moves on, the Submission decides whether the work runs now or is refused,
// and the permit is released when the task returns — cancelled or not. A
// task's context derives from the connection context, so a disconnect
// cancels the work, and the same derived context lets Stop cancel every
// in-flight task at shutdown.

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
)

// defaultControlDrainTimeout is the documented maximum Stop waits for
// in-flight off-loop control work to finish after cancelling it. Five
// seconds covers a cooperative task's cancellation round trip with room to
// spare; a task that ignores cancellation is abandoned at this bound
// (forced abandonment — see inflight.waitDrained).
const defaultControlDrainTimeout = 5 * time.Second

// inflightTask is the registration handle of one admitted task. It must have
// nonzero size: distinct live registrations need distinct pointer identities.
type inflightTask struct{ _ byte }

// inflight tracks admitted off-loop control tasks so Stop can cancel them
// and wait, bounded, for them to drain.
//
// begin() is called before TrySubmit, and its Add is serialized with stop()
// under the same mutex: no task can register once shutdown has begun, so a
// WaitGroup.Wait started by Stop observes every task that will ever run —
// the Add/Wait race is closed by construction, not by luck.
type inflight struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	cancels map[*inflightTask]context.CancelFunc
	stopped bool
}

// begin registers a task, deriving a cancellable context from the caller's.
// It reports ok=false once shutdown has begun (the caller must refuse the
// work). The returned release unregisters the task exactly once, cancelling
// its context and freeing the admission permit's WaitGroup slot.
func (t *inflight) begin(ctx context.Context) (tctx context.Context, cancel context.CancelFunc, release func(), ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return nil, nil, nil, false
	}
	tctx, cancel = context.WithCancel(ctx)
	h := &inflightTask{}
	if t.cancels == nil {
		t.cancels = make(map[*inflightTask]context.CancelFunc)
	}
	t.cancels[h] = cancel
	t.wg.Add(1)
	var once sync.Once
	release = func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.cancels, h)
			t.mu.Unlock()
			cancel()
			t.wg.Done()
		})
	}
	return tctx, cancel, release, true
}

// cancelAll cancels every in-flight task. Idempotent; the tasks' own
// release calls unregister them.
func (t *inflight) cancelAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.cancels {
		c()
	}
}

// stop begins shutdown: no new task registers, in-flight ones are cancelled.
func (t *inflight) stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.cancelAll()
}

// waitDrained waits up to timeout for every registered task to finish. It
// reports false on timeout: the remaining tasks are ABANDONED — they may
// still be running and their admission permits stay held until they return.
// That is the forced-abandonment policy for work outside a commit interval:
// the process is exiting, and a dependency that ignores cancellation must
// not be able to hold shutdown hostage past the documented maximum.
func (t *inflight) waitDrained(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// inflightSubmission registers every admitted task with the inflight
// machinery before the inner submission runs it. The probe and dialog tasks
// need Stop to cancel them and wait (bounded) for them to drain, and the
// registration must PRECEDE TrySubmit so the WaitGroup Add cannot race the
// shutdown Wait (the inflight contract: begin is serialized with stop under
// the same mutex). A refusal from the inner submission releases the
// registration and passes the rejection up to the dispatcher.
type inflightSubmission struct {
	inflight *inflight
	inner    control.Submission
}

// Name identifies the resource for metrics only.
func (s *inflightSubmission) Name() string { return "inflight" }

func (s *inflightSubmission) TrySubmit(ctx context.Context, task control.Task) *control.Rejection {
	tctx, _, release, ok := s.inflight.begin(ctx)
	if !ok {
		return &control.Rejection{Reason: "server shutting down", Scope: "control"}
	}
	rej := s.inner.TrySubmit(tctx, control.Task{Key: task.Key, Run: func(pctx context.Context) {
		defer release()
		task.Run(pctx)
	}})
	if rej != nil {
		release()
	}
	return rej
}

// Shutdown fences an owned worker queue as well as its inflight registration.
// The shared inflight tracker supplies cancellation and the bounded join in Stop.
func (s *inflightSubmission) Shutdown() {
	if inner, ok := s.inner.(control.Shutdownable); ok {
		inner.Shutdown()
	}
}

// saturatedNotifyLimiter rate-limits the control.saturated notification that
// a refused NOTIFICATION (no id) triggers: it has no response to carry the
// -32004 error, so the server emits the notification instead — but one per
// refused frame would flood the wire when a burst of notifications is
// refused, so the emission is bounded to at most one per (class, scope) per
// interval. The renderer also deduplicates its toast over a 10 s window;
// this bound protects the wire itself, independent of the renderer.
type saturatedNotifyLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
}

// newSaturatedNotifyLimiter builds a limiter that allows at most one
// notification per key per interval.
func newSaturatedNotifyLimiter(interval time.Duration) *saturatedNotifyLimiter {
	return &saturatedNotifyLimiter{interval: interval, last: make(map[string]time.Time)}
}

// allow reports whether a notification for the key may be emitted now. The
// key is (methodClass, scope) — server vocabulary both, so no request data
// reaches the limiter.
func (l *saturatedNotifyLimiter) allow(class, scope string) bool {
	key := class + "\x00" + scope
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	last, ok := l.last[key]
	if !ok || now.Sub(last) >= l.interval {
		l.last[key] = now
		return true
	}
	return false
}
