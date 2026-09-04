package wave

// The two calls the epic's DONE WHEN names and nothing provided
// (nocx-dkawo.13): one wait that returns when the first of N settles, and a
// close that ends a worker.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeCloser records what it was asked to end. It does NOT terminalize
// anything, because the product's closer does not either: ending a session
// produces a process exit, and that exit reaches the record by the ordinary
// path.
type fakeCloser struct {
	mu     sync.Mutex
	closed []ParticipantID
	err    error
}

func (c *fakeCloser) Close(_ context.Context, p Participant) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.closed = append(c.closed, p.ID)
	return nil
}

func (c *fakeCloser) seen() []ParticipantID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ParticipantID(nil), c.closed...)
}

// withCloser rebuilds the harness's registrar with a closer wired in. The
// harness's other doubles are untouched.
func withCloser(t *testing.T, h *harness) *fakeCloser {
	t.Helper()
	c := &fakeCloser{}
	h.reg.closer = c
	return c
}

// ── the wait ──────────────────────────────────────────────────────────────

// THE CRITERION: three live workers, ONE wait, and it returns when the first
// settles — with the other two still live.
func TestOneWaitReturnsWhenTheFirstOfThreeSettles(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 3)

	done := make(chan []Participant, 1)
	go func() {
		held, err := h.reg.Wait(ctx, coordSession, testWave)
		if err != nil {
			t.Errorf("wait: %v", err)
		}
		done <- held
	}()

	// Nothing has settled, so the wait is still holding. The first worker
	// finishing is a ROUTINE fact — two others are still running, so it
	// wakes nobody — and the wait must return on it anyway: a wait is a turn
	// already spent, and this is the event it was opened for.
	finish(t, h, workers[0])

	select {
	case held := <-done:
		var terminal, live int
		for _, p := range held {
			if p.State.Terminal() {
				terminal++
			} else {
				live++
			}
		}
		if terminal != 1 || live != 2 {
			t.Fatalf("the wait returned %d settled and %d live, want 1 and 2", terminal, live)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait never returned, so the first settling reached nobody")
	}
	// And the coordinator was not woken for it: a routine completion is not
	// worth a turn, and the wait is a turn the coordinator already had.
	if got := len(h.wake.seen()); got != 0 {
		t.Fatalf("wakes = %d, want 0 for a routine completion", got)
	}
}

// A wait with nothing outstanding does not block: a session whose workers
// have all settled, or which holds none at all, is answered at once. Blocking
// there would hold a turn open for an event that cannot happen.
func TestAWaitWithNothingToWaitForAnswersAtOnce(t *testing.T) {
	ctx := context.Background()

	t.Run("the session holds nothing", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		held := mustWait(t, h, ctx)
		if len(held) != 0 {
			t.Fatalf("held = %v, want nothing", held)
		}
	})

	t.Run("everything it holds has settled", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		p := mustRegister(t, h)
		finish(t, h, p)
		held := mustWait(t, h, ctx)
		if len(held) != 1 || !held[0].State.Terminal() {
			t.Fatalf("held = %+v, want one settled participant", held)
		}
	})
}

// AN EXPIRED WAIT IS AN ANSWER AND NOT A FAILURE. The coordinator asked to be
// told promptly and was not; what it holds is still true, and an error would
// only make it read the same thing again to find out.
func TestAWaitWhoseDeadlinePassesAnswersWithWhatIsStillTrue(t *testing.T) {
	h := newHarnessBound(t, 5)
	p := mustRegister(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	held, err := h.reg.Wait(ctx, coordSession, testWave)
	if err != nil {
		t.Fatalf("an expired wait failed instead of answering: %v", err)
	}
	if len(held) != 1 || held[0].ID != p.ID || held[0].State.Terminal() {
		t.Fatalf("held = %+v, want the still-live worker", held)
	}
}

// A wait is a FETCH (D7: a report, a wait or an explicit inbox check returns
// pending mail), so what it hands over is dispatched and a later holdings
// does not owe it again.
func TestAWaitDispatchesTheFactsItHandsOver(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	p := mustRegister(t, h)

	if _, err := h.reg.Declared(ctx, p.ID, testLiveness(), Declaration{OK: false}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 1 {
		t.Fatalf("undispatched before the wait = %d, want 1", got)
	}
	if _, err := h.reg.Wait(ctx, coordSession, testWave); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := len(h.reg.Undispatched()); got != 0 {
		t.Fatalf("undispatched after the wait = %d, want 0", got)
	}
}

// A FACT ADMITTED BETWEEN THE WAIT'S READ AND ITS SELECT MUST NOT BE MISSED.
// The channel is taken FIRST, so an entry that raced the read has already
// closed the one the waiter is holding.
//
// The window is a few instructions wide, so racing two goroutines and hoping
// proves nothing: the fact lands before the read or after the select almost
// every time, and a wait that took its channel too late would pass. The store
// puts the fact exactly in the window instead, which makes the ordering the
// test's rather than the scheduler's.
func TestAWaitDoesNotMissAFactThatRacedItsRead(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	workers := fanout(t, h, 2)

	// Inside HeldBy, after the wait has taken its channel and before it can
	// select on it.
	h.store.duringHeldBy = func() {
		if _, err := h.reg.Exited(ctx, workers[0].ID, testLiveness(), Exit{Cause: "exited"}); err != nil {
			t.Errorf("exit: %v", err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := h.reg.Wait(ctx, coordSession, testWave); err != nil {
			t.Errorf("wait: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the wait missed a fact admitted between its read and its select")
	}
}

func mustWait(t *testing.T, h *harness, ctx context.Context) []Participant {
	t.Helper()
	done := make(chan []Participant, 1)
	go func() {
		held, err := h.reg.Wait(ctx, coordSession, testWave)
		if err != nil {
			t.Errorf("wait: %v", err)
		}
		done <- held
	}()
	select {
	case held := <-done:
		return held
	case <-time.After(5 * time.Second):
		t.Fatal("the wait blocked with nothing to wait for")
		return nil
	}
}

// ── the close ─────────────────────────────────────────────────────────────

// A close ends the worker and WRITES NO STATE. The exit that follows reaches
// the record by the ordinary path, which is what keeps one author of a
// participant's state.
func TestAClosedWorkerIsEndedAndNotTerminalizedDirectly(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	closer := withCloser(t, h)
	p := mustRegister(t, h)

	if err := h.reg.Close(ctx, coordSession, p.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := closer.seen(); len(got) != 1 || got[0] != p.ID {
		t.Fatalf("closed %v, want exactly %q", got, p.ID)
	}
	stored, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("the record lost the participant")
	}
	if stored.State.Terminal() {
		t.Fatalf("close wrote %q; the exit is what decides a participant's state", stored.State)
	}
}

// THE FIRST OPERATION THAT READS A DELEGATION. Membership makes a worker
// addressable and delegation makes it controllable, and until this nothing
// had ever consulted EffectClose.
func TestCloseIsRefusedWithoutADelegationThatCarriesIt(t *testing.T) {
	ctx := context.Background()

	t.Run("another session holds it", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		if err := h.reg.Close(ctx, "sess-somebody-else", p.ID); !errors.Is(err, ErrNotDelegated) {
			t.Fatalf("close by a stranger = %v, want ErrNotDelegated", err)
		}
		if got := closer.seen(); len(got) != 0 {
			t.Fatalf("a refused close ended %v anyway", got)
		}
	})

	t.Run("the bundle does not carry close", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		if err := h.store.PutDelegation(ctx, Delegation{
			ControllerSession: coordSession, Participant: p.ID,
			Effects: []Effect{EffectObserve}, State: DelegationActive,
		}); err != nil {
			t.Fatalf("put delegation: %v", err)
		}
		if err := h.reg.Close(ctx, coordSession, p.ID); !errors.Is(err, ErrNotDelegated) {
			t.Fatalf("close without the effect = %v, want ErrNotDelegated", err)
		}
		if got := closer.seen(); len(got) != 0 {
			t.Fatalf("a refused close ended %v anyway", got)
		}
	})

	t.Run("the delegation is revoked", func(t *testing.T) {
		h := newHarnessBound(t, 5)
		closer := withCloser(t, h)
		p := mustRegister(t, h)
		if err := h.store.PutDelegation(ctx, Delegation{
			ControllerSession: coordSession, Participant: p.ID,
			Effects: DefaultBundle(), State: DelegationRevoked,
		}); err != nil {
			t.Fatalf("put delegation: %v", err)
		}
		if err := h.reg.Close(ctx, coordSession, p.ID); !errors.Is(err, ErrNotDelegated) {
			t.Fatalf("close under a revoked delegation = %v, want ErrNotDelegated", err)
		}
		if got := closer.seen(); len(got) != 0 {
			t.Fatalf("a refused close ended %v anyway", got)
		}
	})
}

// A HUMAN TAKEOVER SUSPENDS SEND-INPUT AND NOT CLOSE. DelegationState.Permits
// has said so since the record was built; this is where it stops being a
// comment. Severing a coordinator from its own worker because a person helped
// it past a prompt is the cost that shape was measured to have.
func TestAHumanTakeoverDoesNotStopACoordinatorClosingItsOwnWorker(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	closer := withCloser(t, h)
	p := mustRegister(t, h)

	if err := h.store.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession, Participant: p.ID,
		Effects: DefaultBundle(), State: DelegationInputSuspended,
	}); err != nil {
		t.Fatalf("put delegation: %v", err)
	}
	if err := h.reg.Close(ctx, coordSession, p.ID); err != nil {
		t.Fatalf("close under a takeover: %v", err)
	}
	if got := closer.seen(); len(got) != 1 {
		t.Fatalf("closed %v, want the worker ended", got)
	}
}

// Closing something already finished is not an error: a coordinator tidying
// up should not have to have raced the record to be allowed to.
func TestClosingAFinishedWorkerIsNotAnError(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	closer := withCloser(t, h)
	p := mustRegister(t, h)
	finish(t, h, p)

	if err := h.reg.Close(ctx, coordSession, p.ID); err != nil {
		t.Fatalf("close of a finished worker: %v", err)
	}
	if got := closer.seen(); len(got) != 0 {
		t.Fatalf("a finished worker was ended again: %v", got)
	}
}

// A backend with nothing to end refuses rather than reporting a worker ended
// that is still running.
func TestABackendWithNoCloserRefuses(t *testing.T) {
	h := newHarnessBound(t, 5)
	p := mustRegister(t, h)
	if err := h.reg.Close(context.Background(), coordSession, p.ID); err == nil {
		t.Fatalf("a close with nothing wired to end anything was accepted")
	}
}
