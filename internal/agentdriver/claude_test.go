package agentdriver_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

func classify(t *testing.T, f panegrid.Frame) agentdriver.State {
	t.Helper()
	return agentdriver.Claude().Classify(f)
}

// ── the five states, each off the capture that holds it ──────────────────

// The ordinary case, and the one every refusal below is paired with: an idle
// input box is where nocx is allowed to type.
func TestTheIdleInputBoxAcceptsFreeText(t *testing.T) {
	if got := classify(t, replay(t, "claude-idle", 11000)); got != agentdriver.StateFreeText {
		t.Errorf("idle input box = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// A turn in flight. The spinner is the only chrome that says so — this
// version prints no "esc to interrupt" anywhere in the capture.
func TestATurnInFlightIsWorking(t *testing.T) {
	if got := classify(t, replay(t, "claude-working", 17000)); got != agentdriver.StateWorking {
		t.Errorf("spinner up = %q, want %q", got, agentdriver.StateWorking)
	}
}

// And the same capture after the turn ends. The input box never went away, so
// this is what separates "the box is on screen" from "the box is waiting".
func TestTheSameTurnFinishedIsFreeTextAgain(t *testing.T) {
	if got := classify(t, replay(t, "claude-working", 44000)); got != agentdriver.StateFreeText {
		t.Errorf("turn finished = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// The tool-approval dialog. It REPLACES the input box rather than overlaying
// it, and its selected row reads "❯ 1. Yes" — the same glyph the input marker
// uses. Getting this wrong is not a mislabelled indicator, it is a keystroke
// that approves a tool call the user never saw.
func TestTheToolApprovalDialogIsAPermissionChoice(t *testing.T) {
	if got := classify(t, replay(t, "claude-permission", 49000)); got != agentdriver.StatePermissionChoice {
		t.Errorf("Write approval dialog = %q, want %q", got, agentdriver.StatePermissionChoice)
	}
}

// A menu the USER opened (/model). Same shape, and not a tool approval — the
// difference decides whether answering it is answering the agent.
func TestAUserOpenedMenuIsAModalChoice(t *testing.T) {
	if got := classify(t, replay(t, "claude-modal", 20000)); got != agentdriver.StateModalChoice {
		t.Errorf("/model menu = %q, want %q", got, agentdriver.StateModalChoice)
	}
}

// ── the subagent trap, which has TWO chrome forms and an end ──────────────

// While the task panel is drawn under the footer. There is no spinner here at
// all: input box live, footer present, and the only thing that says work is
// happening is the panel below the mode line.
func TestABackgroundAgentReportedByItsPanelIsNotFreeText(t *testing.T) {
	if got := classify(t, replay(t, "claude-subagent", 30000)); got == agentdriver.StateFreeText {
		t.Errorf("blocked on a background agent (task panel) = %q, want anything but %q",
			got, agentdriver.StateFreeText)
	}
}

// Later in the same capture the panel is gone and the mode line carries
// "· /tasks to see subagents ·" instead. Still no spinner.
func TestABackgroundAgentReportedByTheModeLineIsNotFreeText(t *testing.T) {
	if got := classify(t, replay(t, "claude-subagent", 40000)); got == agentdriver.StateFreeText {
		t.Errorf("blocked on a background agent (mode line) = %q, want anything but %q",
			got, agentdriver.StateFreeText)
	}
}

// The interval's second end, and the reason the mode-line segment is evidence
// rather than decoration: at 70s the background agent has finished, the
// segment is gone, and the pane is free text again. A rule with no end would
// leave this pane refusing input for the rest of the session.
func TestWhenTheBackgroundAgentFinishesThePaneAcceptsInputAgain(t *testing.T) {
	if got := classify(t, replay(t, "claude-subagent", 70000)); got != agentdriver.StateFreeText {
		t.Errorf("background agent finished = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// ── anchored in chrome, never in what the agent printed ───────────────────

// The failure this repository already measured once, as a completion sentinel
// that matched itself because the agent printed the brief it had just read.
// Here the agent prints an input marker, a menu row, a cancel line and both a
// live-looking and a finished-looking spinner into its own transcript, and the
// verdict may not move: all of them are content, and every anchor is a
// position.
func TestTextTheAgentPrintedCannotForgeAnyVerdict(t *testing.T) {
	store, pane := replayStore(t, "claude-idle", 11000)
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
	store.Feed(pane, []byte(forged))
	fr, err := store.Frame(pane)
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
	blank := replay(t, "claude-idle", 0)
	if got := classify(t, blank); got != agentdriver.StateUnknown {
		t.Errorf("screen before the TUI has drawn = %q, want %q", got, agentdriver.StateUnknown)
	}
}

// Every frame of every capture answers from the closed set, and never answers
// "exited" — that one is a fact about the process and is deliberately not
// taken from the screen.
func TestEveryFrameOfEveryCaptureAnswersFromTheClosedSet(t *testing.T) {
	captures := []string{
		"claude-idle", "claude-working", "claude-permission",
		"claude-modal", "claude-subagent",
	}
	for _, name := range captures {
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
