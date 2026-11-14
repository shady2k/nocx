package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/shellenv"
)

// The base starts the agent with the login shell's environment, not the
// backend's own: a program launched from the desktop has launchd's bare PATH,
// and the person's shell is where the tools are.
func TestLoginEnvironmentCarriesPath(t *testing.T) {
	env, err := loginEnvironment(context.Background())
	if err != nil {
		t.Fatalf("loginEnvironment: %v", err)
	}
	if _, ok := shellenv.Path(env); !ok {
		t.Fatalf("the login environment has no PATH: %v", env)
	}
}

// A program that never sends a newline must not grow the backend's memory. The
// turn fails with the bound's reason instead.
func TestBaseBoundsStdoutLines(t *testing.T) {
	line := strings.Repeat("a", 200)
	turn, err := (Base{MaxLineBytes: 64}).Start(context.Background(), Launch{
		Argv: []string{"/bin/sh", "-c", "printf '%s\\n' '" + line + "'"},
		Decode: func(raw []byte) ([]Event, error) {
			return []Event{{Kind: EventAnswerDelta, Text: string(raw)}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var events []Event
	for ev := range turn.Events() {
		events = append(events, ev)
	}
	if len(events) != 1 || events[0].Kind != EventFailure ||
		!strings.Contains(events[0].Failure.Reason, "longer than 64 bytes") {
		t.Fatalf("events = %+v, want one failure naming the bound", events)
	}
	if _, err := turn.Wait(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "longer than 64 bytes") {
		t.Fatalf("Wait error = %v", err)
	}
}
