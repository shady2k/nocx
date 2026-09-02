package transport

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestSnapshotAndTheHelperWindowShareOneDecisionRule is AD-8 applied to a
// predicate rather than to a package. "Is this offset still in the window" has
// been asked here since AD-9 and is now asked on the host too, by the helper's
// bounded output window (nocx-k6p18.3) — and a second derivation of it would
// be two owners that agree everywhere anybody looks and disagree somewhere
// nobody does.
//
// So the verdict has one owner, proto.ResumeAt, and this asserts the two agree
// across the boundary rather than trusting that they were written the same way
// once. What the ring does NOT take from it is where a reset restarts, and the
// difference is a fact about the two windows rather than a divergence: this
// ring is lossless — a byte leaves it only after a consumer passed it — so a
// request below its base is a stale cursor and the honest restart is "now".
// The helper's window is capacity-reclaimed, so a request below ITS base is a
// loss nobody could have prevented, and the honest restart is the oldest byte
// that still exists, with the hole stated.
func TestSnapshotAndTheHelperWindowShareOneDecisionRule(t *testing.T) {
	ring := newOutputRing()
	if err := ring.write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Ack four bytes so the ring trims and its base is genuinely non-zero: a
	// base of zero could never disagree with anything.
	if err := ring.ack(4); err != nil {
		t.Fatalf("ack: %v", err)
	}

	ring.mu.Lock()
	base, written := ring.base, ring.written()
	ring.mu.Unlock()
	if base != 4 || written != 10 {
		t.Fatalf("base=%d written=%d, want 4 and 10", base, written)
	}

	for _, offset := range []uint64{0, 3, 4, 5, 10, 11} {
		_, from, needsReset, _ := ring.snapshot(offset)
		want := proto.ResumeAt(proto.StreamOffset(base), proto.StreamOffset(written), proto.StreamOffset(offset))
		if needsReset != want.Reset {
			t.Errorf("snapshot(%d) reset=%v, proto.ResumeAt says %v", offset, needsReset, want.Reset)
		}
		if needsReset && from != written {
			t.Errorf("snapshot(%d) resets to %d, want the stream's end: this ring is lossless, so a request below its base is a stale cursor, not a loss", offset, from)
		}
	}
}
