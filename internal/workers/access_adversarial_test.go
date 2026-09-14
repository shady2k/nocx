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
func TestAChainAGrandchildsIntentAlreadyHeldGoesStaleTheInstantRevokeReturns(t *testing.T) {
	const iterations = 500
	ctx := context.Background()
	checkedRacing := 0
	for i := 0; i < iterations; i++ {
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

		var raceErr error
		var raceChain Chain
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			reach, err := h.reg.Resolve(ctx, top, w2Session, EffectObserve)
			raceErr = err
			raceChain = reach.Chain
		}()
		go func() {
			defer wg.Done()
			if _, err := h.reg.Revoke(ctx, w1.ID, "racing intent"); err != nil {
				t.Errorf("iteration %d: revoke: %v", i, err)
			}
		}()
		wg.Wait()

		if h.reg.StillHolds(ctx, before.Chain) {
			t.Fatalf("iteration %d: a chain resolved strictly before the race still holds once Revoke returned", i)
		}
		if raceErr == nil {
			checkedRacing++
			if h.reg.StillHolds(ctx, raceChain) {
				t.Fatalf("iteration %d: a chain resolved DURING the race still holds once Revoke returned", i)
			}
		} else if !errors.Is(raceErr, ErrNotReachable) {
			t.Fatalf("iteration %d: racing resolve error = %v, want nil or ErrNotReachable", i, raceErr)
		}
	}
	if checkedRacing == 0 {
		t.Fatal("no trial ever had the racing resolve succeed before the revoke committed — the racing half asserted nothing")
	}
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
