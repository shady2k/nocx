package workers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// registerUnder registers one participant whose controller is
// controllerSession and whose own pane session is workerSession, on the
// harness's shared store. It mutates h.spawn.live and h.enrol.live, which is
// safe because Register calls both synchronously from the calling
// goroutine — nothing else touches them concurrently with a call made this
// way.
func registerUnder(t *testing.T, h *harness, controllerSession, workerSession string) Participant {
	t.Helper()
	live := Liveness{
		BackendInstance: "backend-A",
		SessionID:       workerSession,
		Epoch:           1,
		Lane:            "lane-1",
		Attempt:         1,
	}
	h.spawn.live = live
	h.enrol.live = live
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		CoordinatorSession: controllerSession,
		Role:               RoleWorker,
		Task:               "read AGENTS.md and report",
		Command:            "agent",
		Environment:        "env-local",
	})
	if err != nil {
		t.Fatalf("register under %q: %v", controllerSession, err)
	}
	return reg.Participant
}

// A grandchild is reachable through two links, a sibling's worker is not,
// and neither is a plain shell — sessions no participant ever ran in are
// never participants, so they can never be reachable (spec §7.1).
func TestAGrandchildIsReachableAndANeighbourIsNot(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()

	w1 := registerUnder(t, h, "sess-C", "sess-w1")
	w2 := registerUnder(t, h, "sess-w1", "sess-w2")
	registerUnder(t, h, "sess-C2", "sess-sib")

	reach, err := h.reg.Resolve(ctx, "sess-C", "sess-w2", EffectObserve)
	if err != nil {
		t.Fatalf("resolve grandchild: %v", err)
	}
	if reach.Participant.ID != w2.ID || reach.SessionID != "sess-w2" {
		t.Fatalf("reach = %+v, want the grandchild's own participant and session", reach)
	}
	if len(reach.Chain) != 2 {
		t.Fatalf("chain = %+v, want two links (w2 -> w1 -> C)", reach.Chain)
	}
	if reach.Chain[0].Participant != w2.ID || reach.Chain[1].Participant != w1.ID {
		t.Fatalf("chain = %+v, want [w2, w1] in that order", reach.Chain)
	}

	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-sib", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve a sibling controller's worker: err = %v, want ErrNotReachable", err)
	}
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-a-plain-shell", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve a plain shell: err = %v, want ErrNotReachable", err)
	}
	// The controller may not resolve itself as though it were its own
	// descendant.
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-C", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve the controller's own session: err = %v, want ErrNotReachable", err)
	}
}

// The chain vector StillHolds is handed must go stale the instant the
// generation it recorded moves, and stay good otherwise.
func TestStillHoldsGoesStaleExactlyWhenTheGenerationMoves(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()
	w1 := registerUnder(t, h, "sess-C", "sess-w1")

	reach, err := h.reg.Resolve(ctx, "sess-C", "sess-w1", EffectObserve)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !h.reg.StillHolds(ctx, reach.Chain) {
		t.Fatal("a freshly resolved chain did not hold")
	}
	if _, err := h.reg.Revoke(ctx, w1.ID, "test"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if h.reg.StillHolds(ctx, reach.Chain) {
		t.Fatal("a chain held after the generation it recorded was bumped")
	}
}

// Revocation cannot be outrun: every spawn under a participant that COMPLETES
// AFTER Revoke has returned must be unreachable from the top controller,
// across many concurrent trials (§7.2, TestARevocationRacingAGrandchildSpawnSerialises).
func TestARevocationRacingAGrandchildSpawnSerialises(t *testing.T) {
	const iterations = 500
	ctx := context.Background()
	checked := 0
	for i := 0; i < iterations; i++ {
		h := newHarnessBound(t, 1000)
		top := fmt.Sprintf("sess-C-%d", i)
		w1Session := fmt.Sprintf("sess-w1-%d", i)
		w2Session := fmt.Sprintf("sess-w2-%d", i)
		w1 := registerUnder(t, h, top, w1Session)

		var revokeDone atomic.Bool
		var spawnSawRevokeDone atomic.Bool
		var spawnErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := h.reg.Revoke(ctx, w1.ID, "racing spawn"); err != nil {
				t.Errorf("revoke: %v", err)
			}
			revokeDone.Store(true)
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
			spawnSawRevokeDone.Store(revokeDone.Load())
		}()
		wg.Wait()

		if spawnErr != nil {
			t.Fatalf("iteration %d: spawn under w1 failed: %v", i, spawnErr)
		}
		if !spawnSawRevokeDone.Load() {
			// This trial's spawn completed concurrently with, or before,
			// the revoke — not the ordering this test is about. §7.2 makes
			// no promise for that case (a chain resolved mid-revocation may
			// legitimately see either side of it).
			continue
		}
		checked++
		if reach, err := h.reg.Resolve(ctx, top, w2Session, EffectObserve); err == nil {
			t.Fatalf("iteration %d: grandchild reachable after a spawn that completed after revoke returned: %+v", i, reach)
		} else if !errors.Is(err, ErrNotReachable) {
			t.Fatalf("iteration %d: resolve error = %v, want ErrNotReachable", i, err)
		}
	}
	if checked == 0 {
		t.Fatal("no trial ever observed the spawn completing after revoke returned — the test asserted nothing")
	}
}

// RevokeController ends every delegation a session directly holds, and
// (through the same subtree walk) whatever those participants themselves
// control — the shape worker_auth.retire needs when an admission ends.
func TestRevokeControllerRevokesEveryDelegationThatSessionHolds(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()
	registerUnder(t, h, "sess-C", "sess-w1")
	registerUnder(t, h, "sess-w1", "sess-w2")

	sessions, err := h.reg.RevokeController(ctx, "sess-C", "admission retired")
	if err != nil {
		t.Fatalf("revoke controller: %v", err)
	}
	want := map[string]bool{"sess-w1": true, "sess-w2": true}
	got := map[string]bool{}
	for _, s := range sessions {
		got[s] = true
	}
	if len(got) != len(want) {
		t.Fatalf("affected sessions = %v, want %v", sessions, want)
	}
	for s := range want {
		if !got[s] {
			t.Fatalf("affected sessions = %v, missing %q", sessions, s)
		}
	}
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w1", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve w1 after RevokeController: err = %v, want ErrNotReachable", err)
	}
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w2", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve w2 after RevokeController: err = %v, want ErrNotReachable", err)
	}
}

// Close is one of §7.2's revocation triggers: ending a participant revokes
// what it itself controls, immediately, rather than waiting for the process
// exit this close causes to reach the record by the ordinary (admit-driven)
// path.
func TestClosingAParticipantRevokesWhatItControls(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()
	w1 := registerUnder(t, h, "sess-C", "sess-w1")
	registerUnder(t, h, "sess-w1", "sess-w2")
	closer := &fakeCloser{}
	h.reg.closer = closer

	if _, err := h.reg.Close(ctx, "sess-C", w1.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w2", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve w2 after closing w1: err = %v, want ErrNotReachable", err)
	}
	if len(closer.seen()) != 1 || closer.seen()[0] != w1.ID {
		t.Fatalf("closer saw %v, want exactly [%q]", closer.seen(), w1.ID)
	}
}

// A participant reaching a terminal state through the ordinary exit path
// (admit) has its delegation revoked too — the "participant terminalized"
// trigger, distinct from an explicit Close.
func TestATerminalizedParticipantsDelegationIsRevoked(t *testing.T) {
	h := newHarnessBound(t, 10)
	ctx := context.Background()
	w1 := registerUnder(t, h, "sess-C", "sess-w1")
	live := Liveness{BackendInstance: "backend-A", SessionID: "sess-w1", Epoch: 1, Lane: "lane-1", Attempt: 1}

	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w1", EffectObserve); err != nil {
		t.Fatalf("resolve before the process fact lands: %v", err)
	}
	after, err := h.reg.Exited(ctx, w1.ID, live, Exit{Cause: "exited"})
	if err != nil {
		t.Fatalf("exited: %v", err)
	}
	if !after.State.Terminal() {
		t.Fatalf("participant state = %s, want terminal", after.State)
	}
	if _, err := h.reg.Resolve(ctx, "sess-C", "sess-w1", EffectObserve); !errors.Is(err, ErrNotReachable) {
		t.Fatalf("resolve after termination: err = %v, want ErrNotReachable", err)
	}
}

// A failed registration that had already reached step 5 (a delegation
// exists) must have that delegation revoked when compensate terminalizes
// the participant — otherwise a coordinator's later retry under the same
// controller session would find a live-looking delegation over a
// participant that never came up.
func TestACompensatedRegistrationRevokesTheDelegationItCreated(t *testing.T) {
	h := newHarnessBound(t, 10)
	h.sup.failOn = 1 // Attach fails, AFTER PutDelegation (step 5) succeeded.
	ctx := context.Background()

	if _, err := h.register(ctx); err == nil {
		t.Fatal("a registration whose supervision attach failed reported success")
	}
	// The participant this created is p-1 (newHarnessBound's newID scheme);
	// its delegation must already be gone from Active.
	del, err := h.store.Delegation(ctx, ParticipantID("p-1"))
	if err != nil {
		t.Fatalf("delegation: %v", err)
	}
	if del.State == DelegationActive {
		t.Fatalf("delegation state = %s, want revoked after the compensated registration", del.State)
	}
}
