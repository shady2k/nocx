package sessionruntime

import (
	"strings"
	"testing"
)

// nextFenceSplit's own bounds (nocx-2v80t.3.12): the split is a mechanical
// hint, never an authority, so its unit tests are about where it agrees the
// SPLIT falls, not about whether a fence is authentic — sightDrainedFenceLocked
// and the emulator's own scanner decide that regardless of where Ingest
// happened to pause.

func TestNextFenceSplitFindsTheMarkersOwnEnd(t *testing.T) {
	nonce := strings.Repeat("ab", 32)
	b := []byte("output\r\n" + fenceMarkerPrefix + nonce + "\x07trailing bytes")
	end, ok := nextFenceSplit(b)
	if !ok {
		t.Fatal("want a split, got none")
	}
	want := len("output\r\n" + fenceMarkerPrefix + nonce + "\x07")
	if end != want {
		t.Fatalf("split at %d, want %d (just past the BEL)", end, want)
	}
}

func TestNextFenceSplitAnswersFalseWithNoMarker(t *testing.T) {
	if _, ok := nextFenceSplit([]byte("plain output, no fence here at all")); ok {
		t.Fatal("found a split where there is no fence")
	}
}

func TestNextFenceSplitAnswersFalseOnAPartialMarker(t *testing.T) {
	// The prefix and part of the nonce, with the rest arriving in a later
	// Ingest call — ordinary for a fence that straddles two pty reads. The
	// emulator's own stateful scanner completes it; this function must not
	// invent a split point past the end of what it was given.
	partial := fenceMarkerPrefix + strings.Repeat("ab", 10)
	if _, ok := nextFenceSplit([]byte(partial)); ok {
		t.Fatal("found a split in a marker that has not finished arriving")
	}
}

func TestNextFenceSplitRejectsALookAlikeWithBadHexOrTerminator(t *testing.T) {
	// Uppercase hex is not the wire's spelling, and a non-BEL terminator is
	// not the marker at all: this function must not manufacture authority
	// the emulator's own scanner would refuse.
	upper := fenceMarkerPrefix + strings.ToUpper(strings.Repeat("ab", 32)) + "\x07"
	if _, ok := nextFenceSplit([]byte(upper)); ok {
		t.Fatal("matched uppercase hex, which the wire format forbids")
	}
	badTerm := fenceMarkerPrefix + strings.Repeat("ab", 32) + "\n"
	if _, ok := nextFenceSplit([]byte(badTerm)); ok {
		t.Fatal("matched a sequence with no BEL terminator")
	}
}

// ---------------------------------------------------------------------------
// The closing screen is read at the fence's own instant, not at the end of
// whatever pty read happened to carry it (nocx-2v80t.3.12). nocx.bash's
// PROMPT_COMMAND writes the fence and then, with nothing to flush in
// between, OSC 133 D, 133 A, OSC 7 and the visible PS1 text — and an
// ordinary pty read often hands Ingest all of it in one feed. Before the
// fence split existed, the closing screen read after the WHOLE feed applied,
// so its last row held the next prompt's own text instead of what the fence
// actually saw.
// ---------------------------------------------------------------------------

func TestAFenceBatchedWithTrailingPromptBytesClosesOnTheFencesOwnScreen(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	nonce := obsNonce(0x11)

	obsFeed(t, s, 0, 5)
	obsSeal(t, s, nonce)

	nonce2Hex := strings.Repeat("cd", 32)
	var nonce2 FenceNonce
	copy(nonce2[:], mustDecodeHexForTest(t, nonce2Hex))

	obsFeed(t, s, 100, 3)
	s.Completed(s.Incarnation(), nonce2, 0)

	// The fence, immediately followed — in the SAME Ingest call — by the
	// bytes nocx.bash's PROMPT_COMMAND writes right after it: 133;D, 133;A,
	// OSC 7 and the visible prompt text, with nothing to flush in between.
	var sb strings.Builder
	sb.WriteString(fenceMarkerPrefix)
	sb.WriteString(nonce2Hex)
	sb.WriteString("\x07")
	sb.WriteString("\x1b]133;D;0\x07")
	sb.WriteString("\x1b]133;A\x07")
	sb.WriteString("\x1b]7;file://host/home/user\x07")
	sb.WriteString("user@host:~$ ")
	if err := s.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest the batched fence and its trailing prompt bytes: %v", err)
	}

	rec, ok := s.ObservationFor(nonce2)
	if !ok {
		t.Fatal("the second interval sealed no record")
	}
	last := rec.Closing.Lines[rec.Closing.Cursor.Y]
	if got := obsRowText(last); got != "" {
		t.Fatalf("the closing screen's cursor row reads %q, want blank: "+
			"the next prompt's own text leaked into this interval's closing screen", got)
	}

	// The end marker on the row stream — the seam ws_block_rows.go stores a
	// block's closing rows from — carries the same, corrected screen. Its
	// own trailing (blank) cursor row is trimmed by closingRowsForStream
	// (nocx-2v80t.3.12), so the assertion here is that the leaked prompt
	// text never appears anywhere in it at all, not merely off its end.
	for _, e := range rs.snapshot() {
		if e.kind != "end" || e.nonce != nonce2 {
			continue
		}
		if len(e.closing) == 0 {
			t.Fatal("the end marker carries no closing rows at all")
		}
		for _, row := range e.closing {
			if got := streamRowText(row); got == "user@host:~$" {
				t.Fatalf("the streamed end marker's closing rows include %q: "+
					"the next prompt's own text leaked into this interval's stored output", got)
			}
		}
	}
}

func mustDecodeHexForTest(t *testing.T, hexed string) []byte {
	t.Helper()
	out := make([]byte, len(hexed)/2)
	for i := range out {
		hi := hexNibble(t, hexed[2*i])
		lo := hexNibble(t, hexed[2*i+1])
		out[i] = hi<<4 | lo
	}
	return out
}

func hexNibble(t *testing.T, c byte) byte {
	t.Helper()
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		t.Fatalf("not a lowercase hex digit: %q", c)
		return 0
	}
}

// ---------------------------------------------------------------------------
// A shell can only extend the boundary's own cursor row without a preceding
// newline, so that ONE row — never any other entry of the window — may be
// rewritten in place (the next prompt, or an app-submitted command's own
// echoed line) before it finally departs. suppressBoundaryScreenLocked must
// still recognise it as the boundary's row leaving, by IDENTITY (it is the
// sole survivor of the window), never by matching what it now reads
// (nocx-2v80t.3.12).
// ---------------------------------------------------------------------------

func TestTheBoundaryWindowsCursorRowIsSuppressedEvenWhenOverwrittenInPlace(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 5))

	obsFeed(t, s, 0, 3) // rows 0-2: L000000..L000002; cursor at row 3, col 0
	obsSeal(t, s, obsNonce(1))

	// Overwrite the boundary's own cursor row IN PLACE, without a preceding
	// newline — exactly what an echoed command line does over a blank
	// prompt row, and what a redrawn PS1 does over a blank one too.
	if err := s.Ingest([]byte("echoed-command")); err != nil {
		t.Fatalf("ingest the in-place rewrite: %v", err)
	}

	// Push everything off the top, one row per newline at the bottom of a
	// 5-row screen: L000000, L000001, L000002 and finally the overwritten
	// cursor row each depart in turn, then OUT001 depart genuinely.
	if err := s.Ingest([]byte("\r\nOUT001\r\nOUT002\r\nOUT003\r\nOUT004\r\nOUT005\r\n")); err != nil {
		t.Fatalf("ingest the scroll-forcing output: %v", err)
	}

	var streamed []string
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for _, r := range e.rows {
			streamed = append(streamed, streamRowText(r))
		}
	}
	for _, text := range streamed {
		if text == "echoed-command" {
			t.Fatalf("the overwritten cursor row was streamed as new output: %v", streamed)
		}
	}
	found := false
	for _, text := range streamed {
		if text == "OUT001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("genuine new output was suppressed along with the boundary row: %v", streamed)
	}
}

// Paired with the case above: a command whose own OUTPUT happens to repeat
// the boundary's cursor-row text must still be kept once the window has
// moved past that one entry — the wildcard is scoped to exactly one row and
// never reappears for a later, merely coincidental match.
func TestOutputThatRepeatsTheBoundaryTextAfterTheWildcardIsKept(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 5))

	obsFeed(t, s, 0, 3) // rows 0-2: L000000..L000002; cursor at row 3, col 0
	obsSeal(t, s, obsNonce(1))

	// The cursor row departs UNCHANGED (still blank) this time, then later,
	// genuine output happens to print the literal text "L000000" again —
	// the same text the FIRST window entry held, well after that entry (and
	// the wildcard) are both long gone. One more line past it is what makes
	// "L000000" itself scroll off and depart, rather than merely sit printed.
	if err := s.Ingest([]byte(
		"\r\nOUT001\r\nOUT002\r\nOUT003\r\nOUT004\r\nL000000\r\nOUT005\r\nOUT006\r\nOUT007\r\nOUT008\r\n",
	)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	var streamed []string
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for _, r := range e.rows {
			streamed = append(streamed, streamRowText(r))
		}
	}
	count := 0
	for _, text := range streamed {
		if text == "L000000" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf(`"L000000" streamed %d times, want exactly 1 (the genuine repeat): %v`, count, streamed)
	}
}
