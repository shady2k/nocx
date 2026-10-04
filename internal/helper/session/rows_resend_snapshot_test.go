package session

// Resend must serve the original indexed cells, not a reflowed view of live history.

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

func TestResendUsesOriginalCellsAfterDisconnectedWidthChange(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	original := decodeResentRows(t, sink.rowFrames())
	if len(original) == 0 || len(original[0].texts) == 0 {
		t.Fatal("initial emission contained no rows")
	}
	want := append([]string(nil), original[0].texts...)
	from := original[0].from

	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	// A narrower geometry reflows live history while the coordinator is
	// away. The resend must not consult that mutable tier.
	if _, err := rt.CommitGeometry(emulator.Geometry{Cols: 40, Rows: 12, CellWidthPx: 8, CellHeightPx: 16}); err != nil {
		t.Fatalf("commit disconnected width change: %v", err)
	}
	rowsFeed(t, rt, 100, 100)

	sink2 := newRowsSink()
	hs.mu.Lock()
	hs.subs["coord-2"] = &subscriber{id: "coord-2", raw: mintRaw(t), sink: sink2}
	hs.mu.Unlock()
	hs.wakeRows()
	sink2.waitFor(1, 0, 0)
	resent := decodeResentRows(t, sink2.rowFrames())
	if len(resent) == 0 || resent[0].from != from {
		t.Fatalf("resent first index = %v, want original index %d", resent, from)
	}
	if len(resent[0].texts) < len(want) {
		t.Fatalf("resend supplied %d original cells, want at least %d", len(resent[0].texts), len(want))
	}
	for i, text := range want {
		if resent[0].texts[i] != text {
			t.Fatalf("resend at original index %d = %q, want original %q", from+uint64(i), resent[0].texts[i], text) //nolint:gosec // i is a non-negative slice index
		}
	}
}
