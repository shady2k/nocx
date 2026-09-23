package sessionruntime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The observation record, on a REAL PTY (nocx-zg3k3.5.2; ADR-0072). A card in
// the transcript is one command — the interval between its authenticated start
// and its authenticated completion — and the record is what the runtime holds
// of what was observed during that interval, INCLUDING rows no longer in the
// live rectangle. The screen at completion is not the command's output: a
// command whose rows scrolled away mid-run reads back from the record's
// departed rows, not from the live grid.
//
// Nothing here is mocked below the contract, and no client is attached for the
// duration of any of it: the terminal is internal/pty's real one, the emulator
// is libghostty-vt behind its port, and the authenticated completion arrives
// through [Session.AuthenticatedEvents] the way the helper delivers it.
// Nothing waits on a duration; every wait ends on an observable state.

// obsRowText reads one row's text the way a reader of the record would: the
// graphemes that carry text, in order, trailing blanks dropped.
func obsRowText(r emulator.Row) string {
	var sb strings.Builder
	for _, c := range r.Cells {
		if c.Grapheme != "" {
			sb.WriteString(c.Grapheme)
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// obsLines returns a printf FORMAT fragment printing n numbered lines,
// L%06d-shaped, starting at from. The escapes stay literal two-character
// sequences so the fragment can sit inside single quotes; printf turns them
// into carriage returns and newlines when the program runs.
func obsLines(from, n int) string {
	var sb strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&sb, `L%06d\r\n`, i)
	}
	return sb.String()
}

// obsFence is the printf FORMAT fragment that writes the render fence the
// way every real-PTY rendezvous program here writes it: escapes inside
// single quotes, so the shell hands them to printf and printf hands the
// sequence to the pty.
var obsFence = "printf '\\033]1337;NOCX_FENCE;" + fenceNonceHex + "\\007'"

func obsWaitSeal(t *testing.T, p *programSession, nonce FenceNonce) {
	t.Helper()
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousComplete)
}

// streamedTexts reads every row the stream has carried so far, in order —
// the read of a running interval, whose end marker has not come.
func streamedTexts(rs *recordingRowStream) []string {
	var out []string
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for _, r := range e.rows {
			out = append(out, streamRowText(r))
		}
	}
	return out
}

// streamedInterval reads the stream up to its first end marker and answers
// the texts of the rows that interval owned (FromRow < the end marker's
// EndRow), the end marker itself, and the texts streamed after it — the rows
// that departed once the boundary had already been taken, which belong to
// the interval that follows.
func streamedInterval(rs *recordingRowStream) (in []string, end rowEvent, after []string, ok bool) {
	events := rs.snapshot()
	for i, e := range events {
		if e.kind != "end" {
			continue
		}
		for _, prev := range events[:i] {
			if prev.kind != "rows" {
				continue
			}
			for j, r := range prev.rows {
				idx := prev.from + uint64(j) // #nosec G115 -- j is a slice index, never negative
				if txt := streamRowText(r); idx < e.endRow {
					in = append(in, txt)
				} else {
					after = append(after, txt)
				}
			}
		}
		return in, e, after, true
	}
	return nil, rowEvent{}, nil, false
}

// ---------------------------------------------------------------------------
// Criterion two: a command nobody watched reads back with its output. This is
// the fence-first order: the sighting parks (an observable state), the
// authenticated completion arrives through the port, and the join seals the
// record.
// ---------------------------------------------------------------------------

// obsWatchedProgram prints a hundred numbered lines — far more than a screen —
// and then writes the fence, so every early row has LEFT the live rectangle by
// the time the authenticated boundary arrives.
// The post-fence line is written only after the runtime sends a byte: the
// sighting therefore always parks with nothing but the flood behind it, and
// the line demonstrably arrives AFTER the capture — the race the record's
// boundary used to lose, pinned on the passing side by construction.
var obsWatchedProgram = rawPreamble + `
printf '` + obsLines(0, 100) + `'
` + obsFence + `
readhex 1 >/dev/null
printf 'OBS-DONE\n'
`

func TestACommandNobodyWatchedReadsBackWithItsOutput(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, obsWatchedProgram, harnessGeometry(80, 24), rs)
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// Fence first: the sighting parks waiting for the authenticated half.
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	// The capture is taken at the sighting; the runtime's byte now makes the
	// program print its post-fence line, and the wait ends on the observable
	// that the line has been INGESTED — after the capture, before the
	// authenticated half. Whatever the record seals, it cannot honestly
	// contain this line.
	p.typed("x")
	p.wait("OBS-DONE")
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	obsWaitSeal(t, p, nonce)

	// The live rectangle cannot be the command's output: a hundred lines on a
	// twenty-four row screen left seventy-seven of them scrolled away, and the
	// fence itself is not painted. The screen the runtime serves holds only
	// the tail.
	if screen := p.screen(); strings.Contains(screen, "L000000") {
		t.Fatalf("the live screen still holds the command's first line; the test premise is broken:\n%s", screen)
	}

	rec, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("no observation record for the authenticated boundary: a command nobody watched left nothing to read")
	}
	if rec.Open() {
		t.Fatalf("the record for a completed boundary is still open (sealed revision %d)", rec.Sealed)
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("an ordinary watched command reads back %v, want complete", rec.Completeness)
	}
	if rec.Sealed <= rec.Opened {
		t.Fatalf("the record's revisions run opened=%d sealed=%d: the boundary did not move the clock the frames carry", rec.Opened, rec.Sealed)
	}

	// The boundary is where the fence sits in the byte stream. A hundred
	// lines on twenty-four rows: the first twenty-four fill the screen and
	// every line after scrolls one off — seventy-seven departures,
	// L000000..L000076, the flood whole and NOTHING of what came after the
	// fence. The sentinel line and its newline belong to the next interval.
	// The rows the flood departed streamed once each, in order, and the
	// interval's end marker followed them stopping at row 77. The rows that
	// departed after the capture (the sentinel's) stream after the end
	// marker: they are the next interval's.
	in, end, after, streamed := streamedInterval(rs)
	if !streamed {
		t.Fatal("no end marker ever streamed: the interval closed with nothing on the stream")
	}
	if len(in) != 77 || in[0] != "L000000" || in[len(in)-1] != "L000076" {
		t.Fatalf("the stream carried %d rows %q..%q for the interval, want 77 rows L000000..L000076, oldest first", len(in), in[0], in[len(in)-1])
	}
	if end.endRow != 77 {
		t.Fatalf("the end marker stops at row %d, want 77 — everything the flood departed", end.endRow)
	}
	if end.nonce != nonce {
		t.Fatalf("the end marker names nonce %v, want the sealed boundary's", end.nonce)
	}
	if len(after) == 0 {
		t.Fatal("the sentinel's departure never streamed after the end marker: the stream's order lies about which interval rows belong to")
	}

	// And the closing screen is the screen AT the fence — the flood's tail,
	// ending at L000099, with the post-fence line nowhere in it.
	if len(rec.Closing.Lines) != 24 {
		t.Fatalf("the closing screen is %d rows, want the 24 the interval ran at", len(rec.Closing.Lines))
	}
	lastText := ""
	for _, r := range rec.Closing.Lines {
		txt := obsRowText(r)
		if txt != "" {
			lastText = txt
		}
		if strings.Contains(txt, "OBS-DONE") {
			t.Fatalf("the closing screen holds post-fence output %q: the record closed after the boundary", txt)
		}
	}
	if lastText != "L000099" {
		t.Fatalf("the closing screen ends at %q, want L000099 — the flood's last line at the fence", lastText)
	}
}

// A command that produces no output at all still ran an interval; its record
// reads back empty rather than not existing.
var obsSilentProgram = rawPreamble + `
` + obsFence + `
printf 'OBS-DONE'
`

func TestACommandWithNoOutputStillLeavesARecord(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, obsSilentProgram, harnessGeometry(80, 24), rs)
	nonce := fenceNonceFromString(t, fenceNonceHex)
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	obsWaitSeal(t, p, nonce)

	rec, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("an interval ran and sealed no record: the record is per interval, not per departure")
	}
	in, end, after, streamed := streamedInterval(rs)
	if !streamed {
		t.Fatal("a silent command's interval streamed no end marker")
	}
	if len(in) != 0 || end.endRow != 0 || len(after) != 0 {
		t.Fatalf("a silent command streamed %d rows and stopped at %d with %d after, want none anywhere", len(in), end.endRow, len(after))
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("a silent command reads back %v, want complete: no output is not lost output", rec.Completeness)
	}
}

// ---------------------------------------------------------------------------
// Criterion two, other arrival order: the completion arriving BEFORE the fence
// is sighted. ADR-0024 decision 7: the two channels are ordered
// independently, and neither order is the error case. The program parks BEFORE
// writing its fence, so the completion lands while no fence byte exists
// anywhere in the stream — the ordering holds by construction, not by timing.
// ---------------------------------------------------------------------------

// Nothing follows the fence in this program: the record it produces is the
// same whichever way the chunking lands, so the paired order is judged on
// the record's content and never on where a read split the bytes.
var obsCompletionFirstProgram = rawPreamble + `
printf '` + obsLines(0, 100) + `'
readhex 1 >/dev/null
` + obsFence + `
`

func TestACompletionBeforeItsFenceSealsTheSameRecord(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, obsCompletionFirstProgram, harnessGeometry(80, 24), rs)
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// The program is parked before its fence: the output is in — its last
	// line still on the screen — and no sighting exists. The authenticated
	// half arrives first.
	p.wait("L000099")
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)

	// Now the fence: the sighting joins the parked completion, and the record
	// seals at that join.
	p.typed("x")
	obsWaitSeal(t, p, nonce)

	rec, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("completion-first order sealed no record")
	}
	// The fence ends the stream in this program: seventy-seven departures,
	// the flood to the fence and nothing else, whichever way the pump
	// chunked it.
	in, end, _, streamed := streamedInterval(rs)
	if !streamed {
		t.Fatal("completion-first order streamed no end marker")
	}
	if len(in) != 77 || in[0] != "L000000" || in[len(in)-1] != "L000076" {
		t.Fatalf("completion-first order streamed %d rows %q..%q, want the same 77 L000000..L000076", len(in), in[0], in[len(in)-1])
	}
	if end.endRow != 77 {
		t.Fatalf("completion-first order's end marker stops at %d, want 77", end.endRow)
	}
	if rec.Completeness != CompletenessComplete {
		t.Fatalf("completion-first order reads back %v, want complete", rec.Completeness)
	}
}

// ---------------------------------------------------------------------------
// Criterion three: a command still running reads back what it has printed so
// far — including rows that have already left the live rectangle.
// ---------------------------------------------------------------------------

// obsRunningProgram prints sixty lines, parks on a byte the test will send
// only after the so-far read, then prints thirty more and the fence. The
// sentinel AFTER the fence waits for a further byte, so it can never ride
// the fence's own chunk into the capture: what the record seals is decided
// by the stream's order, never by where a read split it.
var obsRunningProgram = rawPreamble + `
printf '` + obsLines(0, 60) + `'
readhex 1 >/dev/null
printf '` + obsLines(60, 30) + `'
` + obsFence + `
readhex 1 >/dev/null
printf 'OBS-DONE\n'
`

func TestACommandStillRunningReadsBackWhatItHasPrintedSoFar(t *testing.T) {
	rs := &recordingRowStream{}
	p := startProgramRows(t, obsRunningProgram, harnessGeometry(80, 24), rs)
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// The program is parked mid-command: sixty lines printed, no boundary in
	// sight. The screen holds the tail of what it printed (the wait ends on
	// that observable), and the first rows are already gone.
	p.wait("L000058")

	rec, ok := p.s.OpenObservation()
	if !ok {
		t.Fatal("a running command has no observation record to read")
	}
	if !rec.Open() {
		t.Fatalf("a running command's record is sealed at revision %d", rec.Sealed)
	}
	if rec.Nonce != (FenceNonce{}) {
		t.Fatal("an open record names a nonce: no authenticated boundary exists yet")
	}
	// Sixty lines on twenty-four rows: sixty minus twenty-four plus the last
	// line's own newline — thirty-seven departed so far, first line first.
	soFar := streamedTexts(rs)
	if len(soFar) != 37 || soFar[0] != "L000000" || soFar[len(soFar)-1] != "L000036" {
		t.Fatalf("the running command has streamed %d rows %q..%q, want the 37 L000000..L000036", len(soFar), soFar[0], soFar[len(soFar)-1])
	}

	// The command finishes: the fence parks the sighting (observable), the
	// sentinel line is then printed and INGESTED — an observable — and the
	// authenticated half arrives last. The record closes at the fence: the
	// ninety flood lines whole, and none of the post-fence sentinel.
	p.typed("x")
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	p.typed("y")
	p.wait("OBS-DONE")
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	obsWaitSeal(t, p, nonce)

	sealed, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the finished command sealed no record")
	}
	// Thirty more lines ran after the read, to the fence: ninety lines on
	// twenty-four rows — sixty-seven departures, L000000..L000066 — and the
	// post-fence sentinel's departure is the NEXT record's first.
	in, end, _, streamed := streamedInterval(rs)
	if !streamed {
		t.Fatal("the finished command streamed no end marker")
	}
	if len(in) != 67 || in[len(in)-1] != "L000066" {
		t.Fatalf("the interval streamed %d rows ending %q, want the whole 67 to the fence ending L000066", len(in), in[len(in)-1])
	}
	if end.endRow != 67 {
		t.Fatalf("the end marker stops at row %d, want 67", end.endRow)
	}
	if sealed.Opened != rec.Opened {
		t.Fatalf("the record changed identity across the boundary: opened %d became %d", rec.Opened, sealed.Opened)
	}
	if sealed.Completeness != CompletenessComplete {
		t.Fatalf("the finished command reads back %v, want complete", sealed.Completeness)
	}
}

// ---------------------------------------------------------------------------
// The failure paths. For every boundary that never arrives, nothing is
// manufactured: an interval with no authenticated boundary stays open and
// seals nothing, whatever its output was worth. The paired ordinary-machine
// case is the two tests above: a boundary that arrives seals exactly one.
// ---------------------------------------------------------------------------

// obsNoFenceProgram prints and parks: the completion arrives, the fence never
// does, and the bounded wait expires (the test drives the expiry as a call —
// a wait may not depend on timing).
var obsNoFenceProgram = rawPreamble + `
printf '` + obsLines(0, 40) + `'
readhex 1 >/dev/null
printf 'OBS-DONE\n'
`

func TestAnExpiredAuthenticatedBoundarySealsNothing(t *testing.T) {
	p := startProgram(t, obsNoFenceProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// The authenticated half arrives; its fence never will.
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	if err := p.s.ExpireRendezvous(nonce); err != nil {
		t.Fatalf("expire the meeting whose fence never came: %v", err)
	}
	if got := p.s.RendezvousFor(nonce).State; got != RendezvousExpired {
		t.Fatalf("the expired meeting reads %v, want expired", rendezvousStateName(got))
	}

	// Nothing sealed: the interval has no authenticated boundary, and a
	// record closed on one would dress an unbounded wait up as a boundary.
	if recs := p.s.Observations(); len(recs) != 0 {
		t.Fatalf("an expired boundary sealed %d records, want none", len(recs))
	}
	if _, ok := p.s.ObservationFor(nonce); ok {
		t.Fatal("a record is keyed by a nonce whose meeting expired")
	}
}

// A fence sighted with nothing authenticated behind it authorised nothing
// (ADR-0024 decision 1); its expiry seals nothing either.
func TestAnExpiredSightingSealsNothing(t *testing.T) {
	p := startProgram(t, obsWatchedProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// The fence is in the stream; no completion ever comes. The sighting
	// parks first — the wait ends on that observable state.
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	if err := p.s.ExpireRendezvous(nonce); err != nil {
		t.Fatalf("expire the parked sighting: %v", err)
	}
	if recs := p.s.Observations(); len(recs) != 0 {
		t.Fatalf("an expired sighting sealed %d records, want none — a fence authorises nothing", len(recs))
	}
}
