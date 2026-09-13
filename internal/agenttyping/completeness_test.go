package agenttyping_test

// The completeness gate (nocx-ygxjv.3, design §6.7): a write is decided on a
// frame the runtime can VOUCH for, and every other claim is refused with a
// reason.
//
// Why it is a gate on the WRITE and not on the reading: an indicator drawn from
// a screen with a hole is a hint that may be wrong; a keystroke sent on such a
// screen is an answer to a question nobody can prove is on it. The hole is
// exactly the case where the cells stop describing the program's state — the
// helper's bounded window reclaimed the bytes between them — so the two
// consumers of one frame answer to different standards.

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestAWriteIsRefusedWhenTheRuntimeCannotVouchForTheScreen walks every claim
// that is not Complete, each beside the SAME pane and rule succeeding at
// Complete. The refusal case without its pair proves nothing: a pane that
// refused everything would pass it.
func TestAWriteIsRefusedWhenTheRuntimeCannotVouchForTheScreen(t *testing.T) {
	for _, tc := range []struct {
		completeness sessionruntime.Completeness
		says         string
	}{
		{sessionruntime.CompletenessUnknown, "cannot say whether it saw the whole stream"},
		{sessionruntime.CompletenessLostIngest, "output was lost before the screen was built"},
		{sessionruntime.CompletenessNoFence, "no authenticated boundary"},
		{sessionruntime.CompletenessEvicted, "retention deliberately kept less"},
	} {
		t.Run(tc.says, func(t *testing.T) {
			frame := idleFrame(t)
			frame.Completeness = tc.completeness
			ty, _, q := typistOn(t, frame, verifiedFor(t))

			got := ty.Type(context.Background(), pane, "wake up")
			if got.Outcome != agenttyping.OutcomeRefused {
				t.Fatalf("outcome = %q, want a refusal: a frame the runtime cannot vouch for may not be typed into", got.Outcome)
			}
			if !strings.Contains(got.Reason, tc.says) {
				t.Errorf("reason = %q, want it to name the claim (%q)", got.Reason, tc.says)
			}
			if wrote := q.jobs; len(wrote) != 0 {
				t.Errorf("bytes reached the pane's queue (%d), want none", len(wrote))
			}

			// THE PAIR: the same pane, the same rule, a frame the runtime can
			// vouch for — and the write goes through.
			ok := idleFrame(t)
			ok.Completeness = sessionruntime.CompletenessComplete
			ty2, _, q2 := typistOn(t, ok, verifiedFor(t))
			got2 := ty2.Type(context.Background(), pane, "wake up")
			if got2.Outcome != agenttyping.OutcomeTyped {
				t.Fatalf("the same write on a vouched frame = %q (%s), want it typed",
					got2.Outcome, got2.Reason)
			}
			if len(q2.jobs) == 0 {
				t.Error("a typed write put nothing in the pane's queue")
			}
		})
	}
}

// TestTheMenuPathIsGatedByTheSameClaim: answering a menu is a write, so the
// permission is decided from a frame the runtime can vouch for as well. One
// gate for both would be a second place the rule lived; this asserts the two
// paths answer alike rather than that they share a function.
func TestTheMenuPathIsGatedByTheSameClaim(t *testing.T) {
	// A menu the rule identifies, with a runtime that cannot vouch for it.
	menu := replay(t, "claude-permission", 49000)
	menu.Completeness = sessionruntime.CompletenessNoFence
	ty, _, q := typistOn(t, menu, verifiedFor(t))

	got := ty.Choose(context.Background(), pane, "Yes")
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want a refusal", got.Outcome)
	}
	if !strings.Contains(got.Reason, "no authenticated boundary") {
		t.Errorf("reason = %q, want it to name the claim", got.Reason)
	}
	if len(q.jobs) != 0 {
		t.Error("a keystroke reached a menu's queue from a frame nobody can vouch for")
	}
}

// idleFrame is the corpus's own idle screen — a pane positively identified as
// free_text — so the only thing that can refuse below is the completeness
// claim and not the pane's state.
func idleFrame(t *testing.T) paneview.Frame {
	t.Helper()
	return replay(t, "claude-idle", 11000)
}
