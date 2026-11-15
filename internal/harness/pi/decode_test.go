package pi

import (
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestSettledWithoutAnswerFails(t *testing.T) {
	d := newDecoder(&harness.Outcome{})
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure {
		t.Fatalf("settled without answer = %+v", events)
	}
}

func TestProviderErrorIsNotSuccess(t *testing.T) {
	d := newDecoder(&harness.Outcome{})
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
	d := newDecoder(&harness.Outcome{})
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
	end := events[0].Outcome
	if end.Model != "fake/actual" || end.Usage == nil || end.Usage.InputTokens != 3 || end.Usage.OutputTokens != 5 || end.Usage.CacheReadTokens != 2 || end.Usage.CacheWriteTokens != 1 {
		t.Fatalf("end = %+v", end)
	}
}

func TestDecoderMapsStreamingAndTools(t *testing.T) {
	d := newDecoder(&harness.Outcome{})
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

func TestHandledPromptEndsWithExplicitFailure(t *testing.T) {
	decode := newDecoder(&harness.Outcome{})
	events, err := decode([]byte(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure == nil || events[0].Failure.Reason != "pi extension handled the prompt without an answer" {
		t.Fatalf("handled prompt response = %+v", events)
	}
}

// pi fills usage with zeros when its provider sent none; the end reports
// no usage rather than three zeros (and a stated usage still comes through).
func TestEndDropsUsagePiFilledWithZeros(t *testing.T) {
	d := newDecoder(&harness.Outcome{})
	if _, err := d([]byte(`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"actual","stopReason":"stop","content":[{"type":"text","text":"answer"}],"usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}}`)); err != nil {
		t.Fatal(err)
	}
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventEnd || events[0].Outcome.Usage != nil {
		t.Fatalf("events = %+v, want an end with no usage", events)
	}
}

// A usage with only cache counters set is stated, not dropped.
func TestEndKeepsCacheOnlyUsage(t *testing.T) {
	d := newDecoder(&harness.Outcome{})
	if _, err := d([]byte(`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"actual","stopReason":"stop","content":[{"type":"text","text":"answer"}],"usage":{"input":0,"output":0,"cacheRead":7,"cacheWrite":0}}}`)); err != nil {
		t.Fatal(err)
	}
	events, err := d([]byte(`{"type":"agent_settled","aborted":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome == nil || events[0].Outcome.Usage == nil || events[0].Outcome.Usage.CacheReadTokens != 7 {
		t.Fatalf("events = %+v, want an end keeping the cache-read usage", events)
	}
}

func TestFailureKeepsCompletedCallUsage(t *testing.T) {
	for _, terminal := range []string{`{"type":"agent_settled","aborted":true}`, `{"type":"agent_settled","aborted":false}`} {
		d := newDecoder(&harness.Outcome{})
		for _, line := range []string{
			`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"actual","stopReason":"toolUse","usage":{"input":3,"output":5,"cacheRead":2,"cacheWrite":1}}}`,
			`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"actual","stopReason":"error","errorMessage":"provider is down","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}}`,
		} {
			if _, err := d([]byte(line)); err != nil {
				t.Fatal(err)
			}
		}
		events, err := d([]byte(terminal))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Kind != harness.EventFailure {
			t.Fatalf("events = %+v", events)
		}
		facts := events[0].Outcome
		if facts == nil || facts.Usage == nil || *facts.Usage != (harness.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1}) {
			t.Fatalf("failure facts = %+v, want completed call usage", facts)
		}
	}
}
