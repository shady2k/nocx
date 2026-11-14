package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
)

func TestModelRemovedBetweenCatalogueAndAnsweringProcess(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "prompts")
	// Initial catalogue is exact. The answering process resolves a replacement.
	script := `#!/bin/sh
while IFS= read -r request; do
 case "$request" in
 *get_available_models*) printf '%s\n' '{"type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"original"}]}}' ;;
 *get_state*) printf '%s\n' '{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"replacement"}}}' ;;
 *set_model*) printf '%s\n' '{"type":"response","command":"set_model","success":true,"data":{"provider":"fixture","id":"replacement"}}' ;;
 *prompt*) printf 'prompt\n' >> "$PROMPT_LOG"; printf '%s\n' '{"type":"agent_settled"}' ;;
 esac
done
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // private executable test fixture
		t.Fatal(err)
	}
	c := New(t.TempDir(), []string{"PATH=" + dir, "PROMPT_LOG=" + log})
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "fixture/original" {
		t.Fatalf("Models = %+v %v", models, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := c.Run(ctx, harness.TurnSpec{ConversationID: "model-race", Model: "fixture/original", Text: "must not prompt replacement"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) }()
	var reason string
	for event := range turn.Events() {
		if event.Kind == harness.EventFailure {
			reason = event.Failure.Reason
		}
		if event.Kind == harness.EventEnd {
			t.Fatal("replacement answered")
		}
	}
	if !strings.Contains(reason, "model") || !strings.Contains(reason, "fixture/original") {
		t.Fatalf("failure = %q", reason)
	}
	if raw, err := os.ReadFile(log); err == nil { //nolint:gosec // private fixture log
		t.Fatalf("prompt sent to replacement: %s", raw)
	}
}

func TestModelSelectionRejectsUnavailableInAnsweringProcess(t *testing.T) {
	selection := newModelSelection("fixture/original", newDecoder(&cancelState{}))
	events, err := selection.Decode([]byte(`{"type":"response","command":"set_model","success":false,"error":"Model not found"}`))
	if err != nil || len(events) != 1 || events[0].Kind != harness.EventFailure || !strings.Contains(events[0].Failure.Reason, "fixture/original") {
		t.Fatalf("events = %+v, %v", events, err)
	}
}
