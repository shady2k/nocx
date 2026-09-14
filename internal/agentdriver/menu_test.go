package agentdriver_test

// Observation.Menu(), the third thing a rule can read off a screen beside its
// scalar state and its extras: the question, its options as drawn, and which
// one is selected — spec §6.3 of .internal/specs/2026-09-14-the-session-
// surface-design.md.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// TestAMenuIsReadOffTheScreenItWasDrawnOn replays the real bash-permission
// dialog (testdata/captures/manifest.json's own "bash-permission" moment) and
// asks Menu() the same question TestTheManifestHolds asks by field, so a
// failure here points at the projection rather than at one manifest number.
func TestAMenuIsReadOffTheScreenItWasDrawnOn(t *testing.T) {
	reg := registry(t)
	f := replay(t, "claude-2.1.266-permission", 49000)
	o := reg.Observe("claude", f)
	menu, ok := o.Menu()
	if !ok {
		t.Fatalf("Menu() found none on a real permission dialog; state was %q", o.State)
	}
	if !strings.Contains(menu.Question, "Do you want to") {
		t.Errorf("question = %q, want it to contain %q", menu.Question, "Do you want to")
	}
	if len(menu.Options) == 0 {
		t.Errorf("options = %v, want at least one", menu.Options)
	}
	if menu.Selected != 0 {
		t.Errorf("selected = %d, want 0 (the first option, \"Yes\")", menu.Selected)
	}
}

// TestInputBoxAndMenuZoneCoverWhatTheyName asserts the acceptance criterion
// the manifest cannot: InputBox and MenuZone are non-empty at an idle
// free-text moment (nothing about a menu is true there, so there is no
// Menu().Rows to compare against), and at every one of the five recorded
// menu moments MenuZone contains the menu's own Rows — the property that
// makes MenuZone usable at all, since a menu drawn outside the zone a
// coordinator watches for one would never be seen.
func TestInputBoxAndMenuZoneCoverWhatTheyName(t *testing.T) {
	reg := registry(t)

	freeText := reg.Observe("claude", replay(t, "claude-2.1.266-turn", 36000))
	if freeText.State != agentdriver.StateFreeText {
		t.Fatalf("claude-2.1.266-turn@36000 = %q, want free_text", freeText.State)
	}
	if freeText.InputBox.Last < freeText.InputBox.First {
		t.Errorf("InputBox is empty at an idle input box: %+v", freeText.InputBox)
	}
	if freeText.MenuZone.Last < freeText.MenuZone.First {
		t.Errorf("MenuZone is empty at an idle input box: %+v", freeText.MenuZone)
	}

	menuMoments := []struct {
		capture string
		atMs    int64
	}{
		{"claude-2.1.266-permission", 49000},  // bash-permission
		{"claude-2.1.266-permission", 118000}, // write-permission
		{"claude-2.1.266-turn", 20500},        // folder-trust
		{"claude-2.1.266-turn", 8000},         // theme-picker
		{"claude-2.1.266-model", 49000},       // model-menu
	}
	for _, m := range menuMoments {
		o := reg.Observe("claude", replay(t, m.capture, m.atMs))
		menu, ok := o.Menu()
		if !ok {
			t.Errorf("%s@%dms: Menu() found none", m.capture, m.atMs)
			continue
		}
		if o.MenuZone.First > menu.Rows.First || menu.Rows.Last > o.MenuZone.Last {
			t.Errorf("%s@%dms: menuZone %+v does not contain menu rows %+v", m.capture, m.atMs, o.MenuZone, menu.Rows)
		}
	}
}

// TestTheMenuZoneAtFreeTextAndWorkingCoversEveryLaterMenuInTheCapture is
// nocx-6q1uh.17's own acceptance criterion: session.message's Enter
// precondition (design §8.2) digests the "working" target's MenuZone at a
// free_text or working moment, before any menu is on screen, and relies on a
// menu APPEARING inside that zone to change the digest and refuse the Enter.
// A menu that draws even one row outside the zone can appear invisibly to
// that precondition.
//
// TestInputBoxAndMenuZoneCoverWhatTheyName already checks MenuZone against
// Menu().Rows at the SAME moment a menu is on screen — necessary, but not the
// property design §8.2 needs, which is about a zone read BEFORE the menu
// exists. This test replays the two real pairs in testdata/captures where a
// free_text or working moment is later followed, in the SAME capture, by a
// permission_choice or modal_choice moment: claude-2.1.266-permission (a
// working moment at 46s, then bash-permission at 49s and write-permission at
// 118s) and claude-lmstudio-permission (working at 48s and free_text at
// 101s, each followed by one or more of the same three dialogs). The other
// captures in the manifest that carry a menu moment (claude-2.1.266-turn's
// theme-picker and folder-trust, claude-2.1.266-model's model-menu, and the
// standalone claude-modal/claude-permission/claude-trust captures) draw their
// menu BEFORE the first free_text or working moment in the same recording,
// so there is no earlier moment for this property to constrain there.
//
// panelFirst is measured by replay (agent-capture replay -at <ms> <capture>,
// quoted in the commit body), not read from Observation: the panel's own
// header row ("Bash command" / "Create file", right below the transcript's
// closing rule) draws well above Menu().Rows, which starts at the question,
// and design §6.3 asks the zone to cover "question, options and panel" —
// checking containment of Rows alone would miss a zone that grew just enough
// to cover the question without reaching the panel above it.
func TestTheMenuZoneAtFreeTextAndWorkingCoversEveryLaterMenuInTheCapture(t *testing.T) {
	reg := registry(t)

	type laterMenu struct {
		atMs       int64
		panelFirst int // measured: the dialog's own panel header row
	}
	pairs := []struct {
		name         string
		capture      string
		earlierAtMs  int64
		earlierState agentdriver.State
		laters       []laterMenu
	}{
		{
			name:         "working before bash-permission and write-permission",
			capture:      "claude-2.1.266-permission",
			earlierAtMs:  46000,
			earlierState: agentdriver.StateWorking,
			laters: []laterMenu{
				{atMs: 49000, panelFirst: 13},  // "Bash command" header; question at row 21
				{atMs: 118000, panelFirst: 20}, // "Create file" header; question at row 25
			},
		},
		{
			name:         "working before all three later dialogs",
			capture:      "claude-lmstudio-permission",
			earlierAtMs:  48000,
			earlierState: agentdriver.StateWorking,
			laters: []laterMenu{
				{atMs: 49000, panelFirst: 13},  // "Bash command" header; question at row 21
				{atMs: 60000, panelFirst: 12},  // "Bash command" header; question at row 20
				{atMs: 110000, panelFirst: 20}, // "Create file" header; question at row 25
			},
		},
		{
			name:         "free_text before write-permission only",
			capture:      "claude-lmstudio-permission",
			earlierAtMs:  101000,
			earlierState: agentdriver.StateFreeText,
			// The 49000ms and 60000ms dialogs precede this free_text moment
			// (it sits between the interrupted bash turn and the write
			// turn), so only the LATER one, write-permission at 110000ms,
			// is a moment this zone needs to reach.
			laters: []laterMenu{
				{atMs: 110000, panelFirst: 20},
			},
		},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			earlier := reg.Observe("claude", replay(t, p.capture, p.earlierAtMs))
			if earlier.State != p.earlierState {
				t.Fatalf("%s@%dms state = %q, want %q", p.capture, p.earlierAtMs, earlier.State, p.earlierState)
			}
			zone := earlier.MenuZone
			if zone.Last < zone.First {
				t.Fatalf("%s@%dms: MenuZone is empty: %+v", p.capture, p.earlierAtMs, zone)
			}
			for _, l := range p.laters {
				laterObs := reg.Observe("claude", replay(t, p.capture, l.atMs))
				menu, ok := laterObs.Menu()
				if !ok {
					t.Errorf("%s@%dms: Menu() found none", p.capture, l.atMs)
					continue
				}
				if zone.First > l.panelFirst {
					t.Errorf("%s@%dms zone %+v (read at %dms) does not reach the panel's own header at row %d",
						p.capture, l.atMs, zone, p.earlierAtMs, l.panelFirst)
				}
				if zone.First > menu.Rows.First || menu.Rows.Last > zone.Last {
					t.Errorf("%s@%dms zone %+v (read at %dms) does not contain menu rows %+v",
						p.capture, l.atMs, zone, p.earlierAtMs, menu.Rows)
				}
			}
		})
	}
}

// TestAMenuWithoutAQuestionIsNoMenu is the paired failure the acceptance
// criterion names: a cursor row that IS an option, with only more options (or
// the frame's own top) above it. There is no question to type an answer past,
// so no menu target is possible (design §6.3) even though the state itself
// still reads as a menu state — the two are different questions, and the
// engine must not conflate "this pane wants a choice" with "a menu extracted
// cleanly".
//
// The frame is hand-built rather than replayed: no capture in the corpus
// exhausts the option walk without finding a question, because every real
// screen we recorded is scrolled from somewhere with prose above it.
func TestAMenuWithoutAQuestionIsNoMenu(t *testing.T) {
	// Row 0 is an unselected numbered option, row 1 is the cursor's own row
	// (selected), and row 2 is blank padding so "belowCursor" — the anchor
	// the "menu" extractor reads up from — has a row to bind at. Walking up
	// from the cursor consumes row 0 as another option and then runs off the
	// top of the frame: no row anywhere above the cursor is a non-option row,
	// so the walk can never find a question.
	lines := []string{"   2. No", " ❯ 1. Yes", ""}
	f := screen(t, 20, len(lines), lines, 1, 1)

	reg := registry(t)
	o := reg.Observe("claude", f)
	if o.State != agentdriver.StatePermissionChoice && o.State != agentdriver.StateModalChoice {
		t.Fatalf("state = %q, want permission_choice or modal_choice", o.State)
	}
	if _, ok := o.Menu(); ok {
		t.Fatalf("Menu() found one, want none: nothing above the cursor's own option is a question")
	}
}

// TestACursorAnchorIsRefusedInAPredicate is the safety property the "cursor"
// anchor kind exists under: a person composes WHERE a predicate looks and may
// not lift the bound that makes the cursor unforgeable. Only an extractor (or
// a document-level region — Document.InputBox, Document.MenuZone) may name a
// cursor anchor; a branch's own predicate may not, because a predicate is what
// DECIDES the state and the cursor's forgeability argument is what the whole
// closed set rests on.
func TestACursorAnchorIsRefusedInAPredicate(t *testing.T) {
	refused := agentdriver.Document{
		Agent:   "test-cursor-refused",
		Default: agentdriver.StateUnknown,
		Anchors: []agentdriver.AnchorSpec{{Name: "cursor", Kind: "cursor"}},
		Branches: []agentdriver.Branch{{
			State: agentdriver.StateFreeText,
			When:  []agentdriver.Pred{{Kind: "anchorBound", RegionSpec: agentdriver.RegionSpec{Anchor: "cursor"}}},
		}},
	}
	raw, err := json.Marshal(refused)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, compileErr := agentdriver.Compile(raw); compileErr == nil {
		t.Fatal("a branch predicate naming a cursor anchor was accepted")
	} else if !strings.Contains(compileErr.Error(), "cursor") {
		t.Errorf("error %v does not name the anchor as a cursor anchor", compileErr)
	}

	// The contrast that makes the refusal evidence rather than a bug: the
	// same anchor, read by an EXTRACTOR instead of a predicate, compiles.
	compiles := agentdriver.Document{
		Agent:   "test-cursor-extractor",
		Default: agentdriver.StateUnknown,
		Anchors: []agentdriver.AnchorSpec{{Name: "cursor", Kind: "cursor"}},
		Extractors: []agentdriver.Extractor{{
			Name:       "atCursor",
			Pattern:    `^(?P<text>.*)$`,
			RegionSpec: agentdriver.RegionSpec{Anchor: "cursor", MaxRows: 1},
		}},
	}
	raw2, err := json.Marshal(compiles)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := agentdriver.Compile(raw2); err != nil {
		t.Fatalf("an extractor naming a cursor anchor was refused: %v", err)
	}
}
