package capability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/schedule"
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
// The session is nil on purpose: nothing on the caller's path may dereference
// it, so a caller that still read the disk would panic here instead of
// passing. Both derivation seams are replaced, because both run on the worker.
func TestGitOpenSettlesItsPromiseWhileTheDomainCannotBeRead(t *testing.T) {
	release := make(chan struct{}, 1)
	op := NewGitOpenOperation(Gate(GateSession, 1, time.Second), testGitBulkhead(), nil, nil, registry.New())
	open, ok := op.(*gitOpenOperation)
	if !ok {
		t.Fatalf("the constructor answered %T, not the git.open operation", op)
	}
	open.provisionalDomain = func(session.Session, string) string { return "checkout:/repo/a" }
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

// A derivation that never returns — the one stat of a hung mount — must be a
// BOUNDED worker of its domain, not a goroutine nobody counts.
//
// The caller's turn settles the call at its deadline either way. What the old
// shape lost was the other half: the derivation ran before any permit was
// taken, so the worker went on blocking in the kernel holding nothing, the
// domain's abandoned count stayed at zero and its breaker never learned the
// disk had stopped answering. Repeated opens therefore left one stuck
// goroutine per attempt, without limit, on a mount that was never going to
// answer (ADR-8eej3e66 §6).
func TestGitOpenBoundsRepeatedOpensWhileTheDomainCannotBeRead(t *testing.T) {
	const capacity = 8
	release := make(chan struct{})
	var entered atomic.Int32
	var ranWork atomic.Int32
	bulkhead := schedule.NewBulkhead("git", schedule.Limits{
		Capacity: capacity, PersonReserved: 2,
		AbandonedPerDomain: 2, AbandonedGlobal: 32,
	})
	op := NewGitOpenOperation(Gate(GateSession, 1, time.Second), bulkhead, nil, nil, registry.New())
	open, ok := op.(*gitOpenOperation)
	if !ok {
		t.Fatalf("the constructor answered %T, not the git.open operation", op)
	}
	open.domain = func(session.Session, string) string {
		entered.Add(1)
		<-release
		return "mount:a"
	}
	open.provisionalDomain = func(session.Session, string) string { return "checkout:/repo/hung" }

	openOnce := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		ctx = catalog.WithScope(ctx, catalog.Begin(ctx, catalog.OpGitOpen, catalog.Invocation{}))
		return open.Open(ctx, nil, "/repo/hung", func(context.Context, GitOpenService) error {
			ranWork.Add(1)
			return nil
		})
	}
	// Every call a person makes is answered, and at most one answer arrives
	// per call: the promise never outlives its deadline.
	answered := make(chan error, 1)
	for i := 0; i < 3*capacity; i++ {
		go func() { answered <- openOnce() }()
		select {
		case err := <-answered:
			if err == nil {
				t.Fatalf("open %d answered nil while its domain never returned", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("open %d never answered", i)
		}
	}
	if got := ranWork.Load(); got != 0 {
		t.Fatalf("%d opens ran their work after the caller had been answered", got)
	}
	// Bounded: only the permits the domain holds can be blocked in the
	// derivation. The old shape entered once per attempt.
	if got := entered.Load(); got > capacity {
		t.Fatalf("%d derivations blocked at once, want at most the domain's %d permits", got, capacity)
	}

	// And the breaker reads it: the abandoned workers made the domain
	// suspect, so a further open is refused at once, naming the domain,
	// rather than leaving one more worker stuck in the kernel.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = catalog.WithScope(ctx, catalog.Begin(ctx, catalog.OpGitOpen, catalog.Invocation{}))
	err := open.Open(ctx, nil, "/repo/hung", func(context.Context, GitOpenService) error {
		ranWork.Add(1)
		return nil
	})
	var expired *ExpiredError
	if !errors.As(err, &expired) || !expired.Suspected {
		t.Fatalf("open on the stuck domain = %v, want the breaker's refusal", err)
	}
	if expired.Domain != "checkout:/repo/hung" {
		t.Fatalf("refusal names domain %q, want the path whose derivation is stuck", expired.Domain)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for bulkhead.Domains() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := bulkhead.Domains(); got != 0 {
		t.Fatalf("%d failure domains still hold a permit after their workers returned", got)
	}
}
