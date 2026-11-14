package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestTurnFilesStableWorkspaceAndPrivateConfig(t *testing.T) {
	c := New(t.TempDir(), nil)
	spec := harness.TurnSpec{ConversationID: "conversation", Dir: t.TempDir(), SystemPrompt: "stable", Tools: harness.ToolBridge{Env: []string{"SECRET=bearer"}}}
	a, err := c.prepareTurn(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.cleanup() }()
	spec.Dir = t.TempDir()
	b, err := c.prepareTurn(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.cleanup() }()
	if a.dir != b.dir || a.dir == spec.Dir {
		t.Fatalf("workspace changed: %q %q", a.dir, b.dir)
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace not empty: %v %v", entries, err)
	}
	info, err := os.Stat(a.configPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private config: %v %v", info, err)
	}
	spec.ConversationID = "other"
	other, err := c.prepareTurn(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.cleanup() }()
	if other.dir == a.dir {
		t.Fatal("conversations share workspace")
	}
}

func TestSweepTurnFilesPreservesLiveUnreadConfig(t *testing.T) {
	c := New(t.TempDir(), nil)
	live, err := c.prepareTurn(harness.TurnSpec{ConversationID: "live"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.cleanup() }()
	stale, err := c.prepareTurn(harness.TurnSpec{ConversationID: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a killed backend: the kernel closes its lock without cleanup.
	if err := stale.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.sweepTurnFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale.configPath); !os.IsNotExist(err) {
		t.Fatalf("stale secret retained: %v", err)
	}
	if _, err := os.Stat(live.configPath); err != nil {
		t.Fatalf("live unread config removed: %v", err)
	}
}

func TestSweepTurnFilesPreservesChildAfterBackendLockCloses(t *testing.T) {
	c := New(t.TempDir(), nil)
	files, err := c.prepareTurn(harness.TurnSpec{ConversationID: "surviving-child"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.cleanup() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	turn, err := (harness.Base{}).Start(ctx, harness.Launch{
		Argv:       []string{"/bin/sh", "-c", `read -r ready; cat "$1" "$2" >/dev/null || exit 1; echo consumed`, "pi-startup", files.extensionPath, files.configPath},
		ExtraFiles: []*os.File{files.lock},
		Decode: func([]byte) ([]harness.Event, error) {
			return []harness.Event{{Kind: harness.EventEnd, End: &harness.End{Model: "test"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) }()
	// Backend death closes its descriptor, but the child still needs both files.
	if err := files.lock.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := New(c.dataDir, nil)
	if restarted.stateErr != nil {
		t.Fatal(restarted.stateErr)
	}
	for _, path := range []string{files.extensionPath, files.configPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("restart removed unread child file: %v", err)
		}
	}
	if _, err := turn.Stdin().Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.sweepTurnFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(files.configPath)); !os.IsNotExist(err) {
		t.Fatalf("exited child's turn files retained: %v", err)
	}
}

func TestTurnFilesCleanupRemovesSecrets(t *testing.T) {
	c := New(t.TempDir(), nil)
	files, err := c.prepareTurn(harness.TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := files.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := files.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(files.configPath)); !os.IsNotExist(err) {
		t.Fatalf("turn files retained: %v", err)
	}
}

func TestTurnFilesCleanupPreservesError(t *testing.T) {
	c := New(t.TempDir(), nil)
	files, err := c.prepareTurn(harness.TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := files.lock.Close(); err != nil {
		t.Fatal(err)
	}
	first := files.cleanup()
	if first == nil {
		t.Fatal("cleanup discarded close error")
	}
	if again := files.cleanup(); again != first {
		t.Fatalf("cleanup error changed: %v then %v", first, again)
	}
	if _, err := os.Stat(filepath.Dir(files.configPath)); !os.IsNotExist(err) {
		t.Fatalf("close error prevented file removal: %v", err)
	}
}

func TestRealPiStablePrefixAcrossPaneDirectoriesAndToolCycles(t *testing.T) {
	c, fake := configuredPi(t,
		fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "probe-one", Name: "mcp__nocx__probe", Arguments: `{"note":"one"}`}}},
		fakemodel.Response{Text: []string{"first answer"}},
		fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "probe-two", Name: "mcp__nocx__probe", Arguments: `{"note":"two"}`}}},
		fakemodel.Response{Text: []string{"second answer"}},
	)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := harness.TurnSpec{ConversationID: "stable-prefix", Model: "nocx-test/test-model", SystemPrompt: "Nocx stable prefix instructions.", Text: "first", Dir: t.TempDir(), Tools: harness.ToolBridge{Command: []string{bin, "-test.run=^TestPiMCPHelper$"}, Env: []string{"PI_TEST_MCP_HELPER=1", "NOCX_TEST_SECRET=first-bridge"}}}
	runRealPi(t, c, spec)
	spec.Dir = t.TempDir()
	spec.Text = "second"
	spec.Tools.Env = []string{"PI_TEST_MCP_HELPER=1", "NOCX_TEST_SECRET=second-bridge"}
	runRealPi(t, c, spec)
	requests := fake.Requests()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	var prefix []byte
	for i, request := range requests {
		var system []byte
		for _, raw := range request.Messages {
			var message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Fatal(err)
			}
			if message.Role == "system" || message.Role == "developer" {
				system = append(system, message.Content...)
			}
		}
		if !bytes.Contains(system, []byte(spec.SystemPrompt)) {
			t.Fatalf("request %d omitted instructions", i)
		}
		if i == 0 {
			prefix = system
		} else if !bytes.Equal(prefix, system) {
			t.Fatalf("system prefix changed at request %d:\n%s\n%s", i, prefix, system)
		}
		if bytes.Contains(system, []byte("first-bridge")) || bytes.Contains(system, []byte("second-bridge")) {
			t.Fatal("bridge credentials in system prompt")
		}
	}
	if !bytes.Contains(requests[1].Body, []byte("server_secret=first-bridge")) || !bytes.Contains(requests[3].Body, []byte("server_secret=second-bridge")) {
		t.Fatal("separate bridge instances did not execute")
	}
}

func TestRealPiConsumesConfigBeforeWait(t *testing.T) {
	c, fake := configuredPi(t, fakemodel.Response{Block: make(chan struct{})})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Run(ctx, harness.TurnSpec{ConversationID: "consume-config", Model: "nocx-test/test-model", Text: "block", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = turn.Cancel(context.Background()); _, _ = turn.Wait(context.Background()) }()
	if _, requestErr := fake.WaitRequests(ctx, 1); requestErr != nil {
		t.Fatal(requestErr)
	}
	files, err := filepath.Glob(filepath.Join(c.dataDir, "pi", "turns", "turn-*", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("bearer config retained without Wait: %v", files)
	}
}
