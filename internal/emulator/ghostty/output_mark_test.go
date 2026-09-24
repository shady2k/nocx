package ghostty

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The output-start mark (nocx-2v80t.3.12): OSC 133 C, ESC ] 1 3 3 ; C BEL,
// with no payload. A sighting authorises nothing — these tests assert only
// that the port SEES it and reports it, the way fence_test.go does for the
// render fence, whose scanner this one now shares a pass with (scanMarkers,
// terminal.go).

const outputMarkSeq = "\x1b]133;C\x07"

func wantOneOutputMark(t *testing.T, got []emulator.Effect) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the drain holds %s, want exactly one output-mark effect", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectOutputMark {
		t.Fatalf("the effect is %s, want an output mark", describeEffects(got))
	}
	if len(got[0].Body) != 0 {
		t.Fatalf("the output mark carries body %q, want none", got[0].Body)
	}
}

// TestTheOutputMarkIsSighted: the bare sequence, straddled across two ingest
// calls (the scanner's state must survive the split), and two marks in one
// chunk producing two effects — the same three shapes fence_test.go covers
// for the fence, since the scan is now the same pass.
func TestTheOutputMarkIsSighted(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, outputMarkSeq)
	wantOneOutputMark(t, term.Effects())

	straddled := newTerminal(t, 20, 4)
	ingest(t, straddled, outputMarkSeq[:4])
	if got := straddled.Effects(); len(got) != 0 {
		t.Fatalf("a partial mark produced %s, want nothing yet", describeEffects(got))
	}
	ingest(t, straddled, outputMarkSeq[4:])
	wantOneOutputMark(t, straddled.Effects())

	twice := newTerminal(t, 20, 4)
	ingest(t, twice, "x"+outputMarkSeq+outputMarkSeq)
	got := twice.Effects()
	if len(got) != 2 {
		t.Fatalf("two marks in one chunk produced %s, want exactly two effects", describeEffects(got))
	}
	for i, e := range got {
		if e.Kind != emulator.EffectOutputMark {
			t.Fatalf("mark %d is %s, want an output mark", i, describeEffects(got))
		}
	}
}

// TestTheOutputMarkNeverEatsTheBytesAroundIt: the mark splits the feed, it
// never withholds bytes — output before and after it both reach the screen,
// in the same ingest call the mark itself arrived in.
func TestTheOutputMarkNeverEatsTheBytesAroundIt(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "before"+outputMarkSeq+"after")
	wantOneOutputMark(t, term.Effects())
	if text := screenText(t, term, 4); text != "beforeafter\n\n\n\n" {
		t.Fatalf("the screen reads %q, want the mark to have cost no bytes either side", text)
	}
}

// TestTheFenceAndTheOutputMarkAreIndependentSightings: the two scanners
// share a five-byte prefix and diverge at the sixth (fence.go, output_mark.go)
// — one pass over the bytes must still resolve each candidate correctly
// regardless of order, and a candidate that dies for one sequence must not
// corrupt the other's.
func TestTheFenceAndTheOutputMarkAreIndependentSightings(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, outputMarkSeq+fenceSeq(fenceNonce))
	got := term.Effects()
	if len(got) != 2 {
		t.Fatalf("a mark then a fence produced %s, want exactly two effects", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectOutputMark {
		t.Fatalf("the first effect is %s, want the output mark first", describeEffects(got))
	}
	if got[1].Kind != emulator.EffectFence || string(got[1].Body) != fenceNonce {
		t.Fatalf("the second effect is %s, want the fence", describeEffects(got))
	}

	reversed := newTerminal(t, 20, 4)
	ingest(t, reversed, fenceSeq(fenceNonce)+outputMarkSeq)
	got = reversed.Effects()
	if len(got) != 2 {
		t.Fatalf("a fence then a mark produced %s, want exactly two effects", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectFence || string(got[0].Body) != fenceNonce {
		t.Fatalf("the first effect is %s, want the fence first", describeEffects(got))
	}
	if got[1].Kind != emulator.EffectOutputMark {
		t.Fatalf("the second effect is %s, want the output mark", describeEffects(got))
	}
}

// TestALookAlikeYieldsNoOutputMark: the fence's own prefix (which shares the
// output mark's first five bytes) must not be mistaken for one when it goes
// on to be an actual fence, or when it is simply malformed.
func TestALookAlikeYieldsNoOutputMark(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "\x1b]133;X\x07") // not C: no effect at all
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("a foreign 133 sequence produced %s, want nothing", describeEffects(got))
	}
}
