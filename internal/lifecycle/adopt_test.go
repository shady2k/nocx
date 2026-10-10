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
	//
	// "Cannot know" is asserted three ways, because it used to be asserted
	// one way that could not fail (nocx-aqz7o). The zero capability is the
	// weakest of them: it is refused by an explicit zero test, so on its own
	// it proves only that a descendant guessing NOTHING is refused. A
	// WELL-FORMED wrong value is the guess a descendant would actually make.
	// And the third is the one that matters — the value a descendant obtains
	// by READING the channel, which is the only place a capability it was
	// never handed could come from.
	descendantGuesses := map[string]Capability{}
	descendantGuesses["nothing at all"] = Capability{}
	var allOnes Capability
	for i := range allOnes {
		allOnes[i] = 0xff
	}
	descendantGuesses["a well-formed wrong value"] = allOnes
	nearMiss := cap
	nearMiss[len(nearMiss)-1] ^= 0x01
	descendantGuesses["the real capability with one bit wrong"] = nearMiss

	// What a descendant reads off the descriptor. Every frame the kernel
	// sends this domain is delivered through the port and its capability
	// harvested; the harvest is the descendant's whole knowledge of the
	// bearer, and it must come to nothing.
	answers, aerr := k.Ingest("tpt-adopt", env(lane, h, 41, Event{
		Kind: KindAgentEnrol, AgentEnrol: &AgentEnrol{
			RequestID: "r-agent-0", Agent: "claude", Cols: 80, Rows: 24,
		},
	}))
	if aerr != nil {
		t.Fatalf("agent_enrol in the adopted domain: %v", aerr)
	}
	refresh, gerr := k.NotifyGap("tpt-adopt", h.Domain, 64, 1)
	if gerr != nil {
		t.Fatalf("NotifyGap: %v", gerr)
	}
	for _, out := range append(answers, refresh...) {
		if derr := k.Deliver(out); derr != nil {
			t.Fatalf("Deliver(%s): %v", out.Envelope.Event.Kind, derr)
		}
	}
	read := port.envelopes()
	if len(read) == 0 {
		t.Fatal("no outbound frame reached the descriptor, so the harvest proves nothing")
	}
	var harvested Capability
	for _, sent := range read {
		if sent.Capability != (Capability{}) {
			t.Fatalf("the %s frame the kernel sent this domain carries the capability; a descendant that inherited the descriptor reads it",
				sent.Event.Kind)
		}
		harvested = sent.Capability
	}
	descendantGuesses["everything readable off the channel"] = harvested

	seq := uint64(41)
	for name, guess := range descendantGuesses {
		t.Run(name, func(t *testing.T) {
			seq++
			forged := h
			forged.Capability = guess
			id := AttemptID("forged")
			if _, err := k.Ingest("tpt-adopt", env(lane, forged, seq, Event{
				Kind: KindStart, Start: &Start{AttemptID: &id, Command: "curl evil"},
			})); !errors.Is(err, ErrBadCapability) {
				t.Fatalf("a frame carrying %q was accepted into an adopted domain: %v", name, err)
			}
		})
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

// The retained window's completion-only replay (ADR-0076): a command that
// ran for the coordinator BEFORE this one completes over the adopted
// domain, naming the shell's own attempt id this kernel never saw. The
// attempt is reconstructed from the shell's authenticated word and
// completed with the reported exit and fence — the lane fact then closes
// the ledger row the previous coordinator opened under the same id and
// resolves the interval end the helper resent. A kernel-native domain
// keeps the foreign-id refusal: an id it neither minted nor adopted names
// nothing.
func TestACompletionOnlyReplayReconstructsTheAdoptedAttempt(t *testing.T) {
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

	id := AttemptID("shell-attempt-past")
	if _, ierr := k.Ingest("tpt-adopt", env(lane, h, 41, Event{
		Kind: KindComplete, Complete: &Complete{AttemptID: &id, ExitCode: intPtr(0), Fence: fence(0x71)},
	})); ierr != nil {
		t.Fatalf("the replayed completion of the previous coordinator's attempt was refused: %v", ierr)
	}
	got, ok := k.Attempt(id)
	if !ok || got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 0 || got.Fence != fence(0x71) {
		t.Fatalf("reconstructed attempt = %+v, want completed with the replay's exit and fence", got)
	}
	snap, serr := k.State(lane)
	if serr != nil {
		t.Fatalf("State: %v", serr)
	}
	// The lane fact must still name the attempt, completed: it is what
	// carries the terminal fact to the ledger and the block stream.
	if snap.Attempt != id {
		t.Fatalf("lane attempt after the replay = %q, want the reconstructed attempt named", snap.Attempt)
	}
	if snap.Lifecycle != LifecycleRunning {
		t.Fatalf("lane lifecycle after the replay = %v, want Running until the prompt", snap.Lifecycle)
	}
	// Exit status is set exactly once here too.
	if _, ierr := k.Ingest("tpt-adopt", env(lane, h, 42, Event{
		Kind: KindComplete, Complete: &Complete{AttemptID: &id, ExitCode: intPtr(1), Fence: fence(0x72)},
	})); !errors.Is(ierr, ErrAttemptNotOpen) {
		t.Fatalf("second completion must be rejected, got %v", ierr)
	}
}

// The native-domain guard stands: an unknown id on a domain this kernel
// minted is refused, exactly as before.
func TestACompletionForAnUnknownAttemptOnANativeDomainIsRefused(t *testing.T) {
	k, _, _ := newTestKernel()
	port := &fakePort{}
	if err := k.BindTransport("tpt", port); err != nil {
		t.Fatalf("bind: %v", err)
	}
	h := establish(t, k, "tpt", port, "lane-x", nil)
	bogus := AttemptID("does-not-exist")
	if _, ierr := k.Ingest("tpt", env("lane-x", h, 2, Event{
		Kind: KindComplete, Complete: &Complete{AttemptID: &bogus, ExitCode: intPtr(0), Fence: fence(0x73)},
	})); !errors.Is(ierr, ErrAttemptNotOpen) {
		t.Fatalf("foreign attempt id on a native domain must be refused, got %v", ierr)
	}
}

// The replayed completion is unnamed by protocol — the kernel resolves the
// domain's single open attempt — so the completion-only replay of a command
// that ran for the previous coordinator arrives with no id at all. On an
// adopted domain it reconstructs the attempt under a synthetic id, and the
// lane names it so the terminal fact still flows.
func TestAnUnnamedCompletionOnlyReplayReconstructsTheAdoptedAttempt(t *testing.T) {
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

	if _, ierr := k.Ingest("tpt-adopt", env(lane, h, 41, Event{
		Kind: KindComplete, Complete: &Complete{ExitCode: intPtr(0), Fence: fence(0x74)},
	})); ierr != nil {
		t.Fatalf("the unnamed replayed completion was refused: %v", ierr)
	}
	snap, serr := k.State(lane)
	if serr != nil {
		t.Fatalf("State: %v", serr)
	}
	if snap.Attempt == "" {
		t.Fatal("the lane names no attempt after the unnamed replay: the terminal fact would carry nothing")
	}
	got, ok := k.Attempt(snap.Attempt)
	if !ok || got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 0 || got.Fence != fence(0x74) {
		t.Fatalf("reconstructed attempt = %+v, want completed with the replay's exit and fence", got)
	}
}
