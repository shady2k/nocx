package sessionruntime

import (
	"fmt"
	"testing"
)

// A PANE RESIZED AROUND A COMMAND'S BOUNDARY STORES EACH OF ITS ROWS ONCE
// (nocx-2v80t.3.41).
//
// The renderer re-measures the live grid as blocks freeze, so the pane's row
// count moves between commands and inside them: measured on the
// transcript-budget e2e, 221 geometry commits over 500 commands, the pane
// stepping between 27, 28 and 33 rows. A block is the rows its interval
// departed plus its closing screen, and a resize moved rows between the screen
// and the history without either side knowing: the port counted neither a
// shrink's push nor a growth's refill as a departure, and the closing screen
// carried whatever the screen held:
//
//   - a pane that SHRINKS after the command's output and before its fence
//     pushes the screen's top row into history. It never departed, so it was
//     never streamed, and it is no longer on the closing screen either: the
//     block holds 99 of its 100 rows (measured: transcript-0151-074 gone, the
//     pane going 28 -> 27 rows between the output and the fence);
//   - a pane that GROWS there pulls rows the interval already streamed back
//     onto the top of the screen, and the closing screen carries them a
//     second time: 105 or 106 of 100, 069..074 twice (the bead's measurement).
//
// The invariant is the block's: whatever the pane does, a command's block
// holds each of its rows exactly once, in order, and none of another's.
func TestAResizeAroundABoundaryStoresEachRowOnce(t *testing.T) {
	const rows = 100
	for _, tc := range []struct {
		name  string
		start int
		// beforeFence commits between A's output and its fence; inGap between
		// the fence's sighting and its completion (only in the sighting-first
		// order, the ordinary local one); afterEnd once A's boundary is
		// sealed, before B runs.
		beforeFence []int
		inGap       []int
		afterEnd    []int
	}{
		{name: "the pane keeps its size", start: 28},
		{name: "the pane shrinks one row before the fence", start: 28, beforeFence: []int{27}},
		{name: "the pane grows six rows before the fence", start: 27, beforeFence: []int{33}},
		{name: "the pane grows five rows before the fence", start: 28, beforeFence: []int{33}},
		{name: "the pane shrinks and grows back before the fence", start: 28, beforeFence: []int{27, 28}},
		{name: "the pane grows and shrinks back before the fence", start: 27, beforeFence: []int{33, 27}},
		{name: "the pane shrinks one row before the completion", start: 28, inGap: []int{27}},
		{name: "the pane grows six rows before the completion", start: 27, inGap: []int{33}},
		{name: "the pane grows and shrinks back before the completion", start: 27, inGap: []int{33, 27}},
		{name: "the pane shrinks one row after the boundary", start: 28, afterEnd: []int{27}},
		{name: "the pane grows six rows after the boundary", start: 27, afterEnd: []int{33}},
		{name: "the pane shrinks after the boundary and grows during the next command", start: 28, afterEnd: []int{27, 33}},
	} {
		for _, sightingFirst := range []bool{true, false} {
			if len(tc.inGap) > 0 && !sightingFirst {
				continue
			}
			order := "the completion arrives first"
			if sightingFirst {
				order = "the fence is sighted first"
			}
			t.Run(tc.name+", "+order, func(t *testing.T) {
				s, rs := streamSession(t, harnessGeometry(80, tc.start))
				commit := func(to []int) {
					t.Helper()
					for _, r := range to {
						if _, err := s.CommitGeometry(harnessGeometry(80, r)); err != nil {
							t.Fatalf("commit %dx%d: %v", 80, r, err)
						}
					}
				}
				seal := func(nonce FenceNonce, gap []int) {
					t.Helper()
					if sightingFirst {
						s.SightFenceBoundary(t, nonce)
						commit(gap)
						s.Completed(s.Incarnation(), nonce, 0)
					} else {
						s.Completed(s.Incarnation(), nonce, 0)
						s.SightFenceBoundary(t, nonce)
					}
					if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
						t.Fatalf("the boundary reads %s, want complete", rendezvousStateName(got))
					}
				}

				promptAndEcho(t, s)
				obsFeed(t, s, 0, rows)
				commit(tc.beforeFence)
				seal(obsNonce(1), tc.inGap)
				commit(tc.afterEnd)

				promptAndEcho(t, s)
				obsFeed(t, s, 1000, rows)
				seal(obsNonce(2), nil)
				// A third command pushes B's closing screen off, so every row
				// of B's has either departed or been handed over as its
				// closing.
				promptAndEcho(t, s)
				obsFeed(t, s, 5000, rows)
				seal(obsNonce(3), nil)

				blocks := blocksAsTheConsumerStoresThem(t, rs)
				if len(blocks) != 3 {
					t.Fatalf("the stream closed %d blocks, want 3", len(blocks))
				}
				assertBlockHoldsExactly(t, "command A", blocks[0], 0, rows)
				assertBlockHoldsExactly(t, "command B", blocks[1], 1000, rows)
			})
		}
	}
}

// promptAndEcho is what the shell writes between two commands: the prompt,
// the command typed onto it, and the newline that runs it.
func promptAndEcho(t *testing.T, s *Session) {
	t.Helper()
	if err := s.Ingest([]byte("$ run\r\n")); err != nil {
		t.Fatalf("draw the prompt and the echo: %v", err)
	}
}

// blocksAsTheConsumerStoresThem assembles the stream the way the block store
// does (internal/transport's closeBlockRowsNow): every row that arrives before
// a block's end marker is that block's, and the end marker's closing screen
// follows it.
func blocksAsTheConsumerStoresThem(t *testing.T, rs *recordingRowStream) [][]string {
	t.Helper()
	var blocks [][]string
	var cur []string
	for _, e := range rs.snapshot() {
		switch e.kind {
		case "rows":
			for _, row := range e.rows {
				cur = append(cur, streamRowText(row))
			}
		case "end":
			for _, row := range e.closing {
				cur = append(cur, streamRowText(row))
			}
			blocks = append(blocks, cur)
			cur = nil
		}
	}
	return blocks
}

// assertBlockHoldsExactly fails unless the block holds each of its command's
// numbered lines exactly once, in order, and no other command's.
func assertBlockHoldsExactly(t *testing.T, name string, block []string, first, n int) {
	t.Helper()
	var numbered []string
	for _, text := range block {
		if len(text) == 7 && text[0] == 'L' {
			numbered = append(numbered, text)
		}
	}
	want := make([]string, n)
	for i := range n {
		want[i] = fmt.Sprintf("L%06d", first+i)
	}
	if fmt.Sprint(numbered) == fmt.Sprint(want) {
		return
	}
	seen := map[string]int{}
	for _, text := range numbered {
		seen[text]++
	}
	var twice, missing, foreign []string
	for _, w := range want {
		switch seen[w] {
		case 0:
			missing = append(missing, w)
		case 1:
		default:
			twice = append(twice, w)
		}
		delete(seen, w)
	}
	for text := range seen {
		foreign = append(foreign, text)
	}
	t.Fatalf("%s's block holds %d numbered rows, want exactly its own %d once each, in order: twice %v, missing %v, another command's %v",
		name, len(numbered), n, twice, missing, foreign)
}

// A window installed below the rows a growing pane pulled back pins the rows
// it names, not the screen's first rows: each entry's pin reads, through
// itself, the very line the entry expects to leave (nocx-2v80t.3.41). A pin
// off by the pulled-back count names another row, and the reconciliation that
// reads through the pins (reconcilePendingScreenLocked) would cut the window
// on a row nothing wrote over.
func TestAWindowBelowPulledBackRowsPinsTheRowsItNames(t *testing.T) {
	s, _ := streamSession(t, harnessGeometry(80, 27))
	promptAndEcho(t, s)
	obsFeed(t, s, 0, 100)
	if _, err := s.CommitGeometry(harnessGeometry(80, 33)); err != nil {
		t.Fatalf("grow: %v", err)
	}
	obsSeal(t, s, obsNonce(1))

	if len(s.pendingScreen) == 0 {
		t.Fatal("the seal installed no window")
	}
	if first := streamRowText(s.pendingScreen[0].Row); first != "L000074" {
		t.Fatalf("the window starts at %q, want L000074: the six rows the growth pulled back were streamed already", first)
	}
	for i, p := range s.pendingScreen {
		now, err := p.Track.Row()
		if err != nil {
			t.Fatalf("entry %d's pin cannot be read: %v", i, err)
		}
		if got, want := streamRowText(now), streamRowText(p.Row); got != want {
			t.Fatalf("entry %d expects %q and its pin reads %q: the window pinned the wrong row", i, want, got)
		}
	}
}
