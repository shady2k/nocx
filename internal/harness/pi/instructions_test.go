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

func TestRealPiPersonalInstructionsUpdateEachRun(t *testing.T) {
	c, fake := configuredPi(t, fakemodel.Response{Text: []string{"one"}}, fakemodel.Response{Text: []string{"two"}}, fakemodel.Response{Text: []string{"three"}})
	defer func() { _ = c.Close(context.Background()) }()
	observations := t.TempDir()
	installPiObserver(t, c, observations)
	spec := harness.TurnSpec{ConversationID: "instruction-update", Model: "nocx-test/test-model", SystemPrompt: "Fixed nocx instructions. Personal: old-731", Text: "first question", Dir: t.TempDir()}
	piSuccess(t, c, spec)
	pid := piPID(t, observations)
	first := piRequestPrompt(t, fake, observations)
	spec.Text = "second question"
	piSuccess(t, c, spec)
	second := piRequestPrompt(t, fake, observations)
	if first.SystemPrompt != second.SystemPrompt {
		t.Fatal("unchanged instructions changed system prompt bytes")
	}
	spec.SystemPrompt = "Fixed nocx instructions. Personal: new-842"
	spec.Text = "third question"
	piSuccess(t, c, spec)
	third := piRequestPrompt(t, fake, observations)
	if piPID(t, observations) != pid {
		t.Fatal("instructions update restarted pi")
	}
	if strings.Contains(third.SystemPrompt, "old-731") || strings.Count(third.SystemPrompt, spec.SystemPrompt) != 1 {
		t.Fatalf("updated instructions not appended exactly once: %s", third.SystemPrompt)
	}
	if strings.Replace(first.SystemPrompt, "old-731", "new-842", 1) != third.SystemPrompt {
		t.Fatal("instruction update changed the agent's system prompt")
	}
}

func TestPromptFileRewrittenBeforeEachPromptAndRemovedOnCleanup(t *testing.T) {
	c := newTestConnector(t.TempDir(), nil)
	spec := harness.TurnSpec{ConversationID: "private-prompt", SystemPrompt: "old instructions"}
	launch, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	codec := c.NewCodec(harness.SessionInfo{ConversationID: spec.ConversationID}, launch)
	path := launch.PromptPath
	for _, instructions := range []string{"old instructions", "new instructions", "new instructions"} {
		spec.SystemPrompt = instructions
		plan := codec.BeginTurn(spec)
		// An unchanged run must still rewrite, rather than trusting the launch copy.
		if err := os.WriteFile(path, []byte("stale content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := codec.Encode("prompt", plan[len(plan)-1].Command); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- test-owned session workspace
		if err != nil || string(raw) != instructions {
			t.Fatalf("prompt bytes: %q %v", raw, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("prompt permissions: %v %v", info, err)
		}
	}
	if err := launch.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("prompt file survives session cleanup: %v", err)
	}
	if err := launch.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPromptWriteFailurePreventsPromptEncoding(t *testing.T) {
	c := &codec{promptPath: filepath.Join(t.TempDir(), "missing", "prompt.txt")}
	plan := c.BeginTurn(harness.TurnSpec{SystemPrompt: "instructions"})
	raw, err := c.Encode("prompt", plan[len(plan)-1].Command)
	if err == nil || raw != nil {
		t.Fatalf("failed prompt write produced command: %s %v", raw, err)
	}
}

// A cancelled startup may finish disposing only after its replacement has
// encoded a prompt. Order these boundaries explicitly, without timing sleeps.
func TestOverlappingIncarnationsKeepTheirOwnPrompt(t *testing.T) {
	c := newTestConnector(t.TempDir(), nil)
	spec := harness.TurnSpec{ConversationID: "overlapping", SystemPrompt: "old instructions"}
	old, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Cleanup() })
	oldCodec := c.NewCodec(harness.SessionInfo{ConversationID: spec.ConversationID}, old)
	spec.SystemPrompt = "new instructions intact"
	next, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Cleanup() })
	nextCodec := c.NewCodec(harness.SessionInfo{ConversationID: spec.ConversationID}, next)
	promptFile := func(launch harness.Launch) string {
		t.Helper()
		for _, entry := range launch.Env {
			if path, ok := strings.CutPrefix(entry, "NOCX_PI_SESSION_FILE="); ok {
				raw, readErr := os.ReadFile(path) //nolint:gosec // generated config in isolated test directory
				if readErr != nil {
					t.Fatal(readErr)
				}
				var config struct {
					PromptFile string `json:"promptFile"`
				}
				if decodeErr := json.Unmarshal(raw, &config); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				return config.PromptFile
			}
		}
		t.Fatal("no extension config")
		return ""
	}
	oldPath, nextPath := promptFile(old), promptFile(next)
	encode := func(codec harness.Codec, instructions string) {
		t.Helper()
		plan := codec.BeginTurn(harness.TurnSpec{SystemPrompt: instructions})
		if _, encodeErr := codec.Encode("prompt", plan[len(plan)-1].Command); encodeErr != nil {
			t.Fatal(encodeErr)
		}
	}
	encode(nextCodec, spec.SystemPrompt)
	// A late old write must not replace what the new extension will read.
	encode(oldCodec, "late old instructions")
	raw, err := os.ReadFile(nextPath) //nolint:gosec // generated prompt in isolated test directory
	if err != nil || string(raw) != spec.SystemPrompt {
		t.Fatalf("replacement prompt after old write: %q %v", raw, err)
	}
	if err = old.Cleanup(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(nextPath) //nolint:gosec // generated prompt in isolated test directory
	if err != nil || string(raw) != spec.SystemPrompt {
		t.Fatalf("replacement prompt after old cleanup: %q %v", raw, err)
	}
	if oldPath == nextPath {
		t.Fatal("incarnations share prompt path")
	}
}
