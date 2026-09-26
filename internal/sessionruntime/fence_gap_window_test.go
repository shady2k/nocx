package sessionruntime

import (
	"testing"
)

// A FENCED SCREEN'S ROWS LEAVING BEFORE THE COMPLETION ARRIVES ARE NOT
// STREAMED AGAIN (nocx-2v80t.3.41).
//
// In the ordinary local order the fence is SIGHTED first: the sighting takes
// the boundary's capture — the row count and the closing screen, in one
// instant — and the completion that follows authenticates it and emits the
// end marker (ADR-0074 case 1). The completion rides another carrier, and the
// person (or the e2e) can already be running the next command when it lands:
// that command's echo and output push the fenced screen's top rows off the
// screen BEFORE the end marker exists.
//
// Those rows are the fenced block's closing screen — the end marker carries
// them — so streaming them as well stores them twice in that block: the
// consumer holds every row that arrives before a block's end marker as that
// block's, and then appends the closing screen after it. The e2e measured the
// same double store from a pane shrinking in that gap
// (TestAResizeAroundABoundaryStoresEachRowOnce); the next command's own
// output reaching the screen before the completion reaches the runtime is
// the other way into it.
//
// The invariant is the consumer's and the block's: every row of a command is
// stored exactly once, in that command's block, whichever order the two halves
// of its boundary arrive in.
func TestAFencedScreenLeavingBeforeItsCompletionIsStoredOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		// early is how many lines of command B are printed while command A's
		// boundary is still waiting for its authenticated half.
		early int
		order string
	}{
		{name: "the completion arrives before the next command prints", order: "sight-complete"},
		{name: "the completion arrives first and the sighting joins it", order: "complete-sight"},
		{name: "the next command pushes one row off before the completion", order: "sight-print-complete", early: 1},
		{name: "the next command pushes six rows off before the completion", order: "sight-print-complete", early: 6},
		{name: "the next command pushes the whole screen off before the completion", order: "sight-print-complete", early: 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rs := streamSession(t, harnessGeometry(80, 24))
			a, b := obsNonce(1), obsNonce(2)

			// Command A: forty lines, seventeen leave during it, the rest are
			// its closing screen.
			obsFeed(t, s, 0, 40)
			switch tc.order {
			case "sight-complete":
				s.SightFenceBoundary(t, a)
				s.Completed(s.Incarnation(), a, 0)
				promptAndEcho(t, s)
			case "complete-sight":
				s.Completed(s.Incarnation(), a, 0)
				s.SightFenceBoundary(t, a)
				promptAndEcho(t, s)
			case "sight-print-complete":
				s.SightFenceBoundary(t, a)
				promptAndEcho(t, s)
				obsFeed(t, s, 100, tc.early)
				s.Completed(s.Incarnation(), a, 0)
			}
			if got := s.RendezvousFor(a).State; got != RendezvousComplete {
				t.Fatalf("command A's boundary reads %s, want complete", rendezvousStateName(got))
			}

			// Command B: the rest of its forty lines, then its own boundary.
			obsFeed(t, s, 100+tc.early, 40-tc.early)
			obsSeal(t, s, b)

			blocks := blocksAsTheConsumerStoresThem(t, rs)
			if len(blocks) != 2 {
				t.Fatalf("the stream closed %d blocks, want 2", len(blocks))
			}
			assertBlockHoldsExactly(t, "command A", blocks[0], 0, 40)
			assertBlockHoldsExactly(t, "command B", blocks[1], 100, 40)
		})
	}
}
