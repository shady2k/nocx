package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// steerProtocol is a shell agent that holds its turn open after the prompt and
// writes every command it receives to a file, so a test can see what reached
// the agent and in which order.
type steerProtocol struct {
	shellProtocol
	// record is the file the agent appends each command's name and text to.
	record string
}

func (p steerProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	script := `while read -r line; do
printf '%s\n' "$line" >> "` + p.record + `"
case "${line#* }" in
barrier* | prompt*) echo "response ${line%% *}";;
steer*)
  echo "response ${line%% *}"
  echo end
  ;;
esac
done`
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}

// NewCodec returns THIS protocol, not the embedded fixture: the codec's
// SteerCommand is what tells the actor the agent steers.
func (p steerProtocol) NewCodec(SessionInfo, Launch) Codec { return p }

func (p steerProtocol) Encode(id string, c Command) ([]byte, error) {
	if c.Name == "steer" {
		payload, _ := c.Payload.(map[string]any)
		text, _ := payload["message"].(string)
		return []byte(id + " steer " + text + "\n"), nil
	}
	return []byte(id + " " + c.Name + "\n"), nil
}

func (p steerProtocol) SteerCommand(text string) (Command, bool) {
	return Command{Name: "steer", Payload: map[string]any{"message": text}}, true
}

type deafSteerProtocol struct{ shellProtocol }

func (deafSteerProtocol) SteerCommand(string) (Command, bool) { return Command{}, false }

// A person's message reaches the agent while the turn runs, in order after the
// prompt, and the steer's own answer is the acceptance the caller waits for.
func TestSteerReachesTheAgentOfTheRunningTurn(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	record := filepath.Join(t.TempDir(), "commands")
	written := make(chan struct{})
	var once sync.Once
	m := NewSessionManager(Base{
		Observe: func(transition Transition) {
			if transition.Kind == "prompt-written" {
				once.Do(func() { close(written) })
			}
		},
	}, steerProtocol{record: record})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "steer", Text: "first question"})
	_ = turn
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the turn never reached its prompt")
	}
	steerer, ok := turn.(Steerer)
	if !ok {
		t.Fatal("an agent that steers has no Steer on its turn")
	}
	if err := steerer.Steer(ctx, "check the second log too"); err != nil {
		t.Fatalf("steer into a running turn: %v", err)
	}
	raw, readErr := os.ReadFile(record) //nolint:gosec // test fixture in t.TempDir
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[1], " prompt") || !strings.HasSuffix(lines[2], " steer check the second log too") {
		t.Fatalf("agent received %q, want the prompt then the person's message while the turn ran", lines)
	}
	// The message did not end the turn: the agent's own end did, after it.
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}

// A message with no turn to land in is refused, and the refusal names the
// turn rather than silently dropping what the person typed.
func TestSteerAfterTheTurnEndedIsRefused(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	m := NewSessionManager(Base{}, shellProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "ended"})
	if err != nil {
		t.Fatal(err)
	}
	if _, waitErr := turn.Wait(ctx); waitErr != nil {
		t.Fatalf("turn: %v", waitErr)
	}
	steerer, ok := turn.(Steerer)
	if !ok {
		t.Fatal("an agent that steers has no Steer on its turn")
	}
	err = steerer.Steer(ctx, "are you still there?")
	if !errors.Is(err, ErrTurnNotRunning) || !strings.Contains(err.Error(), "turn") {
		t.Fatalf("steer after the turn ended = %v, want ErrTurnNotRunning", err)
	}
}

// An agent whose protocol has no steering says so instead of being fed a
// command it cannot read.
func TestSteerIsRefusedByAnAgentWithoutSteering(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	written := make(chan struct{})
	var once sync.Once
	m := NewSessionManager(Base{
		Observe: func(transition Transition) {
			if transition.Kind == "prompt-written" {
				once.Do(func() { close(written) })
			}
		},
	}, deafSteerProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "deaf"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the turn never reached its prompt")
	}
	steerer, ok := turn.(Steerer)
	if !ok {
		t.Fatal("the turn has no Steer")
	}
	err = steerer.Steer(ctx, "hello?")
	if !errors.Is(err, ErrNotSteerable) {
		t.Fatalf("steer to an agent without steering = %v, want ErrNotSteerable", err)
	}
	_ = turn.Cancel(ctx)
	_, _ = turn.Wait(ctx)
}
