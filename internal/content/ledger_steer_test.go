package content_test

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// A person's message sent into a running turn is part of what was SHOWN, so it
// is part of the ledger's record of the turn (ADR-0019). It is one run of prose
// in the turn like the answer's own — kind `text`, and `source` says whose it
// is — seated in the order the person sent it, with its words as the body.
func TestASteerMessageIsThePersonsOwnProseInTheTurnsOrder(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	ask := askOne(t, led, "session-steer")

	before, err := led.OpenProse(ctx, ask.EntryID, ask.RunID)
	if err != nil {
		t.Fatalf("OpenProse: %v", err)
	}
	if err = led.AppendChunk(ctx, before.ArtifactID, 1, []byte("let me look")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if err = led.SealProse(ctx, before.EntryID); err != nil {
		t.Fatalf("SealProse: %v", err)
	}

	const message = "also check the second log"
	steer, err := led.SubmitSteer(ctx, ask.EntryID, ask.RunID, message)
	if err != nil {
		t.Fatalf("SubmitSteer: %v", err)
	}
	// Its words are the child's body, complete the moment it is written: a
	// message a person typed is not streamed.
	body, err := led.Artifact(ctx, steer.ArtifactID)
	if err != nil || body == nil {
		t.Fatalf("Artifact(steer) = %+v, %v", body, err)
	}
	if body.MediaType != content.MediaText || body.State != content.ArtifactSealed {
		t.Fatalf("steer body = %q %q, want sealed text", body.MediaType, body.State)
	}
	if got := joinChunks(body.Chunks); got != message {
		t.Fatalf("steer body = %q, want the person's message", got)
	}
	if body.ExecutionID != nil {
		t.Fatalf("the message names execution %d — it was typed, not attempted", *body.ExecutionID)
	}

	after, err := led.OpenProse(ctx, ask.EntryID, ask.RunID)
	if err != nil {
		t.Fatalf("OpenProse after the message: %v", err)
	}
	if err = led.AppendChunk(ctx, after.ArtifactID, 1, []byte("then the second log says")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	// THE ORDER IS THE TURN'S: the prose before the message, the message, the
	// prose after it — which is what a restored block draws, top to bottom.
	kids, err := led.Caused(ctx, ask.EntryID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(kids) != 3 {
		t.Fatalf("the turn's children = %+v, want three", kids)
	}
	want := []struct {
		entryID string
		source  content.Source
	}{
		{before.EntryID, content.SourceAssistant},
		{steer.EntryID, content.SourceUser},
		{after.EntryID, content.SourceAssistant},
	}
	for i, w := range want {
		got := kids[i]
		if got.EntryID != w.entryID || got.Kind != content.EntryText || got.Source != w.source ||
			got.Position != i || got.Intent != "" {
			t.Fatalf("child %d = %+v, want %s as %s text at seat %d", i, got, w.entryID, w.source, i)
		}
	}
}

// A message with no turn to belong to is refused: prose is somebody's, and a
// seat with no parent has no place in any flow.
func TestASteerMessageNeedsItsTurn(t *testing.T) {
	_, led := newLedger(t)
	if _, err := led.SubmitSteer(context.Background(), "", 1, "hello?"); err == nil {
		t.Fatal("a message with no turn was accepted")
	}
}

func joinChunks(chunks [][]byte) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.Write(chunk)
	}
	return b.String()
}
