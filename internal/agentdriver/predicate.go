package agentdriver

// The closed predicate set.
//
// A rule document composes these and can invent none. That closure is the
// safety property: a person may say WHERE to look and WHAT to look for, and
// may not lift a bound the engine enforces — a region's cap, the cursor's
// unforgeability, the exact column a marker must occupy.
//
// Every predicate here is a pure function of one panegrid.Frame. Nothing
// remembers a previous frame, because Driver's own contract forbids it: "a
// rule that remembers is a rule that can be stuck".

import (
	"regexp"
	"strings"

	"github.com/shady2k/nocx/internal/panegrid"
)

// cursorOn answers whether the cursor's OWN cell carries this text.
//
// This is the predicate an agent cannot forge. Printed text cannot take the
// cursor: the TUI parks it after every repaint, so a "❯ 1. Yes" an agent wrote
// into its own transcript sits under no cursor and matches nothing.
func cursorOn(f panegrid.Frame, glyph string) bool {
	cell, ok := cellAt(f, f.CursorX, f.CursorY)
	return ok && cell.Text == glyph
}

// rowOpensWith answers whether the first NON-BLANK cell of a row is this text.
// The transcript is indented and the chrome is not, so "opens with" is what
// separates a marker from the same glyph wrapped into a sentence.
func rowOpensWith(f panegrid.Frame, row int, glyph string) bool {
	col, ok := firstNonBlankCol(f, row)
	if !ok {
		return false
	}
	cell, ok := cellAt(f, col, row)
	return ok && cell.Text == glyph
}

// cellAtCol answers whether an EXACT column of a row carries this text.
//
// This is deliberately not rowOpensWith, and the two must never be folded
// together: the input box requires its marker at column 0 exactly, while a
// menu marker may open an indented row. The box is one of the two markers the
// driver's safety argument rests on, and widening it would weaken that.
func cellAtCol(f panegrid.Frame, row, col int, glyph string) bool {
	cell, ok := cellAt(f, col, row)
	return ok && cell.Text == glyph
}

// fullWidthRule answers whether a row is nothing but one repeated cell, edge
// to edge. It does NOT skip zero-width continuation cells, unlike Frame.Text:
// a rule is a statement about every column, and a row that needs interpreting
// to look like one is not one.
func fullWidthRule(f panegrid.Frame, row int, glyph string) bool {
	if row < 0 || row >= len(f.Lines) || f.Cols <= 0 {
		return false
	}
	line := f.Lines[row]
	if len(line) < f.Cols {
		return false
	}
	for x := 0; x < f.Cols; x++ {
		if line[x].Text != glyph {
			return false
		}
	}
	return true
}

// rowContains reads the anchor's OWN row. Every other text predicate here
// looks above or below an anchor; the mode line has to be read where it sits.
func rowContains(f panegrid.Frame, row int, text string) bool {
	if row < 0 || row >= f.Rows {
		return false
	}
	return strings.Contains(f.Text(row), text)
}

// nearestNonBlankAbove walks up from a row and returns the first row with any
// non-blank cell, rendered whole.
func nearestNonBlankAbove(f panegrid.Frame, row int) (string, bool) {
	for i := row - 1; i >= 0; i-- {
		if _, ok := firstNonBlankCol(f, i); ok {
			return f.Text(i), true
		}
	}
	return "", false
}

// region is a search area computed from an anchor rather than from a fixed
// row, and its bounds belong to the engine.
//
// maxRows is the cap that stops an agent whose printed lines abut the chrome
// from extending the area a forged marker would be looked for in. stopAtBlank
// and col0Only look similar and are not: a blank row ENDS the region, while an
// indented row is merely SKIPPED. The status stack is a contiguous run of
// unindented rows, so both are needed and neither substitutes for the other.
type region struct {
	anchor      int
	up          bool
	maxRows     int
	col0Only    bool
	stopAtBlank bool
}

// eachRow walks the region once, in order, handing every candidate row's
// right-trimmed text to visit, and stops when visit says so or the region ends.
//
// It is the ONE walk. anyRow is this with a boolean out-parameter and capture
// is this collecting, so the cap, the blank terminator, the indent skip and the
// frame's own edge are enforced in a single place — a second walk written
// beside it is a second set of bounds, and the whole argument for the region is
// that its bounds are the engine's.
func (r region) eachRow(f panegrid.Frame, visit func(text string) bool) {
	for i := 0; i < r.maxRows; i++ {
		y := r.anchor + 1 + i
		if r.up {
			y = r.anchor - 1 - i
		}
		if y < 0 || y >= f.Rows {
			return
		}
		text := strings.TrimRight(f.Text(y), " ")
		if strings.TrimSpace(text) == "" {
			if r.stopAtBlank {
				return
			}
			continue
		}
		if r.col0Only {
			if col, ok := firstNonBlankCol(f, y); !ok || col != 0 {
				continue
			}
		}
		if !visit(text) {
			return
		}
	}
}

// anyRow reports whether any row inside the region satisfies match, which is
// handed the row's text right-trimmed.
func (r region) anyRow(f panegrid.Frame, match func(text string) bool) bool {
	found := false
	r.eachRow(f, func(text string) bool {
		if match(text) {
			found = true
			return false
		}
		return true
	})
	return found
}

// capture is the region's OTHER answer, and the half predicate.go did not have
// until the driver had to report a subagent panel it could plainly see. Every
// predicate above says yes or no about a row; this reads values out of one.
//
// The pattern chooses WHAT comes out. The region chooses WHERE the rows come
// from, and a document cannot widen it — which is the same argument maxRows
// carries for a forged spinner, applied to a forged panel row. A group that did
// not participate in a match contributes no key, so an absent field is absent
// rather than empty.
func (r region) capture(f panegrid.Frame, re *regexp.Regexp) []map[string]string {
	names := re.SubexpNames()
	var out []map[string]string
	r.eachRow(f, func(text string) bool {
		m := re.FindStringSubmatch(text)
		if m == nil {
			return true
		}
		row := make(map[string]string, len(names))
		for i, name := range names {
			if i == 0 || name == "" || m[i] == "" {
				continue
			}
			row[name] = m[i]
		}
		out = append(out, row)
		return true
	})
	return out
}

// numberedOption matches the option grammar after a menu marker: optional
// spaces, one or more digits, a dot, a space.
func numberedOption(s string) bool {
	s = strings.TrimLeft(s, " ")
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits+1 >= len(s) {
		return false
	}
	return s[digits] == '.' && s[digits+1] == ' '
}

// belowVerdict is why this predicate is not a boolean.
//
// Three cases have to stay apart, and a boolean collapses two of them in the
// expensive direction: everything down there is recognised chrome; something
// down there is NOT, which must answer unknown; or nothing is drawn down there
// at all, which decides nothing and must fall through to the branches after
// it. Folding the middle into the last turns a refusal into free_text, and a
// wrong free_text is a keystroke into whatever appears next.
type belowVerdict int

const (
	belowNothing belowVerdict = iota
	belowAllMatched
	belowCounterexample
)

// belowAnchorOpensOnlyWith reads what is drawn below the first non-blank row
// beneath an anchor — chrome territory the transcript cannot reach, because
// the transcript scrolls above the input box.
//
// The frame's own bottom edge is the cap here, and that is the engine owning
// the bound as much as maxRows is elsewhere: there is nothing below the last
// row to reach.
func belowAnchorOpensOnlyWith(f panegrid.Frame, anchor int, glyphs []string) belowVerdict {
	mode, ok := firstNonBlankRowBelow(f, anchor)
	if !ok {
		return belowNothing
	}
	out := belowNothing
	for y := mode + 1; y < f.Rows; y++ {
		col, ok := firstNonBlankCol(f, y)
		if !ok {
			continue
		}
		cell, ok := cellAt(f, col, y)
		if !ok {
			continue
		}
		matched := false
		for _, g := range glyphs {
			if cell.Text == g {
				matched = true
				break
			}
		}
		if !matched {
			return belowCounterexample
		}
		out = belowAllMatched
	}
	return out
}

// ── frame arithmetic ──────────────────────────────────────────────────────
//
// These were claude.go's until the rule became a document. They are not
// predicates — they are how a predicate reads a row — and they are here rather
// than beside one agent because every rule needs them.

func cellAt(f panegrid.Frame, x, y int) (panegrid.Cell, bool) {
	if y < 0 || y >= len(f.Lines) {
		return panegrid.Cell{}, false
	}
	if x < 0 || x >= len(f.Lines[y]) {
		return panegrid.Cell{}, false
	}
	return f.Lines[y][x], true
}

func firstNonBlankCol(f panegrid.Frame, y int) (int, bool) {
	if y < 0 || y >= len(f.Lines) {
		return 0, false
	}
	for x, c := range f.Lines[y] {
		if c.Width == 0 {
			continue
		}
		if strings.TrimSpace(c.Text) != "" {
			return x, true
		}
	}
	return 0, false
}

// rowTextFrom renders a row from a column onwards, so a marker's own cell does
// not have to be trimmed off the front of the text it introduces.
func rowTextFrom(f panegrid.Frame, y, from int) string {
	if y < 0 || y >= len(f.Lines) {
		return ""
	}
	var b strings.Builder
	for x, c := range f.Lines[y] {
		if x < from || c.Width == 0 {
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Text)
	}
	return strings.TrimRight(b.String(), " ")
}

func firstNonBlankRowBelow(f panegrid.Frame, y int) (int, bool) {
	for i := y + 1; i < f.Rows && i < len(f.Lines); i++ {
		if _, ok := firstNonBlankCol(f, i); ok {
			return i, true
		}
	}
	return 0, false
}
