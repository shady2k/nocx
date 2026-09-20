package proto

// The capture shape's round trip (nocx-2v80t.2.2): the params marshal to the
// keys the contract will declare and decode back to the same value, because a
// sender and a reader that disagree about one key is an interval lost on the
// wire. The ack's closed reason set is pinned beside it, because "nothing was
// kept" without a why is the silent success the shape exists to prevent.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureParamsRoundTrips(t *testing.T) {
	in := CaptureParams{
		Session:      HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"},
		Incarnation:  Incarnation{Session: "0123456789abcdef0123456789abcdef", Generation: 1},
		Nonce:        strings.Repeat("ab", 32),
		Revision:     7,
		Completeness: CompletenessComplete,
		Opening:      CaptureScreen{Cols: 80, Rows: 24},
		Departed: []CaptureRow{{
			Wrap:  true,
			Cells: []CaptureCell{{Text: "x", Width: 1, HasText: true, Attrs: 1, Underline: 2}},
		}},
		DepartedHole: true,
		Closing: CaptureScreen{
			Cols: 80, Rows: 24, CursorVisible: true,
			Lines: []CaptureRow{{Cells: []CaptureCell{{
				Text: "out", Width: 1, HasText: true,
				Fg: &CaptureColor{Kind: "rgb", RGB: "#ff0000"},
			}}}},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"session"`, `"incarnation"`, `"nonce"`, `"revision"`, `"completeness"`,
		`"opening"`, `"departed"`, `"departedHole"`, `"closing"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("marshalled params carry no %s: %s", key, raw)
		}
	}
	var out CaptureParams
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Nonce != in.Nonce || out.Revision != in.Revision || out.Completeness != in.Completeness {
		t.Fatalf("identity round trip drifted: %+v", out)
	}
	if !out.DepartedHole || len(out.Departed) != 1 || out.Departed[0].Cells[0].Attrs != 1 {
		t.Fatalf("departed round trip drifted: %+v", out.Departed)
	}
	if got := out.Closing.Lines[0].Cells[0].Fg; got == nil || got.RGB != "#ff0000" {
		t.Fatalf("closing colour round trip drifted: %+v", got)
	}
}

func TestCaptureResultReasonIsClosedAndEmptyOnlyWhenKept(t *testing.T) {
	kept := CaptureResult{Kept: true}
	if raw, err := json.Marshal(kept); err != nil || strings.Contains(string(raw), "reason") {
		t.Fatalf("a kept ack carries no reason: %s, %v", raw, err)
	}
	for _, reason := range []string{"outputOff", "sensitive", "critical", "noEntry"} {
		in := CaptureResult{Kept: false, Reason: reason}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal %s: %v", reason, err)
		}
		var out CaptureResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal %s: %v", reason, err)
		}
		if out.Kept || out.Reason != reason {
			t.Fatalf("ack round trip drifted: %+v", out)
		}
	}
}
