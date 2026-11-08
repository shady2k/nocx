package app

// nocx-q9od5: the pane's first prompt is the LAST step of an open, and it
// arrives after the open's own stages are done — which is exactly why no
// stage owned the owner's twenty silent seconds. The leg that applies the
// session's frames is where the fact is, so it is noted there, once.
import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/lifecycle"
)

func TestTheLegNotesTheOpensFirstPromptOnce(t *testing.T) {
	rec := diagnostics.NewRecorder()
	ctx := diagnostics.WithRecorder(context.Background(), rec)
	ctx, trace := diagnostics.BeginOpen(ctx, diagnostics.OpenInfo{Kind: "ssh", Key: "ssh:profile:one"})
	if trace == nil {
		t.Fatal("BeginOpen returned no trace with a recorder on the context")
	}
	c := newLifecycleCursor(ctx, nil, &atomic.Bool{})
	applied := 0
	apply := func(context.Context) { applied++ }
	// A hello comes first: it is not a prompt, and a leg that counted it
	// would report a prompt that had not been drawn yet.
	if err := c.applyFrame(0, lifecycle.KindHello, apply); err != nil {
		t.Fatalf("hello frame: %v", err)
	}
	if err := c.applyFrame(0, lifecycle.KindPromptReady, apply); err != nil {
		t.Fatalf("prompt_ready frame: %v", err)
	}
	// A session's later prompts are the same session's, not this open's: the
	// first one is the end of the walk being measured.
	if err := c.applyFrame(0, lifecycle.KindPromptReady, apply); err != nil {
		t.Fatalf("second prompt_ready frame: %v", err)
	}
	trace.Finish(nil)
	if applied != 3 {
		t.Fatalf("frames applied = %d, want 3 (the note must not eat a frame)", applied)
	}
	recent := rec.Snapshot().Recent
	if len(recent) != 1 {
		t.Fatalf("recent opens = %d, want 1", len(recent))
	}
	if recent[0].FirstPrompt <= 0 {
		t.Fatalf("the open recorded no first prompt: %+v", recent[0])
	}
}
