package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"

	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"

	"github.com/shady2k/nocx/internal/harness"
)

func isolatedPiEnvironment(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("real pi is not installed on PATH")
	}
	home := t.TempDir()
	return []string{
		"ANTHROPIC_API_KEY=", "OPENAI_API_KEY=", "GEMINI_API_KEY=", "GOOGLE_API_KEY=",
		"XAI_API_KEY=", "GROQ_API_KEY=", "OPENROUTER_API_KEY=", "MISTRAL_API_KEY=",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PI_CODING_AGENT_DIR=" + filepath.Join(home, "agent"),
		"PI_OFFLINE=1",
	}
}

func TestReadinessWithoutPi(t *testing.T) {
	c := New(t.TempDir(), []string{"PATH=" + t.TempDir(), "PI_OFFLINE=1"})
	got := c.Readiness(context.Background())
	if got.State != harness.NotInstalled || got.Message == "" {
		t.Fatalf("Readiness = %+v, want not installed with an explanation", got)
	}
}

func TestRealPiWithoutProviderIsNotConfigured(t *testing.T) {
	c := New(t.TempDir(), isolatedPiEnvironment(t))
	got := c.Readiness(context.Background())
	if got.State != harness.NotConfigured || got.Message == "" {
		t.Fatalf("Readiness = %+v, want not configured with an explanation", got)
	}
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("models without provider credentials = %+v", models)
	}
}

func TestPiDeclaresSupportedFacts(t *testing.T) {
	got := New(t.TempDir(), isolatedPiEnvironment(t)).Facts()
	if got.ToolBoundary != harness.NocxToolsOnly || !got.ContinuesConversationByID || !got.ReportsAnsweringModel {
		t.Fatalf("Facts = %+v", got)
	}
}

func configuredPi(t *testing.T, responses ...fakemodel.Response) (*Connector, *fakemodel.Server) {
	t.Helper()
	fake := fakemodel.New(responses...)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return configuredPiAtURL(t, server.URL), fake
}

func configuredPiAtURL(t *testing.T, url string) *Connector {
	t.Helper()
	env := isolatedPiEnvironment(t)
	var agentDir string
	for _, entry := range env {
		if strings.HasPrefix(entry, "PI_CODING_AGENT_DIR=") {
			agentDir = strings.TrimPrefix(entry, "PI_CODING_AGENT_DIR=")
		}
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"nocx-test": map[string]any{
		"baseUrl": url + "/v1", "api": "openai-completions", "apiKey": "disposable-test-key",
		"models": []map[string]any{{
			"id": "test-model", "name": "Disposable model", "reasoning": true,
			"contextWindow": 128000, "maxTokens": 4096,
			"cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	}}}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return New(t.TempDir(), env)
}

func runRealPi(t *testing.T, c *Connector, spec harness.TurnSpec) ([]harness.Event, harness.Outcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Run(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) })
	var events []harness.Event
	for {
		select {
		case ev, ok := <-turn.Events():
			if !ok {
				outcome, err := turn.Wait(ctx)
				if err != nil {
					t.Fatalf("Wait: %v; events: %+v", err, events)
				}
				return events, outcome
			}
			events = append(events, ev)
		case <-ctx.Done():
			t.Fatalf("pi event stream: %v", ctx.Err())
		}
	}
}

func TestRealPiModelsAndReadiness(t *testing.T) {
	c, _ := configuredPi(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := c.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "nocx-test/test-model" || models[0].Name != "Disposable model" {
		t.Fatalf("Models = %+v", models)
	}
	if got := c.Readiness(ctx); got.State != harness.Ready {
		t.Fatalf("Readiness = %+v", got)
	}
}

func TestRealPiContinuesOnlyMatchingConversationAndKeepsPromptStable(t *testing.T) {
	c, fake := configuredPi(t,
		fakemodel.Response{Text: []string{"first answer"}},
		fakemodel.Response{Text: []string{"second answer"}},
		fakemodel.Response{Text: []string{"isolated answer"}},
	)
	dir := t.TempDir()
	spec := harness.TurnSpec{ConversationID: "conversation-one", Text: "first question", Model: "nocx-test/test-model", SystemPrompt: "Nocx stable system instructions.", Dir: dir}
	_, outcome := runRealPi(t, c, spec)
	if outcome.Model != "nocx-test/test-model" {
		t.Fatalf("answering model = %q", outcome.Model)
	}
	// Recreate the connector: continuity must live on disk, not in process memory.
	c = New(c.dataDir, c.env)
	spec.Text = "second question"
	runRealPi(t, c, spec)
	spec.ConversationID = "conversation-two"
	spec.Text = "isolated question"
	runRealPi(t, c, spec)
	requests := fake.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	var systems []string
	for _, request := range requests {
		var system string
		for _, raw := range request.Messages {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Role == "system" || msg.Role == "developer" {
				system += string(msg.Content)
			}
		}
		if !strings.Contains(system, spec.SystemPrompt) {
			t.Fatalf("system prompt omitted nocx instructions: %s", system)
		}
		systems = append(systems, system)
		if len(request.Tools) != 0 {
			t.Fatalf("built-in tools exposed: %s", request.Tools)
		}
	}
	if systems[0] != systems[1] {
		t.Fatalf("system prompt changed across turns:\n%q\n%q\n%q", systems[0], systems[1], systems[2])
	}
	if !strings.Contains(string(requests[1].Body), "first question") || !strings.Contains(string(requests[1].Body), "first answer") {
		t.Fatalf("continued request lost history: %s", requests[1].Body)
	}
	if strings.Contains(string(requests[2].Body), "first question") || strings.Contains(string(requests[2].Body), "second question") {
		t.Fatalf("conversation leaked history: %s", requests[2].Body)
	}
}

func TestRealPiCancelBlockedProvider(t *testing.T) {
	c, fake := configuredPi(t, fakemodel.Response{Block: make(chan struct{})})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Run(ctx, harness.TurnSpec{ConversationID: "cancel-conversation", Text: "block", Model: "nocx-test/test-model", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) })
	if _, err := fake.WaitRequests(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	for {
		select {
		case event, ok := <-turn.Events():
			if !ok {
				if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure == nil || events[0].Failure.Reason != "cancelled" {
					t.Fatalf("cancel events = %+v", events)
				}
				if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
					t.Fatalf("Wait = %v", err)
				}
				return
			}
			events = append(events, event)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestRealPiToolBridgeKeepsEnvironmentPrivate(t *testing.T) {
	c, fake := configuredPi(t,
		fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "call-probe", Name: "mcp__nocx__probe", Arguments: `{"note":"hello"}`}}},
		fakemodel.Response{Text: []string{"tool completed"}},
	)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	events, _ := runRealPi(t, c, harness.TurnSpec{ConversationID: "tool-conversation", Text: "use the probe", Model: "nocx-test/test-model", Dir: t.TempDir(), Tools: harness.ToolBridge{Command: []string{bin, "-test.run=^TestPiMCPHelper$"}, Env: []string{"NOCX_TEST_SECRET=bridge-private-marker", "PI_TEST_MCP_HELPER=1"}}})
	var started, finished *harness.ToolCall
	for _, event := range events {
		if event.Kind == harness.EventToolStarted {
			started = event.Tool
		}
		if event.Kind == harness.EventToolFinished {
			finished = event.Tool
		}
	}
	if started == nil || finished == nil || started.ID == "" || started.ID != finished.ID || started.Name != finished.Name {
		t.Fatalf("tool lifecycle missing or mismatched: %+v", events)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if !strings.Contains(string(requests[1].Body), "server_secret=bridge-private-marker") {
		t.Fatalf("bridge did not receive its environment: %s", requests[1].Body)
	}
	if strings.Contains(string(requests[0].Body), "bridge-private-marker") {
		t.Fatal("bridge secret exposed before tool execution")
	}
}

// TestPiMCPHelper is a real stdio MCP server; its protocol is independent of pi.
func TestPiMCPHelper(t *testing.T) {
	if os.Getenv("PI_TEST_MCP_HELPER") != "1" {
		return
	}
	if path := os.Getenv("PI_TEST_PIDFILE"); path != "" {
		raw, _ := json.Marshal([]int{os.Getppid(), os.Getpid()})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			os.Exit(1)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "pi-test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "probe", "description": "Report the bridge environment", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"note": map[string]string{"type": "string"}}}}}}
		case "tools/call":
			if os.Getenv("PI_TEST_BLOCK") == "1" {
				_ = os.WriteFile(os.Getenv("PI_TEST_PIDFILE")+".blocked", []byte("blocked"), 0o600)
				continue
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "server_secret=" + os.Getenv("NOCX_TEST_SECRET")}}, "isError": false}
		default:
			result = map[string]any{}
		}
		if err := out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func TestRealPiReportsDefaultAnsweringModel(t *testing.T) {
	c, _ := configuredPi(t, fakemodel.Response{Text: []string{"default answer"}})
	_, outcome := runRealPi(t, c, harness.TurnSpec{ConversationID: "default-conversation", Text: "hello", Dir: t.TempDir()})
	if outcome.Model != "nocx-test/test-model" {
		t.Fatalf("default answering model = %q", outcome.Model)
	}
}

func TestRealPiParentDeath(t *testing.T) {
	for _, state := range []string{"stream", "tool"} {
		t.Run(state, func(t *testing.T) {
			response := fakemodel.Response{Block: make(chan struct{})}
			if state == "tool" {
				response = fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "blocked-call", Name: "mcp__nocx__probe", Arguments: `{}`}}}
			}
			c, fake := configuredPi(t, response)
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "pids")
			spec := harness.TurnSpec{ConversationID: "parent-death", Text: "block", Model: "nocx-test/test-model", Dir: dir, Tools: harness.ToolBridge{Command: []string{bin, "-test.run=^TestPiMCPHelper$"}, Env: []string{"PI_TEST_MCP_HELPER=1", "PI_TEST_PIDFILE=" + pidfile, "PI_TEST_BLOCK=1"}}}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			host := exec.Command(bin, "-test.run=^TestPiParentDeathHost$") //nolint:gosec // this test binary is the process host
			host.Env = append(os.Environ(), c.env...)
			host.Env = append(host.Env, "PI_TEST_HOST_SPEC="+string(raw), "PI_TEST_HOST_DATA="+c.dataDir)
			if err := host.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := fake.WaitRequests(ctx, 1); err != nil {
				t.Fatal(err)
			}
			var pids []int
			waittest.WaitFor(t, "pi and bridge started", func() bool {
				data, err := os.ReadFile(pidfile) //nolint:gosec // private test directory
				return err == nil && json.Unmarshal(data, &pids) == nil && len(pids) == 2
			})
			if state == "tool" {
				waittest.WaitFor(t, "tool call reached bridge", func() bool { _, err := os.Stat(pidfile + ".blocked"); return err == nil })
			}
			t.Cleanup(func() {
				for _, pid := range pids {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if err := host.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = host.Wait()
			waittest.WaitFor(t, "pi and bridge exited after parent death", func() bool {
				for _, pid := range pids {
					if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
						continue
					}
					// An orphan zombie is terminated and belongs to the operating-system reaper.
					if raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
						end := strings.LastIndexByte(string(raw), ')')
						fields := strings.Fields(string(raw[end+1:]))
						if len(fields) > 0 && fields[0] == "Z" {
							continue
						}
					}
					return false
				}
				return true
			})
		})
	}
}

func TestPiParentDeathHost(t *testing.T) {
	raw := os.Getenv("PI_TEST_HOST_SPEC")
	if raw == "" {
		return
	}
	var spec harness.TurnSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	// Remove the helper payload so no bridge configuration enters pi's environment.
	data := os.Getenv("PI_TEST_HOST_DATA")
	_ = os.Unsetenv("PI_TEST_HOST_SPEC")
	_ = os.Unsetenv("PI_TEST_HOST_DATA")
	turn, err := New(data, nil).Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	_, _ = turn.Wait(context.Background())
}
