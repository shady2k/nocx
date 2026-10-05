package sessionruntime

import (
	"context"
	"testing"
)

// An interval settled without its fence (ADR-0074 decision 3, amended by
// nocx-n5ent) is sealed with no closing screen and reads no-fence on its
// record — and the END MARKER is what the coordinator stores the block from,
// so the marker has to say it too (nocx-2v80t.3.29). Before this, the record
// said no-fence and the block said nothing: its output read as whole with its
// closing screen missing.
//
// WHAT THE AMENDMENT MOVED IS ONLY WHEN THE SETTLE IS TAKEN. The event that
// asks for it — here a completion for another nonce — arrives on the
// authenticated channel, which the command's own bytes are not ordered
// against, so it DEFERS: the interval keeps its own rows until the byte stream
// reaches its next boundary (a later fence's sighting, or the session's end).
// The settle itself is unchanged, and so is the marker it emits.
func TestAnIntervalSettledWithoutItsFenceSaysSoOnItsEndMarker(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	parked, later := obsNonce(0x31), obsNonce(0x32)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), parked, 0)
	s.Completed(s.Incarnation(), later, 0)

	if _, early := settledEnds(rs)[parked]; early {
		t.Fatal("a completion for another nonce sealed the interval before the stream reached its next boundary")
	}

	// The next boundary on the byte stream: the later command's own fence is
	// sighted, which is the proof that the parked interval's bytes have been
	// read.
	if err := s.SightFence(later, []byte("$ ")); err != nil {
		t.Fatalf("sight the next interval's fence: %v", err)
	}

	end, ok := settledEnds(rs)[parked]
	if !ok {
		t.Fatal("the settled interval emitted no end marker")
	}
	if !end.noFence {
		t.Fatal("the end marker of an interval settled without its fence does not say so")
	}
}

// Paired: an interval whose fence joined its completion ends on a marker that
// claims nothing is missing, and so does an environment entry, which has no
// fence by design rather than by loss.
func TestAFencedIntervalAndAnEntryEndOnAWholeMarker(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	fenced := obsNonce(0x33)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), fenced, 0)
	if err := s.SightFence(fenced, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	end, ok := settledEnds(rs)[fenced]
	if !ok {
		t.Fatal("the fenced interval emitted no end marker")
	}
	if end.noFence {
		t.Fatal("a fenced interval's end marker says its fence never arrived")
	}

	if err := s.SealEnvironmentEntry(context.Background(), s.Incarnation(), "dom-child"); err != nil {
		t.Fatalf("seal the entry: %v", err)
	}
	entry, ok := settledEnds(rs)[FenceNonce{}]
	if !ok {
		t.Fatal("the environment entry emitted no end marker")
	}
	if entry.noFence {
		t.Fatal("an environment entry's end marker says a fence went missing")
	}
}
