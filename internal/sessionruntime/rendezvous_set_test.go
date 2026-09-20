package sessionruntime

import (
	"errors"
	"testing"
)

// The two blockers the review filed against the rendezvous, as tests that can
// fail:
//
//  1. A forged fence — any program's output — parked a meeting whose expiry
//     treated it like an authenticated one and set CompletenessNoFence, so
//     unauthenticated output permanently revoked write authority
//     (Commit answers ErrCompletenessUnknown and never recovers, because the
//     runtime survives the coordinator). Expiring a meeting with nothing
//     authenticated behind it must change NOTHING but the meeting.
//  2. The rendezvous was one slot, so a second command evicted the first:
//     its fence was then rejected on nonce mismatch, its pinned source was
//     gone, and completeness could still read complete. The rendezvous is a
//     set keyed by nonce (the coordinator's decision; ADR-0024 decision 7),
//     bounded by MaxPendingRendezvous.
//
// Every wait here is fired by hand through the injected scheduler and asserted
// as a STATE; nothing reads a clock (AGENTS.md). Each test carries its own
// mutation record in the worker report for fix-rendezvous-8W4D.

const (
	// setForgedHex is a fence nonce nothing authenticated carries.
	setForgedHex = "f001f001f001f001f001f001f001f001f001f001f001f001f001f001f001f001"
	// setANoncedHex and setBNonceHex are two commands' nonces, distinct from
	// every other fixture's.
	setANoncedHex = "a10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10ba10b"
	setBNonceHex  = "b20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20cb20c"
)

// grantPerson hands the person the keys: the write gate's gatekeeper.
func grantPerson(t *testing.T, s *Session) Control {
	t.Helper()
	ctrl, err := grant(s, person())
	if err != nil {
		t.Fatalf("grant control to the person: %v", err)
	}
	return ctrl
}

// mustCommit admits and commits one key intent. It is the write-authority
// probe: Commit answers ErrCompletenessUnknown — and nothing else it can
// answer here — once the gate has closed.
func mustCommit(t *testing.T, s *Session, ctrl Control, key byte) {
	t.Helper()
	id, err := admitKey(s, ctrl, []byte{key})
	if err != nil {
		t.Fatalf("admit an intent: %v", err)
	}
	if _, err := s.Commit(Intent{ID: id}, nil); err != nil {
		t.Fatalf("commit an intent while the gate should be open: %v", err)
	}
}

// mustRefuseCommit is mustCommit for the gate that has to be shut.
func mustRefuseCommit(t *testing.T, s *Session, ctrl Control, key byte) {
	t.Helper()
	id, err := admitKey(s, ctrl, []byte{key})
	if err != nil {
		t.Fatalf("admit an intent: %v", err)
	}
	if _, err := s.Commit(Intent{ID: id}, nil); !errors.Is(err, ErrCompletenessUnknown) {
		t.Fatalf("commit with completeness %v returned %v, want %v: the write gate must refuse", s.Completeness(), err, ErrCompletenessUnknown)
	}
}

// TestAForgedFenceExpiryLeavesCompletenessAndWriteAuthorityAlone is blocker 1.
// Asserted as an INTERVAL, not a moment: before the expiry (parked, gate
// open), at the expiry (meeting expired, pin dropped, completeness untouched)
// and after it (commit still accepted).
func TestAForgedFenceExpiryLeavesCompletenessAndWriteAuthorityAlone(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))
	nonce := fenceNonceFromString(t, setForgedHex)
	ctrl := grantPerson(t, s)

	if err := s.SightFence(nonce, []byte("a fence nobody authenticated")); err != nil {
		t.Fatalf("sight the forged fence: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the forged fence parked %s, want awaiting-authenticated", rendezvousStateName(got))
	}

	// BEFORE: the gate is open while the unbacked sighting waits.
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("a parked sighting moved completeness to %v, want unchanged complete", got)
	}
	mustCommit(t, s, ctrl, 'l')

	// AT: the bounded wait elapses. The pin is dropped and nothing else
	// changes.
	sched.fire()
	rv := s.RendezvousFor(nonce)
	if rv.State != RendezvousExpired {
		t.Fatalf("the unbacked meeting is %s after its wait elapsed, want expired", rendezvousStateName(rv.State))
	}
	if len(rv.PinnedSource) != 0 {
		t.Fatalf("the expired sighting still pins %q, want nothing pinned", rv.PinnedSource)
	}
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("expiring an unbacked sighting moved completeness to %v, want unchanged complete: a fence that authorised nothing may degrade nothing", got)
	}

	// AFTER: write authority survived — a second intent still commits.
	mustCommit(t, s, ctrl, 's')
}

// TestAnAuthenticatedCompletionWhoseFenceNeverArrivesStillEndsNoFence guards
// the other end of the distinction: the expiry that DOES speak about the
// interval is the authenticated half's, and it must still be NoFence — with
// the write gate refusing — once the fix above exists.
func TestAnAuthenticatedCompletionWhoseFenceNeverArrivesStillEndsNoFence(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))
	nonce := fenceNonceFromString(t, setANoncedHex)
	ctrl := grantPerson(t, s)

	s.Completed(s.Incarnation(), nonce, 0)
	if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the completion parked %s, want awaiting-sighting", rendezvousStateName(got))
	}
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("a parked completion moved completeness to %v, want unchanged complete", got)
	}

	sched.fire()

	if got := s.RendezvousFor(nonce).State; got != RendezvousExpired {
		t.Fatalf("the meeting is %s after its wait elapsed, want expired", rendezvousStateName(got))
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("an authenticated completion whose fence never arrived left completeness %v, want no-fence", got)
	}
	mustRefuseCommit(t, s, ctrl, 'l')
}

// TestTwoAuthenticatedCompletionsKeepTheirOwnPinsWhateverOrderTheirFencesArriveIn
// is blocker 2, completion-first: two completions pending at once, their
// fences crossing, each meeting ending complete with its OWN pinned source.
func TestTwoAuthenticatedCompletionsKeepTheirOwnPinsWhateverOrderTheirFencesArriveIn(t *testing.T) {
	s, _, _ := realRuntime(t)
	nonceA := fenceNonceFromString(t, setANoncedHex)
	nonceB := fenceNonceFromString(t, setBNonceHex)
	sourceA := []byte("output of command A")
	sourceB := []byte("output of command B")

	s.Completed(s.Incarnation(), nonceA, 0)
	s.Completed(s.Incarnation(), nonceB, 0)
	for _, n := range []FenceNonce{nonceA, nonceB} {
		if got := s.RendezvousFor(n).State; got != RendezvousAwaitingSighting {
			t.Fatalf("a second completion left a meeting %s, want both still awaiting their own sighting", rendezvousStateName(got))
		}
	}

	// The fences arrive in the other order.
	if err := s.SightFence(nonceB, sourceB); err != nil {
		t.Fatalf("sight the second fence: %v", err)
	}
	if err := s.SightFence(nonceA, sourceA); err != nil {
		t.Fatalf("sight the first fence: %v", err)
	}
	for _, tc := range []struct {
		nonce  FenceNonce
		source []byte
	}{
		{nonceA, sourceA},
		{nonceB, sourceB},
	} {
		rv := s.RendezvousFor(tc.nonce)
		if rv.State != RendezvousComplete {
			t.Fatalf("a meeting is %s after both fences arrived, want complete", rendezvousStateName(rv.State))
		}
		if string(rv.PinnedSource) != string(tc.source) {
			t.Fatalf("a meeting pinned %q, want its own %q: nothing was evicted", rv.PinnedSource, tc.source)
		}
	}
}

// TestInterleavedHalvesEndCompleteWithTheirOwnPins drives the mixed order:
// command A completion-first, command B fence-first, crossing on the way.
func TestInterleavedHalvesEndCompleteWithTheirOwnPins(t *testing.T) {
	s, _, _ := realRuntime(t)
	nonceA := fenceNonceFromString(t, setANoncedHex)
	nonceB := fenceNonceFromString(t, setBNonceHex)
	sourceA := []byte("A was drawn over this")
	sourceB := []byte("B was drawn over this")

	s.Completed(s.Incarnation(), nonceA, 0) // A parks, completion first
	if err := s.SightFence(nonceB, sourceB); err != nil {
		t.Fatalf("sight B's fence first: %v", err)
	}
	s.Completed(s.Incarnation(), nonceB, 0) // B closes
	if err := s.SightFence(nonceA, sourceA); err != nil {
		t.Fatalf("sight A's fence second: %v", err)
	}

	for _, tc := range []struct {
		nonce  FenceNonce
		source []byte
	}{
		{nonceA, sourceA},
		{nonceB, sourceB},
	} {
		rv := s.RendezvousFor(tc.nonce)
		if rv.State != RendezvousComplete {
			t.Fatalf("a meeting is %s, want complete", rendezvousStateName(rv.State))
		}
		if string(rv.PinnedSource) != string(tc.source) {
			t.Fatalf("a meeting pinned %q, want its own %q", rv.PinnedSource, tc.source)
		}
	}
}

// TestTwoFencesPendingKeepTheirOwnPinsWhateverOrderTheirCompletionsArriveIn is
// blocker 2, fence-first: two parked sightings, their completions crossing.
func TestTwoFencesPendingKeepTheirOwnPinsWhateverOrderTheirCompletionsArriveIn(t *testing.T) {
	s, _, _ := realRuntime(t)
	nonceA := fenceNonceFromString(t, setANoncedHex)
	nonceB := fenceNonceFromString(t, setBNonceHex)
	sourceA := []byte("A was drawn over this")
	sourceB := []byte("B was drawn over this")

	if err := s.SightFence(nonceA, sourceA); err != nil {
		t.Fatalf("sight the first fence: %v", err)
	}
	if err := s.SightFence(nonceB, sourceB); err != nil {
		t.Fatalf("sight the second fence: %v", err)
	}

	// Both parked, each with its own pin, neither evicting the other.
	for _, tc := range []struct {
		nonce  FenceNonce
		source []byte
	}{
		{nonceA, sourceA},
		{nonceB, sourceB},
	} {
		rv := s.RendezvousFor(tc.nonce)
		if rv.State != RendezvousAwaitingAuthenticated || string(rv.PinnedSource) != string(tc.source) {
			t.Fatalf("a parked meeting reads %s pinning %q, want parked with its own %q",
				rendezvousStateName(rv.State), rv.PinnedSource, tc.source)
		}
	}

	// The completions arrive in the other order.
	s.Completed(s.Incarnation(), nonceB, 0)
	s.Completed(s.Incarnation(), nonceA, 0)
	for _, tc := range []struct {
		nonce  FenceNonce
		source []byte
	}{
		{nonceA, sourceA},
		{nonceB, sourceB},
	} {
		rv := s.RendezvousFor(tc.nonce)
		if rv.State != RendezvousComplete || string(rv.PinnedSource) != string(tc.source) {
			t.Fatalf("a meeting reads %s pinning %q, want complete with its own %q",
				rendezvousStateName(rv.State), rv.PinnedSource, tc.source)
		}
	}
}

// TestAtTheBoundAForgedFenceIsRefusedAndTheAuthenticatedChannelSurvives is the
// bound's behaviour, and the reason the set must not be a map a flood can
// grow: at MaxPendingRendezvous a further FORGED fence is refused and changes
// nothing, a pending authenticated rendezvous is never evicted by the flood,
// and an authenticated completion still gets in — preempting the oldest
// unbacked sighting, which held a slot doing nothing.
func TestAtTheBoundAForgedFenceIsRefusedAndTheAuthenticatedChannelSurvives(t *testing.T) {
	s, _, _ := realRuntime(t)
	auth := fenceNonceFromString(t, setANoncedHex)

	// The authenticated half comes first: one real meeting in flight.
	s.Completed(s.Incarnation(), auth, 0)
	if got := s.RendezvousFor(auth).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the completion parked %s, want awaiting-sighting", rendezvousStateName(got))
	}

	// The flood fills the rest of the set.
	forged := make([]FenceNonce, 0, MaxPendingRendezvous-1)
	for i := 0; i < MaxPendingRendezvous-1; i++ {
		nonce := fenceNonceFromString(t, fenceHexByte(byte(0xf0+i)))
		if err := s.SightFence(nonce, []byte("forged")); err != nil {
			t.Fatalf("sight forged fence %d: %v", i, err)
		}
		forged = append(forged, nonce)
	}

	// At the bound the next forged fence is refused — and refused without
	// touching the real meeting or the flood it arrived with.
	if err := s.SightFence(fenceNonceFromString(t, setForgedHex), []byte("one too many")); !errors.Is(err, ErrRendezvousFull) {
		t.Fatalf("a sighting at the bound returned %v, want %v", err, ErrRendezvousFull)
	}
	if got := s.RendezvousFor(auth).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the flood left the authenticated meeting %s, want it still waiting: a forged fence evicts nothing", rendezvousStateName(got))
	}
	for i, nonce := range forged {
		if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
			t.Fatalf("forged fence %d's meeting is %s, want still parked", i, rendezvousStateName(got))
		}
	}

	// An authenticated completion is never refused while a mere sighting
	// holds a slot: it preempts the OLDEST unbacked one.
	second := fenceNonceFromString(t, setBNonceHex)
	s.Completed(s.Incarnation(), second, 0)
	if got := s.RendezvousFor(second).State; got != RendezvousAwaitingSighting {
		t.Fatalf("a completion at the bound parked %s, want awaiting-sighting: the authenticated channel is never the one refused", rendezvousStateName(got))
	}
	if got := s.RendezvousFor(forged[0]).State; got != RendezvousIdle {
		t.Fatalf("the oldest forged fence's meeting is %s after the preemption, want gone", rendezvousStateName(got))
	}
	if got := s.RendezvousFor(forged[len(forged)-1]).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the newest forged fence's meeting is %s after the preemption, want still parked", rendezvousStateName(got))
	}
}

// TestTheBoundRecyclesSettledMeetings: a settled meeting is record, not
// authority — the oldest one gives up its slot to a new sighting, so a
// session that runs commands one after another never wedges at the bound.
func TestTheBoundRecyclesSettledMeetings(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))

	// Fill the set exactly: MaxPendingRendezvous forged fences, each with
	// its own wait.
	nonces := make([]FenceNonce, 0, MaxPendingRendezvous)
	for i := 0; i < MaxPendingRendezvous; i++ {
		nonce := fenceNonceFromString(t, fenceHexByte(byte(0xe0+i)))
		if err := s.SightFence(nonce, []byte("forged")); err != nil {
			t.Fatalf("sight forged fence %d: %v", i, err)
		}
		nonces = append(nonces, nonce)
	}

	// The oldest expires: its meeting is settled, and the slot it held is
	// record now, not authority.
	sched.fireAt(0)
	if got := s.RendezvousFor(nonces[0]).State; got != RendezvousExpired {
		t.Fatalf("the oldest meeting is %s after its wait elapsed, want expired", rendezvousStateName(got))
	}

	// A new sighting is admitted, taking the settled meeting's place; the
	// still-pending meetings are untouched.
	fresh := fenceNonceFromString(t, setForgedHex)
	if err := s.SightFence(fresh, []byte("the next fence")); err != nil {
		t.Fatalf("a sighting after a meeting settled returned %v, want admitted: settled meetings are record, not authority", err)
	}
	if got := s.RendezvousFor(fresh).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the new meeting parked %s, want awaiting-authenticated", rendezvousStateName(got))
	}
	// The settled meeting is gone — the fresh sighting took its slot — and
	// every meeting still pending is exactly where it was.
	for i, nonce := range nonces {
		want := RendezvousIdle
		if i > 0 {
			want = RendezvousAwaitingAuthenticated
		}
		if got := s.RendezvousFor(nonce).State; got != want {
			t.Fatalf("fence %d's meeting is %s, want %s: the settled slot is recycled, the pending ones are not touched",
				i, rendezvousStateName(got), rendezvousStateName(want))
		}
	}
}

// TestEachMeetingCarriesItsOwnWait: two pending meetings — one backed by an
// authenticated completion, one forged — each hold a wait of their own.
// Expiring the forged one degrades nothing; expiring the authenticated one
// does; and a stale trigger whose meeting has already settled expires nothing.
func TestEachMeetingCarriesItsOwnWait(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))
	auth := fenceNonceFromString(t, setANoncedHex)
	forged := fenceNonceFromString(t, setForgedHex)

	if err := s.SightFence(forged, []byte("forged")); err != nil {
		t.Fatalf("sight the forged fence: %v", err)
	} // arms trigger 0
	s.Completed(s.Incarnation(), auth, 0) // arms trigger 1

	sched.fireAt(0)
	if got := s.RendezvousFor(forged).State; got != RendezvousExpired {
		t.Fatalf("the forged meeting is %s after ITS wait fired, want expired", rendezvousStateName(got))
	}
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("expiring the forged meeting moved completeness to %v, want unchanged", got)
	}
	if got := s.RendezvousFor(auth).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the authenticated meeting is %s, want still parked: the waits are per meeting", rendezvousStateName(got))
	}

	// The forged meeting's trigger, stale now that its meeting has settled,
	// spends nothing further.
	sched.fireAt(0)
	if got := s.RendezvousFor(auth).State; got != RendezvousAwaitingSighting {
		t.Fatalf("a stale trigger moved the authenticated meeting to %s", rendezvousStateName(got))
	}

	sched.fireAt(1)
	if got := s.RendezvousFor(auth).State; got != RendezvousExpired {
		t.Fatalf("the authenticated meeting is %s after ITS wait fired, want expired", rendezvousStateName(got))
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("expiring the authenticated meeting left completeness %v, want no-fence", got)
	}
}

// TestTheWindowNamesTheAdmittedMeetingAfterARecycle pins the no-arg window
// across a bound recycle: the meeting it named is evicted to make room, and
// the window's next answer is the meeting ADMITTED by that eviction — never
// the evicted one, and never idle about a set that is not empty.
func TestTheWindowNamesTheAdmittedMeetingAfterARecycle(t *testing.T) {
	s, _, _ := realRuntime(t)

	// Fill the set with forged fences; the window names the LAST sighted.
	nonces := make([]FenceNonce, 0, MaxPendingRendezvous)
	for i := 0; i < MaxPendingRendezvous; i++ {
		nonce := fenceNonceFromString(t, fenceHexByte(byte(0xd0+i)))
		if err := s.SightFence(nonce, []byte("forged")); err != nil {
			t.Fatalf("sight forged fence %d: %v", i, err)
		}
		nonces = append(nonces, nonce)
	}
	if got := s.Rendezvous(); got.Nonce != nonces[MaxPendingRendezvous-1] {
		t.Fatalf("the window names %v, want the most recently sighted meeting", got.Nonce)
	}

	// The meeting the window names settles, and its slot is then recycled by
	// a fresh sighting: the window must fall back to a SURVIVING meeting.
	if err := s.ExpireRendezvous(nonces[MaxPendingRendezvous-1]); err != nil {
		t.Fatalf("expire the latest meeting: %v", err)
	}
	fresh := fenceNonceFromString(t, setForgedHex)
	if err := s.SightFence(fresh, []byte("the meeting that recycles the slot")); err != nil {
		t.Fatalf("sight the recycling fence: %v", err)
	}

	rv := s.Rendezvous()
	if rv.State == RendezvousIdle {
		t.Fatalf("the window answers idle while %d meetings are tracked", MaxPendingRendezvous)
	}
	if rv.Nonce != fresh {
		t.Fatalf("the window fell back to %v, want the most recent surviving meeting %v", rv.Nonce, fresh)
	}
}

// fenceHexByte renders one byte into a 64-hex fence nonce, the shape the
// emulator's adapter matches (fenceNonceOf: exactly 64 hex characters).
func fenceHexByte(b byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 64)
	pair := []byte{hexdigits[b>>4], hexdigits[b&0x0f]}
	for i := 0; i < 32; i++ {
		out = append(out, pair...)
	}
	return string(out)
}
