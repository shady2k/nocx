package lifecycle

import (
	"errors"
	"testing"
)

// The capability's VALIDITY interval and the recovery fence's AUTHORITY
// interval, each with both of its ends (carrier design §5.3; AGENTS.md rule 3:
// an invariant with no named closing event is not yet understood).
//
// The capability is three intervals, not one, because they close on different
// events and conflating them promises more than we can hold:
//
//   - CONFIDENTIALITY opens at minting and closes at the LAST of the per-copy
//     events, each named rather than summarised. Its copies are spread across
//     the backend, the frame and the far host, so no one package can assert
//     it; the copies this package owns are asserted here and the attempt's own
//     buffer is asserted where it lives (internal/app, internal/shellintegration).
//   - VALIDITY opens at minting and is closed HARD by backend invalidation.
//     THIS IS THE INTERVAL WE CAN GUARANTEE, and it is the whole of what
//     bounds the one exposure §6.1 admits it cannot remove: a forged
//     STAGE_READY that outruns an honest refusal produces a bearer, and the
//     refusal is what kills it. That is what these tests are for.
//   - RETENTION OF THE REMOTE COPY closes at observed hook teardown or session
//     exit, and if the channel is lost we cannot guarantee prompt erasure of a
//     variable in a shell we can no longer address. The design does not claim
//     we can, so nothing here asserts that we do.

// Assertion 10: bootstrap refusal, timeout, epoch rotation and session
// teardown EACH close the capability's validity interval — after each, a frame
// of that epoch is rejected.
//
// It asserts INVALIDATION, not erasure of every copy. A rejected frame is the
// observable end of the interval; whether some copy of the bytes still exists
// somewhere is a different interval with different ends.
func TestCapabilityValidity_ClosesOnEveryInvalidationEvent(t *testing.T) {
	// The four events, each named as the design names it. Three of them run
	// through the same seam — the backend closing the transport it opened —
	// and that is worth stating rather than collapsing: a refusal, a
	// timeout and a teardown are three different reasons to reach it, and a
	// change that stopped one of them from reaching it would leave a live
	// epoch behind exactly where the design says there must not be one.
	events := map[string]func(k *Kernel, tid TransportID, lane LaneID, h DomainHandle){
		"a bootstrap refusal": func(k *Kernel, tid TransportID, _ LaneID, _ DomainHandle) {
			// internal/ssh closes the lifecycle handle on any terminal
			// outcome that is not `accepted`, which is this.
			if err := k.TransportLost(tid); err != nil {
				t.Fatalf("TransportLost: %v", err)
			}
		},
		"a bootstrap timeout": func(k *Kernel, tid TransportID, _ LaneID, _ DomainHandle) {
			if err := k.TransportLost(tid); err != nil {
				t.Fatalf("TransportLost: %v", err)
			}
		},
		"session teardown": func(k *Kernel, tid TransportID, _ LaneID, _ DomainHandle) {
			if err := k.TransportLost(tid); err != nil {
				t.Fatalf("TransportLost: %v", err)
			}
		},
		"epoch rotation": func(k *Kernel, tid TransportID, lane LaneID, h DomainHandle) {
			// The lane has to be free before it can carry a new domain,
			// which is the rotation: the old epoch is gone and a fresh
			// one takes the lane. A new session is never a resumption.
			if err := k.TransportLost(tid); err != nil {
				t.Fatalf("TransportLost: %v", err)
			}
			if _, err := k.RequestDomain(lane, nil, tid); err != nil {
				t.Fatalf("rotate: %v", err)
			}
		},
	}

	for name, close := range events {
		t.Run(name, func(t *testing.T) {
			k, _, _ := newTestKernel()
			const tid = TransportID("T")
			const lane = LaneID("lane-1")
			if err := k.BindTransport(tid, &fakePort{}); err != nil {
				t.Fatalf("BindTransport: %v", err)
			}
			h, err := k.RequestDomain(lane, nil, tid)
			if err != nil {
				t.Fatalf("RequestDomain: %v", err)
			}
			// The interval is OPEN: a frame of this epoch, with this
			// capability, is accepted. Without this half the test would
			// pass against a kernel that rejects everything.
			if _, err := k.Ingest(tid, env(lane, h, 1, helloEvt("/bin/bash"))); err != nil {
				t.Fatalf("the interval was never open: hello = %v", err)
			}

			close(k, tid, lane, h)

			// And it is CLOSED: a frame of that epoch is rejected. The
			// sequence is fresh so a replay rule cannot be what refuses it.
			if _, err := k.Ingest(tid, env(lane, h, 2, promptReadyEvt())); err == nil {
				t.Fatal("a frame of the invalidated epoch was accepted; the validity interval never closed")
			}
			// Including a fresh hello, which is the one an attacker holding
			// a raced capability would send to claim the domain.
			if _, err := k.Ingest(tid, env(lane, h, 3, helloEvt("/bin/bash"))); err == nil {
				t.Fatal("a hello of the invalidated epoch was accepted; a raced bearer would still claim the domain")
			}
		})
	}
}

// Assertion 11's authority half: the fence's authority interval closes on ITS
// OWN events — sent-and-acknowledged, teardown, generation replacement — one
// case each. They are not the capability's events and must not be conflated
// with them.
func TestRecoveryFenceAuthority_ClosesOnItsOwnEvents(t *testing.T) {
	t.Run("sent once on channel loss and acknowledged", func(t *testing.T) {
		k, lane, tid, h := laneWithDomain(t)
		mustEstablish(t, k, tid, lane, h)
		// The channel is lost: the fence goes out with the lost fact, and
		// the backend KEEPS the expected value, because it is what
		// validates the acknowledgement. That is the interval still open.
		if err := k.TransportLost(tid); err != nil {
			t.Fatalf("TransportLost: %v", err)
		}
		st, _ := k.State(lane)
		if st.RecoveryNonce == (FenceNonce{}) {
			t.Fatal("the expected fence was dropped when the fence was sent; nothing would validate the answer")
		}
		// The acknowledgement lands. Authority closes.
		if err := k.RecoverLane(lane); err != nil {
			t.Fatalf("RecoverLane: %v", err)
		}
		st, _ = k.State(lane)
		if st.RecoveryNonce != (FenceNonce{}) {
			t.Error("the authority interval did not close on acknowledgement")
		}
	})

	t.Run("teardown with no recovery needed", func(t *testing.T) {
		k, lane, tid, h := laneWithDomain(t)
		mustEstablish(t, k, tid, lane, h)
		// A clean close: the shell said goodbye, so there is nothing to
		// recover and the fence has no further authority to hold.
		if _, err := k.Ingest(tid, env(lane, h, 3, closeEvt())); err != nil {
			t.Fatalf("domain_closed: %v", err)
		}
		st, _ := k.State(lane)
		if st.RecoveryNonce != (FenceNonce{}) {
			t.Error("the authority interval did not close on a teardown with no recovery needed")
		}
	})

	t.Run("generation replacement", func(t *testing.T) {
		k, lane, tid, h := laneWithDomain(t)
		first, _ := k.State(lane)
		if err := k.TransportLost(tid); err != nil {
			t.Fatalf("TransportLost: %v", err)
		}
		if _, err := k.RequestDomain(lane, nil, tid); err != nil {
			t.Fatalf("replace: %v", err)
		}
		st, _ := k.State(lane)
		if st.RecoveryNonce == first.RecoveryNonce {
			t.Error("a replacement generation kept the previous fence; a late ack from the old episode would still match")
		}
		if st.RecoveryNonce == (FenceNonce{}) {
			t.Error("a replacement generation left no fence at all")
		}
		_ = h
	})
}

// The half of assertion 11 that is easiest to get wrong, and the one the
// design calls out in as many words: CLOSING AUTHORITY IS ASSERTED NOT TO
// CLOSE CONFIDENTIALITY BY ITSELF.
//
// After the acknowledgement the fence has no authority left, and a copy of it
// still exists — on the domain record here, and on the far host in a shell
// variable we may no longer be able to address. A test that asserted "and now
// every copy is gone" would be asserting a promise this design explicitly
// declines to make.
func TestRecoveryFenceAuthority_ClosingItDoesNotDestroyEveryCopy(t *testing.T) {
	k, lane, tid, h := laneWithDomain(t)
	mustEstablish(t, k, tid, lane, h)
	if err := k.TransportLost(tid); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	if err := k.RecoverLane(lane); err != nil {
		t.Fatalf("RecoverLane: %v", err)
	}
	st, _ := k.State(lane)
	if st.RecoveryNonce != (FenceNonce{}) {
		t.Fatal("authority did not close, so this test asserts nothing")
	}
	// A different copy, on a different clock: the domain's own record.
	d, ok := k.registry.Lookup(h.Domain)
	if !ok {
		t.Fatal("the domain record is gone; this test needs a surviving copy to be about")
	}
	if d.recovery == (FenceNonce{}) {
		t.Error("closing the authority interval also destroyed the domain's copy — the two " +
			"intervals have been conflated, which promises more than this design holds")
	}
	if d.recovery != h.Recovery {
		t.Error("the domain's copy is not the fence that was minted for it")
	}
}

// laneWithDomain is one bound transport, one lane, one Pending domain.
func laneWithDomain(t *testing.T) (*Kernel, LaneID, TransportID, DomainHandle) {
	t.Helper()
	k, _, _ := newTestKernel()
	const tid = TransportID("T")
	const lane = LaneID("lane-1")
	if err := k.BindTransport(tid, &fakePort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := k.RequestDomain(lane, nil, tid)
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	return k, lane, tid, h
}

// mustEstablish drives the handshake far enough that the fence has authority:
// the bootstrap succeeded and the backend has registered it for a generation.
func mustEstablish(t *testing.T, k *Kernel, tid TransportID, lane LaneID, h DomainHandle) {
	t.Helper()
	out, err := k.Ingest(tid, env(lane, h, 1, helloEvt("/bin/bash")))
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	for _, o := range out {
		if o.Envelope.Event.Kind == KindAccept {
			if derr := k.Deliver(o); derr != nil {
				t.Fatalf("deliver accept: %v", derr)
			}
		}
	}
}

// A domain with no capability authenticates NOBODY.
//
// Found while establishing where the validity interval opens, and it is not
// this package's own work: randomCapability USED TO tolerate a failed random
// read and leave a zero capability, and `d.capability != env.Capability`
// compares equal when both are zero — so any candidate sending thirty-two zero
// bytes was authenticated for a domain whose mint had failed.
//
// The mint is fallible now: a failed read is ErrNoRandomness and no domain is
// created (nocx-s16k8, mint_failure_test.go). This test is the OTHER guard and
// deliberately does not depend on it — it writes the zero straight onto a
// registered domain, so it stays true however a zero got there and goes red on
// its own if the ingest-side refusal is ever removed.
//
// The interval never opens rather than opening to everyone.
func TestCapabilityValidity_AZeroCapabilityAuthenticatesNobody(t *testing.T) {
	k, _, _ := newTestKernel()
	const tid = TransportID("T")
	const lane = LaneID("lane-1")
	if err := k.BindTransport(tid, &fakePort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := k.RequestDomain(lane, nil, tid)
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	// The machine had no randomness when this domain was minted.
	d, ok := k.registry.Lookup(h.Domain)
	if !ok {
		t.Fatal("domain missing")
	}
	d.capability = Capability{}

	// The bearer a candidate can produce without knowing anything.
	_, err = k.Ingest(tid, envRaw(lane, h.Domain, h.Epoch, Capability{}, 1, helloEvt("/bin/bash")))
	if !errors.Is(err, ErrBadCapability) {
		t.Fatalf("a zero capability authenticated: err = %v, want %v", err, ErrBadCapability)
	}
	// And the honest bearer is refused too, because there is no longer one to
	// match: fail closed, never open.
	if _, err := k.Ingest(tid, envRaw(lane, h.Domain, h.Epoch, h.Capability, 2, helloEvt("/bin/bash"))); err == nil {
		t.Fatal("a domain that failed to mint a capability accepted a frame")
	}
}
