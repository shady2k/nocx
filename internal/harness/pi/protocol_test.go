package pi

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func codecResponse(t *testing.T, codec harness.Codec, line string) *harness.Response {
	t.Helper()
	decoded, err := codec.Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Response == nil {
		t.Fatalf("missing command response: %+v", decoded)
	}
	return decoded.Response
}

func codecWire(t *testing.T, codec harness.Codec, id string, command harness.Command) map[string]any {
	t.Helper()
	raw, err := codec.Encode(id, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("command is not newline framed: %q", raw)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["id"] != id || wire["type"] != command.Name {
		t.Fatalf("command identity: %s", raw)
	}
	return wire
}

func TestCodecTurnPlanBarrierAndPrompt(t *testing.T) {
	connector := newTestConnector(t.TempDir(), nil)
	launch, err := connector.Start(context.Background(), harness.TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Cleanup() })
	codec := connector.NewCodec(harness.SessionInfo{}, launch)
	text := "line one\nline two with \"quotes\""
	plan := codec.BeginTurn(harness.TurnSpec{Text: text})
	if len(plan) < 2 || plan[0].Command.Name != "get_state" || !plan[0].Barrier {
		t.Fatalf("missing get_state barrier: %+v", plan)
	}
	for i, step := range plan {
		if step.Barrier != (i == 0) || step.Prompt != (i == len(plan)-1) {
			t.Fatalf("step %d flags: %+v", i, step)
		}
	}
	prompt := plan[len(plan)-1]
	if prompt.Command.Name != "prompt" {
		t.Fatalf("final command: %+v", prompt)
	}
	// Checks consume the same response payloads that the actor receives.
	for i, step := range plan[:len(plan)-1] {
		response := codecResponse(t, codec, `{"type":"response","id":"state","command":"get_state","success":true,"data":{"model":{"provider":"fake","id":"default"}}}`)
		if step.Check != nil {
			if err := step.Check(response.Payload); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
		}
	}
	wire := codecWire(t, codec, "actor-prompt-17", prompt.Command)
	if wire["message"] != text {
		t.Fatalf("prompt payload: %+v", wire)
	}
}

func TestCodecPinnedModelSelectionIsVerified(t *testing.T) {
	codec := newTestConnector(t.TempDir(), nil).NewCodec(harness.SessionInfo{}, harness.Launch{})
	plan := codec.BeginTurn(harness.TurnSpec{Model: "fake/chosen", Text: "question"})
	names := make([]string, len(plan))
	for i, step := range plan {
		names[i] = step.Command.Name
	}
	if !reflect.DeepEqual(names, []string{"get_state", "set_model", "get_state", "prompt"}) {
		t.Fatalf("model plan: %v", names)
	}
	if !plan[0].Barrier || !plan[3].Prompt {
		t.Fatalf("plan flags: %+v", plan)
	}
	initial := codecResponse(t, codec, `{"type":"response","id":"state","command":"get_state","success":true,"data":{"model":{"provider":"fake","id":"other"}}}`)
	if plan[0].Check != nil {
		if err := plan[0].Check(initial.Payload); err != nil {
			t.Fatal(err)
		}
	}
	wire := codecWire(t, codec, "actor-select-8", plan[1].Command)
	if wire["provider"] != "fake" || wire["modelId"] != "chosen" {
		t.Fatalf("selection payload: %+v", wire)
	}
	if plan[2].Check == nil {
		t.Fatal("no exact-model verification after selection")
	}
	for _, tc := range []struct {
		provider, model string
		ok              bool
	}{
		{"fake", "chosen", true}, {"fake", "other", false}, {"other", "chosen", false},
	} {
		raw, err := json.Marshal(map[string]any{"type": "response", "id": "verify", "command": "get_state", "success": true, "data": map[string]any{"model": map[string]string{"provider": tc.provider, "id": tc.model}}})
		if err != nil {
			t.Fatal(err)
		}
		response := codecResponse(t, codec, string(raw))
		if err := plan[2].Check(response.Payload); (err == nil) != tc.ok {
			t.Errorf("verify %s/%s: %v", tc.provider, tc.model, err)
		}
	}
}

func TestCodecCommandResponsesKeepActorIDsAndErrors(t *testing.T) {
	codec := newTestConnector(t.TempDir(), nil).NewCodec(harness.SessionInfo{}, harness.Launch{})
	for _, id := range []string{"actor-9", "actor-10"} {
		codecWire(t, codec, id, harness.Command{Name: "abort"})
		response := codecResponse(t, codec, `{"type":"response","id":"`+id+`","command":"abort","success":true,"data":{}}`)
		if response.ID != id || response.Error != "" {
			t.Fatalf("response: %+v", response)
		}
	}
	decoded, err := codec.Decode([]byte(`{"type":"response","id":"actor-11","command":"set_model","success":false,"error":"model unavailable"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Response == nil || decoded.Response.ID != "actor-11" || decoded.Response.Error != "model unavailable" || len(decoded.Events) != 0 {
		t.Fatalf("unsuccessful command must be an actor-matched error, not a turn event: %+v", decoded)
	}
}

func TestCodecHandledPromptFailsImmediately(t *testing.T) {
	codec := newTestConnector(t.TempDir(), nil).NewCodec(harness.SessionInfo{}, harness.Launch{})
	codec.BeginTurn(harness.TurnSpec{Text: "question"})
	decoded, err := codec.Decode([]byte(`{"type":"response","id":"prompt-2","command":"prompt","success":true,"data":{"disposition":"handled"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Response == nil || decoded.Response.ID != "prompt-2" {
		t.Fatalf("lost prompt response ID: %+v", decoded)
	}
	if len(decoded.Events) != 0 || !strings.Contains(decoded.Response.Error, "handled") {
		t.Fatalf("handled prompt: %+v", decoded)
	}
}

func codecEvents(t *testing.T, codec harness.Codec, line string) []harness.Event {
	t.Helper()
	decoded, err := codec.Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Events
}

func TestCodecBeginTurnResetsLastMessageAndUsage(t *testing.T) {
	codec := &codec{}
	codec.BeginTurn(harness.TurnSpec{})
	codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"first","stopReason":"stop","content":[{"type":"text","text":"first answer"}],"usage":{"input":13,"output":7}}}`)
	first := codecEvents(t, codec, `{"type":"agent_settled"}`)
	if len(first) != 1 || first[0].Kind != harness.EventEnd {
		t.Fatalf("first terminal: %+v", first)
	}
	codec.BeginTurn(harness.TurnSpec{})
	empty := codecEvents(t, codec, `{"type":"agent_settled"}`)
	if len(empty) != 1 || empty[0].Kind != harness.EventFailure {
		t.Fatalf("previous answer leaked into new turn: %+v", empty)
	}
	codec.BeginTurn(harness.TurnSpec{})
	codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"second","stopReason":"stop","content":[{"type":"text","text":"second answer"}],"usage":{"input":2,"output":3}}}`)
	second := codecEvents(t, codec, `{"type":"agent_settled"}`)
	if len(second) != 1 || second[0].Kind != harness.EventEnd {
		t.Fatalf("second terminal: %+v", second)
	}
	end := second[0].End
	if end == nil || end.Model != "fake/second" || end.Usage == nil || end.Usage.InputTokens != 2 || end.Usage.OutputTokens != 3 {
		t.Fatalf("turn-local facts leaked: %+v", end)
	}
}

func TestCodecLastAssistantErrorOverridesEarlierAnswer(t *testing.T) {
	codec := &codec{}
	codec.BeginTurn(harness.TurnSpec{})
	codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"chosen","stopReason":"stop","content":[{"type":"text","text":"earlier answer"}]}}`)
	codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"chosen","stopReason":"error","errorMessage":"provider quota exhausted"}}`)
	terminal := codecEvents(t, codec, `{"type":"agent_settled"}`)
	if len(terminal) != 1 || terminal[0].Kind != harness.EventFailure || terminal[0].Failure == nil || terminal[0].Failure.Reason != "provider quota exhausted" {
		t.Fatalf("last assistant error was hidden: %+v", terminal)
	}
	codec.BeginTurn(harness.TurnSpec{})
	codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"chosen","stopReason":"stop","content":[{"type":"text","text":"recovered"}]}}`)
	next := codecEvents(t, codec, `{"type":"agent_settled"}`)
	if len(next) != 1 || next[0].Kind != harness.EventEnd {
		t.Fatalf("previous error leaked: %+v", next)
	}
}

func TestCodecCancelSequence(t *testing.T) {
	codec := newTestConnector(t.TempDir(), nil).NewCodec(harness.SessionInfo{}, harness.Launch{})
	commands := codec.CancelSequence()
	names := make([]string, len(commands))
	for i, command := range commands {
		names[i] = command.Name
		codecWire(t, codec, "cancel-"+command.Name, command)
	}
	if !reflect.DeepEqual(names, []string{"clear_queue", "abort"}) {
		t.Fatalf("cancel sequence: %v", names)
	}
}

func TestCodecLaterTurnUsesObservedModel(t *testing.T) {
	codec := newTestConnector(t.TempDir(), nil).NewCodec(harness.SessionInfo{}, harness.Launch{})
	first := codec.BeginTurn(harness.TurnSpec{Model: "fake/chosen", Text: "first"})
	if len(first) != 4 || first[2].Check == nil {
		t.Fatalf("first pinned plan: %+v", first)
	}
	response := codecResponse(t, codec, `{"type":"response","id":"readback","command":"get_state","success":true,"data":{"model":{"provider":"fake","id":"chosen"}}}`)
	if err := first[2].Check(response.Payload); err != nil {
		t.Fatal(err)
	}
	same := codec.BeginTurn(harness.TurnSpec{Model: "fake/chosen", Text: "second"})
	names := make([]string, len(same))
	for i, step := range same {
		names[i] = step.Command.Name
	}
	if !reflect.DeepEqual(names, []string{"get_state", "prompt"}) {
		t.Fatalf("same pinned model plan: %v", names)
	}
	if !same[0].Barrier || same[0].Check == nil {
		t.Fatalf("missing checked barrier: %+v", same[0])
	}
	// The already-built plan cannot add selection after the barrier. Reject
	// an unexpected change before it can prompt under the wrong model.
	changed := codecResponse(t, codec, `{"type":"response","id":"barrier","command":"get_state","success":true,"data":{"model":{"provider":"fake","id":"other"}}}`)
	if err := same[0].Check(changed.Payload); err == nil {
		t.Fatal("changed model passed the same-model barrier; prompt would use the wrong pin")
	}
	next := codec.BeginTurn(harness.TurnSpec{Model: "fake/chosen", Text: "third"})
	names = make([]string, len(next))
	for i, step := range next {
		names[i] = step.Command.Name
	}
	if !reflect.DeepEqual(names, []string{"get_state", "set_model", "get_state", "prompt"}) {
		t.Fatalf("changed observed model plan: %v", names)
	}
}
