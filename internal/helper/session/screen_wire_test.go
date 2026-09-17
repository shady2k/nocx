package session

// The wire rendering of the screen reads (nocx-ygxjv.3), asserted where the two
// vocabularies meet.
//
// These live in the INTERNAL test package on purpose. The public seam is
// Service.Call, and screen_test.go drives that; what it cannot reach is the
// conversion between this process's completeness vocabulary and the ABI's
// spelling of it, which is the thing a coordinator's write gate depends on and
// the thing a future value could silently fall out of.

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestCompletenessCrossesAsItself walks the whole closed set: a runtime's claim
// must arrive on the wire as the same claim, or a gate that refuses on
// "unknown" would be reading a value nobody wrote.
func TestCompletenessCrossesAsItself(t *testing.T) {
	for _, tc := range []struct {
		in   sessionruntime.Completeness
		want proto.Completeness
	}{
		{sessionruntime.CompletenessUnknown, proto.CompletenessUnknown},
		{sessionruntime.CompletenessComplete, proto.CompletenessComplete},
		{sessionruntime.CompletenessLostIngest, proto.CompletenessLostIngest},
		{sessionruntime.CompletenessNoFence, proto.CompletenessNoFence},
		{sessionruntime.CompletenessEvicted, proto.CompletenessEvicted},
	} {
		if got := completenessName(tc.in); got != tc.want {
			t.Errorf("completenessName(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// And a value this build has never heard of is spelled UNKNOWN rather than
	// passed through: an unrecognised claim must never arrive at a caller
	// looking stronger than it is.
	if got := completenessName(sessionruntime.Completeness(200)); got != proto.CompletenessUnknown {
		t.Errorf("an unrecognised completeness crossed as %q, want %q", got, proto.CompletenessUnknown)
	}
}

// TestAScreenFrameIsARectangle pins the shape a consumer indexes: rows long and
// columns wide, with the caret and the buffer beside them. A frame whose rows
// were ragged would let a reader of a position read past the end of one.
func TestAScreenFrameIsARectangle(t *testing.T) {
	in := paneview.Frame{
		Cols: 4, Rows: 3,
		CursorX: 2, CursorY: 1, CursorVisible: true, AltScreen: true,
		Lines: [][]paneview.Cell{
			{{Text: "a", Width: 1}, {Text: "こ", Width: 2}, {Width: 0}, {}},
			{{}, {}, {}, {}},
			{{Text: "z", Width: 1}, {}, {}, {}},
		},
	}
	out := screenFrame(in)

	if out.Cols != 4 || out.Rows != 3 || !out.AltScreen || !out.CursorVisible || out.CursorX != 2 || out.CursorY != 1 {
		t.Fatalf("frame crossed as %+v, want the geometry, the caret and the buffer it was given", out)
	}
	if len(out.Lines) != 3 {
		t.Fatalf("frame carries %d rows, want 3", len(out.Lines))
	}
	for y, row := range out.Lines {
		if len(row) != 4 {
			t.Errorf("row %d is %d columns long, want 4", y, len(row))
		}
	}
	if got := out.Lines[0][1]; got.Text != "こ" || got.Width != 2 {
		t.Errorf("the wide cluster crossed as %+v, want width 2", got)
	}
	if got := out.Lines[0][2]; got.Width != 0 {
		t.Errorf("the continuation crossed with width %d, want 0", got.Width)
	}
}
