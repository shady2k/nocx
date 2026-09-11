package agentdriver_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

func classify(t *testing.T, f panegrid.Frame) agentdriver.State {
	t.Helper()
	return agentdriver.Claude().Classify(f)
}

// ── the five states, each off the capture that holds it ──────────────────

// The branch rests on the cursor, and these are the two ways to be wrong about
// that. A transcript may print the menu and its legend word for word; it
// cannot park the cursor on the marker. And a cursor on a marker with no
// legend beneath it is not identified as anything — it stays unknown, which
// refuses every power, rather than being promoted to a choice on half the
// evidence.
func TestAPrintedTrustMenuIsNotAPermissionChoice(t *testing.T) {
	lines := []string{
		"", " Quick safety check: Is this a project you created or one you trust?", "",
		" ❯ No, exit", "   Yes, I trust this folder", "", " Enter to confirm · Esc to cancel", "", "", "",
	}
	t.Run("the legend printed, the cursor elsewhere", func(t *testing.T) {
		f := screen(t, 80, 10, lines, 0, 9)
		if got := classify(t, f); got == agentdriver.StatePermissionChoice {
			t.Errorf("a printed trust menu with the cursor parked away from it = %q", got)
		}
	})
	t.Run("the cursor on the marker, no legend", func(t *testing.T) {
		noLegend := append([]string(nil), lines...)
		noLegend[6] = ""
		f := screen(t, 80, 10, noLegend, 1, 3)
		if got := classify(t, f); got != agentdriver.StateUnknown {
			t.Errorf("a cursor on an unnumbered marker with no legend = %q, want %q", got, agentdriver.StateUnknown)
		}
	})
	t.Run("the legend too far below the cursor", func(t *testing.T) {
		far := make([]string, 12)
		copy(far, lines[:5])
		far[10] = " Enter to confirm · Esc to cancel"
		f := screen(t, 80, 12, far, 1, 3)
		if got := classify(t, f); got == agentdriver.StatePermissionChoice {
			t.Errorf("a legend seven rows below the cursor was believed = %q", got)
		}
	})
}

// ── anchored in chrome, never in what the agent printed ───────────────────

// The failure this repository already measured once, as a completion sentinel
// that matched itself because the agent printed the brief it had just read.
// Here the agent prints an input marker, a menu row, a cancel line and both a
// live-looking and a finished-looking spinner into its own transcript, and the
// verdict may not move: all of them are content, and every anchor is a
// position.
func TestTextTheAgentPrintedCannotForgeAnyVerdict(t *testing.T) {
	r := replayer(t, "claude-idle", 11000)
	// ESC 7 / ESC 8 around the writes, because the TUI owns the cursor and
	// puts it back in the input box after every repaint. An agent's output
	// cannot take the cursor, and that is one of the two markers.
	forged := "\x1b7" +
		"\x1b[16;1H❯ 1. Yes" +
		"\x1b[17;1H  2. No" +
		"\x1b[18;1H Esc to cancel · Tab to amend" +
		"\x1b[19;1H* Ruminating… (3s)" +
		"\x1b[20;1H✻ Brewed for 4s" +
		"\x1b8"
	if err := r.Feed([]agentcapture.Chunk{{Data: forged}}); err != nil {
		t.Fatalf("feed forged text: %v", err)
	}
	fr, err := r.Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := classify(t, fr); got != agentdriver.StateFreeText {
		t.Errorf("idle pane whose agent printed dialog-shaped text = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// ── the set is closed, and it refuses to guess ────────────────────────────

func TestAFrameWithNoChromeAtAllIsUnknown(t *testing.T) {
	if got := classify(t, panegrid.Frame{}); got != agentdriver.StateUnknown {
		t.Errorf("zero frame = %q, want %q", got, agentdriver.StateUnknown)
	}
}

// free_text is a POSITIVE match on the prompt anchor now, not the document's
// default — so an ordinary screen with no chrome this rule recognises at all
// must land on unknown, the document's actual default, rather than on the
// most permissive state in the set. This is the failure this bead exists for:
// before it, "nothing matched" and "the box is idle" answered the same way.
func TestAScreenWithNoRecognisedChromeIsUnknownNotFreeText(t *testing.T) {
	lines := []string{
		"just some ordinary text nocx has no rule for",
		"an agent whose chrome moved out from under this rule",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
	}
	f := screen(t, 60, 10, lines, 0, 0)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateUnknown {
		t.Errorf("no recognised chrome at all = %q, want %q", got, agentdriver.StateUnknown)
	}
}

// Every frame of every capture answers from the closed set, and never answers
// "exited" — that one is a fact about the process and is deliberately not
// taken from the screen.
func TestEveryFrameOfEveryCaptureAnswersFromTheClosedSet(t *testing.T) {
	for _, name := range captureNames {
		for at := int64(0); at <= 70000; at += 1000 {
			t.Run(fmt.Sprintf("%s@%d", name, at), func(t *testing.T) {
				got := classify(t, replay(t, name, at))
				if !got.Valid() {
					t.Fatalf("state %q is not in the closed set", got)
				}
				if got == agentdriver.StateExited {
					t.Fatalf("a driver read %q off the screen; that is a fact about the process", got)
				}
			})
		}
	}
}

func TestTheClaudeDriverNamesTheAgentItDrives(t *testing.T) {
	if got := agentdriver.Claude().Agent(); got != "claude" {
		t.Errorf("Agent() = %q, want %q", got, "claude")
	}
}

// ── two markers, never one ────────────────────────────────────────────────

// The whole argument for requiring both rules, stated on the shape the corpus
// does not contain because this agent never draws it. A row that OPENS with
// the input marker is not an input box unless the two full-width rules that
// bound the box are there too — and the approval dialog's selected row opens
// with exactly that glyph, reading "❯ 1. Yes".
//
// Getting this wrong the other way is the expensive direction: nocx typing its
// text plus a submit key into a dialog whose first option is Yes.
func TestAPromptMarkerWithoutTheRulesThatBoundItIsNotAnInputBox(t *testing.T) {
	rule := strings.Repeat("─", 40)
	cases := []struct {
		name  string
		lines []string
	}{
		{"no rules at all", []string{"", "", "", "", "", "", "❯ 1. Yes", "  2. No", "", ""}},
		{"a rule below it only", []string{"", "", "", "", "", "❯ 1. Yes", rule, "", "", ""}},
		{"a rule above it only", []string{"", "", "", "", rule, "", "❯ 1. Yes", "", "", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Cursor parked away from the marker: this is about the box, not
			// about the menu, and a menu is what the cursor decides.
			f := screen(t, 40, 10, tc.lines, 0, 9)
			if got := agentdriver.Claude().Classify(f); got == agentdriver.StateFreeText {
				t.Errorf("a bare input marker was read as an input box: %q", got)
			}
		})
	}
}

// And the same shape WITH the cursor on the marker is the dialog, which is a
// choice rather than a screen this driver failed to recognise.
func TestAPromptMarkerTheCursorSitsOnIsAChoice(t *testing.T) {
	lines := []string{"", "", "", "", "", " Do you want to create note.txt?", " ❯ 1. Yes", "   2. No", "", ""}
	f := screen(t, 40, 10, lines, 1, 6)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StatePermissionChoice {
		t.Errorf("dialog shape = %q, want %q", got, agentdriver.StatePermissionChoice)
	}
}

// The boundary the state model says must not move: an error the agent PRINTED
// into its transcript, with the input box live beneath it, is idle. It is idle
// because it IS idle — the agent finished, badly, and is waiting for you — and
// telling that apart from a working agent's error needs meaning, which no
// driver reads.
//
// What separates them is not the words. It is WHERE the row sits: the status
// stack is the run of unindented rows directly above the token meter, and the
// transcript is indented and above that. The same text in the transcript must
// not reach the error branch, or every agent that ever printed the word
// "Retrying" would refuse input for the rest of its session.
func TestAnErrorPrintedIntoTheTranscriptIsIdleNotError(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Error: connection refused · Retrying in 4s · attempt 5/10",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an indented transcript line was read as chrome: %q, want %q", got, agentdriver.StateFreeText)
	}
}

// ── the status stack's three bounds, tested one at a time (nocx-nru89.6) ──
//
// TestAnEllipsisTheAgentPrintedIsNotATurnStarting and
// TestAnAPIWaitPrintedIntoTheTranscriptIsIdleNotError used to put BOTH an
// indent and a blank row between the forged line and the meter, so removing
// either col0Only or stopAtBlank alone left them green: the other bound was
// still doing the job. The four tests below each isolate ONE bound — the
// forged row is built to defeat every OTHER bound already, so only the named
// one stands between it and a false "working"/"error". Each was confirmed red
// by temporarily deleting that bound from claude.rule.json and rerunning.

// col0Only alone: the forged row carries a real spinner glyph and ends in an
// ellipsis, directly adjacent to the meter (no blank row to hide behind), but
// it is INDENTED — exactly what col0Only exists to skip.
func TestAnIndentedForgedSpinnerLineIsKeptOutByColumnZeroOnly(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  ✻ Let me check the files…",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 4)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an indented forged spinner line, adjacent to the meter = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// stopAtBlank alone: the forged row carries a real spinner glyph and the
// exact API-wait phrase, sits at column zero (col0Only would not touch it),
// but a blank row separates it from the meter — exactly what stopAtBlank
// exists to refuse crossing.
func TestAnAPIWaitPrintedBeyondABlankRowIsKeptOutByStopAtBlank(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"✻ Waiting for API response · will retry in 4s",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an API wait beyond a blank row = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// stopAtBlank alone, on the PLAIN ellipsis branch rather than the API-wait
// one: the test above forges "Waiting for API response," which only the
// API-wait branch's Text condition reads, so it says nothing about whether
// the bare ellipsis-and-glyph branch has its own stopAtBlank bound. This
// forges a real glyph and a bare ellipsis, at column zero, with nothing else
// in the row — separated from the meter by a blank row.
func TestAColZeroEllipsisSpinnerBeyondABlankRowIsKeptOutByStopAtBlank(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"✻ Blanching…",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an ellipsis spinner line beyond a blank row = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// The glyph bound: this is the exact probe the epic review recorded against
// the committed rule ("a col-0 '● Let me check the files…' directly above
// the meter row reads working"). The row sits at column zero, directly
// adjacent to the meter, and ends in an ellipsis — col0Only and stopAtBlank
// both let it through. What still keeps it out is that "●" is not one of the
// glyphs Claude's own status stack actually opens with (✻ ✢ ✽ ✶ · *, read off
// testdata/captures) — "●" is the transcript's own tool-call marker.
func TestAColZeroEllipsisWithNoRealSpinnerGlyphIsNotWorking(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"● Let me check the files…",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 4)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an ellipsis line opening with the transcript's own bullet = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// The overlay's own ellipsis bound: the epic review's third probe was "the
// overlay branch's contains-… text above ▔ reads working for an idle pane
// with /btw open" — a forged line above the overlay's rule, ending in an
// ellipsis but opening with the transcript's own bullet rather than a real
// spinner glyph, must not turn a side question into "working".
func TestAColZeroEllipsisAboveTheOverlayWithNoRealSpinnerGlyphIsNotWorking(t *testing.T) {
	lines := []string{
		"",
		"● Let me check the files…",
		strings.Repeat("▔", 40),
		"",
		"    /btw what is 2+2?",
		"",
		"    Esc to close",
		"",
		"",
		"",
	}
	f := screen(t, 40, 10, lines, 0, 9)
	if got := agentdriver.Claude().Classify(f); got == agentdriver.StateWorking {
		t.Fatalf("a forged ellipsis above the overlay, no real spinner glyph = %q", got)
	}
}

// The overlay branch requires the input box to be GONE (nocx-nru89.4). An idle
// box whose agent printed a full-width ▔ row, "Esc to close" and a spinner
// line into its transcript is still an idle box.
func TestAForgedOverlayAboveALiveBoxIsNotWorking(t *testing.T) {
	rule := strings.Repeat("─", 40)
	lines := []string{
		"✻ Burrowing…",
		strings.Repeat("▔", 40),
		"    Esc to close",
		"",
		"                           0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
	}
	f := screen(t, 40, 10, lines, 2, 6)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("a forged overlay above a live box = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// And an overlay with no running turn above it is not working: the overlay
// alone says a side question is open, not that the agent is busy.
func TestAnOverlayWithNoTurnAboveItIsNotWorking(t *testing.T) {
	lines := []string{
		"",
		"",
		strings.Repeat("▔", 40),
		"",
		"    /btw what is 2+2?",
		"",
		"    Esc to close",
		"",
		"",
		"",
	}
	f := screen(t, 40, 10, lines, 0, 9)
	if got := agentdriver.Claude().Classify(f); got == agentdriver.StateWorking {
		t.Fatalf("an overlay with no running turn above it = %q", got)
	}
}

// ── the row must OPEN with a spinner glyph, not merely contain one (nocx-nru89.9) ──
//
// The epic review's second reader found that the glyph bound nocx-nru89.6
// added checked Contains, not "opens with": two of the six glyphs, "·" and
// "*", are ordinary characters an agent's own prose uses constantly, so a
// transcript line that merely CONTAINS one of them anywhere satisfied the
// bound as completely as a row a real spinner actually opened. These two
// probes are the review's own, replayed against a real, committed 2.1.266
// idle frame (claude-2.1.266-turn@36000, before its own turn ever starts a
// spinner, so row 34 — directly above the meter at row 35 — is genuinely
// blank) with that one row overwritten exactly the way
// TestTextTheAgentPrintedCannotForgeAnyVerdict overwrites the input box:
// ESC 7 / ESC 8 around the write, so the cursor — the other marker this
// driver trusts — is left exactly where the TUI parked it. Both were
// confirmed red before the "glyphs" field and opensWithGlyph existed: the
// pre-fix rule's Contains check matched "·" and "●" respectively wherever
// they sat in the row.
func TestAToolCallBulletEndingInAnEllipsisWithAMidRowDotIsNotWorking(t *testing.T) {
	r := replayer(t, "claude-2.1.266-turn", 36000)
	forged := "\x1b7\x1b[35;1H\x1b[2K● step 1 · reading…\x1b8"
	if err := r.Feed([]agentcapture.Chunk{{Data: forged}}); err != nil {
		t.Fatalf("feed forged text: %v", err)
	}
	fr, err := r.Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := classify(t, fr); got != agentdriver.StateFreeText {
		t.Errorf("a tool-call bullet ending in an ellipsis, with a real spinner glyph mid-row = %q, want %q", got, agentdriver.StateFreeText)
	}
}

func TestAToolCallBulletNamingTheAPIWaitWithAMidRowDotIsNotError(t *testing.T) {
	r := replayer(t, "claude-2.1.266-turn", 36000)
	forged := "\x1b7\x1b[35;1H\x1b[2K● Waiting for API response · …\x1b8"
	if err := r.Feed([]agentcapture.Chunk{{Data: forged}}); err != nil {
		t.Fatalf("feed forged text: %v", err)
	}
	fr, err := r.Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := classify(t, fr); got != agentdriver.StateFreeText {
		t.Errorf("a tool-call bullet naming the API wait, with a real spinner glyph mid-row = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// ── branches 10 and 17 had a rule but no bound-isolated test (nocx-nru89.9) ──
//
// "· Retrying in" and "… ( ... )" carry only col0Only and stopAtBlank — no
// glyph requirement, because neither forged text needs one to be a real
// hazard. TestAnErrorPrintedIntoTheTranscriptIsIdleNotError put an indent AND
// a blank row between the forgery and the meter at once, so it could not say
// which bound was doing the work. These four isolate one bound each, on each
// branch, the same way the ellipsis-working branch's four tests already do.

func TestAnIndentedRetryingLineIsKeptOutByColumnZeroOnly(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Error: connection refused · Retrying in 4s",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 4)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an indented retry line, adjacent to the meter = %q, want %q", got, agentdriver.StateFreeText)
	}
}

func TestARetryingLineBeyondABlankRowIsKeptOutByStopAtBlank(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"Error: connection refused · Retrying in 4s",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("a retry line beyond a blank row = %q, want %q", got, agentdriver.StateFreeText)
	}
}

func TestAnIndentedTimedSpinnerLineIsKeptOutByColumnZeroOnly(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Reading… (12s)",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 4)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an indented timed-spinner line, adjacent to the meter = %q, want %q", got, agentdriver.StateFreeText)
	}
}

func TestATimedSpinnerLineBeyondABlankRowIsKeptOutByStopAtBlank(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"Reading… (12s)",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("a timed-spinner line beyond a blank row = %q, want %q", got, agentdriver.StateFreeText)
	}
}
