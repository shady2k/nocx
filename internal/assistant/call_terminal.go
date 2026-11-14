package assistant

import (
	"context"
	"sync"
)

// CallTerminal orders cancellation against completion for one invocation.
// The zero value is pending. Completion is claimed after dispatch and before
// recording its outcome; the ledger and delivery must use that same verdict.
// Once claimed, a late cancel cannot rewrite a completed attempt or suppress
// its answer. Cancellation that wins first withholds the result instead.
type CallTerminal struct {
	mu        sync.Mutex
	decided   bool
	cancelled bool
	err       error
}

// Cancel claims a pending call for cancellation. The caller cancels the
// executor's context only when this returns true.
func (t *CallTerminal) Cancel(cause error) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.decided {
		return false
	}
	if cause == nil {
		cause = context.Canceled
	}
	t.decided, t.cancelled, t.err = true, true, cause
	return true
}

// Complete freezes the dispatch verdict, including cancellation already
// observed on its context. Repeated calls return the same verdict, even if
// cancellation arrives while the attempt is being recorded.
func (t *CallTerminal) Complete(ctx context.Context, dispatchErr error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.complete(ctx, dispatchErr)
}

func (t *CallTerminal) complete(ctx context.Context, dispatchErr error) error {
	if !t.decided {
		if cause := context.Cause(ctx); cause != nil {
			dispatchErr = cause
			t.cancelled = true
		}
		t.decided, t.err = true, dispatchErr
	}
	return t.err
}

// Delivery applies the terminal decision at the carrier boundary. A completed
// dispatcher may still report a bookkeeping failure; preserve that error rather
// than replacing it with the earlier successful execution verdict.
func (t *CallTerminal) Delivery(ctx context.Context, dispatchErr error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.decided {
		return t.complete(ctx, dispatchErr)
	}
	if t.cancelled {
		return t.err
	}
	return dispatchErr
}
