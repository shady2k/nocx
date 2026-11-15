package harness

import (
	"context"
	"encoding/json"
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

// A message the actor never WROTE (the agent is not even past its prompt) is
// handed back at the turn's own bound: nothing is out there to change its fate.
func TestAnUnwrittenSteerIsHandedBackAtTheTurnBound(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	written := make(chan struct{}, 1)
	prompted := make(chan struct{})
	var promptedOnce sync.Once
	m := NewSessionManager(Base{
		ShutdownTimeout: 100 * time.Millisecond,
		Observe: func(transition Transition) {
			if transition.Kind == "write" && strings.Contains(string(transition.Data), "steer") {
				select {
				case written <- struct{}{}:
				default:
				}
			}
			if transition.Kind == "prompt-written" {
				promptedOnce.Do(func() { close(prompted) })
			}
		},
	}, silentPromptProtocol{})
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
		t.Fatalf("a message the turn never wrote = %v, want ErrSteerNotTaken", err)
	}
	select {
	case <-written:
		t.Fatal("the message was written to the agent, so the bound was not what answered")
	default:
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
}

// A message the actor DID write is the agent's until the agent answers it: the
// bound does not take it back, because the agent may hold it and a second copy
// asked as the next question would run the person's words twice.
func TestAWrittenSteerIsNotTakenBackByTheTurnBound(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	written := make(chan struct{}, 1)
	prompted := make(chan struct{})
	var promptedOnce sync.Once
	m := NewSessionManager(Base{
		ShutdownTimeout: 100 * time.Millisecond,
		Observe: func(transition Transition) {
			if transition.Kind == "write" && strings.Contains(string(transition.Data), "steer") {
				select {
				case written <- struct{}{}:
				default:
				}
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
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "ask this next")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	// THE BOUND IS THE SUBJECT HERE, so the wait IS the assertion: three bounds
	// passing must leave the message with the turn.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-steer:
		t.Fatalf("the turn's bound handed back a message the agent may hold: %v", err)
	default:
	}
	// The cancel settles it, once: the agent never acknowledged, and this
	// agent's cancel says nothing about what it held.
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case err := <-steer:
		if !errors.Is(err, ErrSteerNotTaken) {
			t.Fatalf("a message the agent never acknowledged = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
}

// silentPromptProtocol answers the barrier and then nothing: the turn cannot
// get past its prompt, so a message can never be written to the agent.
type silentPromptProtocol struct{ shellProtocol }

func (silentPromptProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", `while read -r line; do
case "${line#* }" in
barrier*) echo "response ${line%% *}" ;;
esac
done`}}, nil
}

func (silentPromptProtocol) NewCodec(SessionInfo, Launch) Codec { return silentPromptProtocol{} }

func (silentPromptProtocol) SteerCommand(text string) (Command, bool) {
	return Command{Name: "steer", Payload: map[string]any{"message": text}}, true
}

// droppedSteersOf reads what a cancelled turn handed back to the RUN, and fails
// the test when the turn cannot answer at all.
func droppedSteersOf(t *testing.T, turn Turn) []string {
	t.Helper()
	handback, ok := turn.(SteerHandback)
	if !ok {
		t.Fatal("a cancelled turn cannot hand back what its agent dropped")
	}
	return handback.DroppedSteers()
}

// ── I1: one disposition per message (nocx-a62cl.6.3) ──────────────────────
//
// A person's message is either CONSUMED by the agent or handed back to be asked
// as the next question — never both and never neither — whatever the turn does:
// end, cancel, the acceptance bound, or the agent exiting under it. A message
// the actor never wrote is the actor's to hand back. A message it WROTE belongs
// to the agent until the agent answers: acknowledged, it is the agent's; listed
// by the cancel's queue answer, it was never used and is handed back once; and a
// written message missing from that answer was delivered.

// queueProtocol is an agent that keeps the messages it cannot use: a steer is
// queued (and answered only when the answer file exists), and the queue-clearing
// command answers with exactly what is left, in pi's shape.
type queueProtocol struct {
	shellProtocol
	dir string
}

func (p queueProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	script := `q="` + p.dir + `/queue"; a="` + p.dir + `/answer"; d="` + p.dir + `/deliver"
f="` + p.dir + `/finish"; x="` + p.dir + `/exit"
while read -r id name text; do
case "$name" in
barrier|prompt) echo "response $id" ;;
abort) echo "response $id"; echo end ;;
steer)
  if [ ! -f "$d" ]; then printf '%s\n' "$text" >> "$q"; fi
  if [ -f "$a" ]; then echo "response $id"; fi
  [ -f "$x" ] && exit 3
  [ -f "$f" ] && echo end ;;
clear_queue)
  list=$(awk 'NF{printf "%s\"%s\"",(n++?",":""),$0}' "$q")
  echo "response $id [$list]"
  : > "$q" ;;
esac
done`
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}

func (p queueProtocol) NewCodec(SessionInfo, Launch) Codec { return p }

func (p queueProtocol) CancelSequence() []Command {
	return []Command{{Name: "clear_queue"}, {Name: "abort"}}
}

func (p queueProtocol) Encode(id string, c Command) ([]byte, error) {
	if payload, ok := c.Payload.(map[string]any); ok {
		if text, ok := payload["message"].(string); ok {
			return []byte(id + " " + c.Name + " " + text + "\n"), nil
		}
	}
	return []byte(id + " " + c.Name + "\n"), nil
}

func (p queueProtocol) SteerCommand(text string) (Command, bool) {
	return Command{Name: "steer", Payload: map[string]any{"message": text}}, true
}

func (p queueProtocol) Decode(line []byte) (Decoded, error) {
	text := string(line)
	if !strings.HasPrefix(text, "response ") {
		return Decoded{Events: []Event{{Kind: EventEnd, Outcome: &Outcome{Model: "test"}}}}, nil
	}
	rest := strings.TrimPrefix(text, "response ")
	id, payload, _ := strings.Cut(rest, " ")
	response := &Response{ID: id}
	if payload != "" {
		var texts []string
		if err := json.Unmarshal([]byte(payload), &texts); err == nil {
			response.Payload = texts
		}
	}
	return Decoded{Response: response}, nil
}

// QueueClear asks the fake agent what it still holds: the same command its
// cancel sequence starts with.
func (p queueProtocol) QueueClear() Command { return Command{Name: "clear_queue"} }

// DroppedSteers reads the fake agent's queue answer: the messages it removed.
func (p queueProtocol) DroppedSteers(payload any) ([]string, bool) {
	texts, ok := payload.([]string)
	return texts, ok
}

// steerRun starts a turn on the queue protocol and answers once its prompt is
// written — the turn is running and can take a message. written receives each
// message as it reaches the agent.
func steerRun(t *testing.T, ctx context.Context, dir string, observe func(Transition)) (Turn, <-chan string, func()) {
	t.Helper()
	prompted := make(chan struct{})
	written := make(chan string, 4)
	var once sync.Once
	m := NewSessionManager(Base{Observe: func(transition Transition) {
		if observe != nil {
			observe(transition)
		}
		switch transition.Kind {
		case "prompt-written":
			once.Do(func() { close(prompted) })
		case "write":
			if index := strings.Index(string(transition.Data), " steer "); index >= 0 {
				written <- strings.TrimSpace(string(transition.Data)[index+len(" steer "):])
			}
		}
	}}, queueProtocol{dir: dir})
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "steer", Text: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-prompted:
	case <-ctx.Done():
		t.Fatal("the turn never reached its prompt")
	}
	return turn, written, func() { _ = m.Close(ctx) }
}

// A message written to the agent and never acknowledged is not the turn's to
// give up on a timer: the agent may still hold it. The cancel's queue answer
// settles it — listed there, it was never used, and it is handed back ONCE.
func TestAWrittenUnacknowledgedSteerIsHandedBackOnceByTheQueueAnswer(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	turn, written, done := steerRun(t, ctx, t.TempDir(), nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "ask this next")
	}()
	select {
	case got := <-written:
		if got != "ask this next" {
			t.Fatalf("the agent received %q", got)
		}
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case err := <-steer:
		if !errors.Is(err, ErrSteerNotTaken) {
			t.Fatalf("a message the queue named = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
	if dropped := droppedSteersOf(t, turn); len(dropped) != 0 {
		t.Fatalf("droppedSteers = %v: the same message was handed back twice", dropped)
	}
}

// A message the queue answer does NOT list was delivered before the turn was
// stopped: the caller is told so, and nothing is asked twice.
func TestAWrittenSteerMissingFromTheQueueAnswerWasDelivered(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	dir := t.TempDir()
	// The agent used the message: it never entered the queue.
	if err := os.WriteFile(filepath.Join(dir, "deliver"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "this one arrived")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case err := <-steer:
		if err != nil {
			t.Fatalf("a delivered message = %v, want it taken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
	if dropped := droppedSteersOf(t, turn); len(dropped) != 0 {
		t.Fatalf("droppedSteers = %v, want nothing: the agent had used it", dropped)
	}
}

// A message the agent ACKNOWLEDGED and the cancel then dropped is handed back
// through the run, once: its sender was already told the turn had taken it, so
// the run's record is the only channel left.
func TestAnAcknowledgedSteerTheCancelDroppedIsListedOnce(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	dir := t.TempDir()
	// The agent answers the steer (took it) and keeps it queued (never used it).
	if err := os.WriteFile(filepath.Join(dir, "answer"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "queued and then dropped")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	select {
	case err := <-steer:
		if err != nil {
			t.Fatalf("an acknowledged message = %v, want it taken", err)
		}
	case <-ctx.Done():
		t.Fatal("the acknowledgement never came")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	dropped := droppedSteersOf(t, turn)
	if len(dropped) != 1 || dropped[0] != "queued and then dropped" {
		t.Fatalf("droppedSteers = %v, want exactly the message the agent never used", dropped)
	}
}

// ── I1 at a NORMAL end, and at the agent's exit (nocx-a62cl.6.3) ──────────
//
// A message the actor wrote is the agent's until the agent says what became of
// it — and the turn that ends normally is not the place to guess. The agent's
// own queue answer is the authority, exactly as it is at a cancel: named, the
// message was never used and is asked next; missing, it was delivered.

// The turn ends on its own while the message it was given has not been
// answered and is still queued: it is asked next, once, and the queue the agent
// was holding is emptied so nothing leaks into the next turn.
func TestATurnEndingNaturallyHandsBackWhatTheAgentStillHeld(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "finish"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "ask this next")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	// The agent settles without answering the message it was given.
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	select {
	case err := <-steer:
		if !errors.Is(err, ErrSteerNotTaken) {
			t.Fatalf("a message the agent still held = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
	if dropped := droppedSteersOf(t, turn); len(dropped) != 0 {
		t.Fatalf("droppedSteers = %v: the same message was handed back twice", dropped)
	}
	// AND IT DOES NOT LEAK: the agent's queue was emptied by the same answer
	// that named it.
	queue, err := os.ReadFile(filepath.Join(dir, "queue")) //nolint:gosec // the fixture's own directory, from t.TempDir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(queue))) != 0 {
		t.Fatalf("the agent still holds %q for the next turn", strings.TrimSpace(string(queue)))
	}
}

// The same turn, but the agent used the message and kept nothing: nothing is
// asked again.
func TestATurnEndingNaturallyKeepsWhatTheAgentUsed(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	dir := t.TempDir()
	for _, name := range []string{"finish", "deliver"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "this one arrived")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	select {
	case err := <-steer:
		if err != nil {
			t.Fatalf("a message the agent used = %v, want it taken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
	if dropped := droppedSteersOf(t, turn); len(dropped) != 0 {
		t.Fatalf("droppedSteers = %v, want nothing: the agent had used it", dropped)
	}
}

// The agent EXITS while the message it was given has not been answered: the
// agent can say nothing, so the message is handed back — a duplicate is the
// lesser harm, and losing the person's words is not (ADR-4khgzeyp decision 3).
func TestAnAgentExitingUnderAWrittenMessageHandsItBack(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "ask this next")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	if _, err := turn.Wait(ctx); err == nil {
		t.Fatal("the turn succeeded although its agent exited")
	}
	select {
	case err := <-steer:
		if !errors.Is(err, ErrSteerNotTaken) {
			t.Fatalf("a message under an exited agent = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the message was never answered")
	}
}

// The same natural end, but the agent had ACKNOWLEDGED the message and then not
// used it: its sender was already told the turn had taken it, so the run's own
// record is the only channel left, and it names it exactly once.
func TestATurnEndingNaturallyListsAnAcknowledgedMessageTheAgentKept(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	dir := t.TempDir()
	for _, name := range []string{"finish", "answer"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	turn, written, done := steerRun(t, ctx, dir, nil)
	defer done()
	steer := make(chan error, 1)
	go func() {
		steerer, ok := turn.(Steerer)
		if !ok {
			steer <- ErrNotSteerable
			return
		}
		steer <- steerer.Steer(ctx, "queued and then dropped")
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the message was never written to the agent")
	}
	select {
	case err := <-steer:
		if err != nil {
			t.Fatalf("an acknowledged message = %v, want it taken", err)
		}
	case <-ctx.Done():
		t.Fatal("the acknowledgement never came")
	}
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	dropped := droppedSteersOf(t, turn)
	if len(dropped) != 1 || dropped[0] != "queued and then dropped" {
		t.Fatalf("droppedSteers = %v, want exactly the message the agent never used", dropped)
	}
}
