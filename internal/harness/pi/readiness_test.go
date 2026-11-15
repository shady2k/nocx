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

func TestRealPiConnectorOwnsReadiness(t *testing.T) {
	c, _ := configuredPi(t)
	if got := c.Readiness(context.Background()); got.State != harness.Ready {
		t.Fatalf("configured pi readiness = %+v", got)
	}
}

func TestRealPiReadinessUsesDefaultAndChosenModel(t *testing.T) {
	c, _ := configuredPi(t)
	agentDir := ""
	for _, entry := range c.env {
		if strings.HasPrefix(entry, "PI_CODING_AGENT_DIR=") {
			agentDir = strings.TrimPrefix(entry, "PI_CODING_AGENT_DIR=")
		}
	}
	// Select a configured model's provider explicitly, not an argument-less auth probe.
	if got := c.ReadinessForModel(context.Background(), "nocx-test/test-model"); got.State != harness.Ready {
		t.Fatalf("chosen readiness = %+v", got)
	}
	settings, _ := json.Marshal(map[string]string{"defaultProvider": "nocx-test", "defaultModel": "test-model"})
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.Readiness(context.Background()); got.State != harness.Ready {
		t.Fatalf("default readiness = %+v", got)
	}
	if got := c.ReadinessForModel(context.Background(), "missing-provider/missing-model"); got.State != harness.NotConfigured {
		t.Fatalf("invalid selection = %+v", got)
	}
}

func TestReadinessAuthUsesPiSentenceAndInvalidCredentials(t *testing.T) {
	for _, tc := range []struct{ code, sentence, state, expected string }{
		{"1", "Sign in to the selected provider.", `"model":{"provider":"fixture","id":"selected"}`, "fixture/selected"},
		{"2", "broken credentials", `"model":{"provider":"fixture","id":"selected"}`, "fixture/selected"},
		{"1", "No default model.", ``, "fixture/first"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
if [ "$1" = "auth" ]; then
 printf '%s' "$4" > "$MODEL_LOG"
 printf '%s\n' '` + tc.sentence + `'
 exit ` + tc.code + `
fi
while IFS= read -r request; do
 case "$request" in
 *get_available_models*) printf '%s\n' '{"type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"first"},{"provider":"fixture","id":"selected"}]}}' ;;
 *get_state*) printf '%s\n' '{"type":"response","command":"get_state","success":true,"data":{` + tc.state + `}}' ;;
 esac
done
`
			if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // executable CLI fixture in private test directory
				t.Fatal(err)
			}
			modelLog := filepath.Join(dir, "selected-model")
			c := newTestConnector(t.TempDir(), []string{"PATH=" + dir, "MODEL_LOG=" + modelLog})
			got := c.Readiness(context.Background())
			selected, err := os.ReadFile(modelLog) //nolint:gosec // private test file
			if err != nil || string(selected) != tc.expected {
				t.Fatalf("default model selection %q: %v", selected, err)
			}
			c.ReadinessForModel(context.Background(), "fixture/first")
			selected, err = os.ReadFile(modelLog) //nolint:gosec // private test file
			if err != nil || string(selected) != "fixture/first" {
				t.Fatalf("chosen model selection %q: %v", selected, err)
			}
			if got.State != harness.NotConfigured || !strings.Contains(got.Message, tc.sentence) {
				t.Fatalf("readiness = %+v", got)
			}
			if tc.code == "2" && !strings.Contains(got.Message, "credentials are invalid") {
				t.Fatalf("invalid readiness = %+v", got)
			}
		})
	}
}
