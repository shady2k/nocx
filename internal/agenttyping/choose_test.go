package agenttyping_test

// A MENU IS ANSWERED BY NAME, WITH ITS OWN KEYS, AND NOTHING ELSE (ADR-0064,
// nocx-f545a.4).
//
// Off the real folder-trust capture. The assertions are on the BYTES that
// reached the pane's input queue, because that is the whole of what this
// package decides: exactly the movement keys the distance needs, the confirm
// key only on a frame that shows the selection on the named option, and
// nothing at all for an option the menu does not offer, a pane that is not a
// menu, a rule without authority, or a screen that stopped being the menu
// between the decision and the key.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
)

const (
	optNo  = "No, exit"
	optYes = "Yes, I trust this folder"
	down   = "\x1b[B"
	enter  = "\r"
	// selectYes is the TUI's own repaint when the selection moves down one
	// row: the marker leaves "No, exit" (row 13) and lands on the Yes row
	// (row 14), and the cursor is parked on it. 1-based in the escape.
	selectYes = "\x1b[14;2H \x1b[15;2H❯\x1b[15;2H"
)

// trustFrame is the real capture at its mark, with extra bytes painted after —
// through a real grid, because a frame assembled any other way is a frame the
// product never produces.
func trustFrame(t *testing.T, extra string) paneview.Frame {
	t.Helper()
	header, chunks, err := agentcapture.Read(filepath.Join("..", "agentdriver", "testdata", "captures", "claude-trust.jsonl"))
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	store := paneviewtest.NewViews(log.NewSlogAdapter(nil))
	const id = "trust"
	if enrolErr := store.Watch(id, header.Cols, header.Rows); enrolErr != nil {
		t.Fatalf("enrol: %v", enrolErr)
	}
	t.Cleanup(func() { store.Withdraw(id) })
	for _, c := range chunks[:agentcapture.ChunksThrough(chunks, 11000, 0)] {
		store.Feed(id, []byte(c.Data))
	}
	if extra != "" {
		store.Feed(id, []byte(extra))
	}
	f, err := store.Frame(id)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return f
}

func TestReadMenuReadsTheTrustQuestionsOptionsAsDrawn(t *testing.T) {
	m := agenttyping.ReadMenu(trustFrame(t, ""))
	if len(m.Options) != 2 || m.Options[0] != optNo || m.Options[1] != optYes {
		t.Fatalf("options = %q, want [%q %q]", m.Options, optNo, optYes)
	}
	if m.Selected != 0 {
		t.Fatalf("selected = %d, want 0: the capture parks the selection on %q", m.Selected, optNo)
	}
	if moved := agenttyping.ReadMenu(trustFrame(t, selectYes)); moved.Selected != 1 {
		t.Fatalf("after the repaint selected = %d, want 1", moved.Selected)
	}
}

// Moving is one key per row and NO confirm: the frame the call read cannot yet
// show the move, and confirming on the belief that it landed is the mistake.
func TestChoosingAnotherOptionMovesTheSelectionAndConfirmsNothing(t *testing.T) {
	ty, _, q := typistOn(t, trustFrame(t, ""), verifiedFor(t))
	got := ty.Choose(context.Background(), pane, optYes)
	if got.Outcome != agenttyping.OutcomeTyped {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Reason, agenttyping.OutcomeTyped)
	}
	if q.all() != down {
		t.Fatalf("bytes = %q, want exactly one down key and no confirm", q.all())
	}
}

// Once the screen shows the selection on the option, choosing it again is the
// confirm key and only that.
func TestChoosingTheSelectedOptionConfirmsIt(t *testing.T) {
	ty, _, q := typistOn(t, trustFrame(t, selectYes), verifiedFor(t))
	got := ty.Choose(context.Background(), pane, optYes)
	if got.Outcome != agenttyping.OutcomeSubmitted {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Reason, agenttyping.OutcomeSubmitted)
	}
	if q.all() != enter {
		t.Fatalf("bytes = %q, want exactly the confirm key", q.all())
	}
}

// The numbering is how a menu labels an option, not what it says: "1. Yes" is
// chosen as "Yes". The permission dialog's selection starts on its first
// option, so choosing it is the confirm key alone.
func TestANumberedMenusOptionIsNamedWithoutItsNumber(t *testing.T) {
	f := replay(t, "claude-permission", 49000)
	m := agenttyping.ReadMenu(f)
	if m.Selected < 0 || m.Selected >= len(m.Options) {
		t.Fatalf("menu = %+v, want the selection on one of its options", m)
	}
	selected := m.Options[m.Selected]
	if selected == "" || (selected[0] >= '0' && selected[0] <= '9') {
		t.Fatalf("option %q is empty or kept its numbering", selected)
	}
	ty, _, q := typistOn(t, f, verifiedFor(t))
	if got := ty.Choose(context.Background(), pane, selected); got.Outcome != agenttyping.OutcomeSubmitted || q.all() != enter {
		t.Fatalf("choosing the selected %q = %+v, bytes %q; want the confirm key alone", selected, got, q.all())
	}
}

func TestAnOptionTheMenuDoesNotOfferReceivesNothing(t *testing.T) {
	ty, _, q := typistOn(t, trustFrame(t, ""), verifiedFor(t))
	got := ty.Choose(context.Background(), pane, "Yes, and trust every folder forever")
	if got.Outcome != agenttyping.OutcomeRefused || got.Reason == "" {
		t.Fatalf("result = %+v, want a refusal with its reason", got)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("bytes reached the pane for an option not on screen: %q", q.all())
	}
}

// Choose is not a second door onto a free_text pane: an input box is not a
// menu, and naming an "option" there writes nothing.
func TestAPaneThatIsNotAMenuCannotBeAnswered(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	if got := ty.Choose(context.Background(), pane, optYes); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("result = %+v, want refused", got)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("bytes reached an input box through Choose: %q", q.all())
	}
}

func TestARuleWithoutAuthorityCannotAnswerAMenu(t *testing.T) {
	ty, _, q := typistOn(t, trustFrame(t, ""), unverified{})
	if got := ty.Choose(context.Background(), pane, optYes); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("result = %+v, want refused", got)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("bytes reached the pane without authority: %q", q.all())
	}
}

// The key is decided on its own frame. The call reads the menu, and by the
// time it would write the first key the screen is an input box: nothing is
// written, because a movement key landing in an input box is text nobody sent.
func TestAMenuThatVanishesBeforeTheKeyReceivesNothing(t *testing.T) {
	ty, sc, q := typistOn(t, trustFrame(t, ""), verifiedFor(t))
	sc.changeAfter(1, replay(t, "claude-idle", 11000))
	if got := ty.Choose(context.Background(), pane, optYes); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("result = %+v, want refused", got)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("a key was written after the menu was gone: %q", q.all())
	}
}

func TestChooseNeedsAnOption(t *testing.T) {
	ty, _, q := typistOn(t, trustFrame(t, ""), verifiedFor(t))
	if got := ty.Choose(context.Background(), pane, "   "); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("result = %+v, want refused", got)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("bytes reached the pane for no option at all: %q", q.all())
	}
}
