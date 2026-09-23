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
// delivers them. The three loss causes the acceptance names each get a test,
// and the record's bound is MEASURED at the three geometries the brief names,
// with scrollback behind it — a bound asserted but never measured is how the
// retired attempt shipped a record that crossed no frame (ADR-0072).

// obsSession builds a runtime over a real ghostty emulator and a harness
// terminal: no PTY and no pump, so the test controls every ingest.
func obsSession(t *testing.T, g Geometry) *Session {
	t.Helper()
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
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
	if err := s.SightFence(nonce, []byte("fence-source")); err != nil {
		t.Fatalf("sight the fence that joins the boundary: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
		t.Fatalf("the boundary reads %s, want complete", rendezvousStateName(got))
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
// Cause one: the emulator's OWN retention pruned rows during a feed. The
// rows that left in that feed cannot be read; the record carries the loss
// as feeds struck and the bytes those feeds carried, and its completeness
// degrades to Evicted — never presented as the whole of the output.
// ---------------------------------------------------------------------------

func TestRetentionPruningMidIntervalIsACountedNamedLoss(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	// Deep enough into scrollback the library prunes whole pages inside a
	// feed (the emulator's own boundary test measures the flag firing well
	// inside sixty thousand rows at this pin). Feed numbered chunks until
	// the flag fires, then once more so a whole interval is behind it.
	flagged := 0
	for chunk := range 60 {
		obsFeed(t, s, chunk*1000, 1000)
		if s.observation != nil && s.observation.Loss.RetentionFeeds > 0 {
			flagged++
			break
		}
	}
	if flagged == 0 {
		t.Fatal("sixty thousand lines never struck the emulator's retention: the premise is broken at this pin")
	}
	retentionBytes := s.observation.Loss.RetentionFeedBytes
	if retentionBytes == 0 {
		t.Fatal("a retention hole names no feed bytes: the loss is not countable")
	}

	obsSeal(t, s, obsNonce(1))
	rec, ok := s.ObservationFor(obsNonce(1))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if rec.Loss.RetentionFeeds == 0 {
		t.Fatal("the sealed record dropped the retention loss")
	}
	if rec.Loss.RetentionFeedBytes != retentionBytes {
		t.Fatalf("the sealed record counts %d feed bytes, want the %d drained in flight", rec.Loss.RetentionFeedBytes, retentionBytes)
	}
	if rec.Completeness != CompletenessEvicted {
		t.Fatalf("a record the emulator pruned mid-interval reads back %v, want evicted", rec.Completeness)
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

// ---------------------------------------------------------------------------
// Cause three: the record's OWN bound. Past MaxObservationRows the oldest
// rows go, and the count is exact — the record held them and counted what it
// let go.
// ---------------------------------------------------------------------------

func TestTheRecordBoundEvictsTheOldestRowsAndCountsThem(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	// Six hundred lines: 577 departures on a twenty-four row screen — past
	// MaxObservationRows, and far below the emulator's retention boundary,
	// so the eviction is the record's own and nothing else's.
	obsFeed(t, s, 0, 600)

	departed := 600 - 23
	if departed <= MaxObservationRows {
		t.Fatalf("the test feeds %d departures, need more than the bound %d", departed, MaxObservationRows)
	}
	rec, ok := s.OpenObservation()
	if !ok {
		t.Fatal("the interval in flight has no record")
	}
	if len(rec.Departed) != MaxObservationRows {
		t.Fatalf("the record holds %d departed rows, want exactly the bound %d", len(rec.Departed), MaxObservationRows)
	}
	want := uint64(departed - MaxObservationRows) // #nosec G115 -- checked above: departed past the bound
	if rec.Loss.EvictedRows != want {
		t.Fatalf("the record counts %d evicted rows, want %d", rec.Loss.EvictedRows, want)
	}
	first, last := obsRowText(rec.Departed[0]), obsRowText(rec.Departed[len(rec.Departed)-1])
	if wantFirst, wantLast := fmt.Sprintf("L%06d", departed-MaxObservationRows), fmt.Sprintf("L%06d", departed-1); first != wantFirst || last != wantLast {
		t.Fatalf("the bound kept %q..%q, want %q..%q — the NEWEST rows", first, last, wantFirst, wantLast)
	}

	obsSeal(t, s, obsNonce(3))
	sealed, ok := s.ObservationFor(obsNonce(3))
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	if sealed.Completeness != CompletenessEvicted {
		t.Fatalf("a record that evicted rows reads back %v, want evicted", sealed.Completeness)
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

	if len(second.Departed) == 0 {
		t.Fatal("the second interval departed nothing; its opening was taken after its output")
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
// The bound, MEASURED at the three geometries the brief names, with
// scrollback behind the departures. The accounting is stated in the helper:
// value shapes at unsafe.Sizeof, plus the bytes every grapheme and the slice
// capacities carry — the memory a sealed record costs a session.
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
	n += cap(r.Departed) * int(unsafe.Sizeof(emulator.Row{}))
	for _, row := range r.Departed {
		n += obsBytesRow(row)
	}
	return n
}

func TestTheRecordBoundIsMeasuredAtTheThreeGeometries(t *testing.T) {
	for _, g := range []Geometry{
		harnessGeometry(80, 24),
		harnessGeometry(120, 40),
		harnessGeometry(200, 50),
	} {
		s := obsSession(t, g)

		// Fill the record's departures to the bound — and with them the
		// emulator's own scrollback, so the rows the record holds were
		// read out of a buffer that had real history behind them.
		fed := 0
		for s.observation == nil || len(s.observation.Departed) < MaxObservationRows {
			obsFeed(t, s, fed, 100)
			fed += 100
			if fed > 100_000 {
				t.Fatal("the record never reached its bound; the drain is losing rows")
			}
		}
		obsSeal(t, s, obsNonce(9))
		rec, ok := s.ObservationFor(obsNonce(9))
		if !ok {
			t.Fatalf("%dx%d: the interval sealed no record", g.Cols, g.Rows)
		}
		if len(rec.Departed) != MaxObservationRows {
			t.Fatalf("%dx%d: the record holds %d rows, want the bound", g.Cols, g.Rows, len(rec.Departed))
		}
		size := obsBytes(rec)
		t.Logf("%dx%d with scrollback: sealed record at the bound is %d rows departed, %d bytes (%d KiB)",
			g.Cols, g.Rows, len(rec.Departed), size, size>>10)

		// The number the commit states: the worst case the bound allows —
		// departures full, both screens carried, every cell a dense
		// grapheme — stays under 4 MiB a record, so a full store of
		// [MaxObservations] records stays under 32 MiB a session even at
		// the largest geometry the product sizes.
		if size > 4<<20 {
			t.Fatalf("%dx%d: a record at the bound measures %d bytes, past the 4 MiB ceiling", g.Cols, g.Rows, size)
		}
		if store := size * MaxObservations; store > 32<<20 {
			t.Fatalf("%dx%d: a full store measures %d bytes, past the 32 MiB ceiling", g.Cols, g.Rows, store)
		}
	}
}
