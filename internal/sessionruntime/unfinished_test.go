package sessionruntime

// The unfinished record (nocx-2v80t.2.4): a long command still running,
// whose first rows have already left the screen, is neither a finished block
// nor a live cell. Its open interval reads back as a record of its own —
// taken once, at the first departure, from a PEEK of the emulator's
// departure report so the settle-time drain keeps everything — and it is
// distinguishable from a settled record by an assertion: it names no nonce,
// carries no closing screen, and declares itself unfinished.

import (
	"fmt"
	"strings"
	"testing"
)

// TestAnOpenIntervalWhoseRowsDepartReadsBackAsAnUnfinishedRecord is
// criterion 1's runtime leg. The test must FAIL if an unfinished interval is
// indistinguishable from a finished one: State says unfinished, the nonce is
// the zero fence (no authenticated boundary exists), and there is no closing
// screen to close one.
func TestAnOpenIntervalWhoseRowsDepartReadsBackAsAnUnfinishedRecord(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	if err := s.Ingest([]byte("first line\r\n")); err != nil {
		t.Fatalf("ingest the first line: %v", err)
	}
	// A command still running, pushing its first rows off the screen.
	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("filler %d\r\n", i))); err != nil {
			t.Fatalf("ingest filler %d: %v", i, err)
		}
	}

	recs := sink.records()
	if len(recs) != 1 {
		t.Fatalf("the sink holds %d records, want exactly the open interval's one", len(recs))
	}
	rec := recs[0]
	if rec.State != CaptureUnfinished {
		t.Fatalf("the open interval reads back as %q, want %q: a record that cannot be told from a finished one is the defect this criterion exists to prevent", rec.State, CaptureUnfinished)
	}
	if rec.Nonce != (FenceNonce{}) {
		t.Fatalf("the unfinished record names nonce %v, want the zero fence: no authenticated boundary has closed anything", rec.Nonce)
	}
	if rec.Completeness != CompletenessNoFence {
		t.Fatalf("completeness = %v, want no-fence: the interval has no authenticated boundary yet", rec.Completeness)
	}
	if rec.DepartedHole {
		t.Fatal("the departure peek reported a hole the emulator never named")
	}
	if got := renderRows(rec.Departed); !strings.Contains(got, "first line") {
		t.Fatalf("the known-so-far rows read %q, want the row the command pushed off", got)
	}
	if got := strings.TrimSpace(renderRows(rec.Opening.Lines)); got != "" {
		t.Fatalf("the opening screen reads %q, want the screen the interval opened on", got)
	}
	// NOT a settled block: nothing closed, so nothing closed the screen.
	if rec.Closing.Cols != 0 || rec.Closing.Rows != 0 || len(rec.Closing.Lines) != 0 {
		t.Fatalf("the unfinished record carries closing %+v, want no closing screen at all", rec.Closing)
	}
	// And the read changed nothing live: the session is still running with
	// the stream state it had.
	if got := s.Completeness(); got != CompletenessComplete {
		t.Fatalf("the session's completeness moved to %v after the peek, want unchanged", got)
	}
}

// TestTheUnfinishedRecordIsTakenOncePerInterval: the record is a fact about
// the interval, stored once at its first departure — not one ask per ingest.
// A later boundary, not a later byte, is what produces the next record.
func TestTheUnfinishedRecordIsTakenOncePerInterval(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("filler %d\r\n", i))); err != nil {
			t.Fatalf("ingest filler %d: %v", i, err)
		}
	}
	for i := 0; i < 10; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("more %d\r\n", i))); err != nil {
			t.Fatalf("ingest more %d: %v", i, err)
		}
	}

	recs := sink.records()
	if len(recs) != 1 {
		t.Fatalf("the sink holds %d records while the command still runs, want 1", len(recs))
	}
	if recs[0].State != CaptureUnfinished {
		t.Fatalf("the record reads %q, want %q", recs[0].State, CaptureUnfinished)
	}
}

// TestTheSettleStillCoversTheWholeIntervalAfterAnUnfinishedSnapshot is the
// epic invariant, both halves: the peek that fed the unfinished record spent
// nothing, so the AUTHENTICATED boundary still produces a finished record
// covering every row the interval departed — and the unauthenticated read
// closed nothing.
func TestTheSettleStillCoversTheWholeIntervalAfterAnUnfinishedSnapshot(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	if err := s.Ingest([]byte("first line\r\n")); err != nil {
		t.Fatalf("ingest the first line: %v", err)
	}
	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("filler %d\r\n", i))); err != nil {
			t.Fatalf("ingest filler %d: %v", i, err)
		}
	}
	if recs := sink.records(); len(recs) != 1 || recs[0].State != CaptureUnfinished {
		t.Fatalf("before the boundary the sink holds %+v, want one unfinished record", sink.records())
	}

	// The command finishes: completion first, fence second — the order
	// ADR-0024 decision 7 names. Only this authenticated pair may produce
	// a finished record.
	nonce := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	recs := sink.records()
	if len(recs) != 2 {
		t.Fatalf("the sink holds %d records, want the unfinished one plus the settled one", len(recs))
	}
	fin := recs[1]
	if fin.State != CaptureSettled {
		t.Fatalf("the boundary's record reads %q, want %q", fin.State, CaptureSettled)
	}
	if fin.Nonce != nonce {
		t.Fatalf("the finished record names nonce %v, want the authenticated %v", fin.Nonce, nonce)
	}
	// The whole interval: the peek spent nothing, so the settled record's
	// departed rows start where the interval began — the unfinished
	// snapshot took a copy, never the rows themselves. 31 lines on a
	// 24-row screen depart 8 rows: first line through filler 6. Filler 29
	// never left the screen, so it belongs to the closing snapshot alone.
	got := renderRows(fin.Departed)
	if !strings.Contains(got, "first line") || !strings.Contains(got, "filler 6") {
		t.Fatalf("the finished record's departed rows read %q, want the whole interval's departures", got)
	}
	if strings.Contains(got, "filler 29") {
		t.Fatalf("the finished record's departed rows read %q: a row still on the screen never departed", got)
	}
	if got := renderRows(fin.Closing.Lines); !strings.Contains(got, "filler 29") {
		t.Fatalf("the closing screen reads %q, want the last output the boundary saw", got)
	}
}

// TestANewIntervalPeeksFresh is the interval boundary: the record is per
// interval, so the NEXT command's open interval reads back unfinished on its
// own first departure, without the previous interval's flag still standing.
func TestANewIntervalPeeksFresh(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("filler %d\r\n", i))); err != nil {
			t.Fatalf("ingest filler %d: %v", i, err)
		}
	}
	nonce := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("settle the first interval: %v", err)
	}
	if recs := sink.records(); len(recs) != 2 {
		t.Fatalf("after the first boundary the sink holds %d records, want 2", len(recs))
	}

	// The second command runs long enough to depart rows of its own.
	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("second %d\r\n", i))); err != nil {
			t.Fatalf("ingest second %d: %v", i, err)
		}
	}
	recs := sink.records()
	// The first rows to depart in the new interval are the OLDEST rows
	// the boundary screen still held (filler 7 onward) — not the new
	// command's first lines, and never anything the first interval
	// already departed.
	third := recs[2]
	if third.State != CaptureUnfinished {
		t.Fatalf("the new interval's record reads %q, want %q", third.State, CaptureUnfinished)
	}
	got := renderRows(third.Departed)
	if !strings.Contains(got, "filler 7") {
		t.Fatalf("the new interval's rows read %q, want the boundary screen's oldest row departing first", got)
	}
	if strings.Contains(got, "first line") || strings.Contains(got, "filler 0\n") {
		t.Fatalf("the new interval's record carried the previous interval's departed rows: %q", got)
	}
}
