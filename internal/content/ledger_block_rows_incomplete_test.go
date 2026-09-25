package content_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

// closeOneBlock opens a kept block for a fresh command, appends two rows and
// seals it with the close the caller hands in, and answers the artifact as a
// client's ledger.get reads it.
func closeOneBlock(t *testing.T, incomplete bool) *content.Artifact {
	t.Helper()
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "make")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("building"), aTextRow("linking")},
	}); err != nil {
		t.Fatalf("AppendBlockRows: %v", err)
	}
	if _, err := led.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, Incomplete: incomplete,
	}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	art := blockRowsArtifactOf(t, led, entryID)
	if art == nil {
		t.Fatal("no block rows artifact")
	}
	return art
}

// A block whose boundary never arrived whole — its completion lost on the way
// to the helper, or its fence never sighted (nocx-2v80t.3.29) — is sealed
// with truncated 'gap': a range of what the command printed is not here, and
// the artifact says so to every reader, which is what the block's "Output
// incomplete" is painted from.
func TestABlockSealedIncompleteIsStoredAsAGap(t *testing.T) {
	art := closeOneBlock(t, true)
	if art.State != content.ArtifactSealed {
		t.Fatalf("state = %s, want sealed", art.State)
	}
	if art.Truncated == nil || *art.Truncated != content.TruncGap {
		t.Fatalf("truncated = %v, want gap", art.Truncated)
	}
}

// Paired: an ordinary close claims nothing is missing.
func TestAnOrdinaryBlockCloseIsNotTruncated(t *testing.T) {
	art := closeOneBlock(t, false)
	if art.State != content.ArtifactSealed {
		t.Fatalf("state = %s, want sealed", art.State)
	}
	if art.Truncated != nil {
		t.Fatalf("truncated = %v, want none", *art.Truncated)
	}
}
