package content_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestFinishAgentRunPersistsAnsweringModelAndHarness(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	ask, err := led.SubmitAgentAsk(ctx, content.AgentAsk{ID: nextAskID(), Client: "test", Env: content.Environment{ID: "local", Kind: content.EnvLocal}, Question: "why?", Facts: content.RunFacts{Mode: "explain", Harness: "pi"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = led.FinishAgentRun(ctx, ask.RunID, content.FinishAgentRun{State: content.RunCompleted, TerminationReason: content.TermCompleted, Model: "provider/actual", EndedAt: 1}); err != nil {
		t.Fatal(err)
	}
	entry, err := led.Entry(ctx, ask.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	var facts content.RunFacts
	if err = json.Unmarshal([]byte(entry.Executions[0].Payload), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Harness != "pi" || facts.Model != "provider/actual" {
		t.Fatalf("facts: %+v", facts)
	}
}
