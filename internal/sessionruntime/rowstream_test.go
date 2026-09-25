package sessionruntime

import (
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// The streamed block output's helper half (nocx-2v80t.3.6): rows leave the
// runtime as they leave the screen, handed once each, in order, to the
// session's row stream; the interval's end marker follows its rows on the
// same ordered stream. The runtime keeps NO copy of departed rows — the
// record keeps boundaries, counts and the closing screen only — because the
// owner's decision gives the helper no second buffer: ghostty's own
// scrollback is the store (nocx-2v80t.3.4's decisions, 2026-09-23).
//
// Every test here runs over the real emulator (libghostty-vt behind its
// port) with the harness terminal, the way the observation record's tests
// do: the test controls every ingest, and nothing waits on a duration.

// rowEvent is one emission in stream order: a row batch or an interval's
// end marker. One slice, because the order between the two kinds is the
// seam's whole point — a row can never be attributed to the wrong interval.
type rowEvent struct {
	kind    string // "rows" | "end" | "clear"
	from    uint64
	lost    uint64
	rows    []emulator.Row
	nonce   FenceNonce
	endRow  uint64
	closing []emulator.Row
	noFence bool
}

// recordingRowStream is a RowStream that records emissions in arrival order.
type recordingRowStream struct {
	mu     sync.Mutex
	events []rowEvent
}

func (r *recordingRowStream) OutputRows(from uint64, rows []emulator.Row, lost uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, rowEvent{kind: "rows", from: from, lost: lost, rows: rows})
}

func (r *recordingRowStream) IntervalEnd(nonce FenceNonce, endRow uint64, closing []emulator.Row, settledWithoutFence bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, rowEvent{kind: "end", nonce: nonce, endRow: endRow, closing: closing, noFence: settledWithoutFence})
}

func (r *recordingRowStream) ClearBoundary() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, rowEvent{kind: "clear"})
}

func (r *recordingRowStream) snapshot() []rowEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rowEvent(nil), r.events...)
}

// streamSession is obsSession with a row stream bound from the start.
func streamSession(t *testing.T, g Geometry) (*Session, *recordingRowStream) {
	t.Helper()
	s := obsSession(t, g)
	rs := &recordingRowStream{}
	s.SetRowStream(rs)
	return s, rs
}

// streamRowText reads one streamed row's text the way the record's reader
// does: the graphemes that carry text, trailing blanks dropped.
func streamRowText(r emulator.Row) string {
	var sb strings.Builder
	for _, c := range r.Cells {
		if c.Grapheme != "" {
			sb.WriteString(c.Grapheme)
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// The boundary's two halves are ONE instant. The end marker stops at the row
// the interval departed to and carries the screen it sat on, both read under
// the session's lock, so the screen's k-th row is exactly the row that leaves
// the screen at absolute index EndRow+k — and a consumer may hold the next
// interval to [EndRow, EndRow+len(Closing)) without dropping a row that is
// the next command's own. This is the relation the coordinator's index floor
// rests on; if the two reads ever came from different instants, the floor
// would eat the successor's first rows (nocx-2v80t.3.9).
func TestTheClosingScreenCarriesTheRowsThatLeaveNext(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	// Thirty lines: the screen holds the last twenty-three, seven left it.
	obsFeed(t, s, 0, 30)
	obsSeal(t, s, obsNonce(1))

	events := rs.snapshot()
	if len(events) != 2 || events[1].kind != "end" {
		t.Fatalf("the stream carried %d events, want a row batch and an end marker", len(events))
	}
	end := events[1]
	closing := end.closing
	if len(closing) == 0 {
		t.Fatal("the end marker carries no closing screen")
	}

	// The next command's output pushes the boundary's screen off, row by row.
	// Those rows belong to the boundary's block already, so the runtime
	// declines to stream them a second time — and everything it does stream
	// after the boundary is the next command's own output, at the first index
	// the next command's own output left. The count of rows that left and the
	// first streamed index can no longer disagree.
	obsFeed(t, s, 30, 40)

	suppressed := s.SuppressedScreenRows()
	if suppressed == 0 {
		t.Fatal("no row of the boundary's screen left it: the boundary and its screen disagree")
	}
	// The suppression window (installed from the UNTRIMMED screen) may hold
	// one entry more than the interval's own closing append streamed: its
	// cursor row, blank at the fence and never part of this interval's own
	// output (nocx-2v80t.3.12; closingRowsForStream), still belongs in the
	// window so whatever departs there next is recognised and suppressed.
	if suppressed > uint64(len(closing))+1 {
		t.Fatalf("the runtime suppressed %d rows, want at most the closing screen's %d (+1 for its own trimmed cursor row)", suppressed, len(closing))
	}
	boundaryTexts := map[string]bool{}
	for _, row := range closing {
		boundaryTexts[streamRowText(row)] = true
	}

	events = rs.snapshot()
	var first *rowEvent
	for i := range events {
		if events[i].kind == "rows" && events[i].from >= end.endRow {
			first = &events[i]
			break
		}
	}
	if first == nil {
		t.Fatal("the next command's own rows never streamed")
	}
	// The batch that follows the boundary's held-back rows is the first
	// batch of the next command's own output, so it streams at the first
	// index no boundary row occupies. Its rows are not the boundary's rows.
	for _, row := range first.rows {
		text := streamRowText(row)
		if boundaryTexts[text] {
			t.Fatalf("a row of the boundary's own screen (%q) streamed again: the block already holds it", text)
		}
	}
	for _, e := range rs.snapshot() {
		if e.kind != "rows" || e.from < end.endRow {
			continue
		}
		for _, row := range e.rows {
			text := streamRowText(row)
			if boundaryTexts[text] {
				t.Fatalf("a row of the boundary's own screen (%q) streamed again: the block already holds it", text)
			}
		}
	}
}

// A pane that GROWS between a boundary and the next command pulls rows back
// out of the scrollback to fill the taller screen. They were reported as
// departed when they first left; the taller screen shows them again, and when
// they leave a second time the emulator reports them again. The rows arriving
// after a boundary are then NOT the closing screen's rows — they are rows the
// interval before it already stored — and a consumer holding the next
// interval to [EndRow, EndRow+len(Closing)) both stores the predecessor's
// tail again and drops the next command's own first rows. The port's own rule
// is that reflowed rows are not departures (emulator.Terminal.DepartedRows),
// so nothing that left before the boundary may be streamed after it.
func TestAGrowingPaneDoesNotReReportRowsThatAlreadyLeft(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	// Thirty lines on a twenty-four row screen: the last twenty-three stay,
	// and L000000..L000006 leave — that is the interval's own output.
	obsFeed(t, s, 0, 30)
	obsSeal(t, s, obsNonce(1))
	events := rs.snapshot()
	if len(events) != 2 || events[1].kind != "end" {
		t.Fatalf("the stream carried %d events, want a row batch and an end marker", len(events))
	}
	end := events[1]
	left := map[string]bool{}
	for _, e := range events {
		if e.kind != "rows" {
			continue
		}
		for _, row := range e.rows {
			left[streamRowText(row)] = true
		}
	}
	if len(left) != 7 {
		t.Fatalf("the interval departed %d rows, want the 7 a 24-row screen pushes off", len(left))
	}

	// The pane grows: the screen is taller, and ghostty refills it from the
	// scrollback — with rows that have already left.
	if _, err := s.CommitGeometry(harnessGeometry(80, 30)); err != nil {
		t.Fatalf("grow the pane: %v", err)
	}
	obsFeed(t, s, 30, 20)

	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for i, row := range e.rows {
			idx := e.from + uint64(i) // #nosec G115 -- a slice index
			text := streamRowText(row)
			if idx < end.endRow {
				continue
			}
			if left[text] {
				t.Fatalf("row %q left the screen before the boundary and was streamed again at index %d: a reflow is not a departure",
					text, idx)
			}
		}
	}
}

// Thirty numbered lines on a twenty-four row screen: the flood fills the
// live rectangle and seven rows leave it — the last line's own newline
// scrolls once more, so twenty-three remain. The stream carries those seven
// once, in order, tagged with their absolute positions; the interval's end
// marker follows them on the same stream and carries the rows still on
// screen. An ordinary feed carries no loss marker.
func TestDepartedRowsStreamOnceInOrderBeforeTheEnd(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 30)
	obsSeal(t, s, obsNonce(1))

	events := rs.snapshot()
	if len(events) != 2 {
		t.Fatalf("the stream carries %d events, want 2 (one row batch, one end marker)", len(events))
	}
	batch, end := events[0], events[1]
	if batch.kind != "rows" || end.kind != "end" {
		t.Fatalf("the stream carried %q then %q, want rows then end", batch.kind, end.kind)
	}
	if batch.from != 0 {
		t.Fatalf("the first batch names FromRow %d, want 0", batch.from)
	}
	if len(batch.rows) != 7 {
		t.Fatalf("the batch carries %d rows, want the 7 that left a 24-row screen", len(batch.rows))
	}
	if batch.lost != 0 {
		t.Fatalf("an ordinary feed carries lost=%d, want none", batch.lost)
	}
	for i, r := range batch.rows {
		if got, want := streamRowText(r), "L"+fmt6(i); got != want {
			t.Fatalf("streamed row %d reads %q, want %q — the rows must leave in order", i, got, want)
		}
	}
	if end.nonce != obsNonce(1) {
		t.Fatalf("the end marker names nonce %v, want the sealed interval's", end.nonce)
	}
	if end.endRow != 7 {
		t.Fatalf("the end marker stops at row %d, want 7 — everything the interval departed", end.endRow)
	}
	// 23, not the screen's 24: the trailing entry is the cursor's own row,
	// blank at the fence's instant, and the interval's own closing append
	// never carries a row it wrote nothing into (nocx-2v80t.3.12) — the
	// suppression window installed for what follows still holds all 24.
	if len(end.closing) != 23 {
		t.Fatalf("the end marker carries %d closing rows, want the screen's 24 minus its blank cursor row", len(end.closing))
	}
	if last := streamLastText(end.closing); last != "L000029" {
		t.Fatalf("the closing screen ends at %q, want L000029", last)
	}

	// The record itself holds no departed rows: the stream took them as they
	// left. Reading the record back names boundaries, counts, screens.
	rec, ok := s.ObservationFor(obsNonce(1))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("an ordinary streamed interval reads back %v, want complete", rec.Completeness)
	}
	if len(rec.Closing.Lines) != 24 {
		t.Fatalf("the record's closing screen is %d rows, want 24", len(rec.Closing.Lines))
	}
}

// streamLastText reads the last non-blank row's text: after a flood the
// cursor parks on an empty bottom row, and a blank row ends no screen.
func streamLastText(rows []emulator.Row) string {
	last := ""
	for _, r := range rows {
		if txt := streamRowText(r); txt != "" {
			last = txt
		}
	}
	return last
}

func fmt6(i int) string {
	digits := "000000"
	s := []byte(digits)
	for k := 5; k >= 0 && i > 0; k-- {
		s[k] = byte('0' + i%10)
		i /= 10
	}
	return string(s)
}

// The fence-first seal stays correct in the streaming world: the capture at
// a parking sighting fixes where the interval's rows STOP at that instant,
// and rows that depart after the sighting belong to the interval that
// follows — the end marker joins with the pre-sighting count even though
// more rows streamed while the authenticated half was on its way.
func TestRowsAfterTheFenceSightingDoNotJoinTheFencedInterval(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := obsNonce(7)

	obsFeed(t, s, 0, 10) // ten lines: nothing has left the screen
	if err := s.SightFence(nonce, []byte("fence-source")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousAwaitingAuthenticated {
		t.Fatalf("the sighting reads %s, want parked awaiting the authenticated half", rendezvousStateName(got))
	}

	// Twenty more lines while the completion is still away: seven rows
	// leave the screen and stream now — they are the NEXT interval's rows.
	obsFeed(t, s, 10, 20)

	s.Completed(s.Incarnation(), nonce, 0)
	if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
		t.Fatalf("after the join the rendezvous reads %s, want complete", rendezvousStateName(got))
	}

	events := rs.snapshot()
	if len(events) != 2 {
		t.Fatalf("the stream carries %d events, want 2 (the post-sighting rows, then the fenced interval's end)", len(events))
	}
	batch, end := events[0], events[1]
	if batch.kind != "rows" {
		t.Fatalf("the first event is %q, want the post-sighting row batch", batch.kind)
	}
	if len(batch.rows) != 7 || batch.from != 0 {
		t.Fatalf("the batch carries %d rows from %d, want 7 rows from 0", len(batch.rows), batch.from)
	}
	if end.kind != "end" || end.nonce != nonce {
		t.Fatalf("the second event is %q for nonce %v, want the fenced interval's end", end.kind, end.nonce)
	}
	if end.endRow != 0 {
		t.Fatalf("the fenced interval's end stops at row %d, want 0 — no row had left when the sighting took the capture", end.endRow)
	}
	if last := streamLastText(end.closing); last != "L000009" {
		t.Fatalf("the closing screen ends at %q, want L000009 — the screen AS the fence sat on it", last)
	}
}

// A row the emulator could not read is not silently skipped: the feed the
// emulator struck carries a counted loss marker on the stream, and the
// ordinary feeds around it carry none.
//
// The strike is scripted (harnessEmulator.StrikeNextDepartures): the adapter
// clears both retention budgets where the terminal is built, so exhausting the
// library's budget is no longer how a report goes unread (nocx-2v80t.3.9), and
// what this schedule judges is the stream's own accounting for one.
func TestAStruckFeedCarriesACountedLossMarker(t *testing.T) {
	screen, err := ghostty.New(harnessGeometry(80, 24))
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	emu := &harnessEmulator{Terminal: screen}
	s := obsSessionOver(t, harnessGeometry(80, 24), emu)
	rs := &recordingRowStream{}
	s.SetRowStream(rs)

	// Ordinary feed, the report that cannot be read, then two ordinary feeds:
	// the marker rides the batch that follows the strike, and nothing else
	// carries one.
	obsFeed(t, s, 0, 100)
	emu.StrikeNextDepartures(errHarnessStrike)
	obsFeed(t, s, 100, 100)
	obsFeed(t, s, 200, 100)
	obsFeed(t, s, 300, 100)

	flagged := 0
	for _, e := range rs.snapshot() {
		if e.kind == "rows" && e.lost > 0 {
			flagged++
		}
	}
	if flagged == 0 {
		t.Fatal("a report that could not be read carried no loss marker: the hole reached the consumer as silence")
	}
	if flagged != 1 {
		t.Fatalf("%d batches carry a loss marker, want exactly the struck feed's one", flagged)
	}
}

// A loss spends the indices it names (nocx-2v80t.3.26, finding 4): every
// batch's FromRow minus its LostRows is exactly where the batch before it
// ended, so a consumer can hold a loss to the stream position it happened at
// — and a hole at the very END of an interval, a struck feed that departed
// nothing readable, is a loss-only emission whose position the interval's end
// marker then agrees with, so the closing screen lands after the hole rather
// than on top of it. Paired with the ordinary feeds around it, which advance
// the index by exactly the rows they carry.
func TestALossSpendsTheIndicesItNames(t *testing.T) {
	screen, err := ghostty.New(harnessGeometry(80, 24))
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	emu := &harnessEmulator{Terminal: screen}
	s := obsSessionOver(t, harnessGeometry(80, 24), emu)
	rs := &recordingRowStream{}
	s.SetRowStream(rs)

	obsFeed(t, s, 0, 100)
	emu.StrikeNextDepartures(errHarnessStrike)
	obsFeed(t, s, 100, 100) // a struck feed WITH rows: the hole rides them
	obsFeed(t, s, 200, 100)
	// A struck feed that departs nothing: no newline on a full screen moves
	// no row off it. The hole is the interval's last event before its end.
	emu.StrikeNextDepartures(errHarnessStrike)
	if err := s.Ingest([]byte("no newline")); err != nil {
		t.Fatalf("ingest the tail: %v", err)
	}
	obsSeal(t, s, obsNonce(7))

	var next uint64
	started := false
	var tail *rowEvent
	var end *rowEvent
	flagged := 0
	for _, e := range rs.snapshot() {
		switch e.kind {
		case "rows":
			if started && e.from-e.lost != next {
				t.Fatalf("a batch at FromRow %d carries lost=%d, so its gap begins at %d — but the batch before ended at %d",
					e.from, e.lost, e.from-e.lost, next)
			}
			if e.lost > 0 {
				flagged++
			}
			started = true
			next = e.from + uint64(len(e.rows)) //nolint:gosec // a row count
			if len(e.rows) == 0 {
				ev := e
				tail = &ev
			}
		case "end":
			ev := e
			end = &ev
		}
	}
	if flagged != 2 {
		t.Fatalf("%d batches carry a loss, want the two struck feeds'", flagged)
	}
	if tail == nil || tail.lost != 1 {
		t.Fatalf("the struck feed that departed nothing reached the stream as %+v, want a loss-only emission of 1", tail)
	}
	if end == nil {
		t.Fatal("the interval's end never reached the stream")
	}
	if end.endRow != next {
		t.Fatalf("the end marker stops at row %d, want %d — the end of the hole the interval's last emission named", end.endRow, next)
	}
}

// The absolute row index is the SESSION's, not the stream consumer's: rows
// that departed before any consumer was bound still spent their indices, so
// a consumer bound late continues where the session left off.
func TestTheRowIndexIsTheSessionsNotTheConsumers(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 30) // seven rows leave with nobody watching

	rs := &recordingRowStream{}
	s.SetRowStream(rs)
	obsFeed(t, s, 30, 30) // thirty more, now streamed

	events := rs.snapshot()
	if len(events) != 1 || events[0].kind != "rows" {
		t.Fatalf("the late-bound stream carries %d events, want the one row batch", len(events))
	}
	batch := events[0]
	if batch.from != 7 {
		t.Fatalf("the batch names FromRow %d, want 7 — the seven rows nobody watched still spent indices 0..6", batch.from)
	}
	if first, last := streamRowText(batch.rows[0]), streamRowText(batch.rows[len(batch.rows)-1]); first != "L000007" || last != "L000036" {
		t.Fatalf("the batch carries %q..%q, want L000007..L000036", first, last)
	}
}

// clearRowStreamSeq is exactly what `clear(1)` emits (nocx-zg3k3.10.3's owner
// decision): home the cursor, erase the display, erase the saved lines.
const clearRowStreamSeq = "\x1b[H\x1b[2J\x1b[3J"

// TestClearBoundaryTravelsInOrderWithTheRowsAroundIt (nocx-2v80t.3.17): the
// erase lands on the SAME ordered stream as OutputRows and IntervalEnd, at
// the position it occurred — never before the interval it interrupts and
// never after the interval it happened inside. A command runs and closes
// (its own end marker), then the NEXT command prints, erases its saved
// lines mid-output, and keeps printing: the clear boundary must sit strictly
// after the first command's own end marker and strictly before the second
// command's own later rows, or a consumer reading the stream in order could
// attribute it to the wrong side of a row.
func TestClearBoundaryTravelsInOrderWithTheRowsAroundIt(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	// Command 1: output, then its authenticated boundary.
	obsFeed(t, s, 0, 30)
	obsSeal(t, s, obsNonce(1))

	// Command 2 (`clear`): some output, then it erases its own saved lines,
	// then more output — the shape an interval containing `clear` has.
	obsFeed(t, s, 1000, 30)
	if err := s.Ingest([]byte(clearRowStreamSeq)); err != nil {
		t.Fatalf("ingest the clear sequence: %v", err)
	}
	obsFeed(t, s, 2000, 30)
	obsSeal(t, s, obsNonce(2))

	events := rs.snapshot()
	clearAt := -1
	for i, e := range events {
		if e.kind == "clear" {
			if clearAt >= 0 {
				t.Fatalf("a second clear-boundary event arrived at %d, want exactly one (first at %d)", i, clearAt)
			}
			clearAt = i
		}
	}
	if clearAt < 0 {
		t.Fatalf("the clear boundary never reached the row stream: events were %v", eventKinds(events))
	}
	sawCommand1End := false
	for _, e := range events[:clearAt] {
		if e.kind == "end" {
			sawCommand1End = true
		}
	}
	if !sawCommand1End {
		t.Fatalf("command 1's own end marker did not precede the clear boundary: events were %v", eventKinds(events))
	}
	sawLaterRows := false
	for _, e := range events[clearAt+1:] {
		if e.kind == "rows" {
			sawLaterRows = true
		}
	}
	if !sawLaterRows {
		t.Fatalf("rows fed after the clear sequence did not stream after the clear boundary: events were %v", eventKinds(events))
	}
}

func eventKinds(events []rowEvent) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.kind
	}
	return kinds
}
