package sessionruntime

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// The observation record's failure paths and bounds, over the REAL emulator
// (libghostty-vt behind its port) driven directly: a Session ingests program
// bytes the way the pump would, and the authenticated boundary arrives
// through [Session.Completed] and [Session.SightFence] the way the contract
// delivers them. The loss causes the acceptance names each get a test, and
// the store is MEASURED at the three geometries the brief names, with
// scrollback behind it — a bound asserted but never measured is how the
// retired attempt shipped a record that crossed no frame (ADR-0072).
//
// Since nocx-2v80t.3.6 the departed rows are not here at all: they stream
// out as they leave the screen (rowstream.go; rowstream_test.go owns the
// stream's own tests), and the record keeps boundaries, counts and the two
// screens.

// obsSession builds a runtime over a real ghostty emulator and a harness
// terminal: no PTY and no pump, so the test controls every ingest.
func obsSession(t *testing.T, g Geometry) *Session {
	t.Helper()
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	return obsSessionOver(t, g, screen)
}

// obsSessionOver is obsSession with the screen left to the caller: a schedule
// that must SEE a departure report which cannot be read hands in the real
// adapter wrapped in the harness instrument that strikes one, while obsSession
// itself stays the plain shape the rest of the suite uses.
func obsSessionOver(t *testing.T, g Geometry, screen emulator.Terminal) *Session {
	t.Helper()
	s, err := New(Config{
		Incarnation:  Incarnation{Session: "observation", Generation: 1},
		Geometry:     g,
		Terminal:     newHarnessTerminal(g),
		Emulator:     screen,
		Completeness: CompletenessComplete,
	})
	if err != nil {
		t.Fatalf("build the runtime over the real emulator: %v", err)
	}
	return s
}

// obsSeal drives one authenticated boundary through the contract: the
// completion parks, the fence sighted on the stream joins it, and the record
// seals at that join.
func obsSeal(t *testing.T, s *Session, nonce FenceNonce) {
	t.Helper()
	s.Completed(s.Incarnation(), nonce, 0)
	s.SightFenceBoundary(t, nonce)
	if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
		t.Fatalf("the boundary reads %s, want complete", rendezvousStateName(got))
	}
}

// SightFenceBoundary is one fence sighting, the test's spelling of the join.
// The completion may park the interval (the screen is never read at the
// completion); the sighting is what carries the boundary and seals it, so
// every harness caller lands both halves.
func (s *Session) SightFenceBoundary(t *testing.T, nonce FenceNonce) {
	t.Helper()
	if err := s.SightFence(nonce, []byte("fence-source")); err != nil {
		t.Fatalf("sight the fence that joins the boundary: %v", err)
	}
}

// obsFeed is one ingest of n numbered lines starting at from — the same
// L%06d lines the real-PTY tests print, fed the way a carrier hands bytes.
func obsFeed(t *testing.T, s *Session, from, n int) {
	t.Helper()
	var sb strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&sb, "L%06d\r\n", i)
	}
	if err := s.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest %d lines: %v", n, err)
	}
}

func obsNonce(k byte) FenceNonce {
	var n FenceNonce
	for i := range n {
		n[i] = k
	}
	return n
}

// ---------------------------------------------------------------------------
// Cause one: a feed whose departures could not be READ. The rows that left in
// that feed are unread; the record carries the loss as feeds struck and the
// bytes those feeds carried, and its completeness degrades to Evicted — never
// presented as the whole of the output.
//
// The strike is scripted (harnessEmulator.StrikeNextDepartures) rather than
// provoked by exhausting the library's retention: the adapter now clears both
// budgets where the terminal is built, because inheriting the library's turned
// a whole command's output into nothing at all (nocx-2v80t.3.9). What this
// test judges is the RUNTIME's answer to a report that could not be read, and
// a premise that depends on a library default stops testing that the moment
// the default changes.
// ---------------------------------------------------------------------------

func TestAStruckDepartureReportIsACountedNamedLoss(t *testing.T) {
	screen, err := ghostty.New(harnessGeometry(80, 24))
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	emu := &harnessEmulator{Terminal: screen}
	s := obsSessionOver(t, harnessGeometry(80, 24), emu)

	// An ordinary feed, then a report the emulator cannot read, then the feed
	// that follows it: the loss is inside the interval, and the interval is a
	// whole one.
	obsFeed(t, s, 0, 100)
	emu.StrikeNextDepartures(errHarnessStrike)
	obsFeed(t, s, 100, 100)
	obsFeed(t, s, 200, 100)

	if s.observation == nil {
		t.Fatal("the interval in flight has no record")
	}
	if got := s.observation.Loss.RetentionFeeds; got != 1 {
		t.Fatalf("the open record counts %d struck feeds, want the one report that could not be read", got)
	}
	retentionBytes := s.observation.Loss.RetentionFeedBytes
	if retentionBytes == 0 {
		t.Fatal("a struck feed names no feed bytes: the loss is not countable")
	}

	obsSeal(t, s, obsNonce(1))
	rec, ok := s.ObservationFor(obsNonce(1))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if rec.Loss.RetentionFeeds == 0 {
		t.Fatal("the sealed record dropped the loss")
	}
	if rec.Loss.RetentionFeedBytes != retentionBytes {
		t.Fatalf("the sealed record counts %d feed bytes, want the %d drained in flight", rec.Loss.RetentionFeedBytes, retentionBytes)
	}
	if rec.Completeness != CompletenessEvicted {
		t.Fatalf("a record whose feed could not be read reads back %v, want evicted", rec.Completeness)
	}
}

// ---------------------------------------------------------------------------
// Cause two: output lost BEFORE the emulator saw it. Bytes that never became
// rows are counted in bytes, exactly as the runtime was told, and the claim
// degrades below evicted: the stream itself was short.
// ---------------------------------------------------------------------------

func TestLostIngestIsCountedOnTheRecordInBytes(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 40)
	if err := s.ReportHole(1234); err != nil {
		t.Fatalf("report the hole: %v", err)
	}
	obsFeed(t, s, 40, 40)
	if err := s.ReportHole(5); err != nil {
		t.Fatalf("report the second hole: %v", err)
	}
	obsFeed(t, s, 80, 40)

	// The record as it stands, before any boundary: the loss is already
	// readable on the open interval.
	open, ok := s.OpenObservation()
	if !ok {
		t.Fatal("the interval in flight has no record")
	}
	if open.Loss.IngestLostBytes != 1239 {
		t.Fatalf("the open record counts %d lost bytes, want 1239", open.Loss.IngestLostBytes)
	}

	obsSeal(t, s, obsNonce(2))
	rec, ok := s.ObservationFor(obsNonce(2))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if rec.Loss.IngestLostBytes != 1239 {
		t.Fatalf("the sealed record counts %d lost bytes, want 1239", rec.Loss.IngestLostBytes)
	}
	if rec.Completeness != CompletenessLostIngest {
		t.Fatalf("a record whose stream lost bytes reads back %v, want lost-ingest", rec.Completeness)
	}
}

// A hole reported before the interval opened — the attach-time hole, before
// any byte of it was ever ingested — reaches the record that opens next: the
// count does not die in the gap, and the cause is named on the record, not
// only in the session's own state.
func TestAHoleBeforeTheFirstIngestIsCountedOnTheFirstRecord(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	if err := s.ReportHole(777); err != nil {
		t.Fatalf("report the attach-time hole: %v", err)
	}

	obsFeed(t, s, 0, 40)
	obsSeal(t, s, obsNonce(4))
	rec, ok := s.ObservationFor(obsNonce(4))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if rec.Loss.IngestLostBytes != 777 {
		t.Fatalf("the first record carries %d lost bytes, want the 777 reported before it opened", rec.Loss.IngestLostBytes)
	}
	if rec.Completeness != CompletenessLostIngest {
		t.Fatalf("a record whose stream was short before it began reads back %v, want lost-ingest", rec.Completeness)
	}
	// The gap is carried once: the next record inherits nothing of it.
	obsFeed(t, s, 100, 40)
	obsSeal(t, s, obsNonce(5))
	second, ok := s.ObservationFor(obsNonce(5))
	if !ok {
		t.Fatal("the second interval sealed no record")
	}
	if second.Loss.IngestLostBytes != 0 {
		t.Fatalf("the second record carries %d lost bytes, want 0 — the gap was already counted", second.Loss.IngestLostBytes)
	}
}

// ---------------------------------------------------------------------------
// The record keeps no rows (nocx-2v80t.3.6): an interval that departs far
// more than a screen streams every row out and stays lean — no bound, no
// eviction count, no copy. Paired: the same interval's completeness reads
// complete, because nothing was lost — the rows left, they did not vanish.
// ---------------------------------------------------------------------------

func TestAHeavyIntervalStreamsItsRowsAndKeepsNoCopy(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	// Six hundred lines: 577 departures on a twenty-four row screen — a
	// flood the old record's bound would have truncated, streamed whole.
	obsFeed(t, s, 0, 600)

	const departed = 600 - 23
	var streamed uint64
	first, last := "", ""
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			t.Fatalf("a running interval streamed a %q event, want row batches only", e.kind)
		}
		if len(e.rows) == 0 {
			continue
		}
		if got := e.from; got != streamed {
			t.Fatalf("a batch names FromRow %d with %d rows already streamed, want %d — the index never skips", got, streamed, streamed)
		}
		if streamed == 0 {
			first = streamRowText(e.rows[0])
		}
		last = streamRowText(e.rows[len(e.rows)-1])
		streamed += uint64(len(e.rows)) // #nosec G115 -- len is never negative
	}
	if streamed != departed {
		t.Fatalf("the stream carried %d rows, want every one of the %d departures", streamed, departed)
	}
	if first != "L000000" || last != fmt.Sprintf("L%06d", departed-1) {
		t.Fatalf("the stream ran %q..%q, want L000000..L%06d, oldest first", first, last, departed-1)
	}

	// The record as it stands holds none of them: boundaries, counts,
	// screens.
	rec, ok := s.OpenObservation()
	if !ok {
		t.Fatal("the interval in flight has no record")
	}
	if rec.Loss.RetentionFeeds != 0 || rec.Loss.RetentionFeedBytes != 0 {
		t.Fatalf("an ordinary flood reads back retention loss %d feeds/%d bytes, want none",
			rec.Loss.RetentionFeeds, rec.Loss.RetentionFeedBytes)
	}

	obsSeal(t, s, obsNonce(3))
	sealed, ok := s.ObservationFor(obsNonce(3))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if sealed.Completeness != CompletenessComplete {
		t.Fatalf("a streamed-whole interval reads back %v, want complete — rows that left were sent, not lost", sealed.Completeness)
	}
}

// ---------------------------------------------------------------------------
// The chaining invariant: the record a boundary seals opens the next
// interval on the screen it closed on, and the next record carries none of
// the previous command's output. One record per interval, and each opening
// is the screen its own interval began on.
// ---------------------------------------------------------------------------

func TestTheSecondIntervalsOpeningIsTheFirstRecordsClosing(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	obsFeed(t, s, 0, 40)
	obsSeal(t, s, obsNonce(1))
	first, ok := s.ObservationFor(obsNonce(1))
	if !ok {
		t.Fatal("the first interval sealed no record")
	}

	obsFeed(t, s, 100, 40)
	obsSeal(t, s, obsNonce(2))
	second, ok := s.ObservationFor(obsNonce(2))
	if !ok {
		t.Fatal("the second interval sealed no record")
	}

	if got := obsRowText(second.Opening.Lines[0]); strings.Contains(got, "L000100") {
		t.Fatalf("the second record's opening holds the second command's first output %q: it opened late", got)
	}
	for y, row := range first.Closing.Lines {
		if want, got := obsRowText(row), obsRowText(second.Opening.Lines[y]); want != got {
			t.Fatalf("the second record's opening row %d reads %q, want the first record's closing %q", y, got, want)
		}
	}
	if first.Sealed != second.Opened {
		t.Fatalf("the second interval opened at revision %d, want the first's boundary %d", second.Opened, first.Sealed)
	}
}

// ---------------------------------------------------------------------------
// The store of sealed records is bounded, and what it drops is counted: a
// vanished record must not look like a command that never ran.
// ---------------------------------------------------------------------------

func TestTheRecordStoreIsBoundedAndCountsItsEvictions(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	for k := byte(1); k <= MaxObservations+2; k++ {
		obsFeed(t, s, int(k)*100, 1)
		obsSeal(t, s, obsNonce(k))
	}

	recs := s.Observations()
	if len(recs) != MaxObservations {
		t.Fatalf("the store holds %d records, want the bound %d", len(recs), MaxObservations)
	}
	if got := s.ObservationsEvicted(); got != 2 {
		t.Fatalf("the store counts %d evictions, want 2", got)
	}
	// The oldest two are GONE: the first record retained is the third.
	if recs[0].Nonce != obsNonce(3) {
		t.Fatal("the store kept a record older than the first it was to keep")
	}
	if _, ok := s.ObservationFor(obsNonce(1)); ok {
		t.Fatal("a record the store evicted is still keyed by its nonce")
	}
}

// ---------------------------------------------------------------------------
// The store, MEASURED at the three geometries the brief names, with
// scrollback behind the interval. The accounting is stated in the helper:
// value shapes at unsafe.Sizeof, plus the bytes every grapheme and the
// slice capacities carry — the memory a sealed record costs a session now
// that it holds boundaries, counts and two screens and no rows
// (nocx-2v80t.3.6).
// ---------------------------------------------------------------------------

func obsBytesRow(r emulator.Row) int {
	n := int(unsafe.Sizeof(r)) + cap(r.Cells)*int(unsafe.Sizeof(emulator.Cell{}))
	for _, c := range r.Cells {
		n += len(c.Grapheme)
	}
	return n
}

func obsBytesScreen(scr ObservationScreen) int {
	n := int(unsafe.Sizeof(scr))
	for _, r := range scr.Lines {
		n += obsBytesRow(r)
	}
	return n
}

// obsBytes measures one record: every slice at its capacity, every row, and
// every grapheme's bytes.
func obsBytes(r ObservationRecord) int {
	n := int(unsafe.Sizeof(r))
	n += obsBytesScreen(r.Opening)
	n += obsBytesScreen(r.Closing)
	return n
}

func TestTheRecordStoreIsMeasuredAtTheThreeGeometries(t *testing.T) {
	for _, g := range []Geometry{
		harnessGeometry(80, 24),
		harnessGeometry(120, 40),
		harnessGeometry(200, 50),
	} {
		s, rs := streamSession(t, g)

		// Fill the screen with an interval's flood — numbered lines with
		// real scrollback behind them — so both screens the record keeps
		// were read out of a buffer that had real history behind it, and
		// so the measurement answers for a record that watched a real
		// command run.
		fed := 0
		for s.observation == nil || s.departedRows < 600 {
			obsFeed(t, s, fed, 100)
			fed += 100
			if fed > 100_000 {
				t.Fatal("the interval never departed; the drain is losing rows")
			}
		}
		obsSeal(t, s, obsNonce(9))
		rec, ok := s.ObservationFor(obsNonce(9))
		if !ok {
			t.Fatalf("%dx%d: the interval sealed no record", g.Cols, g.Rows)
		}
		var streamed uint64
		for _, e := range rs.snapshot() {
			streamed += uint64(len(e.rows)) // #nosec G115 -- len is never negative
		}
		if streamed != s.departedRows {
			t.Fatalf("%dx%d: the stream carried %d rows, want the session's %d", g.Cols, g.Rows, streamed, s.departedRows)
		}
		size := obsBytes(rec)
		t.Logf("%dx%d after a flood: sealed record is %d bytes (%d KiB), stream carried %d rows",
			g.Cols, g.Rows, size, size>>10, streamed)

		// The number the commit states: a record is two screens and the
		// counts — under 4 MiB a record at every shipped geometry, so a
		// full store of [MaxObservations] records stays under 32 MiB a
		// session even at the largest geometry the product sizes.
		if size > 4<<20 {
			t.Fatalf("%dx%d: a sealed record measures %d bytes, past the 4 MiB ceiling", g.Cols, g.Rows, size)
		}
		if store := size * MaxObservations; store > 32<<20 {
			t.Fatalf("%dx%d: a full store measures %d bytes, past the 32 MiB ceiling", g.Cols, g.Rows, store)
		}
	}
}
