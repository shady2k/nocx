package pi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestRealPiConformance(t *testing.T) { harnesstest.Run(t, realPiDriver) }

type piHostConfig struct {
	DataDir string
	Env     []string
}

// The observer is a native pi extension. It only records facts; it never changes
// prompts, tools, model selection, or process lifetime.
const piObserver = `import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
export default function(pi) {
 const dir = process.env.NOCX_PI_OBSERVATIONS;
 writeFileSync(join(dir, 'agent.pid'), String(process.pid));
 writeFileSync(join(dir, 'environment'), JSON.stringify(process.env));
 writeFileSync(join(dir, 'cwd'), process.cwd());
 pi.on('before_agent_start', (event) => {
  writeFileSync(join(dir, 'native-prompt'), JSON.stringify(event));
 });
}
`

func piAgentDir(env []string) string {
	for _, v := range env {
		if strings.HasPrefix(v, "PI_CODING_AGENT_DIR=") {
			return strings.TrimPrefix(v, "PI_CODING_AGENT_DIR=")
		}
	}
	return ""
}

func writePiJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func installPiObserver(t *testing.T, c *Connector, dir string) {
	t.Helper()
	extensions := filepath.Join(piAgentDir(c.env), "extensions")
	if err := os.MkdirAll(extensions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensions, "observe.ts"), []byte(piObserver), 0o600); err != nil {
		t.Fatal(err)
	}
	c.env = append(c.env, "NOCX_PI_OBSERVATIONS="+dir)
}

func readPiFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	waittest.WaitFor(t, "pi observation "+name, func() bool {
		raw, err := os.ReadFile(path) //nolint:gosec // isolated fixture observation in t.TempDir
		return err == nil && len(raw) > 0
	})
	raw, err := os.ReadFile(path) //nolint:gosec // isolated fixture observation in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func piPID(t *testing.T, dir string) int {
	t.Helper()
	n, err := strconv.Atoi(readPiFile(t, dir, "agent.pid"))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func piRequestPrompt(t *testing.T, fake *fakemodel.Server, dir string) harnesstest.Prompt {
	t.Helper()
	requests := fake.Requests()
	if len(requests) == 0 {
		t.Fatal("no provider request")
	}
	r := requests[len(requests)-1]
	p := harnesstest.Prompt{Model: "nocx-test/" + r.Model, Dir: readPiFile(t, dir, "cwd")}
	var history []json.RawMessage
	for _, raw := range r.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		text := piMessageText(msg.Content)
		if msg.Role == "system" || msg.Role == "developer" {
			p.SystemPrompt = text
		} else {
			history = append(history, raw)
			if msg.Role == "user" {
				p.Text = text
			}
		}
	}
	if len(history) > 0 {
		history = history[:len(history)-1]
	}
	raw, _ := json.Marshal(history)
	p.History = string(raw)
	return p
}

func realPiDriver(t *testing.T, s harnesstest.DriverSetup) harnesstest.Fixture {
	t.Helper()
	if s.HostConfig != "" {
		var cfg piHostConfig
		if err := json.Unmarshal([]byte(s.HostConfig), &cfg); err != nil {
			t.Fatal(err)
		}
		return harnesstest.Fixture{Harness: New(cfg.DataDir, cfg.Env), Model: "nocx-test/test-model"}
	}
	gate := make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate) }) }
	t.Cleanup(unblock)
	end := fakemodel.Response{Text: []string{"first answer"}}
	var script []fakemodel.Response
	switch s.Scenario {
	case harnesstest.Ordered:
		script = []fakemodel.Response{{Deltas: []fakemodel.Delta{{Text: "Hel"}, {Thinking: "thinking", Block: gate}, {Text: "lo"}}, Usage: &fakemodel.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2}}}
	case harnesstest.CancelStream:
		script = []fakemodel.Response{{Deltas: []fakemodel.Delta{{Text: "Hel"}, {Text: "lo", Block: gate}}}}
	case harnesstest.ParentStream:
		script = []fakemodel.Response{{Deltas: []fakemodel.Delta{{Text: "Hel"}}, ToolCalls: []fakemodel.ToolCall{{ID: "probe-1", Name: "mcp__nocx__probe", Arguments: `{"note":"hello"}`}}}}
	case harnesstest.Tool, harnesstest.CancelTool, harnesstest.ParentTool:
		script = []fakemodel.Response{{ToolCalls: []fakemodel.ToolCall{{ID: "probe-1", Name: "mcp__nocx__probe", Arguments: `{"note":"hello","agent_environment":{}}`}}}, end}
	case harnesstest.ParentIdle:
		script = []fakemodel.Response{{Block: gate}}
	case harnesstest.Failure:
		script = []fakemodel.Response{{StatusCode: 400, Error: "the provider is down"}}
	default:
		script = []fakemodel.Response{end, end, end}
	}
	fake := fakemodel.New(script...)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := configuredPiAtURL(t, server.URL)
	installPiObserver(t, c, s.Dir)
	cfg := piHostConfig{c.dataDir, c.env}
	raw, _ := json.Marshal(cfg)
	ready := func() {
		switch s.Scenario {
		case harnesstest.CancelTool, harnesstest.ParentTool:
			_ = readPiFile(t, s.Dir, "blocked")
		default:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := fake.WaitRequests(ctx, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	return harnesstest.Fixture{
		Harness: c, Model: "nocx-test/test-model", ToolName: "mcp__nocx__probe", WorkingDirectoryRoot: filepath.Join(c.dataDir, "pi", "workspaces"), HostConfig: string(raw), Release: unblock, Ready: ready,
		Prompt:   func() harnesstest.Prompt { return piRequestPrompt(t, fake, s.Dir) },
		AgentPID: func() int { return piPID(t, s.Dir) }, AgentEnvironment: func() string { return readPiFile(t, s.Dir, "environment") },
		ToolResult: func() string {
			reqs := fake.Requests()
			if len(reqs) < 2 {
				t.Fatal("tool result never reached provider")
			}
			var content []string
			for _, raw := range reqs[len(reqs)-1].Messages {
				var msg struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal(raw, &msg)
				if msg.Role == "tool" {
					content = append(content, msg.Content)
				}
			}
			return strings.Join(content, "\n")
		},
		ChangeDefaultModel: func() string {
			t.Helper()
			path := filepath.Join(piAgentDir(c.env), "models.json")
			raw, err := os.ReadFile(path) //nolint:gosec // isolated pi models fixture in t.TempDir
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
			second := map[string]any{}
			for k, v := range models[0] {
				second[k] = v
			}
			second["id"] = "changed-model"
			encoded, err := json.Marshal(append(models, second))
			if err != nil {
				t.Fatal(err)
			}
			provider["models"] = encoded
			writePiJSON(t, path, config)
			writePiJSON(t, filepath.Join(piAgentDir(c.env), "settings.json"), map[string]string{"defaultProvider": "nocx-test", "defaultModel": "changed-model"})
			return "nocx-test/changed-model"
		},
	}
}

func piMessageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	for _, part := range parts {
		text += part.Text
	}
	return text
}
