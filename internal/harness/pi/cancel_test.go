package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestRealPiCancelToolPersistsResultForContinuation(t *testing.T) {
	for _, method := range []string{"cancel", "context"} {
		t.Run(method, func(t *testing.T) {
			c, fake := configuredPi(t,
				fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "cancelled-probe", Name: "mcp__nocx__probe", Arguments: `{}`}}},
				fakemodel.Response{Text: []string{"continued after cancellation"}},
			)
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "pids")
			spec := harness.TurnSpec{
				ConversationID: "cancelled-tool", Text: "use the probe", Model: "nocx-test/test-model", Dir: dir,
				Tools: harness.ToolBridge{Command: []string{bin, "-test.run=^TestPiMCPHelper$"}, Env: []string{"PI_TEST_MCP_HELPER=1", "PI_TEST_PIDFILE=" + pidfile, "PI_TEST_BLOCK=1"}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			runCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			turn, err := c.Run(runCtx, spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) })
			if _, err = fake.WaitRequests(ctx, 1); err != nil {
				t.Fatal(err)
			}
			waittest.WaitFor(t, "blocked tool reached bridge", func() bool { _, statErr := os.Stat(pidfile + ".blocked"); return statErr == nil })
			if method == "cancel" {
				if err = turn.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				cancelRun()
			}
			var terminal int
			for event := range turn.Events() {
				if event.Kind == harness.EventEnd {
					t.Fatal("cancelled tool reported success")
				}
				if event.Kind == harness.EventFailure {
					terminal++
					if event.Failure == nil || event.Failure.Reason != "cancelled" {
						t.Fatalf("cancellation failure = %+v", event)
					}
				}
			}
			if terminal != 1 {
				t.Fatalf("terminal failures = %d", terminal)
			}
			if _, err = turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
				t.Fatalf("Wait = %v", err)
			}
			// Inspect disk before a new pi process can repair an interrupted call.
			// Continuation alone is insufficient: pi synthesizes missing results.
			var persistedResult bool
			err = filepath.WalkDir(filepath.Join(c.dataDir, "pi", "sessions"), func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				raw, err := os.ReadFile(path) //nolint:gosec // session file in private test directory
				if err != nil {
					return err
				}
				for _, line := range strings.Split(string(raw), "\n") {
					var record struct {
						Message struct {
							Role       string `json:"role"`
							ToolCallID string `json:"toolCallId"`
						} `json:"message"`
					}
					if json.Unmarshal([]byte(line), &record) == nil && record.Message.Role == "toolResult" && strings.HasPrefix(record.Message.ToolCallID, "cancelled-probe") {
						persistedResult = true
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !persistedResult {
				t.Fatal("cancelled tool result was not persisted before continuation")
			}
			c = New(c.dataDir, c.env)
			spec.Text = "continue after the cancelled tool"
			runRealPi(t, c, spec)
			requests := fake.Requests()
			if len(requests) != 2 {
				t.Fatalf("provider requests = %d", len(requests))
			}
			var sawCall, sawResult bool
			for _, raw := range requests[1].Messages {
				var message struct {
					Role       string `json:"role"`
					ToolCallID string `json:"tool_call_id"`
					ToolCalls  []struct {
						ID string `json:"id"`
					} `json:"tool_calls"`
				}
				if err := json.Unmarshal(raw, &message); err != nil {
					t.Fatal(err)
				}
				for _, call := range message.ToolCalls {
					if call.ID == "cancelled-probe" {
						sawCall = true
					}
				}
				if message.Role == "tool" && strings.HasPrefix(message.ToolCallID, "cancelled-probe") {
					sawResult = true
				}
			}
			if !sawCall || !sawResult {
				t.Fatalf("continuation lost cancelled call/result (call=%v result=%v): %s", sawCall, sawResult, requests[1].Body)
			}
		})
	}
}
