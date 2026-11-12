package ghostty

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The tests below drive the PORT — emulator.Terminal — and never the adapter's
// own types, because that is what the session runtime will do. Each one is
// written from the behaviour a caller can observe: the bytes a program's query
// produced, the cells a screen holds, what a key encodes to, and what survives
// the memory it came from.
//
// The conversions in convert.go are exercised here rather than by unit-testing
// them, and the port's numbering being its own is what makes that evidence: a
// conversion that copied upstream's number through would report WidthNarrow for
// a wide cluster and these tests would fail.

func newTerminal(t *testing.T, cols, rows int) emulator.Terminal {
	t.Helper()
	term, err := New(emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("create a %dx%d terminal: %v", cols, rows, err)
	}
	t.Cleanup(term.Close)
	return term
}

func ingest(t *testing.T, term emulator.Terminal, s string) []byte {
	t.Helper()
	replies, err := term.Ingest([]byte(s))
	if err != nil {
		t.Fatalf("ingest %q: %v", s, err)
	}
	return replies
}

// TestIngestHandsBackTheProgramsOwnReplies is the port's half of design §6.5:
// the program asks about its own terminal and the answer comes back from the
// one emulator that owns the coordinates. The report is consumed once, because
// the caller writes it to the PTY and a second write would answer a question
// the program asked once.
func TestIngestHandsBackTheProgramsOwnReplies(t *testing.T) {
	term := newTerminal(t, 20, 4)
	if replies := ingest(t, term, "hello\r\nworld"); len(replies) != 0 {
		t.Fatalf("plain output produced replies %q", replies)
	}
	replies := ingest(t, term, "\x1b[6n")
	if got, want := string(replies), "\x1b[2;6R"; got != want {
		t.Fatalf("cursor report = %q, want %q", got, want)
	}
	if again := ingest(t, term, ""); len(again) != 0 {
		t.Errorf("the same report was handed over twice: %q", again)
	}
}

// TestDeviceAttributesAreAnswered is the DA half of the same rule, and it is
// the half a missing callback would make silent: without an attributes callback
// the library ignores CSI c and CSI > c entirely, so the program hears nothing
// rather than hearing something wrong.
func TestDeviceAttributesAreAnswered(t *testing.T) {
	term := newTerminal(t, 20, 4)
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"\x1b[c", "\x1b[?62;22c"},
		{"\x1b[>c", "\x1b[>1;0;0c"},
	} {
		if got := string(ingest(t, term, tc.query)); got != tc.want {
			t.Errorf("%q answered %q, want %q", tc.query, got, tc.want)
		}
	}
}

// TestDecrqmAnswersDecPrivateModes is the half of DECRQM the pinned upstream
// does answer, and it is here so that the skipped test below is about the ANSI
// form specifically rather than about the query being unimplemented.
func TestDecrqmAnswersDecPrivateModes(t *testing.T) {
	term := newTerminal(t, 20, 4)
	replies := ingest(t, term, "\x1b[?7$p")
	if got, want := string(replies), "\x1b[?7;1$y"; got != want {
		t.Errorf("DECRQM for DEC mode 7 = %q, want %q", got, want)
	}
}

// TestDecrqmAnswersTheAnsiForm is the other half of DECRQM, and it was the
// debt nocx-ygxjv.8 was filed on: the ANSI form of the query (`CSI Ps $ p`, ONE
// intermediate) was answered with nothing while the DEC private form
// (`CSI ? Ps $ p`, two) was answered.
//
// The cause was one line of upstream's, not a missing feature:
// src/terminal/stream.zig took its DECRQM branch only when a CSI carried TWO
// intermediates, so the block's own `1 =>` arm — which classifies the ANSI form
// — could never run. nocx carries the fix as a patch on the pinned fork
// (`upstream.patch` in third_party/libghostty-vt/MANIFEST.json), which is why
// this test is no longer gated behind an environment variable.
//
// IRM is set before the assertion because the reply is a report about the
// mode's STATE, and IRM starts reset like every ANSI mode outside ghostty's
// `default` column — so the same query on a fresh terminal answers `\x1b[4;2$y`.
// Both answers are asserted, in that order, because both are the contract.
func TestDecrqmAnswersTheAnsiForm(t *testing.T) {
	term := newTerminal(t, 20, 4)
	if got, want := string(ingest(t, term, "\x1b[4$p")), "\x1b[4;2$y"; got != want {
		t.Errorf("DECRQM for insert mode (reset) = %q, want %q", got, want)
	}
	if replies := ingest(t, term, "\x1b[4h"); len(replies) != 0 {
		t.Fatalf("setting insert mode produced replies %q", replies)
	}
	const want = "\x1b[4;1$y" // IRM is set: the reply x/vt gives and ghostty did not
	if got := string(ingest(t, term, "\x1b[4$p")); got != want {
		t.Errorf("DECRQM for insert mode = %q, want %q", got, want)
	}
}

// TestCellReportsGraphemeWidthAndStyle is the cell contract: a whole cluster,
// an authoritative column width, and a colour that keeps its three shapes
// apart. The wide cluster is the case a font measurement cannot answer and the
// reason the emulator owns widths.
// TestModesExposeOnlyWhatTheProgramSet verifies that the modes come from the
// program's own state and from nothing else. Two of them are the facts the
// client needs for local gesture routing; the frontend does not parse terminal
// bytes to reconstruct them, and the third (the synchronized-output hold) is the
// session runtime's own publish decision.
func TestModesExposeOnlyWhatTheProgramSet(t *testing.T) {
	term := newTerminal(t, 20, 4)
	if got, err := term.Modes(); err != nil || got != (emulator.Modes{}) {
		t.Fatalf("initial modes = %+v, %v; want zero modes", got, err)
	}
	ingest(t, term, "\x1b[?2004h\x1b[?1002h")
	want := emulator.Modes{BracketedPaste: true, MouseTracking: true}
	if got, err := term.Modes(); err != nil || got != want {
		t.Fatalf("enabled modes = %+v, %v; want %+v", got, err, want)
	}
	ingest(t, term, "\x1b[?2004l\x1b[?1002l")
	if got, err := term.Modes(); err != nil || got != (emulator.Modes{}) {
		t.Fatalf("reset modes = %+v, %v; want zero modes", got, err)
	}
}

// TestModesReportTheSynchronizedOutputHold is the port's half of nocx-1ebrs.33:
// DEC private mode 2026 is a fact the terminal tracks, so a caller reads it
// here instead of hunting the byte stream for the sequence that set it (AD-6).
// The RESET arm is why the read is a query and not a bit kept beside the
// terminal: the library ends a hold itself on a full reset, and a copy in the
// caller would go on believing a hold nobody is in.
func TestModesReportTheSynchronizedOutputHold(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "\x1b[?2026h")
	if got, err := term.Modes(); err != nil || !got.SynchronizedOutput {
		t.Fatalf("after BSU the modes are %+v, %v; want the hold reported", got, err)
	}
	ingest(t, term, "\x1b[?2026l")
	if got, err := term.Modes(); err != nil || got.SynchronizedOutput {
		t.Fatalf("after ESU the modes are %+v, %v; want no hold", got, err)
	}
	ingest(t, term, "\x1b[?2026h\x1bc")
	if got, err := term.Modes(); err != nil || got.SynchronizedOutput {
		t.Fatalf("after a full reset the modes are %+v, %v; want no hold: the terminal ends one itself", got, err)
	}
}

// TestYazisThemeIconsTakeOneCell is the width half of nocx-peg8e.1.3, and it
// is here because the question the report asked was "which party's width is
// wrong": yazi positions the cell after an icon by counting one column for it
// (Rust's unicode-width reports 1 for every private-use codepoint), and the
// terminal has to agree or the rest of that row lands somewhere else. These are
// the codepoints yazi's shipped theme-dark.toml assigns — Library's U+EB9C, the
// directory and hovered-directory U+E5FF/U+E5FE, the Documents/Downloads
// U+F401/U+F498, and the supplementary-plane U+F0219 — read out of the bytes
// yazi 26.9.1 writes, not out of its source. A pin that made any of them two
// columns would move every cell after it on that row, which is exactly what the
// owner saw; this test fails at the emulator rather than in a screenshot.
func TestYazisThemeIconsTakeOneCell(t *testing.T) {
	icons := []string{"\ueb9c", "\ue5ff", "\ue5fe", "\uf401", "\uf498", "\U000f0219"}
	term := newTerminal(t, 20, 2)
	for _, icon := range icons {
		ingest(t, term, icon)
	}
	cursor, err := term.Cursor()
	if err != nil {
		t.Fatal(err)
	}
	if cursor.X != len(icons) {
		t.Fatalf("%d one-cell icons left the cursor at column %d, want %d: a two-column icon shifts its row",
			len(icons), cursor.X, len(icons))
	}
	for x, icon := range icons {
		cell, err := term.Cell(x, 0)
		if err != nil {
			t.Fatalf("cell %d: %v", x, err)
		}
		if cell.Grapheme != icon || cell.Width != emulator.WidthNarrow || !cell.HasText {
			t.Errorf("cell %d = %q width %v text %v, want %q at one column",
				x, cell.Grapheme, cell.Width, cell.HasText, icon)
		}
	}
	if tail, err := term.Cell(len(icons), 0); err != nil || tail.Width != emulator.WidthNarrow || tail.HasText {
		t.Errorf("the cell after the icons = %+v, %v; want an untouched narrow blank", tail, err)
	}
}

func TestCellReportsGraphemeWidthAndStyle(t *testing.T) {
	term := newTerminal(t, 20, 3)
	ingest(t, term, "\x1b[1;31mA\x1b[0m\x1b[38;2;1;2;3mB\x1b[0m\x1b[48;5;42mC\x1b[0m\x1b[4:3mD\x1b[0m中")

	for _, tc := range []struct {
		x    int
		want emulator.Cell
	}{
		{0, emulator.Cell{
			Grapheme: "A", Width: emulator.WidthNarrow, HasText: true,
			Style: emulator.Style{
				Foreground: emulator.Color{Kind: emulator.ColorPalette, Palette: 1},
				Attributes: emulator.AttrBold,
			},
		}},
		{1, emulator.Cell{
			Grapheme: "B", Width: emulator.WidthNarrow, HasText: true,
			Style: emulator.Style{
				Foreground: emulator.Color{Kind: emulator.ColorRGB, RGB: emulator.RGB{R: 1, G: 2, B: 3}},
			},
		}},
		{2, emulator.Cell{
			Grapheme: "C", Width: emulator.WidthNarrow, HasText: true,
			Style: emulator.Style{
				Background: emulator.Color{Kind: emulator.ColorPalette, Palette: 42},
			},
		}},
		{3, emulator.Cell{
			Grapheme: "D", Width: emulator.WidthNarrow, HasText: true,
			Style: emulator.Style{Underline: emulator.UnderlineCurly},
		}},
		{4, emulator.Cell{
			Grapheme: "中", Width: emulator.WidthWide, HasText: true,
		}},
		{5, emulator.Cell{
			Width: emulator.WidthSpacerTail,
		}},
	} {
		got, err := term.Cell(tc.x, 0)
		if err != nil {
			t.Fatalf("cell %d: %v", tc.x, err)
		}
		if got.Grapheme != tc.want.Grapheme {
			t.Errorf("cell %d grapheme = %q, want %q", tc.x, got.Grapheme, tc.want.Grapheme)
		}
		if got.Width != tc.want.Width {
			t.Errorf("cell %d width = %d, want %d", tc.x, got.Width, tc.want.Width)
		}
		if got.HasText != tc.want.HasText {
			t.Errorf("cell %d hasText = %v, want %v", tc.x, got.HasText, tc.want.HasText)
		}
		if got.Style.Foreground != tc.want.Style.Foreground {
			t.Errorf("cell %d foreground = %+v, want %+v", tc.x, got.Style.Foreground, tc.want.Style.Foreground)
		}
		if got.Style.Background != tc.want.Style.Background {
			t.Errorf("cell %d background = %+v, want %+v", tc.x, got.Style.Background, tc.want.Style.Background)
		}
		if got.Style.Attributes != tc.want.Style.Attributes {
			t.Errorf("cell %d attributes = %#x, want %#x", tc.x, got.Style.Attributes, tc.want.Style.Attributes)
		}
		if got.Style.Underline != tc.want.Style.Underline {
			t.Errorf("cell %d underline = %d, want %d", tc.x, got.Style.Underline, tc.want.Style.Underline)
		}
	}
}

// TestSpacerHeadEndsAWrappedLine is the fourth width class, and the one that
// only exists at a wrap boundary: a two-column cluster that does not fit in the
// last column leaves a spacer head there and moves to the next line, which the
// serialiser has to be able to tell from a blank cell.
func TestSpacerHeadEndsAWrappedLine(t *testing.T) {
	term := newTerminal(t, 4, 3)
	ingest(t, term, "abc中")

	last, err := term.Cell(3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if last.Width != emulator.WidthSpacerHead || last.HasText {
		t.Errorf("the last column of a line broken by a wide cluster = %+v, want a spacer head with no text", last)
	}
	next, err := term.Cell(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if next.Grapheme != "中" || next.Width != emulator.WidthWide {
		t.Errorf("the wrapped cluster = %+v, want 中 at two columns", next)
	}
}

// TestRowReportsSoftWrapContinuation is per-line state a serialiser cannot
// recompute: joining the physical rows of one logical line while keeping a hard
// newline needs both flags, and they are not each other's negation.
func TestRowReportsSoftWrapContinuation(t *testing.T) {
	term := newTerminal(t, 10, 4)
	ingest(t, term, "0123456789ABCDEFGHIJ")

	for _, tc := range []struct {
		y             int
		wrap, cont    bool
		firstGrapheme string
	}{
		{y: 0, wrap: true, cont: false, firstGrapheme: "0"},
		{y: 1, wrap: false, cont: true, firstGrapheme: "A"},
		{y: 2, wrap: false, cont: false, firstGrapheme: ""},
		{y: 3, wrap: false, cont: false, firstGrapheme: ""},
	} {
		row, err := term.Row(tc.y)
		if err != nil {
			t.Fatalf("row %d: %v", tc.y, err)
		}
		if row.Wrap != tc.wrap || row.Continuation != tc.cont {
			t.Errorf("row %d wrap/continuation = %v/%v, want %v/%v", tc.y, row.Wrap, row.Continuation, tc.wrap, tc.cont)
		}
		if len(row.Cells) != 10 {
			t.Errorf("row %d has %d cells, want one per column", tc.y, len(row.Cells))
		}
		if got := row.Cells[0].Grapheme; got != tc.firstGrapheme {
			t.Errorf("row %d starts with %q, want %q", tc.y, got, tc.firstGrapheme)
		}
	}
}

// TestHeldRowSurvivesTheTerminalChanging is the borrowed-data rule of ADR-0065
// point 3, and it is the one the C API makes easy to get wrong: a grid
// reference is a snapshot that dies at the next mutating call, so a Row handed
// to a caller must be a copy and not a view. The terminal is both MUTATED and
// FREED here, and the row the caller holds is expected to be unchanged by
// either.
func TestHeldRowSurvivesTheTerminalChanging(t *testing.T) {
	term := newTerminal(t, 8, 2)
	ingest(t, term, "AAABBB")
	held, err := term.Row(0)
	if err != nil {
		t.Fatal(err)
	}
	ingest(t, term, "\x1b[HZZZZZZ")
	term.Close()

	var got strings.Builder
	for _, c := range held.Cells {
		if c.HasText {
			got.WriteString(c.Grapheme)
		}
	}
	if want := "AAABBB"; got.String() != want {
		t.Errorf("a held row reads %q after the terminal changed and closed, want %q", got.String(), want)
	}
}

// TestHeldRepliesSurviveLaterIngest is the same rule on the reply path, where
// the borrowed memory is the parser's: the bytes exist only for the duration of
// the callback that receives them, so a port that kept the pointer would hand
// the caller bytes the next write overwrites.
func TestHeldRepliesSurviveLaterIngest(t *testing.T) {
	term := newTerminal(t, 20, 4)
	replies := ingest(t, term, "\x1b[6n")
	if len(replies) == 0 {
		t.Fatal("the cursor report was not handed over")
	}
	want := string(replies)

	ingest(t, term, strings.Repeat("overwrite everything\r\n", 200))
	term.Close()

	if got := string(replies); got != want {
		t.Errorf("a held reply reads %q after later output and a close, want %q", got, want)
	}
}

// TestKeyEncodingIsDrivenFromTerminalState is ADR-0065's carried gate, through
// the NATIVE binding: it was measured on the same pinned library compiled to
// wasm (.internal/spikes/vtwasm, TestKeyEncodingIsDrivenFromTerminalState) and
// never through this call path. The caller asks for the same key three times
// and the PROGRAM's modes decide what the bytes are — which is the whole reason
// input is intent rather than bytes (design §6.1).
func TestKeyEncodingIsDrivenFromTerminalState(t *testing.T) {
	term := newTerminal(t, 20, 4)
	left := emulator.KeyEvent{Key: emulator.KeyLeft}

	for _, tc := range []struct {
		name   string
		ingest string
		want   string
	}{
		{"legacy", "", "\x1b[D"},
		{"the program enables application cursor keys", "\x1b[?1h", "\x1bOD"},
		{"the program disables them again", "\x1b[?1l", "\x1b[D"},
	} {
		ingest(t, term, tc.ingest)
		got, err := term.EncodeKey(left)
		if err != nil {
			t.Fatalf("%s: encode: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: Left encoded as %q, want %q", tc.name, got, tc.want)
		}
	}

	// The Kitty keyboard protocol is the same rule one level further: the
	// program asks for event types and the encoder, derived from the terminal,
	// reports the press and the release as events rather than as a keystroke.
	ingest(t, term, "\x1b[>2u")
	got, err := term.EncodeKey(left)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[1;1:1D"; string(got) != want {
		t.Errorf("under the Kitty flags the program pushed, Left encoded as %q, want %q", got, want)
	}
	got, err = term.EncodeKey(emulator.KeyEvent{Key: emulator.KeyLeft, Action: emulator.KeyRelease})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[1;1:3D"; string(got) != want {
		t.Errorf("a release under those flags encoded as %q, want %q", got, want)
	}
}

// TestKeyIdentitiesEncodeTheirOwnSequences pins cKey's arms to observable bytes
// across the table: the control pad, an arrow, a function key, a printable
// letter and a control combination. The completeness test in convert_test.go
// proves every declared key HAS a conversion; this proves a spread of them
// convert to the RIGHT one, which is the half a swapped arm would break —
// two keys converted to each other's upstream identity would still be complete
// and distinct.
//
// The bytes are the legacy xterm encodings, which is what a program that has
// asked for nothing gets. Text is set where the emulator needs the character:
// the encoder cannot derive "a" from [KeyA], and a key with no text is a real
// event (the space key with nothing to insert encodes to nothing).
func TestKeyIdentitiesEncodeTheirOwnSequences(t *testing.T) {
	term := newTerminal(t, 20, 4)
	for _, tc := range []struct {
		name string
		ev   emulator.KeyEvent
		want string
	}{
		{"Left", emulator.KeyEvent{Key: emulator.KeyLeft}, "\x1b[D"},
		{"Right", emulator.KeyEvent{Key: emulator.KeyRight}, "\x1b[C"},
		{"Up", emulator.KeyEvent{Key: emulator.KeyUp}, "\x1b[A"},
		{"Down", emulator.KeyEvent{Key: emulator.KeyDown}, "\x1b[B"},
		{"Home", emulator.KeyEvent{Key: emulator.KeyHome}, "\x1b[H"},
		{"End", emulator.KeyEvent{Key: emulator.KeyEnd}, "\x1b[F"},
		{"PageUp", emulator.KeyEvent{Key: emulator.KeyPageUp}, "\x1b[5~"},
		{"PageDown", emulator.KeyEvent{Key: emulator.KeyPageDown}, "\x1b[6~"},
		{"Insert", emulator.KeyEvent{Key: emulator.KeyInsert}, "\x1b[2~"},
		{"Delete", emulator.KeyEvent{Key: emulator.KeyDelete}, "\x1b[3~"},
		{"Backspace", emulator.KeyEvent{Key: emulator.KeyBackspace}, "\x7f"},
		{"Enter", emulator.KeyEvent{Key: emulator.KeyEnter}, "\r"},
		{"Tab", emulator.KeyEvent{Key: emulator.KeyTab}, "\t"},
		{"Escape", emulator.KeyEvent{Key: emulator.KeyEscape}, "\x1b"},
		{"Space", emulator.KeyEvent{Key: emulator.KeySpace, Text: " "}, " "},
		{"A", emulator.KeyEvent{Key: emulator.KeyA, Text: "a"}, "a"},
		{"Semicolon", emulator.KeyEvent{Key: emulator.KeySemicolon, Text: ";"}, ";"},
		{"Digit1", emulator.KeyEvent{Key: emulator.KeyDigit1, Text: "1"}, "1"},
		{"Ctrl-C", emulator.KeyEvent{Key: emulator.KeyC, Mods: emulator.Mods{Ctrl: true}, Text: "c"}, "\x03"},
		{"Ctrl-Left", emulator.KeyEvent{Key: emulator.KeyLeft, Mods: emulator.Mods{Ctrl: true}}, "\x1b[1;5D"},
		{"Shift-Up", emulator.KeyEvent{Key: emulator.KeyUp, Mods: emulator.Mods{Shift: true}}, "\x1b[1;2A"},
		{"F1", emulator.KeyEvent{Key: emulator.KeyF1}, "\x1bOP"},
		{"F5", emulator.KeyEvent{Key: emulator.KeyF5}, "\x1b[15~"},
		{"F12", emulator.KeyEvent{Key: emulator.KeyF12}, "\x1b[24~"},
	} {
		got, err := term.EncodeKey(tc.ev)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s encoded as %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestNumpadEnterIsReturnUntilTheProgramAsksForItsOwnSequence is
// nocx-1ebrs.32's rule, measured through the native binding: the numeric
// keypad's Enter is a key of its own, and what it is WORTH is the program's,
// not the client's.
//
// The defect it closes: the pane's key vocabulary had no name for the keypad's
// Enter, so a keypress on it emitted no intent at all and reached the program as
// silence — while Return worked. The fix is not a second encoding path; it is
// this key's identity reaching the one encoder, where the modes the program set
// decide what the key is worth.
func TestNumpadEnterIsReturnUntilTheProgramAsksForItsOwnSequence(t *testing.T) {
	// EVERY MODE COMBINATION BELOW WAS MEASURED against this encoder before it
	// was written down, because the alternative is a protocol table of our own
	// beside the library's. Two of the results are not the obvious guess:
	//
	//  - DECNKM (mode 66) ALONE changes nothing. Mode 1035, "ignore the keypad
	//    with NumLock", is ON by default and this encoder reads the keypad as
	//    numeric while it is on, whatever DECNKM says. That is xterm's rule and
	//    the library owns it; a nocx-side rule that overrode it would be the
	//    second key table ADR-0066 exists against.
	//  - Under the Kitty protocol the keypad's Enter is its own codepoint
	//    (57414, KP_ENTER), so there it parts from Return without any keypad
	//    mode at all — the same rule one level further, not a different one.
	for _, tc := range []struct {
		name   string
		modes  string
		numpad string
		enter  string
	}{
		{
			name:   "no protocol enabled: the two keys are the same bytes",
			numpad: "\r",
			enter:  "\r",
		},
		{
			name:   "DECNKM alone, with mode 1035 still on: numerically keypad, so CR",
			modes:  "\x1b[?66h",
			numpad: "\r",
			enter:  "\r",
		},
		{
			name:   "application keypad mode: the keypad's own sequence, and Return's stays CR",
			modes:  "\x1b[?66h\x1b[?1035l",
			numpad: "\x1bOM",
			enter:  "\r",
		},
		{
			name:   "the program takes application keypad mode back",
			modes:  "\x1b[?66h\x1b[?1035l\x1b[?66l",
			numpad: "\r",
			enter:  "\r",
		},
		{
			name:   "the Kitty keyboard protocol: the keypad's Enter is KP_ENTER",
			modes:  "\x1b[>1u",
			numpad: "\x1b[57414u",
			enter:  "\r",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTerminal(t, 20, 4)
			if tc.modes != "" {
				ingest(t, term, tc.modes)
			}
			for _, key := range []struct {
				name string
				ev   emulator.KeyEvent
				want string
			}{
				{"keypad Enter", emulator.KeyEvent{Key: emulator.KeyNumpadEnter}, tc.numpad},
				{"Return", emulator.KeyEvent{Key: emulator.KeyEnter}, tc.enter},
			} {
				got, err := term.EncodeKey(key.ev)
				if err != nil {
					t.Fatalf("%s: encode: %v", key.name, err)
				}
				if string(got) != key.want {
					t.Errorf("%s encoded as %q, want %q", key.name, got, key.want)
				}
			}
		})
	}
}

// TestEncodeKeyRefusesAKeyItDoesNotCarry is the port saying what it does not
// know instead of encoding something else. The zero Key is the case that
// matters: a Key nobody set must not become Escape or UNIDENTIFIED.
func TestEncodeKeyRefusesAKeyItDoesNotCarry(t *testing.T) {
	term := newTerminal(t, 20, 4)
	for _, key := range []emulator.Key{emulator.KeyUnknown, emulator.Key(9999)} {
		if _, err := term.EncodeKey(emulator.KeyEvent{Key: key}); !errors.Is(err, emulator.ErrUnsupported) {
			t.Errorf("key %d: got %v, want ErrUnsupported", key, err)
		}
	}
}

// TestResizeCommitsBothSidesAndAnswersInBand. The report on the right is not
// decoration: a program that enabled in-band size reports (mode 2048) is told
// about a resize by the emulator, so a Resize that returned only an error would
// drop a message the program is waiting for.
func TestResizeCommitsBothSidesAndAnswersInBand(t *testing.T) {
	term := newTerminal(t, 20, 4)
	got, err := term.Geometry()
	if err != nil {
		t.Fatalf("geometry: %v", err)
	}
	if got.Cols != 20 || got.Rows != 4 || got.CellWidthPx != 10 || got.CellHeightPx != 20 {
		t.Fatalf("a fresh terminal reports %+v", got)
	}

	ingest(t, term, "\x1b[?2048h")
	replies, err := term.Resize(emulator.Geometry{Cols: 30, Rows: 5, CellWidthPx: 10, CellHeightPx: 20})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	// rows; cols; height px; width px — the shape mode 2048 defines.
	if got, want := string(replies), "\x1b[48;5;30;100;300t"; got != want {
		t.Errorf("in-band size report = %q, want %q", got, want)
	}
	got, err = term.Geometry()
	if err != nil {
		t.Fatalf("geometry after the resize: %v", err)
	}
	if got.Cols != 30 || got.Rows != 5 {
		t.Errorf("after the resize the terminal reports %+v", got)
	}

	// A refused size leaves the commit in force standing, so a caller that
	// treats resize as an interval has two ends rather than a half-applied one.
	if _, refuseErr := term.Resize(emulator.Geometry{Cols: 0, Rows: 5}); !errors.Is(refuseErr, emulator.ErrOutOfRange) {
		t.Errorf("a zero-column geometry: got %v, want ErrOutOfRange", refuseErr)
	}
	got, err = term.Geometry()
	if err != nil {
		t.Fatalf("geometry after the refusal: %v", err)
	}
	if got.Cols != 30 || got.Rows != 5 {
		t.Errorf("the refused resize changed the terminal to %+v", got)
	}
}

// TestAlternateScreenIsEnteredAndLeftAndResized is the buffer transition, which
// design §6.8 needs the port to report rather than infer: a full-screen program
// leaves the primary screen's cells alone and the client is sent a different
// one, and a resize while the alternate screen is active does not reflow it.
func TestAlternateScreenIsEnteredAndLeftAndResized(t *testing.T) {
	term := newTerminal(t, 10, 3)
	ingest(t, term, "primary\x1b[?1049h")
	screen, err := term.Screen()
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if screen != emulator.ScreenAlternate {
		t.Fatalf("screen after the alternate buffer was entered = %v, want alternate", screen)
	}
	first, err := term.Cell(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.HasText {
		t.Errorf("the alternate screen began with %q where the primary screen had a cell", first.Grapheme)
	}

	ingest(t, term, "\x1b[HALT")
	if _, resizeErr := term.Resize(emulator.Geometry{Cols: 8, Rows: 2, CellWidthPx: 10, CellHeightPx: 20}); resizeErr != nil {
		t.Fatalf("resize on the alternate screen: %v", resizeErr)
	}
	screen, err = term.Screen()
	if err != nil {
		t.Fatalf("screen after the resize: %v", err)
	}
	if screen != emulator.ScreenAlternate {
		t.Errorf("the resize left the alternate screen: %v", screen)
	}
	row, err := term.Row(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := row.Cells[0].Grapheme; got != "A" {
		t.Errorf("the alternate screen's row 0 begins with %q after a resize, want %q", got, "A")
	}

	ingest(t, term, "\x1b[?1049l")
	screen, err = term.Screen()
	if err != nil {
		t.Fatalf("screen after leaving the alternate buffer: %v", err)
	}
	if screen != emulator.ScreenPrimary {
		t.Errorf("screen after leaving the alternate buffer = %v, want primary", screen)
	}
	restored, err := term.Cell(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Grapheme != "p" {
		t.Errorf("the primary screen came back with %q where it had %q", restored.Grapheme, "p")
	}
}

// TestReadsOutsideTheTerminalAreRefused keeps the port's bounds the port's: a
// coordinate the grid does not have is an answer of its own and not a blank
// cell, which is what a caller comparing two screens would read as a change.
func TestReadsOutsideTheTerminalAreRefused(t *testing.T) {
	term := newTerminal(t, 4, 2)
	for _, tc := range []struct{ x, y int }{{-1, 0}, {0, -1}, {4, 0}, {0, 2}} {
		if _, err := term.Cell(tc.x, tc.y); !errors.Is(err, emulator.ErrOutOfRange) {
			t.Errorf("cell %d,%d: got %v, want ErrOutOfRange", tc.x, tc.y, err)
		}
	}
	if _, err := term.Row(2); !errors.Is(err, emulator.ErrOutOfRange) {
		t.Errorf("row 2 of 2: got %v, want ErrOutOfRange", err)
	}
}

// TestCloseIsIdempotentAndEveryCallAfterItIsRefused. A runtime closes
// deliberately and defers a close, and a reader may be mid-flight; so the
// second close must be harmless and every later call must say so rather than
// reach a freed handle.
func TestCloseIsIdempotentAndEveryCallAfterItIsRefused(t *testing.T) {
	term := newTerminal(t, 8, 2)
	ingest(t, term, "text")
	term.Close()
	term.Close()

	if _, err := term.Ingest([]byte("x")); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("ingest after close: got %v, want ErrClosed", err)
	}
	if _, err := term.Row(0); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("row after close: got %v, want ErrClosed", err)
	}
	if _, err := term.Cell(0, 0); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("cell after close: got %v, want ErrClosed", err)
	}
	if _, err := term.EncodeKey(emulator.KeyEvent{Key: emulator.KeyA, Text: "a"}); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("encode after close: got %v, want ErrClosed", err)
	}
	if _, err := term.Resize(emulator.Geometry{Cols: 8, Rows: 2}); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("resize after close: got %v, want ErrClosed", err)
	}
	if _, err := term.Geometry(); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("geometry after close: got %v, want ErrClosed", err)
	}
	if _, err := term.Screen(); !errors.Is(err, emulator.ErrClosed) {
		t.Errorf("screen after close: got %v, want ErrClosed", err)
	}
}

// TestConcurrentIngestAndReadIsSerialised is ADR-0065 point 3's explicit
// serialisation, and it is stated as a schedule rather than as a duration: a
// reader goroutine reads rows and cells while output is ingested, and the
// contract is that this completes rather than corrupting or deadlocking. The
// effect callbacks run inside the lock the writer holds, so a callback that
// took the lock would deadlock on the first reply — this test would hang.
func TestConcurrentIngestAndReadIsSerialised(t *testing.T) {
	term := newTerminal(t, 20, 5)
	done := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := term.Row(0); err != nil {
					t.Errorf("concurrent row: %v", err)
					return
				}
				if _, err := term.Cell(1, 1); err != nil {
					t.Errorf("concurrent cell: %v", err)
					return
				}
			}
		}()
	}
	for i := range 500 {
		ingest(t, term, fmt.Sprintf("line %d\r\n", i))
		if _, err := term.EncodeKey(emulator.KeyEvent{Key: emulator.KeyA, Text: "a"}); err != nil {
			t.Fatalf("concurrent encode: %v", err)
		}
	}
	close(done)
	readers.Wait()
}

// TestSiblingTerminalsDoNotShareState: one runtime holds one terminal per
// session, so creating, using and closing one must not disturb another.
func TestSiblingTerminalsDoNotShareState(t *testing.T) {
	first := newTerminal(t, 8, 2)
	second := newTerminal(t, 8, 2)

	ingest(t, first, "first")
	ingest(t, second, "second")
	first.Close()

	cell, err := second.Cell(0, 0)
	if err != nil {
		t.Fatalf("the surviving terminal: %v", err)
	}
	if cell.Grapheme != "s" {
		t.Errorf("the surviving terminal holds %q, want its own content", cell.Grapheme)
	}
	if _, err := second.Ingest([]byte("more")); err != nil {
		t.Errorf("the surviving terminal refused output: %v", err)
	}
}

// A chord under the Kitty keyboard protocol is the key, not its text
// (nocx-zg3k3.14.20). The session runtime hands the encoder Ctrl+C with the
// layout's unmodified text "c", which is what the encoder's contract asks for;
// codex pushes the Kitty flags, and Ctrl+C reached it as a plain "c" — it
// could not be interrupted and typed the letter instead.
func TestEncodeKey_ChordUnderKittyFlagsIsTheKeyNotItsText(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ctrlC := emulator.KeyEvent{Key: emulator.KeyC, Mods: emulator.Mods{Ctrl: true}, Text: "c"}

	got, err := term.EncodeKey(ctrlC)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x03"; string(got) != want {
		t.Errorf("legacy: Ctrl+C encoded as %q, want %q", got, want)
	}

	for _, tc := range []struct {
		name   string
		ingest string
		want   string
	}{
		{"disambiguate", "\x1b[>1u", "\x1b[99;5u"},
		// What crossterm (codex) pushes: disambiguate, event types and
		// alternate keys. A press carries no event type of its own.
		{"crossterm's flags", "\x1b[>7u", "\x1b[99;5u"},
	} {
		ingest(t, term, tc.ingest)
		got, err := term.EncodeKey(ctrlC)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: Ctrl+C encoded as %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAnErasedCellKeepsTheBackgroundItWasErasedWith: an erase under a set
// background (BCE) leaves cells that carry no text and no style, only a
// background colour, which the library stores as the cell's CONTENT rather
// than its style. Reading only the style lost it, so mc's menu bar and
// htop's header stopped where their text did (nocx-zg3k3.26). Both read
// paths are asserted: the screen's own cell and a row read from history.
func TestAnErasedCellKeepsTheBackgroundItWasErasedWith(t *testing.T) {
	term := newTerminal(t, 10, 2)
	ingest(t, term, "\x1b[44mAB\x1b[K\x1b[0m\r\n\x1b[48;2;1;2;3mC\x1b[K\x1b[0m")
	palette := emulator.Color{Kind: emulator.ColorPalette, Palette: 4}
	rgb := emulator.Color{Kind: emulator.ColorRGB, RGB: emulator.RGB{R: 1, G: 2, B: 3}}
	for _, tc := range []struct {
		x, y int
		want emulator.Color
	}{{5, 0, palette}, {9, 0, palette}, {5, 1, rgb}} {
		got, err := term.Cell(tc.x, tc.y)
		if err != nil {
			t.Fatalf("cell %d,%d: %v", tc.x, tc.y, err)
		}
		if got.HasText || got.Style.Background != tc.want {
			t.Errorf("screen cell %d,%d = text %v background %+v, want no text on %+v", tc.x, tc.y, got.HasText, got.Style.Background, tc.want)
		}
	}
	// Push both rows into history and read them back there.
	ingest(t, term, "\r\n\r\n\r\n")
	page, err := term.HistoryRows(0, 2)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(page.Rows) < 2 {
		t.Fatalf("history rows = %d, want 2", len(page.Rows))
	}
	if got := page.Rows[0].Cells[7].Style.Background; got != palette {
		t.Errorf("history row 0 cell 7 background = %+v, want %+v", got, palette)
	}
	if got := page.Rows[1].Cells[7].Style.Background; got != rgb {
		t.Errorf("history row 1 cell 7 background = %+v, want %+v", got, rgb)
	}
}
