package agentdriver

// The Claude Code driver.
//
// Every rule below was read off the corpus in testdata/captures, which is real
// byte streams from a real PTY at 120x40 against Claude Code v2.1.238. Three
// of them contradict what reasoning about the TUI had predicted, and the
// contradictions are named where they bite.

import (
	"strings"

	"github.com/shady2k/nocx/internal/panegrid"
)

type claudeDriver struct{}

// Claude returns the driver for Claude Code.
func Claude() Driver { return claudeDriver{} }

func (claudeDriver) Agent() string { return "claude" }

// Classify reads the frame's chrome. The order of the branches is a safety
// property, not a style: the menu branch is evaluated BEFORE the free-text
// branch, so a dialog can never be masked by an input box still drawn beneath
// it. In this agent the dialog REPLACES the box, so the two never coexist —
// the order costs nothing here and is what an agent that overlays them needs.
func (claudeDriver) Classify(f panegrid.Frame) State {
	if f.Rows <= 0 || f.Cols <= 0 || len(f.Lines) == 0 {
		return StateUnknown
	}
	box, hasBox := claudeInputBox(f)

	if kind, ok := claudeMenu(f, box, hasBox); ok {
		return kind
	}
	if !hasBox {
		// No input box and no menu: a screen this driver does not recognise.
		// It is not idle, and saying so is the point of having the value.
		return StateUnknown
	}
	if claudeSpinnerIsLive(f, box) {
		return StateWorking
	}
	switch claudeUnderTheModeLine(f, box) {
	case underTaskPanel:
		return StateWorking
	case underSomethingElse:
		return StateUnknown
	}
	if claudeModeLineReportsABackgroundAgent(f, box) {
		return StateWorking
	}
	return StateFreeText
}

// ── the input box ─────────────────────────────────────────────────────────

// claudeBox is where the input box's furniture sits on this frame. Nothing
// here is a fixed row: the box floats, because a task panel drawn under the
// mode line pushes the whole assembly up.
type claudeBox struct {
	topRule    int // the full-width rule above the prompt row
	prompt     int // the row carrying the input marker
	bottomRule int // the full-width rule below it
	meter      int // topRule-1: the right-aligned token counter / effort row
}

// claudeInputBox finds the box, and requires TWO markers because one is not
// enough here — and not as a precaution. The approval dialog's selected row
// reads "❯ 1. Yes", the same glyph the input marker uses, so a rule that looks
// for "❯" and stops has just found an input box inside a dialog whose first
// option is "Yes". The input row is only an input row when it sits between the
// two full-width rules that bound the box, and no dialog has those.
func claudeInputBox(f panegrid.Frame) (claudeBox, bool) {
	bottom := -1
	for y := f.Rows - 1; y >= 0; y-- {
		if claudeIsFullWidthRule(f, y) {
			bottom = y
			break
		}
	}
	if bottom < 2 {
		return claudeBox{}, false
	}
	top := -1
	for y := bottom - 2; y >= 1; y-- {
		if claudeIsFullWidthRule(f, y) {
			top = y
			break
		}
	}
	if top < 1 {
		return claudeBox{}, false
	}
	prompt := top + 1
	if cell, ok := cellAt(f, 0, prompt); !ok || cell.Text != "❯" {
		return claudeBox{}, false
	}
	return claudeBox{topRule: top, prompt: prompt, bottomRule: bottom, meter: top - 1}, true
}

// claudeIsFullWidthRule is the box's own marker: a row that is nothing but the
// horizontal rule, edge to edge. The transcript is indented and wrapped, so it
// does not produce one by accident.
func claudeIsFullWidthRule(f panegrid.Frame, y int) bool {
	if y < 0 || y >= len(f.Lines) || f.Cols <= 0 {
		return false
	}
	line := f.Lines[y]
	if len(line) < f.Cols {
		return false
	}
	for x := 0; x < f.Cols; x++ {
		if line[x].Text != "─" {
			return false
		}
	}
	return true
}

// ── the menu, anchored on the cursor ──────────────────────────────────────

// claudeMenu recognises a numbered menu awaiting a choice, and it anchors on
// the CURSOR rather than on the glyph. Measured: in the approval dialog the
// cursor sits on the selected option's "❯" (1,26); in the /model menu it sits
// on the same glyph (3,31); in every frame with a live input box it sits just
// after the input marker instead. An agent's output can print "❯ 1. Yes" into
// its own transcript, and this repository has burned itself on exactly that
// kind of self-match before — but printed text cannot take the cursor, because
// the TUI parks it after every repaint.
func claudeMenu(f panegrid.Frame, box claudeBox, hasBox bool) (State, bool) {
	y, x := f.CursorY, f.CursorX
	cell, ok := cellAt(f, x, y)
	if !ok || cell.Text != "❯" {
		return "", false
	}
	// The marker must OPEN its row. A "❯" the transcript wrapped into the
	// middle of a sentence is not a selected option.
	if col, ok := firstNonBlankCol(f, y); !ok || col != x {
		return "", false
	}
	// And the row must read as a numbered option: "❯ 1. Yes".
	if !claudeIsNumberedOption(rowTextFrom(f, y, x+1)) {
		return "", false
	}
	// A menu sits ABOVE the input box's furniture when both are on screen.
	if hasBox && y >= box.meter {
		return "", false
	}
	// Which menu it is decides whether answering it answers the AGENT. The
	// approval dialog states its question directly above the options; the
	// user-opened menus describe themselves instead.
	if q, ok := nearestNonBlankAbove(f, y); ok && strings.Contains(q, "Do you want to") {
		return StatePermissionChoice, true
	}
	return StateModalChoice, true
}

// claudeIsNumberedOption matches the option grammar after the marker: a
// number, a dot, a space.
func claudeIsNumberedOption(s string) bool {
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

// ── the turn in flight ────────────────────────────────────────────────────

// claudeSpinnerIsLive answers whether a turn is running.
//
// TWO corrections to what the corpus was expected to show, both of which
// change the rule:
//
// The spinner is NOT at a fixed offset above the input box. It sits in the
// status stack — the contiguous run of rows directly above the token meter —
// and that stack grows: with a tip line present the spinner is three rows
// above the top rule rather than two (claude-subagent at 42s).
//
// And POSITION alone does not separate a live spinner from a finished one. The
// turn's closing summary lands in the same slot and stays there: "✻ Sautéed
// for 29s" occupies it from 46s to the end of claude-subagent, long after the
// turn ended. So the stack decides WHERE to look and the grammar decides WHAT
// it found: a live spinner always carries "… (" and closes its parenthesis —
// "* Misting… (2s)", "✶ Lollygagging… (2s · ↓ 68 tokens)" — and a finished one
// never does.
//
// The stack is capped, so an agent whose last printed lines happen to abut the
// meter cannot extend the region a forged spinner would be looked for in.
func claudeSpinnerIsLive(f panegrid.Frame, box claudeBox) bool {
	const maxStack = 4
	for i := 0; i < maxStack; i++ {
		y := box.meter - 1 - i
		if y < 0 {
			return false
		}
		text := strings.TrimRight(f.Text(y), " ")
		if strings.TrimSpace(text) == "" {
			return false // the stack ends at the first blank row
		}
		if col, ok := firstNonBlankCol(f, y); !ok || col != 0 {
			continue // the transcript is indented; the status stack is not
		}
		if strings.Contains(text, "… (") && strings.HasSuffix(text, ")") {
			return true
		}
	}
	return false
}

// ── the background agent, which is the trap ───────────────────────────────

type underMode int

const (
	underNothing underMode = iota
	underTaskPanel
	underSomethingElse
)

// claudeUnderTheModeLine reads what is drawn BELOW the mode line — chrome
// territory the transcript can never reach, because the transcript scrolls
// above the input box.
//
// This is half of the subagent trap, and it is the half the bead did not
// predict. With a backgrounded agent running, Claude keeps the input box live,
// keeps the mode line, and shows NO spinner; between 25s and 38s of
// claude-subagent the only thing on screen saying work is happening is a task
// panel under the mode line: "● main" over "◯ Explore  List files in
// directory". A rule written from "input box and no spinner means idle"
// classifies that pane as ready for input, and under the typing design that is
// a keystroke into whatever appears next.
//
// Anything else drawn down here is chrome this driver has not seen, and it
// answers unknown rather than guessing — unknown is treated as busy, so the
// failure direction is a refusal.
func claudeUnderTheModeLine(f panegrid.Frame, box claudeBox) underMode {
	mode, ok := firstNonBlankRowBelow(f, box.bottomRule)
	if !ok {
		return underNothing
	}
	out := underNothing
	for y := mode + 1; y < f.Rows; y++ {
		col, ok := firstNonBlankCol(f, y)
		if !ok {
			continue
		}
		switch f.Lines[y][col].Text {
		case "●", "◯":
			out = underTaskPanel
		default:
			return underSomethingElse
		}
	}
	return out
}

// claudeModeLineReportsABackgroundAgent is the trap's other half, and it has
// both ends. Once the task panel collapses, the mode line carries the segment
// "· /tasks to see subagents ·" instead — and it is live evidence rather than
// a permanent hint: in claude-subagent it appears at 40s and is GONE by 70s,
// when the background agent has finished and the pane is genuinely waiting.
// A rule without that second end would leave the pane refusing input for the
// rest of the session.
func claudeModeLineReportsABackgroundAgent(f panegrid.Frame, box claudeBox) bool {
	mode, ok := firstNonBlankRowBelow(f, box.bottomRule)
	if !ok {
		return false
	}
	return strings.Contains(f.Text(mode), "/tasks to see subagents")
}

// ── frame arithmetic ──────────────────────────────────────────────────────

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
