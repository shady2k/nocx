// Package harnesstest is the conformance suite every connector runs. It is
// exported so a connector's own test can call Run with a factory that builds
// that connector, and the assertions are about the contract rather than about
// any one agent's wire.
//
// The suite runs on a scripted fake agent. A connector that cannot pass it is
// not supported (ADR-anntnxdj decision 1); the same suite runs against a real
// agent at release.
package harnesstest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/waittest"
)

const (
	fakeAgentPackage = "github.com/shady2k/nocx/internal/harness/fakeagent"
	mcpServerPackage = "github.com/shady2k/nocx/internal/harness/harnesstest/testdata/mcpserver"
)

// Setup is what the suite gives a connector's factory: the programs it may
// launch and the script the fake agent must follow.
type Setup struct {
	AgentPath      string
	ToolServerPath string
	Script         protocol.Script
}

// Factory builds the connector under test. It is called once per check, so a
// check's script is the agent's whole behaviour for that check.
type Factory func(t *testing.T, setup Setup) harness.Harness

// Run runs every conformance check against the connector factory.
func Run(t *testing.T, hostPackage string, factory Factory) {
	t.Helper()

	bin := t.TempDir()
	agentPath := build(t, bin, "fakeagent", fakeAgentPackage)
	serverPath := build(t, bin, "mcpserver", mcpServerPackage)
	hostPath := build(t, bin, "host", hostPackage)

	t.Run("streams answer, reasoning and end in order", func(t *testing.T) {
		testOrderedStream(t, factory, agentPath, serverPath)
	})
	t.Run("a tool call reaches the bridge and its environment stays there", func(t *testing.T) {
		testToolBridge(t, factory, agentPath, serverPath)
	})
	t.Run("Cancel during a stream ends the turn with no event after", func(t *testing.T) {
		testCancelDuringStream(t, factory, agentPath, serverPath)
	})
	t.Run("Cancel during a blocked tool call ends the turn with no event after", func(t *testing.T) {
		testCancelDuringTool(t, factory, agentPath, serverPath)
	})
	t.Run("successful RPC completion closes stdin", func(t *testing.T) {
		script := protocol.Script{CompleteOnEOF: true, Steps: []protocol.Step{
			{Kind: protocol.StepEnd, Model: "fake-model"}, {Kind: protocol.StepHang},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{Dir: t.TempDir()})
		collect(t, turn)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := turn.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a failure is reported as a failure", func(t *testing.T) {
		testFailures(t, factory, agentPath, serverPath)
	})
	t.Run("Wait leaves no process of the turn", func(t *testing.T) {
		testNoProcessAfterWait(t, factory, agentPath, serverPath)
	})
	t.Run("SIGKILL of the parent ends the agent and its bridge", func(t *testing.T) {
		testParentDeath(t, agentPath, serverPath, hostPath)
	})
}

func testOrderedStream(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "working-directory")
	if err = os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, "gate")
	if err = syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(dir, "prompt")
	script := protocol.Script{Gate: gate, PromptFile: promptFile, Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "Hel"},
		{Kind: protocol.StepGate},
		{Kind: protocol.StepReasoning, Text: "thinking"},
		{Kind: protocol.StepAnswer, Text: "lo"},
		{Kind: protocol.StepEnd, Model: "fake-model", Usage: &protocol.Usage{
			InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2,
		}},
	}}
	turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "hello", Model: "chosen-model", SystemPrompt: "system",
		Dir: dir,
	})
	first := nextEvent(t, turn.Events(), 5*time.Second)
	// The process cannot finish until the consumer has observed its delta.
	writer, err := os.OpenFile(gate, os.O_WRONLY, 0) //nolint:gosec // test TempDir FIFO
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	events := append([]harness.Event{first}, collect(t, turn)...)
	raw, err := os.ReadFile(promptFile) //nolint:gosec // test TempDir
	if err != nil {
		t.Fatal(err)
	}
	var prompt protocol.Prompt
	if err = json.Unmarshal(raw, &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt.ConversationID != "conversation-1" || prompt.Text != "hello" || prompt.SystemPrompt != "system" || prompt.Model != "chosen-model" || prompt.Dir != target {
		t.Fatalf("forwarded prompt = %+v", prompt)
	}

	want := []harness.EventKind{
		harness.EventAnswerDelta, harness.EventReasoningDelta,
		harness.EventAnswerDelta, harness.EventEnd,
	}
	if got := kinds(events); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	if events[0].Text != "Hel" || events[1].Text != "thinking" || events[2].Text != "lo" {
		t.Fatalf("deltas = %q %q %q", events[0].Text, events[1].Text, events[2].Text)
	}
	if events[3].End == nil || events[3].End.Model != "fake-model" {
		t.Fatalf("end = %+v, want the model that answered", events[3].End)
	}
	if events[3].End.Usage == nil || events[3].End.Usage.InputTokens != 3 ||
		events[3].End.Usage.OutputTokens != 5 || events[3].End.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %+v", events[3].End.Usage)
	}

	outcome, err := turn.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if outcome.Model != "fake-model" || outcome.Usage == nil || outcome.Usage.OutputTokens != 5 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func testToolBridge(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	dir := t.TempDir()
	resultFile := filepath.Join(dir, "tool-result")
	serverPIDFile := filepath.Join(dir, "server.pid")
	secret := "bridge-only-secret"
	script := protocol.Script{
		ResultFile: resultFile,
		Steps: []protocol.Step{
			{Kind: protocol.StepTool, Name: "probe", Arguments: map[string]any{"note": "hello"}},
			{Kind: protocol.StepEnd, Model: "fake-model"},
		},
	}
	tools := harness.ToolBridge{
		Command: []string{serverPath},
		Env:     []string{"NOCX_TEST_SECRET=" + secret, "MCP_PIDFILE=" + serverPIDFile},
	}
	turn := start(t, factory, agentPath, serverPath, script, tools, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "use the tool", Dir: dir,
	})
	events := collect(t, turn)

	var started, finished *harness.ToolCall
	for _, ev := range events {
		switch ev.Kind {
		case harness.EventToolStarted:
			started = ev.Tool
		case harness.EventToolFinished:
			finished = ev.Tool
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("tool events missing: %v", kinds(events))
	}
	if started.Name != "probe" || started.ID == "" {
		t.Fatalf("tool started = %+v", started)
	}
	if finished.Name != started.Name || finished.ID != started.ID {
		t.Fatalf("tool finished = %+v, started = %+v", finished, started)
	}

	if _, err := turn.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	result, err := os.ReadFile(resultFile) //nolint:gosec // test TempDir
	if err != nil {
		t.Fatalf("the tool result: %v", err)
	}
	if !strings.Contains(string(result), "server_secret="+secret) {
		t.Fatalf("the bridge did not see its own environment: %q", result)
	}
	if strings.Contains(strings.SplitN(string(result), ";agent_environment=", 2)[1], secret) {
		t.Fatal("the agent environment contains the bridge secret")
	}
	serverPID := readPID(t, serverPIDFile)
	assertGone(t, serverPID, "the tool bridge")
}

func testCancelDuringStream(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	dir := t.TempDir()
	agentPIDFile := filepath.Join(dir, "agent.pid")
	script := protocol.Script{
		PIDFile:        agentPIDFile,
		HangFile:       filepath.Join(dir, "hang"),
		IgnoreStdinEOF: true,
		Steps: []protocol.Step{
			{Kind: protocol.StepAnswer, Text: "first"},
			{Kind: protocol.StepHang},
		},
	}
	turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "stream", Dir: dir,
	})

	events := []harness.Event{nextEvent(t, turn.Events(), 5*time.Second)}
	if events[0].Kind != harness.EventAnswerDelta {
		t.Fatalf("first event = %+v, want an answer delta", events[0])
	}
	waitFile(t, filepath.Join(dir, "hang"))
	if err := turn.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	events = append(events, collectWithin(t, turn.Events(), 2*time.Second)...)
	assertCancelled(t, turn, events)
	assertGone(t, readPID(t, agentPIDFile), "the agent")
}

func testCancelDuringTool(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	dir := t.TempDir()
	agentPIDFile := filepath.Join(dir, "agent.pid")
	serverPIDFile := filepath.Join(dir, "server.pid")
	script := protocol.Script{
		PIDFile:        agentPIDFile,
		IgnoreStdinEOF: true,
		Steps: []protocol.Step{
			{Kind: protocol.StepTool, Name: "probe"},
			{Kind: protocol.StepEnd},
		},
	}
	tools := harness.ToolBridge{
		Command: []string{serverPath},
		Env: []string{
			"NOCX_TEST_SECRET=bridge-only-secret",
			"MCP_PIDFILE=" + serverPIDFile,
			"MCP_BLOCK=1",
			"MCP_BLOCKEDFILE=" + filepath.Join(dir, "blocked"),
		},
	}
	turn := start(t, factory, agentPath, serverPath, script, tools, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "block", Dir: dir,
	})

	events := []harness.Event{nextEvent(t, turn.Events(), 5*time.Second)}
	if events[0].Kind != harness.EventToolStarted {
		t.Fatalf("first event = %+v, want the tool call", events[0])
	}
	waitFile(t, filepath.Join(dir, "blocked"))
	if err := turn.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	events = append(events, collectWithin(t, turn.Events(), 2*time.Second)...)
	assertCancelled(t, turn, events)
	assertGone(t, readPID(t, agentPIDFile), "the agent")
	assertGone(t, readPID(t, serverPIDFile), "the blocked tool bridge")
}

func assertCancelled(t *testing.T, turn harness.Turn, events []harness.Event) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("events after cancellation must be only failure: %v", kinds(events))
	}
	last := events[len(events)-1]
	if last.Kind != harness.EventFailure || last.Failure == nil || last.Failure.Reason != "cancelled" {
		t.Fatalf("last event = %+v, want the cancelled failure", last)
	}
	terminals := 0
	for _, ev := range events {
		if ev.Kind == harness.EventEnd || ev.Kind == harness.EventFailure {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events = %d, want exactly one: %v", terminals, kinds(events))
	}
	if _, err := turn.Wait(context.Background()); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait error = %v, want ErrCancelled", err)
	}
}

func testFailures(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	t.Run("the agent states a failure", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepFail, Reason: "the provider is down"},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "fail", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || last.Failure.Reason != "the provider is down" {
			t.Fatalf("last event = %+v", last)
		}
		_, err := turn.Wait(context.Background())
		var turnErr *harness.TurnError
		if !errors.As(err, &turnErr) || turnErr.Reason != "the provider is down" {
			t.Fatalf("Wait error = %v, want the stated failure", err)
		}
	})

	t.Run("the process exits non-zero without an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepExit, Code: 7}}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "crash", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || !strings.Contains(last.Failure.Reason, "status 7") {
			t.Fatalf("last event = %+v, want a failure naming the exit", last)
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "status 7") {
			t.Fatalf("Wait error = %v, want the exit status", err)
		}
	})

	t.Run("the process exits non-zero after an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepEnd, Model: "fake-model"},
			{Kind: protocol.StepExit, Code: 1},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "end then die", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		if events[len(events)-1].Kind != harness.EventEnd {
			t.Fatalf("last event = %+v, want the end event", events[len(events)-1])
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "status 1") {
			t.Fatalf("Wait error = %v, want the exit status rather than the end event", err)
		}
	})

	t.Run("the process ends without an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepAnswer, Text: "partial"},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "silent end", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || !strings.Contains(last.Failure.Reason, "without an end event") {
			t.Fatalf("last event = %+v", last)
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "without an end event") {
			t.Fatalf("Wait error = %v", err)
		}
	})
}

func testNoProcessAfterWait(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	dir := t.TempDir()
	agentPIDFile := filepath.Join(dir, "agent.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	script := protocol.Script{
		PIDFile:      agentPIDFile,
		ChildPIDFile: childPIDFile,
		Steps: []protocol.Step{
			{Kind: protocol.StepChild},
			{Kind: protocol.StepExit, Code: 0},
		},
	}
	turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "leave a child", Dir: dir,
	})
	collect(t, turn)
	if _, err := turn.Wait(context.Background()); err == nil || !strings.Contains(fmt.Sprint(err), "without an end event") {
		t.Fatalf("Wait error = %v, want the missing end", err)
	}
	assertGone(t, readPID(t, agentPIDFile), "the agent")
	assertGone(t, readPID(t, childPIDFile), "the child the agent left")
}

func testParentDeath(t *testing.T, agentPath, serverPath, hostPath string) {
	t.Helper()
	for _, state := range []string{"idle", "stream", "tool"} {
		t.Run(state, func(t *testing.T) { testParentDeathState(t, agentPath, serverPath, hostPath, state) })
	}
}

func testParentDeathState(t *testing.T, agentPath, serverPath, hostPath, state string) {
	t.Helper()
	dir := t.TempDir()
	agentPIDFile := filepath.Join(dir, "agent.pid")
	serverPIDFile := filepath.Join(dir, "server.pid")
	script := protocol.Script{
		HangFile: filepath.Join(dir, "hang"),
		PIDFile:  agentPIDFile,
		Steps: []protocol.Step{
			{Kind: protocol.StepTool, Name: "probe"},
			{Kind: protocol.StepEnd},
		},
	}
	if state != "tool" {
		script.Steps = []protocol.Step{{Kind: protocol.StepBridge}, {Kind: protocol.StepHang}}
		if state == "stream" {
			script.Steps = append([]protocol.Step{{Kind: protocol.StepAnswer, Text: "streaming"}}, script.Steps...)
		}
	}

	tools := harness.ToolBridge{
		Command: []string{serverPath},
		Env: []string{
			"NOCX_TEST_SECRET=bridge-only-secret",
			"MCP_PIDFILE=" + serverPIDFile,
			"MCP_BLOCK=1",
			"MCP_BLOCKEDFILE=" + filepath.Join(dir, "blocked"),
		},
	}
	scriptJSON, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}

	host := exec.Command(hostPath) //nolint:gosec // the suite built this program
	host.Env = append(os.Environ(),
		"HOST_AGENT="+agentPath,
		"HOST_SCRIPT="+string(scriptJSON),
		"HOST_TOOLS="+string(toolsJSON),
		"HOST_DIR="+dir,
	)
	host.Stdout = nil
	host.Stderr = nil
	if err := host.Start(); err != nil {
		t.Fatalf("the helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = host.Process.Kill()
		_, _ = host.Process.Wait()
	})

	waitFile(t, agentPIDFile)
	waitFile(t, serverPIDFile)
	if state == "tool" {
		waitFile(t, filepath.Join(dir, "blocked"))
	} else {
		waitFile(t, filepath.Join(dir, "hang"))
	}
	agentPID := readPID(t, agentPIDFile)
	t.Cleanup(func() { _ = syscall.Kill(-agentPID, syscall.SIGKILL) })
	serverPID := readPID(t, serverPIDFile)

	// The backend is gone: no Cancel, no Wait, just the process that held
	// the pipes. The agent must see stdin close and take its bridge with it.
	if err := host.Process.Kill(); err != nil {
		t.Fatalf("kill the parent: %v", err)
	}
	_, _ = host.Process.Wait()

	waittest.WaitForTimeout(t, "the agent and its bridge to be gone", time.Second, func() bool {
		return !alive(agentPID) && (serverPID == 0 || !alive(serverPID))
	})
}

func start(t *testing.T, factory Factory, agentPath, serverPath string, script protocol.Script, tools harness.ToolBridge, spec harness.TurnSpec) harness.Turn {
	t.Helper()
	spec.Tools = tools
	h := factory(t, Setup{AgentPath: agentPath, ToolServerPath: serverPath, Script: script})
	turn, err := h.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		_ = turn.Cancel(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = turn.Wait(ctx)
	})
	return turn
}

// collect drains a turn's stream, and fails rather than hanging when the turn
// never ends. The bound is a failure message, not the test's synchronisation:
// every check waits on an event the turn produces.
func collect(t *testing.T, turn harness.Turn) []harness.Event {
	t.Helper()
	return collectWithin(t, turn.Events(), 5*time.Second)
}

func collectWithin(t *testing.T, events <-chan harness.Event, timeout time.Duration) []harness.Event {
	t.Helper()
	var out []harness.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("the turn's event stream did not close within %s", timeout)
		}
	}
}

func nextEvent(t *testing.T, events <-chan harness.Event, timeout time.Duration) harness.Event {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("the event stream closed before the expected event")
		}
		return ev
	case <-time.After(timeout):
		t.Fatalf("the expected event did not arrive within %s", timeout)
	}
	return harness.Event{}
}

func kinds(events []harness.Event) []harness.EventKind {
	out := make([]harness.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func build(t *testing.T, dir, name, pkg string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-p", "1", "-o", out, pkg) //nolint:gosec // the package paths are this suite's own constants
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, stderr.String())
	}
	return out
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return pid
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	waittest.WaitForDetail(t, "the process to write "+path, func() string {
		raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
		return fmt.Sprintf("read error: %v, content: %q", err, raw)
	}, func() bool {
		raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
		return err == nil && len(raw) > 0
	})
}

func assertGone(t *testing.T, pid int, what string) {
	t.Helper()
	if alive(pid) {
		t.Fatalf("%s (pid %d) remains immediately after Wait", what, pid)
	}
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
