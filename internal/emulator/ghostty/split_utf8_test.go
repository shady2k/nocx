package ghostty

// A UTF-8 SEQUENCE SPLIT ACROSS INGEST CALLS IS STILL ONE CODEPOINT
// (nocx-15b13).
//
// The PTY hands a program's output over in whatever sizes the kernel chose, and
// a write of one character is not a write of one codepoint: a local tool that
// writes a Nerd Font glyph can deliver its bytes in separate reads. Measured on
// the shipping tree through the e2e: a local pane writing U+E0B0 in ONE write
// keeps the glyph, and the same three bytes in three writes leave an EMPTY cell
// — the sequence was dropped, not decoded as U+FFFD.
//
// The library is the decoder (ADR-0065), so this is where the property is
// asserted — at the port, on the cells a caller reads.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// rowGraphemes reads one screen row's text, skipping the blanks.
func rowGraphemes(t *testing.T, term emulator.Terminal, y int) string {
	t.Helper()
	row, err := term.Row(y)
	if err != nil {
		t.Fatalf("row %d: %v", y, err)
	}
	var out strings.Builder
	for _, cell := range row.Cells {
		if cell.HasText {
			out.WriteString(cell.Grapheme)
		}
	}
	return out.String()
}

func TestASplitUTF8SequenceIsOneCodepoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"powerline separator", "\ue0b0"},
		{"nerd font private use", "\uf600"},
		{"supplementary private use", "\U000f0000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.text)
			// ONE BYTE PER WRITE, the shape a program that writes a glyph in
			// pieces produces.
			split := newTerminal(t, 8, 2)
			for _, b := range payload {
				ingest(t, split, string([]byte{b}))
			}
			if got := rowGraphemes(t, split, 0); got != tc.text {
				t.Errorf("after %d one-byte writes the row reads %q, want %q",
					len(payload), got, tc.text)
			}
			// And the whole sequence in one write, the control: the codepoint
			// is one this terminal can hold at all.
			whole := newTerminal(t, 8, 2)
			ingest(t, whole, tc.text)
			if got := rowGraphemes(t, whole, 0); got != tc.text {
				t.Fatalf("one write of %q reads %q", tc.text, got)
			}
		})
	}
}
