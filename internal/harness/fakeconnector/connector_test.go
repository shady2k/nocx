package fakeconnector_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/harness/fakeconnector"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestConformance(t *testing.T) { harnesstest.Run(t, fakeDriver) }

func TestFakeProcessChecks(t *testing.T) {
	harnesstest.RunFakeProcessChecks(t, func(t *testing.T, s harnesstest.Setup) harness.Harness {
		return fakeconnector.New(fakeconnector.Config{AgentPath: s.AgentPath, Script: s.Script})
	})
}

func fakeDriver(t *testing.T, setup harnesstest.DriverSetup) harnesstest.Fixture {
	t.Helper()
	path := func(name string) string { return filepath.Join(setup.Dir, name) }
	script := protocol.Script{EnvironmentFile: path("environment"), PIDFile: path("agent.pid"), PromptFile: path("prompt"), ResultFile: path("result"), HangFile: path("hang")}
	end := protocol.Step{Kind: protocol.StepEnd, Model: "fake-model", Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2}}
	switch setup.Scenario {
	case harnesstest.Ordered:
		script.Gate = path("gate")
		if err := syscall.Mkfifo(script.Gate, 0o600); err != nil {
			t.Fatal(err)
		}
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepGate}, {Kind: protocol.StepReasoning, Text: "thinking"}, {Kind: protocol.StepAnswer, Text: "lo"}, end}
	case harnesstest.Tool, harnesstest.CancelTool, harnesstest.ParentTool:
		script.Steps = []protocol.Step{{Kind: protocol.StepTool, Name: "probe", Arguments: map[string]any{"note": "hello"}}, end}
	case harnesstest.CancelStream:
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepHang}}
	case harnesstest.ParentIdle:
		script.Steps = []protocol.Step{{Kind: protocol.StepBridge}, {Kind: protocol.StepHang}}
	case harnesstest.ParentStream:
		script.Steps = []protocol.Step{{Kind: protocol.StepAnswer, Text: "Hel"}, {Kind: protocol.StepBridge}, {Kind: protocol.StepHang}}
	case harnesstest.Failure:
		script.Steps = []protocol.Step{{Kind: protocol.StepFail, Reason: "the provider is down"}}
	}
	waitFile := func(name string) {
		waittest.WaitFor(t, "fixture "+name, func() bool { raw, err := os.ReadFile(path(name)); return err == nil && len(raw) > 0 })
	}
	return harnesstest.Fixture{
		Harness: fakeconnector.New(fakeconnector.Config{AgentPath: setup.AgentPath, Script: script}), Model: "fake-model", ToolName: "probe",
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
