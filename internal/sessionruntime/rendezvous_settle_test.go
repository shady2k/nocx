package sessionruntime

import "testing"

// Settling a boundary by an EVENT, never by a timer (nocx-2v80t.3.9,
// REVIEW-4). The closing screen is the one taken at the fence's SIGHTING —
// the half that arrives in the ordered stream — so a completion that wins the
// race by whole feeds waits for its sighting and reads nothing at the
// completion. When the sighting never arrives at all, the interval is settled
// by the next event there is: the next interval's start (a fence or a
// completion for ANOTHER nonce) or the session's end. The settle seals the
// parked interval with NO closing screen, at the row count the completion
// measured, and says so — the meeting reads [RendezvousExpired], the end
// marker carries settledWithoutFence, and the claim about the stream goes
// [CompletenessNoFence].
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
	if ends := settledEnds(rs); len(ends) != 0 {
		t.Fatalf("a completion with no sighting emitted %d end markers, want none: the boundary's screen is taken at the sighting", len(ends))
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

	end, ok := settledEnds(rs)[nonce]
	if !ok {
		t.Fatal("the sighting that joined the meeting emitted no end marker")
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

	end, ok := settledEnds(rs)[parked]
	if !ok {
		t.Fatal("the settled interval emitted no end marker: a parked interval must be sealed by the settle")
	}
	if len(end.closing) != 0 {
		t.Fatalf("the settled end marker carries %d closing rows, want none: its boundary was never seen", len(end.closing))
	}
	// The parked count is where the completion measured the boundary, and the
	// interval kept streaming after it: the end marker is never behind what the
	// interval already handed over, or the block's closing append lands behind
	// its own cursor and the block never freezes. It carries the count at the
	// settle, which can only be more than the completion's.
	if end.endRow < atCompletion {
		t.Fatalf("the settled end marker stops at row %d, behind the %d its completion measured", end.endRow, atCompletion)
	}
	if want := streamedRowsBefore(rs); end.endRow != want {
		t.Fatalf("the settled end marker stops at row %d while the interval had already streamed %d rows", end.endRow, want)
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
	if _, ok := settledEnds(rs)[next]; !ok {
		t.Fatal("the next interval's own boundary emitted no end marker")
	}
}

// TestAParkedIntervalIsSettledAtTheStreamsNextBoundaryAfterASecondCompletion:
// the settle a completion for ANOTHER nonce asks for, taken where it can be
// taken without losing the command's own rows (nocx-n5ent, the ADR-0074 case 3
// amendment).
//
// The completion arrives over the authenticated channel, and this carrier
// cannot tell "the parked interval's fence has not been READ yet" from "it was
// never written" — the shell sends its completion BEFORE it writes its fence,
// so the completion that says a later command finished can arrive while the
// pty still holds the earlier command's whole drain. Taking the settle there
// froze the interval at a count the byte stream had not reached and left
// everything after it outside every interval: measured on the runner, 5000
// rows printed and 3843 stored. So the completion DEFERS, and the settle is
// taken at the byte stream's next boundary — the same marker, later, with the
// rows it covers.
func TestAParkedIntervalIsSettledAtTheStreamsNextBoundaryAfterASecondCompletion(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	parked, later := obsNonce(3), obsNonce(4)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), parked, 0)
	atCompletion := s.departedRows

	s.Completed(s.Incarnation(), later, 0)

	// THE DEFERRAL: nothing is sealed, and the interval in flight is still the
	// parked one, so the rows the pty has not yet handed over remain its own.
	if _, early := settledEnds(rs)[parked]; early {
		t.Fatalf("a completion for another nonce sealed the parked interval at row %d before the stream reached its next boundary", atCompletion)
	}
	if s.observation == nil || s.observation.Nonce != parked {
		t.Fatal("the second completion took the interval in flight away from the parked nonce, and with it the rows still to come")
	}
	// The later completion is its own interval's half, not A's: it is in the
	// set waiting for its own fence.
	if got := s.RendezvousFor(later).State; got != RendezvousAwaitingSighting {
		t.Fatalf("the later completion's meeting reads %s, want awaiting-sighting", rendezvousStateName(got))
	}

	// The stream catches up, then reaches the later command's own boundary.
	obsFeed(t, s, 30, 40)
	if err := s.SightFence(later, []byte("$ ")); err != nil {
		t.Fatalf("sight the next interval's own fence: %v", err)
	}

	end, ok := settledEnds(rs)[parked]
	if !ok || end.endRow < atCompletion || len(end.closing) != 0 || !end.noFence {
		t.Fatalf("the settled end marker is %+v, want no less than row %d, no closing screen, and no-fence", end, atCompletion)
	}
	// The marker names the rows the stream had reached: the interval kept
	// them, and they are inside its block rather than outside every block.
	if want := streamedRowsBefore(rs); end.endRow != want {
		t.Fatalf("the settled end marker stops at row %d, want the %d rows the stream had carried", end.endRow, want)
	}
	if end.endRow <= atCompletion {
		t.Fatalf("the settled end marker stops at row %d, no further than the %d the completion measured: the rows the pty still owed are outside the block", end.endRow, atCompletion)
	}
	if got := s.RendezvousFor(parked).State; got != RendezvousExpired {
		t.Fatalf("the settled meeting reads %s, want expired", rendezvousStateName(got))
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

	end, ok := settledEnds(rs)[nonce]
	if !ok || end.endRow < atCompletion || len(end.closing) != 0 || !end.noFence {
		t.Fatalf("a session that ended with an interval parked emitted %+v, want no less than row %d, no closing screen, and no-fence", end, atCompletion)
	}
	if want := streamedRowsBefore(rs); end.endRow != want {
		t.Fatalf("the settled end marker stops at row %d while the interval had already streamed %d rows", end.endRow, want)
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

	end, ok := settledEnds(rs)[nonce]
	if !ok || end.endRow < atCompletion || len(end.closing) != 0 || !end.noFence {
		t.Fatalf("the settled end marker is %+v, want no less than row %d, no closing screen, and no-fence", end, atCompletion)
	}
	if want := streamedRowsBefore(rs); end.endRow != want {
		t.Fatalf("the settled end marker stops at row %d while the interval had already streamed %d rows", end.endRow, want)
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

	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence nobody authenticated: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the sighting with nothing behind it reads %s, want awaiting-authenticated", rendezvousStateName(got))
	}
	if o := s.observation; o == nil || o.Rebased != nonce {
		t.Fatalf("the parking sighting did not rebase the interval in flight to its fence, want Rebased=%v", nonce)
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
	if o := s.observation; o == nil || o.Rebased != (FenceNonce{}) {
		t.Fatalf("the settled sighting did not return its capture: the interval in flight is still rebased to %v", o.Rebased)
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
// eviction DEFERS the settle rather than taking it (nocx-n5ent): spending a
// slot is not evidence about the byte stream, so the record is sealed at the
// stream's next boundary, at the count the stream had then reached — and never
// left unsealed.
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
	// takes is the oldest meeting. The record that meeting parked is DEFERRED
	// there — the interval in flight is still the oldest one's, holding the
	// rows the stream has not handed over yet — and the newest completion's
	// own half waits in the set for its own fence.
	s.Completed(s.Incarnation(), obsNonce(0xFF), 0)

	if got := s.RendezvousFor(oldest).State; got != RendezvousIdle {
		t.Fatalf("the meeting the bound recycled is %s, want idle", rendezvousStateName(got))
	}
	if _, early := settledEnds(rs)[oldest]; early {
		t.Fatalf("the bound sealed the recycled meeting's record at row %d before the stream reached its next boundary", atCompletion)
	}
	if s.observation == nil || s.observation.Nonce != oldest {
		t.Fatal("the interval in flight is no longer the recycled meeting's: its rows went with it")
	}

	// The stream reaches its next boundary: the newest command's own fence is
	// sighted. The deferred record is sealed there, and the newest interval
	// joins its own fence.
	obsFeed(t, s, 30, 25)
	if err := s.SightFence(obsNonce(0xFF), []byte("$ ")); err != nil {
		t.Fatalf("sight the newest interval's fence: %v", err)
	}

	end, ok := settledEnds(rs)[oldest]
	if !ok || end.endRow < atCompletion || len(end.closing) != 0 || !end.noFence {
		t.Fatalf("the settled end marker is %+v, want no less than row %d, no closing screen, and no-fence", end, atCompletion)
	}
	if want := streamedRowsBefore(rs); end.endRow != want {
		t.Fatalf("the settled end marker stops at row %d while the interval had already streamed %d rows", end.endRow, want)
	}
	if ends := endRowsInOrder(rs); len(ends) != 2 {
		t.Fatalf("%d end markers were emitted, want the flushed record's and the newest interval's own (%d)", len(ends), 2)
	}
}

// streamedRowsBefore is how many rows the runtime had handed to the stream
// before the FIRST end marker: what that marker may not be behind.
func streamedRowsBefore(rs *recordingRowStream) uint64 {
	total := uint64(0)
	for _, ev := range rs.snapshot() {
		if ev.kind == "end" {
			break
		}
		total += uint64(len(ev.rows)) // #nosec G115 -- a row count
	}
	return total
}

// endRowsInOrder is every end marker's endRow, in arrival order.
func endRowsInOrder(rs *recordingRowStream) []uint64 {
	var out []uint64
	for _, ev := range rs.snapshot() {
		if ev.kind == "end" {
			out = append(out, ev.endRow)
		}
	}
	return out
}

// TestAStaleCompletionNeverParksOnAnotherFencesInterval: command A's fence is
// undecodable, so nothing ever sights it; command B's fence is sighted and its
// sighting takes the boundary and rebases the interval in flight onto B. A's
// authenticated half arrives afterwards and is OLDER than the interval it
// would park on: parking it there gives one record two overlapping row spans
// and emits an end marker behind rows the interval already streamed
// (nocx-2v80t.3.9).
func TestAStaleCompletionNeverParksOnAnotherFencesInterval(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	stale, live := obsNonce(1), obsNonce(2)

	obsFeed(t, s, 0, 30)
	// A's fence, in the shell's own sequence but with a body nothing can
	// decode: the sighting is dropped and A's boundary is never seen.
	if err := s.Ingest([]byte("\x1b]1337;NOCX_FENCE;not-a-nonce\x07")); err != nil {
		t.Fatalf("feed A's undecodable fence: %v", err)
	}
	if got := s.RendezvousFor(stale).State; got != RendezvousIdle {
		t.Fatalf("an undecodable fence left a meeting %s, want idle", rendezvousStateName(got))
	}

	// B prints, and B's fence is sighted: that sighting takes the boundary and
	// rebases the interval in flight onto B.
	obsFeed(t, s, 30, 30)
	s.SightFenceBoundary(t, live)

	// A's authenticated half arrives after B's sighting: it must not park.
	s.Completed(s.Incarnation(), stale, 0)
	if _, ok := settledEnds(rs)[stale]; ok {
		t.Fatal("a completion whose fence was never sighted emitted an end marker: no boundary was seen")
	}
	if got := s.observation; got != nil && got.Nonce != (FenceNonce{}) {
		t.Fatalf("a stale completion parked the interval in flight (nonce %v)", got.Nonce)
	}

	// B's own completion closes B's capture, on B's screen.
	s.Completed(s.Incarnation(), live, 0)

	// And the session's end markers never go backwards — that is what the wire
	// trace showed before this fix: two ends at one count, the second behind
	// rows the interval had already streamed.
	rows := endRowsInOrder(rs)
	if len(rows) != 1 {
		t.Fatalf("%d end markers, want one: a boundary nobody saw is never invented", len(rows))
	}
}

// TestASightingNobodyAuthenticatedReturnsItsHeldRowsWhenTheSessionFails is
// the review's finding (nocx-2v80t.3.47): a sighting authorises nothing
// (ADR-0024 decision 1), so a fence sighted before anything authenticates it
// only HOLDS the rows its window matches (suppressBoundaryScreenLocked) —
// it never drops them. Eviction (evictRendezvousLocked) and an explicit
// expiry ([Session.ExpireRendezvous]) already give a held capture back when
// the meeting they were tracking goes away; the session's own end did not —
// [Session.Fail] settled only the OTHER pending shape, a completion whose
// fence never arrived (settlePendingLocked), and never walked the
// rendezvous set for a sighting still waiting on its authenticated half. A
// forged OSC sighting followed by the PTY exiting therefore cost those rows
// with no stated loss at all, which is exactly what the ADR forbids: an
// unauthenticated fence may not cost anything.
func TestASightingNobodyAuthenticatedReturnsItsHeldRowsWhenTheSessionFails(t *testing.T) {
	// setup drives one command's output under a pane that then shrinks by a
	// row: the fence is sighted (forged — nothing has authenticated it), and
	// the shrink pushes the top row of ITS OWN closing screen off before
	// anything joins it, so the row is held rather than streamed. It answers
	// the held row's own text, so both subtests can check it lands exactly
	// once, wherever it ends up.
	setup := func(t *testing.T) (*Session, *recordingRowStream, FenceNonce, string) {
		t.Helper()
		s, rs := streamSession(t, harnessGeometry(80, 28))
		promptAndEcho(t, s)
		obsFeed(t, s, 0, 100)
		nonce := obsNonce(1)
		s.SightFenceBoundary(t, nonce)
		if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
			t.Fatalf("the forged sighting reads %s, want awaiting-authenticated", rendezvousStateName(got))
		}
		if _, err := s.CommitGeometry(harnessGeometry(80, 27)); err != nil {
			t.Fatalf("shrink the pane by one row: %v", err)
		}
		cap := s.pendingCapture
		if cap == nil || !cap.Holding || len(cap.Held) != 1 {
			t.Fatalf("the shrink did not leave exactly one row held on the forged sighting's window: %+v", cap)
		}
		held := streamRowText(cap.Held[0])
		if held == "" {
			t.Fatal("the held row carries no text this test can check for")
		}
		return s, rs, nonce, held
	}

	// The failing shape: nothing ever authenticates the sighting, and the
	// session ends. The held row must reach the stream from Fail itself —
	// there is no later event that could ever settle it otherwise.
	t.Run("the session fails before anything authenticates the sighting", func(t *testing.T) {
		s, rs, nonce, held := setup(t)
		before := len(rs.snapshot())

		if err := s.Fail("session ended"); err != nil {
			t.Fatalf("end the session: %v", err)
		}

		var streamedRows []string
		for _, e := range rs.snapshot()[before:] {
			if e.kind != "rows" {
				continue
			}
			for _, row := range e.rows {
				streamedRows = append(streamedRows, streamRowText(row))
			}
		}
		if len(streamedRows) != 1 || streamedRows[0] != held {
			t.Fatalf("failing the session streamed %v for the row a forged sighting held, want exactly [%q]: "+
				"ADR-0024 says a sighting nobody authenticated authorises nothing, so it may never cost the rows it merely located",
				streamedRows, held)
		}
		if got := s.RendezvousFor(nonce).State; got != RendezvousExpired {
			t.Fatalf("the forged sighting reads %s after the session ended, want expired", rendezvousStateName(got))
		}
		if s.pendingCapture != nil {
			t.Fatal("the session's end left a capture window still installed")
		}

		// Fail is idempotent here too: a second call finds nothing left to
		// give back and streams nothing more.
		before = len(rs.snapshot())
		if err := s.Fail("session ended again"); err != ErrUnavailable {
			t.Fatalf("a second Fail returned %v, want %v", err, ErrUnavailable)
		}
		if streamed := rs.snapshot()[before:]; len(streamed) != 0 {
			t.Fatalf("a second Fail streamed %d more emissions, want none", len(streamed))
		}
	})

	// The pair, on the path that works today: the completion authenticates
	// the sighting before the session ends. The held row is already the
	// boundary's own closing screen, carried by ITS end marker — captured at
	// the sighting, before the shrink — so it must appear there, and Fail
	// afterwards has nothing left of this meeting to give back.
	t.Run("the ordinary path: the completion authenticates the sighting before the session ends", func(t *testing.T) {
		s, rs, nonce, held := setup(t)

		s.Completed(s.Incarnation(), nonce, 0)
		if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
			t.Fatalf("the authenticated boundary reads %s, want complete", rendezvousStateName(got))
		}
		end, ok := settledEnds(rs)[nonce]
		if !ok {
			t.Fatal("the authenticated boundary emitted no end marker")
		}
		found := false
		for _, row := range end.closing {
			if streamRowText(row) == held {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the authenticated boundary's own end marker does not carry the row its window held, want %q among its closing screen", held)
		}

		before := len(rs.snapshot())
		if err := s.Fail("session ended"); err != nil {
			t.Fatalf("end the session: %v", err)
		}
		if streamed := rs.snapshot()[before:]; len(streamed) != 0 {
			t.Fatalf("ending the session after an authenticated boundary already sealed streamed %d more emissions, want none", len(streamed))
		}
	})
}
