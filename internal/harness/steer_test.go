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

// slowStartProtocol is an agent that takes its time coming up: the window in
// which a person has sent a message and no turn is running yet.
type slowStartProtocol struct {
	shellProtocol
	started chan struct{}
	release chan struct{}
}

func (p slowStartProtocol) Start(ctx context.Context, spec TurnSpec) (Launch, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return Launch{}, ctx.Err()
	}
	return p.shellProtocol.Start(ctx, spec)
}

func (p slowStartProtocol) NewCodec(SessionInfo, Launch) Codec { return p }

// A message sent while the agent is still starting is QUEUED; a cancel before
// the agent takes it hands it back to the caller, which asks it as the
// conversation's next question (ADR-4khgzeyp decision 3).
func TestSteerQueuedWhenTheTurnIsCancelledIsHandedBack(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	protocol := slowStartProtocol{started: make(chan struct{}), release: make(chan struct{})}
	queued := make(chan struct{})
	var once sync.Once
	m := NewSessionManager(Base{
		Observe: func(transition Transition) {
			if transition.Kind == "steer-queued" {
				once.Do(func() { close(queued) })
			}
		},
	}, protocol)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "handback", Text: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-protocol.started:
	case <-ctx.Done():
		t.Fatal("the agent never started coming up")
	}
	steerer, ok := turn.(Steerer)
	if !ok {
		t.Fatal("the turn has no Steer")
	}
	steered := make(chan error, 1)
	go func() { steered <- steerer.Steer(ctx, "ask this next") }()
	// The message is in the actor's queue and the agent has not seen it: the
	// cancel below is about a turn that had already taken it.
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("the message was never queued")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case err := <-steered:
		if !errors.Is(err, ErrSteerNotTaken) {
			t.Fatalf("a message the cancelled turn never took = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the queued message was never answered")
	}
}

// noSteerAnswerProtocol takes the steer command and never answers it: the agent
// has stopped moving. The turn's own bound hands the message back instead of
// holding the caller for ever (nocx-a62cl.6.3).
type noSteerAnswerProtocol struct {
	shellProtocol
}

func (p noSteerAnswerProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	script := `while read -r line; do
case "${line#* }" in
barrier* | prompt*) echo "response ${line%% *}";;
steer*) : ;;
esac
done`
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}

func (p noSteerAnswerProtocol) NewCodec(SessionInfo, Launch) Codec { return p }

func (p noSteerAnswerProtocol) SteerCommand(text string) (Command, bool) {
	return Command{Name: "steer", Payload: map[string]any{"message": text}}, true
}

func TestSteerNotAcceptedWithinTheTurnBoundIsHandedBack(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	written := make(chan struct{})
	prompted := make(chan struct{})
	var once, promptedOnce sync.Once
	m := NewSessionManager(Base{
		ShutdownTimeout: 100 * time.Millisecond,
		Observe: func(transition Transition) {
			if transition.Kind == "write" && strings.Contains(string(transition.Data), "steer") {
				once.Do(func() { close(written) })
			}
			if transition.Kind == "prompt-written" {
				promptedOnce.Do(func() { close(prompted) })
			}
		},
	}, noSteerAnswerProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "silent", Text: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-prompted:
	case <-ctx.Done():
		t.Fatal("the turn never reached its prompt")
	}
	steerer, ok := turn.(Steerer)
	if !ok {
		t.Fatal("the turn has no Steer")
	}
	if err := steerer.Steer(ctx, "ask this next"); !errors.Is(err, ErrSteerNotTaken) {
		t.Fatalf("a message the agent never accepted = %v, want ErrSteerNotTaken", err)
	}
	select {
	case <-written:
	default:
		t.Fatal("the message was never written to the agent, so the bound was not what answered")
	}
	// The turn itself is not killed by a message the agent ignored.
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
}
