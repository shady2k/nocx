package panegrid_test

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	xvt "github.com/charmbracelet/x/vt"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

func newStore(t *testing.T) *panegrid.Store {
	t.Helper()
	return panegrid.New(log.NewSlogAdapter(nil))
}

// The ordinary case. Every "returns an error when…" below is paired with this
// one, per AGENTS.md: a suite that only proves the refusals cannot report that
// the feature never works.
func TestAnEnrolledPaneShowsWhatWasWrittenToIt(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 5); err != nil {
		t.Fatalf("enrol on an ordinary store: %v", err)
	}
	s.Feed("p1", []byte("hello"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := strings.TrimRight(f.Text(0), " "); got != "hello" {
		t.Errorf("row 0 = %q, want %q", got, "hello")
	}
	if f.Cols != 40 || f.Rows != 5 {
		t.Errorf("size = %dx%d, want 40x5", f.Cols, f.Rows)
	}
	if f.CursorX != 5 || f.CursorY != 0 {
		t.Errorf("cursor = %d,%d want 5,0", f.CursorX, f.CursorY)
	}
}

// The geometry that decided the library (nocx-szb40.1). A double-width
// character takes two columns; the second is a continuation with Width 0.
// Both permitted powers are positional, so this is the property that matters
// more than the text.
func TestADoubleWidthCharacterOccupiesTwoColumns(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	s.Feed("p1", []byte("[こ]"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Lines[0][0].Text != "[" {
		t.Fatalf("col 0 = %q, want %q", f.Lines[0][0].Text, "[")
	}
	if got := f.Lines[0][1]; got.Text != "こ" || got.Width != 2 {
		t.Errorf("col 1 = %q/w%d, want こ/w2", got.Text, got.Width)
	}
	if got := f.Lines[0][2]; got.Width != 0 {
		t.Errorf("col 2 width = %d, want 0 (continuation of the wide cell)", got.Width)
	}
	// And the closing bracket is at column 3, not column 2 — which is the
	// whole point: a chrome anchor is addressed by column.
	if got := f.Lines[0][3]; got.Text != "]" {
		t.Errorf("col 3 = %q, want %q — the wide character did not consume two columns",
			got.Text, "]")
	}
	// Text() skips continuations, so content readers see it once.
	if got := strings.TrimRight(f.Text(0), " "); got != "[こ]" {
		t.Errorf("Text(0) = %q, want %q", got, "[こ]")
	}
}

// The interval has both ends, and this asserts the closing one: after
// Withdraw the pane is gone, not merely stale.
func TestWithdrawEndsTheIntervalAndTheGridIsGone(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	s.Feed("p1", []byte("x"))
	s.Withdraw("p1")
	if s.Enrolled("p1") {
		t.Error("pane still enrolled after Withdraw")
	}
	if _, err := s.Frame("p1"); !errors.Is(err, panegrid.ErrNotEnrolled) {
		t.Errorf("Frame after Withdraw = %v, want ErrNotEnrolled", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d after Withdraw, want 0", s.Count())
	}
	// Idempotent: a caller racing session teardown should not have to care
	// who won.
	s.Withdraw("p1")
	s.Withdraw("never-existed")
}

// Bytes for an unenrolled pane cost nothing and are not buffered for later.
// This is the hot path of every session in the product.
func TestBytesForAnUnenrolledPaneAreDroppedNotBuffered(t *testing.T) {
	s := newStore(t)
	s.Feed("p1", []byte("written before anybody asked"))
	if s.Enrolled("p1") {
		t.Fatal("Feed enrolled a pane; enrolment must be an act, never a side effect")
	}
	if err := s.Enrol("p1", 20, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := strings.TrimSpace(f.Text(0)); got != "" {
		t.Errorf("row 0 = %q after enrolling; pre-enrolment bytes must not be replayed", got)
	}
}

func TestEnrollingTwiceIsRefusedRatherThanDiscardingTheGrid(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Fatalf("first enrol: %v", err)
	}
	s.Feed("p1", []byte("abc"))
	if err := s.Enrol("p1", 10, 2); !errors.Is(err, panegrid.ErrAlreadyEnrolled) {
		t.Fatalf("second enrol = %v, want ErrAlreadyEnrolled", err)
	}
	// The point of refusing: the first grid is intact, so the byte-zero
	// guarantee still holds.
	f, _ := s.Frame("p1")
	if got := strings.TrimRight(f.Text(0), " "); got != "abc" {
		t.Errorf("row 0 = %q, want %q — the refused enrol damaged the grid", got, "abc")
	}
}

func TestEnrolRefusesNonsenseAndStillWorksAfterwards(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("", 10, 2); err == nil {
		t.Error("empty pane id was accepted")
	}
	if err := s.Enrol("p1", 0, 2); err == nil {
		t.Error("zero columns was accepted")
	}
	if err := s.Enrol("p1", 10, -1); err == nil {
		t.Error("negative rows was accepted")
	}
	if s.Count() != 0 {
		t.Fatalf("Count = %d after three refusals, want 0", s.Count())
	}
	// Paired success: the store is not poisoned by having refused.
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Errorf("enrol after refusals: %v", err)
	}
}

func TestTheEnrolmentBoundHoldsAndFreesOnWithdraw(t *testing.T) {
	s := newStore(t)
	for i := 0; i < panegrid.MaxEnrolled; i++ {
		if err := s.Enrol(paneName(i), 10, 2); err != nil {
			t.Fatalf("enrol %d: %v", i, err)
		}
	}
	if err := s.Enrol("one-too-many", 10, 2); !errors.Is(err, panegrid.ErrTooManyEnrolled) {
		t.Fatalf("enrol past the bound = %v, want ErrTooManyEnrolled", err)
	}
	s.Withdraw(paneName(0))
	if err := s.Enrol("one-too-many", 10, 2); err != nil {
		t.Errorf("enrol after a withdraw freed a slot: %v", err)
	}
}

func paneName(i int) string { return "pane-" + string(rune('a'+i%26)) + string(rune('0'+i/26)) }

// Feed runs on the session pump goroutine and Frame on a handler's. A data
// race here would be a race in every session, so it is asserted rather than
// assumed. Run the suite with -race for this to mean anything.
func TestFeedAndFrameAreSafeToCallConcurrently(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 10); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.Feed("p1", []byte("line of output\r\n"))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := s.Frame("p1"); err != nil {
					t.Errorf("frame during concurrent feed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A TUI sends diffs, not repaints — the reason the grid has to be fed
// continuously rather than reconstructed from a tail. Feeding the same stream
// in two pieces must land in the same place as feeding it whole.
func TestAStreamSplitAcrossFeedsLandsWhereAWholeOneDoes(t *testing.T) {
	whole := newStore(t)
	split := newStore(t)
	if err := whole.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := split.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	// An escape sequence deliberately cut in half across the two writes.
	stream := "ab\x1b[2;3Hcd"
	whole.Feed("p", []byte(stream))
	split.Feed("p", []byte(stream[:5]))
	split.Feed("p", []byte(stream[5:]))
	fw, _ := whole.Frame("p")
	fs, _ := split.Frame("p")
	for y := 0; y < fw.Rows; y++ {
		if fw.Text(y) != fs.Text(y) {
			t.Errorf("row %d differs: whole %q, split %q", y, fw.Text(y), fs.Text(y))
		}
	}
	if fw.CursorX != fs.CursorX || fw.CursorY != fs.CursorY {
		t.Errorf("cursor differs: whole %d,%d split %d,%d",
			fw.CursorX, fw.CursorY, fs.CursorX, fs.CursorY)
	}
}

// The alternate screen is an observation and decides nothing on its own
// (ADR-0024 decision 1). It is reported because the driver needs it; the
// assertion here is only that it is reported honestly.
func TestAlternateScreenIsReportedAndIsOnlyAnObservation(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	f, _ := s.Frame("p")
	if f.AltScreen {
		t.Error("a fresh grid reports the alternate screen")
	}
	s.Feed("p", []byte("\x1b[?1049h"))
	f, _ = s.Frame("p")
	if !f.AltScreen {
		t.Error("entering the alternate screen was not reported")
	}
	s.Feed("p", []byte("\x1b[?1049l"))
	f, _ = s.Frame("p")
	if f.AltScreen {
		t.Error("leaving the alternate screen was not reported")
	}
}

// The interval outlives a resize, so the grid has to survive one. Every anchor
// a driver reads is POSITIONAL — the input box is bounded by full-width rules,
// the spinner sits directly above the token counter — so a grid left at the
// size it was enrolled at does not answer about a stale screen, it answers
// about a screen that never existed: the pane repaints at the new width while
// the emulator keeps wrapping at the old one.
func TestAResizedPaneAnswersAtTheNewSize(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 4); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := s.Resize("p1", 40, 6); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.Feed("p1", []byte("wider than twenty columns"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Cols != 40 || f.Rows != 6 {
		t.Fatalf("size = %dx%d, want 40x6", f.Cols, f.Rows)
	}
	// At 20 columns this would have wrapped onto a second row; the point of
	// the resize is that it does not.
	if got := strings.TrimRight(f.Text(0), " "); got != "wider than twenty columns" {
		t.Errorf("row 0 = %q, want the whole line unwrapped", got)
	}
	if got := strings.TrimRight(f.Text(1), " "); got != "" {
		t.Errorf("row 1 = %q, want it empty", got)
	}
}

// nocx-nru89.5. Claude Code 2.1.266 sets a window title such as
// `\x1b]0;✳ marker.txt creation\x07`. ✳ is U+2733, UTF-8 `E2 9C B3`, and
// `0x9C` is also the 8-bit form of the ANSI String Terminator. x/vt's parser
// does not distinguish "a raw 8-bit ST" from "a UTF-8 continuation byte that
// happens to have the same numeric value" — it dispatches the OSC title on
// the first `0x9C` it sees, wherever that byte falls, and prints whatever
// follows at the cursor. The dialog underneath is real; the corruption is on
// top of it.
func TestATitleWithAUtf8ContinuationByteValuedLikeTheStringTerminatorDoesNotPaintIntoTheGrid(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	s.Feed("p1", []byte("0123456789"))
	// U+2733 (✳) encodes as E2 9C B3; the middle byte is 0x9C.
	s.Feed("p1", []byte("\x1b]0;\xe2\x9c\xb3 title text\x07"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	for y := 0; y < f.Rows; y++ {
		if got := f.Text(y); strings.Contains(got, "title text") {
			t.Errorf("row %d = %q: the title's text painted into the grid", y, got)
		}
	}
	if got := strings.TrimRight(f.Text(0), " "); got != "0123456789" {
		t.Errorf("row 0 = %q, want %q — the screen changed", got, "0123456789")
	}
}

// The paired success cases (AGENTS.md rule 1): a title carrying no byte that
// collides with the ST cannot regress by the fix above. Confirmed by the
// coordinator's repro to already work; asserted so a future change to the
// filter cannot silently break them.
func TestATitleWithNoAmbiguousByteAlsoLeavesTheScreenUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{"ascii", "marker.txt creation"},
		{"non-ambiguous unicode", "◐ marker.txt creation"}, // ◐, E2 97 90 — no 0x9C byte
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", 40, 3); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			s.Feed("p1", []byte("0123456789"))
			s.Feed("p1", []byte("\x1b]0;"+c.title+"\x07"))
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			if got := strings.TrimRight(f.Text(0), " "); got != "0123456789" {
				t.Errorf("row 0 = %q, want %q — the screen changed", got, "0123456789")
			}
			for y := 1; y < f.Rows; y++ {
				if got := strings.TrimRight(f.Text(y), " "); got != "" {
					t.Errorf("row %d = %q, want empty", y, got)
				}
			}
		})
	}
}

// A genuine standalone 8-bit ST — not part of any multi-byte UTF-8 sequence —
// must still terminate the OSC string exactly as x/vt already does correctly.
// The fix narrows the ambiguity; it must not remove 8-bit ST support outright.
func TestAGenuineStandaloneStringTerminatorStillEndsTheTitle(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	// OSC title terminated by a bare 8-bit ST (0x9C), then ordinary text.
	s.Feed("p1", []byte("\x1b]0;ignored title\x9cvisible"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := strings.TrimRight(f.Text(0), " "); got != "visible" {
		t.Errorf("row 0 = %q, want %q — a genuine 8-bit ST must still end the OSC string", got, "visible")
	}
}

// nocx-nru89.11. The second review of nocx-nru89 (.internal/sdd/fix-review.md
// findings 7 and 8) found that c1filter.go's own byte-by-byte state
// tracking was a SECOND, independent guess at "are we inside an OSC/DCS
// string" that disagreed with x/ansi's real transition table
// (github.com/charmbracelet/x/ansi/parser, v0.11.7) in both directions: it
// missed starts x/ansi recognises, and it corrupted text x/ansi's Utf8State
// already decoded correctly. The tests below are every case the review
// named, run both as one Feed and fed one byte at a time (this bead's
// Method step 3) — the persistent, cross-call state is exactly what
// TestAStreamSplitAcrossFeedsLandsWhereAWholeOneDoes already requires of
// the grid itself.

// feedWhole and feedPerByte are the two delivery shapes a live PTY stream
// can arrive in: one Feed call, or split at every byte boundary. Both must
// land identically.
func feedWhole(s *panegrid.Store, paneID string, b []byte) {
	s.Feed(paneID, b)
}

func feedPerByte(s *panegrid.Store, paneID string, b []byte) {
	for _, c := range b {
		s.Feed(paneID, []byte{c})
	}
}

var feedShapes = []struct {
	name string
	fn   func(*panegrid.Store, string, []byte)
}{
	{"whole", feedWhole},
	{"byte-by-byte", feedPerByte},
}

// rawXVTRows feeds b directly into a fresh x/vt emulator, bypassing
// panegrid entirely (no c1Filter in front of it), and returns the text of
// every row. It is the "what x/vt alone shows" side of the parity tests
// below — the reply-drain goroutine mirrors Store.Enrol's own, because
// x/vt's emulator replies upstream through an unbuffered pipe and a title
// sequence with a device-attributes-like prefix would otherwise deadlock
// the write.
func rawXVTRows(t *testing.T, cols, rows int, b []byte) []string {
	t.Helper()
	term := xvt.NewEmulator(cols, rows)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			if _, err := term.Read(buf); err != nil {
				return
			}
		}
	}()
	if _, err := term.Write(b); err != nil {
		t.Fatalf("raw x/vt write: %v", err)
	}
	if c, ok := term.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	<-drained
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		var sb strings.Builder
		for x := 0; x < cols; x++ {
			cell := term.CellAt(x, y)
			switch {
			case cell == nil, cell.Width == 0 && cell.Content == "":
				sb.WriteByte(' ')
			case cell.Width == 0:
				// continuation cell of a double-width grapheme already
				// written; nothing to add.
			case cell.Content == "":
				sb.WriteByte(' ')
			default:
				sb.WriteString(cell.Content)
			}
		}
		out[y] = sb.String()
	}
	_ = term.Close()
	return out
}

// x/ansi's Anywhere transitions start an OSC string on the 8-bit introducer
// 0x9D from any state (transition_table.go:111), exactly like the 7-bit
// `ESC ]` the tests above already cover — c1filter.go recognised only the
// 7-bit form, so a title opened this way still painted into the grid
// (fix-review.md finding 7).
func TestAn8BitOSCIntroducerCarryingATitleDoesNotPaintIntoTheGrid(t *testing.T) {
	// 0x9D, then "0;", then U+2733 (✳, E2 9C B3 — the same ambiguous middle
	// byte as the 7-bit tests), then ordinary text, terminated by BEL.
	title := []byte("\x9d0;\xe2\x9c\xb3 title text\x07")
	for _, feed := range feedShapes {
		t.Run(feed.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", 40, 3); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			s.Feed("p1", []byte("0123456789"))
			feed.fn(s, "p1", title)
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for y := 0; y < f.Rows; y++ {
				if got := f.Text(y); strings.Contains(got, "title text") {
					t.Errorf("row %d = %q: the title's text painted into the grid", y, got)
				}
			}
			if got := strings.TrimRight(f.Text(0), " "); got != "0123456789" {
				t.Errorf("row 0 = %q, want %q — the screen changed", got, "0123456789")
			}
		})
	}
}

// x/ansi's Anywhere transitions likewise start a DCS from the 8-bit
// introducer 0x90 (transition_table.go:109), landing in DcsEntryState —
// which needs one more byte (here 'q', a Sixel-style final byte,
// transition_table.go:187) to actually reach the string state that carries
// the title bytes. c1filter.go recognised only `ESC P`.
func TestAn8BitDCSIntroducerCarryingATitleDoesNotPaintIntoTheGrid(t *testing.T) {
	dcs := []byte("\x90q\xe2\x9c\xb3 title text\x1b\\")
	for _, feed := range feedShapes {
		t.Run(feed.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", 40, 3); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			s.Feed("p1", []byte("0123456789"))
			feed.fn(s, "p1", dcs)
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for y := 0; y < f.Rows; y++ {
				if got := f.Text(y); strings.Contains(got, "title text") {
					t.Errorf("row %d = %q: the title's text painted into the grid", y, got)
				}
			}
			if got := strings.TrimRight(f.Text(0), " "); got != "0123456789" {
				t.Errorf("row 0 = %q, want %q — the screen changed", got, "0123456789")
			}
		})
	}
}

// x/ansi's EscapeState executes every C0 control byte and STAYS in
// EscapeState (transition_table.go:135-137); c1filter.go instead reset to
// ground on any byte other than ']', 'P' or ESC (c1filter.go:99-100,
// before this change), so `ESC CR ]` never registered as an OSC introducer
// at all and the title painted (fix-review.md finding 7).
func TestAControlByteBetweenEscapeAndTheOSCBracketStillOpensTheTitle(t *testing.T) {
	title := []byte("\x1b\r]0;\xe2\x9c\xb3 title text\x07")
	for _, feed := range feedShapes {
		t.Run(feed.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", 40, 3); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			s.Feed("p1", []byte("0123456789"))
			feed.fn(s, "p1", title)
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for y := 0; y < f.Rows; y++ {
				if got := f.Text(y); strings.Contains(got, "title text") {
					t.Errorf("row %d = %q: the title's text painted into the grid", y, got)
				}
			}
			if got := strings.TrimRight(f.Text(0), " "); got != "0123456789" {
				t.Errorf("row 0 = %q, want %q — the screen changed", got, "0123456789")
			}
		})
	}
}

// x/ansi's Utf8State bypasses the transition table entirely once a lead
// byte diverts into it (x/ansi/parser.go:181-204, advanceUtf8): every byte
// that follows — ESC included — is consumed as a raw rune byte until the
// rune is complete, and x/vt never leaves ground state at all. So in
// `AB E2 ESC ] E2 9C BB Working BEL`, the ESC and `]` are the 2nd and 3rd
// bytes of an (invalid, hence replacement-charactered) rune, not an OSC
// introducer, and Claude's own spinner glyph (E2 9C BB) that follows is a
// second, valid rune — never an OSC string at all. c1filter.go did not
// track UTF-8 in ground state, believed an OSC had started, and corrupted
// the spinner glyph's own 0x9C byte to '?' (fix-review.md finding 8). The
// fix must match x/vt with no filter in front of it, byte for byte.
func TestTheGridMatchesXVTAloneWhenAnEscapeIsSwallowedInsideAnUnfinishedRune(t *testing.T) {
	stream := []byte("AB\xe2\x1b]\xe2\x9c\xbb Working\x07")
	const cols, rows = 40, 3
	want := rawXVTRows(t, cols, rows, stream)
	for _, feed := range feedShapes {
		t.Run(feed.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", cols, rows); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			feed.fn(s, "p1", stream)
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for y := 0; y < rows; y++ {
				if got := f.Text(y); got != want[y] {
					t.Errorf("row %d = %q, want %q (x/vt alone)", y, got, want[y])
				}
			}
		})
	}
}

// `ESC P` leaves EscapeState for DcsEntryState (transition_table.go:153),
// which does not override the 0x80-0xFF byte range (:171-187) — so a raw
// UTF-8 lead byte there still takes the Anywhere -> Utf8State transition
// (:112-115), and advanceUtf8 unconditionally returns to GroundState once
// the rune completes (x/ansi/parser.go:200), regardless of the DCS that
// never got to start: the rune is printed as ordinary ground text and the
// DCS is simply abandoned. Real x/vt does this with no filter at all;
// c1filter.go instead believed a DCS string was open and corrupted the
// rune (fix-review.md finding 8).
func TestTheGridMatchesXVTAloneWhenADCSEntryMeetsARawUTF8Character(t *testing.T) {
	stream := []byte("\x1bP\xe2\x9c\xb3 shown\x1b\\")
	const cols, rows = 40, 3
	want := rawXVTRows(t, cols, rows, stream)
	for _, feed := range feedShapes {
		t.Run(feed.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Enrol("p1", cols, rows); err != nil {
				t.Fatalf("enrol: %v", err)
			}
			feed.fn(s, "p1", stream)
			f, err := s.Frame("p1")
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for y := 0; y < rows; y++ {
				if got := f.Text(y); got != want[y] {
					t.Errorf("row %d = %q, want %q (x/vt alone)", y, got, want[y])
				}
			}
		})
	}
}

// A resize for a pane with no grid is the ordinary case, not a failure: most
// panes never have one and every one of them is resized.
func TestResizingAPaneWithNoGridIsNotAnError(t *testing.T) {
	s := newStore(t)
	if err := s.Resize("nobody", 40, 6); !errors.Is(err, panegrid.ErrNotEnrolled) {
		t.Errorf("resize of an unenrolled pane = %v, want ErrNotEnrolled", err)
	}
}

func TestResizeRefusesASizeThatIsNotASize(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 4); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := s.Resize("p1", 0, 6); err == nil {
		t.Error("resize to 0 columns was accepted")
	}
	if err := s.Resize("p1", 40, 0); err == nil {
		t.Error("resize to 0 rows was accepted")
	}
	// And the refusal left the grid usable at the size it had.
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame after a refused resize: %v", err)
	}
	if f.Cols != 20 || f.Rows != 4 {
		t.Errorf("size after a refused resize = %dx%d, want 20x4", f.Cols, f.Rows)
	}
}
