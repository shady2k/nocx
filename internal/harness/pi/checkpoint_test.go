package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestRealPiTruncatedSessionIsReportedLost(t *testing.T) {
	for _, replacement := range []string{"truncate", "same-count-other-entries"} {
		t.Run(replacement, func(t *testing.T) {
			c, fake := configuredPi(t, fakemodel.Response{Text: []string{"first answer"}}, fakemodel.Response{Text: []string{"second answer"}}, fakemodel.Response{Text: []string{"must not answer"}})
			defer func() { _ = c.Close(context.Background()) }()
			spec := harness.TurnSpec{ConversationID: "checkpoint", Model: "nocx-test/test-model", Text: "first question"}
			piSuccess(t, c, spec)
			files, err := filepath.Glob(filepath.Join(c.dataDir, "pi", "sessions", "*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("session files: %v %v", files, err)
			}
			first, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			spec.Text = "second question"
			piSuccess(t, c, spec)
			if err = c.CloseConversation(context.Background(), spec.ConversationID); err != nil {
				t.Fatal(err)
			}
			if replacement == "same-count-other-entries" {
				raw, readErr := os.ReadFile(files[0])
				if readErr != nil {
					t.Fatal(readErr)
				}
				var lines []string
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					var entry map[string]any
					if err = json.Unmarshal([]byte(line), &entry); err != nil {
						t.Fatal(err)
					}
					if entry["type"] != "session" {
						entry["id"] = fmt.Sprint("replacement-", entry["id"])
					}
					encoded, marshalErr := json.Marshal(entry)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					lines = append(lines, string(encoded))
				}
				first = []byte(strings.Join(lines, "\n") + "\n")
			}
			if err = os.WriteFile(files[0], first, 0o600); err != nil {
				t.Fatal(err)
			}
			spec.Text = "continue"
			turn := piTurn(t, c, spec)
			events := piCollect(t, turn)
			_, err = turn.Wait(context.Background())
			if err == nil || !strings.Contains(err.Error(), "lost") {
				t.Fatalf("silently resumed rolled back history: err=%v events=%+v", err, events)
			}
			if len(fake.Requests()) != 2 {
				t.Fatal("lost session reached provider")
			}
		})
	}
}

func TestCodecCheckpointsFailedAndCancelledFirstTurn(t *testing.T) {
	for _, stop := range []string{"error", "aborted"} {
		t.Run(stop, func(t *testing.T) {
			dir := t.TempDir()
			session := filepath.Join(dir, "native.jsonl")
			raw := []byte(`{"type":"session","id":"conversation"}` + "\n" + `{"type":"message","id":"completed","parentId":null,"message":{"role":"assistant","stopReason":"toolUse"}}` + "\n")
			if err := os.WriteFile(session, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			connector := New(dir, nil)
			codec := connector.NewCodec(harness.SessionInfo{ConversationID: "conversation"})
			plan := codec.BeginTurn(harness.TurnSpec{})
			if err := plan[0].Check(map[string]any{"sessionFile": session, "messageCount": float64(0)}); err != nil {
				t.Fatal(err)
			}
			codecEvents(t, codec, `{"type":"message_end","message":{"role":"assistant","stopReason":"`+stop+`","errorMessage":"stopped"}}`)
			codecEvents(t, codec, `{"type":"agent_settled"}`)
			if err := os.WriteFile(session, []byte(`{"type":"session","id":"conversation"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			next := connector.NewCodec(harness.SessionInfo{ConversationID: "conversation"}).BeginTurn(harness.TurnSpec{})
			err := next[0].Check(map[string]any{"sessionFile": session, "messageCount": float64(0)})
			if err == nil || !strings.Contains(err.Error(), "lost") {
				t.Fatalf("first %s turn left no checkpoint: %v", stop, err)
			}
		})
	}
}

func TestCodecNonemptySessionWithoutRecordIsLost(t *testing.T) {
	connector := New(t.TempDir(), nil)
	plan := connector.NewCodec(harness.SessionInfo{ConversationID: "missing-record"}).BeginTurn(harness.TurnSpec{})
	err := plan[0].Check(map[string]any{"sessionFile": "native.jsonl", "messageCount": float64(2)})
	if err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatalf("unrecorded native history resumed: %v", err)
	}
}
