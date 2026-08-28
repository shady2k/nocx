package assistant

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSettingsSystemPromptArtifactMatchesPrompt(t *testing.T) {
	artifactJSON, err := os.ReadFile("../../frontend/src/systemprompt.json")
	if err != nil {
		t.Fatalf("read settings prompt artifact: %v", err)
	}
	var artifact string
	if err := json.Unmarshal(artifactJSON, &artifact); err != nil {
		t.Fatalf("decode settings prompt artifact: %v", err)
	}

	got := SettingsSystemPrompt()
	if artifact != got {
		t.Fatalf("settings prompt artifact drifted from SystemPrompt:\nartifact:\n%s\nsource:\n%s", artifact, got)
	}
	for _, placeholder := range []string{
		"<session id>",
		"<working directory>",
		"<local shell or ssh session>",
		"<host or local machine>",
		"<attached or absent>",
	} {
		if !strings.Contains(got, placeholder) {
			t.Errorf("settings prompt lacks placeholder %q:\n%s", placeholder, got)
		}
	}
	for _, example := range []string{
		"- id: <item id>; state: <running or exited>; command: \"<command>\"",
		"- id: <row item id>; state: <running or exited>; start: 2; count: 4; command: \"<row command>\"",
	} {
		if !strings.Contains(got, example) {
			t.Errorf("settings prompt lacks mark example %q:\n%s", example, got)
		}
	}
	for _, old := range []string{
		"<operating system>",
		"- id: <item id>; command: <command>; state: <running or exited>",
		"- id: <row item id>; command: <row command>; state: <running or exited>; start: 2; count: 4",
	} {
		if strings.Contains(got, old) {
			t.Errorf("SettingsSystemPrompt retained source-only anchor %q:\n%s", old, got)
		}
	}
	for _, replacement := range []string{
		"This pane is a <local shell or ssh session> on <host or local machine>.\n",
		"Terminal content: <attached or absent>.\n",
	} {
		if !strings.Contains(got, replacement) {
			t.Errorf("SettingsSystemPrompt lacks exact replacement %q:\n%s", replacement, got)
		}
	}
	if strings.Contains(got, "What the person added") {
		t.Fatal("settings prompt includes the person's private instructions section")
	}
}
