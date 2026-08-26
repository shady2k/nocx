package transport

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
)

func TestAskFailure_UnexecutedToolCallEnvelopeDialectsFailAtTransportPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "xml",
			payload: "<tool_call><function=run><parameter=command>df -h</parameter></function></tool_call>",
		},
		{
			name:    "json",
			payload: `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>`,
		},
		{
			name:    "multiple mixed dialect envelopes",
			payload: "<tool_call>{\"name\":\"run\",\"arguments\":{\"command\":\"df -h\"}}</tool_call>\n\n<tool_call><function=run><parameter=command>du -sh .</parameter></function></tool_call>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, sentence, engineErr := failingRun(t, "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				streamAnswerChunk(w, tt.payload)
			}))
			if state != "failed" {
				t.Fatalf("runState = %q, want failed", state)
			}
			var envelopeErr *assistant.UnexecutedToolCallError
			if !errors.As(engineErr, &envelopeErr) {
				t.Fatalf("engine error = %v, want UnexecutedToolCallError", engineErr)
			}
			if sentence != assistant.UnexecutedToolCallSentence {
				t.Fatalf("runState sentence = %q, want %q", sentence, assistant.UnexecutedToolCallSentence)
			}
			if strings.Contains(sentence, "<tool_call>") {
				t.Fatalf("runState sentence leaked the provider envelope: %q", sentence)
			}
		})
	}
}

func TestAskFailure_OrdinaryProseThatMentionsToolCallCompletes(t *testing.T) {
	state, sentence, engineErr := failingRun(t, "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		streamAnswerChunk(w, "The answer quotes <tool_call> but gives a real explanation.")
	}))
	if state != "completed" {
		t.Fatalf("runState = %q, want completed", state)
	}
	if sentence != "" {
		t.Fatalf("completed run sentence = %q, want empty", sentence)
	}
	if engineErr != nil {
		t.Fatalf("ordinary prose returned error: %v", engineErr)
	}
}

func TestAgentAsk_FailedToolEnvelopeIsNotReplayedAsAssistantMessage(t *testing.T) {
	const envelope = `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>`
	client := &conversationClient{
		scripts: [][]assistant.AskEvent{
			{answerEvent(envelope)},
			{answerEvent("try again")},
		},
		fail: []error{&assistant.UnexecutedToolCallError{}, nil},
	}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-envelope", "how much disk space is free?", 2)
	askAndSettle(t, h, sid, "ask-follow-up", "please answer that", 3)

	follow := client.nth(t, 1)
	for _, msg := range follow.Messages {
		if strings.Contains(msg.Content, "<tool_call>") {
			t.Fatalf("follow-up payload replayed the provider envelope in a %s message: %q", msg.Role, msg.Content)
		}
	}
	if answer := theAnswerIn(t, follow.Messages); answer != proseUnexecutedToolCallNotice {
		t.Fatalf("failed turn reads %q, want the explicit notice %q", answer, proseUnexecutedToolCallNotice)
	}
}

func TestAgentAsk_CompletedToolEnvelopeIsNotReplayedAsAssistantMessage(t *testing.T) {
	const envelope = `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>`
	client := &conversationClient{
		scripts: [][]assistant.AskEvent{
			{answerEvent(envelope)},
			{answerEvent("try again")},
		},
	}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-completed-envelope", "how much disk space is free?", 2)
	askAndSettle(t, h, sid, "ask-completed-follow-up", "please answer that", 3)

	follow := client.nth(t, 1)
	for _, msg := range follow.Messages {
		if strings.Contains(msg.Content, "<tool_call>") {
			t.Fatalf("follow-up payload replayed the provider envelope in a %s message: %q", msg.Role, msg.Content)
		}
	}
	if answer := theAnswerIn(t, follow.Messages); answer != proseUnexecutedToolCallNotice {
		t.Fatalf("completed envelope reads %q, want the explicit notice %q", answer, proseUnexecutedToolCallNotice)
	}
}
