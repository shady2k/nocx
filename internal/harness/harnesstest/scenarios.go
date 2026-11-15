package harnesstest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/waittest"
)

type Scenario string

const (
	ModelPin     Scenario = "model-pin"
	History      Scenario = "history"
	Ordered      Scenario = "ordered"
	Tool         Scenario = "tool"
	CancelStream Scenario = "cancel-stream"
	CancelTool   Scenario = "cancel-tool"
	Failure      Scenario = "failure"
	ParentIdle   Scenario = "parent-idle"
	ParentStream Scenario = "parent-stream"
	ParentTool   Scenario = "parent-tool"
)

// Driver translates semantic scenarios into an agent's native test interface.
// Observations are returned to the suite; contract assertions live here.
type Driver func(*testing.T, DriverSetup) Fixture

// DriverSetup describes an isolated scenario. HostConfig reconnects a killed-host
// helper to fixtures owned by the surviving test process.
type DriverSetup struct {
	Scenario                       Scenario
	Dir, AgentPath, ToolServerPath string
	HostConfig                     string
}

// Prompt contains observations from the native agent/provider boundary.
type Prompt struct{ Text, SystemPrompt, ConversationID, Model, Dir, History string }

// Fixture supplies a connector and observations, never contract assertions.
type Fixture struct {
	Harness          harness.Harness
	Model            string
	ToolName         string
	Release          func()
	Ready            func()
	Prompt           func() Prompt
	ToolResult       func() string
	AgentPID         func() int
	AgentEnvironment func() string
	HostConfig       string
	// ChangeDefaultModel changes native configuration and returns the new model ID.
	ChangeDefaultModel func() string
	// WorkingDirectoryRoot declares a connector-owned cwd instead of the pane cwd.
	WorkingDirectoryRoot string
}

// Run applies the same behavioral assertions to every connector driver.
func Run(t *testing.T, driver Driver) {
	t.Helper()
	original := driver
	driver = func(t *testing.T, setup DriverSetup) Fixture {
		fixture := original(t, setup)
		t.Cleanup(func() {
			if c, ok := fixture.Harness.(closer); ok {
				_ = c.Close(context.Background())
			}
		})
		return fixture
	}

	if os.Getenv("HARNESS_SCENARIO_HOST") == "1" {
		setup := DriverSetup{Scenario: Scenario(os.Getenv("SCENARIO_KIND")), Dir: os.Getenv("SCENARIO_DIR"), AgentPath: os.Getenv("SCENARIO_AGENT"), ToolServerPath: os.Getenv("SCENARIO_SERVER"), HostConfig: os.Getenv("SCENARIO_CONFIG")}
		for _, key := range []string{"HARNESS_SCENARIO_HOST", "SCENARIO_KIND", "SCENARIO_DIR", "SCENARIO_AGENT", "SCENARIO_SERVER", "SCENARIO_CONFIG"} {
			_ = os.Unsetenv(key)
		}
		f := driver(t, setup)
		turn := sharedStart(t, f, setup)
		if setup.Scenario == ParentStream {
			first := nextEvent(t, turn.Events(), 10*time.Second)
			if first.Kind != harness.EventAnswerDelta || first.Text != "Hel" {
				t.Fatalf("parent stream first delta: %+v", first)
			}
		}
		if err := os.WriteFile(filepath.Join(setup.Dir, "host-ready"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		for range turn.Events() {
		}
		_, _ = turn.Wait(context.Background())
		return
	}
	bin := t.TempDir()
	agent := build(t, bin, "fakeagent", fakeAgentPackage)
	server := build(t, bin, "mcpserver", mcpServerPackage)
	caller := t.Name()
	for _, scenario := range []Scenario{Ordered, Tool, CancelStream, CancelTool, Failure} {
		t.Run(string(scenario), func(t *testing.T) {
			target, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "working-directory")
			if linkErr := os.Symlink(target, dir); linkErr != nil {
				t.Fatal(linkErr)
			}
			setup := DriverSetup{scenario, dir, agent, server, ""}
			f := driver(t, setup)
			t.Cleanup(func() {
				if c, ok := f.Harness.(closer); ok {
					_ = c.Close(context.Background())
				}
			})
			facts := f.Harness.Facts()
			if facts.ToolBoundary != harness.NocxToolsOnly && facts.ToolBoundary != harness.NocxToolsByInstruction {
				t.Fatalf("unsupported tool boundary: %q", facts.ToolBoundary)
			}
			turn := sharedStart(t, f, setup)
			var events []harness.Event
			if scenario == Ordered || scenario == CancelStream {
				first := nextEvent(t, turn.Events(), 10*time.Second)
				if first.Kind != harness.EventAnswerDelta || first.Text != "Hel" {
					t.Fatalf("first streamed delta = %+v", first)
				}
				events = append(events, first)
			}
			if scenario == Ordered {
				f.Release()
			}
			if scenario == CancelStream || scenario == CancelTool {
				if scenario == CancelTool {
					first := nextEvent(t, turn.Events(), 10*time.Second)
					if first.Kind != harness.EventToolStarted {
						t.Fatalf("first tool event = %+v", first)
					}
					events = append(events, first)
				}
				f.Ready()
				if cancelErr := turn.Cancel(context.Background()); cancelErr != nil {
					t.Fatal(cancelErr)
				}
			}
			events = append(events, collectWithin(t, turn.Events(), 10*time.Second)...)
			if len(events) == 0 {
				t.Fatal("no events")
			}
			switch scenario {
			case Ordered:
				want := []harness.EventKind{harness.EventAnswerDelta, harness.EventReasoningDelta, harness.EventAnswerDelta, harness.EventEnd}
				if fmt.Sprint(kinds(events)) != fmt.Sprint(want) || events[1].Text != "thinking" || events[2].Text != "lo" {
					t.Fatalf("ordered events = %+v", events)
				}
				end := events[3].Outcome
				if end == nil || end.Model != f.Model || end.Usage == nil || end.Usage.InputTokens != 3 || end.Usage.OutputTokens != 5 || end.Usage.CacheReadTokens != 2 {
					t.Fatalf("end = %+v", end)
				}

				got := f.Prompt()
				if got.Text != "hello" || !strings.Contains(got.SystemPrompt, "system") || got.Model != f.Model {
					t.Fatalf("forwarded prompt = %+v", got)
				}
				assertWorkingDirectory(t, f, got.Dir, target)

			case Tool:
				var started, finished *harness.ToolCall
				for _, ev := range events {
					if ev.Kind == harness.EventToolStarted {
						started = ev.Tool
					}
					if ev.Kind == harness.EventToolFinished {
						finished = ev.Tool
					}
				}
				if started == nil || finished == nil || started.ID == "" || started.ID != finished.ID || started.Name != f.ToolName || finished.Name != started.Name {
					t.Fatalf("tool events = %+v", events)
				}
				result := f.ToolResult()
				if !strings.Contains(result, "server_secret=bridge-only-secret") {
					t.Fatalf("bridge environment missing: %s", result)
				}

				// Read the native agent process observation. Tool arguments are
				// model-controlled input, not evidence of the agent environment.
				if strings.Contains(f.AgentEnvironment(), "bridge-only-secret") {
					t.Fatal("bridge secret leaked into agent environment")
				}

			case CancelStream, CancelTool:
				assertCancelled(t, turn, events)
			case Failure:
				last := events[len(events)-1]
				if last.Kind != harness.EventFailure || last.Failure == nil || !strings.Contains(last.Failure.Reason, "provider is down") {
					t.Fatalf("failure = %+v", last)
				}
				_, waitErr := turn.Wait(context.Background())
				var turnErr *harness.TurnError
				if !errors.As(waitErr, &turnErr) {
					t.Fatalf("Wait error = %v", waitErr)
				}
			}

			terminals := 0
			for i, event := range events {
				if event.Kind == harness.EventEnd || event.Kind == harness.EventFailure {
					terminals++
					if i != len(events)-1 {
						t.Fatalf("event after terminal: %+v", events)
					}
				}
			}
			if terminals != 1 {
				t.Fatalf("terminal event count %d: %+v", terminals, events)
			}
			outcome, err := turn.Wait(context.Background())
			terminal := events[len(events)-1]
			if terminal.Outcome == nil {
				t.Fatal("terminal has no outcome")
			}
			if !reflect.DeepEqual(outcome, *terminal.Outcome) {
				t.Fatalf("Wait outcome = %+v; terminal = %+v", outcome, terminal.Outcome)
			}
			if scenario != Failure && scenario != CancelStream && scenario != CancelTool {
				if err != nil {
					t.Fatal(err)
				}
				if outcome.Model != f.Model {
					t.Fatalf("outcome model = %q", outcome.Model)
				}
			}
			agentPID := f.AgentPID()
			if agentPID <= 0 {
				t.Fatalf("invalid observed agent PID %d", agentPID)
			}
			if scenario == CancelStream || scenario == CancelTool {
				waittest.WaitFor(t, "cancelled agent exits", func() bool { return !alive(agentPID) })
			} else if !alive(agentPID) {
				t.Fatal("normal turn ended session")
			}
			if scenario == CancelTool {
				bridgePID := readPID(t, filepath.Join(dir, "server.pid"))
				waittest.WaitFor(t, "cancelled session bridge exits", func() bool { return !alive(bridgePID) })
			}
		})
	}

	t.Run("model pin survives changed native default", func(t *testing.T) {
		setup := DriverSetup{ModelPin, t.TempDir(), agent, server, ""}
		f := driver(t, setup)
		if f.ChangeDefaultModel == nil {
			t.Skip("driver has no native default model configuration")
		}
		pinned := ""
		changed := ""
		for i := 0; i < 3; i++ {
			chosen, want := "", f.Model
			if i == 1 {
				changed = f.ChangeDefaultModel()
				if changed == "" || changed == pinned {
					t.Fatalf("changed default = %q, pinned model = %q", changed, pinned)
				}
				chosen, want = pinned, pinned
			} else if i == 2 {
				want = changed
			}
			conversation := "model-pin-conversation"
			if i == 2 {
				conversation = "default-control"
			}
			turn, err := f.Harness.Run(context.Background(), harness.TurnSpec{ConversationID: conversation, Text: "hello", Model: chosen, Dir: setup.Dir})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, turn)
			outcome, err := turn.Wait(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 || events[len(events)-1].Kind != harness.EventEnd || events[len(events)-1].Outcome == nil {
				t.Fatalf("model selection events = %+v", events)
			}
			observed := f.Prompt().Model
			end := events[len(events)-1].Outcome
			if observed != want || end.Model != want || outcome.Model != want {
				t.Fatalf("chosen %q: provider model = %q, end = %q, outcome = %q; want %q", chosen, observed, end.Model, outcome.Model, want)
			}
			if i == 0 {
				pinned = end.Model
			}
		}
	})

	t.Run("conversation history and stable system prompt", func(t *testing.T) {
		setup := DriverSetup{History, t.TempDir(), agent, server, ""}
		f := driver(t, setup)
		if !f.Harness.Facts().ContinuesConversationByID {
			t.Skip("connector declares no conversation continuation")
		}
		var observations []Prompt
		for i, text := range []string{"hello", "followup", "isolated"} {
			id := "conversation-1"
			if i == 2 {
				id = "conversation-2"
			}
			turn, err := f.Harness.Run(context.Background(), harness.TurnSpec{ConversationID: id, Text: text, SystemPrompt: "system", Model: f.Model, Dir: setup.Dir})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, turn)
			if len(events) == 0 || events[len(events)-1].Kind != harness.EventEnd {
				t.Fatalf("history turn events: %+v", events)
			}
			if _, err := turn.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			observations = append(observations, f.Prompt())
		}
		first, second, isolated := observations[0], observations[1], observations[2]
		if first.SystemPrompt != second.SystemPrompt || !strings.Contains(first.SystemPrompt, "system") || !strings.Contains(isolated.SystemPrompt, "system") {
			t.Fatal("system prompt changed within a conversation or omitted instructions")
		}
		if f.WorkingDirectoryRoot != "" {
			assertWorkingDirectory(t, f, first.Dir, setup.Dir)
			if first.Dir != second.Dir {
				t.Fatalf("conversation working directory changed: %q -> %q", first.Dir, second.Dir)
			}
		}
		if !strings.Contains(second.History, "hello") || !strings.Contains(second.History, "first answer") {
			t.Fatalf("continuation lost history: %s", second.History)
		}
		if strings.Contains(isolated.History, "hello") || strings.Contains(isolated.History, "followup") || strings.Contains(isolated.History, "first answer") {
			t.Fatalf("conversation leaked history: %s", isolated.History)
		}
	})
	for _, state := range []Scenario{ParentIdle, ParentStream, ParentTool} {
		t.Run(string(state), func(t *testing.T) {
			sharedParentDeath(t, driver, DriverSetup{state, t.TempDir(), agent, server, ""}, caller)
		})
	}
}

func assertWorkingDirectory(t *testing.T, f Fixture, observed, pane string) {
	t.Helper()
	if f.WorkingDirectoryRoot == "" {
		if observed != pane {
			t.Fatalf("agent cwd = %q, want pane cwd %q", observed, pane)
		}
		return
	}
	rel, err := filepath.Rel(f.WorkingDirectoryRoot, observed)
	if observed == pane || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("agent cwd = %q, want private directory under %q, distinct from pane %q", observed, f.WorkingDirectoryRoot, pane)
	}
}

func sharedStart(t *testing.T, f Fixture, s DriverSetup) harness.Turn {
	t.Helper()
	spec := harness.TurnSpec{Text: "hello", SystemPrompt: "system", ConversationID: "conversation-1", Model: f.Model, Dir: s.Dir}
	if s.Scenario == Tool || s.Scenario == CancelTool || s.Scenario == ParentIdle || s.Scenario == ParentStream || s.Scenario == ParentTool {
		spec.Tools = harness.ToolBridge{Command: []string{s.ToolServerPath}, Env: []string{"NOCX_TEST_SECRET=bridge-only-secret", "MCP_PIDFILE=" + filepath.Join(s.Dir, "server.pid")}}
		if s.Scenario != Tool {
			spec.Tools.Env = append(spec.Tools.Env, "MCP_BLOCK=1", "MCP_BLOCKEDFILE="+filepath.Join(s.Dir, "blocked"))
		}
	}
	turn, err := f.Harness.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = turn.Cancel(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = turn.Wait(ctx)
	})
	return turn
}

func sharedParentDeath(t *testing.T, driver Driver, s DriverSetup, caller string) {
	t.Helper()
	fixture := driver(t, s)
	s.HostConfig = fixture.HostConfig
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Split(caller, "/")
	for i, name := range names {
		names[i] = "^" + regexp.QuoteMeta(name) + "$"
	}
	host := exec.Command(executable, "-test.run="+strings.Join(names, "/")) //nolint:gosec // this test binary
	host.Env = append(os.Environ(), "HARNESS_SCENARIO_HOST=1", "SCENARIO_KIND="+string(s.Scenario), "SCENARIO_DIR="+s.Dir, "SCENARIO_AGENT="+s.AgentPath, "SCENARIO_SERVER="+s.ToolServerPath, "SCENARIO_CONFIG="+s.HostConfig)
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Process.Kill(); _, _ = host.Process.Wait() })
	waitFile(t, filepath.Join(s.Dir, "host-ready"))
	fixture.Ready()
	waitFile(t, filepath.Join(s.Dir, "server.pid"))
	agentPID := fixture.AgentPID()
	if agentPID <= 0 {
		t.Fatalf("invalid observed agent PID %d", agentPID)
	}
	bridgePID := readPID(t, filepath.Join(s.Dir, "server.pid"))
	t.Cleanup(func() { _ = syscall.Kill(-agentPID, syscall.SIGKILL); _ = syscall.Kill(bridgePID, syscall.SIGKILL) })
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = host.Process.Wait()
	waittest.WaitForTimeout(t, "agent and bridge gone after parent SIGKILL", time.Second, func() bool { return !alive(agentPID) && !alive(bridgePID) })
}
