package agentdriver_test

// Observation.InputText, and the defect it replaces (nocx-6q1uh.18).
//
// internal/app/pane_messages.go used to answer "is the box empty" and "did
// the paste echo" by trimming strings.TrimSpace over regionText's raw
// cell-join of the whole minted target span, which for claude is
// Document.InputBox — the box's two rule rows included, by design (a menu
// displacing the box must still make a target minted from it refuse). A row
// of nothing but the rule's own full-width "─" is never whitespace, so that
// trim was never empty on any real frame: session.message could neither
// paste into an idle box (refused "someone is typing") nor confirm its own
// submission. See claude.go's own note for the corpus evidence and the fix.
//
// This file asserts the RULE's own answer instead, replayed off the same
// corpus claude.go's note cites.

import (
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// TestInputTextIsEmptyAtAnIdleClaudePrompt is the failure path's own
// baseline: a real idle input box, where the OLD trim-the-whole-span check
// was never empty because the span's own closing rule row is not
// whitespace. The rule's own inputText reading must say "empty" here, or
// nothing this bead fixed can ever paste.
func TestInputTextIsEmptyAtAnIdleClaudePrompt(t *testing.T) {
	reg := registry(t)
	for _, m := range []struct {
		capture string
		atMs    int64
	}{
		{"claude-2.1.266-turn", 36000},    // idle-120, the corpus's own free_text baseline
		{"claude-2.1.266-idle-60", 38000}, // idle-60, the narrow geometry
		{"claude-2.1.266-idle-80", 38000}, // idle-80
		{"claude-2.1.266-turn", 47500},    // working: the box the turn just cleared
	} {
		f := replay(t, m.capture, m.atMs)
		o := reg.Observe("claude", f)
		text, ok := o.InputText()
		if !ok {
			t.Errorf("%s@%dms (state=%s): InputText ok=false, want true (the rule reads this box)", m.capture, m.atMs, o.State)
			continue
		}
		if text != "" {
			t.Errorf("%s@%dms (state=%s): InputText = %q, want \"\" — the OLD trim-the-whole-span check answered non-empty here on every real frame, because the span's own closing rule row is not whitespace", m.capture, m.atMs, o.State, text)
		}
	}
}

// TestInputTextReadsWhatWasTyped is the success path a caller pastes on: the
// text a person (or session.message) actually typed, read off the box's own
// content rows with the prompt marker and any wrap indent stripped —
// including the two-row wrap the corpus actually shows, which is why the
// extractor reads more than the prompt row alone.
func TestInputTextReadsWhatWasTyped(t *testing.T) {
	reg := registry(t)
	cases := []struct {
		name    string
		capture string
		atMs    int64
		want    string
	}{
		{
			// Single content row: claude-2.1.266-turn at 47s, replayed with
			// `go run ./cmd/agent-capture replay -at 47000` while
			// investigating this bead — the box shows exactly
			// "❯ Write a 400 word story about a lighthouse keeper."
			// (a NO-BREAK SPACE after the marker, not the ordinary space it
			// prints as; see claude.go's own note).
			name:    "single line",
			capture: "claude-2.1.266-turn",
			atMs:    47000,
			want:    "Write a 400 word story about a lighthouse keeper.",
		},
		{
			// Two content rows: a typed line long enough to wrap at the
			// pane's own width. The continuation row's own two-cell indent
			// (ordinary spaces, not the marker's NO-BREAK SPACE) is what the
			// second half of the pattern strips.
			name:    "wrapped across two rows",
			capture: "claude-2.1.266-subagent-finished",
			atMs:    38000,
			want:    "Use the Agent tool to launch the Explore subagent with run_in_background true, asking it to list the files in this\nfolder. Do not wait for it.",
		},
		{
			// The same wrap at a narrower geometry (60 columns), so the join
			// is asserted on more than one recording.
			name:    "wrapped at 60 columns",
			capture: "claude-permission-60",
			atMs:    13000,
			want:    "Create a file named note.txt whose only content is the\nword hi",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := replay(t, c.capture, c.atMs)
			o := reg.Observe("claude", f)
			if o.State != agentdriver.StateFreeText {
				t.Fatalf("%s@%dms: state = %q, want free_text (the box this test reads must actually be idle-with-text)", c.capture, c.atMs, o.State)
			}
			text, ok := o.InputText()
			if !ok {
				t.Fatalf("%s@%dms: InputText ok=false, want true", c.capture, c.atMs)
			}
			if text != c.want {
				t.Errorf("%s@%dms: InputText = %q, want %q", c.capture, c.atMs, text, c.want)
			}
		})
	}
}

// TestInputTextIsUnreadableWhenAMenuDisplacesTheBox is the paired failure
// this projection must keep: a menu moment has no box to read at all
// (nocx-6q1uh.10, menuDisplacesInputBox), and InputText must say so with
// ok==false rather than text=="" — the two are different claims, and only
// "the rule read this box and it's empty" is a licence to paste.
func TestInputTextIsUnreadableWhenAMenuDisplacesTheBox(t *testing.T) {
	reg := registry(t)
	f := replay(t, "claude-2.1.266-permission", 49000) // bash-permission
	o := reg.Observe("claude", f)
	if o.State != agentdriver.StatePermissionChoice {
		t.Fatalf("state = %q, want permission_choice", o.State)
	}
	if text, ok := o.InputText(); ok {
		t.Fatalf("InputText = (%q, true), want ok=false — a menu has displaced the box, there is nothing to read", text)
	}
}
