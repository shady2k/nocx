package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The fixture observes actual commands in a child process. It sends the wrong
// response ID first; only a matched response may run the payload check.
type planProtocol struct {
	script         string
	sessions       chan SessionInfo
	checks         chan string
	reject         bool
	barrierFixture bool
}

func (p *planProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", p.script}}, nil
}

func (p *planProtocol) NewCodec(info SessionInfo) Codec {
	p.sessions <- info
	if p.barrierFixture {
		return &barrierCodec{}
	}
	return &planCodec{checks: p.checks, reject: p.reject}
}

type planCodec struct {
	checks       chan string
	reject       bool
	ready        bool
	stateChecked bool
}

func (c *planCodec) Setup() []Step {
	return []Step{{Command: Command{Name: "setup"}, Check: func(value any) error {
		if value != "ready" {
			return fmt.Errorf("setup payload: %v", value)
		}
		c.checks <- "setup"
		if c.reject {
			return errors.New("setup rejected")
		}
		c.ready = true
		return nil
	}}}
}

func (c *planCodec) BeginTurn(TurnSpec) []Step {
	c.stateChecked = false
	return []Step{
		{Command: Command{Name: "state"}, Barrier: true, Check: func(value any) error {
			if !c.ready || value != "state" {
				return fmt.Errorf("state before setup or bad payload: %v", value)
			}
			c.stateChecked = true
			c.checks <- "state"
			return nil
		}},
		{Command: Command{Name: "prompt"}, Prompt: true},
	}
}
func (*planCodec) CancelSequence() []Command { return []Command{{Name: "abort"}} }
func (c *planCodec) Encode(id string, command Command) ([]byte, error) {
	if command.Name == "state" && !c.ready {
		return nil, errors.New("state encoded before setup response")
	}
	if command.Name == "prompt" && !c.stateChecked {
		return nil, errors.New("prompt encoded before state response")
	}
	return []byte(id + " " + command.Name + "\n"), nil
}

func (*planCodec) Decode(line []byte) (Decoded, error) {
	fields := strings.Fields(string(line))
	if len(fields) == 3 && fields[0] == "response" {
		return Decoded{Response: &Response{ID: fields[1], Payload: fields[2]}}, nil
	}
	if len(fields) > 0 && fields[0] == "end" {
		model := "plan"
		if len(fields) == 2 {
			model = fields[1]
		}
		return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: model}}}}, nil
	}
	return Decoded{}, fmt.Errorf("bad fixture line: %q", line)
}

const planScript = `while read id command; do
 case "$command" in
 setup) echo "response wrong ignored"; echo "response $id ready" ;;
 state) echo "response $id state" ;;
 prompt) echo "response $id accepted"; echo end ;;
 abort) echo "response $id accepted" ;;
 esac
done`

func TestPlanChecksMatchedPayloadBeforeNextCommand(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	p := &planProtocol{script: planScript, sessions: make(chan SessionInfo, 8), checks: make(chan string, 8)}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "one"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := turn.Wait(ctx)
		if err != nil || result.Model != "plan" {
			t.Fatalf("Wait=%+v, %v", result, err)
		}
	}
	if got := len(p.sessions); got != 1 {
		t.Fatalf("codec creations=%d, want one for reused process", got)
	}
	info := <-p.sessions
	if info.ConversationID != "one" || info.Generation == 0 {
		t.Fatalf("session=%+v", info)
	}
	if len(p.checks) != 3 {
		t.Fatalf("checks=%d, want setup and two state checks", len(p.checks))
	}
	for _, want := range []string{"setup", "state", "state"} {
		if got := <-p.checks; got != want {
			t.Fatalf("check=%q, want %q", got, want)
		}
	}
	if len(p.checks) != 0 {
		t.Fatal("unexpected check for unmatched response")
	}
}

func TestPlanCheckFailureStopsPromptAndCreatesNewCodec(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	p := &planProtocol{script: planScript, sessions: make(chan SessionInfo, 8), checks: make(chan string, 8), reject: true}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "restart"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = turn.Wait(ctx); err == nil || !strings.Contains(err.Error(), "setup rejected") {
			t.Fatalf("Wait=%v", err)
		}
	}
	if len(p.sessions) != 2 {
		t.Fatalf("codec creations=%d, want two", len(p.sessions))
	}
	first, second := <-p.sessions, <-p.sessions
	if first.ConversationID != "restart" || second.ConversationID != "restart" || first.Generation == second.Generation {
		t.Fatalf("incarnations: %+v, %+v", first, second)
	}
	if len(p.checks) != 2 {
		t.Fatalf("checks=%d, want only two setup checks", len(p.checks))
	}
}

func TestPlanRejectsTerminalBeforePrompt(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	p := &planProtocol{
		script: `while read id command; do
 case "$command" in
 setup) echo end; echo "response $id ready" ;;
 state) echo "response $id state" ;;
 prompt) echo "response $id accepted"; echo end ;;
 esac
done`, sessions: make(chan SessionInfo, 8), checks: make(chan string, 8),
	}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{ConversationID: "early"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); err == nil {
		t.Fatal("terminal from setup settled the turn successfully")
	}
}

// barrierCodec intentionally has no stateful prompt guard: removing the actor's
// barrier must expose the stale terminal, not fail an unrelated payload check.
type barrierCodec struct{}

func (*barrierCodec) Setup() []Step { return nil }
func (*barrierCodec) BeginTurn(TurnSpec) []Step {
	return []Step{
		{Command: Command{Name: "state"}, Barrier: true},
		{Command: Command{Name: "prompt"}, Prompt: true},
	}
}
func (*barrierCodec) CancelSequence() []Command { return nil }
func (*barrierCodec) Encode(id string, command Command) ([]byte, error) {
	return []byte(id + " " + command.Name + "\n"), nil
}

func (*barrierCodec) Decode(line []byte) (Decoded, error) {
	return (*planCodec)(nil).Decode(line)
}

// The old terminal is deliberately held until the next barrier command. It
// cannot be removed by draining stdout when the previous turn finishes.
func TestPlanBarrierRejectsPreviousTurnTerminal(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	p := &planProtocol{
		barrierFixture: true,
		script: `turn=0
stale=0
while read id command; do
 case "$command" in
 state)
   if [ "$turn" = 1 ]; then echo "end stale"; stale=1; fi
   echo "response $id state" ;;
 prompt)
   # Without a barrier the pending old terminal follows the new prompt.
   if [ "$turn" = 1 ] && [ "$stale" = 0 ]; then echo "end stale"; fi
   turn=$((turn + 1))
   echo "response $id accepted"
   echo "end fresh-$turn" ;;
 esac
done`, sessions: make(chan SessionInfo, 8), checks: make(chan string, 8),
	}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	for i := 1; i <= 3; i++ {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "stale"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := turn.Wait(ctx)
		if i == 2 {
			if err == nil {
				t.Fatalf("stale output settled the next turn successfully: %+v", result)
			}
			continue
		}
		if err != nil || result.Model != "fresh-1" {
			t.Fatalf("turn %d: Wait=%+v, %v", i, result, err)
		}
	}
	if len(p.sessions) != 2 {
		t.Fatalf("codec creations=%d, want replacement after stale output", len(p.sessions))
	}
	first, replacement := <-p.sessions, <-p.sessions
	if first.Generation == replacement.Generation {
		t.Fatalf("stale incarnation reused: %+v, %+v", first, replacement)
	}
}

func TestPlanSeparateConversationsHaveIndependentCodecs(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	p := &planProtocol{script: planScript, sessions: make(chan SessionInfo, 8), checks: make(chan string, 8)}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	for _, id := range []string{"one", "two", "one", "two"} {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = turn.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.sessions) != 2 {
		t.Fatalf("codec creations=%d, want two", len(p.sessions))
	}
	one, two := <-p.sessions, <-p.sessions
	if one.ConversationID != "one" || two.ConversationID != "two" {
		t.Fatalf("sessions=%+v, %+v", one, two)
	}
	if len(p.checks) != 6 {
		t.Fatalf("checks=%d, want setup once and state twice per conversation", len(p.checks))
	}
}
