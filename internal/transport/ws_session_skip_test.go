package transport

// A recorder that loses its place records the hole and carries on
// (nocx-k6p18.2).
//
// Before this, the `needsReset` branch of recordSessionOutput returned: the
// loop ended, the flag came down, and that session was never recorded again
// however long it went on running. That was defensible while a recorder
// could not be absent — losing your place meant a defect. Once a coordinator
// can be REPLACED under a live session it means a hole, and a hole is a fact
// about the stream rather than a reason to stop writing it down.
//
// The interval, both ends named: from the moment the recorder finds its
// cursor behind the ring's oldest byte until it has appended anything after
// the resume point, the store holds the range [pos, resumeAt) as a gap with
// its own reason, and the loop is still the ring's consumer throughout.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// ringPastItsRecorder builds a ring whose oldest retained byte is already
// past zero — the state a recorder attaching to a session that has been
// running without one finds. Nothing here waits on a duration: the ring is
// closed before the loop runs, so the loop drains what is there and returns.
func ringPastItsRecorder(t *testing.T, lost, kept []byte) *outputRing {
	t.Helper()
	ring := newOutputRing()
	if err := ring.write(lost); err != nil {
		t.Fatalf("write the bytes nobody recorded: %v", err)
	}
	// An ack with no recorder attached is what frees them: trim's floor is
	// the ack cursor while `recording` is false, which is exactly the window
	// in which a recorder can arrive late.
	if err := ring.ack(uint64(len(lost))); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := ring.write(kept); err != nil {
		t.Fatalf("write the bytes the recorder should get: %v", err)
	}
	ring.close()
	return ring
}

func recorderServer(t *testing.T, rec SessionOutputRecorder) *WSServer {
	t.Helper()
	return NewWSServer(log.NewSlogAdapter(nil), nil, WithSessionOutputRecorder(rec))
}

// THE CLAUSE THAT MATTERS: one missed second must not cost the session.
func TestRecorderSkipsTheHoleAndKeepsRecording(t *testing.T) {
	lost, kept := []byte("nobody was here to write this down"), []byte("but this arrives")
	ring := ringPastItsRecorder(t, lost, kept)
	rec := newFakeRecorder()
	ws := recorderServer(t, rec)

	ws.recordSessionOutput(context.Background(), session.ID("sid-skip"), ring)

	skips := rec.skipCalls()
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want exactly one across the hole", skips)
	}
	if skips[0].resumeAt != uint64(len(lost)) {
		t.Fatalf("skipped to %d, want %d — the ring's oldest retained byte", skips[0].resumeAt, len(lost))
	}
	if skips[0].reason != content.GapReasonUnrecorded {
		t.Fatalf("skip reason = %q, want %q: the cap did not take these bytes", skips[0].reason, content.GapReasonUnrecorded)
	}
	if got := string(rec.recorded()); got != string(kept) {
		t.Fatalf("recorded %q, want %q: recording did not resume after the hole", got, kept)
	}
	if rec.firstOffset() != uint64(len(lost)) {
		t.Fatalf("the resumed run starts at %d, want %d", rec.firstOffset(), len(lost))
	}
}

// A store that cannot record the hole is a store that cannot be trusted with
// the resume either: the loop stops, exactly as it does for a failed append,
// and says so where every other history write failure is said.
func TestRecorderStopsWhenTheHoleCannotBeRecorded(t *testing.T) {
	ring := ringPastItsRecorder(t, []byte("gone"), []byte("later"))
	rec := newFakeRecorder()
	rec.setSkipFailure(errors.New("disk is gone"))
	st := NewHistoryStatus()
	ws := NewWSServer(log.NewSlogAdapter(nil), nil,
		WithSessionOutputRecorder(rec), WithHistoryStatus(st))

	ws.recordSessionOutput(context.Background(), session.ID("sid-skip-fail"), ring)

	if len(rec.recorded()) != 0 {
		t.Fatalf("recorded %q after the skip failed", rec.recorded())
	}
	if st.Available() {
		t.Fatal("a failed skip is a write failure the product never hears about")
	}
	got := st.snapshot()
	if got.Reason == nil || *got.Reason != string(HistoryDegradeWriteFailed) {
		t.Fatalf("reason = %v, want writeFailed", got.Reason)
	}
	if got.Detail == nil || *got.Detail != "disk is gone" {
		t.Errorf("detail = %v, want the store's own words", got.Detail)
	}
}

// The fallback stops guessing (found while doing nocx-fz4qa). A hole with no
// recorded reason used to be named `cap` on purpose — the cap was the only
// thing that could make one. Skip makes a second kind, so that fallback would
// confidently name the retention bound for bytes it never touched.
func TestSessionOutputGapReasonNeverGuessesTheCap(t *testing.T) {
	gaps := []content.Gap{
		{Start: 10, End: 20, Reason: content.GapReasonCap},
		{Start: 40, End: 90, Reason: content.GapReasonUnrecorded},
	}
	if got := sessionOutputGapReason(10, 20, gaps); got != content.GapReasonCap {
		t.Fatalf("a cap eviction reads back as %q", got)
	}
	if got := sessionOutputGapReason(40, 90, gaps); got != content.GapReasonUnrecorded {
		t.Fatalf("a never-ingested range reads back as %q, want %q", got, content.GapReasonUnrecorded)
	}
	if got := sessionOutputGapReason(200, 300, gaps); got != content.GapReasonUnknown {
		t.Fatalf("an unexplained hole reads back as %q; naming the cap for bytes the cap never had is the false statement this forbids", got)
	}
}
