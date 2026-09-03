package transport

// A hole the EXECUTION HOST left is recorded as a hole, in the coordinate the
// client already speaks, and never as bytes (nocx-k6p18.25).
//
// The helper owns a bounded output window on the far machine. When a
// coordinator falls behind it the window is reclaimed past that reader and
// the helper says so — AD-9's explicit reset — and the bytes in between
// reached nobody: they were never carried across the wire, so no retention
// bound here touched them and no recorder here was missing.
//
// The first shape of this fix spliced a line of nocx's own text into the
// stream to say so. These tests are the second shape, and they assert the
// difference: the hole is a Gap with its own bounds and its own reason, the
// runs on either side of it are the shell's bytes and nothing else, and the
// offsets after it still name the stream the session produced.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// The recorder's own end of it: the shipped loop over a ring that has been
// told a stretch never arrived. Nothing here waits on a duration — the ring
// is closed before the loop runs, so it drains what is there and returns.
func TestRecorderRecordsTheHostWindowHoleWithItsOwnBoundsAndReason(t *testing.T) {
	before, after := []byte("the shell printed this"), []byte("and this, past the hole")
	const lost = 40960

	ring := newOutputRing()
	if err := ring.write(before); err != nil {
		t.Fatalf("write the bytes before the hole: %v", err)
	}
	ring.hole(lost, content.GapReasonHostWindow)
	if err := ring.write(after); err != nil {
		t.Fatalf("write the bytes after the hole: %v", err)
	}
	ring.close()

	rec := newFakeRecorder()
	ws := recorderServer(t, rec)
	ws.recordSessionOutput(context.Background(), session.ID("sid-host-window"), ring)

	skips := rec.skipCalls()
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want exactly one across the host's window", skips)
	}
	if want := uint64(len(before)) + lost; skips[0].resumeAt != want {
		t.Fatalf("skipped to %d, want %d — the far end of what was lost", skips[0].resumeAt, want)
	}
	if skips[0].reason != content.GapReasonHostWindow {
		t.Fatalf("skip reason = %q, want %q: nobody here failed to record these bytes and no bound here dropped them",
			skips[0].reason, content.GapReasonHostWindow)
	}
	// The bytes, and only the bytes: a coordinator that wrote its own
	// sentence into the stream would have it here, in the recording, for as
	// long as the recording lives.
	if got, want := string(rec.recorded()), string(before)+string(after); got != want {
		t.Fatalf("recorded %q, want %q", got, want)
	}
}

// The ring is the owner of the coordinate every cursor in the system is
// measured against, so a hole has to live in it: `written` counts the missing
// bytes, the bytes after the hole keep the offsets the session gave them, and
// a consumer is never handed two non-adjacent stretches as one run.
func TestRingHoleKeepsTheStreamsCoordinate(t *testing.T) {
	ring := newOutputRing()
	if err := ring.write([]byte("first")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ring.hole(1000, content.GapReasonHostWindow)
	if err := ring.write([]byte("second")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got, want := ring.writtenLocked(), uint64(5+1000+6); got != want {
		t.Fatalf("written = %d, want %d: the stream produced the bytes nobody carried across", got, want)
	}
	data, _, needsReset, hole := ring.snapshot(0)
	if needsReset || hole != nil {
		t.Fatalf("snapshot(0) = reset %v hole %+v, want the first run", needsReset, hole)
	}
	if string(data) != "first" {
		t.Fatalf("snapshot(0) = %q, want %q: a run may never cross a hole", data, "first")
	}
	_, from, _, hole := ring.snapshot(5)
	if hole == nil {
		t.Fatal("snapshot at the hole reported no hole; a consumer would splice the two stretches")
	}
	if hole.n != 1000 || hole.reason != content.GapReasonHostWindow {
		t.Fatalf("hole = %+v, want 1000 bytes with the host-window reason", *hole)
	}
	if from != 1005 {
		t.Fatalf("the stream resumes at %d, want 1005", from)
	}
	data, _, _, hole = ring.snapshot(1005)
	if hole != nil || string(data) != "second" {
		t.Fatalf("after the hole: data %q hole %+v, want the second run at its own offset", data, hole)
	}
}

// A hole is bytes nobody can ever ack, so it may not be charged to the
// credit window (AD-10). Charging it parks the subscriber's pump on a window
// that can never reopen — the tab goes silent for good the first time a host
// loses more than CreditLimit — which is the deadlock this asserts is not
// there.
func TestRingHoleIsNotChargedToTheCreditWindow(t *testing.T) {
	ring := newOutputRing()
	if err := ring.write([]byte("sent")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ring.hole(4*CreditLimit, content.GapReasonHostWindow)
	if err := ring.write([]byte("also sent")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The client has acked everything it was actually sent before the hole.
	if err := ring.ack(4); err != nil {
		t.Fatalf("ack: %v", err)
	}
	done := make(chan bool, 1)
	go func() { done <- ring.waitForCredit(context.Background(), 0, ring.writtenLocked(), CreditLimit) }()
	waittest.WaitForTimeout(t, "the credit window to stay open across a hole", wantWithin, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

// A hole leaves the ring the way a byte does — when every consumer has passed
// it — and not before. The base may never come to rest inside one: an offset
// in the middle of a stretch that never arrived names no byte, and a ring
// whose base named no byte would answer for it.
func TestRingHoleLeavesTheRingWhenItsConsumersHavePassedIt(t *testing.T) {
	ring := newOutputRing()
	if err := ring.write([]byte("first")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ring.hole(1000, content.GapReasonHostWindow)
	if err := ring.write([]byte("second")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// An ack that lands INSIDE the hole frees the bytes before it and no
	// more: the hole is still owed to whoever has not crossed it.
	if err := ring.ack(500); err != nil {
		t.Fatalf("ack inside the hole: %v", err)
	}
	if got := ring.oldestLocked(); got != 5 {
		t.Fatalf("base = %d after an ack inside the hole, want 5 — the hole's own start", got)
	}
	if _, _, _, hole := ring.snapshot(5); hole == nil {
		t.Fatal("the hole was dropped while a consumer was still short of it")
	}
	// And an ack past it takes the whole thing.
	if err := ring.ack(1005); err != nil {
		t.Fatalf("ack past the hole: %v", err)
	}
	if got := ring.oldestLocked(); got != 1005 {
		t.Fatalf("base = %d once the hole was passed, want 1005", got)
	}
	data, _, needsReset, hole := ring.snapshot(1005)
	if needsReset || hole != nil || string(data) != "second" {
		t.Fatalf("after the hole left the ring: data %q reset %v hole %+v", data, needsReset, hole)
	}
	if got := ring.writtenLocked(); got != 1011 {
		t.Fatalf("written = %d, want 1011: what the session produced does not change when the ring forgets it", got)
	}
}

// And the product's own answer: a client reading the recording back is told
// where the hole is, how wide it is and who took the bytes — and the runs on
// either side are the session's own output, byte for byte.
func TestSessionOutputReportsTheHostWindowHole(t *testing.T) {
	const lost = uint64(4096)
	before, after := recordingStream(2048), recordingStream(3072)
	// The three offsets this test is about, named once: where the hole
	// starts, where it ends, and what the session has produced by the end.
	holeAt, resumeAt := uint64(len(before)), uint64(len(before))+lost
	produced := resumeAt + uint64(len(after))
	db := openRecordingStore(t, 1<<20)

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	awaitPush(t, "the output before the hole", pushOutput(term, before))
	awaitProduced(t, db, sid, holeAt)

	// The helper's reset, at the point in the stream it happened: this is
	// what internal/helper/client's OnOutputHole delivers on the pump's own
	// goroutine, and internal/app hands to the ring through
	// HostedSessionOpen.ObserveOutputHoles.
	ws.getRx(session.ID(sid)).ring.hole(lost, content.GapReasonHostWindow)

	awaitPush(t, "the output after the hole", pushOutput(term, after))
	awaitProduced(t, db, sid, produced)

	got := mustReadSessionOutput(t, conn, 42, map[string]any{"sessionId": sid, "from": 0})
	if len(got.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly the hole the host's window left", got.Gaps)
	}
	if got.Gaps[0].Start != holeAt || got.Gaps[0].End != resumeAt {
		t.Fatalf("the hole is reported as [%d,%d), want [%d,%d)",
			got.Gaps[0].Start, got.Gaps[0].End, holeAt, resumeAt)
	}
	if got.Gaps[0].Reason != content.GapReasonHostWindow {
		t.Fatalf("the hole reads back as %q, want %q — the retention bound never had these bytes and no recorder was missing",
			got.Gaps[0].Reason, content.GapReasonHostWindow)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("runs = %d, want two: the recording may not join two stretches that are not adjacent", len(got.Runs))
	}
	if got.Runs[0].Offset != 0 || !bytesEqual(got.bodyAt(t, 0), before) {
		t.Fatalf("the run before the hole is not the session's own output (offset %d, %d bytes)",
			got.Runs[0].Offset, len(got.bodyAt(t, 0)))
	}
	if got.Runs[1].Offset != resumeAt || !bytesEqual(got.bodyAt(t, 1), after) {
		t.Fatalf("the run after the hole is at %d and is not the session's own output; want offset %d and %d bytes",
			got.Runs[1].Offset, resumeAt, len(after))
	}
}

// The one place the host's vocabulary and the store's meet. A generation that
// names a cause this build has never heard of must reach a person as "we do
// not know" rather than as this build's guess at what it meant — which is
// what proto.GapReasonWindow's own doc requires of a coordinator.
func TestTheHostsWordForAHoleIsTranslatedOnceAndNeverGuessed(t *testing.T) {
	if got := sessionOutputHoleReason(proto.GapReasonWindow); got != content.GapReasonHostWindow {
		t.Fatalf("the host's window reads back as %q, want %q", got, content.GapReasonHostWindow)
	}
	if got := sessionOutputHoleReason("a cause a later generation invented"); got != content.GapReasonUnknown {
		t.Fatalf("an unrecognised cause reads back as %q, want %q: this build may not say what it does not know",
			got, content.GapReasonUnknown)
	}
}

// bytesEqual keeps the assertions above readable; a mismatch is reported by
// the caller in its own words rather than as a wall of bytes.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
