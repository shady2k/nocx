package agentdriver_test

// The measurement nocx-6q1uh.10 made before touching session.message: does a
// Claude permission/modal menu moment always DISPLACE the input box, relative
// to the nearest preceding free_text/working moment in the SAME capture?
//
// Measured with `go run ./cmd/agent-capture replay -at <ms,...> <capture>`
// and a throwaway program over agentdriver.Registry.Observe (deleted before
// commit), then reduced to the assertions below. Every pair with a real
// preceding baseline in the same capture went from InputBox bound (e.g. rows
// 35-38) to fully unbound ({0,-1}) at the menu moment — never merely shifted
// while staying bound — with zero counterexamples across nine such pairs
// spanning both permission_choice and modal_choice, both the 2.1.266 and
// lmstudio recordings, and three distinct captures (claude-2.1.266-
// permission, claude-lmstudio-permission, claude-modal). The three captures
// with NO preceding free_text/working moment (theme-picker, folder-trust,
// claude-trust) are onboarding dialogs shown before the input box was ever
// drawn in that pane; they are not counterexamples, they have nothing to
// displace.
//
// This is what backs claude.rule.json's "menuDisplacesInputBox": true and
// Task 10's choice to bind session.message's paste and Enter targets to
// TargetInput rather than TargetWorking for Claude (internal/app/
// pane_messages.go): a menu appearing is caught because InputBox itself goes
// empty, so a target minted from it stops matching the instant a menu is
// drawn — no need for the wider menu-zone target design §8.2 names.

import (
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

type displacementPair struct {
	capture string
	freeMs  int64
	menuMs  int64
}

// displacementPairs is every corpus pair that has a real preceding free_text
// or working moment in the SAME capture, drawn from the measurement above.
var displacementPairs = []displacementPair{
	{"claude-2.1.266-permission", 46000, 49000},  // working -> bash-permission
	{"claude-2.1.266-permission", 46000, 118000}, // working -> write-permission
	{"claude-lmstudio-permission", 48000, 49000},
	{"claude-lmstudio-permission", 48000, 60000},
	{"claude-lmstudio-permission", 101000, 110000}, // free_text -> permission
	{"claude-modal", 14000, 20000},                 // free_text -> modal
	{"claude-permission", 18000, 49000},            // working -> permission
	{"claude-permission-60", 18000, 49000},
	{"claude-2.1.266-model", 44000, 48000}, // free_text -> modal (2nd model menu)
	{"claude-lmstudio-model", 44000, 48000},
}

// TestAMenuAlwaysDisplacesTheInputBox asserts the measurement above as a
// replayed fact, not a paraphrase of it: for every pair, the preceding
// moment's InputBox is bound (Last >= First) and the menu moment's InputBox
// is fully unbound — never merely relocated to different rows while staying
// bound, which the RowSpan equality on its own would not distinguish from
// "moved".
func TestAMenuAlwaysDisplacesTheInputBox(t *testing.T) {
	reg := registry(t)
	for _, p := range displacementPairs {
		freeFrame := replay(t, p.capture, p.freeMs)
		menuFrame := replay(t, p.capture, p.menuMs)
		free := reg.Observe("claude", freeFrame)
		menu := reg.Observe("claude", menuFrame)
		if free.InputBox.Last < free.InputBox.First {
			t.Errorf("%s@%dms: preceding InputBox = %+v, want it bound (this pair's own baseline)", p.capture, p.freeMs, free.InputBox)
			continue
		}
		if menu.State != agentdriver.StatePermissionChoice && menu.State != agentdriver.StateModalChoice {
			t.Errorf("%s@%dms: state = %q, want a menu state", p.capture, p.menuMs, menu.State)
			continue
		}
		if menu.InputBox.Last >= menu.InputBox.First {
			t.Errorf("%s@%dms: menu InputBox = %+v, want fully unbound (displaced) — the whole property this test guards", p.capture, p.menuMs, menu.InputBox)
		}
	}
}

// TestClaudeDeclaresMenuDisplacesInputBox is the declared, tested fact
// itself: the registry's per-agent accessor answers true for claude (set in
// claude.rule.json on the strength of the measurement above), and false —
// fail closed, fall back to the menu zone — for an agent with no driver at
// all.
func TestClaudeDeclaresMenuDisplacesInputBox(t *testing.T) {
	reg := registry(t)
	if !reg.MenuDisplacesInputBox("claude") {
		t.Fatal(`MenuDisplacesInputBox("claude") = false, want true`)
	}
	if reg.MenuDisplacesInputBox("no-such-agent") {
		t.Fatal(`MenuDisplacesInputBox("no-such-agent") = true, want false (fail closed)`)
	}
}
