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

// ---------------------------------------------------------------------------
// Criterion two: a command nobody watched reads back with its output. This is
// the fence-first order: the sighting parks (an observable state), the
// authenticated completion arrives through the port, and the join seals the
// record.
// ---------------------------------------------------------------------------

// obsWatchedProgram prints a hundred numbered lines — far more than a screen —
// and then writes the fence, so every early row has LEFT the live rectangle by
// the time the authenticated boundary arrives.
var obsWatchedProgram = rawPreamble + `
printf '` + obsLines(0, 100) + `'
` + obsFence + `
printf 'OBS-DONE\n'
`

func TestACommandNobodyWatchedReadsBackWithItsOutput(t *testing.T) {
	p := startProgram(t, obsWatchedProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// Fence first: the sighting parks waiting for the authenticated half.
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
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

	// A hundred lines on twenty-four rows: the first twenty-four fill the
	// screen, and every line after — the hundredth's own newline included —
	// scrolls one off. Seventy-seven departures, first line first, last line
	// last.
	if len(rec.Departed) != 78 {
		t.Fatalf("the record holds %d departed rows, want 78 (100 lines on a 24-row screen, plus the sentinel line's own newline)", len(rec.Departed))
	}
	if first, last := obsRowText(rec.Departed[0]), obsRowText(rec.Departed[len(rec.Departed)-1]); first != "L000000" || last != "L000077" {
		t.Fatalf("the departed rows run %q..%q, want L000000..L000077, oldest first", first, last)
	}

	// And the closing screen is the screen AT the boundary — the tail the
	// person could see, not the output the command printed.
	if len(rec.Closing.Lines) != 24 {
		t.Fatalf("the closing screen is %d rows, want the 24 the interval ran at", len(rec.Closing.Lines))
	}
	closing := false
	for _, r := range rec.Closing.Lines {
		if strings.Contains(obsRowText(r), "OBS-DONE") {
			closing = true
		}
	}
	if !closing {
		t.Fatal("the closing screen does not hold the program's last line")
	}
}

// A command that produces no output at all still ran an interval; its record
// reads back empty rather than not existing.
var obsSilentProgram = rawPreamble + `
` + obsFence + `
printf 'OBS-DONE'
`

func TestACommandWithNoOutputStillLeavesARecord(t *testing.T) {
	p := startProgram(t, obsSilentProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	obsWaitSeal(t, p, nonce)

	rec, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("an interval ran and sealed no record: the record is per interval, not per departure")
	}
	if len(rec.Departed) != 0 {
		t.Fatalf("a silent command's record holds %d departed rows", len(rec.Departed))
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

var obsCompletionFirstProgram = rawPreamble + `
printf '` + obsLines(0, 100) + `'
readhex 1 >/dev/null
` + obsFence + `
printf 'OBS-DONE\n'
`

func TestACompletionBeforeItsFenceSealsTheSameRecord(t *testing.T) {
	p := startProgram(t, obsCompletionFirstProgram, harnessGeometry(80, 24))
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
	if len(rec.Departed) != 78 {
		t.Fatalf("completion-first order holds %d departed rows, want the same 78", len(rec.Departed))
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
// only after the so-far read, then prints thirty more and the fence: one
// command, read in the middle.
var obsRunningProgram = rawPreamble + `
printf '` + obsLines(0, 60) + `'
readhex 1 >/dev/null
printf '` + obsLines(60, 30) + `'
` + obsFence + `
printf 'OBS-DONE\n'
`

func TestACommandStillRunningReadsBackWhatItHasPrintedSoFar(t *testing.T) {
	p := startProgram(t, obsRunningProgram, harnessGeometry(80, 24))
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
	if len(rec.Departed) != 37 {
		t.Fatalf("the open record holds %d departed rows, want the 37 printed so far", len(rec.Departed))
	}
	if first, last := obsRowText(rec.Departed[0]), obsRowText(rec.Departed[len(rec.Departed)-1]); first != "L000000" || last != "L000036" {
		t.Fatalf("the open record's departures run %q..%q, want L000000..L000036", first, last)
	}

	// The command finishes: the fence parks the sighting (observable), the
	// authenticated half arrives, and the SAME record closes with the whole
	// output — one record per interval, not one per read.
	p.typed("x")
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	p.s.AuthenticatedEvents().Completed(p.s.Incarnation(), nonce, 0)
	obsWaitSeal(t, p, nonce)

	sealed, ok := p.s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the finished command sealed no record")
	}
	// Thirty more lines ran after the read: sixty-plus-thirty minus
	// twenty-four, plus one — sixty-seven.
	if len(sealed.Departed) != 68 {
		t.Fatalf("the sealed record holds %d departed rows, want the whole 68", len(sealed.Departed))
	}
	if last := obsRowText(sealed.Departed[len(sealed.Departed)-1]); last != "L000067" {
		t.Fatalf("the sealed record's last departure is %q, want L000067", last)
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
