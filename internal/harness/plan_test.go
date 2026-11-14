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
	script   string
	sessions chan SessionInfo
	checks   chan string
	reject   bool
}

func (p *planProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", p.script}}, nil
}

func (p *planProtocol) NewCodec(info SessionInfo) Codec {
	p.sessions <- info
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
		{Command: Command{Name: "state"}, Check: func(value any) error {
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
	if string(line) == "end" {
		return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "plan"}}}}, nil
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
