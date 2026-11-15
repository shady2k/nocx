package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestSessionLaunchIsPrivateAndStable(t *testing.T) {
	c := newTestConnector(t.TempDir(), []string{"PI_OFFLINE=1"})
	spec := harness.TurnSpec{ConversationID: "conversation", SystemPrompt: "exact instructions", Dir: t.TempDir(), Tools: harness.ToolBridge{Command: []string{"bridge", "mcp"}, Env: []string{"BEARER=private"}}}
	first, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Dir = t.TempDir()
	second, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dir != second.Dir || first.Dir == spec.Dir {
		t.Fatalf("unstable private cwd: %q %q pane %q", first.Dir, second.Dir, spec.Dir)
	}
	entries, err := os.ReadDir(first.Dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "prompt.txt" {
		t.Fatalf("workspace has unexpected files: %v %v", entries, err)
	}
	var configs []string
	for _, launch := range []harness.Launch{first, second} {
		var config string
		for _, entry := range launch.Env {
			if strings.HasPrefix(entry, "BEARER=") {
				t.Fatal("bridge bearer entered agent environment")
			}
			if strings.HasPrefix(entry, "NOCX_PI_SESSION_FILE=") {
				config = strings.TrimPrefix(entry, "NOCX_PI_SESSION_FILE=")
			}
		}
		configs = append(configs, config)
		raw, readErr := os.ReadFile(config) // #nosec G304 -- generated private fixture path
		if readErr != nil {
			t.Fatal(readErr)
		}
		info, statErr := os.Stat(config)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("config permissions: %v %v", info, statErr)
		}
		var data struct {
			Command    []string          `json:"command"`
			Env        map[string]string `json:"env"`
			PromptFile string            `json:"promptFile"`
		}
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Env["BEARER"] != "private" || data.PromptFile != filepath.Join(first.Dir, "prompt.txt") || strings.Join(data.Command, " ") != "bridge mcp" {
			t.Fatalf("config: %+v", data)
		}
		prompt, err := os.ReadFile(data.PromptFile)
		if err != nil || string(prompt) != spec.SystemPrompt {
			t.Fatalf("prompt: %q %v", prompt, err)
		}
		info, err = os.Stat(data.PromptFile)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("prompt permissions: %v %v", info, err)
		}
		if !strings.Contains(strings.Join(launch.Argv, " "), "--session-dir "+filepath.Join(c.dataDir, "pi", "sessions")) {
			t.Fatalf("launch: %+v", launch)
		}
	}
	if configs[0] == configs[1] {
		t.Fatal("incarnations share a config file")
	}
}
