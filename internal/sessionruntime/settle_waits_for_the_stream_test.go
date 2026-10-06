package sessionruntime

// nocx-n5ent: a settle may not freeze an interval whose rows are still in
// flight (ADR-0074 case 3, amended).
//
// ADR-0074 case 3 settles a parked interval "by the next event that proves it
// will not come: the next interval's start (a fence or a completion for
// another nonce), the session's end, or the contract's own ExpireRendezvous".
// The two carriers are ordered independently: the completion travels the
// AUTHENTICATED channel, the fence and the command's rows travel the pty. So a
// completion for another nonce is not proof that the parked interval's bytes
// have been read — the shell sends its completion BEFORE it writes its fence
// (internal/shellintegration/scripts/nocx.bash), and the pty can still hold a
// whole drain's worth of the parked command's own output.
//
// Settling there freezes the interval at the count measured on the fast
// channel, takes the interval in flight away from its own rows, and hands
// everything still unread to a next interval that never comes: measured on the
// runner, a 5000-row command's block sealed at row 3843 with no closing screen
// (truncated=gap, endRow == cursor) and its remaining 1157 rows were stored
// nowhere.
//
// The amended rule: an event on the authenticated channel DEFERS the settle.
// The interval stays in flight, keeps its own rows, and is sealed either by its
// own fence's sighting — the byte stream's own proof that its output is over,
// with the screen the fence sat on — or, when the fence truly never comes, by
// the stream's NEXT boundary (a later fence) or the session's end, with no
// closing screen as before.

import (
	"encoding/hex"
	"testing"
)

// nonceHex is the wire spelling of a fence nonce, as the shell writes it into
// the pty.
func nonceHex(n FenceNonce) string { return hex.EncodeToString(n[:]) }

// endBefore is the first end marker the stream carried for the nonce, if any.
func endBefore(rs *recordingRowStream, nonce FenceNonce) (rowEvent, bool) {
	for _, e := range rs.snapshot() {
		if e.kind == "end" && e.nonce == nonce {
			return e, true
		}
	}
	return rowEvent{}, false
}

// streamedRows is how many rows the runtime had handed to the stream in total:
// what a settled end marker may not be behind.
func streamedRows(rs *recordingRowStream) uint64 {
	total := uint64(0)
	for _, e := range rs.snapshot() {
		total += uint64(len(e.rows)) // #nosec G115 -- a row count
	}
	return total
}

// TestASettleOnTheAuthenticatedChannelWaitsForTheIntervalsOwnRows is the red
// case: the command's completion parks its interval, the pty has not yet handed
// the runtime the rest of the command's output, and a completion for ANOTHER
// nonce arrives on the authenticated channel. That event must not seal the
// parked interval at the stale count.
func TestASettleOnTheAuthenticatedChannelWaitsForTheIntervalsOwnRows(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	command, later := obsNonce(0x11), obsNonce(0x22)

	// The command prints a prefix and its authenticated completion arrives
	// over the channel the pty is not ordered against. The interval parks at
	// what has streamed so far; the rest of the command's output is still in
	// the pty, unread.
	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), command, 0)
	atCompletion := s.DepartedRowCount()

	// The next command's completion reaches the runtime while the pty is
	// still behind — exactly the order the runner's log shows, where the
	// seal and the completion envelope land in the same millisecond and the
	// helper is handed the completion after it.
	s.Completed(s.Incarnation(), later, 0)

	// THE INVARIANT: nothing may seal the interval here. Its rows are still
	// in the pty, and an end marker emitted now is a boundary that leaves
	// them outside every interval.
	if end, frozen := endBefore(rs, command); frozen {
		t.Fatalf("a completion for another nonce sealed the interval at row %d (closing rows %d, noFence %v) while the pty still owed the command's rows: the settle must wait for the stream",
			end.endRow, len(end.closing), end.noFence)
	}
	if end, frozen := endBefore(rs, later); frozen {
		t.Fatalf("the later completion's own interval was sealed at row %d before its fence was sighted", end.endRow)
	}

	// The pty catches up: the REST OF THE SAME COMMAND departs. Those rows are
	// the parked interval's own, and they stream while it is still in flight.
	obsFeed(t, s, 30, 20)

	// The command's own fence is finally read off the pty. That is the byte
	// stream's proof that its output is over, and it is the boundary: the
	// interval seals on the screen the fence sat on, at the count at the
	// sighting.
	if err := s.Ingest([]byte(fenceSeq(nonceHex(command)))); err != nil {
		t.Fatalf("the shell's own fence byte sequence: %v", err)
	}

	end, ok := endBefore(rs, command)
	if !ok {
		t.Fatal("the command's own fence did not seal its interval")
	}
	if want := streamedRows(rs); end.endRow != want {
		t.Fatalf("the sealed end marker stops at row %d while the interval had streamed %d rows: the command's tail was left outside its own block", end.endRow, want)
	}
	if end.endRow <= atCompletion {
		t.Fatalf("the sealed end marker stops at row %d, no further than the %d the completion measured: the rows the pty still owed are outside the block", end.endRow, atCompletion)
	}
	if len(end.closing) == 0 {
		t.Fatal("the end marker carries no closing screen for a boundary that WAS sighted")
	}
	if end.noFence {
		t.Fatal("the end marker says no-fence for a boundary whose fence was sighted")
	}
	if got := s.RendezvousFor(command).State; got != RendezvousComplete {
		t.Fatalf("the command's meeting reads %s after its own fence was sighted, want complete", rendezvousStateName(got))
	}
}

// TestADeferredSettleIsFlushedByTheStreamsNextBoundary is the other half: when
// the deferred interval's own fence truly never comes, the settle still
// happens — at the byte stream's next boundary, where the rows are in, with no
// closing screen and no-fence, exactly as ADR-0074 case 3 records.
func TestADeferredSettleIsFlushedByTheStreamsNextBoundary(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	lost, next := obsNonce(0x33), obsNonce(0x44)

	obsFeed(t, s, 0, 40)
	s.Completed(s.Incarnation(), lost, 0)
	atCompletion := s.DepartedRowCount()

	// The next command's completion defers the settle; its own output then
	// departs, and those rows are still the parked interval's.
	s.Completed(s.Incarnation(), next, 0)
	obsFeed(t, s, 40, 10)

	// The next command's FENCE — a byte-stream event — is the boundary the
	// settle waits for.
	if err := s.SightFence(next, []byte("$ ")); err != nil {
		t.Fatalf("sight the next interval's own fence: %v", err)
	}

	end, ok := endBefore(rs, lost)
	if !ok {
		t.Fatal("the deferred settle was never flushed: a parked interval must not leak")
	}
	if end.endRow <= atCompletion {
		t.Fatalf("the flushed end marker stops at row %d, behind the %d the stream had carried: the rows the pty owed are outside the block", end.endRow, atCompletion)
	}
	if got := streamedRows(rs); end.endRow > got {
		t.Fatalf("the flushed end marker stops at row %d, past the %d the stream had carried", end.endRow, got)
	}
	if len(end.closing) != 0 {
		t.Fatalf("the flushed end marker carries %d closing rows, want none: its boundary was never seen", len(end.closing))
	}
	if !end.noFence {
		t.Fatal("the flushed end marker does not say no-fence")
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("the session reads %v after an authenticated boundary went unmet, want no-fence", got)
	}
	if got := s.RendezvousFor(lost).State; got != RendezvousExpired {
		t.Fatalf("the settled meeting reads %s, want expired", rendezvousStateName(got))
	}
}

// TestADeferredSettleStillHoldsWhenTheSessionEnds: the last event there is. A
// session that ends with a deferred settle outstanding seals the interval it
// was holding, at what had streamed, with no closing screen.
func TestADeferredSettleStillHoldsWhenTheSessionEnds(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	command, later := obsNonce(0x55), obsNonce(0x66)

	obsFeed(t, s, 0, 25)
	s.Completed(s.Incarnation(), command, 0)
	s.Completed(s.Incarnation(), later, 0)
	obsFeed(t, s, 25, 15)

	if err := s.Fail("the shell is gone"); err != nil {
		t.Fatalf("end the session: %v", err)
	}

	end, ok := endBefore(rs, command)
	if !ok {
		t.Fatal("a session that ended with a deferred settle emitted no end marker: the parked interval leaked")
	}
	if want := streamedRows(rs); end.endRow != want {
		t.Fatalf("the flushed end marker stops at row %d while the interval had streamed %d rows", end.endRow, want)
	}
	if len(end.closing) != 0 || !end.noFence {
		t.Fatalf("the flushed end marker is %+v, want no closing screen and no-fence", end)
	}
}
