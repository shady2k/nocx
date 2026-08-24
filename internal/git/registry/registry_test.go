package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/session"
)

// stubRepo is a Repo that records calls and can block them.
type stubRepo struct {
	mu       sync.Mutex
	closeErr error
	calls    int
	inflight int
	maxIn    int
	block    chan struct{} // when non-nil, Status blocks until it receives
	closed   bool
}

func (s *stubRepo) Status(ctx context.Context) (git.Status, error) {
	s.mu.Lock()
	s.calls++
	s.inflight++
	if s.inflight > s.maxIn {
		s.maxIn = s.inflight
	}
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()
	return git.Status{Staged: []git.Entry{}, Unstaged: []git.Entry{}, Conflicted: []git.Entry{}}, nil
}

func (s *stubRepo) EnvState() (git.EnvState, string) {
	return git.EnvResolved, ""
}

func (s *stubRepo) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	return git.Diff{}, nil
}

func (s *stubRepo) Log(ctx context.Context, max int) (git.Log, error) {
	return git.Log{Entries: []git.LogEntry{}}, nil
}

func (s *stubRepo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	return s.Status(ctx)
}

func (s *stubRepo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	return s.Status(ctx)
}

func (s *stubRepo) StageAll(ctx context.Context) (git.Status, error) { return s.Status(ctx) }

func (s *stubRepo) UnstageAll(ctx context.Context) (git.Status, error) { return s.Status(ctx) }

func (s *stubRepo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	return git.CommitOutcome{State: git.CommitOK}, nil
}

func (s *stubRepo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	return git.HeadMessage{State: git.HeadMessageOK}, nil
}

func (s *stubRepo) RemoteURL(ctx context.Context) (string, error) {
	return "git@github.com:shady2k/nocx.git", nil
}

func (s *stubRepo) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.closeErr
}

func (s *stubRepo) inflightCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight
}

type ownsAll struct{}

func (ownsAll) Owns(session.ID) bool { return true }

type ownsNone struct{}

func (ownsNone) Owns(session.ID) bool { return false }

func TestRegisterMintsUnguessableIDs(t *testing.T) {
	reg := New()
	id1, err := reg.Register(&stubRepo{}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := reg.Register(&stubRepo{}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("two registrations minted the same id")
	}
	if len(id1) != 32 {
		t.Fatalf("id %q is not 16 random bytes in hex", id1)
	}
	if _, err := reg.Register(nil, "s1"); err == nil {
		t.Fatal("Register accepted a nil repo")
	}
}

// TestRegisterRejectsTypedNilRepo: `repo == nil` catches a nil interface but
// not an interface holding a typed-nil pointer, which registers fine and
// panics on first use or close.
func TestRegisterRejectsTypedNilRepo(t *testing.T) {
	reg := New()
	var r *stubRepo // typed nil: non-nil interface, nil pointer
	if _, err := reg.Register(r, "s1"); err == nil {
		t.Fatal("Register accepted a typed-nil repo")
	}
}

func TestRegisterMintingFailureLeavesNoBinding(t *testing.T) {
	orig := newBindingID
	newBindingID = func() (string, error) { return "", errors.New("rand broke") }
	t.Cleanup(func() { newBindingID = orig })

	reg := New()
	id, err := reg.Register(&stubRepo{}, "s1")
	if err == nil {
		t.Fatal("Register succeeded with a broken id mint")
	}
	if id != "" {
		t.Fatalf("Register returned id %q on failure", id)
	}
	// No binding was returned, so the registry holds nothing — this is the
	// "no binding returned" half of the ownership rule. (The repo-closing
	// half is the composition layer's, per spec §5.1 rule 2.)
	if _, _, err := reg.Acquire("anything", ownsAll{}); err == nil {
		t.Fatal("registry has a binding after a failed Register")
	}
}

func TestAcquireChecksOwnership(t *testing.T) {
	reg := New()
	id, err := reg.Register(&stubRepo{}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, aerr := reg.Acquire(id, ownsNone{}); aerr == nil {
		t.Fatal("Acquire allowed a caller that does not own the session")
	} else if _, ok := aerr.(*ErrNotOwned); !ok {
		t.Fatalf("Acquire returned %T, want *ErrNotOwned", aerr)
	}
	if _, _, aerr := reg.Acquire("nope", ownsAll{}); aerr == nil {
		t.Fatal("Acquire allowed an unknown binding")
	} else if _, ok := aerr.(*ErrUnknownBinding); !ok {
		t.Fatalf("Acquire returned %T, want *ErrUnknownBinding", aerr)
	}
	if _, _, aerr := reg.Acquire(id, nil); aerr == nil {
		t.Fatal("Acquire allowed a nil caller")
	}
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Status(context.Background()); err != nil {
		t.Fatalf("handle Status: %v", err)
	}
	release()
}

func TestHandleInvalidAfterRelease(t *testing.T) {
	reg := New()
	id, _ := reg.Register(&stubRepo{}, "s1")
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // idempotent
	if _, err := h.Status(context.Background()); err == nil {
		t.Fatal("handle usable after release")
	} else if _, ok := err.(*ErrHandleReleased); !ok {
		t.Fatalf("released handle returned %T, want *ErrHandleReleased", err)
	}
}

// TestBindingInterval is the binding interval, stated with both ends: a
// binding is reachable from the moment Register returns until Close returns,
// and no Repo call is in flight after Close returns.
func TestBindingInterval(t *testing.T) {
	repo := &stubRepo{block: make(chan struct{})}
	reg := New()
	id, err := reg.Register(repo, "s1")
	if err != nil {
		t.Fatal(err)
	}

	// Reachable from the moment Register returns.
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatalf("binding not reachable after Register: %v", err)
	}

	// A call in flight.
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = h.Status(context.Background())
	}()
	waitInflight(t, repo)

	// Close must NOT return while the call is in flight.
	closed := make(chan struct{})
	go func() {
		reg.Close(id) //nolint:errcheck,gosec // Close's error is not this test's assertion; the select below asserts that it blocks
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a Repo call was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the call finish and release both guards; Close now completes.
	close(repo.block)
	<-callDone
	release()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the in-flight call drained")
	}

	// No Repo call is in flight after Close returns.
	if repo.inflightCount() != 0 {
		t.Fatalf("%d calls in flight after Close returned", repo.inflightCount())
	}
	if !repo.closed {
		t.Fatal("repo was not closed")
	}

	// Not reachable after Close: unknown binding, and the old handle errors.
	if _, _, err := reg.Acquire(id, ownsAll{}); err == nil {
		t.Fatal("Acquire succeeded after Close")
	}
	if _, err := h.Status(context.Background()); err == nil {
		t.Fatal("old handle usable after Close")
	}
}

// TestCloseWaitsForEveryMethodGuard: each handle method takes its own guard,
// so a call in flight through ANY method keeps Close waiting.
func TestCloseWaitsForEveryMethodGuard(t *testing.T) {
	repo := &stubRepo{block: make(chan struct{})}
	reg := New()
	id, _ := reg.Register(repo, "s1")
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Stage(context.Background(), []string{"x"})
	}()
	waitInflight(t, repo)

	closed := make(chan struct{})
	go func() {
		reg.Close(id) //nolint:errcheck,gosec // Close's error is not this test's assertion; the select below asserts that it blocks
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a Stage call was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(repo.block)
	<-done
	release() // the acquire's own guard; Close waits for it too
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not drain the Stage call")
	}
}

// TestMethodRefusedAfterCloseBegins: once Close has begun, no new method may
// start — the closed flag flips at the very start of close, before it waits.
func TestMethodRefusedAfterCloseBegins(t *testing.T) {
	repo := &stubRepo{block: make(chan struct{})}
	reg := New()
	id, _ := reg.Register(repo, "s1")
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Status(ctx)
	}()
	waitInflight(t, repo)

	go reg.Close(id) //nolint:errcheck // the error is not this test's assertion (the poll below is); sets closed, then waits for the in-flight guard

	// Poll until a new method is refused: Close's closed flag flips at its
	// very start, so this becomes true and stays true.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := h.Diff(ctx, "x", git.SideStaged, 100); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("methods kept running after Close began")
		}
		time.Sleep(time.Millisecond)
	}

	close(repo.block)
	<-done
	// release() (the deferred one) drops the acquire guard; Close completes.
}

// TestNoCallAfterCloseReturns: after Close returns, every handle method is
// refused — the second end of the binding interval.
func TestNoCallAfterCloseReturns(t *testing.T) {
	reg := New()
	repo := &stubRepo{}
	id, _ := reg.Register(repo, "s1")
	h, release, err := reg.Acquire(id, ownsAll{})
	if err != nil {
		t.Fatal(err)
	}
	release() // nothing in flight; Close can complete
	if err := reg.Close(id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Status(context.Background()); err == nil {
		t.Fatal("method ran after Close returned")
	}
}

func TestCloseSurfacesRepoCloseError(t *testing.T) {
	reg := New()
	repo := &stubRepo{closeErr: errors.New("repo teardown failed")}
	id, _ := reg.Register(repo, "s1")
	err := reg.Close(id)
	if err == nil || !errors.Is(err, repo.closeErr) {
		t.Fatalf("Close returned %v, want the repo close error", err)
	}
}

func TestCloseSessionClosesOnlyThatSession(t *testing.T) {
	reg := New()
	repoA := &stubRepo{}
	repoB := &stubRepo{}
	repoC := &stubRepo{}
	if _, err := reg.Register(repoA, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(repoB, "s1"); err != nil {
		t.Fatal(err)
	}
	idC, _ := reg.Register(repoC, "s2")

	reg.CloseSession("s1")

	if !repoA.closed || !repoB.closed {
		t.Fatal("CloseSession left a same-session repo open")
	}
	if repoC.closed {
		t.Fatal("CloseSession closed another session's repo")
	}
	if _, _, err := reg.Acquire(idC, ownsAll{}); err != nil {
		t.Fatalf("other session's binding unreachable after CloseSession: %v", err)
	}
}

func waitInflight(t *testing.T, repo *stubRepo) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for repo.inflightCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if repo.inflightCount() == 0 {
		t.Fatal("call never started")
	}
}
