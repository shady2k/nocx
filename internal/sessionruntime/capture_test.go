package sessionruntime

// The capture record (nocx-2v80t.2.2): ONE record per settled execution
// interval, built by the runtime at the authenticated render boundary and
// handed to a sink the composition root wires — never reconstructed after
// the fact by a caller that was not there when the interval was live.
//
// The schedule drives a REAL emulator behind the runtime (harness_test.go's
// realRuntime), because the record's content is what the emulator held:
// the rows that left the screen during the interval are the emulator's own
// departure report, and the snapshots are the rows as they stood.

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// recordingCaptures is the sink a schedule reads the hand-off through: the
// records arrive in settle order, and nothing here decides what one is worth.
type recordingCaptures struct {
	mu  sync.Mutex
	got []CaptureRecord
}

func (r *recordingCaptures) Capture(rec CaptureRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, rec)
}

func (r *recordingCaptures) records() []CaptureRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CaptureRecord(nil), r.got...)
}

// renderRows is a test's own reading of a row list: graphemes joined per
// physical row, spacer cells skipped, soft wraps joined, hard newlines kept.
func renderRows(rows []emulator.Row) string {
	var sb strings.Builder
	for i, row := range rows {
		if i > 0 && !row.Continuation {
			sb.WriteByte('\n')
		}
		for _, c := range row.Cells {
			if c.Width == emulator.WidthSpacerTail {
				continue
			}
			sb.WriteString(c.Grapheme)
		}
	}
	return sb.String()
}

// The spine: one command's interval — its first byte, the rows it pushed off
// the screen, its authenticated boundary — becomes exactly one record on the
// sink, keyed by the meeting's nonce, carrying the completeness the runtime
// itself holds.
func TestASettledRendezvousHandsTheSinkOneCaptureRecord(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	// The interval opens with the session's first byte: what was on the
	// screen before it is the opening snapshot.
	if err := s.Ingest([]byte("first line\r\n")); err != nil {
		t.Fatalf("ingest the first line: %v", err)
	}
	// More output pushes the first row off the screen. The departure is the
	// visual change the closing screen can no longer show, and nothing but
	// the capture path drains the emulator's report before the settle.
	for i := 0; i < 30; i++ {
		if err := s.Ingest([]byte(fmt.Sprintf("filler %d\r\n", i))); err != nil {
			t.Fatalf("ingest filler %d: %v", i, err)
		}
	}

	nonce := fenceNonceFromString(t, setANoncedHex)
	// Completion first, fence second — the order ADR-0024 decision 7 names.
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	recs := sink.records()
	if len(recs) != 1 {
		t.Fatalf("the sink holds %d records, want exactly one per settled interval", len(recs))
	}
	rec := recs[0]
	if rec.Nonce != nonce {
		t.Fatalf("the record names nonce %v, want the settled meeting's %v", rec.Nonce, nonce)
	}
	if rec.At != s.Incarnation() {
		t.Fatalf("the record names incarnation %v, want %v", rec.At, s.Incarnation())
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("completeness = %v, want the runtime's own %v", rec.Completeness, s.Completeness())
	}
	if got := renderRows(rec.Departed); !strings.Contains(got, "first line") {
		t.Fatalf("the departed rows read %q, want the row the interval pushed off", got)
	}
	if got := renderRows(rec.Closing.Lines); !strings.Contains(got, "filler 29") {
		t.Fatalf("the closing screen reads %q, want the last output the boundary saw", got)
	}
	if got := strings.TrimSpace(renderRows(rec.Opening.Lines)); got != "" {
		t.Fatalf("the opening screen reads %q, want the empty screen the interval opened on", got)
	}
}

// The interval state advances at every settle: the SECOND record's opening
// is the first record's closing snapshot — the screen the second interval
// really began on — and its closing content is its own. A runtime whose
// opening froze at session start would hand every later record the same
// story twice.
func TestTheSecondIntervalsOpeningIsTheFirstRecordsClosing(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })

	if err := s.Ingest([]byte("first command\r\n")); err != nil {
		t.Fatalf("ingest the first command: %v", err)
	}
	nonceA := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonceA, 0)
	if err := s.SightFence(nonceA, []byte("$ ")); err != nil {
		t.Fatalf("settle the first interval: %v", err)
	}

	if err := s.Ingest([]byte("second command output\r\n")); err != nil {
		t.Fatalf("ingest the second command: %v", err)
	}
	nonceB := fenceNonceFromString(t, "0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b0b2b")
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonceB, 0)
	if err := s.SightFence(nonceB, []byte("$ ")); err != nil {
		t.Fatalf("settle the second interval: %v", err)
	}

	recs := sink.records()
	if len(recs) != 2 {
		t.Fatalf("the sink holds %d records, want one per interval", len(recs))
	}
	first, second := recs[0], recs[1]
	if got, want := renderRows(second.Opening.Lines), renderRows(first.Closing.Lines); got != want {
		t.Fatalf("the second interval opened on %q, want the first record's closing %q", got, want)
	}
	if got := renderRows(second.Closing.Lines); !strings.Contains(got, "second command output") {
		t.Fatalf("the second closing reads %q, want the second interval's own content", got)
	}
	if got := renderRows(second.Opening.Lines); strings.Contains(got, "second command output") {
		t.Fatalf("the second opening already holds %q: the boundary read is not where the record starts", got)
	}
}

// The two channels are ordered independently, so the record is produced once
// whatever order the halves arrive in, and a late duplicate half produces
// nothing more: the settle is the trigger, not the arrival.
func TestTheCaptureRecordIsProducedOnceWhateverOrderTheHalvesArriveIn(t *testing.T) {
	sink := &recordingCaptures{}
	s, _, _ := realRuntime(t, func(c *Config) { c.Captures = sink })
	if err := s.Ingest([]byte("hello\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	nonce := fenceNonceFromString(t, setANoncedHex)
	// Fence first this time: the sighting parks, the completion closes.
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	if got := len(sink.records()); got != 0 {
		t.Fatalf("a parked sighting produced %d records, want none before the settle", got)
	}
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if got := len(sink.records()); got != 1 {
		t.Fatalf("the settle produced %d records, want exactly one", got)
	}

	// A duplicate second half re-settles nothing: a settled meeting stays
	// settled, and its record was already handed over exactly once.
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("re-sight the fence: %v", err)
	}
	if got := len(sink.records()); got != 1 {
		t.Fatalf("duplicate halves produced %d records, want the one", got)
	}
}

// A runtime whose composition root wired no capture consumer — every runtime
// this package's own schedules build, and every session before this bead —
// settles exactly as before. The record is the runtime's own fact; the sink
// is somebody else's to provide.
func TestASettledRendezvousWithoutAConsumerStillSettles(t *testing.T) {
	s, _, _ := realRuntime(t)
	if err := s.Ingest([]byte("x\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	nonce := fenceNonceFromString(t, setANoncedHex)
	s.AuthenticatedEvents().Completed(s.Incarnation(), nonce, 0)
	if err := s.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
		t.Fatalf("the meeting is %v, want complete — the sink is not the settle's gate", got)
	}
}
