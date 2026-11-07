package control

import "context"

// ImmediateSubmission runs every task inline on the caller's goroutine. It is
// for ingress-critical resolvers that must never wait for a slot: they get no
// bound, but also no scheduling delay, and nothing can be refused in front of
// them.
type ImmediateSubmission struct{}

func (ImmediateSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	if startAllowed(task) {
		task.Run(ctx)
	}
	return nil
}

// AsyncSubmission runs every task on its own goroutine and never refuses.
//
// IT IS WHAT A SUBMISSION IS ONCE NOTHING REFUSES ADMISSION (ADR-8eej3e66
// §7, stage 8). The read loop hands the work over and moves on; what bounds
// accepted work is the per-connection credit window (transport/credit.go),
// and what bounds each call is its declaration's turn and max. A worker pool
// that answered "all busy" was the last capacity refusal, and it is gone.
//
// A panic is deliberately not swallowed: it propagates and crashes the
// process, exactly as a panicking task anywhere else does.
type AsyncSubmission struct{}

func (AsyncSubmission) TrySubmit(ctx context.Context, task Task) *Rejection {
	go func() {
		if startAllowed(task) {
			task.Run(ctx)
		}
	}()
	return nil
}
