package content_test

import (
	"context"
	"testing"
)

func TestOpenProse_RefusesARunThatIsNotThisTurnsAndAcceptsOneThatIs(t *testing.T) {
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-conv", "tab-conv", "pane-conv")
	envReady(t, led, "local")
	ctx := context.Background()
	mine := askIn(t, led, "session-1", "pane-conv")
	other := askIn(t, led, "session-1", "pane-conv")

	if _, err := led.OpenProse(ctx, mine.EntryID, other.RunID); err == nil {
		t.Fatal("prose opened under one turn was attributed to another turn's run")
	}
	if _, err := led.OpenProse(ctx, mine.EntryID, 0); err == nil {
		t.Fatal("prose opened with no run at all — prose belongs to the run that printed it")
	}
	// The paired success on the same store, so the refusals above are not a
	// method that refuses everything.
	if _, err := led.OpenProse(ctx, mine.EntryID, mine.RunID); err != nil {
		t.Fatalf("OpenProse with the turn's own run: %v", err)
	}
}
