package sessionruntime

// The scrolled-away rows (nocx-2v80t.2.3): a command whose output exceeds
// the screen keeps the rows that scrolled away, and they read back after
// completion. The record is where that lives — Opening, Departed, Closing —
// and every test here reads it back the way a client attaching afterwards
// would, by rendering the record's own rows (capture_test.go's renderRows:
// soft wraps joined, hard newlines kept), never by asserting on the
// emulator's internals.
//
// Where the budget itself lives is the emulator's (internal/emulator/
// ghostty: the departure report flags an interval when the library's
// retention prunes a page mid-feed). What the runtime owns is the honesty
// of the record: an interval whose own departure report came back with a
// hole is never presented as complete.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// Criterion 1: three screens of numbered lines — the output the brief calls
// ordinary long output — read back through the stored record with every
// line, in order, none duplicated and none missing. The departed rows carry
// what left the screen; the closing screen carries the tail; joined, they
// are the command's output and nothing else.
func TestThreeScreensOfNumberedLinesReadBackWhole(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	const lines = 72 // three screens of the harness's 80x24 grid
	var feed strings.Builder
	var want strings.Builder
	for i := 0; i < lines; i++ {
		if i > 0 {
			feed.WriteString("\r\n")
			want.WriteByte('\n')
		}
		fmt.Fprintf(&feed, "%04d", i)
		fmt.Fprintf(&want, "%04d", i)
	}
	// The last numbered line is unterminated: a command's output ends
	// where its last line ends, and the record then holds exactly the
	// command's lines — no caret row to explain away.
	if err := s.Ingest([]byte(feed.String())); err != nil {
		t.Fatalf("ingest the command's output: %v", err)
	}

	nonce := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	recs := sink.records()
	if len(recs) != 1 {
		t.Fatalf("the sink holds %d records, want exactly one per settled interval", len(recs))
	}
	rec := recs[0]
	if rec.DepartedHole {
		t.Fatalf("the interval's departure report has a hole: three screens fit the budget whole")
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("completeness = %v, want complete for an interval nothing was lost from", rec.Completeness)
	}
	got := renderRows(rec.Departed) + "\n" + renderRows(rec.Closing.Lines)
	if got != want.String() {
		t.Fatalf("the record reads back as:\n%q\nwant every line in order, none missing, none duplicated:\n%q", got, want.String())
	}
}

// Criterion 2: a soft-wrapped long line reads back as ONE logical line —
// the physical rows it wrapped into carry the terminal's own wrap flags,
// and a reader that honours them rejoins exactly one line — while a hard
// newline reads back as two.
func TestASoftWrappedLineReadsBackAsOneLineAndAHardNewlineAsTwo(t *testing.T) {
	t.Run("soft wrap joins", func(t *testing.T) {
		sink := &recordingCaptures{}
		s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

		long := strings.Repeat("a", 200) // wraps into three 80-column rows
		if err := s.Ingest([]byte(long + "\r\n")); err != nil {
			t.Fatalf("ingest the long line: %v", err)
		}
		if err := s.Ingest([]byte(strings.Repeat("filler\r\n", 30))); err != nil {
			t.Fatalf("ingest the filler that scrolls the line away: %v", err)
		}
		nonce := fenceNonceFromString(t, setANoncedHex)
		s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
		if err := s.SightFence(nonce, []byte("$ ")); err != nil {
			t.Fatalf("sight the fence: %v", err)
		}

		recs := sink.records()
		if len(recs) != 1 {
			t.Fatalf("the sink holds %d records, want one", len(recs))
		}
		got := renderRows(recs[0].Departed)
		if !strings.HasPrefix(got, long+"\n") {
			t.Fatalf("the soft-wrapped line reads back as:\n%q\nwant ONE logical line of %d a's first", got, len(long))
		}
	})

	t.Run("hard newline splits", func(t *testing.T) {
		sink := &recordingCaptures{}
		s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

		if err := s.Ingest([]byte("alpha\r\nbeta\r\n")); err != nil {
			t.Fatalf("ingest the two lines: %v", err)
		}
		if err := s.Ingest([]byte(strings.Repeat("filler\r\n", 30))); err != nil {
			t.Fatalf("ingest the filler that scrolls them away: %v", err)
		}
		nonce := fenceNonceFromString(t, setANoncedHex)
		s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
		if err := s.SightFence(nonce, []byte("$ ")); err != nil {
			t.Fatalf("sight the fence: %v", err)
		}

		recs := sink.records()
		if len(recs) != 1 {
			t.Fatalf("the sink holds %d records, want one", len(recs))
		}
		got := renderRows(recs[0].Departed)
		if !strings.HasPrefix(got, "alpha\nbeta\n") {
			t.Fatalf("the hard-newlined pair reads back as:\n%q\nwant TWO logical lines, alpha then beta", got)
		}
	})
}

// Criterion 3: when the departure report itself comes back with a hole —
// the port's answer when retention pruned inside a feed and some of what
// left cannot be read — the record's completeness says so. A record that
// kept the stream's "complete" beside a holed row list would present the
// interval as whole, which is the one lie Completeness exists to prevent.
// The injected refusal drives the runtime's handling deterministically; the
// real retention event that produces this refusal is proven where it lives
// (internal/emulator/ghostty's departed tests).
func TestAHoledDepartureReportMarksTheRecordLostIngest(t *testing.T) {
	sink := &recordingCaptures{}
	hole := errors.New("ghostty: scrollback retention pruned during one feed (depth 1 -> 0): the rows that left in that feed cannot be read")
	s, _, _ := realRuntime(t, func(c *Config) {
		c.Captures = sink
		c.Emulator = &holedDeparture{Terminal: c.Emulator, err: hole}
	})

	if err := s.Ingest([]byte(strings.Repeat("filler\r\n", 30))); err != nil {
		t.Fatalf("ingest output that scrolls: %v", err)
	}
	nonce := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	recs := sink.records()
	if len(recs) != 1 {
		t.Fatalf("the sink holds %d records, want one", len(recs))
	}
	rec := recs[0]
	if !rec.DepartedHole {
		t.Fatalf("DepartedHole = false, want the departure report's own flag carried")
	}
	if rec.Completeness != CompletenessLostIngest {
		t.Fatalf("completeness = %v, want lost-ingest: the interval's rows are not the whole of what departed, and complete would be the lie this field exists to prevent", rec.Completeness)
	}
}

// holedDeparture is the real emulator with one failure injected: the
// departure report refuses, exactly the shape the port answers with when
// retention pruned inside a feed.
type holedDeparture struct {
	emulator.Terminal
	err error
}

func (h *holedDeparture) DepartedRows() ([]emulator.Row, error) { return nil, h.err }
