package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestRealPiUnassignedDefaultPinnedAcrossRestart(t *testing.T) {
	for _, restart := range []string{"idle", "cancel"} {
		t.Run(restart, func(t *testing.T) {
			gate := make(chan struct{})
			defer close(gate)
			responses := []fakemodel.Response{{Text: []string{"first answer"}}}
			if restart == "cancel" {
				responses = append(responses, fakemodel.Response{Block: gate})
			}
			responses = append(responses, fakemodel.Response{Text: []string{"continued answer"}}, fakemodel.Response{Text: []string{"new conversation answer"}})
			original, fake := configuredPi(t, responses...)
			agentDir := piAgentDir(original.env)
			// Both models exist before admission; only the person's default changes.
			path := filepath.Join(agentDir, "models.json")
			raw, readErr := os.ReadFile(path) //nolint:gosec // private pi fixture
			if readErr != nil {
				t.Fatal(readErr)
			}
			var config struct {
				Providers map[string]struct {
					BaseURL string           `json:"baseUrl"`
					API     string           `json:"api"`
					APIKey  string           `json:"apiKey"`
					Models  []map[string]any `json:"models"`
				} `json:"providers"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			provider := config.Providers["nocx-test"]
			second := make(map[string]any)
			for k, v := range provider.Models[0] {
				second[k] = v
			}
			second["id"] = "changed-model"
			provider.Models = append(provider.Models, second)
			config.Providers["nocx-test"] = provider
			writePiJSON(t, path, config)
			settings := filepath.Join(agentDir, "settings.json")
			writePiJSON(t, settings, map[string]string{"defaultProvider": "nocx-test", "defaultModel": "test-model"})
			observations := t.TempDir()
			installPiObserver(t, original, observations)
			base := harness.Base{}
			if restart == "idle" {
				base.IdleTimeout = 100 * time.Millisecond
			}
			c := NewWithBase(original.dataDir, original.env, base)
			defer func() { _ = c.Close(context.Background()) }()
			// An unassigned role supplies no model, on either side of the restart.
			spec := harness.TurnSpec{ConversationID: "unassigned-default", Dir: t.TempDir(), SystemPrompt: "Fixed conversation instructions", Text: "first question"}
			piSuccess(t, c, spec)
			pid := piPID(t, observations)
			writePiJSON(t, settings, map[string]string{"defaultProvider": "nocx-test", "defaultModel": "changed-model"})
			if restart == "idle" {
				waittest.WaitFor(t, "idle pi exits", func() bool { return syscall.Kill(pid, 0) != nil })
			} else {
				spec.Text = "cancel this question"
				turn := piTurn(t, c, spec)
				ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if _, err := fake.WaitRequests(ctx, 2); err != nil {
					t.Fatal(err)
				}
				if err := turn.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
				piCollect(t, turn)
				if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
					t.Fatalf("cancel: %v", err)
				}
			}
			spec.Text = "continue the conversation"
			piSuccess(t, c, spec)
			if piPID(t, observations) == pid {
				t.Fatal("restart reused pi process")
			}
			requests := fake.Requests()
			for i, r := range requests {
				if r.Model != "test-model" {
					t.Fatalf("%s request %d selected %q, want pinned test-model", restart, i, r.Model)
				}
			}
			if !piInputPrefixPreserved(requests[0], requests[len(requests)-1]) {
				t.Fatalf("%s restart lost the ordered model input prefix", restart)
			}
			// A fresh conversation proves the changed default is real and usable,
			// not merely a settings edit pi ignored.
			spec.ConversationID = "new-unassigned-default"
			spec.Text = "new conversation"
			turn := piTurn(t, c, spec)
			events := piCollect(t, turn)
			outcome, err := turn.Wait(context.Background())
			if err != nil || outcome.Model != "nocx-test/changed-model" {
				t.Fatalf("new conversation: outcome=%+v err=%v events=%+v", outcome, err, events)
			}
			requests = fake.Requests()
			if requests[len(requests)-1].Model != "changed-model" {
				t.Fatalf("new conversation ignored changed default: %s", requests[len(requests)-1].Body)
			}
		})
	}
}
