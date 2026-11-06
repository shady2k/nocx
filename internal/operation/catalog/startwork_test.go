package catalog

import (
	"context"
	"testing"
	"time"
)

// Max begins when WORK begins, not when the invocation was made (ADR-8eej3e66
// §3). An operation that waited for a key or a capacity permit must not lose
// part of its work budget to that wait — and its settlement timer must not
// fire while the work is still inside the declared bound.
func TestStartWorkRearmsMaxFromTheMomentWorkStarts(t *testing.T) {
	op := methodHandle("internal.session.endHold") // max 5 s
	s := Begin(context.Background(), op, Invocation{})
	defer s.Settle(OutcomeCompleted)

	before := s.Deadline()
	time.Sleep(60 * time.Millisecond)

	got := s.StartWork()
	if got < 4*time.Second || got > 5*time.Second {
		t.Fatalf("StartWork returned %v, want the declaration's 5 s", got)
	}
	after := s.Deadline()
	if delay := after.Sub(before); delay < 50*time.Millisecond {
		t.Fatalf("deadline moved by %v; StartWork did not re-arm from now", delay)
	}
	if remaining := time.Until(after); remaining < 4900*time.Millisecond || remaining > 5100*time.Millisecond {
		t.Fatalf("deadline is %v away after StartWork, want the full 5 s", remaining)
	}
}

// A child's re-armed max stays within its parent's remainder.
func TestStartWorkKeepsAChildInsideItsParent(t *testing.T) {
	parent := Begin(context.Background(), methodHandle("internal.session.endHold"), Invocation{})
	defer parent.Settle(OutcomeCompleted)
	child := Begin(WithScope(context.Background(), parent), methodHandle("internal.git.process"), Invocation{})
	defer child.Settle(OutcomeCompleted)

	got := child.StartWork()
	if got <= 0 || got > 5*time.Second {
		t.Fatalf("child StartWork = %v, want at most the parent's 5 s", got)
	}
	if deadline := child.Deadline(); deadline.After(parent.Deadline()) {
		t.Fatalf("child deadline %v is after its parent's %v", deadline, parent.Deadline())
	}
}
