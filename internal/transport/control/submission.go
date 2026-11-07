package control

import "context"

// ImmediateSubmission runs every task inline on the caller's goroutine and
// never refuses. It is for ingress-critical resolvers that must never wait
// for a slot: they get no bound, but also no scheduling delay.
type ImmediateSubmission struct{}

func (ImmediateSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	if startAllowed(task) {
		task.Run(ctx)
	}
	return nil
}

// boundedSubmission runs tasks on their own goroutines under an Admission.
type boundedSubmission struct {
	admission Admission
}

// NewBoundedSubmission returns a Submission that acquires a Permit from the
// given NON-BLOCKING admission, runs the task on its own goroutine, and
// releases the Permit when the task returns. If the Admission refuses, the
// task is NOT run and the rejection is returned without blocking — the read
// loop may submit and move on either way.
//
// The parameter type is the guard: only a NonblockingAdmission can be wired
// here, so a waiting admission (NewWaitingSemaphore — TryAcquire may block up
// to its wait timeout) cannot reach a Submission's TrySubmit, which the read
// loop calls. The miswiring is a compile error (ADR-0026).
func NewBoundedSubmission(a NonblockingAdmission) Submission {
	return &boundedSubmission{admission: a}
}

func (s *boundedSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	permit, rej := s.admission.TryAcquire(ctx)
	if rej != nil {
		return rej
	}
	go runAndRelease(permit, ctx, task)
	return nil
}

// runAndRelease guarantees the Permit is released on every exit path: normal
// return, context cancellation, or panic (the deferred Release runs during
// panic unwinding). A panic is deliberately not swallowed: it propagates and
// crashes the process, but never leaks the Permit.
func runAndRelease(permit Permit, ctx context.Context, task Task) {
	defer permit.Release()
	if startAllowed(task) {
		task.Run(ctx)
	}
}
