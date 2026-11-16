package fakeconnectortest_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/harness/fakeconnectortest"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestConformance(t *testing.T) { harnesstest.Run(t, fakeDriver) }

func TestFakeProcessChecks(t *testing.T) {
	harnesstest.RunFakeProcessChecks(t, func(t *testing.T, s harnesstest.Setup) harness.Harness {
		return fakeconnectortest.New(fakeconnectortest.Config{AgentPath: s.AgentPath, Script: s.Script, Base: s.Base})
	})
}

func fakeDriver(t *testing.T, setup harnesstest.DriverSetup) harnesstest.Fixture {
	t.Helper()
	path := func(name string) string { return filepath.Join(setup.Dir, name) }
	script := protocol.Script{ProbeEnvironment: true, EnvironmentFile: path("environment"), PIDFile: path("agent.pid"), PromptFile: path("prompt"), ResultFile: path("result"), HangFile: path("hang")}
	end := protocol.Step{Kind: protocol.StepEnd, Model: "fake-model", Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2}}
	switch setup.Scenario {
	case harnesstest.ModelPin:
		script.DefaultModelFile = path("default-model")
		if err := os.WriteFile(script.DefaultModelFile, []byte("fake-model"), 0o600); err != nil {
			t.Fatal(err)
		}
		end.Model = ""
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "answer"}, end}
	case harnesstest.Ordered:
		script.Gate = path("gate")
		if err := syscall.Mkfifo(script.Gate, 0o600); err != nil {
			t.Fatal(err)
		}
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepGate}, {Kind: protocol.StepReasoning, Text: "thinking"}, {Kind: protocol.StepAnswer, Text: "lo"}, end}
	case harnesstest.Tool, harnesstest.CancelTool, harnesstest.ParentTool:
		script.Steps = []protocol.Step{{Kind: protocol.StepTool, Name: "probe", Arguments: map[string]any{"note": "hello"}}, end}
	case harnesstest.Steering:
		// The turn is held open after its first words, so the message is sent
		// while it is genuinely running.
		script.Gate = path("gate")
		if err := syscall.Mkfifo(script.Gate, 0o600); err != nil {
			t.Fatal(err)
		}
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepGate}, {Kind: protocol.StepEnd, Model: "fake-model"}}
	case harnesstest.CancelStream:
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepHang}}
	case harnesstest.ParentIdle:
		script.Steps = []protocol.Step{{Kind: protocol.StepBridge}, {Kind: protocol.StepHang}}
	case harnesstest.ParentStream:
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepBridge}, {Kind: protocol.StepHang}}
	case harnesstest.Failure:
		script.Steps = []protocol.Step{{Kind: protocol.StepFail, Reason: "the provider is down", Model: "fake-model", Usage: end.Usage}}
	}
	waitFile := func(name string) {
		waittest.WaitFor(t, "fixture "+name, func() bool { raw, err := os.ReadFile(path(name)); return err == nil && len(raw) > 0 })
	}
	var base harness.Base
	if setup.Scenario == harnesstest.CancelTool {
		gate := path("cancel-tool")
		if err := syscall.Mkfifo(gate, 0o600); err != nil {
			t.Fatal(err)
		}
		base.Tools = func(context.Context, harness.SessionInfo) (harness.ToolSession, harness.ToolBridge, error) {
			bridge := harness.ToolBridge{Command: []string{setup.ToolServerPath}, Env: []string{
				"MCP_BLOCK=1", "MCP_BLOCKEDFILE=" + path("blocked"), "MCP_CANCEL_GATE=" + gate,
				"MCP_PIDFILE=" + path("server.pid"), "NOCX_TEST_SECRET=bridge-only-secret",
			}}
			return &cancelToolSession{gate: gate}, bridge, nil
		}
	}
	return harnesstest.Fixture{
		Harness: fakeconnectortest.New(fakeconnectortest.Config{AgentPath: setup.AgentPath, Script: script, Base: base}), Model: "fake-model", ToolName: "probe",
		ChangeDefaultModel: func() string {
			if err := os.WriteFile(script.DefaultModelFile, []byte("changed-model"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "changed-model"
		},
		Release: func() { harnesstest.ReleaseGate(t, script.Gate) },
		Ready: func() {
			switch setup.Scenario {
			case harnesstest.CancelTool, harnesstest.ParentTool:
				waitFile("blocked")
			default:
				waitFile("hang")
			}
		},
		Prompt: func() harnesstest.Prompt {
			raw, err := os.ReadFile(script.PromptFile)
			if err != nil {
				t.Fatal(err)
			}
			var p protocol.Prompt
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			return harnesstest.Prompt{Text: p.Text, SystemPrompt: p.SystemPrompt, ConversationID: p.ConversationID, Model: p.Model, Dir: p.Dir}
		},
		ToolResult: func() string {
			raw, err := os.ReadFile(script.ResultFile)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		},
		AgentEnvironment: func() string {
			raw, err := os.ReadFile(script.EnvironmentFile)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		},
		AgentPID: func() int {
			waitFile("agent.pid")
			raw, err := os.ReadFile(script.PIDFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		},
	}
}

// cancelToolSession supplies the fixture endpoint's cancellation boundary.
// Its server returns a cancelled result before the agent reports tool_finished.
type cancelToolSession struct {
	gate string
	once sync.Once
	err  error
}

func (*cancelToolSession) BeginTurn(harness.TurnAuthority) error { return nil }
func (s *cancelToolSession) EndTurn(context.Context) error {
	s.once.Do(func() { s.err = os.WriteFile(s.gate, []byte("cancel"), 0o600) })
	return s.err
}
func (*cancelToolSession) Pending() bool                { return false }
func (*cancelToolSession) Retire(context.Context) error { return nil }

func TestProtocolCommandsAndResponse(t *testing.T) {
	c := fakeconnectortest.New(fakeconnectortest.Config{})
	defer func() { _ = c.Close(context.Background()) }()
	codec := c.NewCodec(harness.SessionInfo{ConversationID: "conversation", Generation: 7}, harness.Launch{})
	setup := codec.Setup()
	if len(setup) != 1 || setup[0].Command.Name != "initialize" || setup[0].Prompt || setup[0].Check == nil {
		t.Fatalf("setup: %+v", setup)
	}
	raw, err := codec.Encode("setup-1", setup[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	var initialize protocol.Command
	if err = json.Unmarshal(raw, &initialize); err != nil {
		t.Fatal(err)
	}
	if initialize.Session == nil || initialize.Session.ConversationID != "conversation" || initialize.Session.Generation != 7 {
		t.Fatalf("initialize: %+v", initialize)
	}
	decoded, err := codec.Decode([]byte(`{"kind":"response","id":"setup-1","payload":{"conversationId":"conversation","generation":7}}`))
	if err != nil || decoded.Response == nil || decoded.Response.ID != "setup-1" || len(decoded.Events) != 0 {
		t.Fatalf("decode: %+v, %v", decoded, err)
	}
	if err = setup[0].Check(decoded.Response.Payload); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []any{nil, "wrong", map[string]any{"conversationId": "other", "generation": float64(7)}, map[string]any{"conversationId": "conversation", "generation": float64(8)}} {
		if err = setup[0].Check(payload); err == nil {
			t.Fatalf("accepted wrong payload: %+v", payload)
		}
	}
	steps := codec.BeginTurn(harness.TurnSpec{Text: "hello", ConversationID: "conversation"})
	if len(steps) != 2 || !steps[0].Barrier || steps[0].Prompt || steps[0].Command.Name != "get_state" || !steps[1].Prompt || steps[1].Barrier || steps[1].Command.Name != "prompt" {
		t.Fatalf("turn: %+v", steps)
	}
	raw, err = codec.Encode("barrier-1", steps[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	var barrier protocol.Command
	if err = json.Unmarshal(raw, &barrier); err != nil || barrier.ID != "barrier-1" || barrier.Name != "get_state" {
		t.Fatalf("barrier: %+v, %v", barrier, err)
	}
	raw, err = codec.Encode("command-1", steps[1].Command)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.Command
	if err = json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	if command.ID != "command-1" || command.Prompt.Text != "hello" {
		t.Fatalf("wire: %+v", command)
	}
	decoded, err = codec.Decode([]byte(`{"kind":"response","id":"command-1","reason":"rejected","payload":{"detail":"bad"}}`))
	if err != nil || decoded.Response == nil || decoded.Response.Error != "rejected" || len(decoded.Events) != 0 {
		t.Fatalf("response: %+v, %v", decoded, err)
	}
	// pi's order, and its answer: the queue is cleared first (the answer names
	// what it dropped) and then the turn is aborted.
	if got := codec.CancelSequence(); len(got) != 2 || got[0].Name != "clear_queue" || got[1].Name != "abort" {
		t.Fatalf("cancel: %+v", got)
	}
	// The fixture decodes that answer the way pi's own does, so the actor's
	// hand-back path is exercised without a real agent.
	recovery, ok := codec.(harness.SteerRecovery)
	if !ok {
		t.Fatal("the fixture codec cannot read back what a cancel dropped")
	}
	dropped, ok := recovery.DroppedSteers(map[string]any{"steering": []any{"first", "second"}})
	if !ok || len(dropped) != 2 || dropped[0] != "first" || dropped[1] != "second" {
		t.Fatalf("dropped: %v, %v", dropped, ok)
	}
	if _, ok := recovery.DroppedSteers(map[string]any{"other": 1}); ok {
		t.Fatal("an answer that names no queue reported dropped messages")
	}
}

func TestCodecsArePerIncarnation(t *testing.T) {
	c := fakeconnectortest.New(fakeconnectortest.Config{})
	defer func() { _ = c.Close(context.Background()) }()
	first := c.NewCodec(harness.SessionInfo{ConversationID: "one", Generation: 1}, harness.Launch{})
	second := c.NewCodec(harness.SessionInfo{ConversationID: "two", Generation: 2}, harness.Launch{})
	restarted := c.NewCodec(harness.SessionInfo{ConversationID: "one", Generation: 3}, harness.Launch{})
	if first == second || first == restarted || second == restarted {
		t.Fatal("codec instances shared")
	}
	for i, codec := range []harness.Codec{first, second, restarted} {
		conversation := []string{"one", "two", "one"}[i]
		raw, err := codec.Encode("init", codec.Setup()[0].Command)
		if err != nil {
			t.Fatal(err)
		}
		var wire protocol.Command
		if err = json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Session.ConversationID != conversation || wire.Session.Generation != uint64(i+1) { //nolint:gosec // loop index is bounded by fixture count
			t.Fatalf("incarnation %d: %+v", i, wire.Session)
		}
	}
}
