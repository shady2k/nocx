package pi

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestRunAnswersThroughSession(t *testing.T) {
	c, _ := configuredPi(t, fakemodel.Response{Text: []string{"answer"}})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Run(ctx, harness.TurnSpec{ConversationID: "conversation", Text: "question", Model: "nocx-test/test-model", SystemPrompt: "nocx instructions", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	for ev := range turn.Events() {
		if ev.Kind == harness.EventAnswerDelta {
			answer += ev.Text
		}
	}
	outcome, err := turn.Wait(ctx)
	if err != nil || answer != "answer" || outcome.Model != "nocx-test/test-model" {
		t.Fatalf("answer=%q outcome=%+v err=%v", answer, outcome, err)
	}
}
