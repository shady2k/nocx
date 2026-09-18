package workers

// The close that ends a worker (nocx-dkawo.13).
//
// WHAT THIS FILE NO LONGER HOLDS is the wait. `workers.wait`, `Registrar.Wait`
// and the channel the fact set used to hand out went with the declaration
// (ADR-0070, design §7): an interactive worker never exits, so a wait on its
// exit held to its deadline, and the coordinator that could have ended the
// worker was the one blocked in the wait. The wake reads the MAILBOX now
// (wake.go), and the fact set's one remaining event is the coordinator's own
// fetch — asserted in backstop_test.go and routing_test.go.

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeCloser records what it was asked to end. It writes no state, because the
// product's closer does not either: ending a session produces a process exit —
// which reaches the record by the ordinary path — and the `closed` the
// coordinator's own call writes is written by the REGISTRAR, after this
// returned (Registrar.Close).
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

// ── the close ─────────────────────────────────────────────────────────────

// A close ends the worker and the record says WHY: the state is the
// coordinator's own, and it is written only after the closer returned — a
// close that failed ended nothing, and the assertion for that is in
// registrar_test.go beside the rest of the state vocabulary.
func TestAClosedWorkerReadsClosed(t *testing.T) {
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
	if stored.State != StateClosed {
		t.Fatalf("state = %q, want %q", stored.State, StateClosed)
	}
	if !stored.State.Terminal() {
		t.Fatalf("%q is not terminal, so the participant keeps holding a reservation", stored.State)
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
		// ErrNotHeld and not ErrNotDelegated: ownership is its own fact, and
		// the wire says a different sentence for it (nocx-e5e8q).
		if err := h.reg.Close(ctx, "sess-somebody-else", p.ID); !errors.Is(err, ErrNotHeld) {
			t.Fatalf("close by a stranger = %v, want ErrNotHeld", err)
		}
		if errors.Is(h.reg.Close(ctx, "sess-somebody-else", p.ID), ErrNotDelegated) {
			t.Fatal("a stranger's close was reported as a delegation state, which is the caller's OWN participant's story")
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
//
// AND IT STILL REACHES THE CLOSER (nocx-xn63t.4.6), which is the half this
// test used to assert the opposite of. For a participant that has already
// ended, the thing left to end is the PLACE it occupied, and the closer is the
// only seam that can give that back: in production it is internal/app's
// workerCloser, which asks the registry before ending a session (so a process
// that is already gone is not a failure) and whose second half is the tab
// write. A close that returned here without calling it is exactly what left a
// finished worker's tab standing on the strip with nothing behind it — the
// defect the bead was filed from: the coordinator's workers.close answered ok
// and the tab stayed.
func TestClosingAFinishedWorkerIsNotAnError(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	closer := withCloser(t, h)
	p := mustRegister(t, h)
	finish(t, h, p)

	if err := h.reg.Close(ctx, coordSession, p.ID); err != nil {
		t.Fatalf("close of a finished worker: %v", err)
	}
	if got := closer.seen(); len(got) != 1 || got[0] != p.ID {
		t.Fatalf("the closer saw %v, want exactly [%s]: a finished worker's place is released by the closer or by nobody",
			got, p.ID)
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

// A COORDINATOR CAN ALWAYS TIDY UP ITS OWN ENDED WORKER (nocx-e5e8q).
//
// Close's own next step says so — "already finished. Not an error: a
// coordinator tidying up should…" — and the delegation-state gate above it
// refused first, so that branch could not be reached for the case it exists
// for. Seen live: a worker in state `interrupted` was listed by
// workers.holdings as the caller's own and could never be closed, so it stayed
// in holdings for ever.
//
// The state gates a LIVE participant. Ending one that is already terminal
// needs no live delegation, because there is nothing left to act on.
func TestAnEndedParticipantCanBeClosedUnderADelegationThatIsNoLongerActive(t *testing.T) {
	ctx := context.Background()
	h := newHarnessBound(t, 5)
	withCloser(t, h)
	p := mustRegister(t, h)

	// The state a compensation writes when a worker's start went wrong — the
	// exact state the live participant was in when it could not be closed.
	if err := h.store.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	if err := h.store.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession, Participant: p.ID,
		Effects: DefaultBundle(), State: DelegationRevoked,
	}); err != nil {
		t.Fatalf("put delegation: %v", err)
	}

	if err := h.reg.Close(ctx, coordSession, p.ID); err != nil {
		t.Fatalf("a coordinator could not tidy up its own ended worker: %v", err)
	}
}

// And the gate still holds for one that is LIVE: a revoked delegation is a
// coordinator that may no longer act, and killing a running worker is acting.
func TestALiveParticipantIsStillRefusedUnderARevokedDelegation(t *testing.T) {
	ctx := context.Background()
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
		t.Fatalf("close of a LIVE participant under a revoked delegation = %v, want ErrNotDelegated", err)
	}
	if got := closer.seen(); len(got) != 0 {
		t.Fatalf("a refused close ended %v anyway", got)
	}
}
