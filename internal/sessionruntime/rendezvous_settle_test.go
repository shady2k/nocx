package sessionruntime

import "testing"

// Settling a boundary by an EVENT, never by a timer (nocx-2v80t.3.9,
// REVIEW-4). The closing screen is the one taken at the fence's SIGHTING —
// the half that arrives in the ordered stream — so a completion that wins the
// race by whole feeds waits for its sighting and reads nothing at the
// completion. When the sighting never arrives at all, the interval is settled
// by the next event there is: the next interval's start (a fence or a
// completion for ANOTHER nonce) or the session's end. The settle seals the
// parked record with NO closing screen, at the row count the completion
// measured, and says so — the meeting reads [RendezvousExpired] and the claim
// about the stream goes [CompletenessNoFence].
//
// No test here reads a clock and none arms a wait: there is no timer in the
// rendezvous to arm, which is the defect REVIEW-4 names (an expiry on a
// duration is itself the defect).

// settledEnds indexes the stream's end markers by the meeting they close, in
// arrival order.
func settledEnds(rs *recordingRowStream) map[FenceNonce]rowEvent {
	out := map[FenceNonce]rowEvent{}
	for _, e := range rs.snapshot() {
		if e.kind == "end" {
			out[e.nonce] = e
		}
	}
	return out
}

// TestACompletionAloneSealsNothing: the completion is not a boundary's screen.
// It parks the interval — the screen is read at the SIGHTING — so nothing is
// sealed and no end marker is emitted until the sighting joins; the join then
// seals on the screen the fence sat on and the count the sighting measured.
func TestACompletionAloneSealsNothing(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), nonce, 0)

	if rv := s.RendezvousFor(nonce).State; rv != RendezvousAwaitingSighting {
		t.Fatalf("a completion with no sighting leaves the meeting %s, want awaiting-sighting", rendezvousStateName(rv))
	}
	if recs := s.Observations(); len(recs) != 0 {
		t.Fatalf("a completion with no sighting sealed %d records, want none: the boundary's screen is taken at the sighting", len(recs))
	}
	if _, ok := s.ObservationFor(nonce); ok {
		t.Fatal("a record is keyed by a nonce whose sighting never arrived")
	}
	if ends := settledEnds(rs); len(ends) != 0 {
		t.Fatalf("a completion with no sighting emitted %d end markers, want none", len(ends))
	}

	// The sighting is the join, and the join is the seal: the count at the
	// sighting, and the screen the fence is sitting on. It arrives here the
	// way it really arrives — on the ordered stream, as the shell writes it
	// (fenceSeq) — so the boundary is the stream's own and not a contract
	// call's.
	if err := s.Ingest([]byte(fenceSeq(fenceNonceHex))); err != nil {
		t.Fatalf("the shell's own fence byte sequence: %v", err)
	}
	atSighting := s.departedRows

	rec, ok := s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the sighting that joined the meeting sealed no record")
	}
	if len(rec.Closing.Lines) == 0 {
		t.Fatal("the record sealed at the sighting carries no closing screen")
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("the joined boundary reads back %v, want complete", rec.Completeness)
	}
	end, ok := settledEnds(rs)[nonce]
	if !ok {
		t.Fatal("the joined boundary emitted no end marker")
	}
	if end.endRow != atSighting {
		t.Fatalf("the end marker stops at row %d, want the %d the sighting measured", end.endRow, atSighting)
	}
	if len(end.closing) == 0 {
		t.Fatal("the end marker carries no closing screen for a boundary that was sighted")
	}
}

// TestAParkedIntervalIsSettledByTheNextIntervalsStart: the fence's sighting
// never arrives for A; the next command's own fence is sighted instead. That
// sighting is A's settle — the record seals with no closing screen at the
// count A's completion measured, the meeting reads expired, and the claim
// about the stream goes no-fence — and the next interval then proceeds
// untouched.
func TestAParkedIntervalIsSettledByTheNextIntervalsStart(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	parked, next := obsNonce(1), obsNonce(2)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), parked, 0)
	atCompletion := s.departedRows

	// The command that follows prints while A's fence is still missing: those
	// rows belong to the interval that follows the unseen boundary.
	obsFeed(t, s, 30, 30)

	if err := s.SightFence(next, []byte("$ ")); err != nil {
		t.Fatalf("sight the next interval's own fence: %v", err)
	}

	rec, ok := s.ObservationFor(parked)
	if !ok {
		t.Fatal("the settled interval sealed no record: a parked record must be sealed by the settle")
	}
	if len(rec.Closing.Lines) != 0 {
		t.Fatalf("the settled record carries %d closing rows, want none: its boundary was never seen", len(rec.Closing.Lines))
	}
	if rec.Completeness != CompletenessNoFence {
		t.Fatalf("the settled record reads back %v, want no-fence: its authenticated boundary went unmet", rec.Completeness)
	}
	end, ok := settledEnds(rs)[parked]
	if !ok {
		t.Fatal("the settled interval emitted no end marker")
	}
	if end.endRow != atCompletion {
		t.Fatalf("the settled end marker stops at row %d, want the %d its completion measured", end.endRow, atCompletion)
	}
	if len(end.closing) != 0 {
		t.Fatalf("the settled end marker carries %d closing rows, want none", len(end.closing))
	}
	rv := s.RendezvousFor(parked)
	if rv.State != RendezvousExpired {
		t.Fatalf("the settled meeting reads %s, want expired", rendezvousStateName(rv.State))
	}
	if len(rv.PinnedSource) != 0 {
		t.Fatalf("the settled meeting still pins %q, want nothing pinned", rv.PinnedSource)
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("the session reads %v after an authenticated boundary went unmet, want no-fence", got)
	}

	// The next interval is nobody's but its own: its own sighting parked it,
	// and its completion closes it on the boundary it sighted.
	if got := s.RendezvousFor(next).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the next interval's meeting reads %s after its own sighting, want awaiting-authenticated", rendezvousStateName(got))
	}
	s.Completed(s.Incarnation(), next, 0)
	if got := s.RendezvousFor(next).State; got != RendezvousComplete {
		t.Fatalf("the next interval's meeting reads %s after its completion, want complete", rendezvousStateName(got))
	}
	if _, ok := s.ObservationFor(next); !ok {
		t.Fatal("the next interval's own boundary sealed no record")
	}
}

// TestAParkedIntervalIsSettledByASecondCompletion: the same settle, driven by
// the other event that names a later interval — an authenticated completion
// for a nonce that is not the parked one.
func TestAParkedIntervalIsSettledByASecondCompletion(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	parked, later := obsNonce(3), obsNonce(4)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), parked, 0)
	atCompletion := s.departedRows

	s.Completed(s.Incarnation(), later, 0)

	rec, ok := s.ObservationFor(parked)
	if !ok {
		t.Fatal("the settled interval sealed no record")
	}
	if len(rec.Closing.Lines) != 0 || rec.Completeness != CompletenessNoFence {
		t.Fatalf("the settled record carries %d closing rows and %v, want none and no-fence", len(rec.Closing.Lines), rec.Completeness)
	}
	end, ok := settledEnds(rs)[parked]
	if !ok || end.endRow != atCompletion || len(end.closing) != 0 {
		t.Fatalf("the settled end marker is %+v, want row %d and no closing screen", end, atCompletion)
	}
	if got := s.RendezvousFor(parked).State; got != RendezvousExpired {
		t.Fatalf("the settled meeting reads %s, want expired", rendezvousStateName(got))
	}
	// The later completion is its own interval's half, not A's.
	if got := s.RendezvousFor(later).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the later completion's meeting reads %s, want awaiting-sighting: it parked its own interval", rendezvousStateName(got))
	}
}

// TestAParkedIntervalIsSettledByTheSessionsEnd: the last event there is. A
// session that ends with an interval parked still seals its record — with no
// closing screen and no-fence — instead of leaking it.
func TestAParkedIntervalIsSettledByTheSessionsEnd(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := obsNonce(5)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), nonce, 0)
	atCompletion := s.departedRows

	if err := s.Fail("the shell is gone"); err != nil {
		t.Fatalf("end the session: %v", err)
	}

	rec, ok := s.ObservationFor(nonce)
	if !ok {
		t.Fatal("a session that ended with an interval parked sealed no record for it")
	}
	if len(rec.Closing.Lines) != 0 || rec.Completeness != CompletenessNoFence {
		t.Fatalf("the settled record carries %d closing rows and %v, want none and no-fence", len(rec.Closing.Lines), rec.Completeness)
	}
	end, ok := settledEnds(rs)[nonce]
	if !ok || end.endRow != atCompletion || len(end.closing) != 0 {
		t.Fatalf("the settled end marker is %+v, want row %d and no closing screen", end, atCompletion)
	}
}

// TestTheContractsOwnCallSettlesAParkedInterval: [Session.ExpireRendezvous]
// is kept as the deterministic way a schedule settles a meeting with one half
// missing — a call, never a wait — and it settles exactly what the events do:
// the parked record seals with no closing screen, the meeting expires and the
// claim about the stream goes no-fence.
func TestTheContractsOwnCallSettlesAParkedInterval(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := obsNonce(6)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), nonce, 0)
	atCompletion := s.departedRows

	if err := s.ExpireRendezvous(nonce); err != nil {
		t.Fatalf("settle the meeting whose fence never arrived: %v", err)
	}

	rec, ok := s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the settled interval sealed no record")
	}
	if len(rec.Closing.Lines) != 0 || rec.Completeness != CompletenessNoFence {
		t.Fatalf("the settled record carries %d closing rows and %v, want none and no-fence", len(rec.Closing.Lines), rec.Completeness)
	}
	end, ok := settledEnds(rs)[nonce]
	if !ok || end.endRow != atCompletion || len(end.closing) != 0 {
		t.Fatalf("the settled end marker is %+v, want row %d and no closing screen", end, atCompletion)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousExpired {
		t.Fatalf("the settled meeting reads %s, want expired", rendezvousStateName(got))
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("the session reads %v, want no-fence: an authenticated boundary went unmet", got)
	}
	if err := s.ExpireRendezvous(nonce); err != ErrNoRendezvous {
		t.Fatalf("settling an already settled meeting returned %v, want %v", err, ErrNoRendezvous)
	}
}

// TestASightingNobodyAuthenticatedSettlesWithoutASeal: the other pending
// state, unchanged. A fence with nothing authenticated behind it authorised
// nothing (ADR-0024 decision 1), so settling it drops the pin and RETURNS the
// capture it took — the split un-does itself and the interval in flight
// resumes its own opening — and it changes nothing else: nothing is sealed,
// and the session\'s claim about its own stream is untouched, so
// unauthenticated output can never revoke the person\'s ability to write.
func TestASightingNobodyAuthenticatedSettlesWithoutASeal(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := obsNonce(7)

	obsFeed(t, s, 0, 30)
	opened, ok := s.OpenObservation()
	if !ok {
		t.Fatal("the interval in flight has no record")
	}

	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence nobody authenticated: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the sighting with nothing behind it reads %s, want awaiting-authenticated", rendezvousStateName(got))
	}
	rebased, ok := s.OpenObservation()
	if !ok || rebased.Opened == opened.Opened {
		t.Fatalf("the parking sighting did not rebase the interval in flight: it opened at %d, then %d", opened.Opened, rebased.Opened)
	}

	if err := s.ExpireRendezvous(nonce); err != nil {
		t.Fatalf("settle the sighting nobody authenticated: %v", err)
	}

	rv := s.RendezvousFor(nonce)
	if rv.State != RendezvousExpired {
		t.Fatalf("the settled sighting reads %s, want expired", rendezvousStateName(rv.State))
	}
	if len(rv.PinnedSource) != 0 {
		t.Fatalf("the settled sighting still pins %q, want nothing pinned", rv.PinnedSource)
	}
	back, ok := s.OpenObservation()
	if !ok || back.Opened != opened.Opened {
		t.Fatalf("the settled sighting did not return its capture: the interval in flight opens at %d, want its original %d", back.Opened, opened.Opened)
	}
	if recs := s.Observations(); len(recs) != 0 {
		t.Fatalf("a sighting nobody authenticated sealed %d records, want none", len(recs))
	}
	if ends := settledEnds(rs); len(ends) != 0 {
		t.Fatalf("a sighting nobody authenticated emitted %d end markers, want none", len(ends))
	}
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("the session reads %v after an unauthenticated sighting was settled, want complete: it authorised nothing", got)
	}
}

// TestTheBoundNeverLeavesAParkedRecordUnsealed: the rendezvous set's bound
// recycles a slot when it is full (design §6.4), and the interval parked on
// the meeting that goes must not be left with a record nothing can seal. The
// events settle it on the way — the completion that parks the NEWEST meeting
// settles the one parked before it — so by the time the bound drops a meeting
// its record is already sealed, at the count its own completion measured.
func TestTheBoundNeverLeavesAParkedRecordUnsealed(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 30)
	// Non-zero nonces: the all-zero FenceNonce is the runtime's own "no
	// interval is parked" sentinel, not a meeting.
	oldest := obsNonce(0xB0)
	s.Completed(s.Incarnation(), oldest, 0)
	atCompletion := s.departedRows
	for i := 1; i < MaxPendingRendezvous; i++ {
		s.Completed(s.Incarnation(), obsNonce(byte(0xB0+i)), 0)
	}
	if got := len(s.rendezvous); got != MaxPendingRendezvous {
		t.Fatalf("the set holds %d meetings, want the bound %d", got, MaxPendingRendezvous)
	}

	// The ninth completion finds every incumbent authenticated: the slot it
	// takes is the oldest meeting the events had ALREADY settled — and by then
	// its interval's record is sealed, which is the point.
	s.Completed(s.Incarnation(), obsNonce(0xFF), 0)

	if got := s.RendezvousFor(oldest).State; got != RendezvousIdle {
		t.Fatalf("the meeting the bound recycled is %s, want idle", rendezvousStateName(got))
	}
	rec, ok := s.ObservationFor(oldest)
	if !ok {
		t.Fatal("the meeting the bound recycled left the interval parked on it unsealed")
	}
	if len(rec.Closing.Lines) != 0 || rec.Completeness != CompletenessNoFence {
		t.Fatalf("the settled record carries %d closing rows and %v, want none and no-fence", len(rec.Closing.Lines), rec.Completeness)
	}
	end, ok := settledEnds(rs)[oldest]
	if !ok || end.endRow != atCompletion || len(end.closing) != 0 {
		t.Fatalf("the settled end marker is %+v, want row %d and no closing screen", end, atCompletion)
	}
	// Every completion whose fence never arrived left a record — the bound
	// recycled a slot, not a record — and the interval in flight is parked on
	// the newest of them.
	if got := len(s.Observations()); got != MaxPendingRendezvous {
		t.Fatalf("%d records were sealed, want one per settled completion (%d)", got, MaxPendingRendezvous)
	}
	if s.observation == nil || s.observation.Nonce != obsNonce(0xFF) {
		t.Fatal("the interval in flight is not parked on the newest completion")
	}
}
