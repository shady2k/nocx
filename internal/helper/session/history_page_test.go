package session

// The history-page op over the real runtime (nocx-zg3k3.10.3): the service
// handler is a wire spelling of sessionruntime's own page read, so what this
// file owns is that the spelling is faithful — the interval, the floor, the
// rows as the emulator holds them, encoded by the ONE encoder of the rows
// vocabulary. The emulator is real (rowsBridgeSession's stand), because the
// retention question and the departures are the library's behaviour.

import (
	"encoding/json"
	"fmt"
	"testing"
)

// decodePageRows reads a result's pre-encoded rows as their texts.
func decodePageRows(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var rows []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode the page's rows: %v\n%s", err, raw)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}

// One page, from the head, of a history the runtime really holds.
func TestHistoryPageOpAnswersWhatTheRuntimeHolds(t *testing.T) {
	hs, rt, _ := rowsBridgeSession(t, 80, 24)
	hs.scrollback.Store(DefaultScrollbackLines)
	rowsFeed(t, rt, 0, 60) // history L000000..L000036, head 37

	res, err := hs.historyPage(nil, 10)
	if err != nil {
		t.Fatalf("historyPage: %v", err)
	}
	if res.Start != 27 || res.End != 37 {
		t.Fatalf("the op answers [%d,%d), want [27,37)", res.Start, res.End)
	}
	if res.Floor != 0 {
		t.Fatalf("the op answers floor %d, want 0", res.Floor)
	}
	if !res.More {
		t.Fatal("the op says no more, want true")
	}
	texts := decodePageRows(t, res.Rows)
	if len(texts) != 10 {
		t.Fatalf("the op's rows hold %d entries, want 10", len(texts))
	}
	for i, txt := range texts {
		if want := fmt.Sprintf("L%06d", 27+i); txt != want {
			t.Fatalf("row %d reads %q, want %q", i, txt, want)
		}
	}
}

// A cursor below the retained floor is the empty answer with the floor
// stated. The floor here is an ED3's: the clear destroys the history the
// runtime had, and the cursor the caller held from before it now points
// below where the live tier begins.
func TestHistoryPageOpAnswersBelowTheFloorAfterClear(t *testing.T) {
	hs, rt, _ := rowsBridgeSession(t, 80, 24)
	hs.scrollback.Store(DefaultScrollbackLines)
	rowsFeed(t, rt, 0, 60)
	if err := rt.Ingest([]byte("\x1b[H\x1b[2J\x1b[3J")); err != nil {
		t.Fatalf("clear: %v", err)
	}

	stale := uint64(5)
	res, err := hs.historyPage(&stale, 10)
	if err != nil {
		t.Fatalf("historyPage with the stale cursor: %v", err)
	}
	if len(decodePageRows(t, res.Rows)) != 0 || res.More {
		t.Fatalf("the stale cursor's page carries rows with more=%v, want the empty answer", res.More)
	}
	if res.Start != 5 || res.End != 5 {
		t.Fatalf("the stale cursor's page is [%d,%d), want the empty [5,5)", res.Start, res.End)
	}
	if res.Floor != 37 {
		t.Fatalf("the stale cursor's page states floor %d, want 37 — where the live tier begins after the clear", res.Floor)
	}
}
