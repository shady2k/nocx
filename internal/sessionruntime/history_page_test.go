package sessionruntime

// The live history page (nocx-zg3k3.10.3): one page of the scrollback the
// emulator holds, read under ONE acquisition of the runtime lock so the
// page's rows, its floor and the head it is measured against describe one
// instant of the buffer.
//
// The cursor a caller pages with is the ABSOLUTE history row number this
// package has kept since the first row left the screen — screenDepartedRows
// space, corrected by the rows a growing pane pulled back onto the screen
// (owed): a row's number never changes because output arrived, the pane was
// resized, or the alternate screen took over. What moves the numbers is only
// what really removes rows: the library's retention pruning (floor rises) and
// an erase-saved-lines (history empties; floor jumps to the head). Every page
// answer states the floor and the interval it delivered, so a cursor that
// fell below the floor is answered with the truth — the empty interval at the
// cursor — instead of a short list a caller would read as a short history.
//
// These tests run over the REAL emulator (libghostty-vt behind its port):
// the retention question, the alternate screen and ED3 are the library's
// behaviour, and a fake would answer for it.

import (
	"fmt"
	"testing"
)

func historyPageUint(v uint64) *uint64 { return &v }

func historyPageTexts(p HistoryPageView) []string {
	out := make([]string, 0, len(p.Rows))
	for _, r := range p.Rows {
		out = append(out, streamRowText(r))
	}
	return out
}

func historyPageWant(t *testing.T, got []string, from, to int) {
	t.Helper()
	if len(got) != to-from {
		t.Fatalf("page holds %d rows, want %d (L%06d..L%06d)", len(got), to-from, from, to-1)
	}
	for i, txt := range got {
		if want := fmt.Sprintf("L%06d", from+i); txt != want {
			t.Fatalf("row %d reads %q, want %q", from+i, txt, want)
		}
	}
}

// A client pages backwards through more history than one page holds and
// receives every row exactly once, in order, with no duplicate and no gap at
// a page seam (the acceptance criterion, at the coordinate layer: the wire
// test repeats it over the real socket). The walk's terminal page is the one
// that reaches the floor — still carrying rows, with more=false — not an
// empty page after it.
func TestHistoryPageWalksBackwardDeliveringEveryRowOnce(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 100) // 23 stay on the screen, 77 are history

	var got []string
	first, last := HistoryPageView{}, HistoryPageView{}
	before := (*uint64)(nil)
	pages := 0
	for {
		p, err := s.ReadHistoryPage(before, 30)
		if err != nil {
			t.Fatalf("read the page: %v", err)
		}
		if pages == 0 {
			first = p
		}
		last = p
		got = append(got, historyPageTexts(p)...)
		if !p.More {
			break
		}
		b := p.Start
		before = &b
		pages++
		if pages > 10 {
			t.Fatal("paging never reached the floor: more stayed true past the buffer")
		}
	}
	// Each page is oldest-first, the walk newest-page-first: three blocks,
	// [47,77), [17,47), [0,17), each ascending within itself.
	for i, txt := range got {
		var want int
		switch {
		case i < 30:
			want = 47 + i
		case i < 60:
			want = 17 + (i - 30)
		default:
			want = i - 60
		}
		if name := fmt.Sprintf("L%06d", want); txt != name {
			t.Fatalf("walked row %d reads %q, want %q", i, txt, name)
		}
	}
	if len(got) != 77 {
		t.Fatalf("the walk collected %d rows, want 77", len(got))
	}
	if first.Start != 47 || first.End != 77 {
		t.Fatalf("the head page is [%d,%d), want [47,77) — the newest thirty", first.Start, first.End)
	}
	if last.Start != 0 || last.End != 17 {
		t.Fatalf("the walk's last page is [%d,%d), want [0,17) with the floor reached", last.Start, last.End)
	}
	if last.Floor != 0 {
		t.Fatalf("the walk ends at floor %d, want 0: nothing was pruned", last.Floor)
	}
}

// Output arriving BETWEEN pages must not duplicate or lose a row at the seam:
// the numbers the first page handed out still name the same rows after forty
// more lines have departed.
func TestHistoryPageAcrossArrivingOutputHasNoSeamDuplicate(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 60) // history L000000..L000036

	first, err := s.ReadHistoryPage(nil, 10)
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if first.Start != 27 || first.End != 37 {
		t.Fatalf("the first page is [%d,%d), want [27,37)", first.Start, first.End)
	}
	historyPageWant(t, historyPageTexts(first), 27, 37)

	obsFeed(t, s, 60, 40) // forty more lines depart while the caller reads

	second, err := s.ReadHistoryPage(&first.Start, 10)
	if err != nil {
		t.Fatalf("read the second page: %v", err)
	}
	if second.Start != 17 || second.End != 27 {
		t.Fatalf("the second page is [%d,%d), want [17,27) — the arrivals moved the head, not the cursor", second.Start, second.End)
	}
	historyPageWant(t, historyPageTexts(second), 17, 27)
	if second.Floor != 0 {
		t.Fatalf("the second page states floor %d, want 0", second.Floor)
	}
}

// A page whose range reaches below what is retained is answered with the
// floor, explicitly — the empty interval at the cursor, with the floor named
// — never a short list a caller cannot tell from a short history. The floor
// the walk below runs into is an ED3's (the one prune a test can produce
// honestly: the adapter clears the library's retention budget where the
// terminal is built, so nothing else prunes).
func TestHistoryPageBelowTheFloorIsAnsweredWithTheFloor(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 50) // history L000000..L000026, head 27

	first, err := s.ReadHistoryPage(nil, 10)
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if err = s.Ingest([]byte("\x1b[H\x1b[2J\x1b[3J")); err != nil {
		t.Fatalf("clear: %v", err)
	}

	p, err := s.ReadHistoryPage(nil, 30)
	if err != nil {
		t.Fatalf("read a page after the clear: %v", err)
	}
	if len(p.Rows) != 0 {
		t.Fatalf("the page after the clear carries %d rows, want none: the emulator's history is what it holds", len(p.Rows))
	}
	if p.Start != 27 || p.End != 27 {
		t.Fatalf("the page after the clear is [%d,%d), want the empty [27,27) at the head", p.Start, p.End)
	}
	if p.Floor != 27 {
		t.Fatalf("the page after the clear states floor %d, want 27 — where the live tier now begins", p.Floor)
	}
	if p.More {
		t.Fatal("the page after the clear says more, want false")
	}

	// The cursor a client held from BEFORE the clear points below the floor.
	// The answer is the floor again, not an error and not silence about it.
	stale := first.Start
	q, err := s.ReadHistoryPage(&stale, 30)
	if err != nil {
		t.Fatalf("read with the stale cursor: %v", err)
	}
	if len(q.Rows) != 0 || q.More {
		t.Fatalf("the stale cursor's page carries %d rows with more=%v, want the empty answer", len(q.Rows), q.More)
	}
	if q.Start != stale || q.End != stale {
		t.Fatalf("the stale cursor's page is [%d,%d), want the empty interval at the cursor [%d,%d)", q.Start, q.End, stale, stale)
	}
	if q.Floor != 27 {
		t.Fatalf("the stale cursor's page states floor %d, want 27", q.Floor)
	}
}

// After `clear` (ED3) in a session with no shell integration the emulator's
// history is simply what it holds, and output that scrolls off afterwards
// departs into a history whose numbering continues — no boundary is invented,
// and no row is renumbered by the clear.
func TestHistoryPageContinuesNumberingAfterClear(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 50)
	if err := s.Ingest([]byte("\x1b[H\x1b[2J\x1b[3J")); err != nil {
		t.Fatalf("clear: %v", err)
	}

	obsFeed(t, s, 50, 40) // seventeen of these lines depart into the fresh history

	p, err := s.ReadHistoryPage(nil, 5)
	if err != nil {
		t.Fatalf("read the head page: %v", err)
	}
	if p.Start != 39 || p.End != 44 {
		t.Fatalf("the head page is [%d,%d), want [39,44) — the clear spent no numbers and invented none", p.Start, p.End)
	}
	historyPageWant(t, historyPageTexts(p), 62, 67) // L000062..L000066
	if p.Floor != 27 {
		t.Fatalf("the head page states floor %d, want 27", p.Floor)
	}
	if !p.More {
		t.Fatal("the head page says no more, want true: rows remain below")
	}
}

// While the alternate screen holds the pane a page answers its own (empty)
// history and never the primary's rows; the primary's history is readable
// again, under the same numbers, once it is restored.
func TestHistoryPageOnTheAlternateScreenReturnsNoPrimaryRows(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 50) // primary history L000000..L000026

	if err := s.Ingest([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("enter the alternate screen: %v", err)
	}
	p, err := s.ReadHistoryPage(nil, 30)
	if err != nil {
		t.Fatalf("read a page on the alternate screen: %v", err)
	}
	if len(p.Rows) != 0 || p.More {
		t.Fatalf("the alternate screen's page carries %d rows with more=%v, want the empty answer", len(p.Rows), p.More)
	}
	if p.Start != 27 || p.End != 27 {
		t.Fatalf("the alternate screen's page is [%d,%d), want the empty [27,27) at the head", p.Start, p.End)
	}

	if err = s.Ingest([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("restore the primary screen: %v", err)
	}
	q, err := s.ReadHistoryPage(nil, 10)
	if err != nil {
		t.Fatalf("read a page after the restore: %v", err)
	}
	if q.Start != 17 || q.End != 27 {
		t.Fatalf("the restored page is [%d,%d), want [17,27) — the excursion moved nothing", q.Start, q.End)
	}
	historyPageWant(t, historyPageTexts(q), 17, 27)
}

// A pane that GREW pulled history rows back onto the screen: they are owed
// their departure, they are not in the history any more, and the head a page
// is measured against must say so — the first page may not name rows that are
// on the screen, and a small limit must not ask the emulator for positions
// past its Total.
func TestHistoryPageHeadExcludesRowsPulledBackOntoTheScreen(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 60) // history L000000..L000036, head 37

	if _, err := s.CommitGeometry(harnessGeometry(80, 48)); err != nil {
		t.Fatalf("grow the pane: %v", err)
	}
	// The growth refilled the screen from the history: L000013..L000036 are
	// on the screen again, each owed the departure it will not report again,
	// and the history holds L000000..L000012. One line departs: it pays one
	// row of that debt, so the history grows by a re-departure the head must
	// not count twice — and a ten-row page still asks the emulator only for
	// positions it retains.
	obsFeed(t, s, 60, 1)

	p, err := s.ReadHistoryPage(nil, 10)
	if err != nil {
		t.Fatalf("read the head page: %v", err)
	}
	if p.Start != 4 || p.End != 14 {
		t.Fatalf("the head page is [%d,%d), want [4,14) — the owed rows are on the screen, not in the history", p.Start, p.End)
	}
	// The fed line lands on the grown screen's one blank row and stays
	// there; the departure it causes is the screen's top row L000013
	// leaving a second time — debt-paid, so the history is L000000..L000013
	// with no gap and no duplicate.
	historyPageWant(t, historyPageTexts(p), 4, 14)
	if p.Floor != 0 {
		t.Fatalf("the head page states floor %d, want 0", p.Floor)
	}
	if !p.More {
		t.Fatal("the head page says no more, want true: L000000..L000002 remain below")
	}
	q, err := s.ReadHistoryPage(&p.Start, 10)
	if err != nil {
		t.Fatalf("read the page below: %v", err)
	}
	if q.Start != 0 || q.End != 4 || q.More {
		t.Fatalf("the page below is [%d,%d) with more=%v, want [0,4) with more=false", q.Start, q.End, q.More)
	}
	historyPageWant(t, historyPageTexts(q), 0, 4)
}

// A missing or out-of-range input is an error, never a silent default.
func TestHistoryPageRefusesAPathologicalLimit(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 30)

	if _, err := s.ReadHistoryPage(nil, 0); err == nil {
		t.Fatal("a zero limit was accepted, want an error")
	}
	if _, err := s.ReadHistoryPage(nil, -1); err == nil {
		t.Fatal("a negative limit was accepted, want an error")
	}
	if _, err := s.ReadHistoryPage(nil, MaxHistoryPageRows+1); err == nil {
		t.Fatal("a limit above the page bound was accepted, want an error")
	}
	_ = historyPageUint
}
