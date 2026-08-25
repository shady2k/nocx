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
	if strings.Contains(got, "What the person added") {
		t.Fatal("settings prompt includes the person's private instructions section")
	}
}
