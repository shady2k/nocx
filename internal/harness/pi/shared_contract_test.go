package pi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestRealPiSharedContract(t *testing.T) {
	harnesstest.Run(t, realPiDriver)
}

func realPiDriver(t *testing.T, setup harnesstest.DriverSetup) harnesstest.Fixture {
	t.Helper()
	if setup.HostConfig != "" {
		var config struct {
			DataDir string
			Env     []string
		}
		if err := json.Unmarshal([]byte(setup.HostConfig), &config); err != nil {
			t.Fatal(err)
		}
		return harnesstest.Fixture{Harness: New(config.DataDir, config.Env), Model: "nocx-test/test-model"}
	}
	gate := make(chan struct{})
	response := fakemodel.Response{}
	switch setup.Scenario {
	case harnesstest.History:
		response.Text = []string{"first answer"}
	case harnesstest.Ordered:
		response.Deltas = []fakemodel.Delta{{Text: "Hel"}, {Thinking: "thinking", Block: gate}, {Text: "lo"}}
		response.Usage = &fakemodel.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2}
	case harnesstest.CancelStream, harnesstest.ParentStream:
		response.Deltas = []fakemodel.Delta{{Text: "Hel"}, {Block: gate}}
	case harnesstest.ParentIdle:
		response.Block = gate
	case harnesstest.Tool, harnesstest.CancelTool, harnesstest.ParentTool:
		response.ToolCalls = []fakemodel.ToolCall{{ID: "probe-call", Name: "mcp__nocx__probe", Arguments: `{"note":"hello"}`}}
	case harnesstest.Failure:
		response.StatusCode = http.StatusBadRequest
		response.Error = "provider is down"
	}
	responses := []fakemodel.Response{response, {Text: []string{"tool completed"}}}
	if setup.Scenario == harnesstest.History {
		responses = []fakemodel.Response{response, {Text: []string{"second answer"}}, {Text: []string{"isolated answer"}}}
	}
	if setup.Scenario == harnesstest.ModelPin {
		responses = []fakemodel.Response{{Text: []string{"default answer"}}, {Text: []string{"pinned answer"}}, {Text: []string{"changed default answer"}}}
	}
	fake := fakemodel.New(responses...)
	observationPath := filepath.Join(t.TempDir(), "process.json")
	preloadPath := filepath.Join(t.TempDir(), "observe.cjs")
	// The real Node runtime records launch facts before loading pi. This neither
	// replaces pi nor participates in its RPC or provider protocols.
	observer := `require('node:fs').writeFileSync(` + strconv.Quote(observationPath) + `, JSON.stringify({pid:process.pid,cwd:process.cwd(),env:process.env}));`
	if err := os.WriteFile(preloadPath, []byte(observer), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := configuredPiAtURL(t, server.URL)
	c.env = append(c.env, "NODE_OPTIONS=--require="+preloadPath)
	type observation struct {
		PID int               `json:"pid"`
		Cwd string            `json:"cwd"`
		Env map[string]string `json:"env"`
	}
	observe := func() observation {
		var result observation
		raw, err := os.ReadFile(observationPath) //nolint:gosec // private test observation file
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	hostConfig, err := json.Marshal(struct {
		DataDir string
		Env     []string
	}{c.dataDir, c.env})
	if err != nil {
		t.Fatal(err)
	}
	return harnesstest.Fixture{
		Harness: c, Model: "nocx-test/test-model", ToolName: "mcp__nocx__probe", HostConfig: string(hostConfig),
		WorkingDirectoryRoot: filepath.Join(c.dataDir, "pi", "workspaces"),
		ChangeDefaultModel: func() string {
			var agentDir string
			for _, entry := range c.env {
				if strings.HasPrefix(entry, "PI_CODING_AGENT_DIR=") {
					agentDir = strings.TrimPrefix(entry, "PI_CODING_AGENT_DIR=")
				}
			}
			modelsPath := filepath.Join(agentDir, "models.json")
			raw, err := os.ReadFile(modelsPath) //nolint:gosec // isolated pi configuration
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Providers map[string]map[string]json.RawMessage `json:"providers"`
			}
			if err = json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			provider := config.Providers["nocx-test"]
			var models []map[string]any
			if err = json.Unmarshal(provider["models"], &models); err != nil {
				t.Fatal(err)
			}
			other := make(map[string]any, len(models[0]))
			for key, value := range models[0] {
				other[key] = value
			}
			other["id"], other["name"] = "other-model", "Changed default model"
			provider["models"], err = json.Marshal(append(models, other))
			if err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(modelsPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			settings := []byte(`{"defaultProvider":"nocx-test","defaultModel":"other-model"}`)
			if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			return "nocx-test/other-model"
		},
		Release: func() { close(gate) },
		Ready: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := fake.WaitRequests(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if setup.Scenario == harnesstest.CancelTool || setup.Scenario == harnesstest.ParentTool {
				waittest.WaitFor(t, "tool blocked", func() bool { _, err := os.Stat(filepath.Join(setup.Dir, "blocked")); return err == nil })
			}
		},
		AgentPID: func() int { return observe().PID },
		AgentEnvironment: func() string {
			raw, err := json.Marshal(observe().Env)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		},
		Prompt: func() harnesstest.Prompt {
			requests := fake.Requests()
			if len(requests) == 0 {
				t.Fatal("no provider request")
			}
			req := requests[len(requests)-1]
			p := harnesstest.Prompt{Model: "nocx-test/" + req.Model, Dir: observe().Cwd}
			for _, raw := range req.Messages {
				var msg struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				if err := json.Unmarshal(raw, &msg); err != nil {
					t.Fatal(err)
				}
				var content string
				if err := json.Unmarshal(msg.Content, &content); err != nil {
					var blocks []struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(msg.Content, &blocks); err != nil {
						t.Fatal(err)
					}
					for _, block := range blocks {
						content += block.Text
					}
				}
				if msg.Role == "system" || msg.Role == "developer" {
					p.SystemPrompt += content
				}
				if msg.Role == "user" {
					p.Text = content
				}
				if msg.Role == "user" || msg.Role == "assistant" {
					p.History += content + "\n"
				}
			}
			return p
		},
		ToolResult: func() string {
			var result strings.Builder
			for _, req := range fake.Requests() {
				for _, raw := range req.Messages {
					var msg struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					}
					_ = json.Unmarshal(raw, &msg)
					if msg.Role == "tool" {
						var text string
						if json.Unmarshal(msg.Content, &text) == nil {
							result.WriteString(text)
						} else {
							result.Write(msg.Content)
						}
					}
				}
			}
			return result.String()
		},
	}
}
