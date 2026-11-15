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
	codec := c.NewCodec(harness.SessionInfo{ConversationID: spec.ConversationID})
	path := filepath.Join(launch.Dir, "prompt.txt")
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
