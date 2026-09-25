package sessionruntime

import "testing"

// An interval settled without its fence (ADR-0074 decision 3) is sealed with
// no closing screen and reads no-fence on its record — and the END MARKER is
// what the coordinator stores the block from, so the marker has to say it
// too (nocx-2v80t.3.29). Before this, the record said no-fence and the block
// said nothing: its output read as whole with its closing screen missing.
func TestAnIntervalSettledWithoutItsFenceSaysSoOnItsEndMarker(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	parked, later := obsNonce(0x31), obsNonce(0x32)

	obsFeed(t, s, 0, 30)
	s.Completed(s.Incarnation(), parked, 0)
	s.Completed(s.Incarnation(), later, 0)

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

	s.SealEnvironmentEntry(s.Incarnation(), "dom-child")
	entry, ok := settledEnds(rs)[FenceNonce{}]
	if !ok {
		t.Fatal("the environment entry emitted no end marker")
	}
	if entry.noFence {
		t.Fatal("an environment entry's end marker says a fence went missing")
	}
}
