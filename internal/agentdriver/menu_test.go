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
