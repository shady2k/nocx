package capability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/session"
)

// The open's failure domain is derived ON THE WORKER, not on the caller.
//
// The derivation reads the cwd's device, which is one stat, and a stat on a
// hung NFS or FUSE mount never returns. While it ran on the caller's own
// goroutine — before gitRun's lifetime existed — no turn, no cancellation and
// no max could settle the call, so the action's promise stayed pending for
// ever (ADR-8eej3e66 §2: no operation exists without a stated lifetime).
//
// The session is nil on purpose: the real derivation dereferences it, so a
// caller path that still read the disk would panic here instead of passing.
func TestGitOpenSettlesItsPromiseWhileTheDomainCannotBeRead(t *testing.T) {
	release := make(chan struct{}, 1)
	op := NewGitOpenOperation(Gate(GateSession, 1, time.Second), testGitBulkhead(), nil, nil, registry.New())
	open, ok := op.(*gitOpenOperation)
	if !ok {
		t.Fatalf("the constructor answered %T, not the git.open operation", op)
	}
	open.domain = func(session.Session, string) string {
		<-release
		return "mount:a"
	}

	// The parent's deadline stands in for the caller's budget: a turn or a
	// lost backend both end the promise, and neither reached the old caller
	// path.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ctx = catalog.WithScope(ctx, catalog.Begin(ctx, catalog.OpGitOpen, catalog.Invocation{}))

	answer := make(chan error, 1)
	go func() {
		answer <- op.Open(ctx, nil, "/repo/a", func(context.Context, GitOpenService) error {
			t.Error("the open ran after its deadline passed")
			return nil
		})
	}()

	select {
	case err := <-answer:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Open err = %v, want the deadline that ended the promise", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the open's promise outlived its deadline: the domain was still read on the caller")
	}

	// The worker that was still reading the disk when the caller answered must
	// not start the open, and must give back the permit it takes afterwards.
	release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for open.bulkhead.Domains() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := open.bulkhead.Domains(); got != 0 {
		t.Fatalf("%d failure domains still hold a permit after the abandoned worker returned", got)
	}
}
