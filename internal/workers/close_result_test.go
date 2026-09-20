package workers

// The close's ANSWER (nocx-xn63t.1.3): what the closer says about the
// checkout it left reaches the close's caller untouched, on both routes —
// the live close and the already-finished one — because a finished worker
// is exactly the case a coordinator closes, and its checkout is the thing
// the answer is about. A failed close answers nothing but its error: a
// result riding a failure would be two answers to one call, and the caller
// would have no way to know which to believe.

import (
	"context"
	"errors"
	"testing"
)

func TestACloseAnswersWhatItsCloserSaysItLeft(t *testing.T) {
	ctx := context.Background()

	t.Run("a live close", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		closer.result = CloseResult{Worktree: Leftover{
			Path: "/wt/nocx-feat", Branch: "feat/one", State: CheckoutRead,
			Uncommitted: true, Ahead: 2,
		}}

		got, err := h.reg.Close(ctx, coordSession, p.ID)
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if got != closer.result {
			t.Fatalf("answer = %+v, want the closer's own %+v", got, closer.result)
		}
	})

	t.Run("a close of an already-finished worker", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		finish(t, h, p)
		closer.result = CloseResult{Worktree: Leftover{
			Path: "/wt/nocx-feat", Branch: "feat/one", State: CheckoutUnknown,
		}}

		got, err := h.reg.Close(ctx, coordSession, p.ID)
		if err != nil {
			t.Fatalf("close of a finished worker: %v", err)
		}
		if got != closer.result {
			t.Fatalf("answer = %+v, want the closer's own %+v", got, closer.result)
		}
	})

	t.Run("a failed close answers nothing", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		// A result the closer answered alongside its failure must not reach
		// the caller: the error is the answer.
		closer.err = errInjected
		closer.result = CloseResult{Worktree: Leftover{Path: "/wt", State: CheckoutRead}}

		got, err := h.reg.Close(ctx, coordSession, p.ID)
		if !errors.Is(err, errInjected) {
			t.Fatalf("close = %v, want the closer's failure", err)
		}
		if got != (CloseResult{}) {
			t.Fatalf("answer = %+v alongside an error, want zero", got)
		}
	})
}
