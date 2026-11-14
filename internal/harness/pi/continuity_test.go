package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestRealPiMissingSessionIsReportedLost(t *testing.T) { testRealPiSessionLost(t, false) }
func TestRealPiCorruptSessionIsReportedLost(t *testing.T) { testRealPiSessionLost(t, true) }

func testRealPiSessionLost(t *testing.T, corrupt bool) {
	t.Helper()
	c, fake := configuredPi(t, fakemodel.Response{Text: []string{"remembered answer"}}, fakemodel.Response{Text: []string{"must not answer without history"}})
	defer func() { _ = c.Close(context.Background()) }()
	spec := harness.TurnSpec{ConversationID: "lost-conversation", Dir: t.TempDir(), Model: "nocx-test/test-model", Text: "remember this question"}
	piSuccess(t, c, spec)
	if err := c.CloseConversation(context.Background(), spec.ConversationID); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(c.dataDir, "pi", "sessions")
	var removed int
	if err := filepath.WalkDir(sessions, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		removed++
		if corrupt {
			return os.WriteFile(path, []byte("not a pi session\n"), 0o600)
		}
		return os.Remove(path)
	}); err != nil {
		t.Fatal(err)
	}
	if removed == 0 {
		t.Fatal("no native session files to remove")
	}
	spec.Text = "continue the remembered question"
	turn, err := c.Run(context.Background(), spec)
	if err == nil {
		events := piCollect(t, turn)
		_, err = turn.Wait(context.Background())
		if err == nil {
			t.Fatalf("silently answered a lost session: %+v", events)
		}
		if len(events) != 1 || events[0].Kind != harness.EventFailure {
			t.Fatalf("lost session must fail without deltas: %+v", events)
		}
	}
	if !strings.Contains(strings.ToLower(err.Error()), "lost") {
		t.Fatalf("session loss must be explicit: %v", err)
	}
	if requests := fake.Requests(); len(requests) != 1 {
		t.Fatalf("lost session reached provider: %d requests", len(requests))
	}
}
