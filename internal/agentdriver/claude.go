package agentdriver

// The Claude Code rule, and the measurements that shaped it.
//
// The rule itself is claude.rule.json — a document in the grammar of
// document.go, composing the predicates of predicate.go. This file loads it
// and records WHY it says what it says, because a JSON document cannot carry
// its own reasoning and the reasoning is the expensive part.
//
// Every branch below was read off the corpus in testdata/captures, which is
// real byte streams from a real PTY at 120x40 against Claude Code v2.1.238.
// Three of them contradict what reasoning about the TUI had predicted.
//
// # The input box floats, so nothing is a fixed row
//
// The box is found by its two full-width rules, searched UPWARD from the
// bottom, because a task panel drawn under the mode line moves it. The prompt
// marker must sit at column zero EXACTLY — not merely open its row — and if it
// does not, the box did not bind and every anchor derived from it goes absent
// together. That is what requireBound is for: a compound piece of chrome binds
// or fails whole.
//
// # The menu anchors on the CURSOR, not on the glyph
//
// Measured: in the approval dialog the cursor sits on the selected option's
// "❯" at (1,26); in the /model menu on the same glyph at (3,31); in every
// frame with a live input box it sits just after the input marker instead. An
// agent's output can print "❯ 1. Yes" into its own transcript, and this
// repository has burned itself on exactly that kind of self-match before — but
// printed text cannot take the cursor, because the TUI parks it after every
// repaint. Which menu it is decides whether answering it answers the AGENT:
// the approval dialog states its question directly above the options, so the
// permission branch is ordered before the modal branch and tests for it.
//
// # The spinner is not at a fixed offset, and position alone does not decide
//
// FIRST CORRECTION. The spinner sits in the status stack — the contiguous run
// of rows directly above the token meter — and that stack grows: with a tip
// line present it is three rows above the top rule rather than two
// (claude-subagent at 42s). Hence a region rather than a row.
//
// SECOND CORRECTION. The turn's closing summary lands in the same slot and
// stays there: "✻ Sautéed for 29s" occupies it from 46s to the end of
// claude-subagent, long after the turn ended. So the region decides WHERE to
// look and the grammar decides WHAT it found: a live spinner always carries
// "… (" and closes its parenthesis — "* Misting… (2s)" — and a finished one
// never does. The region also stops at the first blank row, which is what
// keeps it from climbing past that summary into the transcript, and it is
// capped at four so an agent whose printed lines abut the meter cannot extend
// the area a forged spinner would be looked for in.
//
// # The background agent is the trap, and it has two halves and two ends
//
// THIRD CORRECTION, and the half that was not predicted. With a backgrounded
// agent running, Claude keeps the input box live, keeps the mode line, and
// shows NO spinner; between 25s and 38s of claude-subagent the only thing on
// screen saying work is happening is a task panel under the mode line: "● main"
// over "◯ Explore  List files in directory". A rule written from "input box and
// no spinner means idle" classifies that pane as ready for input, and under the
// typing design that is a keystroke into whatever appears next.
//
// Anything ELSE drawn down there is chrome this rule has not seen, and it
// answers unknown rather than guessing — unknown is treated as busy, so the
// failure direction is a refusal. That is why the below-the-mode-line branch is
// three-valued and not a conjunction.
//
// The trap's other half is the mode line itself, and it has both ends: once the
// task panel collapses the mode line carries "· /tasks to see subagents ·"
// instead, and it is live evidence rather than a permanent hint — in
// claude-subagent it appears at 40s and is GONE by 70s, when the background
// agent has finished and the pane is genuinely waiting. A rule without that
// second end would leave the pane refusing input for the rest of the session.
//
// # The panel says more than "working", and the extractor is how that survives
//
// The same panel that decides the verdict carries, per row, an agent name, the
// task it was given, how long it has been running and how many tokens have
// flowed which way — and the verdict is the single word "working". The
// "subagents" extractor reads those rows out of the SAME chrome the verdict is
// decided from, and its yield reaches nothing that decides: it is anchored on
// the mode line, capped by the engine, and evaluated after the branches. So the
// panel can be reported row by row without any of it being able to move the
// answer.
//
// The fields are what the rows actually carry, not what a subagent feature will
// want. "● main" is the parent and carries a name only; a child carries a task
// and, once it has run a second, an elapsed time and a token flow. A capture
// group that did not participate contributes no field at all, so a child with
// no timing yet is SILENT about timing rather than claiming zero.

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed claude.rule.json
var claudeRuleJSON []byte

// claudeDriver is parsed, validated and compiled once, at package init. A
// malformed document is a wiring mistake and belongs to process start, exactly
// as NewRegistry's three refusals do — and Claude() cannot report it, because
// it takes no arguments, returns no error, and is called twice in one
// expression by an existing test.
var claudeDriver = mustParseDocument(claudeRuleJSON)

// Claude returns the driver for Claude Code.
func Claude() Driver { return claudeDriver }

func mustParseDocument(raw []byte) documentDriver {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic(fmt.Sprintf("agentdriver: rule document does not parse: %v", err))
	}
	d, err := newDocumentDriver(doc)
	if err != nil {
		panic(fmt.Sprintf("agentdriver: rule document is not usable: %v", err))
	}
	return d
}
