package lifecycle

import (
	"errors"
	"io"
	"testing"
)

// failingRand is a randomness source that cannot deliver. The two shapes are
// distinct failures and both must be refused: an outright error, and a SHORT
// read that returns no error — the second is the quieter one, because
// io.ReadFull's own error is what a caller has to notice, and a source that
// returns (n<len, nil) forever is a source io.ReadFull reports as
// ErrUnexpectedEOF only if it ever stops.
type failingRand struct {
	err   error
	short int // bytes to deliver before reporting the error
}

func (f *failingRand) Read(p []byte) (int, error) {
	n := f.short
	if n > len(p) {
		n = len(p)
	}
	for i := 0; i < n; i++ {
		p[i] = 0x01
	}
	f.short = 0
	return n, f.err
}

// A mint that could not get randomness produces NO domain (nocx-s16k8).
//
// randomCapability used to tolerate a failed read and return a zero-valued
// capability, on the argument that the caller has no useful answer to "this
// machine has no randomness". The ingest side then refused a zero capability
// so the hole was closed — but the domain still existed: registered, holding
// an authenticator nobody could ever present, offered to a shell that would
// hand it to the far side and wait forever for a handshake that cannot
// authenticate.
//
// The mint is where the failure is knowable, so it is where it is named. A
// caller CAN act on this: it is the same fail-open every other refusal on
// this path takes — no domain, no integration, a plain shell, a reason.
func TestRequestDomain_AFailedRandomReadMintsNothing(t *testing.T) {
	sentinel := errors.New("no entropy on this machine")
	for _, tc := range []struct {
		name string
		r    io.Reader
	}{
		{"the source reports an error", &failingRand{err: sentinel}},
		{"the source returns short", &failingRand{err: io.ErrUnexpectedEOF, short: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			k := New(Options{Now: clock.now, Rand: tc.r})
			const tid = TransportID("T")
			const lane = LaneID("lane-1")
			if err := k.BindTransport(tid, &fakePort{}); err != nil {
				t.Fatalf("BindTransport: %v", err)
			}

			h, err := k.RequestDomain(lane, nil, tid)
			if !errors.Is(err, ErrNoRandomness) {
				t.Fatalf("RequestDomain returned (%v, %v), want ErrNoRandomness", h, err)
			}
			if h != (DomainHandle{}) {
				t.Errorf("a handle was returned for a domain that was never minted: %v", h)
			}
			// Nothing was offered to a shell, and nothing was left behind
			// for the next caller to trip over: no domain in the registry
			// and the lane is still free.
			if h.Domain != "" {
				if _, ok := k.registry.Lookup(h.Domain); ok {
					t.Error("the domain was registered anyway")
				}
			}
			if got := k.getLane(lane).top(); got != "" {
				t.Errorf("the lane's top is %q; a refused mint must leave the lane free", got)
			}
		})
	}
}

// And the paired success AGENTS.md asks for next to every failure test: on an
// ordinary machine the mint succeeds and the capability is not zero.
func TestRequestDomain_OnAnOrdinaryMachineTheMintSucceeds(t *testing.T) {
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
	if h.Capability == (Capability{}) {
		t.Error("the capability is zero on a machine with working randomness")
	}
	if h.Recovery == (FenceNonce{}) {
		t.Error("the recovery fence is zero on a machine with working randomness")
	}
}

// The SECOND guard — a zero capability refused at ingest, however it got
// there — is asserted independently by
// TestCapabilityValidity_AZeroCapabilityAuthenticatesNobody in
// intervals_test.go, which writes the zero straight onto the registered
// domain and so does not depend on the mint above at all. That is what makes
// the two guards independent: delete either one and exactly one of the two
// tests goes red. It is deliberately not restated here — a second copy of an
// assertion is a second thing to keep true (nocx-s16k8).
