package lifecycle

import (
	"errors"
	"testing"
)

// TestModelScenario is the acceptance criterion of bead nocx-u7uh.2: the pure
// model test that must run before any transport exists, so a transport
// implementation can never choose the domain model by accident. It runs the
// brief's twelve steps verbatim with a fake in-memory transport and no I/O.
func TestModelScenario(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if bindErr := k.BindTransport("T", tp); bindErr != nil {
		t.Fatal(bindErr)
	}
	const L = LaneID("L")

	// 1. Establish transport T. (Bound above.)

	// 2. Authenticate domain A on lane L.
	hA := establish(t, k, "T", tp, L, nil)
	st := mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// 3. Start and complete attempt A1.
	a1, err := k.SubmitAttempt(hA.Domain, "echo hi", "/home/dev", "local", "")
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	if a1.Origin != OriginApp || a1.Started {
		t.Fatalf("app attempt must be unstarted app-origin: %+v", a1)
	}
	st = mustState(t, k, L)
	assertState(t, st, LifecycleRunning, hA.Domain, a1.ID, []DomainID{hA.Domain})
	// The authenticated Start attaches: same attempt id, app text kept.
	mustIngest(t, k, "T", env(L, hA, 2, startEvt(&a1.ID, "rm -rf /")))
	if got, ok := k.Attempt(a1.ID); !ok || got.Command != "echo hi" {
		t.Fatalf("attach must keep app-owned text, got %+v", got)
	}
	mustIngest(t, k, "T", env(L, hA, 3, completeEvt(a1.ID, 0, fence(0x11))))
	gotA1, ok := k.Attempt(a1.ID)
	if !ok || gotA1.State != AttemptCompleted || gotA1.ExitCode == nil || *gotA1.ExitCode != 0 {
		t.Fatalf("A1 must be completed with exit 0, got %+v", gotA1)
	}
	if gotA1.Fence != fence(0x11) {
		t.Fatalf("fence not recorded: %x", gotA1.Fence)
	}
	mustIngest(t, k, "T", env(L, hA, 4, promptReadyEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// 4. Suspend A and authenticate child domain B over the SAME transport.
	mustIngest(t, k, "T", env(L, hA, 5, suspendEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecycleNative, "", "", []DomainID{hA.Domain}) // A not active
	hB, err := k.RequestDomain(L, &hA.Domain, "T")
	if err != nil {
		t.Fatalf("RequestDomain child: %v", err)
	}
	if hB.Epoch <= hA.Epoch || hB.Capability == hA.Capability {
		t.Fatalf("child must get a fresh epoch and capability")
	}
	mustIngest(t, k, "T", env(L, hB, 1, helloEvt("bash")))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hB.Domain, "", []DomainID{hA.Domain, hB.Domain})

	// 5. Reject an A completion while B is active. Nothing moves.
	before := mustState(t, k, L)
	if _, ingestErr := k.Ingest("T", env(L, hA, 6, completeEvt(a1.ID, 0, fence(0x22)))); !errors.Is(ingestErr, ErrDomainInactive) {
		t.Fatalf("completion for suspended A must be rejected, got %v", ingestErr)
	}
	if after := mustState(t, k, L); !statesEqual(before, after) {
		t.Fatalf("rejected completion mutated the lane: %+v -> %+v", before, after)
	}
	if got, _ := k.Attempt(a1.ID); got.State != AttemptCompleted || *got.ExitCode != 0 {
		t.Fatalf("A1 status must persist exactly once, got %+v", got)
	}

	// 6. Start and complete attempt B1 (shell-originated, no app attempt).
	mustIngest(t, k, "T", env(L, hB, 2, startEvt(nil, "ls")))
	b1, ok := k.OpenAttempt(hB.Domain)
	if !ok || b1.Origin != OriginShell || !b1.Started {
		t.Fatalf("shell-originated attempt expected, got %+v", b1)
	}
	st = mustState(t, k, L)
	assertState(t, st, LifecycleRunning, hB.Domain, b1.ID, []DomainID{hA.Domain, hB.Domain})
	mustIngest(t, k, "T", env(L, hB, 3, completeEvt(b1.ID, 2, fence(0x33))))
	mustIngest(t, k, "T", env(L, hB, 4, promptReadyEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hB.Domain, "", []DomainID{hA.Domain, hB.Domain})

	// 7. Close B. A must NOT be restored by the close itself.
	mustIngest(t, k, "T", env(L, hB, 5, closeEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecycleNative, "", "", []DomainID{hA.Domain})
	if dA, _ := k.Domain(hA.Domain); dA.State != DomainSuspended {
		t.Fatalf("A must stay suspended after B closes, got %v", dA.State)
	}

	// 8. Restore A only through an authenticated activation.
	mustIngest(t, k, "T", env(L, hA, 7, activateEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// 9. Reject stale B events after the restore.
	for _, evt := range []Event{
		completeEvt(b1.ID, 2, fence(0x44)),
		promptReadyEvt(),
		startEvt(nil, "whoami"),
	} {
		if _, ingestErr := k.Ingest("T", env(L, hB, 6, evt)); !errors.Is(ingestErr, ErrDomainNotLive) {
			t.Fatalf("stale B event must be rejected, got %v", ingestErr)
		}
	}
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// 10. Disconnect T and lose every domain bound to it.
	if bindErr := k.TransportLost("T"); bindErr != nil {
		t.Fatal(bindErr)
	}
	st = mustState(t, k, L)
	if st.Lifecycle != LifecycleLost || len(st.Stack) != 0 {
		t.Fatalf("lane must fall to Lost with an empty stack, got %+v", st)
	}
	if dA, _ := k.Domain(hA.Domain); dA.State != DomainLost {
		t.Fatalf("A must be lost with its transport, got %v", dA.State)
	}

	// 11. Reconnect through a new transport; fresh epochs required, never
	// a resumed one.
	tp2 := &fakePort{}
	if bindErr := k.BindTransport("T2", tp2); bindErr != nil {
		t.Fatal(bindErr)
	}
	hA2, err := k.RequestDomain(L, nil, "T2")
	if err != nil {
		t.Fatal(err)
	}
	if hA2.Epoch <= hA.Epoch {
		t.Fatalf("new establishment must mint a fresh epoch: %d <= %d", hA2.Epoch, hA.Epoch)
	}
	// The old epoch on the new transport is rejected; the old capability on
	// the new domain is rejected; the fresh domain authenticates.
	if _, ingestErr := k.Ingest("T2", envRaw(L, hA2.Domain, hA.Epoch, hA2.Capability, 1, helloEvt("bash"))); !errors.Is(ingestErr, ErrStaleEpoch) {
		t.Fatalf("stale epoch must be rejected, got %v", ingestErr)
	}
	if _, ingestErr := k.Ingest("T2", envRaw(L, hA2.Domain, hA2.Epoch, hA.Capability, 1, helloEvt("bash"))); !errors.Is(ingestErr, ErrBadCapability) {
		t.Fatalf("old capability must be rejected, got %v", ingestErr)
	}
	mustIngest(t, k, "T2", env(L, hA2, 1, helloEvt("bash")))
	mustAccept(t, tp2)
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA2.Domain, "", []DomainID{hA2.Domain})

	// 12. Two lanes multiplexed through one fake helper-like transport.
	k2, _, _ := newTestKernel()
	rp := &fakePort{}
	if bindErr := k2.BindTransport("R", rp); bindErr != nil {
		t.Fatal(bindErr)
	}
	hX, err := k2.RequestDomain("L1", nil, "R")
	if err != nil {
		t.Fatal(err)
	}
	hY, err := k2.RequestDomain("L2", nil, "R")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, k2, "R", env("L1", hX, 1, helloEvt("bash")))
	mustIngest(t, k2, "R", env("L2", hY, 1, helloEvt("bash")))
	// The helper multiplexes: each envelope names its lane and domain; the
	// kernel keeps the two lanes' domains strictly apart.
	if _, ingestErr := k2.Ingest("R", env("L1", hY, 2, startEvt(nil, "ls"))); !errors.Is(ingestErr, ErrWrongLane) {
		t.Fatalf("cross-lane event must be rejected, got %v", ingestErr)
	}
	x1, err := k2.SubmitAttempt(hX.Domain, "make", "/work/nocx", "local", "")
	if err != nil {
		t.Fatal(err)
	}
	y1, err := k2.SubmitAttempt(hY.Domain, "ls", "/tmp", "remote", "")
	if err != nil {
		t.Fatal(err)
	}
	// Interleave the two lanes' events through one transport.
	mustIngest(t, k2, "R", env("L1", hX, 2, startEvt(&x1.ID, "make")))
	mustIngest(t, k2, "R", env("L2", hY, 2, startEvt(&y1.ID, "ls")))
	mustIngest(t, k2, "R", env("L2", hY, 3, completeEvt(y1.ID, 0, fence(0x51))))
	mustIngest(t, k2, "R", env("L1", hX, 3, completeEvt(x1.ID, 0, fence(0x52))))
	mustIngest(t, k2, "R", env("L2", hY, 4, promptReadyEvt()))
	// L2's completion must not have touched L1's running attempt.
	stX := mustState(t, k2, "L1")
	assertState(t, stX, LifecycleRunning, hX.Domain, x1.ID, []DomainID{hX.Domain})
	stY := mustState(t, k2, "L2")
	assertState(t, stY, LifecyclePromptReady, hY.Domain, "", []DomainID{hY.Domain})
	mustIngest(t, k2, "R", env("L1", hX, 4, promptReadyEvt()))
	// A nested domain on L1 over the same helper transport, while L2 stays
	// put — the helper carries several domains across two lanes.
	mustIngest(t, k2, "R", env("L1", hX, 5, suspendEvt()))
	hXc, err := k2.RequestDomain("L1", &hX.Domain, "R")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, k2, "R", env("L1", hXc, 1, helloEvt("bash")))
	stX = mustState(t, k2, "L1")
	assertState(t, stX, LifecyclePromptReady, hXc.Domain, "", []DomainID{hX.Domain, hXc.Domain})
	// L2 unaffected by L1's nesting.
	stY = mustState(t, k2, "L2")
	assertState(t, stY, LifecyclePromptReady, hY.Domain, "", []DomainID{hY.Domain})
	// Transport loss takes every lane bound to the helper down with it.
	if bindErr := k2.TransportLost("R"); bindErr != nil {
		t.Fatal(bindErr)
	}
	for _, lane := range []LaneID{"L1", "L2"} {
		st := mustState(t, k2, lane)
		if st.Lifecycle != LifecycleLost {
			t.Fatalf("lane %s must be lost with the helper, got %+v", lane, st)
		}
	}
}

func statesEqual(a, b LaneSnapshot) bool {
	if a.Lifecycle != b.Lifecycle || a.Domain != b.Domain || a.Attempt != b.Attempt {
		return false
	}
	if len(a.Stack) != len(b.Stack) {
		return false
	}
	for i := range a.Stack {
		if a.Stack[i] != b.Stack[i] {
			return false
		}
	}
	if len(a.OpenAttempts) != len(b.OpenAttempts) {
		return false
	}
	for i := range a.OpenAttempts {
		if a.OpenAttempts[i] != b.OpenAttempts[i] {
			return false
		}
	}
	return true
}
