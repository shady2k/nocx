package vault

// The coalescing seam: a sealed vault raises ONE unlock prompt and every
// caller that arrives while it is outstanding joins it. These tests pin the
// join, the fan-out, the reason composition and the partial failures — a
// caller that gives up, the leader that gives up, the vault closing with
// waiters outstanding — against a fake requester. The one-prompt count over
// the REAL socket lives in internal/transport (unlock_coalesce_test.go).

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/testwait"
)

// countPrefixRe strips the composition prefix ("3 operations need the
// vault: ...") from a recorded reason so its remaining sentences can be
// checked against the callers that asked.
var countPrefixRe = regexp.MustCompile(`^\d+ operations need the vault: `)

// fakeUnlockRequester records every RequestUnlock call and, when release is
// non-nil, blocks until release delivers an error or the context ends —
// mirroring the transport's RequestUnlock, whose ask lives until the
// renderer resolves it or the context is done.
type fakeUnlockRequester struct {
	mu      sync.Mutex
	calls   int
	reasons []string
	entered chan struct{} // buffered; signaled on every call
	release chan error    // nil: resolve instantly with nil
}

func (f *fakeUnlockRequester) RequestUnlock(ctx context.Context, reason string) error {
	f.mu.Lock()
	f.calls++
	f.reasons = append(f.reasons, reason)
	f.mu.Unlock()
	select {
	case f.entered <- struct{}{}:
	default:
	}
	if f.release == nil {
		return nil
	}
	select {
	case err := <-f.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeUnlockRequester) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeUnlockRequester) recordedReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reasons...)
}

// waitForJoined blocks until n callers have joined the outstanding prompt.
// It waits on the observable join state (the prompt's waiter count), never
// on a duration: the callers are already running, so the count reaches n
// deterministically. Racing tests use it so the resolution is released only
// after every caller is known to be waiting — a caller released too early
// would (correctly) raise a fresh prompt and wedge the fake's single answer.
func waitForJoined(t *testing.T, v *Vault, n int) {
	t.Helper()
	testwait.WaitFor(t, "callers to join the outstanding prompt", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		p := v.unlockPending
		if p == nil {
			return false
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.reasons) >= n
	})
}

// sealedVault builds a vault that is set up and sealed, with the fake
// requester attached.
func sealedVault(t *testing.T, req *fakeUnlockRequester) *Vault {
	t.Helper()
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	mustSetup(t, v, "hunter2")
	v.Seal()
	if v.State() != StateSealed {
		t.Fatalf("state = %v, want sealed", v.State())
	}
	if req != nil {
		v.SetUnlockRequester(req)
	}
	return v
}

func TestEnsureUnsealed_UnsealedReturnsNilWithoutPrompt(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	mustSetup(t, v, "hunter2")
	req := &fakeUnlockRequester{entered: make(chan struct{}, 1)}
	v.SetUnlockRequester(req)

	if err := v.EnsureUnsealed(context.Background(), "history needs the content key"); err != nil {
		t.Fatalf("EnsureUnsealed on an unsealed vault = %v, want nil", err)
	}
	if req.callCount() != 0 {
		t.Fatalf("an unsealed vault raised %d prompts, want 0", req.callCount())
	}
}

func TestEnsureUnsealed_UninitializedReturnsErrWithoutPrompt(t *testing.T) {
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	req := &fakeUnlockRequester{entered: make(chan struct{}, 1)}
	v.SetUnlockRequester(req)

	err := v.EnsureUnsealed(context.Background(), "history needs the content key")
	if !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("EnsureUnsealed on an uninitialized vault = %v, want ErrVaultUninitialized", err)
	}
	if req.callCount() != 0 {
		t.Fatalf("an uninitialized vault raised %d prompts, want 0", req.callCount())
	}
}

func TestEnsureUnsealed_SealedWithoutRequesterKeepsOldAnswer(t *testing.T) {
	// A vault with no prompt carrier answers a sealed call with
	// ErrVaultSealed — the exact answer every existing caller sees today.
	// The seam must not change what a caller sees when it is not wired.
	v := sealedVault(t, nil)

	err := v.EnsureUnsealed(context.Background(), "history needs the content key")
	if !errors.Is(err, ErrVaultSealed) {
		t.Fatalf("EnsureUnsealed without a requester = %v, want ErrVaultSealed", err)
	}
}

func TestEnsureUnsealed_ThreeCallersRaiseOnePrompt(t *testing.T) {
	// The COUNT: three callers racing a sealed vault produce exactly one
	// RequestUnlock, and one answer resolves all of them. The fake blocks
	// until released, so the first prompt stays pending and callers two and
	// three have no choice but to join it — the count is deterministic.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 1),
		release: make(chan error, 1),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	ctx := context.Background()
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(n int) {
			done <- v.EnsureUnsealed(ctx, "operation "+string(rune('a'+n)))
		}(i)
	}

	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first prompt was never raised")
	}

	// All three callers are waiting on that one prompt. Resolve it.
	waitForJoined(t, v, 3)
	req.release <- nil

	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("caller %d = %v, want nil after a successful unlock", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d was never released by the single resolution", i)
		}
	}

	if n := req.callCount(); n != 1 {
		t.Fatalf("three racing callers raised %d prompts, want exactly 1", n)
	}
}

func TestEnsureUnsealed_ReasonIsComposedFromEveryJoinedWaiter(t *testing.T) {
	// The prompt's dialog text accounts for every waiting caller known at
	// raise time: the count and each sentence, never just the first.
	p := &unlockPrompt{done: make(chan struct{})}
	p.join("ssh srv-01 needs the vault")
	p.join("history needs the content key")
	p.join("ssh srv-02 needs the vault")

	got := p.reason()
	want := "3 operations need the vault: ssh srv-01 needs the vault; history needs the content key; ssh srv-02 needs the vault"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestEnsureUnsealed_SingleCallerKeepsItsOwnSentence(t *testing.T) {
	p := &unlockPrompt{done: make(chan struct{})}
	p.join("history needs the content key")
	if got, want := p.reason(), "history needs the content key"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestEnsureUnsealed_CancelledResolutionFansOutToEveryWaiter(t *testing.T) {
	// "Not unlocked, everyone gets the error": the requester's outcome is
	// passed through verbatim to every waiter — a caller that depended on
	// ErrUnlockCancelled still sees it, because it never became anything else.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 1),
		release: make(chan error, 1),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}
	errCancelled := errors.New("unlock cancelled by user")

	ctx := context.Background()
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { done <- v.EnsureUnsealed(ctx, "operation") }()
	}
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never raised")
	}
	waitForJoined(t, v, 3)
	req.release <- errCancelled

	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, errCancelled) {
				t.Errorf("caller %d = %v, want the requester's error verbatim", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d was never released by the cancelled resolution", i)
		}
	}
	if n := req.callCount(); n != 1 {
		t.Fatalf("three racing callers raised %d prompts, want exactly 1", n)
	}
}

func TestEnsureUnsealed_LeaderCancelledOthersStillWait(t *testing.T) {
	// The first caller's context is cancelled while others still wait: the
	// leader is released with its own error and the prompt keeps serving the
	// rest — the ask runs on the vault's context, not the leader's.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 1),
		release: make(chan error, 1),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- v.EnsureUnsealed(leaderCtx, "first pane") }()
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never raised")
	}

	// A follower joins while the prompt is up, then the leader gives up.
	followerDone := make(chan error, 1)
	go func() { followerDone <- v.EnsureUnsealed(context.Background(), "second pane") }()
	waitForJoined(t, v, 2)
	cancelLeader()

	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled leader = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled leader was never released")
	}

	// The follower is still waiting — the prompt survived the leader's exit.
	select {
	case err := <-followerDone:
		t.Fatalf("follower released early with %v while the prompt was still up", err)
	case <-time.After(100 * time.Millisecond):
	}

	// One answer still resolves the follower.
	req.release <- nil
	select {
	case err := <-followerDone:
		if err != nil {
			t.Errorf("follower = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follower was never released after the answer")
	}

	if n := req.callCount(); n != 1 {
		t.Fatalf("raised %d prompts, want exactly 1", n)
	}
}

func TestEnsureUnsealed_WaiterDeadlineReleasesOnlyThatWaiter(t *testing.T) {
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 1),
		release: make(chan error, 1),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	done := make(chan error, 2)
	go func() { done <- v.EnsureUnsealed(context.Background(), "pane one") }()
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never raised")
	}

	// A second waiter with a deadline arrives while the prompt is up.
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() { done <- v.EnsureUnsealed(deadlineCtx, "pane two") }()
	waitForJoined(t, v, 2)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("timed-out waiter = %v, want DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timed-out waiter was never released")
	}

	// The first waiter is still waiting: the deadline released only the
	// waiter that owned it.
	select {
	case err := <-done:
		t.Fatalf("first waiter released early with %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	req.release <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("first waiter = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first waiter was never released")
	}
}

func TestEnsureUnsealed_CloseReleasesOutstandingWaiters(t *testing.T) {
	// The renderer vanished mid-prompt: the ask's lifetime is the vault's,
	// so Close cancels it and every waiter is released with the outcome the
	// transport's RequestUnlock returns on a cancelled context.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 1),
		release: make(chan error, 1),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	done := make(chan error, 2)
	go func() { done <- v.EnsureUnsealed(context.Background(), "pane one") }()
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never raised")
	}
	go func() { done <- v.EnsureUnsealed(context.Background(), "pane two") }()

	v.Close()

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("waiter %d after Close = %v, want context.Canceled", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waiter %d was never released by Close", i)
		}
	}
	// The prompt state is cleared by the prompt's own goroutine after the
	// resolution fans out, so wait on the observable state rather than
	// asserting a moment.
	testwait.WaitFor(t, "the outstanding prompt to clear after Close", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.unlockPending == nil
	})
}

func TestEnsureUnsealed_ResolutionRacingJoinLosesNoCaller(t *testing.T) {
	// The prompt is answered while a new caller is mid-join. The join and
	// the resolution are serialized under the vault lock, so the caller
	// either receives the resolution or observes the cleared prompt and
	// answers from the vault's state — either way it returns; it is never
	// lost and never hangs. The fake resolves instantly, so every
	// interleaving is exercised many times under -race.
	v := sealedVault(t, &fakeUnlockRequester{entered: make(chan struct{}, 8)})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	const callers = 8
	ctx := context.Background()
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { done <- v.EnsureUnsealed(ctx, "operation") }()
	}
	for i := 0; i < callers; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("caller %d = %v, want nil", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d was never released", i)
		}
	}
	// Every caller answered from the resolution or the vault's state; the
	// prompt count is whatever the interleavings produced, but never zero
	// and never more than one per sequential prompt — this test pins the
	// no-lost-caller invariant, the blocking-fake test pins the count.
	if n := req.callCount(); n < 1 {
		t.Fatalf("no prompt was ever raised")
	}
}

func TestEnsureUnsealed_SecondUserActionRaisesFreshPrompt(t *testing.T) {
	// A dismissed unlock is the end of one prompt, not of the need: the next
	// caller — the next user action — raises a fresh one.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 4),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}
	errCancelled := errors.New("unlock cancelled by user")

	ctx := context.Background()
	done := make(chan error, 2)
	go func() { done <- v.EnsureUnsealed(ctx, "first attempt") }()
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first prompt was never raised")
	}
	req.release <- errCancelled
	if err := <-done; !errors.Is(err, errCancelled) {
		t.Fatalf("first attempt = %v, want the cancel error", err)
	}

	go func() { done <- v.EnsureUnsealed(ctx, "second attempt") }()
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second prompt was never raised")
	}
	req.release <- nil
	if err := <-done; err != nil {
		t.Fatalf("second attempt = %v, want nil", err)
	}

	if n := req.callCount(); n != 2 {
		t.Fatalf("two user actions raised %d prompts, want 2", n)
	}
}

func TestEnsureUnsealed_JoinedWaiterNamedBeforeSnapshot(t *testing.T) {
	// When a waiter joins before the prompt's reason is snapshotted, the
	// dialog names it: the snapshot happens under the vault lock, after the
	// join. Here the fake records the reason it received, and the reason is
	// the composition of every sentence known at that instant — each of the
	// three belongs to one of the racing callers, and never to a caller that
	// did not exist.
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 4),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	ctx := context.Background()
	done := make(chan error, 3)
	reasons := []string{"ssh srv-01 needs the vault", "history needs the content key", "ssh srv-02 needs the vault"}
	for _, r := range reasons {
		go func(r string) { done <- v.EnsureUnsealed(ctx, r) }(r)
	}
	// The first prompt is up; the other two callers joined it. Answer it so
	// the goroutines finish and the recorded reason is stable.
	select {
	case <-req.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never raised")
	}
	waitForJoined(t, v, 3)
	req.release <- nil
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatalf("caller %d = %v, want nil", i, err)
		}
	}
	recorded := req.recordedReasons()
	if len(recorded) == 0 {
		t.Fatal("no reason was recorded")
	}
	for _, got := range recorded {
		// The composition form carries a count prefix ("3 operations need
		// the vault: ..."); strip it so the remaining sentences are the
		// callers' own.
		got = countPrefixRe.ReplaceAllString(got, "")
		for _, sentence := range strings.Split(got, "; ") {
			if sentence == "" {
				continue
			}
			known := false
			for _, want := range reasons {
				if sentence == want {
					known = true
					break
				}
			}
			if !known {
				t.Errorf("reason %q names %q, which no caller asked for", got, sentence)
			}
		}
	}
}

// TestEnsureUnsealed_PromptIsRetiredBeforeAnyWaiterIsReleased asserts the
// invariant at the one instant it can be falsified.
//
// The behavioural test above drives two real requests and catches this only
// when the second happens to land inside the window: it survived 400
// iterations on an idle machine and failed on CI, which makes it a report
// about the machine's speed rather than about the code. The invariant itself
// has no window — the moment a waiter is released, the prompt it waited on
// must no longer be the pending one, or the next user action joins something
// that is already over and is answered without anything being raised.
//
// Read through a hook at exactly that moment, so the assertion cannot pass by
// scheduling luck: before the fix the pointer still names this prompt here.
func TestEnsureUnsealed_PromptIsRetiredBeforeAnyWaiterIsReleased(t *testing.T) {
	v := sealedVault(t, &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 4),
	})
	req, ok := v.unlockReq.(*fakeUnlockRequester)
	if !ok {
		t.Fatal("the sealed vault does not hold the fake requester")
	}

	stillPending := make(chan bool, 1)
	v.beforeResolve = func() {
		v.mu.Lock()
		stillPending <- v.unlockPending != nil
		v.mu.Unlock()
	}

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "first attempt") }()
	<-req.entered
	req.release <- errors.New("unlock cancelled by user")
	<-done

	if <-stillPending {
		t.Fatal("a waiter was released while its prompt was still the pending one: the next user action joins a prompt that is over and is answered with nothing raised")
	}
}
