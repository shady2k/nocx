package capability_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/schedule"
	"github.com/shady2k/nocx/internal/session"
)

// testRepo is a git.Repo whose two watched calls are scripted; everything
// else is an explicit failure, so a test that reaches another call notices.
type testRepo struct {
	status func(context.Context) (git.Status, error)
	stage  func(context.Context) (git.Status, error)
	commit func(context.Context) (git.CommitOutcome, error)
}

func (r *testRepo) Status(ctx context.Context) (git.Status, error) {
	if r.status != nil {
		return r.status(ctx)
	}
	return git.Status{}, nil
}

func (r *testRepo) EnvState() (git.EnvState, string) { return git.EnvResolved, "" }

func (r *testRepo) Diff(context.Context, string, git.Side, int64) (git.Diff, error) {
	return git.Diff{}, errors.New("testRepo: Diff not scripted")
}

func (r *testRepo) Log(context.Context, int) (git.Log, error) {
	return git.Log{}, errors.New("testRepo: Log not scripted")
}

func (r *testRepo) Stage(ctx context.Context, _ []string) (git.Status, error) {
	if r.stage != nil {
		return r.stage(ctx)
	}
	return git.Status{}, nil
}

func (r *testRepo) Unstage(context.Context, []string) (git.Status, error) {
	return git.Status{}, errors.New("testRepo: Unstage not scripted")
}

func (r *testRepo) StageAll(context.Context) (git.Status, error) {
	return git.Status{}, errors.New("testRepo: StageAll not scripted")
}

func (r *testRepo) UnstageAll(context.Context) (git.Status, error) {
	return git.Status{}, errors.New("testRepo: UnstageAll not scripted")
}

func (r *testRepo) Commit(ctx context.Context, _ string, _ bool) (git.CommitOutcome, error) {
	if r.commit != nil {
		return r.commit(ctx)
	}
	return git.CommitOutcome{}, errors.New("testRepo: Commit not scripted")
}

func (r *testRepo) HeadMessage(context.Context) (git.HeadMessage, error) {
	return git.HeadMessage{}, errors.New("testRepo: HeadMessage not scripted")
}

func (r *testRepo) RemoteURL(context.Context) (string, error) {
	return "", errors.New("testRepo: RemoteURL not scripted")
}

func (r *testRepo) AddWorktree(context.Context, string, string, string) (git.WorktreeAdded, error) {
	return git.WorktreeAdded{}, errors.New("testRepo: AddWorktree not scripted")
}

func (r *testRepo) Worktrees(context.Context, string) ([]git.Worktree, error) {
	return nil, errors.New("testRepo: Worktrees not scripted")
}

func (r *testRepo) RemoveWorktree(context.Context, string, git.RemoveWorktreeOptions) (git.WorktreeDiscard, error) {
	return git.WorktreeDiscard{}, errors.New("testRepo: RemoveWorktree not scripted")
}

func (r *testRepo) DeleteWorktreeBranch(context.Context, string, string) error {
	return errors.New("testRepo: DeleteWorktreeBranch not scripted")
}
func (r *testRepo) Close() error { return nil }

func newGitGates() (*schedule.Keyed, *schedule.Bulkhead) {
	order := schedule.NewKeyed("git", 16)
	bulkhead := schedule.NewBulkhead("git", schedule.Limits{
		Capacity: 8, PersonReserved: 2, Queue: 32, AbandonedPerDomain: 8, AbandonedGlobal: 32,
	})
	return order, bulkhead
}

func newGitBindingOp(reg *registry.Registry, order *schedule.Keyed, bulkhead *schedule.Bulkhead) capability.GitBindingOperation {
	return capability.NewGitBindingOperation(order, bulkhead, testLane(), reg)
}

type ownsAll struct{}

func (ownsAll) Owns(session.ID) bool { return true }

// stageViaBinding is a handler's shape: acquire the binding, call the
// provider, release. The operation orders and bounds the whole callback.
func stageViaBinding(id string) func(context.Context, capability.GitBindingService) error {
	return func(ctx context.Context, svc capability.GitBindingService) error {
		h, release, err := svc.Acquire(id, ownsAll{})
		if err != nil {
			return err
		}
		defer release()
		_, err = h.Stage(ctx, []string{"f"})
		return err
	}
}

func statusViaBinding(id string) func(context.Context, capability.GitBindingService) error {
	return func(ctx context.Context, svc capability.GitBindingService) error {
		h, release, err := svc.Acquire(id, ownsAll{})
		if err != nil {
			return err
		}
		defer release()
		_, err = h.Status(ctx)
		return err
	}
}

func scopeCtx(ctx context.Context, op catalog.Method) context.Context {
	return catalog.WithScope(ctx, catalog.Begin(ctx, op, catalog.Invocation{}))
}

// A mutation on one checkout runs to completion while another checkout's
// mutation is blocked for ever: one hung mount does not stop the other.
func TestGitBindingUnrelatedCheckoutCompletesBesideABlockedOne(t *testing.T) {
	reg := registry.New()
	blocked := make(chan struct{})
	repoA := &testRepo{stage: func(ctx context.Context) (git.Status, error) {
		close(blocked)
		<-ctx.Done()
		return git.Status{}, ctx.Err()
	}}
	idA, err := reg.Register(repoA, "s1", "/repo/a", "mount:a")
	if err != nil {
		t.Fatal(err)
	}
	var stagedB sync.WaitGroup
	stagedB.Add(1)
	repoB := &testRepo{stage: func(context.Context) (git.Status, error) {
		stagedB.Done()
		return git.Status{}, nil
	}}
	idB, err := reg.Register(repoB, "s1", "/repo/b", "mount:b")
	if err != nil {
		t.Fatal(err)
	}

	order, bulkhead := newGitGates()
	op := newGitBindingOp(reg, order, bulkhead)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	doneA := make(chan error, 1)
	go func() {
		doneA <- op.Run(scopeCtx(ctxA, catalog.OpGitStage), idA, stageViaBinding(idA))
	}()
	<-blocked

	doneB := make(chan error, 1)
	go func() {
		doneB <- op.Run(scopeCtx(context.Background(), catalog.OpGitStage), idB, stageViaBinding(idB))
	}()
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatalf("the healthy checkout's mutation failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the healthy checkout waited on the blocked one")
	}
	stagedB.Wait()
	cancelA()
	<-doneA
}

// Reads take no key: a status on a checkout whose mutation is blocked runs
// beside it.
func TestGitBindingReadRunsBesideItsOwnBlockedMutation(t *testing.T) {
	reg := registry.New()
	blocked := make(chan struct{})
	repo := &testRepo{
		stage: func(ctx context.Context) (git.Status, error) {
			close(blocked)
			<-ctx.Done()
			return git.Status{}, ctx.Err()
		},
		status: func(context.Context) (git.Status, error) { return git.Status{}, nil },
	}
	id, err := reg.Register(repo, "s1", "/repo/a", "mount:a")
	if err != nil {
		t.Fatal(err)
	}
	order, bulkhead := newGitGates()
	op := newGitBindingOp(reg, order, bulkhead)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = op.Run(scopeCtx(ctx, catalog.OpGitStage), id, stageViaBinding(id))
	}()
	<-blocked

	done := make(chan error, 1)
	go func() {
		done <- op.Run(scopeCtx(context.Background(), catalog.OpGitStatus), id, statusViaBinding(id))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("status: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a status waited behind its checkout's mutation")
	}
	cancel()
}

// A queued mutation whose request is cancelled never starts: the fn is not
// called, and the checkout's queue passes the withdrawn entry by.
func TestGitBindingCancelledQueuedMutationNeverStarts(t *testing.T) {
	reg := registry.New()
	released := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	repo := &testRepo{stage: func(context.Context) (git.Status, error) {
		startOnce.Do(func() { close(started) })
		<-released
		return git.Status{}, nil
	}}
	id, err := reg.Register(repo, "s1", "/repo/a", "mount:a")
	if err != nil {
		t.Fatal(err)
	}
	order, bulkhead := newGitGates()
	op := newGitBindingOp(reg, order, bulkhead)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- op.Run(scopeCtx(context.Background(), catalog.OpGitStage), id, stageViaBinding(id))
	}()
	<-started

	var ran sync.Once
	ranCount := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- op.Run(scopeCtx(ctx, catalog.OpGitStage), id, func(ctx context.Context, svc capability.GitBindingService) error {
			ran.Do(func() { close(ranCount) })
			return stageViaBinding(id)(ctx, svc)
		})
	}()
	// Wait until the second call is queued on the checkout, then withdraw it.
	waitFor(t, func() bool { return order.Waiting("/repo/a") == 1 })
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("withdrawn call err = %v, want context.Canceled", err)
	}

	close(released)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	// A third mutation behind the withdrawn one proves the queue passed it.
	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- op.Run(scopeCtx(context.Background(), catalog.OpGitStage), id, stageViaBinding(id))
	}()
	if err := <-thirdDone; err != nil {
		t.Fatalf("third mutation: %v", err)
	}
	select {
	case <-ranCount:
		t.Fatal("the withdrawn mutation ran")
	default:
	}
}

// A read whose bulkhead wait reaches the declaration's turn never starts, and
// a read on another mount completes while the first mount is saturated.
func TestGitBindingReadExpiresAtItsTurnOnASaturatedMount(t *testing.T) {
	reg := registry.New()
	order, bulkhead := newGitGates()
	var started sync.WaitGroup
	started.Add(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 8; i++ {
		repo := &testRepo{status: func(ctx context.Context) (git.Status, error) {
			started.Done()
			<-ctx.Done()
			return git.Status{}, ctx.Err()
		}}
		id, err := reg.Register(repo, "s1", fmt.Sprintf("/repo/a%d", i), "mount:a")
		if err != nil {
			t.Fatal(err)
		}
		op := newGitBindingOp(reg, order, bulkhead)
		go func() {
			_ = op.Run(scopeCtx(ctx, catalog.OpGitStatus), id, statusViaBinding(id))
		}()
	}
	started.Wait()

	healthyRepo := &testRepo{status: func(context.Context) (git.Status, error) { return git.Status{}, nil }}
	healthyID, err := reg.Register(healthyRepo, "s1", "/repo/b", "mount:b")
	if err != nil {
		t.Fatal(err)
	}
	healthyOp := newGitBindingOp(reg, order, bulkhead)
	healthy := make(chan error, 1)
	go func() {
		healthy <- healthyOp.Run(scopeCtx(context.Background(), catalog.OpGitStatus), healthyID, statusViaBinding(healthyID))
	}()
	select {
	case herr := <-healthy:
		if herr != nil {
			t.Fatalf("healthy mount read: %v", herr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read on a healthy mount waited behind a saturated one")
	}

	// The ninth read on the saturated mount waits its 1 s turn and answers
	// not-started; its work never runs.
	waiter := &testRepo{status: func(context.Context) (git.Status, error) {
		t.Error("a read past its turn reached the provider")
		return git.Status{}, nil
	}}
	waiterID, err := reg.Register(waiter, "s1", "/repo/a9", "mount:a")
	if err != nil {
		t.Fatal(err)
	}
	waiterOp := newGitBindingOp(reg, order, bulkhead)
	err = waiterOp.Run(scopeCtx(context.Background(), catalog.OpGitStatus), waiterID, func(context.Context, capability.GitBindingService) error {
		t.Error("a read past its turn ran")
		return nil
	})
	var notStarted *capability.NotStartedError
	if !errors.As(err, &notStarted) {
		t.Fatalf("ninth read err = %v, want NotStartedError", err)
	}
}

// waitFor polls an observable condition with a deadline; it is the test form
// of "wait until the product says the thing happened", never a fixed sleep.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
