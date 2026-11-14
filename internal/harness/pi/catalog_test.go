package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func agentConfigDir(c *Connector) string {
	for _, entry := range c.env {
		if strings.HasPrefix(entry, "PI_CODING_AGENT_DIR=") {
			return strings.TrimPrefix(entry, "PI_CODING_AGENT_DIR=")
		}
	}
	return ""
}

func TestRunRefusesRemovedOrSimilarlyNamedModel(t *testing.T) {
	c, fake := configuredPi(t, fakemodel.Response{Text: []string{"wrong model"}})
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentConfigDir(c), "models.json")
	raw, err := os.ReadFile(path) //nolint:gosec // private test configuration
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
	var models []map[string]json.RawMessage
	if err = json.Unmarshal(provider["models"], &models); err != nil {
		t.Fatal(err)
	}
	models[0]["id"] = json.RawMessage(`"test-model-replacement"`)
	provider["models"], err = json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	turn, err := c.Run(context.Background(), harness.TurnSpec{ConversationID: "removed", Text: "question", Model: "nocx-test/test-model", Dir: t.TempDir()})
	if err == nil {
		_ = turn.Cancel(context.Background())
		_, _ = turn.Wait(context.Background())
		t.Fatal("Run accepted removed model through fuzzy matching")
	}
	if !strings.Contains(err.Error(), "not in pi's catalogue") {
		t.Fatalf("Run error = %v", err)
	}
	if len(fake.Requests()) != 0 {
		t.Fatal("removed model reached provider")
	}
}

func TestReadinessAndModelsShareCatalogue(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "launches")
	script := `#!/bin/sh
if [ "$1" = "auth" ]; then
 [ "$5" = "--no-refresh" ] || exit 2
 exit 0
fi
printf 'launch\n' >> "$LAUNCH_LOG"
while IFS= read -r request; do
 case "$request" in
 *get_available_models*) printf '%s\n' '{"type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"one"}]}}' ;;
 *get_state*) printf '%s\n' '{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"one"}}}' ;;
 esac
done
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // executable fixture in private test directory
		t.Fatal(err)
	}
	c := New(t.TempDir(), []string{"PATH=" + dir, "LAUNCH_LOG=" + log})
	if ready := c.Readiness(context.Background()); ready.State != harness.Ready {
		t.Fatalf("Readiness = %+v", ready)
	}
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("Models = %+v, %v", models, err)
	}
	models[0].ID = "caller mutation"
	models, err = c.Models(context.Background())
	if err != nil || models[0].ID != "fixture/one" {
		t.Fatalf("shared catalogue mutated: %+v %v", models, err)
	}
	raw, err := os.ReadFile(log) //nolint:gosec // private fixture launch log
	if err != nil || string(raw) != "launch\n" {
		t.Fatalf("catalogue process launches = %q, %v", raw, err)
	}
}

func TestCatalogueCacheWaitHonorsDeadline(t *testing.T) {
	c := New(t.TempDir(), nil)
	c.catalogGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.catalog(ctx); err != context.Canceled {
		t.Fatalf("catalog = %v", err)
	}
	<-c.catalogGate
}
