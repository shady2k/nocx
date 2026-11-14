package content_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestConversationModelPinsActualDefault(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	submit := func(conversation, model string) content.AgentAskResult {
		t.Helper()
		ask, err := led.SubmitAgentAsk(ctx, content.AgentAsk{ID: nextAskID(), Client: "pin-test", Env: content.Environment{ID: "local", Kind: content.EnvLocal}, Question: "why?", Facts: content.RunFacts{Mode: "explain", Harness: "pi", ConversationID: conversation, Model: model}})
		if err != nil {
			t.Fatal(err)
		}
		return ask
	}
	first := submit("thread-a", "")
	if err := led.FinishAgentRun(ctx, first.RunID, content.FinishAgentRun{State: content.RunCompleted, TerminationReason: content.TermCompleted, Model: "provider/actual", EndedAt: 1}); err != nil {
		t.Fatal(err)
	}
	submit("thread-a", "provider/changed-role")
	submit("thread-b", "provider/other")
	for conversation, want := range map[string]string{"thread-a": "provider/actual", "thread-b": "provider/other", "new-thread": ""} {
		got, err := led.ConversationModel(ctx, conversation, "pi")
		if err != nil || got != want {
			t.Fatalf("%s: model=%q err=%v want %q", conversation, got, err, want)
		}
	}
	if err := led.DeleteEntry(ctx, first.EntryID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := reopenStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	led = reopened.Ledger()
	pinned, err := led.ConversationModel(ctx, "thread-a", "pi")
	if err != nil || pinned != "provider/actual" {
		t.Fatalf("restarted: %q %v", pinned, err)
	}
	got, err := led.ConversationModel(ctx, "thread-a", "other-agent")
	if err != nil || got != "" {
		t.Fatalf("other agent: %q %v", got, err)
	}
}

func TestConversationModelPinsReportedFailureAndCancellation(t *testing.T) {
	for _, state := range []content.RunState{content.RunFailed, content.RunCancelled} {
		t.Run(string(state), func(t *testing.T) {
			_, led := newLedger(t)
			ctx := context.Background()
			ask, err := led.SubmitAgentAsk(ctx, content.AgentAsk{ID: nextAskID(), Client: "pin", Env: content.Environment{ID: "local", Kind: content.EnvLocal}, Question: "why?", Facts: content.RunFacts{Mode: "explain", Harness: "pi", ConversationID: "thread"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = led.FinishAgentRun(ctx, ask.RunID, content.FinishAgentRun{State: state, TerminationReason: content.TermFailed, Model: "reported-model", EndedAt: 1}); err != nil {
				t.Fatal(err)
			}
			model, err := led.ConversationModel(ctx, "thread", "pi")
			if err != nil || model != "reported-model" {
				t.Fatalf("pin %q %v", model, err)
			}
		})
	}
}
