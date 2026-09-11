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

// The same words the agent printed into its transcript are content, not the
// TUI's own error: indented, above a blank row, with the box live (nocx-emors).
func TestAnAPIWaitPrintedIntoTheTranscriptIsIdleNotError(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Waiting for API response · will retry in 4s",
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
		t.Fatalf("an API wait in the transcript was read as chrome: %q, want %q", got, agentdriver.StateFreeText)
	}
}

// The ellipsis branch reads the STATUS STACK, not the transcript (nocx-ys9jd).
// An agent that printed "Loading…" into its own output, indented and above a
// blank row, with the box live beneath it, is idle.
func TestAnEllipsisTheAgentPrintedIsNotATurnStarting(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Loading…",
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
		t.Fatalf("an ellipsis in the transcript was read as a turn: %q, want %q", got, agentdriver.StateFreeText)
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
