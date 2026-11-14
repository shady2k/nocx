package pi

import (
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestSettledWithoutAnswerFails(t *testing.T) {
	d := newDecoder()
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure {
		t.Fatalf("settled without answer = %+v", events)
	}
}

func TestProviderErrorIsNotSuccess(t *testing.T) {
	d := newDecoder()
	_, err := d([]byte(`{"type":"message_end","message":{"role":"assistant","model":"test","stopReason":"error","errorMessage":"invalid key"}}`))
	if err != nil {
		t.Fatal(err)
	}
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure.Reason != "invalid key" {
		t.Fatalf("provider failure = %+v", events)
	}
}

func TestEndUsesCompletedMessageModelAndUsage(t *testing.T) {
	d := newDecoder()
	_, err := d([]byte(`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"actual","stopReason":"stop","content":[{"type":"text","text":"answer"}],"usage":{"input":3,"output":5,"cacheRead":2,"cacheWrite":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventEnd {
		t.Fatalf("events = %+v", events)
	}
	end := events[0].End
	if end.Model != "fake/actual" || end.Usage == nil || end.Usage.InputTokens != 3 || end.Usage.OutputTokens != 5 || end.Usage.CacheReadTokens != 2 || end.Usage.CacheWriteTokens != 1 {
		t.Fatalf("end = %+v", end)
	}
}

func TestDecoderMapsStreamingAndTools(t *testing.T) {
	d := newDecoder()
	lines := []string{
		`{"type":"message_end","message":{"role":"user","content":"question"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"answer"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"reason"}}`,
		`{"type":"tool_execution_start","toolName":"mcp__nocx__probe","toolCallId":"call"}`,
		`{"type":"tool_execution_end","toolName":"mcp__nocx__probe","toolCallId":"call"}`,
	}
	var events []harness.Event
	for _, line := range lines {
		ev, err := d([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev...)
	}
	want := []harness.EventKind{harness.EventAnswerDelta, harness.EventReasoningDelta, harness.EventToolStarted, harness.EventToolFinished}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event %d = %+v", i, events[i])
		}
	}
	if events[0].Text != "answer" || events[1].Text != "reason" || events[2].Tool.ID != "call" || events[3].Tool.Name != "mcp__nocx__probe" {
		t.Fatalf("events = %+v", events)
	}
}

func TestCancellationSettlesAsFailureEvenAfterCompletedAnswer(t *testing.T) {
	state := &cancelState{}
	decode := newDecoder(state)
	if _, err := decode([]byte(`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"answer"}]}}`)); err != nil {
		t.Fatal(err)
	}
	state.status.Store(turnCancelled)
	events, err := decode([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure.Reason != "cancelled" {
		t.Fatalf("cancelled settlement = %+v", events)
	}
}

func TestHandledPromptEndsWithExplicitFailure(t *testing.T) {
	decode := newDecoder()
	events, err := decode([]byte(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure == nil || events[0].Failure.Reason != "pi extension handled the prompt without an answer" {
		t.Fatalf("handled prompt response = %+v", events)
	}
}

func TestCancellationSuppressesLateStreamingAndTools(t *testing.T) {
	state := &cancelState{}
	decode := newDecoder(state)
	state.status.Store(turnCancelled)
	for _, line := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"late"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"late"}}`,
		`{"type":"tool_execution_start","toolName":"probe","toolCallId":"call"}`,
		`{"type":"tool_execution_end","toolName":"probe","toolCallId":"call"}`,
	} {
		events, err := decode([]byte(line))
		if err != nil || len(events) != 0 {
			t.Fatalf("cancelled stream = %+v, %v", events, err)
		}
	}
}
