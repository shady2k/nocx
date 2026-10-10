package content_test

// The resend's counted loss (nocx-zg3k3.5.3): a gap the coordinator's own
// absence caused rides the same index accounting as every loss, but the
// block says WHICH cause took it. The count is the store's own field —
// distinct from what the emulator's struck feeds lost and from what the
// cap dropped — because "rows are missing" with no why sends a reader to
// raise a limit that took nothing.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

// The resend's counted loss (nocx-zg3k3.5.3): a gap the coordinator's own
// absence caused rides the same index accounting as every loss, but the
// block says WHICH cause took it. The count is the store's own field —
// distinct from what the emulator's struck feeds lost and from what the
// cap dropped — because "rows are missing" with no why sends a reader to
// raise a limit that took nothing.
func TestAppendBlockRows_AnUnavailableGapIsCountedWithItsCause(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "make")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("one"), aTextRow("two")},
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// The resend's gap: rows [2, 4) the scrollback pruned while the
	// coordinator was away, stated at the position the survivors start at.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 4, LostRows: 2,
		LostCause: content.LostCauseCoordinatorUnavailable,
		Rows:      []emulator.Row{aTextRow("three"), aTextRow("four")},
	}); err != nil {
		t.Fatalf("the unavailable gap: %v", err)
	}
	// An ordinary struck-feed tail, no cause: it counts as lost, never as
	// the absence's own.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 7, LostRows: 1,
	}); err != nil {
		t.Fatalf("the struck tail: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.UnavailableRows != 2 {
		t.Fatalf("summary.UnavailableRows = %d, want 2: the absence's own count, distinct from the feeds'", summary.UnavailableRows)
	}
	if summary.LostRows != 3 {
		t.Fatalf("summary.LostRows = %d, want 3: both gaps spend their indices", summary.LostRows)
	}
}

// The paired ordinary case: a gap with no cause is the emulator's own, and
// the absence count stays zero.
func TestAppendBlockRows_AGapWithoutACauseIsNotTheAbsence(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "make")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 3, LostRows: 3,
		Rows: []emulator.Row{aTextRow("after the hole")},
	}); err != nil {
		t.Fatalf("the gap: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.UnavailableRows != 0 {
		t.Fatalf("summary.UnavailableRows = %d, want 0: no cause named the absence", summary.UnavailableRows)
	}
	if summary.LostRows != 3 {
		t.Fatalf("summary.LostRows = %d, want 3", summary.LostRows)
	}
}
