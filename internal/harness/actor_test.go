package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type shellProtocol struct{}

func (shellProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", "while read x; do echo end; done"}}, nil
}

func (shellProtocol) Commands(TurnSpec) []Command { return []Command{{Name: "prompt"}} }

func (shellProtocol) Cancel() []Command { return []Command{{Name: "abort"}} }

func (shellProtocol) Encode(_ string, c Command) ([]byte, error) { return []byte(c.Name + "\n"), nil }

func (shellProtocol) Decode([]byte) (Decoded, error) {
	return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "test"}}}}, nil
}

func TestSessionTwoTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	m := NewSessionManager(Base{}, shellProtocol{})
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "one"})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := turn.Wait(ctx); err != nil || out.Model != "test" {
			t.Fatalf("%+v %v", out, err)
		}
	}
}

type startupProtocol struct {
	shellProtocol
	entered chan struct{}
}

func (p startupProtocol) Start(ctx context.Context, _ TurnSpec) (Launch, error) {
	close(p.entered)
	<-ctx.Done()
	return Launch{}, ctx.Err()
}

func TestCancelDuringStartup(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	p := startupProtocol{entered: make(chan struct{})}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	<-p.entered
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
}

type oversizedProtocol struct{ shellProtocol }

func (oversizedProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", "printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\\n'; cat >/dev/null"}}, nil
}

func TestSessionBoundsLines(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	m := NewSessionManager(Base{MaxLineBytes: 32}, oversizedProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = turn.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "longer than 32 bytes") {
		t.Fatalf("Wait=%v", err)
	}
}

type brokenProtocol struct{ shellProtocol }

func (brokenProtocol) Decode([]byte) (Decoded, error) { return Decoded{}, errors.New("invalid line") }

func TestProtocolFailureRestartsSession(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	m := NewSessionManager(Base{}, brokenProtocol{})
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "restart"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = turn.Wait(ctx)
		if err == nil || !strings.Contains(err.Error(), "invalid line") {
			t.Fatalf("Wait=%v", err)
		}
	}
}
