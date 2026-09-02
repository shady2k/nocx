package lifecycle

import (
	"errors"
	"testing"
)

// The domain a replacing coordinator takes over is not a domain this kernel
// minted, and it is not a Lost one being revived: the shell never lost its
// channel — the helper holds the parent end of the socketpair and kept it
// open — so what died is the REGISTRY that could address it, not the domain.
// These tests pin what adoption may and may not do (nocx-k6p18.31).

func adoptedHandle() (LaneID, DomainID, uint64, Capability, FenceNonce) {
	var cap Capability
	for i := range cap {
		cap[i] = byte(i + 1)
	}
	var rec FenceNonce
	for i := range rec {
		rec[i] = byte(i + 100)
	}
	return LaneID("lane-adopted"), DomainID("dom-adopted"), 7, cap, rec
}

func TestAnAdoptedDomainAcceptsTheShellsNextEventWithoutAHello(t *testing.T) {
	k, _, _ := newTestKernel()
	port := &fakePort{}
	if err := k.BindTransport("tpt-adopt", port); err != nil {
		t.Fatalf("bind: %v", err)
	}
	lane, dom, epoch, cap, rec := adoptedHandle()
	h, err := k.AdoptDomain(lane, dom, epoch, cap, rec, "tpt-adopt")
	if err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	if h.Domain != dom || h.Epoch != epoch || h.Capability != cap || h.Recovery != rec {
		t.Fatalf("adoption changed the identity the shell already holds: %+v", h)
	}

	// The shell was never told anything, so it goes on speaking at whatever
	// sequence it had reached. A command it runs must open an attempt.
	id := AttemptID("shell-attempt-1")
	if _, ierr := k.Ingest("tpt-adopt", env(lane, h, 41, Event{
		Kind: KindStart, Start: &Start{AttemptID: &id, Command: "make"},
	})); ierr != nil {
		t.Fatalf("a command run in the adopted domain was refused: %v", ierr)
	}
	snap, serr := k.State(lane)
	if serr != nil {
		t.Fatalf("State: %v", serr)
	}
	if snap.Lifecycle != LifecycleRunning {
		t.Fatalf("lane lifecycle after the adopted shell started a command = %v, want Running", snap.Lifecycle)
	}
	if len(snap.OpenAttempts) != 1 {
		t.Fatalf("open attempts = %d, want 1", len(snap.OpenAttempts))
	}
}

func TestAnAdoptedDomainStillRefusesAWriterWithoutTheCapability(t *testing.T) {
	k, _, _ := newTestKernel()
	port := &fakePort{}
	if err := k.BindTransport("tpt-adopt", port); err != nil {
		t.Fatalf("bind: %v", err)
	}
	lane, dom, epoch, cap, rec := adoptedHandle()
	h, err := k.AdoptDomain(lane, dom, epoch, cap, rec, "tpt-adopt")
	if err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	// A descendant that inherited the descriptor knows the addressing — it
	// is in the environment — and cannot know the capability.
	forged := h
	forged.Capability = Capability{}
	id := AttemptID("forged")
	if _, err := k.Ingest("tpt-adopt", env(lane, forged, 41, Event{
		Kind: KindStart, Start: &Start{AttemptID: &id, Command: "curl evil"},
	})); !errors.Is(err, ErrBadCapability) {
		t.Fatalf("a frame without the capability was accepted into an adopted domain: %v", err)
	}
}

func TestAdoptionRefusesWhatItCannotAuthenticate(t *testing.T) {
	lane, dom, epoch, cap, rec := adoptedHandle()
	cases := []struct {
		name  string
		lane  LaneID
		dom   DomainID
		epoch uint64
		cap   Capability
		want  error
	}{
		{"no lane", "", dom, epoch, cap, ErrInvalidArgument},
		{"no domain", lane, "", epoch, cap, ErrInvalidArgument},
		{"no epoch", lane, dom, 0, cap, ErrInvalidArgument},
		{"no capability", lane, dom, epoch, Capability{}, ErrInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, _, _ := newTestKernel()
			if err := k.BindTransport("tpt-adopt", &fakePort{}); err != nil {
				t.Fatalf("bind: %v", err)
			}
			if _, err := k.AdoptDomain(tc.lane, tc.dom, tc.epoch, tc.cap, rec, "tpt-adopt"); !errors.Is(err, tc.want) {
				t.Fatalf("AdoptDomain = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAdoptionRefusesAnUnboundTransportAndABusyLane(t *testing.T) {
	k, _, _ := newTestKernel()
	lane, dom, epoch, cap, rec := adoptedHandle()
	if _, err := k.AdoptDomain(lane, dom, epoch, cap, rec, "tpt-nope"); !errors.Is(err, ErrUnknownTransport) {
		t.Fatalf("AdoptDomain on an unbound transport = %v, want ErrUnknownTransport", err)
	}
	if err := k.BindTransport("tpt-adopt", &fakePort{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := k.AdoptDomain(lane, dom, epoch, cap, rec, "tpt-adopt"); err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	if _, err := k.AdoptDomain(lane, "dom-second", epoch+1, cap, rec, "tpt-adopt"); !errors.Is(err, ErrLaneBusy) {
		t.Fatalf("a second adoption onto a live lane = %v, want ErrLaneBusy", err)
	}
	if _, err := k.AdoptDomain("lane-other", dom, epoch, cap, rec, "tpt-adopt"); !errors.Is(err, ErrDomainExists) {
		t.Fatalf("re-adopting a domain id this kernel already holds = %v, want ErrDomainExists", err)
	}
}

// An adopted epoch comes from another process's counter, so this kernel's own
// counter must be lifted past it: a domain minted afterwards that reused the
// number would let one shell's stale frame authenticate against another's
// domain if the ids ever collided.
func TestAdoptionLiftsTheEpochCounterPastWhatItAdopted(t *testing.T) {
	k, _, _ := newTestKernel()
	if err := k.BindTransport("tpt-adopt", &fakePort{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	lane, dom, _, cap, rec := adoptedHandle()
	if _, err := k.AdoptDomain(lane, dom, 40, cap, rec, "tpt-adopt"); err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	h, err := k.RequestDomain("lane-fresh", nil, "tpt-adopt")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	if h.Epoch <= 40 {
		t.Fatalf("a domain minted after an adoption got epoch %d, want > 40", h.Epoch)
	}
}

// Both ends of the interval: the adopted domain is live from the adoption
// until the transport dies, and the transport's death ends it exactly as it
// ends a domain this kernel minted — no special case, no survivor.
func TestAnAdoptedDomainDiesWithItsTransport(t *testing.T) {
	k, _, _ := newTestKernel()
	if err := k.BindTransport("tpt-adopt", &fakePort{}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	lane, dom, epoch, cap, rec := adoptedHandle()
	h, err := k.AdoptDomain(lane, dom, epoch, cap, rec, "tpt-adopt")
	if err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	if lerr := k.TransportLost("tpt-adopt"); lerr != nil {
		t.Fatalf("TransportLost: %v", lerr)
	}
	d, ok := k.Domain(h.Domain)
	if !ok || d.State != DomainLost {
		t.Fatalf("adopted domain after transport loss = %v (found=%v), want DomainLost", d.State, ok)
	}
	snap, serr := k.State(lane)
	if serr != nil {
		t.Fatalf("State: %v", serr)
	}
	if snap.Lifecycle != LifecycleLost {
		t.Fatalf("lane after transport loss = %v, want Lost", snap.Lifecycle)
	}
	if snap.RecoveryNonce != rec {
		t.Fatalf("the lost lane must publish the recovery fence the shell was given at spawn")
	}
}
