package transport

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// The old eino engine recognizes unexecuted envelopes. Harness agents own
// their own tool dialect; the transport still maps this typed failure to a
// safe sentence if an answering backend returns it.
func TestAskFailure_UnexecutedToolCallEnvelopeDialectsFailAtTransportPayload(t *testing.T) {
	reason, sentence := classifyAskFailure(&assistant.UnexecutedToolCallError{}, "fixture-model")
	if reason != content.TermFailed {
		t.Fatalf("reason=%q, want failed", reason)
	}
	if sentence != assistant.UnexecutedToolCallSentence {
		t.Fatalf("sentence=%q", sentence)
	}
	if strings.Contains(sentence, "<tool_call>") {
		t.Fatalf("provider envelope leaked: %q", sentence)
	}
}

func TestAskFailure_OrdinaryProseThatMentionsToolCallCompletes(t *testing.T) {
	const answer = "The answer quotes <tool_call> but gives a real explanation."
	h := newAskHarness(t, answerScript(answer))
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it mean?")
	if got := proseBodyOf(t, h.db.Ledger(), turn); got != answer {
		t.Fatalf("ordinary prose=%q", got)
	}
	entry, err := h.db.Ledger().Entry(t.Context(), turn)
	if err != nil || entry == nil || len(entry.Executions) != 1 || entry.Executions[0].State == nil || *entry.Executions[0].State != content.RunCompleted {
		t.Fatalf("ordinary prose run: %+v, %v", entry, err)
	}
}

// A harness owns its tool syntax. Text that resembles another provider's
// envelope cannot execute a command without a bridge call.
func TestAgentAsk_TextEnvelopeDoesNotExecuteATool(t *testing.T) {
	const envelope = `<tool_call>{"name":"session.run","arguments":{"command":"df -h"}}</tool_call>`
	h := newAskHarness(t, answerScript(envelope))
	sid := openSessionInAskPane(t, h.conn, 1)
	turn := askAndWaitForState(t, h, sid, "how much disk space is free?")
	kids, err := h.db.Ledger().Caused(t.Context(), turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, kid := range kids {
		if kid.Kind != content.EntryText {
			t.Fatalf("text executed a tool: %+v", kids)
		}
	}
	if got := proseBodyOf(t, h.db.Ledger(), turn); got != envelope {
		t.Fatalf("harness prose changed: %q", got)
	}
}
