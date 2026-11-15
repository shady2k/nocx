package pi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	c := newTestConnector(t.TempDir(), []string{"PATH=" + t.TempDir(), "PI_OFFLINE=1"})
	got := c.Readiness(context.Background())
	if got.State != harness.NotInstalled || got.Message == "" {
		t.Fatalf("Readiness = %+v, want not installed with an explanation", got)
	}
}

func TestRealPiWithoutProviderIsNotConfigured(t *testing.T) {
	c := newTestConnector(t.TempDir(), isolatedPiEnvironment(t))
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
	got := newTestConnector(t.TempDir(), isolatedPiEnvironment(t)).Facts()
	if got.ToolBoundary != harness.NocxToolsOnly || !got.ContinuesConversationByID || !got.ReportsAnsweringModel {
		t.Fatalf("Facts = %+v", got)
	}
	// pi takes a message while it answers: its steer is queued and delivered
	// before the next model call (ADR-anntnxdj decision 4).
	if got.Steering != harness.Steers {
		t.Fatalf("Steering = %q, want %q", got.Steering, harness.Steers)
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
	return newTestConnector(t.TempDir(), env)
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
