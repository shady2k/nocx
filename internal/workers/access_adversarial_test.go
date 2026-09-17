package workers

// Independent adversarial tests for design §7's reachability and revocation
// promises (spec 2026-09-14-the-session-surface-design.md §7.1–7.2), written
// by a worker who did not implement Task 7 (AGENTS.md testing rule 4). These
// exercise the same seams access.go and access_test.go already use
// (registerUnder, newHarnessBound, h.store directly) but ask different
// questions of them: a chain resolved BEFORE a race must be provably stale
// afterward (not merely "the walk fails again if retried"), a delegation
// chain corrupted in its middle hop must fail exactly where the walk cannot
// continue, and a racing spawn's own delegation must show non-Active state
// at the store level, not only "the top controller can no longer resolve
// it".
//
// bead: nocx-6q1uh.13

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── item 5: neighbours, plain shells, and a chain broken in the middle ─────

// A sibling controller's worker, a session no participant has ever run in,
// and a chain whose middle hop names a controller session that is nobody's
// all fail the identical way — paired against the ordinary success (a real
// grandchild) so the failure set is measured against something that does
// resolve (AGENTS.md testing rule 1).
func TestNeighboursPlainShellsAndABrokenChainAreAllUnreachable(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()

	w1 := registerUnder(t, h, "sess-C", "sess-w1")
	registerUnder(t, h, "sess-w1", "sess-w2")

	// Ordinary success: the genuine grandchild resolves before anything
	// below corrupts state.
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w2", EffectObserve); err != nil {
		t.Fatalf("resolve the genuine grandchild: %v", err)
	}

	// A sibling controller's worker: a chain that reaches SOME bound
	// controller, just never this one.
	registerUnder(t, h, "sess-neighbour", "sess-neighbour-w")
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-neighbour-w", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve a sibling controller's worker: err = %v, want ErrNotReachable", err)
	}

	// A session no participant has ever run in: never a distinct "not a
	// participant" answer (access.go's own contract), so it must fail the
	// same way as every other unreachable case.
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-never-a-participant", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve a plain shell: err = %v, want ErrNotReachable", err)
	}

	// A delegation chain broken in the middle: w2's own delegation is
	// intact and permits the effect, but the hop above it (w1's own
	// delegation) is corrupted to name a controller session that is
	// neither sess-C nor any live participant. Resolve must fail exactly
	// where the walk cannot continue, never report the dangling prefix as
	// reachable.
	del, err := h.store.Delegation(ctx, w1.ID)
	if err != nil {
		t.Fatalf("read w1's delegation: %v", err)
	}
	del.ControllerSession = "sess-does-not-exist-anywhere"
	if err := h.store.PutDelegation(ctx, del); err != nil {
		t.Fatalf("corrupt w1's delegation: %v", err)
	}
	if reach, err := h.reg.Resolve(ctx, "sess-C", "sess-w2", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve through a chain broken in the middle: reach = %+v, err = %v, want ErrNotReachable", reach, err)
	}
}

// ── item 2: a grandchild's already-resolved intent racing revocation ──────

// A chain a grandchild's intent already held — reachable at the instant it
// was read — must be detectably stale the moment a racing revocation of its
// grandparent RETURNS, not merely "eventually" or "if re-resolved": StillHolds
// is checked here strictly after Revoke has returned, for both a chain
// resolved deterministically before the race and one resolved concurrently
// with it, over 500 iterations (design §7.2).
//
// WHICH SIDE OF THE RACE A TRIAL PRODUCES IS PINNED, NOT HOPED FOR. Raced as
// two goroutines started back to back, this trial was not reliable evidence of
// anything: under the load this brief names it produced the revoke-first
// ordering on every trial, the racing resolve was answered from the revoked
// subtree every time, and the half of this test that asserts a chain resolved
// DURING the race goes stale asserted nothing — measured 2026-09-17, 50 runs of
// the trial under load on the unmodified branch, 50 failures, every one of them
// at the count this test used to carry and none of them at the invariant
// (nocx-xn63t.4.7). resolveAgainstRevoke below starts the revoke only once the
// resolve is inside its critical section, so the resolve is the side that
// commits first on every trial; the check immediately after it is where a trial
// that did not get that ordering says so, rather than asserting nothing.
func TestAChainAGrandchildsIntentAlreadyHeldGoesStaleTheInstantRevokeReturns(t *testing.T) {
	const iterations = 500
	ctx := context.Background()
	for i := range iterations {
		h := newHarnessBound(t, 1000)
		top := fmt.Sprintf("sess-C-%d", i)
		w1Session := fmt.Sprintf("sess-w1-%d", i)
		w2Session := fmt.Sprintf("sess-w2-%d", i)
		w1 := registerUnder(t, h, top, w1Session)
		registerUnder(t, h, w1Session, w2Session)

		// The floor this test is measured against: a chain resolved
		// strictly before the race begins must ALWAYS go stale once
		// Revoke returns — deterministic, not a race.
		before, err := h.reg.Resolve(ctx, top, w2Session, EffectObserve)
		if err != nil {
			t.Fatalf("iteration %d: resolve before the race: %v", i, err)
		}

		raceChain, raceErr := resolveAgainstRevoke(t, h, top, w2Session, w1.ID)

		if h.reg.StillHolds(ctx, before.Chain) {
			t.Fatalf("iteration %d: a chain resolved strictly before the race still holds once Revoke returned", i)
		}
		if raceErr != nil {
			// The premise, not a claim about the product: the pin is what
			// makes the racing resolve the side that commits first, and a
			// trial that did not get that ordering has nothing to assert and
			// says so here (nocx-xn63t.4.7's failure was this condition,
			// counted rather than named).
			t.Fatalf("iteration %d: the racing resolve was pinned to commit before the revoke and answered %v — this trial asserted nothing", i, raceErr)
		}
		if h.reg.StillHolds(ctx, raceChain) {
			t.Fatalf("iteration %d: a chain resolved DURING the race still holds once Revoke returned", i)
		}
	}
}

// resolveAgainstRevoke runs one trial of "a chain resolved while a revocation
// of its grandparent is in flight", with the resolution pinned to commit
// first, and answers what that resolve saw.
//
// The pin is memStore.duringResolve, which runs once inside the store call
// resolveLocked makes while the Registrar holds storeMu across it. The revoke
// is started from there, so it cannot take that mutex before the resolve has
// read its chain, and the resolve is released only after the revoke is
// contending for it — which keeps the two genuinely overlapped rather than
// merely ordered, and is the one ordering this trial could not previously
// produce. §7.2's mutex is what makes it legal: the revoke cannot move the
// generations out from under the read it is waiting on.
func resolveAgainstRevoke(t *testing.T, h *harness, controller, session string, root ParticipantID) (Chain, error) {
	t.Helper()
	ctx := context.Background()
	resolveInLock := make(chan struct{})
	release := make(chan struct{})
	h.store.duringResolve = func() {
		close(resolveInLock)
		<-release
	}

	var (
		reach  Reach
		resErr error
		revErr error
		wg     sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		reach, resErr = h.reg.Resolve(ctx, controller, session, EffectObserve)
	}()
	select {
	case <-resolveInLock:
	case <-time.After(10 * time.Second):
		t.Fatal("the racing resolve never reached its critical section — the ordering this trial pins was not produced")
	}
	go func() {
		defer wg.Done()
		_, revErr = h.reg.Revoke(ctx, root, "racing intent")
	}()
	close(release)
	wg.Wait()
	if revErr != nil {
		t.Fatalf("revoke: %v", revErr)
	}
	return reach.Chain, resErr
}

// ── item 3: a grandchild spawn racing the grandparent's revocation ────────

// No spawn that COMPLETES after Revoke has returned is ever reachable from
// the top controller — distinct from the item above, which is about a chain
// already resolved before the race started. Here the race is between the
// spawn's own Register call and the revocation of the participant it is
// registering under, so the property under test is that Register cannot
// commit a delegation whose parent the store mutex has already invalidated.
// Every trial that observed the spawn finishing after the revoke also has
// its own delegation checked directly at the store level, not merely through
// Resolve — a spawn "winning" the mutex race would show as an Active
// delegation at the store even if Resolve happened to still refuse it for
// an unrelated reason.
func TestNoGrandchildSpawnCompletingAfterRevokeReturnedIsEverReachable(t *testing.T) {
	const iterations = 500
	ctx := context.Background()
	observedAfter := 0
	for i := 0; i < iterations; i++ {
		h := newHarnessBound(t, 1000)
		top := fmt.Sprintf("sess-C-%d", i)
		w1Session := fmt.Sprintf("sess-w1-%d", i)
		w2Session := fmt.Sprintf("sess-w2-%d", i)
		w1 := registerUnder(t, h, top, w1Session)

		var revokeReturned atomic.Bool
		var spawnAfterRevoke atomic.Bool
		var spawnErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := h.reg.Revoke(ctx, w1.ID, "racing spawn (independent trial)"); err != nil {
				t.Errorf("iteration %d: revoke: %v", i, err)
			}
			revokeReturned.Store(true)
		}()
		go func() {
			defer wg.Done()
			h.spawn.live = Liveness{BackendInstance: "backend-A", SessionID: w2Session, Epoch: 1, Lane: "lane-1", Attempt: 1}
			h.enrol.live = h.spawn.live
			_, err := h.reg.Register(ctx, RegisterRequest{
				CoordinatorSession: w1Session,
				Role:               RoleWorker,
				Task:               "read AGENTS.md and report",
				Command:            "agent",
				Environment:        "env-local",
			})
			spawnErr = err
			spawnAfterRevoke.Store(revokeReturned.Load())
		}()
		wg.Wait()

		if spawnErr != nil {
			t.Fatalf("iteration %d: spawn under w1 failed: %v", i, spawnErr)
		}
		if !spawnAfterRevoke.Load() {
			// This trial's spawn completed concurrently with, or before,
			// the revoke — §7.2 makes no promise for that ordering.
			continue
		}
		observedAfter++

		if reach, err := h.reg.Resolve(ctx, top, w2Session, EffectObserve); err == nil {
			t.Fatalf("iteration %d: grandchild reachable from the top controller after its spawn completed post-revoke: %+v", i, reach)
		} else if !errors.Is(err, ErrNotReachable) {
			t.Fatalf("iteration %d: resolve error = %v, want ErrNotReachable", i, err)
		}

		w1Del, err := h.store.Delegation(ctx, w1.ID)
		if err != nil {
			t.Fatalf("iteration %d: read w1's delegation: %v", i, err)
		}
		if w1Del.State == DelegationActive {
			t.Fatalf("iteration %d: w1's own delegation still Active after Revoke returned — the spawn's parent was not actually invalidated first", i)
		}
	}
	if observedAfter == 0 {
		t.Fatal("no trial ever observed the spawn completing after revoke returned — the test asserted nothing")
	}
}
